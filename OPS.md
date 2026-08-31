# MuseFrame 运维手册

服务器 `ubuntu@43.155.234.117`（腾讯云，2GB）。本机与 **LensCript 生产环境共用**，
两者各自独立、互不影响。改动只碰 MuseFrame 自己的目录 / compose 项目 / Caddy 站点文件。

## 1. 部署形态一览

| 组件 | 位置 / 方式 |
|---|---|
| MuseFrame API | Docker 容器 `museframe-api`（compose 项目 `museframe`），绑 `127.0.0.1:8787` |
| 源码 / 配置 / 数据 | `/opt/museframe/`（`server/ web/ assets/ Dockerfile docker-compose.yml .env data/`） |
| 公网入口 | Caddy 反代 → `https://museframe.lenscript.cn`（配置 `/etc/caddy/sites-enabled/museframe.caddy`） |
| 数据持久化 | `/opt/museframe/data`（SQLite + 用户图片）挂载进容器，重建不丢 |
| 密钥 | `/opt/museframe/.env`（图像 API key、SMTP、ADMIN_TOKEN…）；Play 服务账号放 `/opt/museframe/secrets/` |
| 代码仓库 | GitHub `github.com/zhousodo/MuseFrame` |

**与 LensCript 的隔离**：LensCript 是独立 compose（`lenscript-prod-*` 容器 + postgres）。
MuseFrame 是另一个 compose 项目（`museframe` 网络），不共享容器 / 端口 / 卷。
Caddy 与 goatcounter 是宿主机 systemd 服务，MuseFrame 只新增自己的 Caddy 站点文件。

## 2. 常用运维命令（都在 `/opt/museframe`）

```bash
cd /opt/museframe

# 查看状态 / 日志
sudo docker compose ps
sudo docker compose logs -f --tail=100

# 重启（改了 .env 后）
sudo docker compose restart

# 重新部署代码（git pull 或 scp 新代码后）
sudo docker compose up -d --build

# 停 / 起
sudo docker compose down
sudo docker compose up -d
```

开机自启：容器 `restart: unless-stopped` + Docker 服务已 `enable`，重启服务器自动拉起。

## 3. 改配置 / 换密钥 / 换服务商——优先用后台，无需 SSH

后台 **配置** 标签页（https://museframe.lenscript.cn/admin.html，令牌见 `.env` 的
`ADMIN_TOKEN`）可热改并**即时生效、无需重启**：

- 图像模型接口地址 / API 密钥 / 模型名
- 免费额度、并发数、超时
- **SMTP（换邮件服务商）** —— 改完用「发送测试邮件」自检
- Google/Apple 登录凭据、Play 包名

只有少数 env-only 开关（`ALLOW_MOCK_PURCHASES`、`ALLOW_TEST_LOGIN`、
`GOOGLE_SERVICE_ACCOUNT_JSON` 路径）需要改 `.env` + `docker compose restart`。

## 2.1 一句话部署（含上线后安全回归）

```bash
cd /opt/museframe && bash server/tools/deploy.sh
```

备份 `.env` → `git pull` → 关掉两个开发开关 → `docker compose up -d --build` →
**对着公网实跑一遍白嫖路径**并逐条打 PASS/FAIL（空 body 换令牌拿不拿得到额度、
演示购买/测试登录无令牌是否被拒、公众端 `billing.mock` 是否为 false）。
全绿才去后台贴回图像密钥。`data/` 全程不碰；脚本幂等，可重复跑。

`/opt/museframe` 若还不是 git 仓库，脚本会打印两种接法后退出，不乱猜。

## 3.1 图像生成开关（后台一眼可见）

后台 **概览 / 配置** 顶部有一条生成状态横幅，三种状态：

| 横幅 | 含义 | 用户侧表现 |
|---|---|---|
| ⛔ 已停用 | `IMAGE_PROVIDER=remote` 但缺 `image_provider_api_key` / `image_provider_base_url` | 生成按钮变灰「Generating is paused」；接口 503 `GENERATION_UNAVAILABLE`；**不预留、不扣额度** |
| ⚠️ 本地像素引擎 | `.env` 里显式 `IMAGE_PROVIDER=local` | 能生成，但产出是本地滤镜、不是模型结果，**不要对外开放** |
| ✅ 正常 | 远程模型已配置 | 正常生成 |

要点：

- **没配密钥就一张也生不出来**，这是硬门禁。API 在建任务前就拒（额度分毫不动），
  worker 启动时也会重查——密钥被摘掉之前排队/卡住的任务会被判 `GENERATION_UNAVAILABLE`
  并**退回预留额度**（`credit_ledger` 的 release），不会再挂着。
