# 交接：MuseFrame 网页端接入 Waffo Pancake（2026-09-23）

> 面向下一个接手的人或 agent：读完这一页就知道**做了什么、线上现在是什么状态、还差什么、出事怎么查**。
> 代码细节看 `server-go/internal/waffo` 与 `internal/httpapi/{public_waffo,webhook_waffo}.go`；
> 发版与迁移看 [`../DEPLOY.md`](../DEPLOY.md) §3 / §6；运维口径看 [`../OPS.md`](../OPS.md)。
> 🔴 本页不含任何凭据；私钥只在生产机 `app.env`，Dashboard 可重新下载。

## 1. 一句话

网页版（`https://museframe.lenscript.cn/app`）的点数包与 Creator 月订阅改由 **Waffo Pancake**（商户记录 MoR，托管收银台）
收款；Android 内购不变（Google Play）。后端是 Go，不能直接用 `@waffo/pancake-ts`，
所以按同一套协议在 Go 里实现（请求 RSA-SHA256 签名、webhook 验签 `t.rawBody`、45 分钟时间窗、按载荷 `id` 去重）。

## 2. 身份与标识（都不是密钥）

| 项 | 值 |
|---|---|
| 商户号 | `MER_4Dq9KxGzXARmX7Pm0K4968` |
| 店铺（museframe） | `STO_2gYlsri8wtsqFPiIEN6kOO`（同一商户下还有 `tese`、`latexdoctor` 两个店铺，与本项目无关） |
| 环境 | prod（API Key 与环境绑定；测试模式另建 key） |
| 生产 API key | `museframe-prod-server`（2026-09-23 所有者创建，「允许再次下载私钥」已开） |
| Webhook（生产） | Raw 格式、全部 14 种事件 → `https://museframe.lenscript.cn/v1/webhooks/waffo`；平台级 LIVE 验签公钥已内置 `internal/waffo/keys.go` |
| 商品（生产模式，价目表 v2，2026-09-23 起） | `trial_3` → `PROD_1ljuEsEEljhWUDU9ReU9Js`（3 张，USD 1.99 / CNY 9.90，每账号一次）· `pack_10` → `PROD_3l2au9D4bKq3SWJtHOeknD`（5.99 / 19.90）· `pack_30` → `PROD_3MGCYkNJsjqqPpM8HlwvXN`（12.99 / 49）· `pack_100` → `PROD_6IYxsqbH1ql6R5ZyAoyvxA`（34.99 / 129）· `creator_pass_30` → `PROD_0kjLl2SI11Y9Cp4R4JntHM`（一次性，30 天 Creator + 30 张，网页端只卖 CNY 39）· `creator_monthly` → `PROD_2V2oX2au6mKplqg3nHesbR`（USD 7.99/月，无试用）· `creator_annual` → `PROD_3D1CEZRO5lunet0eHPwHpG`（USD 59.99/年，360 张/年，无试用）。完整表见 DEPLOY.md §6 |
| 商品的 successUrl | `https://museframe.lenscript.cn/app?checkout=success` |

Waffo 的币种限制：一次性商品可 USD/EUR/GBP/HKD/JPY/CNY，**CNY 只能微信支付且单笔 ≤ ¥1000**；**订阅不能用 CNY**。
所以网页端（价目表 v2）：付费墙有 USD / ¥ CNY 切换（中文界面默认 CNY，记在 localStorage `mf.payCurrency`）；
CNY = 加购包 + 30 天通行证（替代不能按 CNY 扣的订阅），USD = 加购包 + 月订 / 年订；服务端同一套规则（`public_waffo.go` 的 `webCheckoutAmount`）。

## 3. 线上状态（2026-09-22 17:45 UTC 起）

- 镜像 `museframe-api:20260922T171022Z-g8ec4f1d5`（源码 `8ec4f1d5`），容器 `museframe-api-go` healthy；上一版 `20260912T145715Z-gbc3c8f0c` 仍在机上可回滚。
- 迁移 006 已执行：`products.waffo_product_id`、`purchases.provider_order_id`、`webhook_events` 表；25 张表 / 64 个索引。
- `app.env` 含 `WAFFO_MERCHANT_ID / WAFFO_STORE_ID / WAFFO_MODE=prod / WAFFO_PRIVATE_KEY`（单行 `\n` 转义 PEM）。
- 开机日志：`Waffo 网页端结账 {"configured":true,"webhookKey":true,"mode":"prod"}`；`GET /v1/auth/config` → `billing.web = true`。
- 无签名 POST 到 webhook 路由回 401（边缘透传正常）。
- **Waffo 店铺审核中**（2026-09-23 提交，1–3 个工作日邮件通知）。审核通过前 Waffo 对建会话回 403，网页端下单会失败，这是平台侧状态，不是故障。
- 站点侧（lenscript-site 仓，release `20260923-011812`）：条款新增第 6 节「取消、退款与账单争议」、隐私政策补支付处理方、支持页补退款 FAQ、`/.well-known/waffo-verify.txt` 域名验证文件。Waffo 后台：域名已验证、网站自检通过。

## 4. 接口与数据流

