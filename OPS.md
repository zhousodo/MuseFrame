# MuseFrame 运维手册（留影）

发版看 [`DEPLOY.md`](DEPLOY.md)；运营后台逐页说明看
[`server-go/docs/admin-guide.html`](server-go/docs/admin-guide.html)（凭据位置 / 7 分组 15 视图 /
37 条管理路由 / 59 个配置项 / 排障 / 备份位置）；仓库规矩看 [`AGENTS.md`](AGENTS.md)。

> **先去后台，再去 SSH。** 改配置、发额度、下架风格、改价、重试任务、重验购买、发测试邮件
> 全部在后台做，都落审计。SSH 只做后台做不到的事（换密钥、发版、翻备份）。

---

## 1. 部署形态一览

生产机与 LensCript / Zlens / 拍打等项目**同机不同栈**，统一在 `/srv/platform` 下，
互不共享容器 / 端口 / 卷。

| 组件 | 位置 / 方式 |
|---|---|
| MuseFrame API | 容器 `museframe-api-go`（compose 栈 `museframe-go`，**服务名 `api`**），绑 `127.0.0.1:18787` |
| 镜像 | `museframe-api:<UTC时间戳>-g<源码短sha>`，**禁 latest**；本地交叉编译后 `docker load`，生产机不 build |
| 配置三件套 | `/srv/platform/apps/museframe/{docker-compose.yml, project.env, app.env}` |
| 业务数据库 | 统一 PostgreSQL 容器 `platform-postgres`，库 `museframe`（24 表，跑过 006 后 25 表；三级角色，运行角色无 DDL 权；连接池硬顶 4） |
| 图片与产出 | `/srv/platform/apps/museframe/data/assets` → 容器 `/var/lib/museframe/assets`（bind mount，**唯一真本**） |
| 前端与后台静态壳 | `/srv/platform/apps/museframe/data/web` → 容器 `/var/lib/museframe/web`（只读挂载，含 `admin.html` 与 30 张封面） |
| 密钥 | `/srv/platform/apps/museframe/app.env`（600）——`ADMIN_TOKEN` / `IMAGE_PROVIDER_API_KEY` / `SMTP_PASS` / `IP_HASH_SALT` 的唯一存放处 |
| 公网入口 | Cloudflare → OpenResty（80/443）→ `127.0.0.1:18787`；`https://museframe.lenscript.cn` |
| 代码仓库 | GitHub `github.com/zhousodo/MuseFrame`（三分支：`main` / `develop` / `backend`） |

容器为什么挂**两张网**：`platform-db-net` 是 `internal=true`（不出网，只通 PG），
而上游图像 API / SMTP / Google·Apple JWKS 全走 HTTPS，所以必须另有一张可出网的 bridge
（`museframe-go-egress`）。只挂 internal 那张时容器 healthy、日志正常，但宿主机 `curl` 直接
refused，**没有任何报错**。

🔴 **旧 Node 栈（容器 `museframe-api`、`/opt/museframe`、SQLite）已停，只停不删，保留到
2026-10-11 作回滚兜底。** 它的数据文件在切换期间原封不动，但窗口期内在 PG 侧产生的新数据
不会回流 SQLite——真要回滚，必须按 `created_at > 切换时刻` 导出人工补录。

---

## 2. 常用命令（统一入口 `platformctl`，八个命令）

```bash
ssh prodsrv

sudo /srv/platform/scripts/platformctl status museframe   # 容器/健康/内存/错误日志/DB连接数/磁盘 一屏，退出码 0 = 全绿
sudo /srv/platform/scripts/platformctl status all         # 五个项目一屏
sudo /srv/platform/scripts/platformctl logs museframe     # compose logs -f
sudo /srv/platform/scripts/platformctl deploy museframe --tag <tag>   # 见 DEPLOY.md
sudo /srv/platform/scripts/platformctl rollback museframe
sudo /srv/platform/scripts/platformctl backup museframe
sudo /srv/platform/scripts/platformctl restore museframe  # 默认恢复到**临时库**；覆盖生产需 --force-production + 二次确认
sudo /srv/platform/scripts/platformctl migrate museframe  # 先 dry-run 再确认
```

退出码：`0` 成功 / `1` 一般错误 / `2` 参数错 / `3` 资源不足拒绝执行 / `4` 校验失败。
`--dry-run` 只打印将执行的动作；`--skip-guard` 会写进审计日志，别用。

开机自启：容器 `restart: unless-stopped` + Docker 服务已 enable。

---

## 3. 改配置 / 换密钥 / 换服务商

### 3.1 后台热改（47 项，立刻生效，不用重启）

