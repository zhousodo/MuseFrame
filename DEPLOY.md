# MuseFrame 后端部署（上架前必须完成）

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
