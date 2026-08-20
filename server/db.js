// MuseFrame — persistence layer (spec §12, adapted from PostgreSQL DDL to node:sqlite).
// UUIDs everywhere, soft deletes on user content, append-only credit ledger.
import { DatabaseSync } from 'node:sqlite';
import { randomUUID } from 'node:crypto';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
export const DATA_DIR = path.join(ROOT, 'data');
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
  source_type TEXT NOT NULL,        -- free_grant | purchase | promo
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

export const uuid = () => randomUUID();
export const now = () => new Date().toISOString();

// Tiny helpers — node:sqlite prepared statements each call (cheap enough at MVP scale).
export function q(sql, ...params) { return db.prepare(sql).all(...params); }
export function q1(sql, ...params) { return db.prepare(sql).get(...params); }
export function run(sql, ...params) { return db.prepare(sql).run(...params); }
export function tx(fn) {
  db.exec('BEGIN IMMEDIATE');
  try { const r = fn(); db.exec('COMMIT'); return r; }
  catch (e) { db.exec('ROLLBACK'); throw e; }
}