后台 **系统配置** 页（`https://museframe.lenscript.cn/admin.html`，令牌见
`app.env` 的 `ADMIN_TOKEN`，读法见 admin-guide 第 1.2 节，**不要把值贴进任何地方**）：

- 图像模型接口地址 / 模型名 / 超时 / 并发 / 重试次数 / 熔断参数 / 输出尺寸与质量档
- 免费额度与四道闸（`free_units`、`free_grants_per_ip_day`、`free_grants_per_day`、
  `free_requires_auth`、`allow_guest`）、加购与免费额度的有效期
- SMTP 主机/端口/用户/发件人、验证码有效期与限流、邮箱登录开关
- Google/Apple 登录凭据、Play 包名
- **AIGC 标识**六项（文案 / 位置 / 不透明度 / 字号 / 制作者 / 显式标识开关）
- 客服邮箱与 QQ 群、数据保留期、会话有效期、单账号存储上限

每一项都带来源徽章（`db 覆盖` / `env` / `默认`），清空即回退到 env / 默认。
越界的值**回 422 而不是悄悄夹一下**，错误信息里写着合法区间。每一次写操作落审计。

### 3.2 只能改 `app.env` + 重新 deploy（12 项 + Waffo 一组）

`ADMIN_TOKEN`、`IMAGE_PROVIDER_API_KEY`、`SMTP_PASS`、`IP_HASH_SALT`、
`GOOGLE_SERVICE_ACCOUNT_JSON`、`MUSEFRAME_DATABASE_URL` 与池上限、资产/web 目录、
`TRUSTED_PROXY` / `TRUST_CF_CONNECTING_IP` / `RATE_LIMIT_MAX_KEYS`、
`ALLOW_MOCK_PURCHASES` / `ALLOW_TEST_LOGIN`、`PORT` / `HOST` / `SHUTDOWN_GRACE_SECONDS`，
以及 2026-09-23 起的 Waffo 网页端结账一组：`WAFFO_MERCHANT_ID` / `WAFFO_STORE_ID` /
🔴 `WAFFO_PRIVATE_KEY`（商户 API 私钥，与上面三个密钥同一红线）/ `WAFFO_MODE` /
`WAFFO_WEBHOOK_PUBLIC_KEY`（prod 可留空，内置）/ `WAFFO_SUCCESS_URL` / `WAFFO_API_BASE_URL`。
这一组**不在后台注册表里**（不显示、不可热改）。接入步骤见 [`DEPLOY.md`](DEPLOY.md) §6。

🔴 **后台没有任何密钥写入口**：`image_provider_api_key` 与 `smtp_pass` 在配置页只显示固定掩码，
写它们直接回 **422**。这是对 Node 版「运维从后台设了一次密钥，密钥就明文躺进 `app_config` 表
并随每日备份落盘」那个问题的修复，属**有意的行为变更**。生产 `app_config` 表实测只剩
`allow_guest` / `pack_credit_expiry_days` 两行，没有任何密钥行。

步骤见 [`DEPLOY.md`](DEPLOY.md) §2。

### 3.3 图像生成开关（后台一眼可见）

后台 **概览 / 系统配置** 顶部有一条生成链路横幅，判据是**最近真实上游调用的结果**，
不是「配置齐不齐」：

| 横幅 | 含义 | 用户侧表现 |
|---|---|---|
| ⛔ 已停用 | 缺 `image_provider_api_key` / `image_provider_base_url` | 生成按钮变灰；接口 503 `GENERATION_UNAVAILABLE`；**不预留、不扣额度** |
| ⚠️ 熔断中 | 连续 `provider_breaker_streak` 次供给类失败 | 冷却期内快速失败，不打上游、不付编译钱；冷却一过自动放一个任务探路，**恢复不需要人工动作** |
| ✅ 正常 | 远程模型已配置且最近调用成功 | 正常生成 |

要点：

- **没配上游就一张也生不出来**，这是硬门禁。API 在建任务前就拒（额度分毫不动），
  worker 启动时也会重查——上游被摘掉之前排队的任务会被判 `GENERATION_UNAVAILABLE`
  并**退回预留额度**（`credit_ledger` 的 release）。
- **要立刻断开上游**：后台把 `image_provider_base_url` 清空即可（立刻生效，无需重启）。
- `local_engine_fallback` 在 Go 版**未实现且线上恒为 false**：回落产出的是本地滤镜、不是模型结果，
  不该交付也不该计费。

---

## 4. 登录与额度

### 4.1 邮箱验证码登录

- 开关：后台 `email_login_enabled`（**同时**要求 SMTP 配齐，否则 App 仍显示「未启用」）。
- SMTP：Brevo（`smtp-relay.brevo.com:587`）。**Brevo 要求把发信服务器出口 IP 加入授权 IP**，
  否则报 `525 Unauthorized IP address`。换服务商时同理。
