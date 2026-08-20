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

免费额度：`free_units`（默认 1）；`free_requires_auth=true` 可要求登录后才发放（防刷）。
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
- 生产 `.env`：`ALLOW_MOCK_PURCHASES=false`、`ALLOW_TEST_LOGIN=false`。
