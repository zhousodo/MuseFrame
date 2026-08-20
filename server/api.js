// MuseFrame API (spec §10). JSON camelCase over /v1, stable error codes,
// Idempotency-Key on writes that bill, bearer sessions (guest-first).
import { createHash, randomBytes } from 'node:crypto';
import { writeFileSync, readFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import { ASSET_DIR, q, q1, run, tx, uuid, now } from './db.js';
import { grantUnits, reserveUnits, availableUnits } from './ledger.js';
import { releaseUnits } from './ledger.js';
import { enqueueJob } from './jobs.js';
import { decodeJpeg, analyzeImage, recommendStyles } from './engine/styleEngine.js';

const FREE_UNITS = Number(process.env.FREE_UNITS || 1);
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
  grantUnits(id, FREE_UNITS, 'free_grant', id);
  return id;
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
    freeCompletedImagesRemaining: plan === 'free' ? Math.max(0, FREE_UNITS - savedFree) : 0,
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

route('POST', '/v1/auth/exchange', (ctx) => {
  const { provider, deviceId, locale, displayName } = ctx.body || {};
  let userId;
  if (provider === 'guest' || !provider) {
    userId = createUser(displayName || null, true, locale);
  } else {
    // Fake provider adapter for dev (spec PR-004): subject derived from token.
    const subject = createHash('sha256').update(String(ctx.body.identityToken || deviceId || 'anon')).digest('hex').slice(0, 24);
    const existing = q1('SELECT user_id FROM auth_identities WHERE provider = ? AND provider_subject = ?', provider, subject);
    if (existing) userId = existing.user_id;
    else {
      userId = createUser(displayName || (provider === 'apple' ? 'Apple user' : 'Google user'), false, locale);
      run('INSERT INTO auth_identities (id, user_id, provider, provider_subject, created_at) VALUES (?,?,?,?,?)',
        uuid(), userId, provider, subject, now());
    }
  }
  const token = createSession(userId, deviceId);
  const user = q1('SELECT id, is_guest, display_name FROM users WHERE id = ?', userId);
  return { accessToken: token, user: { id: user.id, isGuest: !!user.is_guest, displayName: user.display_name } };
});

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
    generation: { estimatedRangeSeconds: process.env.IMAGE_PROVIDER === 'remote' ? [60, 300] : [5, 30] },
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
    job: { id: jobId, status: 'queued', stage: 'preparing', estimatedRangeSeconds: process.env.IMAGE_PROVIDER === 'remote' ? [60, 300] : [5, 30], reservedUnits: units, createdAt: t },
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
  })),
}));

// Mock purchase verification (spec §10.10 shape; web platform stands in for stores).
route('POST', '/v1/purchases/verify', (ctx) => {
  const user = requireUser(ctx);
  const { platform = 'web', productKey, transactionId } = ctx.body || {};
  const product = q1('SELECT * FROM products WHERE internal_key = ? AND active = 1', productKey);
  if (!product) throw new ApiError(404, 'NOT_FOUND', 'Unknown product.');
  const txId = transactionId || `mock_${uuid()}`;
  const existing = q1('SELECT * FROM purchases WHERE platform = ? AND external_transaction_id = ?', platform, txId);
  if (existing) return { purchaseId: existing.id, status: existing.status, entitlements: entitlements(user.id) };
  const purchaseId = uuid(), t = now();
  const expires = product.period
    ? new Date(Date.now() + (product.period === 'month' ? 30 : 365) * 86400000).toISOString()
    : null;
  tx(() => {
    run(`INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id, status, amount_minor, currency, purchased_at, expires_at, created_at)
         VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
      purchaseId, user.id, product.id, platform, txId, 'verified', product.price_minor, product.currency, t, expires, t);
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