- 换服务商：后台改 `smtp_host/port/user/from`（口令改 `app.env` 的 `SMTP_PASS` + 重新 deploy）→
  「运营 · 发信记录 → 发送测试邮件」自检 → 完成。
- 流程：`POST /v1/auth/email/request {email}` 发码 → `POST /v1/auth/email/verify {email,code}` 登录。
  验证码 6 位、有效期 `email_code_ttl_seconds`（默认 600s）、错 `email_code_max_attempts` 次锁定、
  单次使用；同邮箱一个窗口最多签发 `email_code_max_issues_per_window` 个。

🔴 **U-2 未完成**：生产 `GET /v1/admin/email-log` 实测 `sends: 0`——这条路在生产上**一次都没走通过**。
上架前必须先在后台发一封测试邮件走通。

### 4.2 游客令牌与私有数据的边界

`requireAccount()` 把 `is_guest` 挡在可信边界上：**游客令牌永远不解锁账号数据**
（作品 / 图片 / 额度 / 生成 / 购买一律 401）。兼容旧客户端的游客交换只能写匿名遥测与登录衔接；
登录时会把在途游客账号的作品与已购额度**并进**正式账号，不丢。
生产 `allow_guest=false`、`free_requires_auth=true`。

### 4.3 免费额度四道闸

1. `free_requires_auth=true`：必须注册账号才发。
2. 单设备只发一次（`device_hash`）。
3. **单 IP 每 24 小时上限** `free_grants_per_ip_day`（默认 3）。
4. **全站每 24 小时上限** `free_grants_per_day`（默认 50）——攻击者换 IP 也吃这一刀，这是成本天花板。

发放记录在 `free_grants` 表：`device_hash` / `ip_hash` 用独立的 `IP_HASH_SALT`
（🔴 **必填且不得等于 `ADMIN_TOKEN`**，否则启动失败——Node 版回落 `ADMIN_TOKEN`，换令牌会静默
重置 24h per-IP 上限），另有 005 迁移新增的明文 `ip` 列供运营判断「这一批账号是不是同一个人」。

后台 **概览 / 系统配置** 顶部第二条横幅实时显示用量与是否触顶。
任一上限设为 `0` 或 `free_units=0` 即**完全停发**。

额度用完的用户走「手动发额度」（后台 **用户** 页，走正规账本 `source_type=manual`，可填备注与
有效期，落审计）。**MuseFrame 没有注册码 / 优惠码 / 兑换码功能**，37 条管理路由里没有任何一条与码有关。

---

## 5. 商品 / 风格 / 反馈 / 任务

| 要做的事 | 在哪 |
|---|---|
| 改价、改发放张数、上下架商品 | 后台 **内容 · 商品与价格**，立刻反映到 `/v1/products`，不用发版。🔴 人民币价清成 null = 该商品对所有中文用户整条消失 |
| 风格改名 / 改副标题 / 位次 / 付费专属 / 紧急下架 | 后台 **内容 · 风格**。「App 可见」列比「状态」更重要 |
| 看用户写的反馈正文、标「已处理」 | 后台 **运营 · 用户反馈** |
| 重试失败的生成任务 | 后台 **生成与资产 · 生成任务**（只有 `failed` / `cancelled` 能重试；**新建一条**带 `parent_job_id` 的任务并重新预留额度） |
| 补发漏入账的购买额度 | 后台 **用户与订单 · 购买记录 → 重验**（幂等；只对 `verified` 生效；**不会**重新向商店要收据） |
| 网页端付了钱额度没到（Waffo） | 先看后台 **数据库浏览 · `webhook_events`**：没有这条订单的行 = 回调没打进来（查 Waffo Dashboard → Webhooks 投递日志与边缘代理）；有行但 `error` 非空 = 订单对不上（照 error 文案处理）；有行且 `processed_at` 为空 = 我们这边事务失败，Waffo 会自动重投。`purchases` 里 `platform=waffo` 且 `status=pending` 的行是**打开了收银台但没付**的，不是故障 |
| 用户要退款（Waffo） | 在 Waffo Dashboard 里退；`refund.succeeded` 回调到达后订单自动标 `refunded`、订阅立即关、加购包**未消费**的额度自动补一笔负分录撤销（部分退款也按全额撤销，要保留额度用后台「手工发放」补回） |
| 查某个用户的全部事实 | 后台 **用户** → 点行打开「单用户纵向详情」（额度账本 / 购买 / 项目 / 任务 / 资产 / 会话六块） |

逐步骤与每个异常返回的含义见 `server-go/docs/admin-guide.html` 第 3 节。

---

## 6. 备份与恢复

