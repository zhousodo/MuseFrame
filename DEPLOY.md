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

```bash
# 增量迁移全部幂等、可重复执行，且只加可空列 / 索引，所以对正在跑的旧镜像完全透明,
# 可以在发版前单独执行。
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f server-go/migrations/003_feedback_handled.sql
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f server-go/migrations/004_aigc_label.sql
psql "$OWNER_DSN" -v ON_ERROR_STOP=1 -f server-go/migrations/005_free_grant_ip.sql

# 自检：24 张表 / 58 个索引
psql "$OWNER_DSN" -tAc "SELECT count(*) FROM information_schema.tables
  WHERE table_schema='public' AND table_type='BASE TABLE';"
psql "$OWNER_DSN" -tAc "SELECT count(*) FROM pg_indexes WHERE schemaname='public';"
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
