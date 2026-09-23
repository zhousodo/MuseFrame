# MuseFrame 发版（留影 · Go 后端）

线上跑的是 `server-go/`（Go 1.26 + PostgreSQL），容器 `museframe-api-go`。
日常运维（改配置 / 查数据 / 备份）看 [`OPS.md`](OPS.md)；
运营后台逐页说明看 [`server-go/docs/admin-guide.html`](server-go/docs/admin-guide.html)；
仓库规矩看 [`AGENTS.md`](AGENTS.md)。

> 已退役的 Node + SQLite 栈（`server/`、根 `Dockerfile` / `docker-compose.yml`）保留到 **2026-10-11**
> 作回滚兜底，**只停不删**。它的老部署步骤（systemd / `/opt/museframe` / `npm ci`）已删，
> 需要考古去 `docs/audit/`。

---

## 0. 一眼看清发版链

```
本地交叉编译 Go 二进制
   ↓
本地 docker build → museframe-api:<UTC时间戳>-g<源码短sha>      （禁 latest）
   ↓
docker save | ssh prodsrv 'sudo docker load'                  （生产机 1.9G 内存，禁止在上面 build）
   ↓
改 /srv/platform/apps/museframe/project.env 的 IMAGE_TAG
   ↓
sudo /srv/platform/scripts/platformctl deploy museframe [--tag <tag>]
   ↓
sudo /srv/platform/scripts/platformctl status museframe        （退出码 0 = 全绿）
```

`admin.html` 与风格封面**不在这条链上**——它们是宿主机 bind-mount 的文件，换它们不需要发版（见 §4）。

---

## 1. 逐条命令

```bash
cd server-go

# 1) tag：UTC 时间戳 + 源码短 sha。两处必须一致（project.env 与 compose 的兜底默认值），
#    所以推荐把 tag 交给 platformctl --tag，它会 export IMAGE_TAG 覆盖兜底。
TAG="$(date -u +%Y%m%dT%H%M%SZ)-g$(git rev-parse --short=8 HEAD)"

# 2) 交叉编译（-X main.version 决定后台「运行状态 · 后端版本」卡片显示什么；
#    漏了这一步面板会显示字面量 "dev"。compose 另外把 IMAGE_TAG 透进容器兜底。）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$TAG" -o dist/museframe-api ./cmd/museframe-api

# 3) 本地打镜像（distroless static 基底，自带 CA 根证书——静态 Go 走 HTTPS 需要它）
docker build -f deploy/Dockerfile -t "museframe-api:$TAG" .

# 4) 送到生产机
docker save "museframe-api:$TAG" | ssh prodsrv 'sudo docker load'

# 5) 固化 tag
ssh prodsrv "sudo sed -i 's/^IMAGE_TAG=.*/IMAGE_TAG=$TAG/' /srv/platform/apps/museframe/project.env"

# 6) 发版
ssh prodsrv "sudo /srv/platform/scripts/platformctl deploy museframe --tag $TAG"

# 7) 验证
ssh prodsrv "sudo /srv/platform/scripts/platformctl status museframe"
ssh prodsrv "curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18787/v1/health"          # 200
ssh prodsrv "curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18787/v1/admin/overview"  # 401（无凭据）
curl -s -o /dev/null -w '%{http_code}\n' https://museframe.lenscript.cn/v1/health                 # 200（公网）
```

发版后去后台「系统配置 · 运行状态」看「后端版本 / 启动时间」两张卡片：
**启动时间还停在发版前 = deploy 没真换容器**。

### 回滚

```bash
ssh prodsrv "sudo /srv/platform/scripts/platformctl rollback museframe"
# 或显式回到某个旧 tag
ssh prodsrv "sudo /srv/platform/scripts/platformctl deploy museframe --tag <旧tag>"
# 日志
ssh prodsrv "sudo /srv/platform/scripts/platformctl logs museframe"
```

旧镜像都还在生产机上（`sudo docker images museframe-api`），回滚不需要重新传。

---

## 2. 配置三件套

全部在 `/srv/platform/apps/museframe/`：

