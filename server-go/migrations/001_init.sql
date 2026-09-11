-- MuseFrame（留影）PostgreSQL 初始 schema —— 24 张业务表 / 58 个索引 / 2 个 CHECK。
--
-- 执行者：museframe_owner（迁移角色）。运行角色 museframe_app 无 DDL 权限，
-- 因此本文件由一次性 job 执行，**绝不在应用启动时自动建表**。
--
-- 索引 58 的构成（与 SQLite 侧逐个对齐）：
--   24 个主键索引（PostgreSQL 的 PRIMARY KEY 自动建）
-- + 11 个唯一约束索引（UNIQUE 自动建）
-- + 23 个显式具名索引（本文件末尾）
-- = 58
--
-- 类型映射（依据 platform/migrations/museframe/config.json 与盘点 A3）：
--   ISO-8601 UTC 文本   -> timestamptz（时区一律 UTC，**不是** paida 的 UTC+8）
--   INTEGER 0/1 布尔    -> boolean
--   TEXT 里的 JSON      -> jsonb
--   REAL                -> double precision
--   TEXT 主键（UUIDv4） -> text（保持 text，不转 uuid：storage_key 等处按字符串拼接，
--                          且源库里没有 AUTOINCREMENT / sqlite_sequence，无序列问题）

BEGIN;

CREATE TABLE users (
  id           text PRIMARY KEY,
  status       text NOT NULL DEFAULT 'active',
  is_guest     boolean NOT NULL DEFAULT true,
  display_name text,
  locale       text NOT NULL DEFAULT 'en',
  timezone     text NOT NULL DEFAULT 'UTC',
  created_at   timestamptz NOT NULL,
  updated_at   timestamptz NOT NULL,
  deleted_at   timestamptz
);

CREATE TABLE auth_identities (
  id               text PRIMARY KEY,
  user_id          text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider         text NOT NULL,
  provider_subject text NOT NULL,
  created_at       timestamptz NOT NULL,
  email_normalized text,
  UNIQUE (provider, provider_subject)
);

CREATE TABLE sessions (
  token        text PRIMARY KEY,
  user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id    text,
  created_at   timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  expires_at   timestamptz
);

CREATE TABLE projects (
  id                    text PRIMARY KEY,
  user_id               text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title                 text,
  source_asset_id       text,
  selected_candidate_id text,
  status                text NOT NULL DEFAULT 'draft',
  created_at            timestamptz NOT NULL,
  updated_at            timestamptz NOT NULL,
  deleted_at            timestamptz
);

CREATE TABLE assets (
  id           text PRIMARY KEY,
  user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id   text,
  kind         text NOT NULL,                      -- source | candidate | thumbnail | export
  status       text NOT NULL DEFAULT 'pending',    -- pending | ready | quarantined | deleted
  storage_key  text NOT NULL UNIQUE,
  content_type text NOT NULL,
  byte_size    bigint,
  width        integer,
  height       integer,
  sha256       text,                               -- 源库全为 NULL，由资产迁移工具现算回填
  created_at   timestamptz NOT NULL,
  updated_at   timestamptz NOT NULL,
  deleted_at   timestamptz
);

CREATE TABLE photo_analyses (
  id               text PRIMARY KEY,
  asset_id         text NOT NULL UNIQUE REFERENCES assets(id) ON DELETE CASCADE,
  analyzer_version text NOT NULL,
  status           text NOT NULL,                  -- pending | ready | failed
  subject_type     text,
  person_count     integer,
  sharpness        double precision,
  exposure         double precision,
  warnings         jsonb NOT NULL DEFAULT '[]'::jsonb,
  recommendations  jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL
);

CREATE TABLE styles (
  id               text PRIMARY KEY,
  internal_key     text NOT NULL UNIQUE,
  slug             text NOT NULL UNIQUE,
  status           text NOT NULL DEFAULT 'published',
  theme            text NOT NULL,
  premium          boolean NOT NULL DEFAULT false,
  public_name      text NOT NULL,
  short_caption    text NOT NULL,
  suitability_tags jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at       timestamptz NOT NULL
);

CREATE TABLE style_versions (
  id           text PRIMARY KEY,
  style_id     text NOT NULL REFERENCES styles(id),
  version      integer NOT NULL,
  status       text NOT NULL DEFAULT 'published',
  spec         jsonb NOT NULL,                     -- 不可变 StyleSpec
  published_at timestamptz,
  created_at   timestamptz NOT NULL,
  UNIQUE (style_id, version)
);

