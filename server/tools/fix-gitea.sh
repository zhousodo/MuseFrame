#!/bin/bash
set -e
echo "--- app.ini head ---"
sudo head -8 /etc/gitea/app.ini
grep -q "^WORK_PATH" /etc/gitea/app.ini || sudo sed -i '3i WORK_PATH = /var/lib/gitea' /etc/gitea/app.ini
echo "--- unit ---"
grep -q "GITEA_WORK_DIR" /etc/systemd/system/gitea.service || sudo sed -i 's|^ExecStart=.*|Environment=GITEA_WORK_DIR=/var/lib/gitea\nWorkingDirectory=/var/lib/gitea\n&|' /etc/systemd/system/gitea.service
sudo head -12 /etc/systemd/system/gitea.service
sudo systemctl daemon-reload
sudo systemctl reset-failed gitea
sudo systemctl restart gitea
sleep 5
systemctl is-active gitea
curl -s -o /dev/null -w "gitea http:%{http_code}\n" -m 5 http://127.0.0.1:3000
GITEA_PASS=$(openssl rand -base64 12 | tr -d '=+/')
sudo -u git GITEA_WORK_DIR=/var/lib/gitea /usr/local/bin/gitea admin user create \
  --config /etc/gitea/app.ini --username zhousodo --password "$GITEA_PASS" \
  --email zhousodo@gmail.com --admin --must-change-password=false \
  && echo "GITEA_LOGIN: zhousodo / $GITEA_PASS"
