#!/bin/bash
# Blog (Hugo + PaperMod) + code hosting (Gitea) on the app server. Idempotent.
set -e
IP="43.155.234.117"

echo "=== 1. Gitea ==="
if ! command -v /usr/local/bin/gitea >/dev/null; then
  GITEA_VER=1.24.7
  curl -fsSL -o /tmp/gitea "https://dl.gitea.com/gitea/${GITEA_VER}/gitea-${GITEA_VER}-linux-amd64"
  sudo mv /tmp/gitea /usr/local/bin/gitea && sudo chmod +x /usr/local/bin/gitea
fi
sudo useradd --system --create-home --home-dir /home/git --shell /bin/bash git 2>/dev/null || true
sudo mkdir -p /var/lib/gitea/{custom,data,log} /etc/gitea
sudo chown -R git:git /var/lib/gitea
sudo tee /etc/gitea/app.ini >/dev/null <<EOF
APP_NAME = Zhousodo Code
RUN_USER = git
RUN_MODE = prod

[server]
HTTP_ADDR = 127.0.0.1
HTTP_PORT = 3000
ROOT_URL = http://git.${IP}.nip.io/
DISABLE_SSH = true
OFFLINE_MODE = true

[database]
DB_TYPE = sqlite3
PATH = /var/lib/gitea/data/gitea.db

[repository]
ROOT = /var/lib/gitea/data/repos

[service]
DISABLE_REGISTRATION = true

[security]
INSTALL_LOCK = true

[log]
ROOT_PATH = /var/lib/gitea/log
LEVEL = Warn
EOF
sudo chown -R git:git /etc/gitea
sudo tee /etc/systemd/system/gitea.service >/dev/null <<'EOF'
[Unit]
Description=Gitea
After=network.target

[Service]
User=git
ExecStart=/usr/local/bin/gitea web --config /etc/gitea/app.ini
Restart=always
Environment=HOME=/home/git
MemoryMax=400M

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload && sudo systemctl enable --now gitea
sleep 4
echo "gitea: $(systemctl is-active gitea)"

echo "=== 2. Gitea admin user ==="
GITEA_PASS=$(openssl rand -base64 12 | tr -d '=+/')
sudo -u git /usr/local/bin/gitea admin user create --config /etc/gitea/app.ini \
  --username zhousodo --password "${GITEA_PASS}" --email zhousodo@gmail.com --admin 2>/dev/null \
  && echo "GITEA_LOGIN zhousodo ${GITEA_PASS}" || echo "GITEA_LOGIN exists (password unchanged)"

echo "=== 3. Hugo ==="
if ! command -v /usr/local/bin/hugo >/dev/null; then
  HUGO_VER=0.147.9
  curl -fsSL -o /tmp/hugo.tgz "https://github.com/gohugoio/hugo/releases/download/v${HUGO_VER}/hugo_extended_${HUGO_VER}_linux-amd64.tar.gz"
  sudo tar xzf /tmp/hugo.tgz -C /usr/local/bin hugo && rm /tmp/hugo.tgz
fi
/usr/local/bin/hugo version | head -1

echo "=== 4. Blog scaffold ==="
sudo mkdir -p /srv/apps/blog && sudo chown ubuntu:ubuntu /srv/apps/blog
cd /srv/apps/blog
if [ ! -f hugo.toml ]; then
  /usr/local/bin/hugo new site . --force >/dev/null
  git init -q -b main
  git -c user.email=zhousodo@gmail.com -c user.name=zhousodo submodule add -q --depth 1 https://github.com/adityatelange/hugo-PaperMod themes/PaperMod
  cat > hugo.toml <<EOF
baseURL = "http://blog.${IP}.nip.io/"
languageCode = "zh-cn"
title = "Zhousodo"
theme = "PaperMod"

[params]
defaultTheme = "auto"
ShowReadingTime = true
ShowCodeCopyButtons = true

[params.homeInfoParams]
Title = "Zhousodo 的博客"
Content = "记录 MuseFrame 与日常开发。"
EOF
  mkdir -p content/posts
  cat > content/posts/hello.md <<'EOF'
---
title: "开张：MuseFrame 上线记"
date: 2026-08-20T22:00:00+08:00
tags: ["museframe", "changelog"]
---

这台服务器现在跑着三样东西：LensCript、MuseFrame 的后端，以及你正在读的这个博客。

MuseFrame 是一个策展画廊式的 AI 照片应用：不写提示词，只选方向 —— 30 种
风格（版画、水墨、胶片、Zine 拼贴、杂志封面……），两分钟把一张照片变成
保留身份的作品。

博客源码托管在本机的 Gitea 里，提交 markdown 即发布。
EOF
fi
/usr/local/bin/hugo --quiet --destination public
echo "blog built: $(ls public/index.html && du -sh public | cut -f1)"

echo "=== 5. Blog repo in Gitea + auto publish ==="
cd /srv/apps/blog
git add -A >/dev/null 2>&1 || true
git -c user.email=zhousodo@gmail.com -c user.name=zhousodo commit -qm "blog init" 2>/dev/null || true
cat > /srv/apps/blog/publish.sh <<'EOF'
#!/bin/bash
# git pull（若远端在 Gitea）并重建博客；由 cron 每 5 分钟执行
cd /srv/apps/blog
git pull -q origin main 2>/dev/null || true
/usr/local/bin/hugo --quiet --destination public
EOF
chmod +x /srv/apps/blog/publish.sh
( crontab -l 2>/dev/null | grep -v publish.sh; echo "*/5 * * * * /srv/apps/blog/publish.sh" ) | crontab -
echo "publish cron installed"
