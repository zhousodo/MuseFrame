#!/bin/bash
# 按 owner 要求移除 Gitea 与博客（MuseFrame 不受影响）
set -e
echo "--- gitea ---"
sudo systemctl disable --now gitea 2>/dev/null || true
sudo rm -f /etc/systemd/system/gitea.service /usr/local/bin/gitea
sudo rm -rf /var/lib/gitea /etc/gitea /home/git
sudo userdel git 2>/dev/null || true
echo "gitea removed"
echo "--- blog ---"
( crontab -l 2>/dev/null | grep -v publish.sh | grep -v '^$' ) | crontab - || true
sudo rm -rf /srv/apps/blog /usr/local/bin/hugo
rm -f ~/gitea-login.txt ~/setup-blog-git.sh ~/fix-gitea.sh ~/reset-gitea-pass.sh ~/push-repos.sh ~/push-mf.sh ~/deploy-remote.sh
echo "blog removed"
echo "--- caddy ---"
sudo rm -f /etc/caddy/sites-enabled/blog-git.caddy
sudo caddy validate --config /etc/caddy/Caddyfile >/dev/null 2>&1 && echo "caddy valid"
sudo systemctl reload caddy && echo "caddy reloaded"
systemctl is-active museframe caddy
