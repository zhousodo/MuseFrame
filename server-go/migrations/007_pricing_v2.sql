-- 007_pricing_v2.sql —— 2026-09-23 新价目表（店主拍板）。由 museframe_owner 执行。
--
-- 价格是**分市场各自定价**，不是汇率换算：price_minor = 美分，price_cny_minor = 人民币分。
--
--   internal_key      类型                 张数  USD     CNY      说明
--   trial_3           pack                  3    1.99    9.90    新增；每账号限购一次（代码里判，见 public_waffo.go）
--   pack_10           pack                 10    5.99   19.90    原 4.99 / 29
--   pack_30           pack                 30   12.99   49.00    原 9.99 / 69（前端标「最划算」）
--   pack_100          pack                100   34.99  129.00    原 29.99 / 199
--   creator_pass_30   subscription/month   30    7.99   39.00    新增；**一次性**商品（one_time=true），30 天 Creator，不续费；
--                                                                网页端只卖 CNY（微信），替代 Waffo 不能按 CNY 扣的订阅
--   creator_monthly   subscription/month   30    7.99   (49.00)  不变；人民币价留给 Play，网页端订阅恒为 USD
--   creator_annual    subscription/year   360   59.99    NULL    原下架行（480 张 / 69.99）→ 上架，360 张每年一次，随周期到期
--
-- 新列 products.one_time：订阅型商品是否「一次性购买、不续费」。
--   - 只有 creator_pass_30 为 true。它在 Waffo 上是一次性商品：付款走 order.completed，
--     expires_at = periodExpiry（+30 天），额度随之到期；UserPlan 期间内报 creator。
--   - 「管理订阅 / 取消订阅」只针对 one_time=false 的订阅（ActiveWaffoSubscription 过滤）。
--   - GET /v1/products 的行序也用它：加购包（按价升序）→ 一次性通行证 → 续费订阅（按价升序）。
--
-- 幂等：可重复执行。ADD COLUMN IF NOT EXISTS；新商品 INSERT … WHERE NOT EXISTS；
-- 改价是绝对赋值（重跑结果相同 —— 但会盖掉此后在后台改过的价，重跑前先核对）。
-- Waffo 商品映射只在 waffo_product_id 仍为 NULL 时写入（与 006 同一口径）。
--
-- 🔴 旧镜像兼容：新列 NOT NULL DEFAULT false，旧镜像的 SELECT 是显式列表、INSERT 不带这一列，
--    完全透明。但**旧镜像**不认识新规则：trial_3 不限购、creator_pass_30 不能按 CNY 下单（旧代码 CNY 只放行
--    加购包）、一次性通行证会被当成可取消的订阅进「管理订阅」。所以跑完迁移应尽快发新镜像；两者之间只隔几分钟。
--
-- 🔴 新行主键：库里现有商品 id 是 uuid 文本（从 Node 版迁过来），这里同样用 gen_random_uuid()::text
--    （PG 13+ 内置，无需扩展）。
--
-- 不新增表 / 索引（DEPLOY.md §3 的 25 张表 / 64 个索引口径不变）。

BEGIN;

ALTER TABLE products ADD COLUMN IF NOT EXISTS one_time boolean NOT NULL DEFAULT false;

-- ---- 改价 --------------------------------------------------------------------
UPDATE products SET price_minor =  599, price_cny_minor =  1990 WHERE internal_key = 'pack_10';
UPDATE products SET price_minor = 1299, price_cny_minor =  4900 WHERE internal_key = 'pack_30';
UPDATE products SET price_minor = 3499, price_cny_minor = 12900 WHERE internal_key = 'pack_100';

-- creator_annual：上架，360 张 / 年，59.99 USD，无人民币价（网页端年订只走 USD），
-- 权益旗标与月订一致。
UPDATE products SET
  active          = true,
  granted_units   = 360,
  price_minor     = 5999,
  price_cny_minor = NULL,
  feature_flags   = COALESCE((SELECT feature_flags FROM products WHERE internal_key = 'creator_monthly'), feature_flags)
WHERE internal_key = 'creator_annual';

-- ---- 新商品 ------------------------------------------------------------------
-- 两个新商品都没有 Play / App Store 商品（google_product_id / apple_product_id 为 NULL），
-- GET /v1/products 的 platforms 字段据此不含 android / ios，原生端不展示。
INSERT INTO products (id, internal_key, product_type, display_name, granted_units, price_minor, currency,
                      period, feature_flags, active, google_product_id, apple_product_id, price_cny_minor, one_time)
SELECT gen_random_uuid()::text, 'trial_3', 'pack', 'Trial 3', 3, 199, 'USD',
       NULL, '{}'::jsonb, true, NULL, NULL, 990, false
WHERE NOT EXISTS (SELECT 1 FROM products WHERE internal_key = 'trial_3');

INSERT INTO products (id, internal_key, product_type, display_name, granted_units, price_minor, currency,
                      period, feature_flags, active, google_product_id, apple_product_id, price_cny_minor, one_time)
SELECT gen_random_uuid()::text, 'creator_pass_30', 'subscription', 'Creator 30-Day Pass', 30, 799, 'USD',
       'month',
       COALESCE((SELECT feature_flags FROM products WHERE internal_key = 'creator_monthly'),
                '{"premiumStyles": true, "priorityQueue": true, "highResolution": true}'::jsonb),
       true, NULL, NULL, 3900, true
WHERE NOT EXISTS (SELECT 1 FROM products WHERE internal_key = 'creator_pass_30');

-- 行已存在（重跑 / 事先手工建过）时也把一次性标记补上。
UPDATE products SET one_time = true WHERE internal_key = 'creator_pass_30' AND one_time = false;

-- ---- Waffo 商品映射 -----------------------------------------------------------
-- 三个新 Waffo 商品已于 2026-09-23 在 Dashboard（prod 模式，店铺 STO_2gYlsri8wtsqFPiIEN6kOO）建好，
-- successUrl 都是 https://museframe.lenscript.cn/app?checkout=success：
--
--   internal_key     waffo_product_id                type          价格
--   trial_3          PROD_1ljuEsEEljhWUDU9ReU9Js     onetime       USD 1.99 / CNY 9.90
--   creator_pass_30  PROD_0kjLl2SI11Y9Cp4R4JntHM     onetime       USD 7.99 / CNY 39.00（SaaS；网页端只按 CNY 卖）
--   creator_annual   PROD_3D1CEZRO5lunet0eHPwHpG     subscription  USD 59.99 / 年（无试用）
--
-- 老四个沿用 006 的映射，价格已在 Dashboard 里就地改成新价：
--   pack_10 PROD_3l2au9D4bKq3SWJtHOeknD (5.99 / 19.90)、pack_30 PROD_3MGCYkNJsjqqPpM8HlwvXN (12.99 / 49.00)、
--   pack_100 PROD_6IYxsqbH1ql6R5ZyAoyvxA (34.99 / 129.00)、creator_monthly PROD_2V2oX2au6mKplqg3nHesbR（不变）。
UPDATE products SET waffo_product_id = 'PROD_1ljuEsEEljhWUDU9ReU9Js' WHERE internal_key = 'trial_3'         AND waffo_product_id IS NULL;
UPDATE products SET waffo_product_id = 'PROD_0kjLl2SI11Y9Cp4R4JntHM' WHERE internal_key = 'creator_pass_30' AND waffo_product_id IS NULL;
UPDATE products SET waffo_product_id = 'PROD_3D1CEZRO5lunet0eHPwHpG' WHERE internal_key = 'creator_annual'  AND waffo_product_id IS NULL;

COMMIT;
