// MuseFrame — persistence layer (spec §12, adapted from PostgreSQL DDL to node:sqlite).
// UUIDs everywhere, soft deletes on user content, append-only credit ledger.
import { DatabaseSync } from 'node:sqlite';
import { randomUUID, randomBytes } from 'node:crypto';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
// MUSEFRAME_DATA_DIR lets the automated tests point the whole persistence layer
// at a throwaway directory; production leaves it unset and uses ./data.
export const DATA_DIR = process.env.MUSEFRAME_DATA_DIR
  ? path.resolve(process.env.MUSEFRAME_DATA_DIR)
  : path.join(ROOT, 'data');
export const ASSET_DIR = path.join(DATA_DIR, 'assets');
mkdirSync(ASSET_DIR, { recursive: true });

export const db = new DatabaseSync(path.join(DATA_DIR, 'museframe.db'));
db.exec('PRAGMA journal_mode = WAL');
db.exec('PRAGMA foreign_keys = ON');

db.exec(`
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'active',
  is_guest INTEGER NOT NULL DEFAULT 1,
  display_name TEXT,
  locale TEXT NOT NULL DEFAULT 'en',
  timezone TEXT NOT NULL DEFAULT 'UTC',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS auth_identities (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  provider_subject TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (provider, provider_subject)
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id TEXT,
  created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title TEXT,
  source_asset_id TEXT,
  selected_candidate_id TEXT,
  status TEXT NOT NULL DEFAULT 'draft',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE INDEX IF NOT EXISTS projects_user_updated_idx ON projects(user_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS assets (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id TEXT,
  kind TEXT NOT NULL,               -- source | candidate | thumbnail | export
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | ready | quarantined | deleted
  storage_key TEXT NOT NULL UNIQUE,
  content_type TEXT NOT NULL,
  byte_size INTEGER,
  width INTEGER,
  height INTEGER,
  sha256 TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE INDEX IF NOT EXISTS assets_project_idx ON assets(project_id);
CREATE TABLE IF NOT EXISTS photo_analyses (
  id TEXT PRIMARY KEY,
  asset_id TEXT NOT NULL UNIQUE REFERENCES assets(id) ON DELETE CASCADE,
  analyzer_version TEXT NOT NULL,
  status TEXT NOT NULL,             -- pending | ready | failed
  subject_type TEXT,
  person_count INTEGER,
  sharpness REAL,
  exposure REAL,
  warnings TEXT NOT NULL DEFAULT '[]',
  recommendations TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS styles (
  id TEXT PRIMARY KEY,
  internal_key TEXT NOT NULL UNIQUE,
  slug TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'published',
  theme TEXT NOT NULL,
  premium INTEGER NOT NULL DEFAULT 0,
  public_name TEXT NOT NULL,
  short_caption TEXT NOT NULL,
  suitability_tags TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS style_versions (
  id TEXT PRIMARY KEY,
  style_id TEXT NOT NULL REFERENCES styles(id),
  version INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'published',
  spec TEXT NOT NULL,               -- immutable StyleSpec JSON
  published_at TEXT,
  created_at TEXT NOT NULL,
  UNIQUE (style_id, version)
);
CREATE TABLE IF NOT EXISTS exhibitions (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  curatorial_note TEXT NOT NULL,
  edition TEXT NOT NULL,
  editorial_rank INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'published',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS exhibition_styles (
  exhibition_id TEXT NOT NULL REFERENCES exhibitions(id) ON DELETE CASCADE,
  style_id TEXT NOT NULL REFERENCES styles(id),
  position INTEGER NOT NULL,
  PRIMARY KEY (exhibition_id, style_id)
);
CREATE TABLE IF NOT EXISTS generation_jobs (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  project_id TEXT NOT NULL REFERENCES projects(id),
  source_asset_id TEXT NOT NULL REFERENCES assets(id),
  style_version_id TEXT NOT NULL REFERENCES style_versions(id),
  parent_job_id TEXT,
  status TEXT NOT NULL DEFAULT 'created', -- created|queued|running|quality_check|succeeded|failed|cancelled
  stage TEXT NOT NULL DEFAULT 'preparing',
  controls TEXT NOT NULL,           -- {strength, fidelity, composition}
  output TEXT NOT NULL,             -- {aspectRatio, qualityTier}
  attempt_count INTEGER NOT NULL DEFAULT 0,
  reserved_units INTEGER NOT NULL DEFAULT 0,
  error_code TEXT,
  cost_minor INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS jobs_user_idx ON generation_jobs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS jobs_status_idx ON generation_jobs(status);
CREATE TABLE IF NOT EXISTS generation_candidates (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
  candidate_index INTEGER NOT NULL DEFAULT 0,
  asset_id TEXT NOT NULL REFERENCES assets(id),
  quality_passed INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  UNIQUE (job_id, candidate_index)
);
CREATE TABLE IF NOT EXISTS products (
  id TEXT PRIMARY KEY,
  internal_key TEXT NOT NULL UNIQUE,
  product_type TEXT NOT NULL,       -- subscription | pack
  display_name TEXT NOT NULL,
  granted_units INTEGER NOT NULL DEFAULT 0,
  price_minor INTEGER NOT NULL,
  currency TEXT NOT NULL DEFAULT 'USD',
  period TEXT,                      -- month | year | null
  feature_flags TEXT NOT NULL DEFAULT '{}',
  active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS purchases (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  product_id TEXT NOT NULL REFERENCES products(id),
  platform TEXT NOT NULL,
  external_transaction_id TEXT NOT NULL,
  status TEXT NOT NULL,
  amount_minor INTEGER,
  currency TEXT,
  purchased_at TEXT NOT NULL,
  expires_at TEXT,
  created_at TEXT NOT NULL,
  UNIQUE (platform, external_transaction_id)
);
CREATE TABLE IF NOT EXISTS credit_buckets (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  source_type TEXT NOT NULL,        -- free_grant | purchase | promo | manual
  source_id TEXT,
  granted_units INTEGER NOT NULL CHECK (granted_units > 0),
  expires_at TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS buckets_user_idx ON credit_buckets(user_id, expires_at, created_at);
CREATE TABLE IF NOT EXISTS credit_ledger (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  entry_type TEXT NOT NULL,         -- grant|reserve|commit|release|expire|refund|adjustment
  units INTEGER NOT NULL,
  balance_bucket_id TEXT NOT NULL REFERENCES credit_buckets(id),
  job_id TEXT,
  purchase_id TEXT,
  reference_key TEXT NOT NULL,
  created_at TEXT NOT NULL,
  CHECK ((entry_type = 'commit' AND units = 0) OR (entry_type <> 'commit' AND units <> 0)),
  UNIQUE (user_id, reference_key)
);
CREATE INDEX IF NOT EXISTS ledger_bucket_idx ON credit_ledger(user_id, balance_bucket_id);
CREATE TABLE IF NOT EXISTS user_feedback (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  candidate_id TEXT,
  rating TEXT NOT NULL,
  reason_codes TEXT NOT NULL DEFAULT '[]',
  comment TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS idempotency_records (
  user_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_status INTEGER,
  response_body TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY (user_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  user_id TEXT,
  name TEXT NOT NULL,
  props TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT NOT NULL
);
`);