| 文件 | 权限 | 装什么 | 改了要做什么 |
|---|---|---|---|
| `docker-compose.yml` | 644 | 容器定义。栈名 `museframe-go`、容器名 `museframe-api-go`、**服务名是 `api`**（`--force-recreate museframe-api` 会报 no such service）、只绑 `127.0.0.1:18787`、两张网（`platform-db-net` internal + `museframe-go-egress` 出网） | `platformctl deploy` |
| `project.env` | 600 | platformctl 元数据：`IMAGE_REPO` / `IMAGE_TAG` / `COMPOSE_PROJECT_NAME` / `SERVICE_PORT` / `HEALTH_URL` / `DB_NAME` / `HAS_ASSETS` / `ASSET_DIRS`。**不放任何口令** | `platformctl deploy` |
| `app.env` | 600 | **运行时环境**，compose 的 `env_file` 指向它。🔴 `ADMIN_TOKEN` / `IMAGE_PROVIDER_API_KEY` / `SMTP_PASS` / `IP_HASH_SALT` 的**唯一**存放处——不入库、不入镜像、不入 Git、不回显 | `platformctl deploy`（重新读 env）|

🔴 **`project.env` 只参与 compose 变量插值，不注入容器。** 容器环境严格由 compose 的
`environment:` 与经 `env_file` 载入的 `app.env` 决定。换版本改 `project.env`，换密钥改 `app.env`——
两件不同的事，两个不同的文件。

**改了 `app.env` 不重新 deploy 不会生效**：

```bash
ssh prodsrv
sudo nano /srv/platform/apps/museframe/app.env          # 权限保持 600
sudo /srv/platform/scripts/platformctl deploy museframe --tag <当前生产 tag>
sudo /srv/platform/scripts/platformctl status museframe
```

**大部分配置不需要走这条路**：59 个配置项里 47 个是后台可热改的（改完下一次使用即生效，
不用重启），只有密钥与部署级的 12 项是只读 / 必须改 `app.env`。清单见
[`server-go/docs/admin-guide.html`](server-go/docs/admin-guide.html) 第 4 节。

### 换模型接口 / API 密钥（不用重发 APK）

客户端从不接触模型供应商与密钥（spec §9.3 适配器架构）：

- **接口地址 / 模型名 / 超时 / 并发**：后台「系统配置 → 生成与上游」热改，**立刻生效**。
- **API 密钥**：🔴 后台改不了（写入回 422，这是对「密钥明文进数据库并随备份落盘」的修复）。
  改 `app.env` 的 `IMAGE_PROVIDER_API_KEY` 后重新 deploy。
- **要立刻断开上游**：把后台的 `image_provider_base_url` 清空即可——服务会在建任务前就拒
  （503 `GENERATION_UNAVAILABLE`，额度分毫不动），worker 也会释放已排队任务的预留额度。

---

## 3. 数据库迁移

库表由**一次性 job** 以 `museframe_owner` 执行；运行角色 `museframe_app` 只有 DML、**无 DDL 权**，
应用**不会**自动建表。

🔴 **2026-09-23 实测口径**（覆盖此前「`psql "$OWNER_DSN"`」的写法）：
`platformctl migrate museframe` 对本项目**不可用**——它期望 `/srv/platform/migrations/museframe/`
下有 `migrate` / `migrate.sh` / `run.sh`，该目录从未配过 runner，跑了只会报错退出（无副作用）。
容器 `platform-postgres` 里**没有 `postgres` 这个角色**，超级用户名在容器环境变量 `$POSTGRES_USER`
里（不要把它的值抄进任何文档）。可用的做法是把 SQL 拷进容器，以超级用户连上后 `SET ROLE museframe_owner`
再执行，新建的表属主仍是 `museframe_owner`（006 的 `webhook_events` 已按此实测）：

