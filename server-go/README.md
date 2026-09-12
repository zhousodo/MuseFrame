# 留影 MuseFrame · Go 后端（`server-go/`）

> **本节由「代码拆分归位」批次（2026-09-11）写入，说明这份代码的来历与使用边界。**
> 正文（第 1 节起）是后端重写 agent 的原始交付文档，逐字保留。

## 它从哪来

2026-09-11 服务器重构轮次里，MuseFrame 后端从 **Node.js 零框架（`node:http` + SQLite）
→ Go + PostgreSQL** 做了完整重写。代码原先堆在掌镜仓库的
`9-服务器重构/platform/apps/museframe/`（那是平台重构的临时工作区，归属是错的）。
本批次把它拆回本仓库的 `server-go/`。

- 来源：掌镜仓库 `D:/sonycanonnik/9-服务器重构/platform/apps/museframe/`
- 写作依据：同仓库 `9-服务器重构/45-MuseFrame后端重写报告.md`（含未解决项全表）
- 规模：89 个 `.go`、13,191 行；67 个测试函数

**源目录在 30 天观察期内不删**。观察期内两边若都要改，**以 `server-go/` 为准**，源目录只读。

## 与现役旧实现（`server/`）的关系

| | `server/`（现役） | `server-go/`（本目录） |
|---|---|---|
| 实现 | Node.js 零框架 `node:http` + SQLite | Go 1.26 + PostgreSQL + pgx/v5 v5.9.2 |
| 线上状态 | 🟢 **正在生产跑**，承接全部流量 | 🔴 **未上线**，从未接过真实流量 |
| 处置 | 30 天观察期内**原封不动**（回滚兜底） | 门禁全绿后才切 |

🔴 **`server-go/` 不覆盖、不替换 `server/`。** 仓库根的 `Dockerfile` / `docker-compose.yml` /
`data/` 都还属于 Node 实现，本次一个字节都没动。切换是一次单独的、需要拍板的动作。

## 部署方式

```
server-go/deploy/
├── Dockerfile            museframe-api 交叉编译 + distroless
├── Dockerfile.migrate    一次性迁移 job
├── docker-compose.yml    接统一 PG 实例 platform-postgres
└── project.env.example   配置样例（🔴 真正的 project.env 与 .env 永不入库）
```

要点：
1. 镜像**本地交叉编译** → `docker save` → `scp` → `docker load`，生产机不 build。
2. compose **必须挂两张网**：`platform-db-net`（external + internal，连 PG）
   与本项目自有 bridge（publish 端口）。只挂 internal 那张时容器 healthy、日志正常，
   但宿主机 `curl` 直接 refused，**没有任何报错**。
3. 库表由**一次性 job** 以 `museframe_owner` 执行 `migrations/001_init.sql` + `002_grants.sql`；
   运行角色 `museframe_app` 只有 DML、无 DDL 权。
4. 内存上限 **512m 不要随手调低**：理论最坏 = `MAX_SOURCE_PIXELS(40MP) × 4B ×
   worker_concurrency(3) ≈ 480 MB`。降到 256m 必须先调小 `MAX_SOURCE_PIXELS`，
   而那是**契约变更**（改变 422 `ASSET_UNSUPPORTED` 的触发阈值）。

### 本地跑测试

集成测试需要 PostgreSQL，**DSN 用 `museframe_owner`**（测试夹具走 `TRUNCATE`，需要表属主权限）：

```bash
psql -U museframe_owner -d <db> -f migrations/001_init.sql
psql -U museframe_owner -d <db> -f migrations/002_grants.sql
export MUSEFRAME_TEST_DATABASE_URL='postgres://museframe_owner:<pass>@127.0.0.1:5432/<db>?sslmode=disable'
go test ./... -count=1
```

## 🔴 切换前必须完成的门禁

摘自 `45-MuseFrame后端重写报告.md` 第 10 节（7 条）：

