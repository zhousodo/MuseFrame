#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""MuseFrame 边缘路由回归 —— 覆盖 museframe.caddy 每一个 matcher 的路径形态。

背景（为什么要有这个脚本）：
  2026-09-11 第一次把 MuseFrame 从 Node 切到 Go 时，用的是一份 53 条纯 /v1/* 的
  API 回归，**全绿**，线上照样炸：炸的是 /app（@app 把请求 rewrite 成 /index.html
  再打后端，而 Go 的 http.ServeFile 对以 /index.html 结尾的 URL 无条件 301）。
  教训：回归集必须按**边缘配置的 matcher** 枚举，而不是按 API 清单枚举。

三种模式：
  shadow   —— 不切流量，同时打旧 Node(:8787) 与新 Go(:18787)，逐条比状态码 + 体 sha256。
              每条 case 声明自己在边缘属于哪一类（edge 字段）：
                app        @app         → 边缘 rewrite 成 /index.html 再打后端
                backend    @backend     → 原样打后端（/v1/*、/admin.html）
                spa        @spa_static  → 原样打后端（6 个根资源）
                landing    @landing     → Caddy 直接发静态文件，**根本不到后端**
                redir      redir 指令   → 边缘 301，不到后端
                notfound   兜底 handle  → 边缘 404，不到后端
              只有 app/backend/spa 三类是**阻塞判定**（它们真的换了后端）；
              landing/redir/notfound 三类由 public 模式的 baseline 逐字节兜底。
  capture  —— 打公网 https://museframe.lenscript.cn，把每条的状态码/体 sha 存成基线。
  verify   —— 打公网，与基线逐条比对。切换前 capture、切换后 verify = 逐字节证明。

红线：
  - 只打读接口与**写入前就会失败的**错误路径，绝不在生产库留行、绝不调上游付费 API。
  - curl/urllib 失败（连不上、超时）一律计为 ERR 并重试，绝不当成「该路径不存在」。
  - 断言 = 状态码 **且** 体 sha256（JSON 先剔除易变字段再规范化）。只比状态码是假绿温床。
"""
import argparse
import hashlib
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request

# ── 易变字段：两端/两次必然不同，纳入 sha 会产生假红 ────────────────────
VOLATILE = {
    "requestId", "ts", "timestamp", "time", "uptime", "ms", "serverTime", "now",
    "generatedAt", "expiresAt", "token", "imgToken", "latencyMs", "durationMs",
    "startedAt", "version", "commit", "buildTime", "checkedAt", "dbLatencyMs",
    "exp", "iat", "issuedAt", "url", "uploadUrl", "requestid",
}

BAD = "00000000-0000-4000-8000-000000000000"
STYLE_COVER = "cinema_distant_flash_01.jpg"
HASHED_CSS = "style.9e32f7ee.css"

CASES = []


def C(cid, edge, name, path, method="GET", body=None, admin=False, hdrs=None, note=""):
    CASES.append(dict(id=cid, edge=edge, name=name, path=path, method=method,
                      body=body, admin=admin, hdrs=hdrs or {}, note=note))


# ══ ① @app：path /app /app/ /app/*  → rewrite * /index.html → 后端 ══════
C(1,  "app", "/app（App 入口，无尾斜杠）", "/app")
C(2,  "app", "/app/（App 入口，带尾斜杠）", "/app/")
C(3,  "app", "/app/index.html（301 陷阱本体）", "/app/index.html",
  note="第一次切换炸在这里：Go 的 http.ServeFile 对 /index.html 结尾无条件 301")
C(4,  "app", "/app/projects（前端深链一层）", "/app/projects")
C(5,  "app", "/app/projects/<uuid>（深链两层）", "/app/projects/" + BAD)
C(6,  "app", "/app/a/b/c/d（深链多层）", "/app/a/b/c/d")
C(7,  "app", "/app?x=1（无尾斜杠 + query）", "/app?x=1")
C(8,  "app", "/app/?foo=bar&baz=1（带 query）", "/app/?foo=bar&baz=1")
C(9,  "app", "/app/settings.html（.html 深链）", "/app/settings.html")
C(10, "app", "/app/nonexistent-deep-path（不存在的深链）", "/app/nonexistent-deep-path")

# ══ ② @backend：path /v1/* /admin.html → 后端 ═══════════════════════════
C(11, "backend", "/admin.html（后台壳）", "/admin.html")
C(12, "backend", "GET /v1/health", "/v1/health")
C(13, "backend", "GET /v1/auth/config", "/v1/auth/config")
C(14, "backend", "GET /v1/discover", "/v1/discover")
C(15, "backend", "GET /v1/styles", "/v1/styles")
C(16, "backend", "GET /v1/styles/<不存在>", "/v1/styles/__no_such_style__")
C(17, "backend", "GET /v1/products", "/v1/products")
C(18, "backend", "GET /v1/entitlements/me（未登录）", "/v1/entitlements/me")
C(19, "backend", "GET /v1/projects（未登录）", "/v1/projects")
C(20, "backend", "GET /v1/projects/<uuid>（未登录）", "/v1/projects/" + BAD)
C(21, "backend", "GET /v1/purchases（未登录）", "/v1/purchases")
C(22, "backend", "GET /v1/assets/<uuid>/analysis", "/v1/assets/" + BAD + "/analysis")
C(23, "backend", "GET /v1/assets/<uuid>/file", "/v1/assets/" + BAD + "/file")
C(24, "backend", "GET /v1/assets/img-token", "/v1/assets/img-token")
C(25, "backend", "GET /v1/generation-jobs/<uuid>", "/v1/generation-jobs/" + BAD)
C(26, "backend", "GET /v1/nope（未知 API 路由）", "/v1/nope")
C(27, "backend", "GET /v1/（API 根）", "/v1/")
C(28, "backend", "POST /v1/auth/exchange 空体", "/v1/auth/exchange", "POST", body={})
C(29, "backend", "POST /v1/auth/email/request 空体", "/v1/auth/email/request", "POST", body={})
C(30, "backend", "POST /v1/auth/email/verify 空体", "/v1/auth/email/verify", "POST", body={})
C(31, "backend", "POST /v1/assets/upload-intents 空体", "/v1/assets/upload-intents", "POST", body={})
C(32, "backend", "POST /v1/projects 空体", "/v1/projects", "POST", body={})
C(33, "backend", "POST /v1/generation-jobs 空体", "/v1/generation-jobs", "POST", body={})
C(34, "backend", "POST /v1/generation-jobs/<uuid>/cancel", "/v1/generation-jobs/" + BAD + "/cancel", "POST", body={})
C(35, "backend", "POST /v1/candidates/<uuid>/feedback", "/v1/candidates/" + BAD + "/feedback", "POST", body={})
C(36, "backend", "POST /v1/candidates/<uuid>/export", "/v1/candidates/" + BAD + "/export", "POST", body={})
C(37, "backend", "POST /v1/purchases/verify 空体", "/v1/purchases/verify", "POST", body={})
C(38, "backend", "POST /v1/events 空体", "/v1/events", "POST", body={})
C(39, "backend", "PATCH /v1/projects/<uuid>", "/v1/projects/" + BAD, "PATCH", body={"selectedCandidateId": BAD})
C(40, "backend", "DELETE /v1/projects/<uuid>", "/v1/projects/" + BAD, "DELETE")
C(41, "backend", "DELETE /v1/auth/session（未登录）", "/v1/auth/session", "DELETE")
C(42, "backend", "PUT /v1/assets/<uuid>/upload 空体", "/v1/assets/" + BAD + "/upload", "PUT", body={})
C(43, "backend", "POST /v1/assets/<uuid>/complete 空体", "/v1/assets/" + BAD + "/complete", "POST", body={})
# —— 管理面（只读 GET；写接口一律不打）——
C(44, "backend", "GET /v1/admin/overview 无令牌", "/v1/admin/overview")
C(45, "backend", "GET /v1/admin/overview 错令牌", "/v1/admin/overview", hdrs={"X-Admin-Token": "definitely-wrong"})
C(46, "backend", "GET /v1/admin/overview 正确令牌", "/v1/admin/overview", admin=True)
C(47, "backend", "GET /v1/admin/jobs", "/v1/admin/jobs", admin=True)
C(48, "backend", "GET /v1/admin/feedback", "/v1/admin/feedback", admin=True)
C(49, "backend", "GET /v1/admin/purchases", "/v1/admin/purchases", admin=True)
C(50, "backend", "GET /v1/admin/users", "/v1/admin/users", admin=True)
C(51, "backend", "GET /v1/admin/stats/daily", "/v1/admin/stats/daily", admin=True)
C(52, "backend", "GET /v1/admin/stats/styles", "/v1/admin/stats/styles", admin=True)
C(53, "backend", "GET /v1/admin/db/tables", "/v1/admin/db/tables", admin=True)
C(54, "backend", "GET /v1/admin/db/table/products", "/v1/admin/db/table/products", admin=True)
C(55, "backend", "GET /v1/admin/config", "/v1/admin/config", admin=True)
C(56, "backend", "GET /v1/admin/products-admin", "/v1/admin/products-admin", admin=True)
C(57, "backend", "GET /v1/admin/styles-admin", "/v1/admin/styles-admin", admin=True)
C(58, "backend", "GET /v1/admin/img-token", "/v1/admin/img-token", admin=True)
C(59, "backend", "GET /v1/admin/assets/<uuid>/file", "/v1/admin/assets/" + BAD + "/file", admin=True)

# ══ ③ @spa_static：6 个 Node 根资源 → 后端 ══════════════════════════════
C(60, "spa", "/app.js", "/app.js")
C(61, "spa", "/app.css", "/app.css")
C(62, "spa", "/api.js", "/api.js")
C(63, "spa", "/native.js", "/native.js")
C(64, "spa", "/config.js", "/config.js")
C(65, "spa", "/i18n.js", "/i18n.js")

# ══ ④ @landing：Caddy 直发静态（不到后端）═══════════════════════════════
C(66, "landing", "/（落地页根）", "/")
C(67, "landing", "/index.html（落地页，非 App 壳）", "/index.html")
C(68, "landing", "/index.html?x=1（带 query）", "/index.html?x=1")
C(69, "landing", "/en/（英文落地页）", "/en/")
C(70, "landing", "/en(无尾斜杠 → 308)", "/en")
C(71, "landing", "/en/index.html", "/en/index.html")
C(72, "landing", "/robots.txt", "/robots.txt")
C(73, "landing", "/sitemap.xml", "/sitemap.xml")
C(74, "landing", "/favicon.svg", "/favicon.svg")
C(75, "landing", "/" + HASHED_CSS + "（immutable 哈希 CSS）", "/" + HASHED_CSS)
C(76, "landing", "/covers/<真实封面>", "/covers/" + STYLE_COVER)
C(77, "landing", "/showcase/original.jpg", "/showcase/original.jpg")
C(78, "landing", "/og-cover.jpg", "/og-cover.jpg")
C(79, "landing", "/og-cover-en.jpg", "/og-cover-en.jpg")
C(80, "landing", "/guide/", "/guide/")
C(81, "landing", "/guide/<文章>(无尾斜杠 → 308)", "/guide/ai-photo-art-vs-filters")
C(82, "landing", "/guide/<文章>/", "/guide/ai-photo-art-vs-filters/")
C(83, "landing", "/privacy/", "/privacy/")
C(84, "landing", "/terms/", "/terms/")
C(85, "landing", "/acceptable-use/", "/acceptable-use/")
C(86, "landing", "/en/privacy/", "/en/privacy/")
C(87, "landing", "/qq-group.ba339a5b.png", "/qq-group.ba339a5b.png")

# ══ ⑤ redir 指令：/privacy.html → /privacy/ ═════════════════════════════
C(88, "redir", "/privacy.html（永久跳转）", "/privacy.html")

# ══ ⑥ 兜底 handle { respond 404 }：必须是真 404，不能是 SPA 软 404 ══════
C(89, "notfound", "/nope-xyz（一定 404）", "/nope-xyz")
C(90, "notfound", "/assets/（落地页无此目录 → 404）", "/assets/")
C(91, "notfound", "/assets/does-not-exist.png", "/assets/does-not-exist.png")
C(92, "notfound", "/index2.html（拼错的落地页 URL）", "/index2.html")
C(93, "notfound", "/apple-app-site-association", "/apple-app-site-association")

# ── 边缘 rewrite 规则：客户端路径 → 后端实际收到的路径 ───────────────────
def backend_path(case):
    """@app 的 rewrite * /index.html 会把路径整条换掉，query 保留。"""
    if case["edge"] == "app":
        p = case["path"]
        q = p.split("?", 1)[1] if "?" in p else ""
        return "/index.html" + ("?" + q if q else "")
    return case["path"]


BLOCKING_EDGES = {"app", "backend", "spa"}   # 真正换了后端的三类

# ── 预期体差异白名单（**切换前**写死在脚本里，事后不许往里加）───────────
# 只允许「体」不同，**状态码永远必须一致**。每条都写清楚为什么，以及
# 「如果它其实坏了，检查会怎么红」。这些差异全部来自 SQLite → PostgreSQL
# 这一层（Go 后端早就接 PG 了，与本次切边缘端口无关），且全部只在
# /admin.html 后台可见，App 与 Web App 一条都不碰。
EXPECT_BODY_DIFF = {
    46: "overview.topStyles：8 条 Top 风格里多条 jobs 并列，Node 无 tiebreak、"
        "Go 是 (jobs DESC, public_name ASC) 确定序 → 并列段取到的成员不同。"
        "若真坏了（比如统计全空），jobs 计数会变 0、集合大小会变，仍会红。",
    47: "admin/jobs.seconds：now-createdAt 的整秒截断，两次请求相隔约 0.5s 时"
        "小数部分跨整秒的那几条会 ±1。这是时间，不是后端差异。",
    48: "admin/feedback.reason_codes：SQLite 存 TEXT '[]'，PG 存 jsonb []。"
        "admin.html 第 318 行 JSON.parse(f.reason_codes||'[]') 外面包了 try/catch，"
        "两种形态都渲染成 '—'，已逐行核对。",
    50: "admin/users.isGuest：SQLite 0/1 → PG boolean。admin.html 第 335/368 行"
        "是真值判断（u.isGuest ? …），两种形态渲染一致。",
    52: "admin/stats/styles：30 条风格集合逐字节相同（multiset 已验证 True），"
        "仅排序不同（Go 是 public_name 确定序）。少一条风格立刻红。",
    53: "admin/db/tables：Node 侧 SQLite 多一张 server_secrets 表、app_config 多一行 —— "
        "正是把上游图像 API 密钥从库里挪进环境变量的那次安全修复的结果，"
        "Go 侧不该有它们。",
    54: "admin/db/table/products：原始表浏览器。6 行集合相同、行序不同；"
        "另外 active 列 SQLite 1 → PG true、features 列 TEXT-JSON → jsonb。",
    55: "admin/config：image_provider_api_key 的 source 由 db 变 env、"
        "requiresRestart 由 false 变 true —— 同上，密钥不再从库里读。",
    57: "admin/styles-admin：30 条风格集合逐字节相同，仅排序不同。",
}


def norm_json(o):
    if isinstance(o, dict):
        return {k: norm_json(v) for k, v in sorted(o.items()) if k not in VOLATILE}
    if isinstance(o, list):
        return [norm_json(v) for v in o]
    return o


def digest(status, ctype, raw):
    """返回 (体 sha256, 规范化 sha256)。JSON 先剔易变字段。"""
    raw_sha = hashlib.sha256(raw).hexdigest()
    base_ct = (ctype or "").split(";")[0].strip().lower()
    if base_ct in ("application/json", "application/problem+json"):
        try:
            return raw_sha, hashlib.sha256(json.dumps(
                norm_json(json.loads(raw)), sort_keys=True, ensure_ascii=False,
                separators=(",", ":")).encode()).hexdigest()
        except Exception:
            pass
    return raw_sha, raw_sha


_CTX_INSECURE = ssl.create_default_context()
_CTX_INSECURE.check_hostname = False
_CTX_INSECURE.verify_mode = ssl.CERT_NONE


def probe(base, case, admin_token, insecure=False, retries=3):
    """单次探测。连接层失败 → 重试；重试耗尽返回 status=-1（ERR），
    **绝不**把 ERR 当成「该路径不存在」。"""
    url = base + case["path"]
    data = None
    h = {"Accept": "*/*", "User-Agent": "museframe-edge-regress/1"}
    if case["body"] is not None:
        data = json.dumps(case["body"]).encode()
        h["Content-Type"] = "application/json"
    if case["admin"]:
        h["X-Admin-Token"] = admin_token
    h.update(case["hdrs"])
    last = ""
    for attempt in range(retries):
        req = urllib.request.Request(url, data=data, headers=h, method=case["method"])
        handlers = [NoRedirect]
        if insecure:
            handlers.append(urllib.request.HTTPSHandler(context=_CTX_INSECURE))
        opener = urllib.request.build_opener(*handlers)
        try:
            with opener.open(req, timeout=30) as r:
                return (r.status, r.headers.get("Content-Type", ""),
                        r.read(), r.headers.get("Location", ""), "")
        except urllib.error.HTTPError as e:
            return (e.code, e.headers.get("Content-Type", ""),
                    e.read(), e.headers.get("Location", ""), "")
        except Exception as e:                       # 连接/超时/TLS
            last = "%s: %s" % (type(e).__name__, e)
            time.sleep(0.4 * (attempt + 1))
    return (-1, "", b"", "", last)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """不跟随跳转 —— 301/308 本身就是要断言的对象。"""
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

    def http_error_301(self, req, fp, code, msg, headers):
        raise urllib.error.HTTPError(req.full_url, code, msg, headers, fp)
    http_error_302 = http_error_303 = http_error_307 = http_error_308 = http_error_301


