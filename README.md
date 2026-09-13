# MuseFrame — Curated image making (MVP)

A working full-stack implementation of the **MuseFrame Gallery MVP Implementation Spec v1.0**:
a curated-gallery photo app where users pick a *direction*, not a prompt — one photo in,
one identity-preserving artwork out, saved in about two minutes.

> 在本仓工作之前先读 [`AGENTS.md`](AGENTS.md)（哪份代码是活的、分支、本地门禁、发版链、红线）。

## 仓库现状（2026-09-13）

| 目录 | 状态 |
|---|---|
| **`server-go/`** | 🟢 **线上后端**。Go 1.26 + PostgreSQL（统一实例 `platform-postgres`，库 `museframe`）。容器 `museframe-api-go`，只绑 `127.0.0.1:18787` |
| **`web/`** | 🟢 线上 SPA + 运营后台静态壳。以 bind-mount 挂进容器（不在镜像里），`web/admin.html` 与生产机那一份 sha256 必须一致 |
| `server/` | 🔴 **已退役**的 Node + SQLite 实现。容器 `museframe-api` 已停（**只停不删**），保留到 **2026-10-11** 作回滚兜底。不要在这里加功能，也不要删它 |
| 根 `Dockerfile` / `docker-compose.yml` / `package.json` | 属于已退役的 Node 栈，同样保留到 10-11 |

公网入口 `https://museframe.lenscript.cn`（Cloudflare → OpenResty → 容器）。
运营后台 `https://museframe.lenscript.cn/admin.html`，逐页说明见
[`server-go/docs/admin-guide.html`](server-go/docs/admin-guide.html)。

### 本地跑起来（开发）

```bash
cd server-go
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
```

集成测试要一个 PostgreSQL（DSN 用 `museframe_owner`，夹具走 `TRUNCATE`）；
不设 `MUSEFRAME_TEST_DATABASE_URL` 时它们 **skip**，不是静默通过。详见 `AGENTS.md` §3。

退役的 Node 栈仍可本地起（`npm install && npm start`，Node ≥ 22.5），
但它读自己的 SQLite、不接生产数据，**只用于比对旧行为**。

## 分支

只有三条长期分支，平时同一个 tip：**`main`（默认，稳定）· `develop`（开发汇合）· `backend`（后端工作线）**。
临时分支用 `feat/` `fix/` `docs/` `chore/` 前缀，**合入后立刻删**。

`main` 上有 GitHub ruleset **`main-protect`**（active、无 bypass 名单）：禁 `deletion`、禁 `non_fast_forward`
——**force push 与改写历史一律被拒**。本地清 `[gone]` 分支只用 `git branch -d`，**绝不 `git worktree prune`**。

## 门禁：本仓没有 GitHub Actions

没有 `.github/workflows/`，CI 不存在。提交前自己跑：
`go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1`（在 `server-go/`），
加上 `node --check web/*.js`、`admin.html` 可解析、`git diff --check`。完整清单在 `AGENTS.md` §3。

## 发版链（生产机不 build）

本地交叉编译 → 镜像 tag `museframe-api:<UTC时间戳>-g<源码短sha>`（禁 latest）→
`docker save | ssh prodsrv 'sudo docker load'` →
改 `/srv/platform/apps/museframe/project.env` 的 `IMAGE_TAG` →
`sudo /srv/platform/scripts/platformctl deploy museframe`。
逐条命令与回滚见 `AGENTS.md` §4 与 [`DEPLOY.md`](DEPLOY.md)。

`admin.html` 是宿主机文件（bind-mount），**换它不需要发版**，但要同步并核对 sha256（`AGENTS.md` §5）。

## 已知问题 / 待办

线上跑的是 `server-go/`，所以下面按**它**的状态记。
2026-09-07 那三份审计笔记（游客会话越权 + 额度泄露 + 后台安全复审）原本是对 Node 版写的，
已归档到 [`docs/audit/`](docs/audit/)，逐条与 Go 实现的对应关系写在
[`docs/audit/README.md`](docs/audit/README.md)。

**已修（Go 版已核实）**