- **要断开上游**：后台把 `image_provider_api_key` 清空即可（立即生效，无需重启）。
  注意后台设过的值写在 `app_config` 表，此后清 `.env` 环境变量无效——必须在后台清，
  或把 `.env` 的 `IMAGE_PROVIDER` 改成别的值再重启。
- `local_engine_fallback`（默认关）：开启后远程失败会回落本地像素引擎并**照常扣费**，
  交付的不是模型结果。除非你清楚代价，否则保持关闭。

## 4. 邮箱验证码登录

- 开关：后台配置 `email_login_enabled`（当前已开）。
- SMTP：Brevo（`smtp-relay.brevo.com:587`）。**注意 Brevo 要求把发信服务器 IP
  `43.155.234.117` 加入授权 IP**，否则报 `525 Unauthorized IP address`。换服务商时同理。
- 换邮件服务商：后台改 `smtp_host/port/user/pass/from` → 发送测试邮件确认 → 完成。
- 流程：`POST /v1/auth/email/request {email}` 发码 → `POST /v1/auth/email/verify
  {email,code}` 登录。验证码 6 位、10 分钟有效、5 次错误锁定、单次使用；发码限流
  5 次/10 分钟。游客登录会自动合并（作品 + 已购点数迁移到邮箱账号）。

## 5. 登录方式与配额（后台可调）

| 登录方式 | 状态 | 依赖 |
|---|---|---|
| 游客 | 开（`ALLOW_GUEST`） | 无，按设备指纹发 1 次免费额度 |
| 邮箱验证码 | 开 | SMTP + Brevo IP 授权 |
| Google | 待配 | 后台填 `google_client_ids` |
| Apple | 待配 | 后台填 `apple_bundle_ids` |

免费额度：`free_units`（默认 1）；`free_requires_auth=true` 可要求登录后才发放。

### 5.1 免费额度的四道闸（防白嫖）

游客令牌零成本可换（`POST /v1/auth/exchange` 空 body 即可拿 token），所以只靠账号维度
限制等于没限制。现在每次发放要同时过四关，任一不过就**不发**（账号照建，仍可登录/购买）：

1. **游客必须带设备指纹**。没有 `deviceId` 一律不发——原先「没指纹就退回按 user_id 去重」
   恰恰是匿名调用者的情形，等于对着白嫖脚本敞开。
2. **同一设备 / 同一身份只发一次**（全局去重，换新账号也不再发）。
3. **单 IP 每 24 小时上限** `free_grants_per_ip_day`（默认 3）。
4. **全站每 24 小时上限** `free_grants_per_day`（默认 50）——攻击者换 IP 也吃这一刀，
   这是最终的成本天花板。用尽后自动停发，滚动窗口到点自愈。

发放记录在 `free_grants` 表（**不存原始 IP，只存加盐哈希**；盐取自 `ADMIN_TOKEN`，
轮换令牌会重置计数）。后台 **概览 / 配置** 顶部第二条横幅实时显示用量与是否触顶。
把任一上限设为 `0` 或 `free_units=0` 即**完全停发**免费额度。

上限值都可在后台热改、即时生效。要更严就把 `free_requires_auth` 打开或 `allow_guest` 关掉。

商品数量/价格/上下架：后台 **配置 → 商品管理**。风格紧急下线：**配置 → 风格管理**。

## 6. 备份与回滚

```bash
# 备份数据（SQLite + 图片）
sudo tar czf ~/museframe-data-$(date +%F).tgz -C /opt/museframe data

# 回滚到上一个镜像：重新 build 上一次的代码，或
sudo docker compose up -d --build   # 用当前 /opt/museframe 源码重建
```

数据不在镜像里（挂载卷），重建镜像不影响数据。

## 7. 安全基线（已落地）

- 登录 / 购买全部服务端验签（伪造被拒），详见 `STORE-READINESS.md`。
- 后台令牌 header 传递、图片短令牌、DB 浏览器对密钥/会话掩码、查询台禁访问凭据表。
- 容器 `mem_limit: 512m`，日志滚动 10m×3，不会拖垮同机 LensCript。
- 未配置图像密钥时整站拒绝生成（见 §3.1），本地像素引擎不会顶替付费模型。
- 免费额度四道闸（见 §5.1），游客循环白嫖已封死；成本有硬天花板。
- **开发开关双重门禁**：`ALLOW_MOCK_PURCHASES`（演示购买＝凭空发额度）与
  `ALLOW_TEST_LOGIN`（任意邮箱冒充登录，可绕过 `free_requires_auth`）现在除了
  env 开关，还**必须带管理员令牌**才生效；`/v1/auth/config` 也不再对公众声明
  `billing.mock`。仍请在生产 `.env` 里设为 `false`——启动时会打印告警，
  后台横幅也会红字提示。