| # | 门禁 | 阻塞？ |
|---|---|---|
| **U-2** | 🔴 用 `POST /v1/admin/email/test` 对着 Brevo **实打实发一封信**——`internal/mailer` 无自动化测试 | **是** |
| U-1 | `internal/oidc`（Google/Apple ID Token 验签）与 `internal/play`（Play 收据）无自动化测试，从未对真实端点跑过 | 否（生产三个凭据全空，这两条路径现在是 501 `PROVIDER_NOT_CONFIGURED`）。**启用任何一个之前必须先端到端联调** |
| U-3 | `seedCatalog` / `seedProducts` 刻意未实现——目录数据改走数据迁移。**需拍板**：补一次性 seed 工具，还是接受「目录只由 DB 管」 | 需拍板 |
| U-4 | 本地像素引擎 `local_engine_fallback` 未实现 | 否（该旗标线上恒为 false） |
| U-5 | 挑带 compiler 的 3 个风格（`press_cover_story_01` / `press_reportage_wash_01` / `press_zine_poster_01`）各跑一次真实生成做人工对比 | 建议做 |
| U-6 | 资产迁移四个数字仍是本地构造的 3/3/0/0，迁移日后补真实的 19/19/0/0 | 迁移日 |
| U-7 | 内存上限 512m 的取舍（见上） | 否 |

另有两条**不属于重写范围、但仍悬着**的红项：
- 生产 `app_config` 表里的**明文密钥行尚未清除**（Go 版不读它，但它还在库里、还在每日备份里）。
  迁移时必须跳过这两行，旧栈下线后清掉历史备份里的凭据。
- 备份**仍无异地、仍未做过恢复演练**。

---

# MuseFrame（留影）后端 —— Go + PostgreSQL

替换原 Node.js（零框架 `node:http`）+ SQLite 实现。**64 条路由**（公开 30 + 管理 34；
另加一条不在公开契约里的 `/v1/ready`）
逐条复刻，外加一条不在公开契约里的内部只读探针 `/v1/ready`。

- 地面事实来源：`/opt/museframe/server/*.js` 只读副本（源码是行为真本）
- 契约：`9-服务器重构/11-API冻结契约.md` 第三章 + `12-契约空白补查.md` D-11~D-18
- 盘点：`9-服务器重构/盘点-20260911/03-MuseFrame盘点.md`

```
cmd/museframe-api       服务主程序（含 healthcheck 子命令）
cmd/museframe-assets    资产迁移 + sha256 回填 + 四数字交叉校验
internal/apierr         统一错误信封与 26 个错误码
internal/aigc           AI 生成内容标识：显式水印（内嵌 GB2312 子集字体）+ 隐式 EXIF/XMP
internal/cfgstore       运行时配置（🔴 密钥只走环境变量）
internal/config         进程配置（环境变量，硬校验）
internal/httpapi        路由表 + 横切中间层 + 全部 handler
internal/imaging        JPEG 解码 / 启发式分析 / 推荐排序
internal/ledger         append-only 额度台账
internal/logx           结构化日志 + Redact
internal/mailer         SMTP
internal/netx           客户端 IP 解析 + 请求体上限
internal/oidc           Google / Apple ID Token（RS256 + JWKS）
internal/play           Google Play 收据校验
internal/provider       上游图像模型适配器
internal/ratelimit      滑动窗口限流
internal/metrics        进程内每接口请求计数器（后台「接口健康」视图）
internal/store          PostgreSQL 持久层 + 管理后台只读数据浏览
internal/worker         生成队列
migrations/             001_init.sql（24 表 / 58 索引）、002_grants.sql（角色授权）、
                        003_feedback_handled.sql（反馈「已处理」两列 + 部分索引）、
                        004_aigc_label.sql（assets.aigc_label 一列 + 未标识成品部分索引）、
                        005_free_grant_ip.sql（free_grants.ip 明文客户端地址，可空、幂等、
                        加一列对正在跑的旧镜像完全透明，可在发版前单独执行）
deploy/                 Dockerfile、docker-compose.yml、project.env.example
```

## 构建

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=<tag>" -o dist/museframe-api ./cmd/museframe-api
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w" -o dist/museframe-assets ./cmd/museframe-assets
```

生产机内存小，**禁止在生产机 docker build**：二进制在本地交叉编译好，
镜像只负责把静态二进制装进 distroless static（自带 CA 根证书，静态 Go 需要它走 HTTPS）。

## 测试

```bash
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
# 集成测试需要一个 PostgreSQL：
MUSEFRAME_TEST_DATABASE_URL='postgres://museframe_owner:...@127.0.0.1:5432/museframe?sslmode=disable' \
  go test ./... -count=1