- 游客会话越权：`requireAccount()` 把 `is_guest` 挡在可信边界上，作品 / 图片 / 额度 / 生成 / 购买全部 401；
  图片令牌只对已注册账号的资产签发（`internal/store/queries_assets.go` 的 `is_guest = false` 过滤，
  同时撤销了历史上为游客资产签发、尚未过期的令牌）。生产 `allow_guest=false`、`free_requires_auth=true`。
- 购买收据跨账号重放：`assertPurchaseClaim()` 在 pending、订单号规范化、最终授予三处校验账号/商品绑定，
  冲突回 409 `PURCHASE_ALREADY_CLAIMED`。
- 长期会话令牌进图片 URL：网页改用一小时 `img_token`（`GET /v1/assets/img-token`）。
- 上游密钥进数据库：Go 版 secret 键只读环境变量，写入回 422；生产 `app_config` 表实测只剩
  `allow_guest` / `pack_credit_expiry_days` 两行，**没有任何密钥行**。
- 容器纵深加固：生产实测 `User=1000:1000`、`ReadonlyRootfs=true`、`CapDrop=[ALL]`、
  `no-new-privileges:true`、`PidsLimit=128`、`mem_limit 512m`。
- 畸形 `Host` 导致进程崩溃：Node 版的 `new URL(Host)` 路径在 Go 版不存在（`net/http` 自己解析请求行）。
- **U-2 SMTP 实打实发信（2026-09-13 关闭）**：生产对 Brevo（`smtp-relay.brevo.com:587` STARTTLS，
  发件人 `no-reply@lenscript.cn`）实发两封并都落账——后台测试邮件（`kind = admin_test`）与一封**真实登录验证码**
  （`kind = login_code`），两行 `ok = true`，`GET /v1/admin/email-log` 的 `sends` 由 `0` 变 `2`。
  **此前的 `sends: 0` 是误读**：发送台账 2026-09-12 才加，那之后无人发信，所以它的含义是
  「这一版上线后还没发过」而非「发过但失败了」——发信代码与配置一直是好的，无需改动。
  排障口径：失败也会落行（`ok = false` 带原因），**一行都没有**只说明还没发过；
  `email_login_enabled` 关着或 SMTP 三项缺任一会在调用 SMTP **之前**回 501，因此同样一行不落。
  手工验证挡不住以后改坏，所以同时补了 `internal/mailer` 的**会话级**测试（假 SMTP 服务端）：
  信封发件人必须是裸地址（生产 `smtp_from` 是显示名形态，整串塞进 `MAIL FROM` 会被 Brevo 501 退回）、
  纯文本 + HTML 两份正文都在信里、非 ASCII 主题走 RFC 2047、465 走隐式 TLS、端口缺省落 587，
  以及**服务端不广播 STARTTLS 时口令绝不明文上线**。

**🔴 仍是待办**

| # | 项 | 说明 |
|---|---|---|
| U-1 | **`internal/oidc`（Google/Apple ID Token）与 `internal/play`（Play 收据）从未对真实端点跑过** | 生产三个凭据当前全空，这两条路径现在回 501 `PROVIDER_NOT_CONFIGURED`。**启用任何一个之前必须先端到端联调** |
| U-3 | `seedCatalog` / `seedProducts` 刻意未实现 | 目录数据只由数据迁移管。**需拍板**：补一次性 seed 工具，还是接受「目录只由 DB 管」 |
| U-5 | 三个带 prompt compiler 的风格没做过真实生成的人工对比 | `press_cover_story_01` / `press_reportage_wash_01` / `press_zine_poster_01` |
| — | 资产 `?token=` 旧入口仍在 | `GET /v1/assets/{id}/file` 仍接受已上架客户端的会话令牌 query 参数（全站唯一一条）。当前网页不用它；旧 APK 下线后应关掉 |
| — | 备份**仍无异地副本** | 每日备份与每周恢复演练已落地（见 `OPS.md` §6），但全部在同一台机器上 |
| — | 上游 `gpt.lenscript.cn` 未端到端复测 | 9/5 起曾持续不可用，9/12 观测到变成 400 参数错，疑似恢复但没有实测确认。生产 `jobsByStatus` 只有 `succeeded: 11` |