| 路由 | 鉴权 | 作用 |
|---|---|---|
| `POST /v1/purchases/web/checkout` `{productKey, currency?}` | 注册账号 | 建 `purchases` pending 行（`platform=waffo`，`external_transaction_id` = 我们的 purchase id，作为 `orderMerchantExternalId` 送给 Waffo），调 `create-session`，回 `{checkoutUrl, sessionId, expiresAt, purchaseId}`；未配置 → 501 |
| `POST /v1/webhooks/waffo` | 无（验签） | 验签 → 解析 → `mode` 过滤 → `webhook_events` 按 `id` 去重 → 处理 → `processed_at`。业务异常（找不到订单）也回 200 并记 `error` 列；只有 DB 故障回 5xx 让 Waffo 重投（最多 4 次） |
| `POST /v1/purchases/web/subscription/cancel` | 注册账号 | 以商户身份调 `cancel-order`（当期末生效） |

事件处理：`order.completed` → 订单 verified + 发额度；`subscription.activated/renewed/recovered` → verified + `expires_at = currentPeriodEnd + 48h` + 按 `<purchaseId>:<periodEnd>` 键发当期 30 张（幂等）；
`payment_succeeded` 只标 verified；`canceled` → `expires_at` 压到当时、状态 `canceled`；`refund.succeeded` → 状态 `refunded`，点数包**未消费**额度以 `refund` 负分录撤销，订阅关闭。
**额度只由验签通过的 webhook 发**；收银台跳回的 `?checkout=success` 只让前端轮询余额 ~20 秒。

## 5. 还差什么（按优先级）

1. **等审核邮件**。通过后：Dashboard → Settings → Webhooks → Send Test Event，生产端回 401 是预期（测试钥签名）；看到 404/502/超时才是问题。
2. **首笔真实订单核对**（所有者明确暂不做买退测试）：后台「数据库浏览」看 `webhook_events`（`processed_at` 非空、`error` 为空）与 `purchases`（`verified`、`provider_order_id` 有值），用户余额增加。
3. 审核若被打回：Waffo 邮件会说原因；条款源文件在 lenscript-site 仓 `tools/legal/content_museframe.py`（生成页不手改，改完 `python tools/legal/build.py museframe` 再 `tools/deploy.sh museframe --no-caddy`）。
4. 可选：一小段私钥 base64（首行 64 字符）曾出现在一次 compose 报错回显里；若介意，在 Dashboard 删掉 `museframe-prod-server` 重建，按 §7 的格式写入 `app.env` 后重新 deploy。

## 6. 出事怎么查

| 现象 | 先看 |
|---|---|
| 网页端没有「购买 / 订阅」按钮 | `curl -s https://museframe.lenscript.cn/v1/auth/config` 的 `billing.web`；false → 开机日志那行 `configured`，多半是 `app.env` 四项缺一或私钥格式不对（§7） |
| 点购买回 403 / 「无法发起支付」 | Waffo 店铺未通过审核，或被平台暂停收款；看 Dashboard 首页横幅 |
| 付了钱额度没到 | 后台 `webhook_events` 有没有这条 `order.completed`：没有 → Dashboard 投递日志（是不是 401/5xx）；有但 `error` 非空 → 订单对不上（看 `orderMerchantExternalId`）；有且 `processed_at` 为空 → 上次处理 DB 失败，等重投或手工重放 |
| webhook 全 401 | `WAFFO_MODE` 与事件 `mode` 是否一致；边缘是否改写了请求体（验签算的是原始字节） |
| 用户要退款 | 条款第 6 节的规则；在 Dashboard 退，`refund.succeeded` 到达后系统自动撤销未用额度 |

## 7. 运维踩坑（已写进 DEPLOY.md / OPS.md，这里再列一遍）

- `platformctl migrate museframe` **没有 runner**，迁移用 `docker cp` + `psql -U "$POSTGRES_USER" -f` 前置 `SET ROLE museframe_owner;`（容器里没有 `postgres` 角色）。
- `app.env` 是 dotenv：`WAFFO_PRIVATE_KEY` 必须**一行**、PEM 头尾齐全、`\n` 字面量、不加引号。多行 → `docker compose config` 解析失败、deploy 中止；裸 base64 → 服务起来但「私钥不可解析」。改完先 `docker compose config >/dev/null`。
- 生产机内存 1.9G，**不在上面 build**；本机 Windows 没有 Go/Docker，用 WSL Ubuntu-24.04（`go 1.26.5`；docker 守护进程需 `wsl -u root -- service docker start`，构建也以 root 跑）。
- 两仓分支流转：临时分支 → PR → `develop` → PR → `main`，再把 `develop`/`backend` 快进到 `main`；`main` 有 ruleset，不能 force push。

## 8. 相关 PR / 记录

- MuseFrame：#5（接入）、#6（develop → main）、本页所在的文档 PR。
- lenscript-site：#21（条款/隐私/验证文件）、#22（→ main）、#23（验证 token 重生成）、#24（→ main）；`sites/museframe/CHANGES.md` 与 `ops/DECISIONS.md` 2026-09-23 行。
