// MuseFrame API (spec §10). JSON camelCase over /v1, stable error codes,
// Idempotency-Key on writes that bill, bearer sessions (guest-first).
import { createHash, randomBytes } from 'node:crypto';
import { writeFileSync, readFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import { ASSET_DIR, q, q1, run, tx, uuid, now } from './db.js';
import { grantUnits, reserveUnits, availableUnits, freeGrantWindow } from './ledger.js';
import { releaseUnits } from './ledger.js';
import { verifyGoogleIdToken, verifyAppleIdToken, verifyPlayPurchase, verifyConfig } from './verify.js';
import { enqueueJob } from './jobs.js';
import { decodeJpeg, analyzeImage, recommendStyles } from './engine/styleEngine.js';
import { generationStatus } from './engine/remoteAdapter.js';
import { cfg } from './configStore.js';
import { sendLoginCode, smtpConfigured } from './email.js';
import { registerAdminRoutes, isAdminRequest } from './admin.js';

const MAX_UPLOAD = 25 * 1024 * 1024;

export class ApiError extends Error {
  constructor(status, code, message, details) {
    super(message);
    this.status = status; this.code = code; this.details = details;
  }
}

// ---- auth ----------------------------------------------------------------

function createUser(displayName, isGuest, locale) {
  const id = uuid(), t = now();
  run('INSERT INTO users (id, is_guest, display_name, locale, created_at, updated_at) VALUES (?,?,?,?,?,?)',
    id, isGuest ? 1 : 0, displayName, locale || 'en', t, t);
  return id;
}

// Salted so the table never carries anything that maps straight back to a
// visitor's address. Rotating ADMIN_TOKEN resets the counters — acceptable.
const IP_SALT = process.env.ADMIN_TOKEN || 'museframe';
const hash24 = (v, salt = '') => createHash('sha256').update(salt + String(v)).digest('hex').slice(0, 24);

/**
 * Hand out the free image — deliberately, idempotently, and with a ceiling.
 *
 * A guest token costs nothing to mint (POST /v1/auth/exchange takes an empty
 * body), so anything keyed only on the account is not a limit at all: loop the
 * exchange and every new user id collects another free generation on the paid
 * model. The old dedupe fell back to the *user id* when no deviceId was sent,
 * which is exactly the anonymous caller's case — so the loop was wide open.
 *
 * Four gates now, cheapest first. All of them are server-observed except the
 * device hash, which is only ever used to make a grant rarer, never to permit one:
 *   1. a guest with no device fingerprint gets nothing (no fallback to user id);
 *   2. that device / identity may claim the free image exactly once, ever;
 *   3. per-IP ceiling in a rolling 24h (`free_grants_per_ip_day`);
 *   4. site-wide ceiling in a rolling 24h (`free_grants_per_day`) — the
 *      circuit breaker that still bounds spend when an attacker rotates IPs.
 *
 * Refusing only skips the grant: the account is still created and can sign in,
 * buy units, and browse. Returns a reason string for logging/telemetry.
 */
function maybeGrantFree(userId, isGuest, deviceId, clientIp) {
  const freeUnits = cfg('free_units');
  if (freeUnits <= 0) return 'FREE_UNITS_ZERO';
  if (isGuest && verifyConfig.freeRequiresAuth) return 'REQUIRES_AUTH';

  const deviceHash = deviceId ? hash24(deviceId) : null;
  // Guests must present a device fingerprint. Without one there is nothing to
  // dedupe on, and "no dedupe key" must mean "no free image", never "free image".
  if (isGuest && !deviceHash) return 'NO_DEVICE_ID';
  const dedupeId = isGuest ? deviceHash : userId;
  if (q1('SELECT id FROM free_grants WHERE dedupe_key = ? LIMIT 1', dedupeId)) return 'ALREADY_CLAIMED';
  // Pre-existing grants predate this table; keep honouring their dedupe key.
  if (q1('SELECT id FROM credit_ledger WHERE reference_key = ? LIMIT 1', `grant:free_grant:${dedupeId}`)) return 'ALREADY_CLAIMED';

  const perIp = cfg('free_grants_per_ip_day');
  const perDay = cfg('free_grants_per_day');
  if (perDay <= 0 || perIp <= 0) return 'GRANTS_DISABLED';
  // An unknown address shares one bucket rather than being exempt from the cap.
  const ipHash = hash24(clientIp || 'unknown', IP_SALT);
  const w = freeGrantWindow(ipHash);
  if (w.today >= perDay) { console.warn(`[free-grant] site cap reached (${w.today}/${perDay} in 24h) — refusing`); return 'SITE_CAP'; }
  if (w.forIp >= perIp) return 'IP_CAP';

  grantUnits(userId, freeUnits, 'free_grant', dedupeId);
  run('INSERT INTO free_grants (id, user_id, dedupe_key, device_hash, ip_hash, units, created_at) VALUES (?,?,?,?,?,?,?)',
    uuid(), userId, dedupeId, deviceHash, ipHash, freeUnits, now());
  return 'GRANTED';
}

function createSession(userId, deviceId) {
  const token = randomBytes(24).toString('base64url');
  run('INSERT INTO sessions (token, user_id, device_id, created_at, last_seen_at) VALUES (?,?,?,?,?)',
    token, userId, deviceId || null, now(), now());
  return token;
}

export function authenticate(req, url) {
  const header = req.headers.authorization || '';
  const token = header.startsWith('Bearer ') ? header.slice(7) : url.searchParams.get('token');
  if (!token) return null;
  const s = q1('SELECT user_id FROM sessions WHERE token = ?', token);
  if (!s) return null;
  return q1('SELECT * FROM users WHERE id = ? AND deleted_at IS NULL', s.user_id) || null;
}

function requireUser(ctx) {
  if (!ctx.user) throw new ApiError(401, 'AUTH_REQUIRED', 'Sign in to continue.');
  return ctx.user;
}

// ---- style catalog helpers -------------------------------------------------

function publishedStyleRows() {
  return q(`
    SELECT s.id AS styleId, s.internal_key, s.premium, s.public_name, s.short_caption, s.suitability_tags, s.theme,
           v.id AS versionId, v.version, v.spec,
           e.editorial_rank AS editorialRank
    FROM styles s
    JOIN style_versions v ON v.style_id = s.id AND v.status = 'published'
      AND v.version = (SELECT MAX(version) FROM style_versions WHERE style_id = s.id AND status = 'published')
    JOIN exhibition_styles es ON es.style_id = s.id
    JOIN exhibitions e ON e.id = es.exhibition_id
    WHERE s.status = 'published'
    ORDER BY e.editorial_rank, es.position`);
}

const WEB_DIR = path.join(path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1')), '..', 'web');
function styleCard(row, plan) {
  const spec = JSON.parse(row.spec);
  const coverFile = path.join(WEB_DIR, 'covers', `${row.internal_key}.jpg`);
  return {
    styleId: row.styleId,
    styleVersionId: row.versionId,
    name: row.public_name,
    shortCaption: row.short_caption,
    suitabilityTags: JSON.parse(row.suitability_tags),
    premium: !!row.premium,
    lockedForUser: !!row.premium && plan === 'free',
    coverArt: spec.coverArt,
    coverUrl: existsSync(coverFile) ? `/covers/${row.internal_key}.jpg` : null,
    estimatedTimeLabel: '20–45 s',
    controls: spec.controls,
    compatibility: spec.compatibility.subjects,
  };
}

function userPlan(userId) {
  const sub = q1(`
    SELECT p.internal_key FROM purchases pu
    JOIN products p ON p.id = pu.product_id
    WHERE pu.user_id = ? AND p.product_type = 'subscription' AND pu.status = 'verified'
      AND (pu.expires_at IS NULL OR pu.expires_at > ?)
    ORDER BY pu.purchased_at DESC LIMIT 1`, userId, now());
  return sub ? sub.internal_key : 'free';
}

function entitlements(userId) {
  const plan = userPlan(userId);
  const isCreator = plan.startsWith('creator');
  const savedFree = q1(`SELECT COUNT(*) AS n FROM credit_ledger WHERE user_id = ? AND entry_type = 'commit'`, userId).n;
  return {
    plan,
    availableUnits: availableUnits(userId),
    freeCompletedImagesRemaining: plan === 'free' ? Math.max(0, cfg('free_units') - savedFree) : 0,
    features: { premiumStyles: isCreator, priorityQueue: isCreator, highResolution: isCreator },
  };
}

// ---- analysis --------------------------------------------------------------

function runAnalysis(assetId) {
  const asset = q1('SELECT * FROM assets WHERE id = ?', assetId);
  if (!asset) return;
  const t = now();
  try {
    const img = decodeJpeg(readFileSync(path.join(ASSET_DIR, asset.storage_key)));
    const a = analyzeImage(img);
    const recs = recommendStyles(a, publishedStyleRows().map(r => ({
      styleId: r.styleId, versionId: r.versionId, editorialRank: r.editorialRank, spec: JSON.parse(r.spec),
    }))).slice(0, 4);
    run(`UPDATE photo_analyses SET status='ready', subject_type=?, person_count=?, sharpness=?, exposure=?, warnings=?, recommendations=?, updated_at=?
         WHERE asset_id = ?`,
      a.subjectType, a.personCount, a.sharpness, a.exposure, JSON.stringify(a.warnings), JSON.stringify(recs), t, assetId);
  } catch (e) {
    console.error('[analysis]', assetId, e.message);
    run(`UPDATE photo_analyses SET status='failed', updated_at=? WHERE asset_id = ?`, t, assetId);
  }
}

// ---- route table -----------------------------------------------------------
// Each handler: (ctx) => body. ctx = {req, res, url, user, params, body, raw}

export const routes = [];
const route = (method, pattern, handler) => routes.push({ method, pattern: new RegExp(`^${pattern}$`), handler });

route('POST', '/v1/auth/exchange', async (ctx) => {
  const { provider, deviceId, locale, displayName, identityToken } = ctx.body || {};
  let userId, grantOutcome = null;

  if (provider === 'guest' || !provider) {
    if (!verifyConfig.allowGuest) throw new ApiError(403, 'AUTH_REQUIRED', 'Sign in to continue.');
    userId = createUser(displayName || null, true, locale);
    grantOutcome = maybeGrantFree(userId, true, deviceId, ctx.clientIp);
  } else if (provider === 'google' || provider === 'apple' || provider === 'dev') {
    // Cryptographically verify the ID token with the issuer's public keys.
    // A forged or replayed token cannot pass — identity is the provider's `sub`.
    let claims, storedProvider = provider;
    if (provider === 'dev') {
      // DEV-ONLY test login (ALLOW_TEST_LOGIN=true, off in production). Exercises
      // the email-capture / guest-merge / admin-display path without live OAuth.
      // Flag AND operator token. A flag left on in production would otherwise let
      // anyone mint a "signed-in" account with any email — which also walks
      // straight past free_requires_auth.
      if (process.env.ALLOW_TEST_LOGIN !== 'true' || !isAdminRequest(ctx.req, ctx.url)) {
        throw new ApiError(403, 'AUTH_INVALID', 'Test login disabled.');
      }
      const email = String(ctx.body.email || '').trim().toLowerCase();
      if (!email) throw new ApiError(422, 'VALIDATION', 'email required for test login.');
      claims = { subject: 'dev:' + createHash('sha256').update(email).digest('hex').slice(0, 16), email, name: ctx.body.displayName || email.split('@')[0] };
      storedProvider = 'google'; // record under a real provider row so it lists normally
    } else {
      try {
        claims = provider === 'google' ? await verifyGoogleIdToken(identityToken) : await verifyAppleIdToken(identityToken);
      } catch (e) {
        if (e.code === 'PROVIDER_NOT_CONFIGURED') throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', `${provider} sign-in is not configured on the server.`);
        throw new ApiError(401, 'AUTH_INVALID', 'Sign-in could not be verified.');
      }
    }
    const subject = claims.subject;
    const existing = q1('SELECT user_id FROM auth_identities WHERE provider = ? AND provider_subject = ?', storedProvider, subject);
    if (existing) {
      userId = existing.user_id;
    } else {
      // Merge an in-flight guest session into the new real account (spec §10.2):
      // its projects and any purchased units follow the user, never a free grant.
      const guest = ctx.user && ctx.user.is_guest ? ctx.user : null;
      if (guest) {
        userId = guest.id;
        run('UPDATE users SET is_guest = 0, display_name = ?, updated_at = ? WHERE id = ?', displayName || claims.name || claims.email || null, now(), userId);
      } else {
        userId = createUser(displayName || claims.name || claims.email || null, false, locale);
      }
      run('INSERT INTO auth_identities (id, user_id, provider, provider_subject, email_normalized, created_at) VALUES (?,?,?,?,?,?)',
        uuid(), userId, storedProvider, subject, claims.email || null, now());
    }
    grantOutcome = maybeGrantFree(userId, false, deviceId, ctx.clientIp);
  } else {
    throw new ApiError(422, 'VALIDATION', 'Unknown provider.');
  }
  if (grantOutcome && grantOutcome !== 'GRANTED') console.log(`[free-grant] not granted: ${grantOutcome}`);

  const token = createSession(userId, deviceId);
  const user = q1('SELECT id, is_guest, display_name FROM users WHERE id = ?', userId);
  return { accessToken: token, user: { id: user.id, isGuest: !!user.is_guest, displayName: user.display_name } };
});

// Resolve a verified provider identity to a user id, merging an in-flight guest
// account (its projects + purchased units) into it. Shared by OAuth and email.
function resolveIdentity({ provider, subject, email, name, ctxUser, locale, deviceId, clientIp }) {
  const existing = q1('SELECT user_id FROM auth_identities WHERE provider = ? AND provider_subject = ?', provider, subject);
  let userId;
  if (existing) {
    userId = existing.user_id;
    if (email) run('UPDATE auth_identities SET email_normalized = ? WHERE provider = ? AND provider_subject = ?', email, provider, subject);
  } else {
    const guest = ctxUser && ctxUser.is_guest ? ctxUser : null;
    if (guest) {
      userId = guest.id;
      run('UPDATE users SET is_guest = 0, display_name = ?, updated_at = ? WHERE id = ?', name || email || null, now(), userId);
    } else {
      userId = createUser(name || email || null, false, locale);
    }
    run('INSERT INTO auth_identities (id, user_id, provider, provider_subject, email_normalized, created_at) VALUES (?,?,?,?,?,?)',
      uuid(), userId, provider, subject, email || null, now());
  }
  const outcome = maybeGrantFree(userId, false, deviceId, clientIp);
  if (outcome !== 'GRANTED') console.log(`[free-grant] not granted: ${outcome}`);
  return userId;
}

// ---- email verification-code login ----------------------------------------
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

route('POST', '/v1/auth/email/request', async (ctx) => {
  if (!cfg('email_login_enabled')) throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', '邮箱登录未开启。');
  const email = String(ctx.body?.email || '').trim().toLowerCase();
  if (!EMAIL_RE.test(email)) throw new ApiError(422, 'VALIDATION', '请输入有效的邮箱地址。');
  // 6-digit code, hashed at rest; one active code per email.
  const code = String(Math.floor(100000 + Math.random() * 900000));
  const codeHash = createHash('sha256').update(email + ':' + code).digest('hex');
  const t = now();
  run(`INSERT INTO email_codes (email, code_hash, expires_at, attempts, created_at) VALUES (?,?,?,0,?)
       ON CONFLICT(email) DO UPDATE SET code_hash=excluded.code_hash, expires_at=excluded.expires_at, attempts=0, created_at=excluded.created_at`,
    email, codeHash, new Date(Date.now() + 10 * 60000).toISOString(), t);
  const testMode = process.env.ALLOW_TEST_LOGIN === 'true';
  try {
    await sendLoginCode(email, code);
  } catch (e) {
    if (e.code === 'SMTP_NOT_CONFIGURED') throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', '邮件服务未配置。');
    console.error('[email] send failed:', e.message);
    // In test mode a delivery failure (e.g. unauthorized sending IP) must not
    // block flow verification — the code is still returned below.
    if (!testMode) throw new ApiError(502, 'EMAIL_SEND_FAILED', '验证码发送失败，请稍后重试。');
  }
  const body = { ok: true, expiresInSeconds: 600 };
  if (testMode) body.devCode = code;
  return body;
});

route('POST', '/v1/auth/email/verify', (ctx) => {
  if (!cfg('email_login_enabled')) throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', '邮箱登录未开启。');
  const email = String(ctx.body?.email || '').trim().toLowerCase();
  const code = String(ctx.body?.code || '').trim();
  const deviceId = ctx.body?.deviceId;
  const rec = q1('SELECT * FROM email_codes WHERE email = ?', email);
  if (!rec) throw new ApiError(422, 'CODE_INVALID', '请先获取验证码。');
  if (new Date(rec.expires_at) < new Date()) throw new ApiError(422, 'CODE_EXPIRED', '验证码已过期，请重新获取。');
  if (rec.attempts >= 5) throw new ApiError(429, 'CODE_LOCKED', '尝试次数过多，请重新获取验证码。');
  const codeHash = createHash('sha256').update(email + ':' + code).digest('hex');
  if (codeHash !== rec.code_hash) {
    run('UPDATE email_codes SET attempts = attempts + 1 WHERE email = ?', email);
    throw new ApiError(422, 'CODE_INVALID', '验证码不正确。');
  }
  run('DELETE FROM email_codes WHERE email = ?', email); // single-use
  const userId = resolveIdentity({
    provider: 'email', subject: 'email:' + email, email, name: email.split('@')[0],
    ctxUser: ctx.user, locale: ctx.body?.locale, deviceId, clientIp: ctx.clientIp,
  });
  const token = createSession(userId, deviceId);
  const user = q1('SELECT id, is_guest, display_name FROM users WHERE id = ?', userId);
  return { accessToken: token, user: { id: user.id, isGuest: !!user.is_guest, displayName: user.display_name, email } };
});

// What sign-in / billing options this deployment supports. The packaged app
// reads this at boot, so enabling providers later is a server-side .env change
// — no app update required.
// Whether this deployment can actually make an image, plus how long it takes.
// `available:false` lets the client say so up front instead of letting someone
// spend a unit on a job the worker is going to refuse.
function generationInfo() {
  const gen = generationStatus();
  return {
    available: gen.available,
    unavailableReason: gen.available ? null : gen.reason,
    estimatedRangeSeconds: gen.mode === 'remote' ? [60, 300] : [5, 30],
  };
}

route('GET', '/v1/auth/config', (ctx) => ({
  guestAllowed: verifyConfig.allowGuest,
  generation: generationInfo(),
  freeRequiresAuth: verifyConfig.freeRequiresAuth,
  google: {
    enabled: verifyConfig.googleSignIn,
    webClientId: (process.env.GOOGLE_WEB_CLIENT_ID || (process.env.GOOGLE_CLIENT_IDS || '').split(',')[0] || '').trim() || null,
  },
  apple: { enabled: verifyConfig.appleSignIn },
  email: { enabled: !!cfg('email_login_enabled') && smtpConfigured() },
  billing: {
    google: verifyConfig.playBilling,
    apple: false, // App Store Server API verification not yet configured
    // Only advertised to the operator — the app hides the demo-buy path for
    // everyone else and says purchases happen in the store app.
    mock: verifyConfig.allowMockPurchases && isAdminRequest(ctx.req, ctx.url),
  },
}));

route('GET', '/v1/discover', (ctx) => {
  const plan = ctx.user ? userPlan(ctx.user.id) : 'free';
  const exhibitions = q(`SELECT * FROM exhibitions WHERE status = 'published' ORDER BY editorial_rank`);
  const rows = publishedStyleRows();
  const byExh = {};
  for (const e of exhibitions) byExh[e.id] = [];
  for (const r of rows) {
    const es = q1('SELECT exhibition_id FROM exhibition_styles WHERE style_id = ?', r.styleId);
    if (es && byExh[es.exhibition_id]) byExh[es.exhibition_id].push(styleCard(r, plan));
  }
  const shelves = exhibitions.map(e => ({
    id: e.id, slug: e.slug, title: e.title, curatorialNote: e.curatorial_note,
    edition: e.edition, styles: byExh[e.id],
  }));
  return {
    edition: '2026-W33', heroExhibition: shelves[0], shelves: shelves.slice(1), configVersion: 1,
    generation: generationInfo(),
  };
});

route('GET', '/v1/styles', (ctx) => {
  const plan = ctx.user ? userPlan(ctx.user.id) : 'free';
  return { styles: publishedStyleRows().map(r => styleCard(r, plan)) };
});

route('GET', '/v1/styles/([\\w-]+)', (ctx) => {
  const plan = ctx.user ? userPlan(ctx.user.id) : 'free';
  const row = publishedStyleRows().find(r => r.styleId === ctx.params[0]);
  if (!row) throw new ApiError(404, 'STYLE_UNAVAILABLE', 'This direction is not available.');
  const spec = JSON.parse(row.spec);
  return {
    ...styleCard(row, plan),
    theme: row.theme,
    intentSummary: spec.intent.summary,
    worksBestWith: JSON.parse(row.suitability_tags),
    version: row.version,
  };
});

// Upload flow: intent → binary PUT → complete (spec §10.4).
route('POST', '/v1/assets/upload-intents', (ctx) => {
  const user = requireUser(ctx);
  const { projectId, contentType, byteSize } = ctx.body || {};
  if (!['image/jpeg'].includes(contentType)) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Upload a JPEG (the app converts for you).');
  if (byteSize > MAX_UPLOAD) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Images up to 20 MB are supported.');
  const assetId = uuid(), t = now();
  run(`INSERT INTO assets (id, user_id, project_id, kind, status, storage_key, content_type, byte_size, created_at, updated_at)
       VALUES (?,?,?,?,?,?,?,?,?,?)`,
    assetId, user.id, projectId || null, 'source', 'pending', `${assetId}.jpg`, contentType, byteSize || null, t, t);
  return { assetId, uploadUrl: `/v1/assets/${assetId}/upload`, expiresAt: new Date(Date.now() + 15 * 60000).toISOString() };
});

route('PUT', '/v1/assets/([\\w-]+)/upload', (ctx) => {
  const user = requireUser(ctx);
  const asset = q1('SELECT * FROM assets WHERE id = ? AND user_id = ?', ctx.params[0], user.id);
  if (!asset) throw new ApiError(404, 'ASSET_NOT_READY', 'Unknown asset.');
  if (!ctx.raw || ctx.raw.length < 100) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Empty upload.');
  if (ctx.raw.length > MAX_UPLOAD) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Images up to 20 MB are supported.');
  // MIME magic bytes: JPEG must start FF D8 (spec §14.2 — never trust extension).
  if (!(ctx.raw[0] === 0xFF && ctx.raw[1] === 0xD8)) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Not a JPEG image.');
  writeFileSync(path.join(ASSET_DIR, asset.storage_key), ctx.raw);
  run('UPDATE assets SET byte_size = ?, updated_at = ? WHERE id = ?', ctx.raw.length, now(), asset.id);
  return { ok: true };
});

route('POST', '/v1/assets/([\\w-]+)/complete', (ctx) => {
  const user = requireUser(ctx);
  const asset = q1('SELECT * FROM assets WHERE id = ? AND user_id = ?', ctx.params[0], user.id);
  if (!asset) throw new ApiError(404, 'ASSET_NOT_READY', 'Unknown asset.');
  const file = path.join(ASSET_DIR, asset.storage_key);
  if (!existsSync(file)) throw new ApiError(409, 'ASSET_NOT_READY', 'Upload has not arrived yet.');
  if (asset.status === 'ready') return { assetId: asset.id, status: 'ready' }; // idempotent complete
  const img = decodeJpeg(readFileSync(file)); // validates decodability + dimensions
  const t = now();
  run('UPDATE assets SET status = ?, width = ?, height = ?, updated_at = ? WHERE id = ?', 'ready', img.width, img.height, t, asset.id);
  run(`INSERT OR IGNORE INTO photo_analyses (id, asset_id, analyzer_version, status, created_at, updated_at)
       VALUES (?,?,?,?,?,?)`, uuid(), asset.id, 'heuristic-0.1', 'pending', t, t);
  setImmediate(() => runAnalysis(asset.id));
  return { assetId: asset.id, status: 'ready', width: img.width, height: img.height };
});

route('GET', '/v1/assets/([\\w-]+)/analysis', (ctx) => {
  const user = requireUser(ctx);
  const a = q1(`SELECT pa.* FROM photo_analyses pa JOIN assets s ON s.id = pa.asset_id
                WHERE pa.asset_id = ? AND s.user_id = ?`, ctx.params[0], user.id);
  if (!a) throw new ApiError(404, 'ASSET_NOT_READY', 'No analysis for this asset.');
  return {
    status: a.status,
    subjectType: a.subject_type,
    personCount: a.person_count,
    quality: { sharpness: a.sharpness, exposure: a.exposure, minimumQualityMet: a.status === 'ready' && !JSON.parse(a.warnings).includes('LOW_RESOLUTION') },
    recommendations: JSON.parse(a.recommendations),
    warnings: JSON.parse(a.warnings),
  };
});

// Image bytes (session-checked stand-in for short-lived signed URLs).
route('GET', '/v1/assets/([\\w-]+)/file', (ctx) => {
  const user = requireUser(ctx);
  const asset = q1('SELECT * FROM assets WHERE id = ? AND user_id = ? AND deleted_at IS NULL', ctx.params[0], user.id);
  if (!asset) throw new ApiError(404, 'ASSET_NOT_READY', 'Unknown asset.');
  const file = path.join(ASSET_DIR, asset.storage_key);
  if (!existsSync(file)) throw new ApiError(404, 'ASSET_NOT_READY', 'File missing.');
  ctx.res.writeHead(200, { 'Content-Type': asset.content_type, 'Cache-Control': 'private, max-age=3600' });
  ctx.res.end(readFileSync(file));
  return null; // handled
});

route('POST', '/v1/projects', (ctx) => {
  const user = requireUser(ctx);
  const id = uuid(), t = now();
  run('INSERT INTO projects (id, user_id, title, source_asset_id, created_at, updated_at) VALUES (?,?,?,?,?,?)',
    id, user.id, ctx.body?.title || null, ctx.body?.sourceAssetId || null, t, t);
  if (ctx.body?.sourceAssetId) run('UPDATE assets SET project_id = ? WHERE id = ? AND user_id = ?', id, ctx.body.sourceAssetId, user.id);
  return { id, title: ctx.body?.title || null, status: 'draft', createdAt: t };
});

route('GET', '/v1/projects', (ctx) => {
  const user = requireUser(ctx);
  const rows = q(`SELECT * FROM projects WHERE user_id = ? AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 100`, user.id);
  return {
    projects: rows.map(p => {
      const job = q1('SELECT * FROM generation_jobs WHERE project_id = ? ORDER BY created_at DESC LIMIT 1', p.id);
      const cand = p.selected_candidate_id ? q1('SELECT * FROM generation_candidates WHERE id = ?', p.selected_candidate_id) : null;
      const sv = job ? q1('SELECT s.public_name FROM style_versions v JOIN styles s ON s.id = v.style_id WHERE v.id = ?', job.style_version_id) : null;
      return {
        id: p.id, title: p.title, status: p.status, updatedAt: p.updated_at,
        styleName: sv?.public_name || null,
        jobStatus: job?.status || null,
        jobId: job?.id || null,
        sourceAssetId: p.source_asset_id,
        candidateAssetId: cand?.asset_id || null,
      };
    }),
  };
});

route('GET', '/v1/projects/([\\w-]+)', (ctx) => {
  const user = requireUser(ctx);
  const p = q1('SELECT * FROM projects WHERE id = ? AND user_id = ? AND deleted_at IS NULL', ctx.params[0], user.id);
  if (!p) throw new ApiError(404, 'NOT_FOUND', 'Project not found.');
  const jobs = q('SELECT * FROM generation_jobs WHERE project_id = ? ORDER BY created_at DESC', p.id);
  return {
    id: p.id, title: p.title, status: p.status, sourceAssetId: p.source_asset_id,
    selectedCandidateId: p.selected_candidate_id,
    jobs: jobs.map(j => ({ id: j.id, status: j.status, stage: j.stage, styleVersionId: j.style_version_id, errorCode: j.error_code, createdAt: j.created_at })),
  };
});

route('PATCH', '/v1/projects/([\\w-]+)', (ctx) => {
  const user = requireUser(ctx);
  const p = q1('SELECT * FROM projects WHERE id = ? AND user_id = ? AND deleted_at IS NULL', ctx.params[0], user.id);
  if (!p) throw new ApiError(404, 'NOT_FOUND', 'Project not found.');
  if (ctx.body?.title !== undefined) run('UPDATE projects SET title = ?, updated_at = ? WHERE id = ?', ctx.body.title, now(), p.id);
  return { ok: true };
});

route('DELETE', '/v1/projects/([\\w-]+)', (ctx) => {
  const user = requireUser(ctx);
  run('UPDATE projects SET deleted_at = ?, updated_at = ? WHERE id = ? AND user_id = ?', now(), now(), ctx.params[0], user.id);
  return { ok: true };
});

// Generation jobs (spec §10.7). Idempotency-Key required; billing via ledger.
route('POST', '/v1/generation-jobs', (ctx) => {
  const user = requireUser(ctx);
  const idemKey = ctx.req.headers['idempotency-key'];
  if (!idemKey) throw new ApiError(400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key header is required.');
  const reqHash = createHash('sha256').update(JSON.stringify(ctx.body || {})).digest('hex');
  const prior = q1('SELECT * FROM idempotency_records WHERE user_id = ? AND idempotency_key = ?', user.id, idemKey);
  if (prior) {
    if (prior.request_hash !== reqHash) throw new ApiError(409, 'IDEMPOTENCY_CONFLICT', 'Key was used with a different request.');
    return JSON.parse(prior.response_body);
  }

  // No configured image provider ⇒ refuse before anything is written or billed.
  // (The worker re-checks; this is the one that keeps units untouched.)
  const gen = generationStatus();
  if (!gen.available) {
    throw new ApiError(503, 'GENERATION_UNAVAILABLE', 'Image generation is unavailable right now.');
  }

  const { projectId, sourceAssetId, styleVersionId, controls = {}, output = {}, parentJobId } = ctx.body || {};
  const project = q1('SELECT * FROM projects WHERE id = ? AND user_id = ? AND deleted_at IS NULL', projectId, user.id);
  if (!project) throw new ApiError(404, 'NOT_FOUND', 'Project not found.');
  const asset = q1('SELECT * FROM assets WHERE id = ? AND user_id = ? AND status = ?', sourceAssetId, user.id, 'ready');
  if (!asset) throw new ApiError(409, 'ASSET_NOT_READY', 'Source photo is not ready.');
  const sv = q1(`SELECT v.*, s.premium FROM style_versions v JOIN styles s ON s.id = v.style_id WHERE v.id = ? AND v.status = 'published'`, styleVersionId);
  if (!sv) throw new ApiError(404, 'STYLE_UNAVAILABLE', 'This direction is not available.');

  const plan = userPlan(user.id);
  if (sv.premium && plan === 'free') {
    throw new ApiError(402, 'INSUFFICIENT_ENTITLEMENT', 'A Creator plan is required for this direction.', { premiumStyle: true });
  }

  const units = 1;
  // Cheap pre-check so a paywall bounce doesn't litter the project with failed
  // rows; reserveUnits below remains the authoritative, race-safe gate.
  if (availableUnits(user.id) < units) {
    throw new ApiError(402, 'INSUFFICIENT_ENTITLEMENT', 'A standard image is required.',
      { requiredUnits: units, availableUnits: availableUnits(user.id) });
  }
  const jobId = uuid(), t = now();
  const response = tx(() => {
    run(`INSERT INTO generation_jobs (id, user_id, project_id, source_asset_id, style_version_id, parent_job_id, status, stage, controls, output, reserved_units, created_at, updated_at)
         VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
      jobId, user.id, projectId, sourceAssetId, styleVersionId, parentJobId || null, 'created', 'preparing',
      JSON.stringify({ strength: controls.strength || 'balanced', fidelity: controls.fidelity || 'high', composition: controls.composition || 'keep' }),
      JSON.stringify({ aspectRatio: output.aspectRatio || 'original', qualityTier: output.qualityTier || 'standard' }),
      units, t, t);
    return null;
  });
  try {
    reserveUnits(user.id, jobId, units);
  } catch (e) {
    run('UPDATE generation_jobs SET status = ?, error_code = ?, updated_at = ? WHERE id = ?', 'failed', e.code || 'INTERNAL_ERROR', now(), jobId);
    if (e.code === 'INSUFFICIENT_ENTITLEMENT') throw new ApiError(402, e.code, e.message, e.details);
    throw e;
  }
  run('UPDATE generation_jobs SET status = ?, updated_at = ? WHERE id = ?', 'queued', now(), jobId);
  run('UPDATE projects SET status = ?, updated_at = ? WHERE id = ?', 'generating', now(), projectId);
  enqueueJob(jobId);

  const body = {
    job: { id: jobId, status: 'queued', stage: 'preparing', estimatedRangeSeconds: generationInfo().estimatedRangeSeconds, reservedUnits: units, createdAt: t },
    entitlementSnapshot: { availableUnits: availableUnits(user.id), plan },
  };
  run(`INSERT INTO idempotency_records (user_id, idempotency_key, request_hash, response_status, response_body, created_at)
       VALUES (?,?,?,?,?,?)`, user.id, idemKey, reqHash, 200, JSON.stringify(body), t);
  return body;
});

route('GET', '/v1/generation-jobs/([\\w-]+)', (ctx) => {
  const user = requireUser(ctx);
  const j = q1('SELECT * FROM generation_jobs WHERE id = ? AND user_id = ?', ctx.params[0], user.id);
  if (!j) throw new ApiError(404, 'NOT_FOUND', 'Job not found.');
  const cand = q1('SELECT * FROM generation_candidates WHERE job_id = ? ORDER BY candidate_index LIMIT 1', j.id);
  const candAsset = cand ? q1('SELECT * FROM assets WHERE id = ?', cand.asset_id) : null;
  return {
    id: j.id, status: j.status, stage: j.stage, attemptCount: j.attempt_count,
    projectId: j.project_id, styleVersionId: j.style_version_id, controls: JSON.parse(j.controls), output: JSON.parse(j.output),
    candidate: cand ? {
      id: cand.id,
      assetId: cand.asset_id,
      previewUrl: `/v1/assets/${cand.asset_id}/file`,
      downloadUrl: `/v1/assets/${cand.asset_id}/file`,
      width: candAsset?.width, height: candAsset?.height,
    } : null,
    billing: { unitsCommitted: j.status === 'succeeded' ? j.reserved_units : 0 },
    error: j.error_code ? { code: j.error_code } : null,
  };
});

route('POST', '/v1/generation-jobs/([\\w-]+)/cancel', (ctx) => {
  const user = requireUser(ctx);
  const j = q1('SELECT * FROM generation_jobs WHERE id = ? AND user_id = ?', ctx.params[0], user.id);
  if (!j) throw new ApiError(404, 'NOT_FOUND', 'Job not found.');
  if (['queued', 'created'].includes(j.status)) {
    releaseUnits(user.id, j.id);
    run('UPDATE generation_jobs SET status = ?, updated_at = ? WHERE id = ?', 'cancelled', now(), j.id);
    return { id: j.id, status: 'cancelled' };
  }
  return { id: j.id, status: j.status }; // best effort — running jobs finish
});

route('POST', '/v1/candidates/([\\w-]+)/feedback', (ctx) => {
  const user = requireUser(ctx);
  const { rating, reasonCodes = [], comment } = ctx.body || {};
  if (!['positive', 'negative'].includes(rating)) throw new ApiError(422, 'VALIDATION', 'rating must be positive|negative');
  run('INSERT INTO user_feedback (id, user_id, candidate_id, rating, reason_codes, comment, created_at) VALUES (?,?,?,?,?,?,?)',
    uuid(), user.id, ctx.params[0], rating, JSON.stringify(reasonCodes), comment || null, now());
  return { ok: true };
});

route('POST', '/v1/candidates/([\\w-]+)/export', (ctx) => {
  const user = requireUser(ctx);
  const cand = q1(`SELECT c.*, j.user_id FROM generation_candidates c JOIN generation_jobs j ON j.id = c.job_id WHERE c.id = ?`, ctx.params[0]);
  if (!cand || cand.user_id !== user.id) throw new ApiError(404, 'NOT_FOUND', 'Candidate not found.');
  run(`INSERT INTO events (id, user_id, name, props, occurred_at) VALUES (?,?,?,?,?)`,
    uuid(), user.id, 'result_saved', JSON.stringify({ candidateId: cand.id }), now());
  const proj = q1('SELECT project_id FROM generation_jobs WHERE id = ?', cand.job_id);
  run('UPDATE projects SET status = ?, updated_at = ? WHERE id = ?', 'saved', now(), proj.project_id);
  return { downloadUrl: `/v1/assets/${cand.asset_id}/file`, format: 'jpeg', qualityTier: 'standard' };
});

route('GET', '/v1/entitlements/me', (ctx) => entitlements(requireUser(ctx).id));

route('GET', '/v1/products', () => ({
  products: q('SELECT * FROM products WHERE active = 1').map(p => ({
    internalKey: p.internal_key, productType: p.product_type, displayName: p.display_name,
    grantedUnits: p.granted_units, priceMinor: p.price_minor, currency: p.currency, period: p.period,
    googleProductId: p.google_product_id || p.internal_key,
    appleProductId: p.apple_product_id || p.internal_key,
  })),
}));

// Purchase verification (spec §10.10). Entitlements are granted ONLY after the
// platform's own signed record confirms the purchase — the client's claim of
// "I bought Creator" is never sufficient. Server is the source of truth.
route('POST', '/v1/purchases/verify', async (ctx) => {
  const user = requireUser(ctx);
  const { productKey, purchaseToken, transactionId } = ctx.body || {};
  const platform = ctx.body?.platform || 'web';
  const product = q1('SELECT * FROM products WHERE internal_key = ? AND active = 1', productKey);
  if (!product) throw new ApiError(404, 'NOT_FOUND', 'Unknown product.');

  // The Play/App Store product ID actually purchased (falls back to internalKey).
  const storeProductId = (platform === 'apple' ? product.apple_product_id : product.google_product_id) || product.internal_key;

  let verified = false, expiresAt = null, externalTxId = transactionId || purchaseToken;
  if (platform === 'google') {
    if (!verifyConfig.playBilling) throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', 'Google Play verification is not configured on the server.');
    if (!purchaseToken) throw new ApiError(422, 'VALIDATION', 'purchaseToken is required.');
    let r;
    try {
      r = await verifyPlayPurchase({ productId: storeProductId, purchaseToken, kind: product.product_type === 'subscription' ? 'subscription' : 'product' });
    } catch (e) {
      if (e.code === 'PROVIDER_NOT_CONFIGURED') throw new ApiError(501, e.code, e.message);
      throw new ApiError(402, 'PURCHASE_INVALID', 'This purchase could not be verified.');
    }
    if (!r.valid) throw new ApiError(402, 'PURCHASE_INVALID', 'This purchase is not active.');
    verified = true; expiresAt = r.expiresAt;
    externalTxId = purchaseToken; // Play purchase tokens are globally unique
  } else if (platform === 'apple') {
    // App Store Server API verification plugs in here (needs the .p8 signing key).
    throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', 'App Store verification is not configured on the server.');
  } else if (verifyConfig.allowMockPurchases && isAdminRequest(ctx.req, ctx.url)) {
    // DEV ONLY. Requires the flag AND the operator's admin token: on its own the
    // flag is an open faucet — any caller could "buy" a pack and be granted
    // paid units for nothing, which is a bigger hole than the free-unit loop.
    verified = true;
    externalTxId = transactionId || `mock_${uuid()}`;
    expiresAt = product.period ? new Date(Date.now() + (product.period === 'month' ? 30 : 365) * 86400000).toISOString() : null;
  } else {
    throw new ApiError(422, 'VALIDATION', 'Unsupported platform for verification.');
  }
  if (!verified) throw new ApiError(402, 'PURCHASE_INVALID', 'Purchase not verified.');

  // Idempotent on the external transaction — replays never double-grant.
  const existing = q1('SELECT * FROM purchases WHERE platform = ? AND external_transaction_id = ?', platform, externalTxId);
  if (existing) return { purchaseId: existing.id, status: existing.status, entitlements: entitlements(user.id) };

  const purchaseId = uuid(), t = now();
  const expires = expiresAt || (product.period ? new Date(Date.now() + (product.period === 'month' ? 30 : 365) * 86400000).toISOString() : null);
  tx(() => {
    run(`INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id, status, amount_minor, currency, purchased_at, expires_at, created_at)
         VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
      purchaseId, user.id, product.id, platform, externalTxId, 'verified', product.price_minor, product.currency, t, expires, t);
  });
  const unitExpiry = product.product_type === 'pack' ? new Date(Date.now() + 90 * 86400000).toISOString() : expires;
  grantUnits(user.id, product.granted_units, 'purchase', purchaseId, unitExpiry);
  return { purchaseId, status: 'verified', entitlements: entitlements(user.id) };
});

route('GET', '/v1/purchases', (ctx) => {
  const user = requireUser(ctx);
  return {
    purchases: q(`SELECT pu.*, p.display_name FROM purchases pu JOIN products p ON p.id = pu.product_id
                  WHERE pu.user_id = ? ORDER BY pu.purchased_at DESC`, user.id)
      .map(p => ({ id: p.id, product: p.display_name, amountMinor: p.amount_minor, currency: p.currency, purchasedAt: p.purchased_at, expiresAt: p.expires_at })),
  };
});

route('POST', '/v1/events', (ctx) => {
  const events = Array.isArray(ctx.body?.events) ? ctx.body.events.slice(0, 50) : [];
  for (const e of events) {
    if (!e?.name) continue;
    run('INSERT INTO events (id, user_id, name, props, occurred_at) VALUES (?,?,?,?,?)',
      uuid(), ctx.user?.id || null, String(e.name).slice(0, 64), JSON.stringify(e.props || {}), now());
  }
  return { accepted: events.length };
});

route('GET', '/v1/health', () => ({ ok: true, service: 'museframe-api', time: now() }));

// ---- Admin back office (spec §19) --------------------------------------
// Route definitions live in admin.js; registered here so they share this
// module's `routes` array. `route` and `ApiError` are passed in explicitly
// (rather than imported by admin.js) so there is no import cycle between the
// two modules.
registerAdminRoutes(route, { ApiError });