// Lightweight migrations for columns added after first ship.
function ensureColumn(table, col, ddl) {
  const cols = db.prepare(`PRAGMA table_info(${table})`).all().map((c) => c.name);
  if (!cols.includes(col)) db.exec(`ALTER TABLE ${table} ADD COLUMN ${ddl}`);
}
ensureColumn('products', 'google_product_id', 'google_product_id TEXT');
ensureColumn('products', 'apple_product_id', 'apple_product_id TEXT');
// 中文站/中文用户看人民币价，英文看美元价：两套价格分别定，不做汇率换算（2026-09）。
ensureColumn('products', 'price_cny_minor', 'price_cny_minor INTEGER');
// auth_identities.email_normalized is written on Google/Apple sign-in but was
// missing from the original CREATE — without this a real sign-in throws.
ensureColumn('auth_identities', 'email_normalized', 'email_normalized TEXT');
// Sessions used to live forever (no expiry column, last_seen_at never touched),
// so a token leaked through a log line was a permanent credential.
ensureColumn('sessions', 'expires_at', 'expires_at TEXT');

// Email verification codes for passwordless email login.
db.exec(`CREATE TABLE IF NOT EXISTS email_codes (
  email TEXT PRIMARY KEY,
  code_hash TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
)`);
// Per-address issuance counter: re-requesting a code resets `attempts`, so the
// only sticky brake on guessing has to count *requests* per address.
ensureColumn('email_codes', 'issue_count', 'issue_count INTEGER NOT NULL DEFAULT 0');
ensureColumn('email_codes', 'window_start', 'window_start TEXT');

// Free-grant audit log. The credit ledger alone can't answer "how many free
// images has this IP claimed today?" — and without that the guest loop
// (POST /v1/auth/exchange with no credentials → 1 free image → repeat) is
// unbounded. One row per granted free bucket; raw IPs are never stored.
db.exec(`CREATE TABLE IF NOT EXISTS free_grants (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  device_hash TEXT,
  ip_hash TEXT,
  units INTEGER NOT NULL,
  created_at TEXT NOT NULL
)`);
db.exec('CREATE INDEX IF NOT EXISTS idx_free_grants_ip ON free_grants (ip_hash, created_at)');
// 管理员手动加额度的审计记录（2026-09 收费模型：用完 → 邮件联系 → 后台充值）。
// 真正的余额仍只在 credit_buckets / credit_ledger（source_type='manual'）；这里只多存备注。
db.exec(`CREATE TABLE IF NOT EXISTS manual_grants (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  units INTEGER NOT NULL,
  note TEXT,
  expires_at TEXT,
  created_at TEXT NOT NULL
)`);
db.exec('CREATE INDEX IF NOT EXISTS idx_manual_grants_user ON manual_grants (user_id, created_at)');
db.exec('CREATE INDEX IF NOT EXISTS idx_free_grants_at ON free_grants (created_at)');

