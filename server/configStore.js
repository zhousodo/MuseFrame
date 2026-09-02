// Runtime config store (admin panel — spec §19). Lets the operator override a
// small set of env-backed settings from the admin UI without a restart: every
// consumer reads through cfg() at call time, never caching the value. DB
// override > env var > built-in default. Only imports db.js — never api.js,
// verify.js, or the engine adapters — to keep the import graph acyclic.
import { db, q, run, now } from './db.js';

db.exec(`
CREATE TABLE IF NOT EXISTS app_config (
  key TEXT PRIMARY KEY,
  value TEXT,
  updated_at TEXT
);
`);

// key -> { type, envVar, default, description, secret, requiresRestart }
// requiresRestart is false throughout: every consumer of these settings reads
// live (cfg() at call time / getters), so a DB override takes effect on the
// next request. It's kept in the schema for settings that may need it later.
const REGISTRY = {
  // 2026-09 收费模型：邮箱注册后送 3 张；用完提示联系客服邮箱，由管理员在后台手动加额度。
  free_units: { type: 'number', envVar: 'FREE_UNITS', default: 3, description: '注册用户免费生成张数' },
  allow_guest: { type: 'boolean', envVar: 'ALLOW_GUEST', default: true, description: '允许游客浏览（游客不发免费额度时仍可看画廊）' },
  free_requires_auth: { type: 'boolean', envVar: 'FREE_REQUIRES_AUTH', default: true, description: '免费额度需登录（邮箱注册）后发放' },
  support_email: { type: 'string', envVar: 'SUPPORT_EMAIL', default: 'donaldkuke@gmail.com', description: '客服 / 加购联系邮箱（额度用完时展示给用户）' },
  // 反白嫖：游客可无凭据换令牌，若不设上限就能循环建号无限领免费额度。
  // 两道闸都按滚动 24 小时统计；设为 0 = 完全停发免费额度。
  free_grants_per_ip_day: { type: 'number', envVar: 'FREE_GRANTS_PER_IP_DAY', default: 3, description: '每个 IP 每 24 小时最多发放几次免费额度（0=停发）' },
  free_grants_per_day: { type: 'number', envVar: 'FREE_GRANTS_PER_DAY', default: 50, description: '全站每 24 小时免费额度发放次数上限（0=停发）' },
  image_provider_base_url: { type: 'string', envVar: 'IMAGE_PROVIDER_BASE_URL', default: '', description: '图像模型接口地址' },
  image_provider_api_key: { type: 'string', envVar: 'IMAGE_PROVIDER_API_KEY', default: '', description: '图像模型 API 密钥', secret: true },
  image_provider_model: { type: 'string', envVar: 'IMAGE_PROVIDER_MODEL', default: 'gpt-image-2', description: '图像模型名称' },
  prompt_compiler_model: { type: 'string', envVar: 'PROMPT_COMPILER_MODEL', default: 'gpt-5.4-mini', description: '提示词编译模型' },
  // 模型档位（2026-09）：standard 档给免费与点数包用户，high 档给 Creator（highResolution 权益）。
  // 值是 OpenAI images/edits 的 quality 参数：low / medium / high / auto。成本差约 4 倍。
  image_quality_standard: { type: 'string', envVar: 'IMAGE_QUALITY_STANDARD', default: 'medium', description: '标准档输出质量（low/medium/high/auto）' },
  image_quality_high: { type: 'string', envVar: 'IMAGE_QUALITY_HIGH', default: 'high', description: '高清档输出质量（low/medium/high/auto）' },
  image_provider_model_high: { type: 'string', envVar: 'IMAGE_PROVIDER_MODEL_HIGH', default: '', description: '高清档使用的模型（留空 = 与标准档相同）' },
  image_provider_timeout_ms: { type: 'number', envVar: 'IMAGE_PROVIDER_TIMEOUT_MS', default: 420000, description: '图像模型请求超时时间（毫秒）' },
  // 关闭时（默认）：远程生成失败即失败退款，绝不用本地像素引擎冒充模型结果。
  local_engine_fallback: { type: 'boolean', envVar: 'LOCAL_ENGINE_FALLBACK', default: false, description: '远程生成失败时回落本地像素引擎（默认关闭：回落产出的不是模型结果）' },
  worker_concurrency: { type: 'number', envVar: 'WORKER_CONCURRENCY', default: 3, description: '生成任务并发数' },
  google_client_ids: { type: 'string', envVar: 'GOOGLE_CLIENT_IDS', default: '', description: 'Google 登录 OAuth Client IDs(逗号分隔)' },
  apple_bundle_ids: { type: 'string', envVar: 'APPLE_BUNDLE_IDS', default: '', description: 'Sign in with Apple Bundle IDs(逗号分隔)' },
  google_package_name: { type: 'string', envVar: 'GOOGLE_PACKAGE_NAME', default: 'com.museframe.app', description: 'Google Play 包名' },
  // 邮件（SMTP）— 支持邮箱验证码登录，可随时在后台切换服务商
  smtp_host: { type: 'string', envVar: 'SMTP_HOST', default: '', description: 'SMTP 服务器地址' },
  smtp_port: { type: 'number', envVar: 'SMTP_PORT', default: 587, description: 'SMTP 端口' },
  smtp_user: { type: 'string', envVar: 'SMTP_USER', default: '', description: 'SMTP 登录用户名' },
  smtp_pass: { type: 'string', envVar: 'SMTP_PASS', default: '', description: 'SMTP 登录密码/密钥', secret: true },
  smtp_from: { type: 'string', envVar: 'SMTP_FROM', default: 'MuseFrame <no-reply@lenscript.cn>', description: '发件人地址' },
  email_login_enabled: { type: 'boolean', envVar: 'EMAIL_LOGIN_ENABLED', default: false, description: '开启邮箱验证码登录' },
};