```bash
# 在本机：把迁移文件送到生产机（幂等、只加可空列 / 索引，对正在跑的旧镜像透明，可在发版前单独跑）
scp server-go/migrations/006_waffo.sql prodsrv:/tmp/006_waffo.sql

# 在生产机：前置一行 SET ROLE，再以容器内的超级用户执行；跑完把临时文件删掉
ssh prodsrv
printf '%s
' 'SET ROLE museframe_owner;' > /tmp/006_run.sql && cat /tmp/006_waffo.sql >> /tmp/006_run.sql
sudo docker cp /tmp/006_run.sql platform-postgres:/006_run.sql
sudo docker exec platform-postgres sh -c 'psql -U "$POSTGRES_USER" -d museframe -v ON_ERROR_STOP=1 -f /006_run.sql; rm -f /006_run.sql'
rm -f /tmp/006_run.sql /tmp/006_waffo.sql
# （docker cp 到容器 /tmp 下时 psql 曾报 No such file，拷到根目录 / 下则正常——照上面写即可）

# 自检（同样以容器内超级用户执行；把 SQL 写进文件再 -f，别在 ssh 参数里嵌 $$ 或引号）
#   2026-09-23 生产实测：跑完 006 是 25 张表 / 64 个索引（此前文档写 61，是基线本来就比文档多 3 个，
#   006 只新增 3 个）；products 里 pack_10 / pack_30 / pack_100 / creator_monthly 四行 waffo_product_id 非空，
#   creator_annual / mini_pack 两个未上架商品为空是预期的（网页端对它们回 501）。
SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE';
SELECT count(*) FROM pg_indexes WHERE schemaname='public';
SELECT internal_key, waffo_product_id FROM products ORDER BY internal_key;
SELECT tablename, tableowner FROM pg_tables WHERE tablename='webhook_events';   -- museframe_owner
```

新增迁移的顺序是：**先跑迁移（旧镜像照常跑），再发新镜像**。反过来会让新代码读到不存在的列。

---

## 4. 换 `admin.html` / 风格封面（不需要发版）

这些文件是宿主机的 bind-mount，不在镜像里：

```
宿主机 /srv/platform/apps/museframe/data/web  →  容器 /var/lib/museframe/web  (:ro)
```

目录里是 `index.html` / `app.js` / `app.css` / `api.js` / `native.js` / `config.js` / `i18n.js` /
`admin.html` / `privacy.html` 与 `covers/*.jpg`（30 张风格封面）。
封面是 `/v1/styles`、`/v1/discover` 的 `coverUrl` 能否非 null 的**硬依赖**，边缘不托管它们
（试过「只挂 covers、SPA 归边缘」，结果 `/app` 与 `/admin.html` 全 404，已回滚）。

```bash
# 备份（目录里已有 admin.html.bak-<UTC时间戳> 的惯例）
ssh prodsrv 'sudo cp /srv/platform/apps/museframe/data/web/admin.html \
  /srv/platform/apps/museframe/data/web/admin.html.bak-$(date -u +%Y%m%dT%H%M%SZ)'

scp web/admin.html prodsrv:/tmp/admin.html
ssh prodsrv 'sudo install -m 644 /tmp/admin.html /srv/platform/apps/museframe/data/web/admin.html && rm -f /tmp/admin.html'

# 🔴 核对：两边 sha256 必须一致
sha256sum web/admin.html
ssh prodsrv 'sudo sha256sum /srv/platform/apps/museframe/data/web/admin.html'
```

浏览器强制刷新即可。回滚就是把 `.bak-<时间戳>` 拷回去。

**换 SPA（`app.js` / `app.css` / `i18n.js` / `api.js` / `native.js` / `index.html`）同样走这个目录**，不发版：

```bash
D=/srv/platform/apps/museframe/data/web; TS=$(date -u +%Y%m%dT%H%M%SZ); SHA=$(git rev-parse --short=8 HEAD)
scp web/<f> prodsrv:/tmp/<f>
ssh prodsrv "sudo cp -p $D/<f> $D/<f>.bak-$TS-g$SHA && sudo install -m 640 -o ubuntu -g netdev /tmp/<f> $D/<f> && rm /tmp/<f>"
```

🔴 **缓存约定**：Cloudflare 对根资源缓存 4h（`max-age=14400`），`/app` 的 HTML 是 `no-cache`（DYNAMIC）。
所以**改了哪个 JS/CSS，就升它的 `?v=`**：`index.html` 里 `app.css?v=` / `app.js?v=`，`app.js` 顶部三条 import 的 `?v=`；
`api.js` 在 `app.js` 与 `native.js` 里各导入一次，**两处的 `?v=` 必须逐字一致**（不一致 = 两个模块实例 = 两份登录态）。
线上核对用带 `?cb=<epoch>` 或新 `?v=` 的 curl 比 sha256。

