-- 006_waffo.sql —— Waffo Pancake 网页端结账接入。由 museframe_owner 执行。
--
-- 三件事：
--   1. products.waffo_product_id —— 本地商品 ↔ Waffo 商品（PROD_xxx）的映射。
--      由 cmd/museframe-waffo-seed 一次性回填；为 NULL 的商品在网页端不可买
--      （POST /v1/purchases/web/checkout 回 501）。
--   2. purchases.provider_order_id —— Waffo 订单号（ORD_xxx）。
--      🔴 它**不是** external_transaction_id：那一列在 Waffo 平台上放的是**我们自己的**
--      purchases.id（建会话时作为 orderMerchantExternalId 送出去，每条 webhook 原样回传，
--      于是 (platform='waffo', external_transaction_id) 这条唯一约束仍然是防重复发放的地基）。
--      订单号单独一列，给「取消订阅」（cancel-order 要 ORD_）和后台对账用。
--   3. webhook_events —— 收到的 webhook 原样落库：按载荷 id 去重（重试会原样重放同一条），
--      processed_at 为 NULL 表示「收下了但业务处理没跑完」（进程崩了 / DB 事务失败），
--      下一次重投会重新处理；error 记业务层面的异常（找不到对应订单、商品对不上），
--      这些**仍回 200**（重投也不会变对），靠后台数据库浏览页看见。
--
-- 幂等：可重复执行（IF NOT EXISTS）。
--
-- 🔴 旧镜像兼容：两个新列都可空、无默认值之外的约束，对正在运行的旧镜像完全透明
--    （它的 INSERT 列表里没有这两列，PG 填 NULL）；新表旧镜像根本不碰。
--    所以这个迁移可以在发版**之前**单独执行，不需要停机。
--
-- 🔴 权限：002_grants.sql 的 ALTER DEFAULT PRIVILEGES 只对 owner **此后**新建的表生效，
--    理论上 webhook_events 建好就自动带 DML 授权；但那条默认授权是在 002 里
--    以「执行者 = museframe_owner」为前提设的，为防这次迁移被别的角色跑，
--    这里显式再 GRANT 一次（与 002 同一口径，重复执行无害）。

BEGIN;

ALTER TABLE products  ADD COLUMN IF NOT EXISTS waffo_product_id  text;
ALTER TABLE purchases ADD COLUMN IF NOT EXISTS provider_order_id text;

-- 取消订阅 / 退款 webhook 按 Waffo 订单号反查；平台限定，避免撞上别家的订单号格式。
CREATE INDEX IF NOT EXISTS purchases_provider_order_idx
  ON purchases (platform, provider_order_id) WHERE provider_order_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS webhook_events (
  id           text PRIMARY KEY,               -- 载荷 id（PAY_/ORD_/REF_…），重试不变
  provider     text NOT NULL,                  -- 'waffo'
  event_type   text NOT NULL,                  -- order.completed / subscription.* / refund.*
  mode         text,                           -- 'test' | 'prod'（载荷自带）
  payload      jsonb NOT NULL,                 -- 原始载荷，审计用
  received_at  timestamptz NOT NULL,
  processed_at timestamptz,                    -- NULL = 业务处理未完成，重投会重跑
  error        text                            -- 业务异常说明（仍已 200）
);

CREATE INDEX IF NOT EXISTS webhook_events_received_idx ON webhook_events (received_at DESC);
CREATE INDEX IF NOT EXISTS webhook_events_pending_idx  ON webhook_events (received_at) WHERE processed_at IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_events TO museframe_app;
GRANT SELECT ON webhook_events TO museframe_readonly;

-- ---- 商品映射回填 ----------------------------------------------------------
-- 四个 Waffo 商品已于 2026-09-23 在 Dashboard（prod 模式，店铺 STO_2gYlsri8wtsqFPiIEN6kOO）
-- 手工建好，价格与本地目录逐项一致，successUrl 都是 https://museframe.lenscript.cn/app?checkout=success：
--
--   internal_key     waffo_product_id                type          价格
--   pack_10          PROD_3l2au9D4bKq3SWJtHOeknD     onetime       USD 4.99  / CNY 29.00  (digital_goods)
--   pack_30          PROD_3MGCYkNJsjqqPpM8HlwvXN     onetime       USD 9.99  / CNY 69.00  (digital_goods)
--   pack_100         PROD_6IYxsqbH1ql6R5ZyAoyvxA     onetime       USD 29.99 / CNY 199.00 (digital_goods)
--   creator_monthly  PROD_2V2oX2au6mKplqg3nHesbR     subscription  USD 7.99 / 月         (saas，无试用)
--
-- 只在 waffo_product_id 还是 NULL 时写入：已经被 CLI 或后台改过的行不动。
-- 所以生产跑完这个迁移就能用，不需要再跑 museframe-waffo-seed。

UPDATE products SET waffo_product_id = 'PROD_3l2au9D4bKq3SWJtHOeknD' WHERE internal_key = 'pack_10'         AND waffo_product_id IS NULL;
UPDATE products SET waffo_product_id = 'PROD_3MGCYkNJsjqqPpM8HlwvXN' WHERE internal_key = 'pack_30'         AND waffo_product_id IS NULL;
UPDATE products SET waffo_product_id = 'PROD_6IYxsqbH1ql6R5ZyAoyvxA' WHERE internal_key = 'pack_100'        AND waffo_product_id IS NULL;
UPDATE products SET waffo_product_id = 'PROD_2V2oX2au6mKplqg3nHesbR' WHERE internal_key = 'creator_monthly' AND waffo_product_id IS NULL;

COMMIT;
