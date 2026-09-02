// MuseFrame admin back office (spec §19). Token-gated (env ADMIN_TOKEN only —
// NOT runtime-configurable, security). Route table + handlers only; wired
// into api.js's shared `routes` array via registerAdminRoutes(route, deps).
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { db, q, q1, run, tx, ASSET_DIR, uuid } from './db.js';
import { cfg, setCfg, listCfg, SECRET_KEYS } from './configStore.js';
import { sendMail } from './email.js';
import { generationStatus, imageProvider } from './engine/remoteAdapter.js';
import { freeGrantWindow, grantUnits, availableUnits } from './ledger.js';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN || '';
const tokenBuf = Buffer.from(ADMIN_TOKEN);
function tokenEq(t) {
  if (!ADMIN_TOKEN || typeof t !== 'string') return false;
  const b = Buffer.from(t);
  return b.length === tokenBuf.length && crypto.timingSafeEqual(b, tokenBuf);
}

/**
 * Does this request carry the operator's admin token? Exported so the dev-only
 * escape hatches on the public API (mock purchases, test login) can require the
 * operator to be present instead of trusting an env flag alone — a flag left on
 * in production is otherwise a free-units faucet for anyone on the internet.
 */
export function isAdminRequest(req, url) {
  // Header only. `?admin_token=` used to be accepted as well, which put the
  // long-lived operator credential into Caddy's access log, the browser's
  // history and any Referer sent onward — for a token that grants the SQL
  // console. Nothing in web/ ever sent it that way; <img src> URLs use the
  // short-lived img_token below instead.
  return tokenEq(req?.headers?.['x-admin-token']);
}

