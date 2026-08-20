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
  free_units: { type: 'number', envVar: 'FREE_UNITS', default: 1, description: '新用户免费生成张数' },
  allow_guest: { type: 'boolean', envVar: 'ALLOW_GUEST', default: true, description: '允许游客使用' },
  free_requires_auth: { type: 'boolean', envVar: 'FREE_REQUIRES_AUTH', default: false, description: '免费额度需登录后发放' },
  image_provider_base_url: { type: 'string', envVar: 'IMAGE_PROVIDER_BASE_URL', default: '', description: '图像模型接口地址' },
  image_provider_api_key: { type: 'string', envVar: 'IMAGE_PROVIDER_API_KEY', default: '', description: '图像模型 API 密钥', secret: true },
  image_provider_model: { type: 'string', envVar: 'IMAGE_PROVIDER_MODEL', default: 'gpt-image-2', description: '图像模型名称' },
  prompt_compiler_model: { type: 'string', envVar: 'PROMPT_COMPILER_MODEL', default: 'gpt-5.4-mini', description: '提示词编译模型' },
  image_provider_timeout_ms: { type: 'number', envVar: 'IMAGE_PROVIDER_TIMEOUT_MS', default: 420000, description: '图像模型请求超时时间（毫秒）' },
  worker_concurrency: { type: 'number', envVar: 'WORKER_CONCURRENCY', default: 3, description: '生成任务并发数' },
  google_client_ids: { type: 'string', envVar: 'GOOGLE_CLIENT_IDS', default: '', description: 'Google 登录 OAuth Client IDs(逗号分隔)' },
  apple_bundle_ids: { type: 'string', envVar: 'APPLE_BUNDLE_IDS', default: '', description: 'Sign in with Apple Bundle IDs(逗号分隔)' },
  google_package_name: { type: 'string', envVar: 'GOOGLE_PACKAGE_NAME', default: 'com.museframe.app', description: 'Google Play 包名' },
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
