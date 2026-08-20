#!/bin/bash
set -e
P=$(openssl rand -base64 12 | tr -d '=+/')
sudo -u git GITEA_WORK_DIR=/var/lib/gitea /usr/local/bin/gitea admin user change-password \
  --config /etc/gitea/app.ini --username zhousodo --password "$P" --must-change-password=false >/dev/null
echo "GITEA_LOGIN zhousodo $P" | tee ~/gitea-login.txt