// Hot-path indexes. Every one of these covered a query that was a full table
// scan on the event loop: the projects list (one scan of generation_jobs per
// project), every balance read (scan of credit_ledger per bucket), userPlan()
// on nearly every catalog request, and both free-grant dedupe probes.
// idx_free_grants_dedupe is deliberately NOT unique: a DB that already ran the
// double-grant bug carries duplicate dedupe keys and a unique index would fail
// to build at boot. The code enforces the invariant; see maybeGrantFree().
db.exec(`
CREATE INDEX IF NOT EXISTS jobs_project_created_idx ON generation_jobs(project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ledger_bucket_only_idx ON credit_ledger(balance_bucket_id);
CREATE INDEX IF NOT EXISTS ledger_refkey_idx ON credit_ledger(reference_key);
CREATE INDEX IF NOT EXISTS purchases_user_idx ON purchases(user_id, purchased_at DESC);
CREATE INDEX IF NOT EXISTS purchases_status_idx ON purchases(status);
CREATE INDEX IF NOT EXISTS idx_free_grants_dedupe ON free_grants(dedupe_key);
CREATE INDEX IF NOT EXISTS events_occurred_idx ON events(occurred_at);
CREATE INDEX IF NOT EXISTS events_name_at_idx ON events(name, occurred_at);
CREATE INDEX IF NOT EXISTS jobs_ledger_job_idx ON credit_ledger(job_id, entry_type);
CREATE INDEX IF NOT EXISTS assets_user_status_idx ON assets(user_id, status);
CREATE INDEX IF NOT EXISTS feedback_user_candidate_idx ON user_feedback(user_id, candidate_id);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idempotency_created_idx ON idempotency_records(created_at);
`);

// Server-side secrets that must survive a restart but must never be operator
// input: the asset image-token HMAC key and the free-grant IP salt. Previously
// both were derived from ADMIN_TOKEN, which defaults to '' — an empty HMAC key
// anyone can compute, and a constant public salt over the whole IPv4 space.
db.exec(`CREATE TABLE IF NOT EXISTS server_secrets (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  created_at TEXT NOT NULL
)`);
export function serverSecret(key) {
  const row = db.prepare('SELECT value FROM server_secrets WHERE key = ?').get(key);
  if (row) return row.value;
  db.prepare('INSERT OR IGNORE INTO server_secrets (key, value, created_at) VALUES (?,?,?)')
    .run(key, randomBytes(32).toString('base64url'), new Date().toISOString());
  return db.prepare('SELECT value FROM server_secrets WHERE key = ?').get(key).value;
}

/**
 * Retention sweep. `events` had no ceiling and no expiry — an ingest flood, or
 * simply a year of ordinary traffic, grew the file forever on a 2 GB box shared
 * with the LensCript production stack, and every admin count over it is a scan.
 * Expired sessions and spent idempotency records are dropped on the same pass.
 * Cheap enough to run at boot and once a day; safe to call concurrently.
 */
export function pruneOldRows({
  eventDays = Number(process.env.EVENT_RETENTION_DAYS) || 90,
  idempotencyDays = 30,
} = {}) {
  const cutoff = (d) => new Date(Date.now() - d * 86400_000).toISOString();
  const t = new Date().toISOString();
  const out = {
    events: db.prepare('DELETE FROM events WHERE occurred_at < ?').run(cutoff(eventDays)).changes,
    sessions: db.prepare('DELETE FROM sessions WHERE expires_at IS NOT NULL AND expires_at < ?').run(t).changes,
    idempotency: db.prepare('DELETE FROM idempotency_records WHERE created_at < ?').run(cutoff(idempotencyDays)).changes,
    emailCodes: db.prepare('DELETE FROM email_codes WHERE expires_at < ?').run(cutoff(1)).changes,
  };
  try { db.exec('PRAGMA optimize'); } catch { /* advisory only */ }
  return out;
}

export const uuid = () => randomUUID();
export const now = () => new Date().toISOString();

// Tiny helpers — node:sqlite prepared statements each call (cheap enough at MVP scale).
export function q(sql, ...params) { return db.prepare(sql).all(...params); }
export function q1(sql, ...params) { return db.prepare(sql).get(...params); }
export function run(sql, ...params) { return db.prepare(sql).run(...params); }
// Re-entrant: an inner tx() joins the transaction its caller already opened
// instead of throwing "cannot start a transaction within a transaction". That
// is what lets a purchase INSERT and its grantUnits() run as one unit — before,
// they committed separately and a failed grant left the money taken.
let txDepth = 0;
export function tx(fn) {
  if (txDepth > 0) return fn();
  db.exec('BEGIN IMMEDIATE');
  txDepth++;
  try { const r = fn(); db.exec('COMMIT'); return r; }
  catch (e) { db.exec('ROLLBACK'); throw e; }
  finally { txDepth--; }
}