🔴 **边缘只把这些路径转给容器**：`/app*`、`/v1/*`、`/admin.html`、六个根资源（`app.js` `app.css` `api.js` `native.js` `config.js` `i18n.js`）。
其余（含 `/covers/*`、`/qq-group.*.png`）是**落地页的静态文件**，由边缘直发、不到容器——
往 `data/web/` 里新增文件在公网上是 404。SPA 里的 QQ 群二维码因此引用落地页自带的 `/qq-group.ba339a5b.png`
（与 `web/qq-group.png` 逐字节一致）。路由清单见 `server-go/deploy/edge-regress.py`。

- **2026-09-23 12:28Z** 网页端同步（PR「付费墙支付入口 / 退出登录 / 二维码」）：`app.js`（`?v=20260923c`）、`app.css`、`i18n.js`、
  `native.js`、`index.html`（后三个模块 `?v=20260923b`），备份 `*.bak-20260923T122638Z-g210f6946` 与 `*.bak-20260923T122837Z-gf3005254`。
  过程中曾装过一份 `covers/qq-group.png`（后证实 `/covers/*` 不到容器，已不再引用，文件留在目录里无害）。
- **2026-09-23 13:13Z** 网页端同步（价目表 v2）：`app.js` `app.css` `i18n.js` `native.js` `index.html`，模块 `?v=20260923d`，
  备份 `*.bak-20260923T131300Z-g9b571e3d`；公网带 `?v=` 的 curl 与仓库 sha256 逐个一致。
- **2026-09-23 13:16Z** 网页端同步（`?auth=1` 深链）：`app.js`（`?v=20260923e`）、`index.html`，备份 `*.bak-20260923T131633Z-g88c6292c`；
  sha256 一致。其余模块仍是 `?v=20260923d`。

---

## 5. 上架材料清单（规格 §21）

- [ ] 隐私政策页面 URL（商店必填，现挂在 `https://museframe.lenscript.cn/privacy.html`）
- [ ] 应用截图（Discover / 风格选择 / 结果对比页）
- [ ] Google Play 用 `app-release.aab`；国内商店用 `MuseFrame-release.apk`
- [ ] 签名文件 `museframe-release.keystore` + 密码（credentials 文件）永久备份 ——
      丢失将无法再更新应用
- [ ] 🔴 上架前必须先完成 `README.md`「已知问题 / 待办」里的 **U-1**（Google/Apple 登录与
      Play 收据从未对真实端点跑过）与 **U-2**（SMTP 从未真发过一封信）

安全与合规现状见 [`STORE-READINESS.md`](STORE-READINESS.md)；AIGC 标识的实现与开关见
`AGENTS.md` §7 与 `server-go/docs/admin-guide.html` 第 3.11 节。

---

## 6. Waffo Pancake 网页端结账接入（2026-09-23）

网页端「购买 / 订阅」按钮走 Waffo Pancake 的托管收银台（商户记录 MoR：税、发票、退款争议都由平台承担）。
后端只做三件事：建结账会话（`POST /v1/purchases/web/checkout`）、验签并处理回调
（`POST /v1/webhooks/waffo`）、以商户身份取消订阅（`POST /v1/purchases/web/subscription/cancel`）。
**额度只由验签通过的 webhook 发**；收银台跳回的 `?checkout=success` 只让前端轮询余额。
实现：`server-go/internal/waffo`（签名客户端 + 验签）、`internal/httpapi/{public_waffo,webhook_waffo}.go`。

**已经做好的（不用再做）**：Dashboard 上 prod 模式的四个商品与 webhook 都已登记（Raw 格式、全部 14 种事件、
URL `https://museframe.lenscript.cn/v1/webhooks/waffo`）；生产 webhook 验签公钥已内置在
`internal/waffo/keys.go`；商品号写在 `migrations/006_waffo.sql`（老四个）与 `007_pricing_v2.sql`（新三个）。
**价目表 v2（2026-09-23 起，所有者拍板；分市场定价，不是汇率换算）**，行序即 `GET /v1/products` 的行序：

