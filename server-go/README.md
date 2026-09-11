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

替换原 Node.js（零框架 `node:http`）+ SQLite 实现。**50 条路由**（公开 30 + 管理 20）
逐条复刻，外加一条不在公开契约里的内部只读探针 `/v1/ready`。

- 地面事实来源：`/opt/museframe/server/*.js` 只读副本（源码是行为真本）
- 契约：`9-服务器重构/11-API冻结契约.md` 第三章 + `12-契约空白补查.md` D-11~D-18
- 盘点：`9-服务器重构/盘点-20260911/03-MuseFrame盘点.md`

```
cmd/museframe-api       服务主程序（含 healthcheck 子命令）
cmd/museframe-assets    资产迁移 + sha256 回填 + 四数字交叉校验
internal/apierr         统一错误信封与 26 个错误码
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
internal/store          PostgreSQL 持久层 + 管理后台只读数据浏览
internal/worker         生成队列
migrations/             001_init.sql（24 表 / 58 索引）、002_grants.sql（角色授权）
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
| `GET /v1/admin/config` | secret 项掩码为 `••••` + 末 4 位，`source` 只可能是 `env` / `default` |

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

| 表 | 列 | 处理 |
|---|---|---|
| `server_secrets` | 整表 | 不在白名单，404 |
| `sessions` | `token` / `device_id` | 前 6 位 + 省略号 / 整体掩码 |
| `auth_identities` | `email_normalized` / `provider_subject` | 邮箱掩码 / 整体掩码 |
| `email_codes` | `code_hash` | 整体掩码 |
| `purchases` | `external_transaction_id` | 整体掩码 |
| `idempotency_records` | `response_body` / `request_hash` | 整体掩码 |
| `free_grants` | `device_hash` / `ip_hash` | 整体掩码 |
| `manual_grants` | `idempotency_key` | 整体掩码 |
| `app_config` | `value` | 该行 key 属于密钥项时 `••••(secret)` |

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

## 切换 runbook

```bash
# 0. 前置：统一 PG 已就绪，museframe 库与三个角色已建好
#    容器 platform-postgres，网络 platform-db-net

# 1. 建 schema（一次性 job，用 owner 角色；运行角色没有 DDL 权）
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f migrations/001_init.sql
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f migrations/002_grants.sql
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