```

未设置 `MUSEFRAME_TEST_DATABASE_URL` 时集成测试会 **skip**（不是静默通过）。

## 🔴 三个安全问题的修复

### 1. 上游 API Key 不再可能来自数据库

Node 版 `configStore.cfg()` 的优先级是 **DB 覆盖 > 环境变量 > 内置默认**，
对 `secret:true` 的键一视同仁。运维 2026-09-02 从后台设了一次密钥，
密钥就以明文躺进 `app_config` 表，并随每日备份 tar 落盘；此后清空 `.env`
里的同名变量**不再生效**。

Go 版把这条路径整条拆掉：

| 位置 | 行为 |
|---|---|
| `cfgstore.Reload` | `app_config` 里 secret 键的行**不进缓存**，只记键名（`SkippedSecretRows`）提醒运维去清，**绝不记值** |
| `cfgstore.String("image_provider_api_key")` | 只读环境变量 |
| `cfgstore.Set(secretKey, …)` | 返回 `ErrSecretNotWritable` → `PUT /v1/admin/config` 得到 **422** |
| `provider.Adapter.apiKey` | 从 `config.Config`（环境变量）注入，不经过 cfgstore |
| `mailer` | 只接收已取好的口令字符串，自己不查库 |
| `GET /v1/admin/config` | secret 项掩码为固定 `••••••••`（2026-09-12 起不再回末 4 位 —— 后台页面可截图，末位足以做泄漏库匹配），`source` 只可能是 `env` / `default` |

**迁移红线**：`app_config` 里的 `image_provider_api_key` / `smtp_pass` 两行
**不迁进 PG**（迁了也不生效，但不该留在库里）。

### 2. `/v1/admin/db/table/{name}` 的泄漏面

Node 版实况：`QUERY_DENY_TABLES` **只拦 `POST /db/query`**，对表浏览器完全不生效；
`maskCell` 只有 3 条通用规则。于是 `server_secrets.value`、
`auth_identities.email_normalized`、`purchases.external_transaction_id`
在生产实现里是**明文返回**的。

Go 版改成**表白名单 + 显式列级脱敏清单**：

- 白名单 23 张表（24 张业务表去掉 `server_secrets`）；白名单外一律 **404**
- 列级清单（`internal/store/dbbrowser.go` 的 `redactColumns`）逐列登记，
  每一条都有一个「不应出现在响应里」的测试
- SQL 控制台的拒绝表在 Node 四张的基础上补上 `server_secrets`、`email_codes`，
  并加两道 PG 侧保险：只读角色 + `SET TRANSACTION READ ONLY`
- 时间列统一归一成 UTC ISO-8601 带 `Z`（pgx 扫出来的 `time.Time` 默认按**进程本地时区**
  序列化成 `+08:00`，这是重写里新引入的坑，已被测试钉死）

🔴 **2026-09-12 第七轮：清单收窄到只挡凭据与密钥。**
这是一个只有管理员令牌打得开的自家后台，所以**用户资料一律完整显示（管理员）**——
邮箱、交易号、设备与 IP 哈希、幂等键此前都被打码，而那样做的实际代价不是「更安全」：
客服照样要联系用户、照样要对账，于是真实发生的事是有人改去 SSH 上 `psql`
（那条路上什么都看得到，还没有审计）。

| 表 | 列 | 处理 |
|---|---|---|
| `server_secrets` | 整表 | 不在白名单，404 |
| `sessions` | `token` | 前 6 位 + 省略号（令牌就是凭据本身） |
| `email_codes` | `code_hash` | 整体掩码（一次性验证码的哈希 = 一个完整凭据） |
| `idempotency_records` | `response_body` / `request_hash` | 整体掩码（响应体里嵌着刚签发的会话令牌） |
| `app_config` | `value` | 该行 key 属于密钥项时 `••••(secret)` |
| `auth_identities` | `email_normalized` / `provider_subject` | **完整显示（管理员）** |
| `purchases` | `external_transaction_id` | **完整显示（管理员）**——拿它去商店后台对账 |
| `free_grants` | `device_hash` / `ip_hash` / `ip` | **完整显示（管理员）**，`ip` 是 005 迁移新增的明文列 |
| `sessions` | `device_id` | **完整显示（管理员）** |
| `manual_grants` | `idempotency_key` | **完整显示（管理员）** |

### 3. 额度四道闸与消费侧 10 步

`internal/httpapi/freegrant.go` 与 `public_jobs.go` 逐条复刻，
§6.6.3 的 12 个校验点每条配一个「应该被拒绝」的测试（见 `gates*_test.go`）。

## 与 Node 版的行为差异清单

**有意的行为变更**（需要单独拍板的已标注）：

| # | 差异 | 原因 / 影响 | 需拍板 |
|---|---|---|---|
| B-1 | `PUT /v1/admin/config` 对 `image_provider_api_key` / `smtp_pass` 返回 **422** | 安全问题 1。后台从此改不了密钥，只能改 project.env 并重启 | 是（运维流程变化） |
| B-2 | `GET /v1/admin/db/table/server_secrets` 返回 **404**；多列新增脱敏 | 安全问题 2。**会改变出参字节** | 是 |
| B-3 | `POST /v1/admin/db/query` 拒绝表新增 `server_secrets`、`email_codes` | 同上 | 是 |
| B-4 | `IP_HASH_SALT` 变成**独立必填**变量，且不得等于 `ADMIN_TOKEN`，否则启动失败 | Node 版回落 ADMIN_TOKEN，换令牌会静默重置 24h per-IP 上限 | 否（迁移时固化当前值即可） |
| B-5 | 新增 `GET /v1/ready`（只读 `SELECT 1`） | 统一探针命名；边缘代理不回源，公网打不到，**不改公开契约** | 否 |
| B-6 | 应用**不再自动建表**；schema 由一次性 job 用 `museframe_owner` 执行 | 运行角色 `museframe_app` 无 DDL 权 | 否 |
| B-7 | `POST /v1/candidates/{id}/export` 的两个写副作用放进**同一事务** | Node 版分开写；出参不变，原子性更强 | 否 |
| B-8 | `?limit` 负数被夹到 1 | Node 版 `Math.min(200, -5) = -5` → SQL `LIMIT -5` = 无限制 | 否（修的是 bug） |
| B-9 | `MUSEFRAME_WEB_DIR` 留空时非 `/v1/*` 一律 404 | 边缘代理托管静态资源时不该由后端返回软 404 的 200 HTML | 否 |
| B-10 | 管理接口与 CSV 导出**完整显示**邮箱 / 用户 id / 交易号 / IP（2026-09-12 第七轮） | 自家后台、管理员令牌保护。打码的代价是客服改去 SSH 查库 | 是（已拍板） |

**实现差异（出参应当等价，但机制不同）**：

| # | 差异 | 说明 |
|---|---|---|
| I-1 | `idempotency_records.response_body` 在 PG 里是 `jsonb`，**不保留键序** | 回放时解进具名结构体再序列化，回放响应与首次响应逐字节一致。有测试钉死 |
| I-2 | `GET /v1/products` 显式 `ORDER BY`（订阅在前 → 价格升序 → internal_key） | Node 版**没有 ORDER BY**，行序靠 SQLite rowid 巧合。PG 堆序会随 UPDATE 漂。当前目录上逐字复现线上行序，有黄金测试 |
| I-3 | 统计分桶写成 `(created_at AT TIME ZONE 'UTC')::date` | `created_at::date` 会按会话 TimeZone 解释，中国区 session 整体偏 8 小时。有 Asia/Shanghai 会话下的黄金测试 |
| I-4 | `/db/table`、`/db/query` 的时间列归一成 UTC `Z` | pgx 的 `time.Time` 默认按进程本地时区序列化 |
| I-5 | 列表 SQL 统一加 `id` 作为次级排序键 | PG 同值行的顺序不稳定；Node/SQLite 靠 rowid 隐式稳定 |
| I-6 | 图片解码用标准库 `image/jpeg`（Node 用 jpeg-js） | 两者都只解基线 JPEG；像素级输出可能有极小差异，不影响契约字段 |
| I-7 | 事务不可重入 | Go 版所有需要原子性的写路径显式接收同一个 `Queryer`，不依赖 Node 版 `tx()` 的重入计数 |
| I-8 | `PATCH /v1/projects/{id}` 只处理 `title` | **与源码一致**：契约里写的 `selectedCandidateId` 在 Node 版根本没实现（契约有误） |
| I-9 | `POST /v1/purchases/verify` 入参是 `{productKey, purchaseToken?, transactionId?, platform?}`，出参是 `{purchaseId, status, entitlements}` | **与源码一致**；契约第三章写的 `{productId, externalTransactionId}` / `{purchase, entitlements}` **与源码不符**，已按源码实现并在报告里标出 |
| I-10 | `POST /v1/events` 入参是 `{events:[…]}`，出参 `{accepted:int}`，且需要任意令牌（含游客） | **与源码一致**；契约写的 `{name, props}` / `{ok:true}` / 「公开」与源码不符 |

## App 出站调用 ↔ 后端路由 ↔ 后台可见处（2026-09-12 全链路审计）

审计判据一句话：**App 上报到后端的每一类数据，后台都要能看（列表 / 详情 / 搜索 / 导出）。**
下表逐条对照 `web/app.js` + `web/native.js` 的**全部**出站调用。
「App 不调用」的行刻意留着——它们是后端有而客户端没用的能力，别因为后台看得见就以为 App 在用。

| App 调用（文件:行） | 方法 + 路径 | 后台看得见的地方 |
|---|---|---|
| `native.js` 社交登录 | `POST /v1/auth/exchange` | 用户列表（登录方式列）、用户详情 · 会话 |
| `native.js emailRequestCode` | `POST /v1/auth/email/request` | **健康 · 邮件发送记录**＋验证码签发台账 |
| `native.js emailVerifyCode` | `POST /v1/auth/email/verify` | 同上；成功后进用户列表 |
| `native.js getAuthConfig` | `GET /v1/auth/config` | 配置页（各开关的生效值） |
| `app.js:1067` 退出登录 | `DELETE /v1/auth/session` | 用户详情 · 会话（行消失） |
| `app.js:155` 首页 | `GET /v1/discover` | 运营 · 风格管理（App 可见性列） |
| `app.js:496` 选图 | `POST /v1/assets/upload-intents` | **资产**（status=pending 的行） |
| `app.js:497` 上传 | `PUT /v1/assets/{id}/upload` | **资产**（字节数 / 宽高 / sha256） |
| `app.js:498` | `POST /v1/assets/{id}/complete` | **资产**（status→ready） |
| `app.js:513` | `GET /v1/assets/{id}/analysis` | 数据库 · `photo_analyses` |
| `api.js ensureAssetToken` | `GET /v1/assets/img-token` | 健康 · 接口健康 |
| `api.js assetUrl` | `GET /v1/assets/{id}/file` | 健康 · 接口健康 |
| `app.js:499/870/914` | `POST/GET /v1/projects`、`GET /v1/projects/{id}` | **用户详情 · 项目**（含软删行） |
| `app.js:663` 提交生成 | `POST /v1/generation-jobs` | **任务**（可筛状态 / 时间窗 / 用户；带生成参数与上游返回摘要） |
| `app.js:693/918` 轮询 | `GET /v1/generation-jobs/{id}` | 同上 |
| `app.js:821/826` | `POST /v1/candidates/{id}/feedback` | **反馈**（含**用户写的正文**＋已处理标记） |
| `app.js:833` 导出成品 | `POST /v1/candidates/{id}/export` | **资产**（kind=export） |
| `api.js ensureSession`、`app.js:169/936` | `GET /v1/entitlements/me` | 用户详情 · 额度账本 + 可用额度 |
| `app.js:155` | `GET /v1/products` | 运营 · 商品与价格 |
| `app.js:1266` 内购 | `POST /v1/purchases/verify` | **购买**（平台 / 交易号 / 金额 / 入账额度 + 重验入口） |
| `app.js:936` | `GET /v1/purchases` | 同上；用户详情 · 购买 |
| `api.js track()`（16 个调用点） | `POST /v1/events` | **埋点**（按事件名 / 天 / App 版本聚合 + 原始样本 + 导出） |
| App 不调用 | `GET /v1/styles`、`GET /v1/styles/{slug}` | 运营 · 风格管理 |
| App 不调用 | `POST /v1/generation-jobs/{id}/cancel` | 任务（status=cancelled 可重试） |
| App 不调用 | `PATCH`/`DELETE /v1/projects/{id}` | 用户详情 · 项目 |
| App 不调用 | `GET /v1/health`、`GET /v1/ready` | 健康 · 接口健康 |

**App 还没做、后台据此也看不到的两件事**（不要在面板上假装有）：

- **崩溃 / 日志上报**：`web/app.js` 里没有任何崩溃上报调用，后端也没有对应路由。
  后台没有这一页，因为做一页空表会让运维以为「没崩过」。
- **版本检查 / App 版本号上报**：`track()` 的 props 里不带版本字段，
  所以「埋点 · 按 App 版本」的正常结果是一行 `(未上报)`。这一行就是这件事的唯一可见处。
  后端已兼容 `appVersion` / `app_version` / `version` 三种键，App 哪天开始报哪一个都会自动分开。

### 管理后台的 34 条路由

| 路径 | 用途 |
|---|---|
| `GET /v1/admin/overview` | 概览卡片（生成链路 / 反白嫖闸 / 累计数） |
| `GET /v1/admin/jobs` | 任务列表。`?status=`、`?sinceHours=`、`?userId=`（8 位前缀）、`?limit=` |
| `POST /v1/admin/jobs/{id}/retry` | 重试失败 / 取消的任务。**新建一条**带 `parent_job_id` 的任务并重新预留额度 |
| `GET /v1/admin/feedback` | 反馈列表（含正文）。`?rating=`、`?handled=yes\|no` |
| `POST /v1/admin/feedback/{id}/handled` | 标记 / 撤销「已处理」，带备注，落审计 |
| `GET /v1/admin/feedback-reasons` | 原因码观测（不是配置） |
| `GET /v1/admin/purchases` | 购买列表（平台 / 交易号 / 入账额度）。`?status=`、`?platform=` |
| `POST /v1/admin/purchases/{id}/reverify` | 补发漏入账的额度（幂等）。只对 `verified` 生效 |
| `GET /v1/admin/users` | 用户列表。`?q=`（邮箱 / 昵称 / id，按字符截到 120） |
| `GET /v1/admin/user-detail` | **单用户纵向详情**（完整邮箱 + 登录身份 + 免费额度发放的明文 IP）。`?userId=`（完整 id 或任意长度前缀；撞前缀回 409 `AMBIGUOUS`） |
| `POST /v1/admin/users/grant` | 手动发额度 |
| `POST /v1/admin/users/{id}/status` | 禁用 / 启用 |
| `GET /v1/admin/user-facts` | 状态分布 + 禁用语义说明 |
| `GET /v1/admin/events` | **埋点**。`?days=`（≤90）、`?name=`（只筛样本）、`?limit=` |
| `GET /v1/admin/assets` | **资产**。`?kind=`、`?status=`、`?userId=`、`?limit=` |
| `GET /v1/admin/email-log` | **发信记录 + 验证码签发台账**（完整地址 + 主题；验证码本体与哈希一个字节都不回） |
| `GET /v1/admin/api-health` | **接口健康**。`?hours=`（≤24） |
| `GET /v1/admin/export/{kind}.csv` | CSV 导出，`kind` ∈ users / jobs / purchases / feedback / events / assets |
| `GET /v1/admin/img-token` | 短时图片令牌（管理员令牌不进 URL） |
| `GET /v1/admin/assets/{id}/file` | 取资产文件（接受图片令牌或管理员令牌） |
| `GET /v1/admin/stats/daily`、`/stats/styles` | 按天趋势、风格排行 |
| `GET /v1/admin/db/tables`、`/db/table/{name}`、`POST /db/query` | 只读数据浏览（列级清单见上文：只挡凭据与密钥） |
| `GET /v1/admin/job-detail` | **任务详情**。`?jobId=`；列出**全部候选**（按 `created_at` 升序）+ 源图画面分析 |
| `GET /v1/admin/photo-analyses` | **画面分析**（photo_analyses）。`?status=`、`?userId=`、`?assetId=`、`?limit=` |
| `GET /v1/admin/style-versions` | **风格版本与完整 spec**（只读）。`?styleId=`、`?limit=` |
| `GET`/`PUT /v1/admin/config` | 运行时热键（密钥只读） |
| `GET /v1/admin/products-admin`、`PATCH .../{key}` | 商品与价格 |
| `GET /v1/admin/styles-admin`、`PATCH .../{id}`、`POST .../{id}/status` | 风格完整编辑 |
| `GET /v1/admin/audit` | 操作审计 |
| `POST /v1/admin/email/test` | 发测试邮件 |

### 三个后台写入口的语义（容易踩错的地方都在这）

**任务重试**必须**新建**一条任务，绝不能把原任务改回 `queued` 再入队。
原因在额度台账：任务失败时 `ledger.Release` 写的是**补偿分录**，原来的 `reserve` 行还留在表里。
于是重排原任务时 `ledger.Reserve` 看到 `job:<id>:reserve` 前缀已存在就直接 `return nil`（不扣），
而 `LedgerHasReserve` 看到 reserve 行还在就放行执行——两个守卫都通过，
钱在失败那一刻已经退给用户了，净结果是**一张免费的图**，且额度对账表上看不出任何异常。
用户不会被重复扣钱：失败退过一次、重试扣回来，净额仍是「成功一张扣一份」。
余额不足回 **409 `INSUFFICIENT_ENTITLEMENT`** 并提示先发额度——悄悄跳过扣减就等于回到那张免费图。

**购买重验**只补发缺失的额度，**不会**重新向商店要收据：`purchaseToken` 只存在于
App 那一次请求里，从不落库，所以「重新找 Google/Apple 验一次」在服务端做不到。
它刻意**不**复用 `finalizePurchase`——那条路径对一个已 `verified` 的购买只在「到期时间变新了」
时才发额度（自动续订的每个计费周期），对没有到期时间的点数包（`expires` 恒为 nil）会直接早退，
也就是一个点了必然回 200 且什么都不做的按钮。这里直接对着幂等键
`grant:purchase:<purchaseId>` 先查再发。
**`purchases.status` 的词汇表只有 `pending` / `verified` 两个值**——没有 `active`，
判据写成 `active` 会让每一笔真实购买都被拒成 409。

**反馈标记已处理**落在 `user_feedback.handled_at` / `handled_note`（003 迁移）。
撤销标记会把备注**一起清掉**：留着上一次的备注会让下一个人看到「未处理」却带着
一条「已退额度」的备注，而这两件事里只有一件是真的。

### 两个必须说清楚的口径限制

**接口健康是进程内的**：重启清零、多副本各算各的，不是全站统计。
P95 从固定延迟直方图算（5/10/25/50/100/250/500/1000/2500/5000/10000ms），
精度到桶边界，标 `>` 的表示落在最后一个开口桶里。
限流的 **429 在路由循环之前返回，不进计数器**；未匹配到路由表的请求（扫描器、静态文件）一律不计——
后者是刻意的：按 `r.URL.Path` 分组会让表的行数变成请求方可控的。

**审计、发信记录都住在 `events` 表里**（换来现成的索引 / 备份 / 数据库浏览器可查），
所以它们**会随 `event_retention_days` 到期被清**。埋点视图把这两类行都排掉了——
不排的话运营自己改一次配置、系统发一封验证码，都会在「用户行为」里多一行。

### CSV 导出

导出**完整显示（管理员）**：邮箱、用户 id、交易号一律完整，和页面同一个口径（同一个 `store` 函数）。
🔴 这一条在 2026-09-12 第七轮反过来了（此前是「一律打码 + id 只给前 8 位」）：
导出的全部用处就是拿它去对账、群发、挨个联系用户，打了码一件也做不了——
实际发生的事是有人绕开导出直接去抄数据库。凭据（会话令牌 / 验证码哈希 / 密钥）
从来不在这六类数据的任何一列里，这一点靠「只导出这些列」保证，不靠打码。
自由文本里的换行压成空格（不依赖下游正确处理多行 CSV 字段），文件带 UTF-8 BOM（否则中文列在 Excel 里全是乱码）。
响应头 `X-Row-Count` 给出数据行数，便于不解析 CSV 就核对。
前端走 **fetch + blob**，不能用 `<a href download>` / `window.open`——
管理员令牌走请求头，浏览器发起的导航带不上头，那样的链接一律回 401。

## AI 生成内容标识（合规）

《人工智能生成合成内容标识办法》（2025-09-01 施行）要求生成服务在产出上同时加
**显式标识**与**隐式标识**。实现全在 `internal/aigc`，接进管线的位置只有一处：
`internal/worker/run.go` 里质量闸之后、落盘之前的 `aigc.Apply`。

| | 是什么 | 有没有开关 |
|---|---|---|
| 显式 | 画进像素的角标文字（默认「AI 生成 · 留影」） | `aigc_label_enabled`，默认**开** |
| 隐式 | JPEG 的 EXIF（ImageDescription / Software / UserComment）+ XMP，携带 GB 45438-2025 附录A 的 `AIGC` 结构（`Label` / `ContentProducer` / `ProduceID` / `ReservedCode1` / `ContentPropagator` / `PropagateID`）、制作时间、成品 sha256，以及 IPTC 的 `DigitalSourceType=trainedAlgorithmicMedia` | **没有**。办法第五条是「应当」，一个开关的唯一用途是把自己关进违规状态 |

四件容易搞错、已经在代码里写死的事：

1. **标识发生在编码落盘那一步，不是下载/导出时。** 磁盘上躺着的那份就得是带标识的 ——
   成品会经由 `/v1/assets/{id}/file` 被 `<img>`、分享、另存为各种路径拿走，
   任何「导出时才加」的设计都必然有一条绕过它的路。
2. **标识失败 = 任务失败退额。** 交付一张没有法定标识的成品是合规事故，而且一旦交付就收不回来。
3. **不回溯历史成品。** 回溯要重编码已经交付给用户的图（不可逆的画质损失），
   而办法约束的是此后的产出。历史行 `assets.aigc_label` 为 NULL，后台显示「未标识，历史成品」。
4. **字体内嵌。** 运行镜像是 distroless static，里面一个字体文件都没有；水印默认含中文。
   内嵌的是 Noto Sans CJK SC Bold 的 ASCII + GB2312 子集（1.5 MB，OFL-1.1，
   见 `internal/aigc/fontdata/NOTICE.md`）。注册表在**写**的一侧逐字校验 `aigc_label_text`
   的字形覆盖：子集外的字会画成空白，那是个后台显示「已保存」、线上每张图都缺半句话的隐形故障。

落库在 `assets.aigc_label`（`004_aigc_label.sql`）：`'visible+meta'` / `'meta'` / NULL。
App 读 `GET /v1/generation-jobs/{id}` 的 `candidate.aigcLabeled`，在结果页与作品墙上显示「AI 生成」角标；
后台「资产」页有「AI 标识」列（CSV 导出同列），「配置」页有 `aigc` 分组的 6 个热键。

核验一张成品：`exiftool -G -a -u <file>`，或 `aigc.ExtractJPEG`。

## 切换 runbook

```bash
# 0. 前置：统一 PG 已就绪，museframe 库与三个角色已建好
#    容器 platform-postgres，网络 platform-db-net

# 1. 建 schema（一次性 job，用 owner 角色；运行角色没有 DDL 权）
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f migrations/001_init.sql
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f migrations/002_grants.sql
# 增量迁移（全部幂等，可重复执行；都只加可空列/索引，旧镜像照常跑）
for f in migrations/00[345]_*.sql; do psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f "$f"; done
# 自检：24 张表 / 58 个索引 / 2 个 CHECK / 11 个 UNIQUE / 24 个 PK
psql "$OWNER_DSN" -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE';"
psql "$OWNER_DSN" -tAc "SELECT count(*) FROM pg_indexes WHERE schemaname='public';"

# 2. 数据迁移（platform/migrations/framework + museframe/config.json）
#    🔴 app_config 里的 image_provider_api_key / smtp_pass 两行必须跳过

# 3. 资产迁移（-src 必须是备份副本，工具会拒绝生产路径）
./dist/museframe-assets -src /srv/platform/backups/museframe/assets \
  -dst /var/lib/docker/volumes/museframe-assets/_data \
  -dsn "$OWNER_DSN" -apply -backfill-sha256
#    验收基线：引用 19 / 命中 19 / 孤儿引用 0 / 孤儿文件 0，字节和 10970358
#    有任何孤儿 -> 工具以退出码 1 停下（不要加 -allow-orphans 绕过）

# 4. 起服务
cp deploy/project.env.example project.env && chmod 600 project.env   # 填入真实值
MUSEFRAME_IMAGE_TAG=20260911T000000Z docker compose -f deploy/docker-compose.yml up -d
#    ⚠️ 服务名是 api，container_name 只是 museframe-api
#       `--force-recreate museframe-api` 会报 no such service（2026-08-31 踩过）

# 5. 冒烟
curl -s localhost:8787/v1/health | jq .
curl -s localhost:8787/v1/products | jq '.products[].internalKey'   # 期望 creator_monthly,pack_10,pack_30,pack_100
curl -s -o /dev/null -w '%{http_code}\n' "localhost:8787/v1/admin/overview?admin_token=$ADMIN_TOKEN"  # 必须 401
curl -s -o /dev/null -w '%{http_code}\n' -H "X-Admin-Token: $ADMIN_TOKEN" localhost:8787/v1/admin/overview  # 200
```

### 回滚

Go 版**不写 SQLite**，旧 Node 栈的数据文件在切换期间原封不动，所以回滚是：

1. `docker compose -f deploy/docker-compose.yml down`（Go 栈停机，PG 数据保留）；
2. 边缘代理把 `museframe.lenscript.cn` 的回源改回旧 Node 容器端口；
3. `cd /opt/museframe && docker compose up -d` 拉起旧栈。

窗口期内在 PG 侧产生的新数据（新用户 / 新任务 / 新订单）**不会回流 SQLite** ——
所以切换必须选在低流量窗口，并在回滚后按 PG 的 `created_at > 切换时刻` 导出人工补录。
边缘代理保留 `header_up -CF-Connecting-IP` 剥头动作这一条，回滚前后都不能丢。

## 未解决项

见 `9-服务器重构/45-MuseFrame后端重写报告.md` 末节。