| internal_key | waffo_product_id | Waffo 类型 | 张数 | USD | CNY | 网页端怎么卖 |
|---|---|---|---|---|---|---|
| `trial_3` | `PROD_1ljuEsEEljhWUDU9ReU9Js` | 一次性 | 3 | 1.99 | 9.90 | USD / CNY；**每账号限购一次**（409 `TRIAL_ALREADY_USED`） |
| `pack_10` | `PROD_3l2au9D4bKq3SWJtHOeknD` | 一次性 | 10 | 5.99 | 19.90 | USD / CNY |
| `pack_30` | `PROD_3MGCYkNJsjqqPpM8HlwvXN` | 一次性 | 30 | 12.99 | 49.00 | USD / CNY（前端标「最划算」） |
| `pack_100` | `PROD_6IYxsqbH1ql6R5ZyAoyvxA` | 一次性 | 100 | 34.99 | 129.00 | USD / CNY |
| `creator_pass_30` | `PROD_0kjLl2SI11Y9Cp4R4JntHM` | 一次性（SaaS） | 30 | 7.99 | 39.00 | **只按 CNY**（微信）；30 天 Creator + 30 张随之到期，不续费；USD 下单 422 |
| `creator_monthly` | `PROD_2V2oX2au6mKplqg3nHesbR` | 订阅 · 月 | 30 | 7.99 | (49.00，仅 Play) | 只按 USD |
| `creator_annual` | `PROD_3D1CEZRO5lunet0eHPwHpG` | 订阅 · 年（无试用） | 360 | 59.99 | — | 只按 USD；每期开始发 360 张，随该期到期（+48h 宽限） |

加购包额度永不过期（`pack_credit_expiry_days=0`）。`products.one_time=true` 只有 `creator_pass_30`：
它是订阅型商品（`UserPlan` 期间内报 creator）但走 `order.completed`，不进「管理 / 取消订阅」。
`trial_3` / `creator_pass_30` 没有 Play 商品（`google_product_id` 为 NULL），`/v1/products` 的 `platforms` 不含 `android`，原生端不展示。

商户号 `MER_4Dq9KxGzXARmX7Pm0K4968`，店铺 `STO_2gYlsri8wtsqFPiIEN6kOO`（都不是密钥）。

**上线记录（2026-09-23，勾选项附实测证据）**：

- [x] **迁移**：006 已于 2026-09-22 17:32 UTC 在生产执行（§3 的做法，不是 `platformctl migrate`）。
      实测 25 张表 / 64 个索引，`webhook_events` 属主 `museframe_owner`，四个上架商品的 `waffo_product_id` 非空。
- [x] **私钥**：所有者在 Dashboard → API 与开发 → 生产模式 建了 key **`museframe-prod-server`**（「允许再次下载私钥」已开，
      以后可在 Dashboard 重新查看），写进 `/srv/platform/apps/museframe/app.env`（600，属主 root）。
      🔴 **格式踩坑（已修）**：compose 的 `env_file` 是 dotenv 语法——值**只能一行、不要加引号、不能只贴 base64 主体**。
      第一次贴进去的是不带 `-----BEGIN/END-----` 的裸 base64 且带单引号，服务起来后日志报「私钥不可解析」；
      修成多行 PEM 又让 `docker compose config` 直接解析失败（`unexpected character "+" in variable name`），
      platformctl 在替换容器**之前**就中止，线上未受影响。正确形态是一行：
      `WAFFO_PRIVATE_KEY=-----BEGIN PRIVATE KEY-----
MII…
-----END PRIVATE KEY-----`（字面量 `
`，服务会还原）。
      写完先 `cd /srv/platform/apps/museframe && sudo docker compose config >/dev/null` 确认能解析，再 deploy。
- [x] **验签公钥**：prod 留空，用内置钥。开机日志 `webhookKey: true`。
- [x] **边缘代理**：未改平台仓；实测对 `https://museframe.lenscript.cn/v1/webhooks/waffo` 发无签名 POST 回 **401**
      （不是 404 / 502 / 超时），说明路由已通、头与请求体没有被边缘吞掉。
- [x] **发版**：镜像 `museframe-api:20260922T171022Z-g8ec4f1d5`（源码 `8ec4f1d5` = PR #5 合入 develop 的 merge），
      WSL 交叉编译 + `docker build` 后 `docker save | ssh prodsrv 'sudo docker load'`，`platformctl deploy` 三次
      （17:34 首发、17:43 带错误密钥、17:45 修正后），最后一次开机日志
      `Waffo 网页端结账 {"configured":true,"webhookKey":true,"mode":"prod"}`；
      `GET /v1/auth/config` 的 `billing` 为 `{'google': False, 'apple': False, 'mock': False, 'web': True}`。
      上一版 tag `20260912T145715Z-gbc3c8f0c` 仍在生产机，可 `platformctl rollback museframe`。
