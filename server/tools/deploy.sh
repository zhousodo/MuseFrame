#!/bin/bash
# MuseFrame 部署 + 上线后安全回归。在服务器上以 ubuntu 运行：
#
#   cd /opt/museframe && bash server/tools/deploy.sh
#
# 做四件事，全部幂等、可重复跑：
#   1. 备份 .env（含密钥，600），data/ 一律不碰
#   2. 取新代码（git checkout 就 pull；否则告诉你怎么放代码后退出，不乱猜）
#   3. 关掉两个开发开关（ALLOW_MOCK_PURCHASES / ALLOW_TEST_LOGIN）
#   4. 重建容器，然后**对着公网实跑一遍白嫖路径**，逐条打 PASS/FAIL
set -uo pipefail
APP=/opt/museframe
BASE=${BASE:-https://museframe.lenscript.cn}
cd "$APP" || { echo "找不到 $APP"; exit 1; }

say() { printf '\n=== %s ===\n' "$1"; }
fail=0
check() { # check <名称> <期望> <实际>
  if [ "$2" = "$3" ]; then printf '  PASS  %-46s %s\n' "$1" "$3"
  else printf '  FAIL  %-46s 期望 %s，实际 %s\n' "$1" "$2" "$3"; fail=1; fi
}

say "1. 备份 .env"
BAK=".env.bak-$(date +%Y%m%d-%H%M%S)"
cp -a .env "$BAK" && chmod 600 "$BAK" && echo "已备份 → $APP/$BAK（含密钥，600）"

say "2. 取新代码"
if [ -d .git ]; then
  git fetch --all --prune || { echo "git fetch 失败"; exit 1; }
  BR=$(git rev-parse --abbrev-ref HEAD)
  git pull --ff-only || { echo "快进合并失败：服务器上有本地改动。先 git stash 或 git reset --hard，再重跑。"; exit 1; }
  echo "当前 $BR @ $(git rev-parse --short HEAD)"
else
  cat <<'EOF'
/opt/museframe 不是 git 仓库，脚本不去猜你的代码怎么放的。二选一后重跑本脚本：

  A) 就地变成 git 仓库（推荐，以后一句 git pull 就能更新）：
       cd /opt/museframe
       git init && git remote add origin https://github.com/zhousodo/MuseFrame.git
       git fetch origin main && git reset --hard origin/main   # .env 与 data/ 已被 .gitignore 保护

  B) 从本机推包：
       Windows: cd D:\MuseFrame\museframe && tar czf mf.tgz server web package.json Dockerfile docker-compose.yml
       scp mf.tgz ubuntu@43.155.234.117:/tmp/
       服务器: cd /opt/museframe && tar xzf /tmp/mf.tgz
EOF
  exit 1
fi

say "3. 关掉开发开关"
for k in ALLOW_MOCK_PURCHASES ALLOW_TEST_LOGIN; do
  if grep -q "^${k}=" .env; then sed -i "s|^${k}=.*|${k}=false|" .env; else printf '%s=false\n' "$k" >> .env; fi
  echo "  ${k}=false"
done

say "4. 重建容器"
sudo docker compose up -d --build || { echo "compose 构建失败"; exit 1; }
for i in $(seq 1 25); do
  sleep 3
  curl -fsS http://127.0.0.1:8787/v1/health >/dev/null 2>&1 && { echo "健康检查通过（${i}0 秒内）"; break; }
  [ "$i" = 25 ] && { echo "健康检查超时，看日志：sudo docker compose logs --tail=80"; exit 1; }
done
sudo docker compose ps

say "5. 上线后安全回归（对公网实跑）"
ADMIN=$(grep -E '^ADMIN_TOKEN=' .env | cut -d= -f2-)
jqv() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null || echo ERR; }

# 5.1 §一 的原始复现路径：空 body 换令牌，应当拿不到任何额度
TOK=$(curl -fsS -X POST "$BASE/v1/auth/exchange" -H 'Content-Type: application/json' -d '{}' | jqv "['accessToken']")
U=$(curl -fsS "$BASE/v1/entitlements/me" -H "Authorization: Bearer $TOK" | jqv "['availableUnits']")
check "空 body 换令牌拿到的免费额度" "0" "$U"

# 5.2 演示购买：没有管理员令牌必须拒
S=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/purchases/verify" \
      -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
      -d '{"platform":"web","productKey":"mini_pack","transactionId":"probe"}')
check "演示购买（无管理员令牌）被拒" "422" "$S"

# 5.3 测试登录：没有管理员令牌必须拒
S=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/auth/exchange" \
      -H 'Content-Type: application/json' -d '{"provider":"dev","email":"probe@example.com"}')
check "测试登录（无管理员令牌）被拒" "403" "$S"

# 5.4 公众看到的 billing.mock 必须是 false
M=$(curl -fsS "$BASE/v1/auth/config" | jqv "['billing']['mock']")
check "公众端 billing.mock" "False" "$M"

# 5.5 未配置图像密钥时，生成必须是「不可用」而不是悄悄回落本地引擎
A=$(curl -fsS "$BASE/v1/discover" | jqv "['generation']['available']")
echo "  INFO  generation.available = $A （贴回密钥前应为 False；贴回后应为 True）"

# 5.6 后台自检
if [ -n "$ADMIN" ]; then
  curl -fsS "$BASE/v1/admin/config" -H "X-Admin-Token: $ADMIN" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);print('  INFO  生成:',d['generation']);print('  INFO  防护:',d['abuse'])"
fi

say "结果"
if [ "$fail" = 0 ]; then
  echo "全部 PASS。可以去 https://museframe.lenscript.cn/admin.html 配置页把图像密钥填回去了。"
  echo "填回后免费额度的最坏日成本 = free_grants_per_day × free_units（默认 50 × 1），后台可随时调小或归零。"
else
  echo "有 FAIL——先别贴密钥。查 sudo docker compose logs --tail=100"
  exit 1
fi