// In-memory cache of DB overrides, loaded once at boot and kept in sync by setCfg.
let cache = new Map();
(function loadCache() {
  cache = new Map();
  for (const row of q('SELECT key, value FROM app_config')) cache.set(row.key, row.value);
})();

function coerce(type, raw) {
  if (type === 'number') return Number(raw);
  if (type === 'boolean') return raw === true || raw === 'true';
  return raw == null ? raw : String(raw);
}

/** Effective typed value for `key`: DB override, else env var if set, else default. */
export function cfg(key) {
  const def = REGISTRY[key];
  if (!def) throw new Error(`Unknown config key: ${key}`);
  if (cache.has(key)) {
    const raw = cache.get(key);
    if (raw !== null && raw !== undefined) return coerce(def.type, raw);
  }
  const envVal = def.envVar ? process.env[def.envVar] : undefined;
  if (envVal !== undefined) return coerce(def.type, envVal);
  return def.default;
}

/** Set (or, with null, clear) the DB override for `key`. Validates against the registry. */
export function setCfg(key, valueOrNull) {
  const def = REGISTRY[key];
  if (!def) { const e = new Error(`Unknown config key: ${key}`); throw e; }

  if (valueOrNull === null || valueOrNull === undefined) {
    run('DELETE FROM app_config WHERE key = ?', key);
    cache.delete(key);
    return;
  }

  let stored;
  if (def.type === 'number') {
    const n = Number(valueOrNull);
    if (!Number.isFinite(n)) throw new Error(`${key} must be a number`);
    stored = String(n);
  } else if (def.type === 'boolean') {
    if (typeof valueOrNull === 'boolean') stored = valueOrNull ? 'true' : 'false';
    else if (valueOrNull === 'true' || valueOrNull === 'false') stored = valueOrNull;
    else throw new Error(`${key} must be a boolean`);
  } else {
    stored = String(valueOrNull);
  }

  const t = now();
  run(`INSERT INTO app_config (key, value, updated_at) VALUES (?,?,?)
       ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, stored, t);
  cache.set(key, stored);
}

function maskSecret(value) {
  if (!value) return null;
  const s = String(value);
  return '••••' + s.slice(-4);
}

/** Registry keys whose stored value is a secret (never expose in cleartext). */
export const SECRET_KEYS = Object.entries(REGISTRY).filter(([, d]) => d.secret).map(([k]) => k);

/** All known settings with their effective value (secrets masked) and provenance. */
export function listCfg() {
  return Object.entries(REGISTRY).map(([key, def]) => {
    const rawSet = cache.has(key) && cache.get(key) !== null && cache.get(key) !== undefined;
    const source = rawSet ? 'db' : (def.envVar && process.env[def.envVar] !== undefined) ? 'env' : 'default';
    let value = cfg(key);
    if (def.secret) value = maskSecret(value);
    return {
      key, value, source, type: def.type,
      description: def.description || '',
      secret: !!def.secret,
      requiresRestart: !!def.requiresRestart,
      rawSet,
    };
  });
}
