# 在本仓工作的规矩（AGENTS.md）

面向任何在这个仓库里改东西的人或 agent。先读这一页，再动手。
产品说明看 [`README.md`](README.md)，后端实现看 [`server-go/README.md`](server-go/README.md)，
运维动作看 [`OPS.md`](OPS.md) 与 [`DEPLOY.md`](DEPLOY.md)，
运营后台逐页说明看 [`server-go/docs/admin-guide.html`](server-go/docs/admin-guide.html)。

---

## 1. 哪份代码是活的

| 目录 | 状态 |
|---|---|
| `server-go/` | 🟢 **线上就是它**（Go 1.26 + PostgreSQL）。容器 `museframe-api-go`。所有后端改动写在这里 |
| `web/` | 🟢 线上的 SPA 与运营后台静态壳。`web/admin.html` 与生产机上那一份必须逐字节一致（见 §5） |
| `server/` | 🔴 **已退役**的 Node + SQLite 实现。容器 `museframe-api` 已停（**只停不删**），数据与镜像保留到 **2026-10-11** 作回滚兜底。不要在这里改功能，也不要删它 |
| 仓库根的 `Dockerfile` / `docker-compose.yml` / `package.json` | 属于已退役的 Node 栈，同样保留到 10-11。生产用的是 `server-go/deploy/` 那一套 |

**新功能一律进 `server-go/`。** 往 `server/` 里加代码等于往一个不接流量的进程里加代码。

---

## 2. 分支

只有三条长期分支，**不开第四条长期分支**：

| 分支 | 用途 |
|---|---|
| `main` | **默认分支**，稳定线。线上跑的代码从这里出 |
| `develop` | 日常开发汇合点。PR 合到这里 |
| `backend` | 后端工作线，与 `main` / `develop` 对齐 |

三条分支平时**同一个 tip**。

- 临时分支：`feat/…`、`fix/…`、`docs/…`、`chore/…`，**合入后立刻删**（GitHub 上删 + 本地 `git fetch --prune` 后 `git branch -d`）。
- 🔴 **本地清理 `[gone]` 分支只用 `git branch -d`**（合过的才删得掉）。
  **绝不跑 `git worktree prune`** —— 这台机器上有别的 worktree 在用。
- 🔴 **不 force push**。`main` 上有 GitHub ruleset **`main-protect`**（`enforcement: active`，无 bypass 名单，
  `current_user_can_bypass: never`），规则两条：
  - `deletion` —— 删不掉 `main`
  - `non_fast_forward` —— force push / 改写历史一律被拒
  想改 `main` 的历史唯一的路是先关 ruleset，那是一个需要拍板的动作，不要顺手做。
- 合并走 PR（`gh pr create` → 合到 `develop`），然后把 `main` / `backend` 快进对齐。

```bash
# 核对分支终态（应该只有三条，且同一个 sha）
git fetch --all --prune
git ls-remote --heads origin        # 只应出现 main / develop / backend
git branch -vv                      # [gone] 的用 git branch -d 删
```

---

## 3. 本地门禁（这个仓**没有** GitHub Actions）

仓库里没有 `.github/workflows/`，**CI 不存在，也不打算建**。
所以下面这几条是提交前必须自己跑的全部门禁：

```bash
# 后端（在 server-go/ 下）
cd server-go
go build ./...
go vet ./...
gofmt -l .                 # 必须无输出
go test ./... -count=1     # 不设 MUSEFRAME_TEST_DATABASE_URL 时集成测试 skip（是 skip，不是静默通过）

# 要把集成测试真正跑起来，需要一个 PostgreSQL，DSN 用 owner 角色（夹具走 TRUNCATE）：
#   psql -U museframe_owner -d <db> -f migrations/001_init.sql
#   psql -U museframe_owner -d <db> -f migrations/002_grants.sql
#   for f in migrations/00[345]_*.sql; do psql -U museframe_owner -d <db> -f "$f"; done
#   export MUSEFRAME_TEST_DATABASE_URL='postgres://museframe_owner:<pass>@127.0.0.1:5432/<db>?sslmode=disable'
#   go test ./... -count=1
```

```bash
# 前端 / 后台静态壳（仓库根）
node --check web/app.js && node --check web/api.js && node --check web/native.js \
  && node --check web/i18n.js && node --check web/config.js     # 语法
python3 -c "import html.parser,sys
p=html.parser.HTMLParser(); p.feed(open('web/admin.html',encoding='utf-8').read())"   # admin.html 能解析
grep -c 'data-view=' web/admin.html     # 侧栏 15 项
grep -n 'const TABS' -A2 web/admin.html # TABS 数组 = 后台视图的唯一真相源
git diff --check                        # 行尾空白 / 冲突标记
```

改了 `server-go/docs/admin-guide.html` 时，额外对着**线上后台**核一遍（路由数 / 视图数 / 配置项数）：

```bash
# 令牌读进变量，不要打印出来
TOK=$(ssh prodsrv 'sudo grep -m1 "^ADMIN_TOKEN=" /srv/platform/apps/museframe/app.env | cut -d= -f2-')
curl -s -H "X-Admin-Token: $TOK" https://museframe.lenscript.cn/v1/admin/config \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d['settings']),'项')"
grep -c 'a.add(' server-go/internal/httpapi/routes_admin.go   # 管理路由数
```

---

## 4. 发版链（生产机不 build）

生产机内存 1.9G，**禁止在生产机 `docker build`**。完整链条：