- [ ] **回调连通性**（等 Waffo 审核通过后做）：Dashboard → Settings → Webhooks → Send Test Event。🔴 测试事件永远用 **Test** 钥签，
      生产服务（内置 prod 钥）会回 **401** —— 这是**预期的**，只证明「路径通、边缘没吞头没改体」；
      看到 401 而不是 404 / 502 / 超时就算通。投递日志里的响应体应是 `AUTH_INVALID` 的错误信封。
- [ ] **真实购买验证**（所有者 2026-09-23 明确：**暂不做**买一笔再退一笔的测试；审核通过后由首笔真实订单代替）：
      期望链路：`purchases` 出现 `platform=waffo, status=pending` 行 → 付款 → 数秒内 `webhook_events`
      出现 `order.completed`（`processed_at` 非空、`error` 为空）→ 该 `purchases` 行变 `verified`、
      `provider_order_id` 填上 → 用户余额 +N。退款时：`refund.succeeded` 到达，行变 `refunded`，
      未消费额度被一笔 `refund` 负分录撤销（后台 · 用户详情 · 额度账本可见）。
- [ ] **订阅验证**（可选，同上等首笔真实订阅）：`subscription.activated` + `payment_succeeded` 两条都到、
      `expires_at` = 本期末 + 48h 宽限、发 30 张（键 `grant:purchase:<id>:<periodEnd>`）→ 在网页端
      「管理订阅 · 取消」→ `subscription.canceling` 到达（权益保留）→ 期末 `subscription.canceled`
      到达后行变 `canceled`、`expires_at` 压到当时，plan 回 free。

- [x] **价目表 v2 上线（2026-09-23 13:08–13:17 UTC）**：迁移 `007_pricing_v2.sql` 13:08Z 在生产执行（§3 做法，`rc=0`；
      pack 三行的新价协调人已先手工 UPDATE 过，007 的绝对赋值与之一致）；自检 25 张表 / 64 个索引不变，8 行商品与上表一致
      （`mini_pack` 仍下架）。镜像 **`museframe-api:20260923T131149Z-g9b571e3d`**（源码 `9b571e3d`），`platformctl deploy` 13:12:30Z
      成功（健康检查 3s），开机日志 `configured:true, webhookKey:true, mode:prod`；公网 `/v1/health` 200、`billing.web=true`、
      `GET /v1/products` 7 行新价新序、无签名 webhook 401、匿名 checkout 401。SPA 同步见 §4 的 13:13Z / 13:16Z 两行。
      上一版 tag `20260923T123244Z-g01c245d2` 仍在生产机，可 rollback（回滚镜像不认识 `one_time`，但列有默认值、不会报错）。
      **未做真实购买**（按所有者要求）。

**Waffo 侧状态（2026-09-23）**：店铺 `STO_2gYlsri8wtsqFPiIEN6kOO` 业务详情已提交，**审核中（1–3 个工作日，邮件通知）**；
审核通过前 `create-session` 会被 Waffo 以 403 `Store is not approved for production payments` 拒绝，网页端付费墙会在弹窗里提示
「支付通道正在审核中，请稍后再试」，这不是我们的故障。后端把上游 403 翻成 `503 PAYMENTS_NOT_READY`（2026-09-23 PR 引入，**需要发版才生效**；
发版前线上仍回 `503 VERIFICATION_UNAVAILABLE`「rejected the checkout」，前端对这句原话做了兼容，提示相同）。域名验证已通过（`/.well-known/waffo-verify.txt`，由 lenscript-site 仓发布），
网站自检「未发现明显问题」。完整交接见 [`docs/HANDOVER-2026-09-23-waffo.md`](docs/HANDOVER-2026-09-23-waffo.md)。

**排障**：`OPS.md` §5 表里有「网页端付了钱额度没到」与「用户要退款」两行。
`webhook_events` 表能在后台「数据库浏览」里翻。回调若 401，先核对 `WAFFO_MODE` 与事件 `mode` 是否一致、
边缘是否改写了请求体。回调若 5xx，Waffo 会自动重投（最多 4 次），`processed_at` 为空的行会被重新处理。