def load_admin_token():
    t = os.environ.get("MF_ADMIN_TOKEN", "")
    if not t:
        sys.stderr.write("需要环境变量 MF_ADMIN_TOKEN（从 project.env 取，切勿打印）\n")
        sys.exit(2)
    return t


def snapshot(base, admin_token, insecure, inject_fail, delay, inject_body=0):
    out = {}
    for c in CASES:
        st, ct, raw, loc, err = probe(base, c, admin_token, insecure)
        if st == -1:
            sys.stderr.write("[ERR ] #%d %s → 探测失败：%s\n" % (c["id"], c["path"], err))
        rs, ns = digest(st, ct, raw)
        rec = dict(id=c["id"], path=c["path"], method=c["method"], edge=c["edge"],
                   name=c["name"], status=st, ctype=(ct or "").split(";")[0].strip(),
                   raw_sha=rs, norm_sha=ns, length=len(raw), location=loc, err=err)
        if inject_fail and c["id"] == inject_fail:
            rec["status"] = 500
            rec["norm_sha"] = "deadbeef" * 8
            rec["raw_sha"] = "deadbeef" * 8
            rec["_injected"] = True
        if inject_body and c["id"] == inject_body:
            # 只动体、不动状态码 —— 用来证明「体 sha 断言」本身会红，
            # 而不是靠状态码那一半在兜底。
            rec["norm_sha"] = "cafebabe" * 8
            rec["raw_sha"] = "cafebabe" * 8
            rec["_injected_body"] = True
        out[str(c["id"])] = rec
        time.sleep(delay)
    return out