## What's implemented (spec P0)

**Product flow** — Onboarding (3 pages) → Discover (hero exhibition + shelves) →
Exhibition → Style detail sheet → Photo import (library/camera, client-side JPEG
re-encode strips EXIF/GPS) → "Reading the image" analysis → Styles (recommendations
with reason codes + theme groups + subject filters) → Preview settings (strength /
fidelity / composition / ratio) → Generation progress (4 honest stages, leave-and-return) →
Result (hold-to-see-original, before/after compare slider, save, share, feedback) →
Refine / Try again → Projects → Profile → Paywall.

**Style system (§11)** — 24 original StyleSpecs in 6 curated exhibitions
(Quiet Portraits, Printed Matter, Dream Geography, Graphic Light, Material Studies,
Small Cinemas). Each spec is a versioned JSON asset with identity, intent,
compatibility, controls, and a pixel `pipeline`. Published StyleVersions are immutable.

**Generation (§9)** — `LocalStyleEngine` Model Adapter: decode → composition/ratio
crop → style pipeline (split-tone grade, duotone/tritone, palette map, posterize,
halftone, weave, paper grain, bloom, chroma offset, vignette…) scaled by *strength*,
identity blend by *fidelity* → quality gate (decodable, non-blank, sane dimensions) →
one automatic retry → candidate asset. Worker queue survives restarts.

**AI content labelling (中国《人工智能生成合成内容标识办法》, in force 2025-09-01)** —
every newly produced artifact is labelled at encode time by the Go backend
(`server-go/internal/aigc`): a burned-in「AI 生成」corner mark on the pixels
(operator-tunable text / position / size / opacity, on by default) **and** an
implicit label in the file metadata — JPEG EXIF + XMP carrying the GB 45438-2025
`AIGC` structure (`Label` / `ContentProducer` / `ProduceID` / …), produce time,
content hash and IPTC `DigitalSourceType=trainedAlgorithmicMedia`. The implicit
label has no off switch; historical artifacts are deliberately not rewritten.
The App shows an「AI 生成」badge driven by `candidate.aigcLabeled`, and the admin
console exposes the per-asset label state. See `server-go/README.md`.

**Billing (§13)** — append-only credit ledger with `reserve → commit / release`,
earliest-expiring bucket first, unique reference keys; free first image; mock store
(`/v1/purchases/verify`) with Mini Pack / Creator Monthly / Creator Annual; premium
style gating; paywall shown after the free save, never before value.

**API (§10)** — `/v1/auth/exchange` (registered-account sign-in; the legacy guest exchange is only for anonymous telemetry and sign-in handoff, never account data), `/v1/discover`, `/v1/styles`,
upload intents → binary PUT → idempotent complete, `/v1/assets/:id/analysis`
(heuristic subject/exposure/sharpness adapter + ranked recommendations), projects CRUD,
`/v1/generation-jobs` (Idempotency-Key required), cancel, feedback, export,
entitlements, products, purchases, `/v1/events` telemetry. Stable error codes
(`INSUFFICIENT_ENTITLEMENT`, `ASSET_UNSUPPORTED`, …) with `requestId`.

## Verified acceptance scenarios (spec §24)

- Free first image: one reserve/commit pair, balance 0, no watermark, paywall after save.
- Duplicate Idempotency-Key → same job id, single reserve.
- Two concurrent jobs racing the last unit → exactly one succeeds, other gets 402.
- Failed/rejected generations release the reserve (0 units charged).
- Failed retry never hides an earlier successful work in Projects.
- Premium style on free plan → 402 with paywall context; unlocked by Creator.

## Layout

