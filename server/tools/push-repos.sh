#!/bin/bash
set -e
PASS=$(awk '{print $3}' ~/gitea-login.txt)
API=http://127.0.0.1:3000/api/v1
# create repos (idempotent: ignore 409)
curl -s -u "zhousodo:$PASS" -X POST "$API/user/repos" -H 'Content-Type: application/json' -d '{"name":"blog","private":true,"description":"Hugo 博客源码 — 提交 markdown 即发布"}' -o /dev/null -w "repo blog: %{http_code}\n"
curl -s -u "zhousodo:$PASS" -X POST "$API/user/repos" -H 'Content-Type: application/json' -d '{"name":"museframe","private":true,"description":"MuseFrame — 策展画廊式 AI 照片应用（全栈 + Android）"}' -o /dev/null -w "repo museframe: %{http_code}\n"
# push blog source
cd /srv/apps/blog
git remote remove origin 2>/dev/null || true
git remote add origin "http://zhousodo:${PASS}@127.0.0.1:3000/zhousodo/blog.git"
git add -A; git -c user.email=zhousodo@gmail.com -c user.name=zhousodo commit -qm "blog content" 2>/dev/null || true
git push -q -u origin main && echo "blog pushed"