def fmt_table(rows, headers):
    w = [len(h) for h in headers]
    for r in rows:
        for i, v in enumerate(r):
            w[i] = max(w[i], len(str(v)))
    line = "  ".join("-" * x for x in w)
    print("  ".join(h.ljust(w[i]) for i, h in enumerate(headers)))
    print(line)
    for r in rows:
        print("  ".join(str(v).ljust(w[i]) for i, v in enumerate(r)))
    print(line)


def cmd_shadow(args):
    tok = load_admin_token()
    old = snapshot(args.old, tok, False, 0, args.delay)
    new = snapshot(args.new, tok, False, args.inject_fail, args.delay, args.inject_body)
    rows, red, red_ids, errs = [], 0, [], 0
    for c in CASES:
        k = str(c["id"])
        o, n = old[k], new[k]
        blocking = c["edge"] in BLOCKING_EDGES
        bp = backend_path(c)
        if o["status"] == -1 or n["status"] == -1:
            errs += 1
            verdict = "探测失败(ERR)"
            bad = True
        else:
            same_status = o["status"] == n["status"]
            same_body = o["norm_sha"] == n["norm_sha"]
            allowed = c["id"] in EXPECT_BODY_DIFF
            bad = not (same_status and (same_body or allowed))
            if same_status and same_body:
                verdict = "一致"
            elif not same_status:
                verdict = "状态码不一致"
            elif allowed:
                verdict = "体差异·白名单内"
            else:
                verdict = "体不一致"
        if bad and blocking:
            red += 1
            red_ids.append(c["id"])
            verdict = "🔴 " + verdict
        elif bad:
            verdict = "（边缘终结·不阻塞）" + verdict
        rows.append([c["id"], c["edge"], c["method"], c["path"],
                     "" if bp == c["path"] else bp,
                     o["status"], n["status"], o["norm_sha"][:16], n["norm_sha"][:16],
                     verdict])
    fmt_table(rows, ["#", "类", "方法", "客户端路径", "后端实收路径",
                     "旧码", "新码", "旧体sha16", "新体sha16", "判定"])
    print("总计 %d 条；阻塞类(app/backend/spa) %d 条；红 %d 条；探测失败 %d 条"
          % (len(CASES), sum(1 for c in CASES if c["edge"] in BLOCKING_EDGES), red, errs))
    if red_ids:
        print("红条编号：%s" % red_ids)
    if args.out:
        json.dump({"old": old, "new": new}, open(args.out, "w"), ensure_ascii=False, indent=1)
    return 1 if (red or errs) else 0