```
server-go/                🟢 线上后端（Go 1.26 + PostgreSQL）—— 详见 server-go/README.md
  cmd/museframe-api       服务主程序（含 healthcheck 子命令）
  cmd/museframe-assets    资产迁移 + sha256 回填 + 四数字交叉校验
  internal/httpapi        路由表（公开 30 + /v1/ready + 管理 37）+ 横切中间层 + 全部 handler
  internal/aigc           AI 生成内容标识：显式水印（内嵌 GB2312 子集字体）+ 隐式 EXIF/XMP
  internal/cfgstore       运行时配置注册表（59 项 = 47 热键 + 12 只读；🔴 密钥只走环境变量）
  internal/store          PostgreSQL 持久层 + 管理后台只读数据浏览
  internal/ledger         append-only 额度台账
  internal/worker         生成队列 + 质量闸
  internal/{provider,imaging,oidc,play,mailer,ratelimit,metrics,netx,logx,apierr,config}
  migrations/             001_init.sql（24 表 / 58 索引）… 005_free_grant_ip.sql
  deploy/                 Dockerfile · Dockerfile.migrate · docker-compose.yml · project.env.example
  docs/admin-guide.html   运营后台使用指南（7 分组 / 15 视图 / 37 路由 / 59 配置项）
web/                      🟢 线上 SPA + 后台静态壳（bind-mount 进容器，不在镜像里）
  index.html · app.css · api.js · app.js · native.js · i18n.js · config.js
  admin.html              运营后台（唯一真相源：侧栏标记 + TABS 数组）
  covers/                 30 张风格封面 —— coverUrl 能否非 null 的硬依赖
server/                   🔴 已退役的 Node + SQLite 实现，保留到 2026-10-11
  index.js · api.js · db.js · ledger.js · jobs.js · styles.js · engine/
docs/
  audit/                  2026-09-07 审计笔记归档 + 与 Go 实现的逐条对应
  PRICING-2026-09.md · ops/
```

## Model adapters (spec §9.3 primary + backup)

- **Primary — RemoteImageAdapter** (live: `server-go/internal/provider`; retired Node original:
  `server/engine/remoteAdapter.js`): calls an
  OpenAI-compatible `/v1/images/edits` endpoint (image-to-image) with the source
  photo and an instruction assembled from the StyleSpec's `promptAssembly`
  (original baseDirection per style + subject rules from analysis + control
  fragments for strength/fidelity/composition + negative constraints). Sources
  are downscaled to 1024 px before sending; results are center-cropped to the
  requested output ratio. Configured from the container environment
  (`/srv/platform/apps/museframe/app.env`: `IMAGE_PROVIDER=remote`,
  base URL / API key / model, default `gpt-image-2`); base URL and model are also
  hot-editable in the admin console, **the key never is** (writes return 422).
  Typical latency 1–5 min.
- **Backup — LocalStyleEngine**: the deterministic pixel engine, and it exists only in the
  retired Node stack. In the Go backend `local_engine_fallback` is a registered flag that is
  **off in production and not implemented** (gate U-4) — a filter pass is not the model's
  output and must not be delivered, and billed, as one. Safety rejections are never
  retried locally: the job fails with `GENERATION_REJECTED` and 0 units charged.
- **No provider configured ⇒ no generation.** With `IMAGE_PROVIDER=remote`
  (the default) and no API key / base URL, the service refuses to generate at
  all: `POST /v1/generation-jobs` returns 503 `GENERATION_UNAVAILABLE` before any
  unit is reserved, the worker refuses jobs queued earlier (releasing their
  reserved units), and `/v1/discover` reports `generation.available: false` so the
  app greys out the button. The local engine never stands in for a missing key.

## Performance notes (provider latency)

The provider's `/v1/images/edits` latency is queue-driven and varies widely
(40 s – 300 s+). Measured findings: the `quality` parameter does **not** reduce
latency; input downscaling to 1024 px helps modestly. Mitigations in place:
- worker concurrency 3 (`WORKER_CONCURRENCY`) so jobs don't serialize,
- 420 s provider timeout so slow generations finish instead of failing,
- honest 1–5 min estimates surfaced before Generate and on the progress screen,
- progress screen supports leaving and returning (Projects shows live status),
- a circuit breaker (`provider_breaker_streak` / `provider_breaker_cooldown_seconds`): after N
  consecutive supply-side upstream failures the service fails fast instead of paying for more
  doomed calls, and probes with one job once the cooldown expires — no manual action needed.
  The local engine is **not** a fallback here: `local_engine_fallback` is off in production and
  the Go backend never silently delivers a filter pass as a model result.

