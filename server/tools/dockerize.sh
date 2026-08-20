#!/bin/bash
# Migrate MuseFrame from bare systemd node → Docker Compose, isolated from the
# LensCript stack. Idempotent-ish; safe to re-run. Run as ubuntu on the server.
set -e
cd /opt/museframe

echo "=== 1. 整理目录 ==="
rm -rf tmp-stage app.log .env.prod _admin_script.js 2>/dev/null || true
mkdir -p secrets
rm -f ~/museframe-server.tar.gz ~/*.bundle ~/push-mf.sh ~/deploy-remote.sh 2>/dev/null || true
ls -la

echo "=== 2. 停用旧的 systemd 服务（保留文件以便回滚） ==="
sudo systemctl stop museframe 2>/dev/null || true
sudo systemctl disable museframe 2>/dev/null || true
# also remove the @reboot crontab fallback from the pre-docker era
( crontab -l 2>/dev/null | grep -v 'museframe' ) | crontab - 2>/dev/null || true

echo "=== 3. 构建镜像并启动容器 ==="
sudo docker compose up -d --build

echo "=== 4. 等待健康检查 ==="
for i in $(seq 1 20); do
  sleep 3
  if curl -fsS http://127.0.0.1:8787/v1/health >/dev/null 2>&1; then echo "健康检查通过"; break; fi
done

echo "=== 5. 状态 ==="
sudo docker compose ps
echo "--- museframe 容器（与 lenscript 隔离，各自独立 compose 项目）---"
sudo docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}' | grep -iE 'muse|lens|NAMES'
echo "--- 内存占用 ---"
free -m | head -2
curl -s http://127.0.0.1:8787/v1/health
echo
