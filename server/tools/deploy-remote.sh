#!/bin/bash
# MuseFrame remote finalize: systemd + caddy route. Idempotent.
set -e
sudo mv -f ~/museframe.service /etc/systemd/system/museframe.service
sudo systemctl daemon-reload
sudo systemctl enable --now museframe
sleep 2
echo "museframe: $(systemctl is-active museframe)"
sudo mv -f ~/museframe.caddy /etc/caddy/sites-enabled/museframe.caddy
sudo caddy validate --config /etc/caddy/Caddyfile >/dev/null 2>&1 && echo "caddy config: valid"
sudo systemctl reload caddy
echo "caddy: reloaded"
curl -s http://127.0.0.1:8787/v1/health
echo
curl -s -m 5 http://127.0.0.1/mf/v1/health -H "Host: 43.155.234.117"
echo