## Style-card samples (spec §5.1)

Cards show real generated samples from `web/covers/{internal_key}.jpg`
(gradient placeholder is only a fallback). Regenerate:

```bash
node server/tools/gen-samples.js               # local engine, all missing, instant
node server/tools/gen-samples.js --remote all  # provider samples, ~2–4 min each
```

## Small Press exhibition (community-adapted styles)

Six styles adapted from MIT-licensed community skills (source and license
recorded in each StyleSpec's `provenance`; local clones in `.skills/`):
- **Zine Poster** — `LiamGvchi/gc-minimal-zine-poster`: warm scanned-paper
  field, dominant negative space, the photo re-entering as torn halftone
  fragments with one saturated accent and sparse typewriter annotations.
- **Cover Story** — `dacnay816y62-hub/fantasy-qiqiguaiguai-skill` (T1 Portrait
  Editorial): identity-preserving witty magazine-cover treatment with an
  invented generic masthead and a restrained 2–4 color palette.
- **Reportage Wash** — `serenashenn3-art/watercolor-sketch-style`: news-sketch
  ink lines + transparent watercolor washes, pastel palette, handwritten scene
  notes (no signatures/dates).
- **Ink & Seal** — `sammyteng/illustration-studio` (oriental-ink-guofeng):
  ink-wash rebuild on rice paper, gongbi contour lines, one cinnabar seal as
  the only saturated accent.
- **One Line** — `sammyteng/illustration-studio` (editorial-line): single
  continuous ink contour + one muted accent, New-Yorker-adjacent restraint.
- **Studio Hour** (premium) — `ZoeZYZY/go-photo-studio-skill`: identity-locked
  executive studio headshot (lighting/backdrop/attire upgrade, never a
  different person).

Evaluated and **rejected**: `HyperfocuSam/style-parody-poster` (recreates
existing posters/ads in their exact style — brand-mimicry risk, spec §1.4).

These styles carry their own `negativeConstraints` (designed text is allowed
where the style calls for it; real brands/logos never are).

**Prompt compiler for designed styles.** Static prompts can't reproduce what
these skills actually do — they have an LLM design each poster around the
specific photo. The prompt compiler ports that step (live:
`server-go/internal/provider`; retired Node original: `server/engine/promptCompiler.js`): before
generation, a fast vision-capable chat model (`PROMPT_COMPILER_MODEL`, default
`gpt-5.4-mini`) looks at the photo and compiles a four-paragraph image-edit
prompt — fragment/layout plan matched to the scene's actual layers, short
content-derived annotation texts, the color-anchor choice, and orientation
invariants (never rotate/flip source fragments). Falls back to the static
`promptAssembly` if the call fails. Enabled per style via
`promptAssembly.compiler: "zine" | "editorial"`.

## Deliberate MVP simplifications

- Analysis is heuristic (no face detection); labeled `heuristic-0.1`.
- Uploads are session-authenticated paths standing in for short-lived signed URLs.
- Per-job cost telemetry stores provider token usage as a proxy metric.
- **Store purchases**: the Go backend verifies real Google Play receipts through the
  `androidpublisher` API (`internal/play`). The mock path is double-gated (env
  `ALLOW_MOCK_PURCHASES` **and** an admin token) and is **off in production**.
  Apple receipt verification is not wired up.
- **Apple/Google sign-in**: the Go backend does real ID-token verification
  (`internal/oidc`: JWKS + RS256 + `iss`/`aud`/`exp`). 🔴 The three production credentials are
  currently empty, so both paths return 501 `PROVIDER_NOT_CONFIGURED` and neither has ever run
  against a real endpoint — end-to-end testing is required before enabling either (see
  「已知问题 / 待办」above). Email-code login is the working path.
- Works, private images, credits, generation and purchases all require a **registered**
  account; guest tokens unlock none of it.