```bash
# 1) 本地交叉编译
cd server-go
TAG="$(date -u +%Y%m%dT%H%M%SZ)-g$(git rev-parse --short=8 HEAD)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$TAG" -o dist/museframe-api ./cmd/museframe-api

# 2) 本地打镜像（tag 固定为 <UTC时间戳>-g<源码短sha>，禁 latest）
docker build -f deploy/Dockerfile -t "museframe-api:$TAG" .

# 3) 传到生产机（生产机与 GitHub 只能经 SOCKS，ssh 别名已配好）
docker save "museframe-api:$TAG" | ssh prodsrv 'sudo docker load'

# 4) 改 project.env 的 IMAGE_TAG（或者把 --tag 交给 platformctl，它会 export 覆盖兜底）
ssh prodsrv "sudo sed -i 's/^IMAGE_TAG=.*/IMAGE_TAG=$TAG/' /srv/platform/apps/museframe/project.env"

# 5) 发版 + 验证
ssh prodsrv "sudo /srv/platform/scripts/platformctl deploy museframe --tag $TAG"
ssh prodsrv "sudo /srv/platform/scripts/platformctl status museframe"   # 退出码 0 = 全绿

# 回滚
ssh prodsrv "sudo /srv/platform/scripts/platformctl rollback museframe"
```

配置三件套都在 `/srv/platform/apps/museframe/`：

| 文件 | 装什么 |
|---|---|
| `docker-compose.yml` | 容器定义。栈名 `museframe-go`、容器名 `museframe-api-go`、**服务名是 `api`**、只绑 `127.0.0.1:18787` |
| `project.env` | platformctl 元数据：`IMAGE_REPO` / `IMAGE_TAG` / `HEALTH_URL` / `DB_NAME` / `ASSET_DIRS`…… **不放任何口令** |
| `app.env` | 运行时环境，**`ADMIN_TOKEN` / `IMAGE_PROVIDER_API_KEY` / `SMTP_PASS` 的唯一存放处**（600）。不入库、不入镜像、不入 Git |

`project.env` 只参与 compose 变量插值，**不注入容器**；容器环境严格由 compose 的 `environment:` 与 `env_file: app.env` 决定。
改了 `app.env` 不重新 deploy 不会生效。

---

## 5. `admin.html` 是 bind-mount，不在镜像里

```
宿主机 /srv/platform/apps/museframe/data/web  →  容器 /var/lib/museframe/web  (:ro)
```

这个目录里装的是 `index.html` / `app.js` / `app.css` / `api.js` / `native.js` / `config.js` /
`i18n.js` / `admin.html` / `privacy.html` 与 30 张风格封面 `covers/*.jpg`。
封面是 `/v1/styles`、`/v1/discover` 的 `coverUrl` 能否非 null 的**硬依赖**，边缘不托管它们
（试过「只挂 covers、SPA 归边缘」，结果 `/app` 与 `/admin.html` 全 404，已回滚）。

**换后台页面不需要发版**，但必须保持仓库与生产一致：

```bash
# 改完 web/admin.html，同步到生产（先备份，目录里已有 admin.html.bak-<时间戳> 的惯例）
ssh prodsrv 'sudo cp /srv/platform/apps/museframe/data/web/admin.html \
  /srv/platform/apps/museframe/data/web/admin.html.bak-$(date -u +%Y%m%dT%H%M%SZ)'
scp web/admin.html prodsrv:/tmp/admin.html
ssh prodsrv 'sudo install -m 644 /tmp/admin.html /srv/platform/apps/museframe/data/web/admin.html && rm -f /tmp/admin.html'

# 核对（两边 sha256 必须一致，这是「文档里写的导航就是线上那一份」的依据）
sha256sum web/admin.html
ssh prodsrv 'sudo sha256sum /srv/platform/apps/museframe/data/web/admin.html'
```

浏览器强制刷新即可看到新页面。

---

## 6. 红线

1. **不 force push**，不改写 `main` 历史（`main-protect` ruleset 会拒）。
2. **不跑 `git worktree prune`**。
3. **不打印任何令牌 / 密钥的值**：读进 shell 变量可以，`echo` 出来不行，贴进文档、工单、截图都不行。
   文档里只写「它在哪个文件、用什么命令读」。
4. **不改生产**：agent 对生产机只做只读核对；改配置 / 发版是人拍板后的单独动作。
5. **不删 `server/` 与旧 Node 栈**（容器只停不删，保留到 2026-10-11）。
6. **客户数据不能丢**：`data/assets` 是用户上传与产出的唯一真本；额度台账是 append-only 的分录表，
   「撤销额度」的正确做法是补一笔分录，不是去改库。
7. 提交前跑完 §3 的门禁。这个仓没有 CI 替你兜底。

---

## 7. AIGC 标识（合规，不要顺手关）

《人工智能生成合成内容标识办法》（2025-09-01 施行）要求生成服务在产出上同时加显式与隐式标识。
实现全在 `server-go/internal/aigc`，接进管线只有一处：`internal/worker/run.go` 里质量闸之后、落盘之前的 `aigc.Apply`。

- **显式**（画在像素上的角标）有开关 `aigc_label_enabled`，**默认开**。关掉是有法律后果的动作。
- **隐式**（EXIF + XMP 的 GB 45438-2025 `AIGC` 结构 + IPTC `DigitalSourceType`）**没有开关**，每张新成品都写。
- **标识失败 = 任务失败退额**，不交付没有标识的成品。
- **不回溯历史成品**（回溯要重编码已交付的图）。历史行 `assets.aigc_label` 为 NULL，后台显示「未标识，历史成品」。
- 水印文案受内嵌字体（ASCII + GB2312 子集）约束，子集外的字**保存时就被拒**。

配置项 6 个（`aigc` 组），后台「系统配置」页可热改，改完对下一个任务生效。
逐条说明见 `server-go/docs/admin-guide.html` 第 3.11 与 4.1 节。