def cmd_capture(args):
    tok = load_admin_token()
    snap = snapshot(args.base, tok, args.insecure, 0, args.delay)
    errs = [r for r in snap.values() if r["status"] == -1]
    json.dump({"base": args.base, "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
               "cases": snap}, open(args.out, "w"), ensure_ascii=False, indent=1)
    print("已写入基线 %s（%d 条，探测失败 %d 条）" % (args.out, len(snap), len(errs)))
    return 1 if errs else 0


def cmd_verify(args):
    tok = load_admin_token()
    baseline = json.load(open(args.baseline))["cases"]
    # 切换后，白名单里那 9 条 admin 路由的体**必然**变成 Go 后端那一份。
    # 这里不是「跳过不查」，而是换一条**更强的正向断言**：体必须恰好等于
    # 影子阶段在 Go 源站(:18787)量到的那个 sha。变成别的什么都算红。
    expect_new = {}
    if args.expect_diff_from:
        expect_new = json.load(open(args.expect_diff_from))["new"]
    snap = snapshot(args.base, tok, args.insecure, args.inject_fail, args.delay, args.inject_body)
    rows, red, red_ids, errs = [], 0, [], 0
    for c in CASES:
        k = str(c["id"])
        b, n = baseline.get(k), snap[k]
        if c["id"] in EXPECT_BODY_DIFF and k in expect_new and b is not None:
            b = dict(b)
            b["norm_sha"] = expect_new[k]["norm_sha"]   # 状态码/Location 仍按基线卡死
        if b is None:
            rows.append([c["id"], c["edge"], c["path"], "-", n["status"], "-",
                         n["norm_sha"][:16], "🔴 基线缺此条"])
            red += 1
            red_ids.append(c["id"])
            continue
        if n["status"] == -1:
            errs += 1
            rows.append([c["id"], c["edge"], c["path"], b["status"], n["status"],
                         b["norm_sha"][:16], n["norm_sha"][:16], "🔴 探测失败(ERR)"])
            red_ids.append(c["id"])
            continue
        same = (b["status"] == n["status"] and b["norm_sha"] == n["norm_sha"]
                and b["location"] == n["location"])
        if same:
            verdict = "一致"
        else:
            red += 1
            red_ids.append(c["id"])
            bits = []
            if b["status"] != n["status"]:
                bits.append("状态码")
            if b["norm_sha"] != n["norm_sha"]:
                bits.append("体")
            if b["location"] != n["location"]:
                bits.append("Location")
            verdict = "🔴 " + "/".join(bits) + "不一致"
        rows.append([c["id"], c["edge"], c["path"], b["status"], n["status"],
                     b["norm_sha"][:16], n["norm_sha"][:16], verdict])
    fmt_table(rows, ["#", "类", "路径", "基线码", "现码", "基线sha16", "现sha16", "判定"])
    print("总计 %d 条；红 %d 条；探测失败 %d 条" % (len(CASES), red, errs))
    if red_ids:
        print("红条编号：%s" % red_ids)
    return 1 if (red or errs) else 0


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("shadow")
    s.add_argument("--old", default="http://127.0.0.1:8787")
    s.add_argument("--new", default="http://127.0.0.1:18787")
    s.add_argument("--out", default="")
    s.add_argument("--inject-fail", type=int, default=0,
                   help="给指定编号注入假结果（状态码+体），用于自检脚本真的会红")
    s.add_argument("--inject-body", type=int, default=0,
                   help="只给指定编号注入假的体 sha，自检「体断言」那一半会红")
    s.add_argument("--delay", type=float, default=0.05)
    s.set_defaults(fn=cmd_shadow)

    s = sub.add_parser("capture")
    s.add_argument("--base", default="https://museframe.lenscript.cn")
    s.add_argument("--out", required=True)
    s.add_argument("--insecure", action="store_true")
    s.add_argument("--delay", type=float, default=0.05)
    s.set_defaults(fn=cmd_capture)

    s = sub.add_parser("verify")
    s.add_argument("--base", default="https://museframe.lenscript.cn")
    s.add_argument("--baseline", required=True)
    s.add_argument("--insecure", action="store_true")
    s.add_argument("--inject-fail", type=int, default=0)
    s.add_argument("--inject-body", type=int, default=0)
    s.add_argument("--expect-diff-from", default="",
                   help="shadow --out 产出的 json；白名单条目改为断言等于 Go 源站实测 sha")
    s.add_argument("--delay", type=float, default=0.05)
    s.set_defaults(fn=cmd_verify)

    a = ap.parse_args()
    sys.exit(a.fn(a))


if __name__ == "__main__":
    main()