CREATE TABLE exhibitions (
  id              text PRIMARY KEY,
  slug            text NOT NULL UNIQUE,
  title           text NOT NULL,
  curatorial_note text NOT NULL,
  edition         text NOT NULL,
  editorial_rank  integer NOT NULL DEFAULT 0,
  status          text NOT NULL DEFAULT 'published',
  created_at      timestamptz NOT NULL
);

CREATE TABLE exhibition_styles (
  exhibition_id text NOT NULL REFERENCES exhibitions(id) ON DELETE CASCADE,
  style_id      text NOT NULL REFERENCES styles(id),
  position      integer NOT NULL,
  PRIMARY KEY (exhibition_id, style_id)
);

CREATE TABLE generation_jobs (
  id               text PRIMARY KEY,
  user_id          text NOT NULL REFERENCES users(id),
  project_id       text NOT NULL REFERENCES projects(id),
  source_asset_id  text NOT NULL REFERENCES assets(id),
  style_version_id text NOT NULL REFERENCES style_versions(id),
  parent_job_id    text,
  status           text NOT NULL DEFAULT 'created', -- created|queued|running|quality_check|succeeded|failed|cancelled
  stage            text NOT NULL DEFAULT 'preparing', -- preparing|building|making|checking|complete|failed
  controls         jsonb NOT NULL,                  -- {strength, fidelity, composition}
  output           jsonb NOT NULL,                  -- {aspectRatio, qualityTier}
  attempt_count    integer NOT NULL DEFAULT 0,
  reserved_units   integer NOT NULL DEFAULT 0,
  error_code       text,
  cost_minor       bigint NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  finished_at      timestamptz
);

CREATE TABLE generation_candidates (
  id              text PRIMARY KEY,
  job_id          text NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
  candidate_index integer NOT NULL DEFAULT 0,
  asset_id        text NOT NULL REFERENCES assets(id),
  quality_passed  boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL,
  UNIQUE (job_id, candidate_index)
);

-- 🔴 价格红线（D-14 四重确认）：
--   price_minor     = 美分（minor unit），NOT NULL，出参字段名 priceMinor，JSON 整数
--   price_cny_minor = 人民币分，可为 NULL，出参字段名 priceCnyMinor，JSON 整数或 null
--   currency 恒为 'USD'，且**只描述 price_minor**；price_cny_minor 的币种是隐含 CNY，
--   没有对应列。任何「按 currency 统一换算」的写法都会把人民币价当成美元算。
CREATE TABLE products (
  id                text PRIMARY KEY,
  internal_key      text NOT NULL UNIQUE,
  product_type      text NOT NULL,                  -- subscription | pack
  display_name      text NOT NULL,
  granted_units     integer NOT NULL DEFAULT 0,
  price_minor       bigint NOT NULL,
  currency          text NOT NULL DEFAULT 'USD',
  period            text,                           -- month | year | null
  feature_flags     jsonb NOT NULL DEFAULT '{}'::jsonb,
  active            boolean NOT NULL DEFAULT true,
  google_product_id text,
  apple_product_id  text,
  price_cny_minor   bigint
);

CREATE TABLE purchases (
  id                      text PRIMARY KEY,
  user_id                 text NOT NULL REFERENCES users(id),
  product_id              text NOT NULL REFERENCES products(id),
  platform                text NOT NULL,
  external_transaction_id text NOT NULL,
  status                  text NOT NULL,
  amount_minor            bigint,
  currency                text,
  purchased_at            timestamptz NOT NULL,
  expires_at              timestamptz,
  created_at              timestamptz NOT NULL,
  UNIQUE (platform, external_transaction_id)
);

CREATE TABLE credit_buckets (
  id            text PRIMARY KEY,
  user_id       text NOT NULL REFERENCES users(id),
  source_type   text NOT NULL,                      -- free_grant | purchase | promo | manual
  source_id     text,
  granted_units integer NOT NULL CHECK (granted_units > 0),
  expires_at    timestamptz,
  created_at    timestamptz NOT NULL
);

-- 🔴 UNIQUE (user_id, reference_key) 是额度幂等的地基，丢了就会重复发放。
--    必须是 DB 级唯一约束，不能退化成应用层检查。
CREATE TABLE credit_ledger (
  id                text PRIMARY KEY,
  user_id           text NOT NULL REFERENCES users(id),
  entry_type        text NOT NULL,                  -- grant|reserve|commit|release|expire|refund|adjustment
  units             integer NOT NULL,
  balance_bucket_id text NOT NULL REFERENCES credit_buckets(id),
  job_id            text,
  purchase_id       text,
  reference_key     text NOT NULL,
  created_at        timestamptz NOT NULL,
  CHECK ((entry_type = 'commit' AND units = 0) OR (entry_type <> 'commit' AND units <> 0)),
  UNIQUE (user_id, reference_key)
);

