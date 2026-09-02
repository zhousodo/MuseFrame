// MuseFrame API (spec §10). JSON camelCase over /v1, stable error codes,
// Idempotency-Key on writes that bill, bearer sessions (guest-first).
import { createHash, createHmac, randomBytes, randomInt, timingSafeEqual } from 'node:crypto';
import { writeFileSync, readFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import { db, ASSET_DIR, q, q1, run, tx, uuid, now, serverSecret } from './db.js';
import { grantUnits, reserveUnits, availableUnits, freeGrantWindow } from './ledger.js';
import { releaseUnits } from './ledger.js';
import { verifyGoogleIdToken, verifyAppleIdToken, verifyPlayPurchase, acknowledgePlayPurchase, assertPlayToken, verifyConfig } from './verify.js';
import { enqueueJob, queueDepth } from './jobs.js';
import { decodeJpeg, analyzeImage, recommendStyles } from './engine/styleEngine.js';
import { generationStatus } from './engine/remoteAdapter.js';
import { cfg } from './configStore.js';
import { PRODUCTS } from './styles.js';
import { sendLoginCode, smtpConfigured } from './email.js';
import { registerAdminRoutes, isAdminRequest } from './admin.js';

const MAX_UPLOAD = 25 * 1024 * 1024;
// A stored source photo is decoded twice (complete + analysis) and again by the
// worker, inside a 512 MB container running up to worker_concurrency jobs. Cap
// the pixel count where the pipeline actually tops out (the provider grid is
// 1536×1024) with generous headroom, instead of the 60 MP jpeg-js allows.
export const MAX_SOURCE_PIXELS = 40 * 1000 * 1000;
const MAX_USER_STORAGE_BYTES = Number(process.env.MAX_USER_STORAGE_BYTES) || 256 * 1024 * 1024;
const SESSION_TTL_DAYS = Number(process.env.SESSION_TTL_DAYS) || 90;
const MAX_FEEDBACK_COMMENT = 1000;
const MAX_EVENT_PROPS = 2048;

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
// visitor's address. The old fallback was the literal string 'museframe' — a
// public salt over the 2^32 IPv4 space, i.e. no salt at all for anyone holding
// a copy of the DB. Falls back to a persisted random secret instead, so a
// deployment with no ADMIN_TOKEN still gets a real salt.
const IP_SALT = process.env.IP_HASH_SALT || process.env.ADMIN_TOKEN || serverSecret('ip_hash_salt');
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
export function maybeGrantFree(userId, isGuest, deviceId, clientIp) {
  const freeUnits = cfg('free_units');
  if (freeUnits <= 0) return 'FREE_UNITS_ZERO';
  if (isGuest && verifyConfig.freeRequiresAuth) return 'REQUIRES_AUTH';

  const deviceHash = deviceId ? hash24(deviceId) : null;
  // Guests must present a device fingerprint. Without one there is nothing to
  // dedupe on, and "no dedupe key" must mean "no free image", never "free image".
  if (isGuest && !deviceHash) return 'NO_DEVICE_ID';
  const dedupeId = isGuest ? deviceHash : userId;
  // The dedupe key used to *switch* when a guest signed in — device hash while
  // guest, user id afterwards — and since a merge keeps the same user row, the
  // user-id key had never been recorded, so the same device collected a second
  // free image on sign-in. Every key in play is now checked, and every one of
  // them is recorded on grant, so neither direction of the merge can re-claim.
  const keys = [...new Set([dedupeId, deviceHash, userId].filter(Boolean))];
  for (const k of keys) {
    if (q1('SELECT id FROM free_grants WHERE dedupe_key = ? LIMIT 1', k)) return 'ALREADY_CLAIMED';
    // Pre-existing grants predate this table; keep honouring their dedupe key.
    if (q1('SELECT id FROM credit_ledger WHERE reference_key = ? LIMIT 1', `grant:free_grant:${k}`)) return 'ALREADY_CLAIMED';
  }

  const perIp = cfg('free_grants_per_ip_day');
  const perDay = cfg('free_grants_per_day');
  if (perDay <= 0 || perIp <= 0) return 'GRANTS_DISABLED';
  // An unknown address shares one bucket rather than being exempt from the cap.
  const ipHash = hash24(clientIp || 'unknown', IP_SALT);
  const w = freeGrantWindow(ipHash);
  if (w.today >= perDay) { console.warn(`[free-grant] site cap reached (${w.today}/${perDay} in 24h) — refusing`); return 'SITE_CAP'; }
  if (w.forIp >= perIp) return 'IP_CAP';

  grantUnits(userId, freeUnits, 'free_grant', dedupeId);
  // One row per key so the *other* key can never claim again (device today,
  // account after the guest merge — same person either way).
  for (const k of keys) {
    run('INSERT INTO free_grants (id, user_id, dedupe_key, device_hash, ip_hash, units, created_at) VALUES (?,?,?,?,?,?,?)',
      uuid(), userId, k, deviceHash, ipHash, k === dedupeId ? freeUnits : 0, now());
  }
  return 'GRANTED';
}

function createSession(userId, deviceId) {
  const token = randomBytes(24).toString('base64url');
  const t = now();
  run('INSERT INTO sessions (token, user_id, device_id, created_at, last_seen_at, expires_at) VALUES (?,?,?,?,?,?)',
    token, userId, deviceId || null, t, t, new Date(Date.now() + SESSION_TTL_DAYS * 86400000).toISOString());
  return token;
}

// Short-lived, per-user HMAC token for <img src> URLs, mirroring the admin
// panel's img_token. The long-lived session token no longer has to ride in a
// query string (Caddy/Cloudflare access logs, browser history, Referer) just to
// render a thumbnail. Valid for the current and previous hour bucket.
function assetImgToken(userId, bucket) {
  return createHmac('sha256', serverSecret('asset_img_token'))
    .update(`img:${userId}:${bucket}`).digest('base64url').slice(0, 24);
}
function assetImgTokenUser(t) {
  if (typeof t !== 'string' || t.length !== 24) return null;
  const h = Math.floor(Date.now() / 3600_000);
  // The token does not name the user, so it is checked against the requester's
  // candidate ids: callers pass the asset owner, see the asset-file route.
  return { verify: (userId) => [h, h - 1].some((b) => {
    const exp = assetImgToken(userId, b);
    return t.length === exp.length && timingSafeEqual(Buffer.from(t), Buffer.from(exp));
  }) };
}

/**
 * A session lookup that actually expires. `?token=` is still accepted, but only
 * on the asset-file route: that is the one place a shipped client puts it (an
 * <img src>), and accepting it everywhere made every query string a credential.
 * New clients use the scoped img_token instead — see web/api.js.
 */
export function authenticate(req, url) {
  const header = req.headers.authorization || '';
  const isAssetFile = req.method === 'GET' && /^\/v1\/assets\/[\w-]+\/file$/.test(url.pathname);
  const token = header.startsWith('Bearer ') ? header.slice(7)
    : isAssetFile ? url.searchParams.get('token') : null;
  if (!token) return null;
  const s = q1('SELECT user_id, expires_at, last_seen_at FROM sessions WHERE token = ?', token);
  if (!s) return null;
  const t = now();
  if (s.expires_at && s.expires_at <= t) return null;
  // Throttled to once an hour so a poll loop doesn't turn every read into a write.
  if (!s.last_seen_at || Date.parse(t) - Date.parse(s.last_seen_at) > 3600_000) {
    run('UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token = ?',
      t, new Date(Date.now() + SESSION_TTL_DAYS * 86400000).toISOString(), token);
  }
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

// ---- control validation -----------------------------------------------------

const CONTROL_DEFAULTS = { strength: 'balanced', fidelity: 'high', composition: 'keep' };
const CONTROL_ALLOWED = {
  strength: ['soft', 'balanced', 'bold'],
  fidelity: ['high', 'natural'],
  composition: ['keep', 'reframe'],
};
export const ASPECT_RATIOS = ['original', '1:1', '4:5', '16:9'];
export const QUALITY_TIERS = ['standard', 'high'];

/**
 * Clamp the three user controls to the allow-list the StyleSpec itself declares
 * (`spec.controls.<name>.allowed`). Anything else — including a caller trying to
 * smuggle instructions into the compiler prompt — collapses to the default.
 * Exported for the tests.
 */
export function coerceControls(spec, controls = {}) {
  const out = {};
  for (const [name, fallback] of Object.entries(CONTROL_DEFAULTS)) {
    const decl = spec?.controls?.[name];
    // A spec that forgets to declare `allowed` must not silently discard the
    // user's choice — fall back to the product-wide vocabulary, never to "".
    const allowed = Array.isArray(decl?.allowed) && decl.allowed.length ? decl.allowed : CONTROL_ALLOWED[name];
    const def = decl?.default && allowed.includes(decl.default) ? decl.default : fallback;
    const given = controls?.[name];
    out[name] = (typeof given === 'string' && allowed.includes(given)) ? given : def;
  }
  return out;
}

// ---- analysis --------------------------------------------------------------

function runAnalysis(assetId, decoded = null) {
  const asset = q1('SELECT * FROM assets WHERE id = ?', assetId);
  if (!asset) return;
  const t = now();
  try {
    const img = decoded || decodeJpeg(readFileSync(path.join(ASSET_DIR, asset.storage_key)));
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

// ---- request field typing ---------------------------------------------------
// A JSON body carries no types, and every handler here binds its fields
// straight into sqlite. An object or array where a string was expected threw a
// TypeError out of the statement — a 500 with a decoder message, sometimes
// after an earlier statement in the same handler had already committed.

/** Reject a present-but-wrong-typed optional string. Returns it (or undefined). */
export function optionalString(v, field, max = 512) {
  if (v === undefined || v === null) return undefined;
  if (typeof v !== 'string') throw new ApiError(422, 'VALIDATION', `${field} must be a string.`);
  if (v.length > max) throw new ApiError(422, 'VALIDATION', `${field} is too long.`);
  return v;
}
/** Reject a present-but-not-a-plain-object optional field. */
export function optionalObject(v, field) {
  if (v === undefined || v === null) return {};
  if (typeof v !== 'object' || Array.isArray(v)) throw new ApiError(422, 'VALIDATION', `${field} must be an object.`);
  return v;
}

// ---- route table -----------------------------------------------------------
// Each handler: (ctx) => body. ctx = {req, res, url, user, params, body, raw}

export const routes = [];
const route = (method, pattern, handler) => routes.push({ method, pattern: new RegExp(`^${pattern}$`), handler });

route('POST', '/v1/auth/exchange', async (ctx) => {
  const { provider, deviceId, locale, displayName, identityToken } = ctx.body || {};
  // JSON gives no types. `deviceId: {}` used to reach the sqlite bind as an
  // object and throw TypeError → 500 INTERNAL_ERROR, after createUser() had
  // already committed a row; `locale: []` did the same. Reject up front.
  optionalString(deviceId, 'deviceId', 200);
  optionalString(locale, 'locale', 40);
  optionalString(displayName, 'displayName', 120);
  optionalString(identityToken, 'identityToken', 8192);
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
        // "We could not reach the issuer's key server" is not "your token is
        // forged". Reporting it as AUTH_INVALID made an outage at Google look
        // to every user like their own account had been rejected, and the
        // client's retry logic treats 401 as terminal.
        if (e.code === 'JWKS_UNAVAILABLE') {
          console.error('[auth] jwks unavailable:', e.message);
          throw new ApiError(503, 'VERIFICATION_UNAVAILABLE', 'Sign-in is temporarily unavailable. Please try again.');
        }
        throw new ApiError(401, 'AUTH_INVALID', 'Sign-in could not be verified.');
      }
    }
    // One code path for both OAuth and email login, so the guest-merge rules
    // (including the "identity already exists" branch) can never drift apart.
    userId = resolveIdentity({
      provider: storedProvider, subject: claims.subject, email: claims.email,
      name: displayName || claims.name || claims.email,
      ctxUser: ctx.user, locale, deviceId, clientIp: ctx.clientIp,
    });
  } else {
    throw new ApiError(422, 'VALIDATION', 'Unknown provider.');
  }
  if (grantOutcome && grantOutcome !== 'GRANTED') console.log(`[free-grant] not granted: ${grantOutcome}`);

  const token = createSession(userId, deviceId);
  const user = q1('SELECT id, is_guest, display_name FROM users WHERE id = ?', userId);
  return { accessToken: token, user: { id: user.id, isGuest: !!user.is_guest, displayName: user.display_name } };
});

/**
 * Move an in-flight guest account onto the account the caller has just proved
 * they own: its projects, photos, jobs and — the part that actually costs money
 * — every unconsumed *purchased* credit bucket. Free grants deliberately do not
 * travel (spec §10.2), which is also why the ledger's UNIQUE (user_id,
 * reference_key) cannot collide here: only uuid-keyed purchase/job references
 * are re-pointed.
 *
 * Before this, the guest id was simply discarded whenever the provider subject
 * already mapped to a user, stranding paid units on an account with no login
 * method — unreachable through the API, forever.
 */
export function mergeGuestInto(targetUserId, guest) {
  if (!guest || !guest.is_guest || guest.id === targetUserId) return false;
  return tx(() => {
    const t = now();
    const buckets = q(`SELECT id FROM credit_buckets WHERE user_id = ? AND source_type <> 'free_grant'`, guest.id);
    for (const b of buckets) {
      run('UPDATE credit_buckets SET user_id = ? WHERE id = ?', targetUserId, b.id);
      run('UPDATE credit_ledger SET user_id = ? WHERE balance_bucket_id = ? AND user_id = ?', targetUserId, b.id, guest.id);
    }
    for (const table of ['projects', 'assets', 'generation_jobs', 'purchases', 'user_feedback', 'events']) {
      run(`UPDATE ${table} SET user_id = ? WHERE user_id = ?`, targetUserId, guest.id);
    }
    // The guest row keeps its free_grants bookkeeping (so the device cannot
    // claim again) but stops being a usable account: its sessions no longer
    // authenticate, because authenticate() filters on deleted_at.
    run(`UPDATE users SET deleted_at = ?, status = 'merged', updated_at = ? WHERE id = ?`, t, t, guest.id);
    console.log(`[merge] guest ${guest.id} → ${targetUserId} (${buckets.length} purchased bucket(s))`);
    return true;
  });
}

// Resolve a verified provider identity to a user id, merging an in-flight guest
// account (its projects + purchased units) into it. Shared by OAuth and email.
function resolveIdentity({ provider, subject, email, name, ctxUser, locale, deviceId, clientIp }) {
  const existing = q1('SELECT user_id FROM auth_identities WHERE provider = ? AND provider_subject = ?', provider, subject);
  let userId;
  if (existing) {
    userId = existing.user_id;
    if (email) run('UPDATE auth_identities SET email_normalized = ? WHERE provider = ? AND provider_subject = ?', email, provider, subject);
    // The identity already exists, so the guest row cannot simply be promoted —
    // carry its content and paid credit across instead of dropping it.
    mergeGuestInto(userId, ctxUser && ctxUser.is_guest ? ctxUser : null);
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

  // Per-address issuance cap. The per-IP rate rule is not enough on its own:
  // it is keyed on a header the caller influences, and re-requesting a code
  // used to reset `attempts` to 0, so unlimited fresh codes × 5 guesses each.
  const prior = q1('SELECT * FROM email_codes WHERE email = ?', email);
  const windowMs = 10 * 60000;
  const inWindow = prior?.window_start && Date.now() - Date.parse(prior.window_start) < windowMs;
  const issued = inWindow ? (prior.issue_count || 0) : 0;
  if (issued >= 5) {
    throw new ApiError(429, 'RATE_LIMITED', '验证码请求过于频繁，请稍后再试。',
      { retryAfterSeconds: Math.ceil((windowMs - (Date.now() - Date.parse(prior.window_start))) / 1000) });
  }

  // 6-digit code, hashed at rest; one active code per email. randomInt is the
  // CSPRNG — Math.random() is xorshift128+, whose state is recoverable from a
  // handful of observed outputs, and this code is a full account credential.
  const code = String(randomInt(100000, 1000000));
  const codeHash = createHash('sha256').update(email + ':' + code).digest('hex');
  const testMode = process.env.ALLOW_TEST_LOGIN === 'true';
  try {
    await sendLoginCode(email, code);
  } catch (e) {
    if (e.code === 'SMTP_NOT_CONFIGURED') throw new ApiError(501, 'PROVIDER_NOT_CONFIGURED', '邮件服务未配置。');
    console.error('[email] send failed:', e.message);
    // In test mode a delivery failure (e.g. unauthorized sending IP) must not
    // block flow verification — the code is still returned below.
    // Outside it, bail out *before* the upsert: overwriting the stored hash on
    // a failed send used to invalidate the working code the user still held.
    if (!testMode) throw new ApiError(502, 'EMAIL_SEND_FAILED', '验证码发送失败，请稍后重试。');
  }
  const t = now();
  run(`INSERT INTO email_codes (email, code_hash, expires_at, attempts, created_at, issue_count, window_start)
       VALUES (?,?,?,0,?,1,?)
       ON CONFLICT(email) DO UPDATE SET code_hash=excluded.code_hash, expires_at=excluded.expires_at,
         attempts=0, created_at=excluded.created_at, issue_count=?, window_start=?`,
    email, codeHash, new Date(Date.now() + windowMs).toISOString(), t, t,
    issued + 1, inWindow ? prior.window_start : t);

  const body = { ok: true, expiresInSeconds: 600 };
  // Double-gated exactly like the other two dev hatches (provider:'dev' login
  // and mock purchases): the flag alone handed the plaintext code for ANY
  // address to ANY unauthenticated caller — takeover of every email account.
  if (testMode && isAdminRequest(ctx.req, ctx.url)) body.devCode = code;
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
  const stored = Buffer.from(String(rec.code_hash));
  const given = Buffer.from(codeHash);
  if (given.length !== stored.length || !timingSafeEqual(given, stored)) {
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
  // 注册即送几张 + 额度用完后的联系邮箱。两者都可在后台热改，客户端每次启动读取。
  freeUnits: cfg('free_units'),
  support: { email: String(cfg('support_email') || '').trim() || null, qqGroup: String(cfg('support_qq_group') || '').trim() || null },
  google: {
    enabled: verifyConfig.googleSignIn,
    // `enabled` reads the live config (admin panel) but this used to read only
    // env, so a client id set in the panel produced enabled:true with a null
    // webClientId — the app then showed a Google button that could not work.
    webClientId: (process.env.GOOGLE_WEB_CLIENT_ID
      || (cfg('google_client_ids') || process.env.GOOGLE_CLIENT_IDS || '').split(',')[0] || '').trim() || null,
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
  // Only the pending source asset this upload intent was issued for. Without
  // the kind/status predicate the route also matched assets that were already
  // 'ready' (re-writing the bytes under a recorded width/height and analysis)
  // and the worker's generated candidates (overwriting a delivered image).
  const asset = q1(`SELECT * FROM assets WHERE id = ? AND user_id = ? AND kind = 'source' AND deleted_at IS NULL`, ctx.params[0], user.id);
  if (!asset) throw new ApiError(404, 'ASSET_NOT_READY', 'Unknown asset.');
  if (asset.status !== 'pending') throw new ApiError(409, 'ASSET_NOT_READY', 'This upload has already been completed.');
  if (!ctx.raw || ctx.raw.length < 100) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Empty upload.');
  if (ctx.raw.length > MAX_UPLOAD) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Images up to 20 MB are supported.');
  // MIME magic bytes: JPEG must start FF D8 (spec §14.2 — never trust extension).
  if (!(ctx.raw[0] === 0xFF && ctx.raw[1] === 0xD8)) throw new ApiError(422, 'ASSET_UNSUPPORTED', 'Not a JPEG image.');
  // Per-account byte ceiling. A session costs nothing to mint, so without this
  // an anonymous caller could write 25 MB per request into data/ — a bind mount
  // on the same host as the LensCript production stack — until the disk filled.
  const used = q1(`SELECT COALESCE(SUM(byte_size), 0) AS n FROM assets WHERE user_id = ? AND deleted_at IS NULL AND id <> ?`, user.id, asset.id).n;
  if (used + ctx.raw.length > MAX_USER_STORAGE_BYTES) {
    throw new ApiError(413, 'STORAGE_QUOTA_EXCEEDED', 'Storage limit reached — delete some projects and try again.');
  }
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
  // jpeg-js throws on a truncated/malformed file (the PUT only checks the FF D8
  // magic bytes), which used to escape as a 500 INTERNAL_ERROR carrying the raw
  // decoder message. The documented contract for an unusable image is 422.
  let img;
  try {
    img = decodeJpeg(readFileSync(file)); // validates decodability + dimensions
  } catch (e) {
    console.error('[complete] decode failed', asset.id, e.message);
    throw new ApiError(422, 'ASSET_UNSUPPORTED', 'This image could not be read.');
  }
  if (img.width * img.height > MAX_SOURCE_PIXELS) {
    throw new ApiError(422, 'ASSET_UNSUPPORTED', 'This image is too large — please use one under 40 megapixels.');
  }
  const t = now();
  run('UPDATE assets SET status = ?, width = ?, height = ?, updated_at = ? WHERE id = ?', 'ready', img.width, img.height, t, asset.id);
  run(`INSERT OR IGNORE INTO photo_analyses (id, asset_id, analyzer_version, status, created_at, updated_at)
       VALUES (?,?,?,?,?,?)`, uuid(), asset.id, 'heuristic-0.1', 'pending', t, t);
  // Hand the already-decoded image over: runAnalysis used to re-read the file
  // and decode it a second time, doubling the CPU burst on the event loop.
  setImmediate(() => runAnalysis(asset.id, img));
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

// Short-lived, account-scoped token for <img src> URLs, so the session bearer
// token stops riding in query strings that Caddy and Cloudflare log verbatim.
route('GET', '/v1/assets/img-token', (ctx) => {
  const user = requireUser(ctx);
  return { token: assetImgToken(user.id, Math.floor(Date.now() / 3600_000)), ttlSeconds: 3600 };
});

// Image bytes (session-checked stand-in for short-lived signed URLs).
route('GET', '/v1/assets/([\\w-]+)/file', (ctx) => {
  const it = ctx.url.searchParams.get('img_token');
  let asset;
  if (it) {
    asset = q1('SELECT * FROM assets WHERE id = ? AND deleted_at IS NULL', ctx.params[0]);
    const v = assetImgTokenUser(it);
    // The token is bound to the owning account, so it only ever unlocks that
    // account's own images and only for the current/previous hour.
    if (!asset || !v || !v.verify(asset.user_id)) throw new ApiError(404, 'ASSET_NOT_READY', 'Unknown asset.');
  } else {
    const user = requireUser(ctx);
    asset = q1('SELECT * FROM assets WHERE id = ? AND user_id = ? AND deleted_at IS NULL', ctx.params[0], user.id);
  }
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

  const { projectId, sourceAssetId, styleVersionId, parentJobId } = ctx.body || {};
  // `controls: null` defeated the `= {}` default (null is a supplied value) and
  // reached `controls.strength` as a TypeError → 500 after the paywall checks.
  const controls = optionalObject(ctx.body?.controls, 'controls');
  const output = optionalObject(ctx.body?.output, 'output');
  const project = q1('SELECT * FROM projects WHERE id = ? AND user_id = ? AND deleted_at IS NULL', projectId, user.id);
  if (!project) throw new ApiError(404, 'NOT_FOUND', 'Project not found.');
  const asset = q1('SELECT * FROM assets WHERE id = ? AND user_id = ? AND status = ?', sourceAssetId, user.id, 'ready');
  if (!asset) throw new ApiError(409, 'ASSET_NOT_READY', 'Source photo is not ready.');
  // s.status matters as much as v.status: the emergency takedown route sets
  // styles.status='disabled', and without this predicate any client holding a
  // cached styleVersionId kept generating with a style that had been pulled.
  const sv = q1(`SELECT v.*, s.premium FROM style_versions v JOIN styles s ON s.id = v.style_id
                 WHERE v.id = ? AND v.status = 'published' AND s.status = 'published'`, styleVersionId);
  if (!sv) throw new ApiError(404, 'STYLE_UNAVAILABLE', 'This direction is not available.');

  const plan = userPlan(user.id);
  if (sv.premium && plan === 'free') {
    throw new ApiError(402, 'INSUFFICIENT_ENTITLEMENT', 'A Creator plan is required for this direction.', { premiumStyle: true });
  }

  // The StyleSpec declares an allow-list per control; it was never enforced, and
  // for the three compiler-backed styles these strings are interpolated straight
  // into the prompt-compiler's user turn — i.e. prompt injection against the
  // operator's own paid provider. Anything not on the list becomes the default.
  const safeControls = coerceControls(JSON.parse(sv.spec), controls);
  // aspectRatio reaches the prompt builder and the crop step; qualityTier is
  // echoed back to the client. Both were taken verbatim from the request.
  // The high tier is a Creator feature (highResolution); anyone else asking for
  // it is quietly served the standard tier rather than refused.
  const wantsHigh = output.qualityTier === 'high';
  const canHigh = plan.startsWith('creator');
  const safeOutput = {
    aspectRatio: ASPECT_RATIOS.includes(output.aspectRatio) ? output.aspectRatio : 'original',
    qualityTier: wantsHigh && canHigh ? 'high' : 'standard',
  };
  void QUALITY_TIERS;

  const units = 1;
  // Cheap pre-check so a paywall bounce doesn't litter the project with failed
  // rows; reserveUnits below remains the authoritative, race-safe gate.
  const balance = availableUnits(user.id);
  if (balance < units) {
    throw new ApiError(402, 'INSUFFICIENT_ENTITLEMENT', 'A standard image is required.',
      { requiredUnits: units, availableUnits: balance });
  }
  const jobId = uuid(), t = now();
  // One transaction for the row AND its reserve. They used to commit separately,
  // which left two bad states behind a crash in between: a 'created' job with no
  // ledger entry (boot recovery re-queued it and generated a free image), and a
  // failed reserve needing a compensating UPDATE that itself could be lost.
  // tx() is re-entrant, so reserveUnits' own tx() joins this one.
  try {
    tx(() => {
      run(`INSERT INTO generation_jobs (id, user_id, project_id, source_asset_id, style_version_id, parent_job_id, status, stage, controls, output, reserved_units, created_at, updated_at)
           VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
        jobId, user.id, projectId, sourceAssetId, styleVersionId, parentJobId || null, 'queued', 'preparing',
        JSON.stringify(safeControls),
        JSON.stringify(safeOutput),
        units, t, t);
      reserveUnits(user.id, jobId, units);
      run('UPDATE projects SET status = ?, updated_at = ? WHERE id = ?', 'generating', now(), projectId);
    });
  } catch (e) {
    // Nothing was written: the whole transaction rolled back, so there is no
    // orphan job row to clean up.
    if (e.code === 'INSUFFICIENT_ENTITLEMENT') throw new ApiError(402, e.code, e.message, e.details);
    throw e;
  }
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
    const t = now();
    tx(() => {
      releaseUnits(user.id, j.id);
      run('UPDATE generation_jobs SET status = ?, stage = ?, finished_at = ?, updated_at = ? WHERE id = ?', 'cancelled', 'failed', t, t, j.id);
      // The project was flipped to 'generating' when the job was created and
      // nothing put it back, so a cancelled job left the project sitting under
      // "In progress" in Projects forever. A project that already has a chosen
      // candidate is still ready; one that never produced anything is a draft.
      const p = q1('SELECT selected_candidate_id FROM projects WHERE id = ?', j.project_id);
      run('UPDATE projects SET status = ?, updated_at = ? WHERE id = ?',
        p?.selected_candidate_id ? 'ready' : 'draft', t, j.project_id);
    });
    return { id: j.id, status: 'cancelled' };
  }
  return { id: j.id, status: j.status }; // best effort — running jobs finish
});

route('POST', '/v1/candidates/([\\w-]+)/feedback', (ctx) => {
  const user = requireUser(ctx);
  const { rating, reasonCodes = [], comment } = ctx.body || {};
  if (!['positive', 'negative'].includes(rating)) throw new ApiError(422, 'VALIDATION', 'rating must be positive|negative');
  // Ownership, resolved the same way /export does it. Without this, any free
  // guest token could bury *other people's* candidates in negative ratings —
  // and those rows feed the per-style numbers the operator uses to decide
  // emergency takedowns.
  const cand = q1(`SELECT c.id, j.user_id FROM generation_candidates c
                   JOIN generation_jobs j ON j.id = c.job_id WHERE c.id = ?`, ctx.params[0]);
  if (!cand || cand.user_id !== user.id) throw new ApiError(404, 'NOT_FOUND', 'Candidate not found.');
  const codes = (Array.isArray(reasonCodes) ? reasonCodes : []).slice(0, 8).map((c) => String(c).slice(0, 40));
  const text = comment == null ? null : String(comment).slice(0, MAX_FEEDBACK_COMMENT);
  // One rating per (user, candidate): re-rating replaces, it does not stack.
  const prior = q1('SELECT id FROM user_feedback WHERE user_id = ? AND candidate_id = ?', user.id, cand.id);
  if (prior) {
    run('UPDATE user_feedback SET rating = ?, reason_codes = ?, comment = ?, created_at = ? WHERE id = ?',
      rating, JSON.stringify(codes), text, now(), prior.id);
  } else {
    run('INSERT INTO user_feedback (id, user_id, candidate_id, rating, reason_codes, comment, created_at) VALUES (?,?,?,?,?,?,?)',
      uuid(), user.id, cand.id, rating, JSON.stringify(codes), text, now());
  }
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

const ZH_PRODUCT_NAMES = Object.fromEntries(PRODUCTS.map(p => [p.internalKey, p.displayNameZh || p.displayName]));
route('GET', '/v1/products', () => ({
  products: q('SELECT * FROM products WHERE active = 1').map(p => ({
    internalKey: p.internal_key, productType: p.product_type, displayName: p.display_name,
    displayNameZh: ZH_PRODUCT_NAMES[p.internal_key] || p.display_name,
    grantedUnits: p.granted_units, priceMinor: p.price_minor, priceCnyMinor: p.price_cny_minor ?? null, currency: p.currency, period: p.period,
    googleProductId: p.google_product_id || p.internal_key,
    appleProductId: p.apple_product_id || p.internal_key,
  })),
}));

/**
 * Grant a purchase's units. A product may legitimately grant none — a
 * subscription that only unlocks premium styles — and credit_buckets has
 * CHECK (granted_units > 0), so the zero case must be skipped, not attempted.
 * Exported for the pending-purchase sweeper in maintenance.js.
 */
export function grantPurchaseUnits(userId, product, purchaseId, expires, referenceId = null) {
  if (!product.granted_units || product.granted_units <= 0) return null;
  const unitExpiry = product.product_type === 'pack'
    ? new Date(Date.now() + 90 * 86400000).toISOString()
    : expires;
  return grantUnits(userId, product.granted_units, 'purchase', purchaseId, unitExpiry, referenceId);
}

// Google order ids look like GPA.3312-1234-5678-90123 (and ..0, ..1 per renewal).
export const PLAY_ORDER_RE = /^[A-Za-z0-9._-]{1,128}$/;

/**
 * Re-key the pending row from the client-supplied purchase token onto Google's
 * own order id, and return the id the rest of the flow should dedupe on. If a
 * row for the order id already exists (a replay, or a retry after a timeout),
 * the token-keyed placeholder is dropped instead — leaving both would let the
 * same payment be counted twice.
 */
export function adoptCanonicalTxId(platform, purchaseToken, orderId) {
  if (!orderId || orderId === purchaseToken) return purchaseToken;
  return tx(() => {
    const canonical = q1('SELECT id FROM purchases WHERE platform = ? AND external_transaction_id = ?', platform, orderId);
    const pending = q1('SELECT id, status FROM purchases WHERE platform = ? AND external_transaction_id = ?', platform, purchaseToken);
    if (canonical && pending && pending.id !== canonical.id) {
      if (pending.status === 'pending') run('DELETE FROM purchases WHERE id = ?', pending.id);
    } else if (!canonical && pending) {
      run('UPDATE purchases SET external_transaction_id = ? WHERE id = ?', orderId, pending.id);
    }
    return orderId;
  });
}

/** Write the "we are about to ask the store about this" row. Idempotent. */
function recordPendingPurchase(userId, product, platform, externalTxId) {
  const t = now();
  run(`INSERT OR IGNORE INTO purchases (id, user_id, product_id, platform, external_transaction_id, status, amount_minor, currency, purchased_at, expires_at, created_at)
       VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
    uuid(), userId, product.id, platform, externalTxId, 'pending', product.price_minor, product.currency, t, null, t);
}

function markPurchase(platform, externalTxId, status) {
  run(`UPDATE purchases SET status = ? WHERE platform = ? AND external_transaction_id = ? AND status = 'pending'`,
    status, platform, externalTxId);
}

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
    const kind = product.product_type === 'subscription' ? 'subscription' : 'product';
    // ---- the replay gate ---------------------------------------------------
    // The token goes into a Play API URL path. Before, it was interpolated raw
    // AND used verbatim as external_transaction_id, so `<token>?x=1`, `<token>#`
    // and `%2E`-escaped variants all resolved at Google to the SAME purchase
    // while landing in `purchases` as brand-new, never-seen transactions: one
    // real payment, unlimited grants. Rejecting anything outside Google's
    // URL-safe-base64 alphabet makes "what we store" and "what Google reads"
    // the same bytes by construction.
    try {
      assertPlayToken(purchaseToken, storeProductId);
    } catch {
      throw new ApiError(422, 'VALIDATION', 'purchaseToken is not a valid Google Play token.');
    }
    externalTxId = purchaseToken; // canonical form; replaced by orderId below
    // Record the attempt BEFORE talking to Play. Google has already taken the
    // user's money at this point; if we cannot reach the Play API there has to
    // be a row for the sweeper to retry, or the entitlement is simply lost.
    recordPendingPurchase(user.id, product, platform, externalTxId);
    let r;
    try {
      r = await verifyPlayPurchase({ productId: storeProductId, purchaseToken, kind });
    } catch (e) {
      if (e.code === 'PROVIDER_NOT_CONFIGURED') throw new ApiError(501, e.code, e.message);
      // "Play says no" and "we could not ask Play" are different answers. Only
      // the first is a permanent 402; a timeout, DNS failure or 5xx leaves the
      // row 'pending' and returns a retryable code.
      if (e.code === 'PURCHASE_INVALID') {
        markPurchase(platform, externalTxId, 'invalid');
        throw new ApiError(402, 'PURCHASE_INVALID', 'This purchase could not be verified.');
      }
      console.error('[purchase] play verification unavailable:', e.code || e.name, e.message);
      throw new ApiError(503, 'VERIFICATION_UNAVAILABLE', 'The store could not be reached. Your purchase is recorded and will be verified automatically.');
    }
    if (!r.valid) {
      markPurchase(platform, externalTxId, 'invalid');
      throw new ApiError(402, 'PURCHASE_INVALID', 'This purchase is not active.');
    }
    // Prefer Google's own transaction id. It is server-issued (so a client can
    // never vary it), and it rolls forward on each subscription renewal, which
    // is exactly the per-period key the ledger wants.
    if (r.orderId && PLAY_ORDER_RE.test(r.orderId)) {
      externalTxId = adoptCanonicalTxId(platform, purchaseToken, r.orderId);
    }
    if (r.acknowledgementState === 0) {
      // Play auto-refunds an unacknowledged purchase after 3 days.
      acknowledgePlayPurchase({ productId: storeProductId, purchaseToken, kind })
        .then((a) => { if (!a.skipped && !a.ok) console.warn('[purchase] play acknowledge returned', a.status); })
        .catch((e) => console.warn('[purchase] play acknowledge failed:', e.message));
      if (process.env.PLAY_ACKNOWLEDGE !== 'true') {
        console.warn(`[purchase] ${externalTxId} is UNACKNOWLEDGED at Play; the client must acknowledge it or Play refunds it in 3 days (set PLAY_ACKNOWLEDGE=true to acknowledge server-side).`);
      }
    }
    verified = true; expiresAt = r.expiresAt;
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

  const t = now();
  const expires = expiresAt || (product.period ? new Date(Date.now() + (product.period === 'month' ? 30 : 365) * 86400000).toISOString() : null);
  const existing = q1('SELECT * FROM purchases WHERE platform = ? AND external_transaction_id = ?', platform, externalTxId);

  // Idempotent on the external transaction — replays never double-grant. But an
  // auto-renewing Play subscription re-presents the SAME purchaseToken every
  // billing period: short-circuiting on the id alone meant period 2 onwards
  // granted nothing and never pushed expires_at forward, so userPlan() dropped
  // a paying subscriber back to 'free' while Play kept charging. Only a verified
  // expiry that is no later than the stored one is a true no-op.
  if (existing && existing.status === 'verified') {
    const known = existing.expires_at ? Date.parse(existing.expires_at) : null;
    const fresh = expires ? Date.parse(expires) : null;
    if (fresh && (!known || fresh > known)) {
      tx(() => {
        run('UPDATE purchases SET expires_at = ? WHERE id = ?', expires, existing.id);
        // Period-scoped reference key: exactly one grant per billing period.
        grantPurchaseUnits(user.id, product, existing.id, expires, `${existing.id}:${expires}`);
      });
    }
    return { purchaseId: existing.id, status: 'verified', entitlements: entitlements(user.id) };
  }

  const purchaseId = existing ? existing.id : uuid();
  // One transaction. The INSERT used to commit on its own and grantUnits ran
  // after it, so a product configured with grantedUnits = 0 tripped the
  // credit_buckets CHECK, left a 'verified' row behind, and every retry then
  // returned 200 with an unchanged balance — money taken, nothing granted.
  tx(() => {
    if (existing) {
      run('UPDATE purchases SET status = ?, expires_at = ?, purchased_at = ? WHERE id = ?', 'verified', expires, t, purchaseId);
    } else {
      run(`INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id, status, amount_minor, currency, purchased_at, expires_at, created_at)
           VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
        purchaseId, user.id, product.id, platform, externalTxId, 'verified', product.price_minor, product.currency, t, expires, t);
    }
    grantPurchaseUnits(user.id, product, purchaseId, expires);
  });
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

// Telemetry ingest. Now needs a session (one is free, but it makes the writer
// attributable and puts the route behind the auth-exchange rate limit) and caps
// the serialized props — `name` was truncated, `props` was not, so a single
// anonymous 26 MB body used to land in the DB verbatim, repeatable at line rate.
route('POST', '/v1/events', (ctx) => {
  const user = requireUser(ctx);
  const events = Array.isArray(ctx.body?.events) ? ctx.body.events.slice(0, 50) : [];
  let accepted = 0;
  for (const e of events) {
    if (!e?.name) continue;
    let props = '{}';
    try { props = JSON.stringify(e.props ?? {}); } catch { props = '{}'; }
    if (props.length > MAX_EVENT_PROPS) props = JSON.stringify({ truncated: true, bytes: props.length });
    run('INSERT INTO events (id, user_id, name, props, occurred_at) VALUES (?,?,?,?,?)',
      uuid(), user.id, String(e.name).slice(0, 64), props, now());
    accepted++;
  }
  return { accepted };
});

// Sign out: the only way, before this, to stop using a token was to forget it —
// the row stayed valid for the lifetime of the database.
route('DELETE', '/v1/auth/session', (ctx) => {
  requireUser(ctx);
  const header = ctx.req.headers.authorization || '';
  if (header.startsWith('Bearer ')) run('DELETE FROM sessions WHERE token = ?', header.slice(7));
  return { ok: true };
});

// Health means "can this process still do its job", not "is the process up".
// A static literal reported green through an unwritable DB, a full disk, and a
// deployment whose image key had been cleared — the exact states the compose
// healthcheck and the post-deploy smoke test exist to catch.
route('GET', '/v1/health', (ctx) => {
  const gen = generationStatus();
  let dbOk = true, dbError = null;
  try {
    db.exec('PRAGMA quick_check(1)');
    q1('SELECT COUNT(*) AS n FROM app_config');
  } catch (e) {
    dbOk = false; dbError = e.message;
    console.error('[health] database probe failed:', e.message);
  }
  if (!dbOk) {
    ctx.res.writeHead(503, { 'Content-Type': 'application/json' });
    ctx.res.end(JSON.stringify({ ok: false, service: 'museframe-api', database: 'error', time: now() }));
    return null;
  }
  return {
    ok: true, service: 'museframe-api', time: now(),
    database: 'ok',
    // Reported, never a 503: a missing provider key is a config state the
    // operator fixes in the panel, not a reason for Docker to cycle the container.
    generation: { available: gen.available, mode: gen.mode, reason: gen.reason },
    queue: queueDepth(),
  };
});

// ---- Admin back office (spec §19) --------------------------------------
// Route definitions live in admin.js; registered here so they share this
// module's `routes` array. `route` and `ApiError` are passed in explicitly
// (rather than imported by admin.js) so there is no import cycle between the
// two modules.
registerAdminRoutes(route, { ApiError });
