# MuseFrame 后端部署与运维

## 日常运维速查

### 更换模型接口 / API 密钥（不用重发 APK）

客户端从不接触模型供应商和密钥（spec §9.3 适配器架构），一切都在服务端 `.env`：

```bash
ssh ubuntu@43.155.234.117
vim /opt/museframe/.env        # 改 IMAGE_PROVIDER_BASE_URL / IMAGE_PROVIDER_API_KEY / IMAGE_PROVIDER_MODEL
sudo systemctl restart museframe
curl -s https://museframe.lenscript.cn/v1/health   # 确认服务回来了
```

生效范围：重启后的所有新生成任务。已排队任务会用旧配置跑完或失败重试。
换供应商时若模型名不同，同步改 `IMAGE_PROVIDER_MODEL`（图生图）和
`PROMPT_COMPILER_MODEL`（vision 编译，需支持 chat + 图片输入）。

### 调整用户配额 / 定价

| 想改什么 | 改哪里 | 生效方式 |
|---|---|---|
| 新用户免费张数 | `.env` 的 `FREE_UNITS`（默认 1）| 重启后对**新注册用户**生效 |
| 订阅/点数包的张数与价格 | `server/styles.js` 的 `PRODUCTS` | 重启后自动 upsert 商品表 |
| 每张消耗几 unit | `server/api.js` 生成路由的 `units` | 重启 |

账本是 append-only 的：已购用户保留已发放的 units，改动只影响之后的发放。

---


APK 内置的 API 地址是 `http://43.156.121.139:8787`（见 `web/config.js`）。
把本包部署到该服务器后，APK 即可在任何网络环境使用。

## 步骤（Ubuntu/Debian，root）

```bash
# 1. 安装 Node.js 22（内置 SQLite 需要 >= 22.5）
curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && apt-get install -y nodejs

# 2. 解包
mkdir -p /opt/museframe && tar xzf museframe-server.tar.gz -C /opt/museframe
cd /opt/museframe

# 3. 安装依赖（仅 jpeg-js / pngjs 两个纯 JS 包）
npm ci --omit=dev

# 4. 检查 .env（已含模型接口配置；如代理就在本机可改为 http://127.0.0.1）

# 5. systemd 常驻
cat > /etc/systemd/system/museframe.service <<'EOF'
[Unit]
Description=MuseFrame API
After=network.target

[Service]
WorkingDirectory=/opt/museframe
ExecStart=/usr/bin/node server/index.js
Restart=always
Environment=PORT=8787

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now museframe

# 6. 放行端口（云控制台安全组同样要放行 8787/TCP）
ufw allow 8787/tcp 2>/dev/null || true

# 7. 验证
curl http://127.0.0.1:8787/v1/health
```

## 首次启动后

- 目录 `data/` 自动创建（SQLite + 用户图片）。定期备份 `data/`。
- 样片：把本地 `web/covers/` 一并解包（包内已带 30 张成品样片）。

## 强烈建议（应对商店审核）

明文 HTTP 大概率过不了 Google Play / 部分国内商店的审核项：

1. 给服务器配域名（如 `api.museframe.app`）
2. `nginx + certbot` 反代 8787 并签发 HTTPS
3. 改 `web/config.js` 的 `apiBase` 为 `https://api.museframe.app`
4. 移除 AndroidManifest 的 `usesCleartextTraffic`，重新构建 APK/AAB

## 上架材料清单（规格 §21）

- [ ] 隐私政策页面 URL（商店必填；可先挂在同一域名下）
- [ ] 应用截图（Discover / 风格选择 / 结果对比页）
- [ ] Google Play 用 `app-release.aab`；国内商店用 `MuseFrame-release.apk`
- [ ] 签名文件 `museframe-release.keystore` + 密码（credentials 文件）永久备份 —
      丢失将无法再更新应用