CREATE TABLE user_feedback (
  id           text PRIMARY KEY,
  user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  candidate_id text,
  rating       text NOT NULL,
  reason_codes jsonb NOT NULL DEFAULT '[]'::jsonb,
  comment      text,
  created_at   timestamptz NOT NULL
);

CREATE TABLE idempotency_records (
  user_id         text NOT NULL,
  idempotency_key text NOT NULL,
  request_hash    text NOT NULL,
  response_status integer,
  response_body   jsonb,
  created_at      timestamptz NOT NULL,
  PRIMARY KEY (user_id, idempotency_key)
);

CREATE TABLE events (
  id          text PRIMARY KEY,
  user_id     text,
  name        text NOT NULL,
  props       jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL
);

-- app_config 只保留**非密钥**的运行时覆盖项。
-- image_provider_api_key / smtp_pass 这两个 secret 键在 Go 版永远不写这张表，
-- 迁移时也必须跳过（见 cmd/museframe-assets 与 README 的迁移 runbook）。
CREATE TABLE app_config (
  key        text PRIMARY KEY,
  value      text,
  updated_at timestamptz
);

CREATE TABLE email_codes (
  email        text PRIMARY KEY,
  code_hash    text NOT NULL,
  expires_at   timestamptz NOT NULL,
  attempts     integer NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL,
  issue_count  integer NOT NULL DEFAULT 0,
  window_start timestamptz
);

-- 反白嫖去重台账。一次发放写多行：主键行带 units，其余键行 units=0 占位。
-- 24h 计数一律带 units > 0，否则占位行会把上限吃光。
-- user_id 沿用源库姿态：**故意不加外键**（保留已合并游客的行）。
CREATE TABLE free_grants (
  id          text PRIMARY KEY,
  user_id     text NOT NULL,
  dedupe_key  text NOT NULL,
  device_hash text,
  ip_hash     text,
  units       integer NOT NULL,
  created_at  timestamptz NOT NULL
);

CREATE TABLE server_secrets (
  key        text PRIMARY KEY,
  value      text NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE TABLE manual_grants (
  id              text PRIMARY KEY,
  user_id         text NOT NULL,
  units           integer NOT NULL,
  note            text,
  expires_at      timestamptz,
  created_at      timestamptz NOT NULL,
  idempotency_key text
);

-- ---- 23 个显式具名索引（与 SQLite 侧同名同列同序） ------------------------
CREATE INDEX projects_user_updated_idx     ON projects(user_id, updated_at DESC);
CREATE INDEX assets_project_idx            ON assets(project_id);
CREATE INDEX assets_user_status_idx        ON assets(user_id, status);
CREATE INDEX jobs_user_idx                 ON generation_jobs(user_id, created_at DESC);
CREATE INDEX jobs_status_idx               ON generation_jobs(status);
CREATE INDEX jobs_project_created_idx      ON generation_jobs(project_id, created_at DESC);
CREATE INDEX buckets_user_idx              ON credit_buckets(user_id, expires_at, created_at);
CREATE INDEX ledger_bucket_idx             ON credit_ledger(user_id, balance_bucket_id);
CREATE INDEX ledger_bucket_only_idx        ON credit_ledger(balance_bucket_id);
CREATE INDEX ledger_refkey_idx             ON credit_ledger(reference_key);
CREATE INDEX jobs_ledger_job_idx           ON credit_ledger(job_id, entry_type);
CREATE INDEX purchases_user_idx            ON purchases(user_id, purchased_at DESC);
CREATE INDEX purchases_status_idx          ON purchases(status);
CREATE INDEX events_occurred_idx           ON events(occurred_at);
CREATE INDEX events_name_at_idx            ON events(name, occurred_at);
CREATE INDEX feedback_user_candidate_idx   ON user_feedback(user_id, candidate_id);
CREATE INDEX sessions_user_idx             ON sessions(user_id);
CREATE INDEX idempotency_created_idx       ON idempotency_records(created_at);
CREATE INDEX idx_free_grants_ip            ON free_grants(ip_hash, created_at);
CREATE INDEX idx_free_grants_at            ON free_grants(created_at);
-- 刻意**非唯一**：跑过双发 bug 的库里带重复 dedupe_key，唯一索引建不起来。
-- 不变量由 maybeGrantFree 的四道闸保证。
CREATE INDEX idx_free_grants_dedupe        ON free_grants(dedupe_key);
CREATE INDEX idx_manual_grants_user        ON manual_grants(user_id, created_at);
CREATE UNIQUE INDEX idx_manual_grants_idem ON manual_grants(idempotency_key) WHERE idempotency_key IS NOT NULL;

COMMIT;