export function registerAdminRoutes(route, deps) {
  const { ApiError } = deps;

  function requireAdmin(ctx) {
    // Prefer the header; a short-lived image token is accepted only on the
    // asset route (checked there), never here.
    if (!isAdminRequest(ctx.req, ctx.url)) throw new ApiError(401, 'AUTH_REQUIRED', 'Admin token required.');
  }

  // Short-lived HMAC image token so the long-lived admin token never rides in
  // <img src> URLs / server logs (defense for M-2). Valid for the current and
  // previous hour bucket.
  function imgToken(bucket) {
    return crypto.createHmac('sha256', ADMIN_TOKEN).update(`img:${bucket}`).digest('base64url').slice(0, 24);
  }
  function imgTokenValid(t) {
    if (typeof t !== 'string') return false;
    const h = Math.floor(Date.now() / 3600_000);
    for (const b of [h, h - 1]) {
      const exp = imgToken(b);
      if (t.length === exp.length && crypto.timingSafeEqual(Buffer.from(t), Buffer.from(exp))) return true;
    }
    return false;
  }

  // DB browser masking: session tokens and secret config values never leave in
  // cleartext (defeats H-2). app_config is row-aware — only secret keys hidden.
  const secretSet = new Set(SECRET_KEYS);
  function maskCell(table, row, col, v) {
    if (v == null) return v;
    if (table === 'sessions' && col === 'token' && typeof v === 'string') return v.slice(0, 6) + '…';
    if (table === 'app_config' && col === 'value' && secretSet.has(row.key)) return '••••(secret)';
    if (typeof v === 'string' && v.length > 300) return v.slice(0, 300) + '…';
    return v;
  }
  // Freeform query console must never reach credential/secret tables (a SELECT
  // could otherwise exfiltrate raw session tokens or config secrets, bypassing
  // column masking via aliases). These tables have no legitimate analytics use.
  const QUERY_DENY_TABLES = /\b(sessions|auth_identities|app_config|idempotency_records)\b/i;

  // ---- existing endpoints (byte-compatible) ---------------------------------

  route('GET', '/v1/admin/overview', (ctx) => {
    requireAdmin(ctx);
    const jobsByStatus = {};
    for (const r of q('SELECT status, COUNT(*) AS n FROM generation_jobs GROUP BY status')) jobsByStatus[r.status] = r.n;
    const dur = q1(`SELECT COUNT(*) AS n, AVG((julianday(finished_at)-julianday(created_at))*86400) AS avg_s
                    FROM generation_jobs WHERE status='succeeded' AND finished_at IS NOT NULL`);
    return {
      generation: generationSummary(),
      abuse: abuseSummary(),
      users: q1('SELECT COUNT(*) AS n FROM users').n,
      usersToday: q1(`SELECT COUNT(*) AS n FROM users WHERE created_at >= datetime('now','-1 day')`).n,
      jobsByStatus,
      succeededAvgSeconds: dur.avg_s ? Math.round(dur.avg_s) : null,
      unitsGranted: q1(`SELECT COALESCE(SUM(units),0) AS n FROM credit_ledger WHERE entry_type='grant'`).n,
      unitsConsumed: q1(`SELECT COUNT(*) AS n FROM credit_ledger WHERE entry_type='commit'`).n,
      revenueMinor: q1(`SELECT COALESCE(SUM(amount_minor),0) AS n FROM purchases WHERE status='verified'`).n,
      purchases: q(`SELECT p.display_name AS product, COUNT(*) AS n FROM purchases pu JOIN products p ON p.id=pu.product_id WHERE pu.status='verified' GROUP BY p.id`),
      feedback: {
        positive: q1(`SELECT COUNT(*) AS n FROM user_feedback WHERE rating='positive'`).n,
        negative: q1(`SELECT COUNT(*) AS n FROM user_feedback WHERE rating='negative'`).n,
      },
      topStyles: q(`SELECT s.public_name AS name, COUNT(*) AS jobs FROM generation_jobs j
                    JOIN style_versions v ON v.id=j.style_version_id JOIN styles s ON s.id=v.style_id
                    GROUP BY s.id ORDER BY jobs DESC LIMIT 8`),
    };
  });

  route('GET', '/v1/admin/jobs', (ctx) => {
    requireAdmin(ctx);
    const limit = Math.min(200, Number(ctx.url.searchParams.get('limit')) || 60);
    return {
      jobs: q(`SELECT j.id, j.status, j.stage, j.error_code, j.attempt_count, j.cost_minor, j.created_at,
                      CAST((julianday(COALESCE(j.finished_at, j.updated_at))-julianday(j.created_at))*86400 AS INTEGER) AS seconds,
                      substr(j.user_id,1,8) AS user,
                      (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=j.user_id AND ai.email_normalized IS NOT NULL LIMIT 1) AS email,
                      s.public_name AS style,
                      j.source_asset_id AS sourceAssetId,
                      (SELECT c.asset_id FROM generation_candidates c WHERE c.job_id=j.id LIMIT 1) AS candidateAssetId
               FROM generation_jobs j
               JOIN style_versions v ON v.id=j.style_version_id JOIN styles s ON s.id=v.style_id
               ORDER BY j.created_at DESC LIMIT ?`, limit),
    };
  });

  route('GET', '/v1/admin/feedback', (ctx) => {
    requireAdmin(ctx);
    return {
      feedback: q(`SELECT f.rating, f.reason_codes, f.created_at, s.public_name AS style
                   FROM user_feedback f
                   LEFT JOIN generation_candidates c ON c.id=f.candidate_id
                   LEFT JOIN generation_jobs j ON j.id=c.job_id
                   LEFT JOIN style_versions v ON v.id=j.style_version_id LEFT JOIN styles s ON s.id=v.style_id
                   ORDER BY f.created_at DESC LIMIT 100`),
    };
  });

  route('GET', '/v1/admin/purchases', (ctx) => {
    requireAdmin(ctx);
    return {
      purchases: q(`SELECT pu.amount_minor, pu.currency, pu.purchased_at, pu.status, p.display_name AS product, substr(pu.user_id,1,8) AS user,
                           (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=pu.user_id AND ai.email_normalized IS NOT NULL LIMIT 1) AS email
                    FROM purchases pu JOIN products p ON p.id=pu.product_id ORDER BY pu.purchased_at DESC LIMIT 100`),
    };
  });

  // 用户列表：id、邮箱、登录方式、身份、点数余额、生成数（spec §19 运营可见性）。
  // `q` 按邮箱 / 显示名 / ID 前缀模糊搜索——客服收到「加额度」邮件后按发件邮箱找人。
  route('GET', '/v1/admin/users', (ctx) => {
    requireAdmin(ctx);
    const limit = Math.min(200, Number(ctx.url.searchParams.get('limit')) || 100);
    const search = String(ctx.url.searchParams.get('q') || '').trim().toLowerCase().slice(0, 120);
    const like = `%${search.replace(/[%_\\]/g, (c) => '\\' + c)}%`;
    const where = search
      ? `AND (u.id LIKE ? ESCAPE '\\' OR lower(u.display_name) LIKE ? ESCAPE '\\'
             OR EXISTS (SELECT 1 FROM auth_identities ai WHERE ai.user_id=u.id AND ai.email_normalized LIKE ? ESCAPE '\\'))`
      : '';
    const params = search ? [like, like, like, limit] : [limit];
    return {
      users: q(`SELECT u.id AS userId, substr(u.id,1,8) AS id, u.display_name AS displayName, u.is_guest AS isGuest, u.created_at AS createdAt,
                       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=u.id AND ai.email_normalized IS NOT NULL LIMIT 1) AS email,
                       (SELECT group_concat(DISTINCT ai.provider) FROM auth_identities ai WHERE ai.user_id=u.id) AS providers,
                       (SELECT COALESCE(SUM(l.units),0) FROM credit_ledger l WHERE l.user_id=u.id) AS units,
                       (SELECT COUNT(*) FROM generation_jobs j WHERE j.user_id=u.id) AS jobs
                FROM users u WHERE u.deleted_at IS NULL ${where} ORDER BY u.created_at DESC LIMIT ?`, ...params),
    };
  });

  // 手动加额度（「额度用完 → 邮件联系我们 → 后台充值」流程的落点）。
  // 只走 ledger 的正规 grant 路径：append-only、有 reference_key，和购买发放同一套账。
  // 目标用户用完整 ID 或邮箱指定；邮箱命中多个账号时拒绝，避免充错人。
  route('POST', '/v1/admin/users/grant', (ctx) => {
    requireAdmin(ctx);
    const { userId, email, units, note, expiresInDays, idempotencyKey } = ctx.body || {};
    if (idempotencyKey !== undefined && (typeof idempotencyKey !== 'string' || !/^[\w.-]{8,80}$/.test(idempotencyKey))) throw new ApiError(422, 'VALIDATION', 'idempotencyKey must be 8-80 chars of [A-Za-z0-9_.-].');
    if (!Number.isInteger(units) || units <= 0 || units > 10000) throw new ApiError(422, 'VALIDATION', 'units must be an integer between 1 and 10000.');
    if (note !== undefined && (typeof note !== 'string' || note.length > 300)) throw new ApiError(422, 'VALIDATION', 'note must be a string of at most 300 chars.');
    let expiresAt = null;
    if (expiresInDays !== undefined && expiresInDays !== null) {
      if (!Number.isInteger(expiresInDays) || expiresInDays <= 0 || expiresInDays > 3650) throw new ApiError(422, 'VALIDATION', 'expiresInDays must be an integer between 1 and 3650.');
      expiresAt = new Date(Date.now() + expiresInDays * 86400000).toISOString();
    }

    let target = null;
    if (typeof userId === 'string' && userId.trim()) {
      target = q1('SELECT id, is_guest FROM users WHERE id = ? AND deleted_at IS NULL', userId.trim());
      if (!target) throw new ApiError(404, 'NOT_FOUND', 'No user with that id.');
    } else if (typeof email === 'string' && email.trim()) {
      const norm = email.trim().toLowerCase();
      const rows = q(`SELECT DISTINCT u.id, u.is_guest FROM users u JOIN auth_identities ai ON ai.user_id = u.id
                      WHERE ai.email_normalized = ? AND u.deleted_at IS NULL`, norm);
      if (rows.length === 0) throw new ApiError(404, 'NOT_FOUND', 'No user with that email.');
      if (rows.length > 1) throw new ApiError(409, 'AMBIGUOUS', `That email matches ${rows.length} accounts — grant by userId instead.`);
      target = rows[0];
    } else {
      throw new ApiError(422, 'VALIDATION', 'Provide userId or email.');
    }

    // Double-submit protection: the panel sends a fresh key per click; a retry of
    // the same click returns the original grant instead of crediting twice.
    if (idempotencyKey) {
      const prior = q1('SELECT id, user_id, units FROM manual_grants WHERE idempotency_key = ?', idempotencyKey);
      if (prior) {
        if (prior.user_id !== target.id || prior.units !== units) throw new ApiError(409, 'IDEMPOTENCY_MISMATCH', 'That idempotency key was used for a different grant.');
        return { ok: true, replayed: true, userId: target.id, isGuest: !!target.is_guest, granted: units, bucketId: null, availableUnits: availableUnits(target.id), expiresAt };
      }
    }
    const grantId = uuid();
    const cleanNote = note ? note.trim().replace(/[\r\n\x00-\x1f\x7f]+/g, ' ') : null;
    // Credits and their audit row commit together, or not at all.
    const bucketId = tx(() => {
      const b = grantUnits(target.id, units, 'manual', grantId, expiresAt);
      run('INSERT INTO manual_grants (id, user_id, units, note, expires_at, idempotency_key, created_at) VALUES (?,?,?,?,?,?,?)',
        grantId, target.id, units, cleanNote, expiresAt, idempotencyKey || null, new Date().toISOString());
      return b;
    });
    console.log(`[admin] manual grant ${units} unit(s) → ${target.id} ${JSON.stringify(cleanNote || '')}`);
    return { ok: true, userId: target.id, isGuest: !!target.is_guest, granted: units, bucketId, availableUnits: availableUnits(target.id), expiresAt };
  });

  // 发送测试邮件：验证 SMTP 配置是否可用（换服务商后一键自检）。
  route('POST', '/v1/admin/email/test', async (ctx) => {
    requireAdmin(ctx);
    const to = String(ctx.body?.to || '').trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(to)) throw new ApiError(422, 'VALIDATION', '请输入有效的收件邮箱。');
    try {
      const r = await sendMail({ to, subject: 'MuseFrame 邮件配置测试', text: '这是一封来自 MuseFrame 管理后台的测试邮件，收到即说明 SMTP 配置正常。' });
      return { ok: true, accepted: r.accepted };
    } catch (e) {
      if (e.code === 'SMTP_NOT_CONFIGURED') throw new ApiError(400, 'SMTP_NOT_CONFIGURED', 'SMTP 未配置。');
      throw new ApiError(502, 'EMAIL_SEND_FAILED', '发送失败：' + e.message);
    }
  });

  // Explicit, token-gated image access for spot checks (spec §19.2: not shown by default).
  // Short-lived token for <img> URLs so the raw admin token stays out of URLs.
  route('GET', '/v1/admin/img-token', (ctx) => {
    requireAdmin(ctx);
    return { token: imgToken(Math.floor(Date.now() / 3600_000)), ttlSeconds: 3600 };
  });

  route('GET', '/v1/admin/assets/([\\w-]+)/file', (ctx) => {
    // Accept either the admin token (header/query) or a valid short-lived image
    // token — the latter is what <img> tags carry, so the real token never does.
    const it = ctx.url.searchParams.get('img_token');
    if (!(it && imgTokenValid(it))) requireAdmin(ctx);
    const asset = q1('SELECT * FROM assets WHERE id = ?', ctx.params[0]);
    if (!asset) throw new ApiError(404, 'NOT_FOUND', 'Unknown asset.');
    const file = path.join(ASSET_DIR, asset.storage_key);
    if (!existsSync(file)) throw new ApiError(404, 'NOT_FOUND', 'File missing.');
    ctx.res.writeHead(200, { 'Content-Type': asset.content_type, 'Cache-Control': 'private, max-age=300' });
    ctx.res.end(readFileSync(file));
    return null;
  });

  // ---- stats ------------------------------------------------------------

  route('GET', '/v1/admin/stats/daily', (ctx) => {
    requireAdmin(ctx);
    const days = Math.min(90, Math.max(1, Number(ctx.url.searchParams.get('days')) || 30));
    const today = new Date();
    const dates = [];
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - i));
      dates.push(d.toISOString().slice(0, 10));
    }
    const since = dates[0];

    const bucket = (rows) => { const m = {}; for (const r of rows) m[r.d] = r.n; return m; };
    const newUsers = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM users WHERE date(created_at) >= ? GROUP BY d`, since));
    const jobsCreated = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM generation_jobs WHERE date(created_at) >= ? GROUP BY d`, since));
    const jobsSucceeded = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM generation_jobs WHERE status='succeeded' AND date(created_at) >= ? GROUP BY d`, since));
    const jobsFailed = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM generation_jobs WHERE status='failed' AND date(created_at) >= ? GROUP BY d`, since));
    const unitsCommitted = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM credit_ledger WHERE entry_type='commit' AND date(created_at) >= ? GROUP BY d`, since));
    const revenueMinor = bucket(q(`SELECT date(purchased_at) AS d, COALESCE(SUM(amount_minor),0) AS n FROM purchases WHERE status='verified' AND date(purchased_at) >= ? GROUP BY d`, since));
    const feedbackPositive = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM user_feedback WHERE rating='positive' AND date(created_at) >= ? GROUP BY d`, since));
    const feedbackNegative = bucket(q(`SELECT date(created_at) AS d, COUNT(*) AS n FROM user_feedback WHERE rating='negative' AND date(created_at) >= ? GROUP BY d`, since));

    return {
      days: dates.map((d) => ({
        date: d,
        newUsers: newUsers[d] || 0,
        jobsCreated: jobsCreated[d] || 0,
        jobsSucceeded: jobsSucceeded[d] || 0,
        jobsFailed: jobsFailed[d] || 0,
        unitsCommitted: unitsCommitted[d] || 0,
        revenueMinor: revenueMinor[d] || 0,
        feedbackPositive: feedbackPositive[d] || 0,
        feedbackNegative: feedbackNegative[d] || 0,
      })),
    };
  });

  route('GET', '/v1/admin/stats/styles', (ctx) => {
    requireAdmin(ctx);
    const rows = q(`
      SELECT s.id, s.internal_key, s.public_name, s.theme, s.status, s.premium,
        COUNT(j.id) AS jobs,
        SUM(CASE WHEN j.status='succeeded' THEN 1 ELSE 0 END) AS succeeded,
        SUM(CASE WHEN j.status='failed' THEN 1 ELSE 0 END) AS failed,
        AVG(CASE WHEN j.status='succeeded' AND j.finished_at IS NOT NULL THEN (julianday(j.finished_at)-julianday(j.created_at))*86400 END) AS avg_s
      FROM styles s
      LEFT JOIN style_versions v ON v.style_id = s.id
      LEFT JOIN generation_jobs j ON j.style_version_id = v.id
      GROUP BY s.id
      ORDER BY jobs DESC`);

    const savesMap = {};
    for (const r of q(`
      SELECT s.id AS styleId, COUNT(*) AS n
      FROM events e
      JOIN generation_candidates c ON c.id = json_extract(e.props, '$.candidateId')
      JOIN generation_jobs j ON j.id = c.job_id
      JOIN style_versions v ON v.id = j.style_version_id
      JOIN styles s ON s.id = v.style_id
      WHERE e.name = 'result_saved'
      GROUP BY s.id`)) savesMap[r.styleId] = r.n;

    const fbPos = {}, fbNeg = {};
    for (const r of q(`
      SELECT s.id AS styleId, f.rating AS rating, COUNT(*) AS n
      FROM user_feedback f
      JOIN generation_candidates c ON c.id = f.candidate_id
      JOIN generation_jobs j ON j.id = c.job_id
      JOIN style_versions v ON v.id = j.style_version_id
      JOIN styles s ON s.id = v.style_id
      GROUP BY s.id, f.rating`)) {
      if (r.rating === 'positive') fbPos[r.styleId] = r.n;
      else if (r.rating === 'negative') fbNeg[r.styleId] = r.n;
    }

    return {
      styles: rows.map((r) => {
        const finished = (r.succeeded || 0) + (r.failed || 0);
        return {
          internalKey: r.internal_key,
          name: r.public_name,
          theme: r.theme,
          status: r.status,
          premium: !!r.premium,
          jobs: r.jobs || 0,
          succeeded: r.succeeded || 0,
          failed: r.failed || 0,
          successRate: finished > 0 ? (r.succeeded || 0) / finished : null,
          avgSeconds: r.avg_s ? Math.round(r.avg_s) : null,
          saves: savesMap[r.id] || 0,
          feedbackPositive: fbPos[r.id] || 0,
          feedbackNegative: fbNeg[r.id] || 0,
        };
      }),
    };
  });

  // ---- db explorer --------------------------------------------------------
  // Read-only visibility into the raw database for support/debugging. Table
  // names are always checked against sqlite_master before use in SQL — never
  // interpolate the URL param directly.

  route('GET', '/v1/admin/db/tables', (ctx) => {
    requireAdmin(ctx);
    const tables = q(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`);
    return { tables: tables.map((t) => ({ name: t.name, rows: q1(`SELECT COUNT(*) AS n FROM "${t.name}"`).n })) };
  });

  route('GET', '/v1/admin/db/table/([A-Za-z0-9_]+)', (ctx) => {
    requireAdmin(ctx);
    const name = ctx.params[0];
    const exists = q1(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, name);
    if (!exists) throw new ApiError(404, 'NOT_FOUND', 'Unknown table.');
    const limit = Math.min(200, Math.max(1, Number(ctx.url.searchParams.get('limit')) || 50));
    const offset = Math.max(0, Number(ctx.url.searchParams.get('offset')) || 0);
    const total = q1(`SELECT COUNT(*) AS n FROM "${name}"`).n;
    const columns = db.prepare(`PRAGMA table_info("${name}")`).all().map((c) => c.name);
    const rows = db.prepare(`SELECT * FROM "${name}" LIMIT ? OFFSET ?`).all(limit, offset);
    return { columns, rows: rows.map((r) => columns.map((c) => maskCell(name, r, c, r[c]))), total };
  });

  route('POST', '/v1/admin/db/query', (ctx) => {
    requireAdmin(ctx);
    const sql = String(ctx.body?.sql || '').trim();
    if (!/^(select|with)\b/i.test(sql)) throw new ApiError(422, 'VALIDATION', 'Only SELECT/WITH queries are allowed.');
    const withoutTrailingSemi = sql.replace(/;\s*$/, '');
    if (withoutTrailingSemi.includes(';')) throw new ApiError(422, 'VALIDATION', 'Only a single statement is allowed.');
    if (/\b(insert|update|delete|drop|alter|create|attach|pragma|vacuum|reindex|replace)\b/i.test(sql)) {
      throw new ApiError(422, 'VALIDATION', 'Read-only queries only.');
    }
    // Block runaway recursion (unbounded recursive CTE = single-thread DoS, M-1).
    if (/\brecursive\b/i.test(sql)) throw new ApiError(422, 'VALIDATION', 'Recursive queries are not allowed here.');
    // Credential/secret tables are off-limits to the freeform console (H-2).
    if (QUERY_DENY_TABLES.test(withoutTrailingSemi)) throw new ApiError(422, 'VALIDATION', 'This table is not queryable from the console.');
    // Hard row cap enforced inside SQLite (pipelined LIMIT stops early instead of
    // materializing everything, then slicing — bounds memory + CPU).
    const wrapped = `SELECT * FROM (${withoutTrailingSemi}) LIMIT 501`;
    let rows;
    try {
      rows = db.prepare(wrapped).all();
    } catch (e) {
      // Fall back to the raw query for statements a subquery can't wrap (rare);
      // still capped by slice below.
      try { rows = db.prepare(withoutTrailingSemi).all(); }
      catch { throw new ApiError(422, 'VALIDATION', e.message); }
    }
    const truncated = rows.length > 500;
    const limited = truncated ? rows.slice(0, 500) : rows;
    const columns = limited.length ? Object.keys(limited[0]) : [];
    return { columns, rows: limited.map((r) => columns.map((c) => r[c])), rowCount: limited.length, truncated };
  });

  // ---- runtime config ------------------------------------------------------

  // Operator-facing answer to "can this deployment generate right now, and with
  // what?". Names the missing settings so an unconfigured server is obvious in
  // the panel rather than looking healthy while quietly refusing every job.
  function generationSummary() {
    const gen = generationStatus();
    return {
      available: gen.available,
      mode: gen.mode,               // 'remote' | 'local' | 'none'
      provider: imageProvider(),    // env-only IMAGE_PROVIDER
      reason: gen.reason,
      missing: gen.missing,
      localFallback: !!cfg('local_engine_fallback'),
    };
  }

  // Anti-farming state: guest tokens are free to mint, so the free grant is
  // capped per device / per IP / site-wide. Surfaced so the operator can see how
  // close the ceilings are before the paid key goes back in.
  function abuseSummary() {
    const w = freeGrantWindow(null);
    const perIp = cfg('free_grants_per_ip_day');
    const perDay = cfg('free_grants_per_day');
    return {
      freeGrants24h: w.today,
      freeGrantIps24h: w.ips,
      perIpCap: perIp,
      perDayCap: perDay,
      capReached: perDay > 0 && w.today >= perDay,
      grantsDisabled: perDay <= 0 || perIp <= 0 || cfg('free_units') <= 0,
      guestAllowed: !!cfg('allow_guest'),
      freeRequiresAuth: !!cfg('free_requires_auth'),
      freeUnits: cfg('free_units'),
      // Dev faucets. Both now additionally require the admin token, but a flag
      // left on in production is still worth flagging.
      mockPurchases: process.env.ALLOW_MOCK_PURCHASES === 'true',
      testLogin: process.env.ALLOW_TEST_LOGIN === 'true',
    };
  }

  route('GET', '/v1/admin/config', (ctx) => {
    requireAdmin(ctx);
    const settings = listCfg();
    settings.push(
      {
        key: 'allow_mock_purchases', value: process.env.ALLOW_MOCK_PURCHASES === 'true', source: 'env', type: 'boolean',
        description: '演示购买开关（生产环境必须为 false）', secret: false, requiresRestart: true, readOnly: true,
      },
      {
        key: 'play_billing_configured', value: !!process.env.GOOGLE_SERVICE_ACCOUNT_JSON, source: 'env', type: 'boolean',
        description: 'Google Play 收据校验服务账号是否已配置', secret: false, requiresRestart: true, readOnly: true,
      },
    );
    return { settings, generation: generationSummary(), abuse: abuseSummary() };
  });

  route('PUT', '/v1/admin/config', (ctx) => {
    requireAdmin(ctx);
    const { key, value } = ctx.body || {};
    try {
      setCfg(key, value === undefined ? null : value);
    } catch (e) {
      throw new ApiError(422, 'VALIDATION', e.message);
    }
    return { ok: true, settings: listCfg(), generation: generationSummary(), abuse: abuseSummary() };
  });

  // ---- products -------------------------------------------------------------

  route('GET', '/v1/admin/products-admin', (ctx) => {
    requireAdmin(ctx);
    return {
      products: q('SELECT * FROM products ORDER BY product_type, price_minor').map((p) => ({
        id: p.id, internalKey: p.internal_key, productType: p.product_type, displayName: p.display_name,
        grantedUnits: p.granted_units, priceMinor: p.price_minor, priceCnyMinor: p.price_cny_minor ?? null, currency: p.currency, period: p.period,
        active: !!p.active, googleProductId: p.google_product_id, appleProductId: p.apple_product_id,
      })),
    };
  });

  route('PATCH', '/v1/admin/products-admin/([a-z0-9_]+)', (ctx) => {
    requireAdmin(ctx);
    const key = ctx.params[0];
    const product = q1('SELECT * FROM products WHERE internal_key = ?', key);
    if (!product) throw new ApiError(404, 'NOT_FOUND', 'Unknown product.');
    const { grantedUnits, priceMinor, priceCnyMinor, active } = ctx.body || {};
    if (grantedUnits !== undefined) {
      if (!Number.isInteger(grantedUnits) || grantedUnits < 0) throw new ApiError(422, 'VALIDATION', 'grantedUnits must be an integer >= 0.');
      run('UPDATE products SET granted_units = ? WHERE internal_key = ?', grantedUnits, key);
    }
    if (priceMinor !== undefined) {
      if (!Number.isInteger(priceMinor) || priceMinor < 0) throw new ApiError(422, 'VALIDATION', 'priceMinor must be an integer >= 0.');
      run('UPDATE products SET price_minor = ? WHERE internal_key = ?', priceMinor, key);
    }
    if (priceCnyMinor !== undefined) {
      if (priceCnyMinor !== null && (!Number.isInteger(priceCnyMinor) || priceCnyMinor < 0)) throw new ApiError(422, 'VALIDATION', 'priceCnyMinor must be an integer >= 0 or null.');
      run('UPDATE products SET price_cny_minor = ? WHERE internal_key = ?', priceCnyMinor, key);
    }
    if (active !== undefined) {
      if (typeof active !== 'boolean') throw new ApiError(422, 'VALIDATION', 'active must be a boolean.');
      run('UPDATE products SET active = ? WHERE internal_key = ?', active ? 1 : 0, key);
    }
    return { ok: true };
  });

  // ---- styles -----------------------------------------------------------

  route('GET', '/v1/admin/styles-admin', (ctx) => {
    requireAdmin(ctx);
    return {
      styles: q(`
        SELECT s.id, s.internal_key, s.public_name, s.theme, s.status, s.premium,
          (SELECT COUNT(*) FROM generation_jobs j JOIN style_versions v ON v.id = j.style_version_id WHERE v.style_id = s.id) AS jobs
        FROM styles s
        ORDER BY s.public_name`).map((r) => ({
        id: r.id, internalKey: r.internal_key, name: r.public_name, theme: r.theme,
        status: r.status, premium: !!r.premium, jobs: r.jobs,
      })),
    };
  });

  // Emergency takedown / republish (spec §19.4). Catalog queries already filter
  // status='published', so 'disabled' immediately hides the style everywhere.
  route('POST', '/v1/admin/styles-admin/([\\w-]+)/status', (ctx) => {
    requireAdmin(ctx);
    const { status } = ctx.body || {};
    if (!['published', 'disabled'].includes(status)) throw new ApiError(422, 'VALIDATION', "status must be 'published' or 'disabled'.");
    const style = q1('SELECT id FROM styles WHERE id = ?', ctx.params[0]);
    if (!style) throw new ApiError(404, 'NOT_FOUND', 'Unknown style.');
    run('UPDATE styles SET status = ? WHERE id = ?', status, style.id);
    return { ok: true };
  });
}