| 类别 | 位置 | 节奏 |
|---|---|---|
| 库备份 | `/srv/platform/backups/db/museframe/`（角色在 `globals/`） | 每日 **02:13 UTC**：`pg_dump -Fc -Z6` + SHA-256 立即校验 + 先验后删清理 |
| 文件资产备份 | `/srv/platform/backups/files/museframe/`（`assets` 与 `web` 两个子目录） | 每日 **02:33 UTC**，硬链接增量（`rsync --link-dest`） |
| 配置备份 | `/srv/platform/backups/files/config/` | `app.env`（密钥唯一存放处）已纳入，不再是备份空白 |
| 恢复演练 | `sudo /srv/platform/scripts/restore-verify.sh --all` | 每周日 **07:08 UTC** 自动跑，日志 `/var/log/platform-restore-drill.log` |
| 备份脚本 | `/srv/platform/scripts/backup-db.sh` · `backup-files.sh` | cron 在 `/etc/cron.d/platform`，由 `install-cron.sh` 幂等生成，**不要手工编辑** |
| 备份日志 | `/var/log/platform-backup.log` · 守护脚本 `/var/log/platform-guard.log` | — |

🔴 **备份仍无异地副本**——以上全部在同一台机器上。这是 `README.md`「已知问题」里的待办。

数据不在镜像里（bind mount），重建镜像不影响数据。恢复默认进**临时库**，
覆盖生产需要 `--force-production` + 二次确认。

**保留期会删东西，别把后台当档案馆**：`events` 表（后台操作审计与发信记录也住在里面）按
`event_retention_days` 清理（下界 7 天就是为审计设的），幂等记录按 `idempotency_retention_days` 清理。
需要长期留存的结论请导出 CSV 或写进文档。

---

## 7. 安全基线（已落地，生产实测）

- **登录 / 购买全部服务端验签**（伪造被拒）；购买收据永久绑定首次领取的账号与商品，
  重放回 409 `PURCHASE_ALREADY_CLAIMED`。详见 [`STORE-READINESS.md`](STORE-READINESS.md)。
- **管理员令牌只走 `X-Admin-Token` 请求头**、恒定时间比对、存浏览器 `sessionStorage`（按标签页隔离，
  关掉即失效）。`?admin_token=` 已删，不得恢复。管理接口限流 120 次/分钟。
- **后台数据库浏览只读**：表白名单 23 张（`server_secrets` 不在其中 → 404），SQL 控制台只接受
  单条 `SELECT` / `WITH`，加只读角色 + `SET TRANSACTION READ ONLY` 两道 PG 侧保险。
- **不掩码政策**：业务字段（邮箱 / 完整用户 id / 交易号 / 设备 / IP）在页面与 CSV 导出里**完整显示**——
  这是只有管理员令牌打得开的自家后台，打码的实际代价是有人改去 SSH 上 `psql`（那条路上什么都
  看得到，还没有审计）。一个字节都不给的只有**凭据**：会话令牌、验证码与其哈希、SMTP 口令、
  上游密钥、`ADMIN_TOKEN`、`IP_HASH_SALT`、服务账号 JSON。
- **容器纵深加固**（`docker inspect museframe-api-go` 实测）：`User=1000:1000`、
  `ReadonlyRootfs=true`、`CapDrop=[ALL]`、`no-new-privileges:true`、`PidsLimit=128`、
  `mem_limit 512m`、日志滚动 10m×3、只绑回环。
  🔴 512m **不要随手调低**：理论最坏 = `MAX_SOURCE_PIXELS(40MP) × 4B × worker_concurrency(3) ≈ 480MB`。
  降到 256m 必须先调小 `MAX_SOURCE_PIXELS`，而那是**契约变更**（改变 422 `ASSET_UNSUPPORTED` 的触发阈值）。
- **未配置上游时整站拒绝生成**（见 §3.3），本地像素引擎不会顶替付费模型。
- **私人数据接口要求正式账户**，游客令牌默认关闭；免费额度四道闸仍作纵深防护（见 §4.3）。
- **开发开关双重门禁**：`ALLOW_MOCK_PURCHASES`（演示购买＝凭空发额度）与 `ALLOW_TEST_LOGIN`
  （任意邮箱冒充登录）除了 env 开关，还**必须带管理员令牌**才生效；`/v1/auth/config` 不对公众
  声明 `billing.mock`。生产实测两者均为 `false`，后台运行状态卡片会红字提示。
- **AIGC 标识**：显式水印可关（`aigc_label_enabled`，默认开，🔴 关掉有法律后果），
  隐式 EXIF/XMP 标识**没有开关**；标识失败 = 任务失败退额；不回溯历史成品。见 `AGENTS.md` §7。
