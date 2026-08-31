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
pass() { printf '  PASS  %-40s %s\n' "$1" "$2"; }
bad()  { printf '  FAIL  %-40s %s\n' "$1" "$2"; fail=1; }
check() { [ "$2" = "$3" ] && pass "$1" "$3" || bad "$1" "期望 $2，实际 $3"; }

say "1. 备份 .env"
BAK=".env.bak-$(date +%Y%m%d-%H%M%S)"
cp -a .env "$BAK" && chmod 600 "$BAK" && echo "已备份 → $APP/$BAK（含密钥，600）"

say "2. 取新代码"
if [ -d .git ]; then
  git fetch --all --prune || { echo "git fetch 失败"; exit 1; }
  git pull --ff-only || { echo "快进合并失败：服务器上有本地改动。先 git stash 或 git reset --hard origin/main 再重跑。"; exit 1; }
  echo "当前 $(git rev-parse --abbrev-ref HEAD) @ $(git rev-parse --short HEAD)"
else
  cat <<'EOF'
/opt/museframe 不是 git 仓库，脚本不去猜你的代码怎么放的。就地接上 GitHub 后重跑本脚本：

  cd /opt/museframe
  git init -b main && git remote add origin https://github.com/zhousodo/MuseFrame.git
  git fetch --depth 1 origin main && git reset --hard origin/main
  git branch --set-upstream-to=origin/main main

.env / data/ / secrets/ 都在 .gitignore 或未跟踪，reset --hard 不会碰。
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
  curl -fsS http://127.0.0.1:8787/v1/health >/dev/null 2>&1 && { echo "健康检查通过"; break; }
  [ "$i" = 25 ] && { echo "健康检查超时：sudo docker compose logs --tail=80"; exit 1; }
done
sudo docker compose ps

say "5. 上线后安全回归（对公网实跑）"
ADMIN=$(grep -E '^ADMIN_TOKEN=' .env | cut -d= -f2-)
jqv() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null || echo ERR; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# 5.1 文档 §一 的原始复现路径：空 body 换令牌。两种可接受的结局——换不到令牌
#     （游客整体关闭），或换到了但一张免费额度都拿不到。
EXBODY=$(curl -s -X POST "$BASE/v1/auth/exchange" -H 'Content-Type: application/json' -d '{}')
EXCODE=$(code -X POST "$BASE/v1/auth/exchange" -H 'Content-Type: application/json' -d '{}')
TOK=""
if [ "$EXCODE" = "200" ]; then
  TOK=$(printf '%s' "$EXBODY" | jqv "['accessToken']")
  check "空 body 换令牌拿到的免费额度" "0" "$(curl -fsS "$BASE/v1/entitlements/me" -H "Authorization: Bearer $TOK" | jqv "['availableUnits']")"
elif [ "$EXCODE" = "403" ]; then
  pass "空 body 换令牌被拒" "403 游客已整体关闭，比发 0 张更严"
else
  bad "空 body 换令牌" "意外的 HTTP $EXCODE"
fi

# 5.2 演示购买：关键性质是「永远不会成功发额度」，与此刻拿不拿得到令牌无关。
AUTH=()
[ -n "$TOK" ] && AUTH=(-H "Authorization: Bearer $TOK")
S=$(code -X POST "$BASE/v1/purchases/verify" "${AUTH[@]}" -H 'Content-Type: application/json' -d '{"platform":"web","productKey":"mini_pack","transactionId":"probe"}')
[ "$S" = "200" ] && bad "演示购买（无管理员令牌）" "200 竟然发放了额度" || pass "演示购买（无管理员令牌）被拒" "HTTP $S"

# 5.3 测试登录：无管理员令牌必须拒
S=$(code -X POST "$BASE/v1/auth/exchange" -H 'Content-Type: application/json' -d '{"provider":"dev","email":"probe@example.com"}')
[ "$S" = "200" ] && bad "测试登录（无管理员令牌）" "200 竟然登录成功" || pass "测试登录（无管理员令牌）被拒" "HTTP $S"

# 5.4 公众看到的 billing.mock 必须是 false
check "公众端 billing.mock" "False" "$(curl -fsS "$BASE/v1/auth/config" | jqv "['billing']['mock']")"

# 5.5 本地像素引擎不该对公众开着：它出的是滤镜图不是模型结果，而且 IMAGE_PROVIDER
#     是 env-only，停在 local 的话你在后台贴回密钥根本不会生效。
MODE=$(curl -fsS "$BASE/v1/admin/config" -H "X-Admin-Token: $ADMIN" | jqv "['generation']['mode']")
[ "$MODE" = "local" ] && bad "生成模式" "local（滤镜冒充模型，且后台贴密钥不生效）" || pass "生成模式不是 local" "$MODE"

# 5.6 供人工确认
echo "  INFO  generation.available = $(curl -fsS "$BASE/v1/discover" | jqv "['generation']['available']") （贴回密钥前应为 False，贴回后应为 True）"
[ -n "$ADMIN" ] && curl -fsS "$BASE/v1/admin/config" -H "X-Admin-Token: $ADMIN" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print('  INFO  生成:',d['generation']);print('  INFO  防护:',d['abuse'])"

say "结果"
if [ "$fail" = 0 ]; then
  echo "全部 PASS。可以去 $BASE/admin.html 配置页把图像密钥填回去了。"
  echo "填回后免费额度的最坏日成本 = free_grants_per_day × free_units，后台可随时调小或归零。"
else
  echo "有 FAIL——先别贴密钥。查 sudo docker compose logs --tail=100"
  exit 1
fi
