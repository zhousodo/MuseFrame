package httpapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// adminHTML 读仓库里那份真实的后台静态壳（web/admin.html）。
//
// 🔴 为什么用 Go 测试去断言一份 HTML：这个文件是生产里**逐字节同步**到
// /srv/platform/apps/museframe/data/web/ 的唯一后台界面，仓库里没有任何 JS
// 测试运行器能跑它（package.json 的 test 只跑旧 Node 后端的 server/test）。
// 下面四条都是 2026-09-12 浏览器验收实际扫出来的缺陷，不是风格偏好 ——
// 回归成本是「下一次验收又要人去点一遍」，所以钉在 `go test` 里。
func adminHTML(t *testing.T) string {
	t.Helper()
	// internal/httpapi → server-go → 仓库根
	p := filepath.Join("..", "..", "..", "web", "admin.html")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读不到 %s：%v", p, err)
	}
	return string(b)
}

// htmlComment / blockComment 用来在「不许出现 X」这类断言前把注释摘掉。
// 这个文件里的注释本身会逐字引用被禁的写法（解释为什么禁），不摘掉就会自己打自己。
// 刻意**不**摘 `//` 行注释：URL 里的 `//` 与字符串里的 `//` 会让摘除越界，
// 把真正的代码一起吃掉 —— 那是假绿，比误报坏得多。
var (
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func adminCode(t *testing.T) string {
	t.Helper()
	s := htmlComment.ReplaceAllString(adminHTML(t), "")
	return blockComment.ReplaceAllString(s, "")
}

// 缺陷 ③：总览上有 1 个空图元素。真凶是大图查看器里那个常驻的 <img id="viewerImg">
// —— 它在每个标签页都存在且没有 src，浏览器按空图处理（还会发一次指向当前页的请求）。
// 断言静态标记里**一个没有 src 的 <img> 都不许有**，且不许出现 src=""。
func TestAdminHTMLHasNoEmptyImg(t *testing.T) {
	html := adminHTML(t)
	code := adminCode(t)

	if strings.Contains(code, `src=""`) || strings.Contains(code, "src=''") {
		t.Fatal("admin.html 里出现了 src=\"\" 的元素")
	}
	// 只看 HTML 标记里的 <img ...>（JS 模板字符串里的 <img 一律带 src="${...}"，
	// 下面的正则同样会检查它们 —— 那是好事）。
	imgTag := regexp.MustCompile(`(?s)<img\b[^>]*>`)
	for _, tag := range imgTag.FindAllString(code, -1) {
		if !strings.Contains(tag, "src=") {
			t.Fatalf("存在没有 src 的 <img>（会被当成空图请求）：%s", tag)
		}
	}
	// 查看器必须是「点开才建 img、关掉就拆掉」，不是一个常驻空壳。
	if strings.Contains(code, `<img id="viewerImg">`) {
		t.Fatal("大图查看器又放回了常驻的空 <img id=\"viewerImg\">")
	}
	if !strings.Contains(html, "function closeViewer(") {
		t.Fatal("少了 closeViewer()：查看器关掉后必须把 img 从 DOM 里拆掉")
	}
	// 缩略图 / 封面为空时渲染「—」或「无封面」，不渲染 img。
	if !strings.Contains(html, `if (!aid) return '<span class="muted">—</span>';`) {
		t.Fatal("thumb() 必须在资产 id 为空时渲染「—」而不是 <img>")
	}
	if !strings.Contains(html, `const cover = (s.coverUrl && String(s.coverUrl).trim())`) {
		t.Fatal("封面必须在 coverUrl 为空/空白串时走 nocover 分支，不渲染 <img>")
	}
}

// 缺陷 ②：同一个页面上审计列显示 UTC（20:44）、页头显示北京时间（04:41），
// 两处都没有时区标注，差 8 小时。统一成北京时间 + 显式 (UTC+8) 标注。
func TestAdminHTMLShowsBeijingTimeWithLabel(t *testing.T) {
	html := adminHTML(t)
	code := adminCode(t)

	// 🔴 不许再用 toLocaleString / toLocaleDateString：那是**浏览器时区**，
	// 运维从 UTC 的跳板机打开就又变成两套时间了。
	for _, bad := range []string{"toLocaleString(", "toLocaleDateString(", "toLocaleTimeString("} {
		if strings.Contains(code, bad) {
			t.Fatalf("admin.html 仍在用 %s（= 浏览器时区），必须走显式 UTC+8 换算", bad)
		}
	}
	for _, want := range []string{
		"const TZ_LABEL = '(UTC+8)';",
		"const TZ_OFFSET_MS = 8 * 3600 * 1000;",
		"function fmtBJ(t)",
		"function nowBJ()",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("缺少北京时间辅助件：%s", want)
		}
	}
	// 审计那一列：渲染走 fmtBJ，列头带 (UTC+8)，title 里保留 UTC 原文以便对照。
	if strings.Contains(code, `x.at.replace('T',' ').slice(0,19)`) {
		t.Fatal("审计时间又回到直接截 UTC ISO 串")
	}
	if !strings.Contains(html, `<td class="muted" title="UTC ${escapeHtml(x.at)}">${escapeHtml(fmtBJ(x.at))}</td>`) {
		t.Fatal("审计时间必须用 fmtBJ() 渲染，并在 title 里留 UTC 原文")
	}
	// 每一个「时间」列头都要有时区标注，否则看的人无从判断。
	if n := strings.Count(code, "<th>时间 (UTC+8)</th>"); n < 4 {
		t.Fatalf("带 (UTC+8) 标注的「时间」列头应有 4 个（任务/购买/反馈/审计），实际 %d", n)
	}
	if !strings.Contains(html, "<th>注册时间 (UTC+8)</th>") {
		t.Fatal("用户表「注册时间」列头少了 (UTC+8) 标注")
	}
	if strings.Contains(code, "<th>时间</th>") || strings.Contains(code, "<th>注册时间</th>") {
		t.Fatal("还有没带时区标注的时间列头")
	}
	// 页头那行「数据时间」也必须是北京时间 + 标注（它就是验收里那个 04:41）。
	if !strings.Contains(html, "`数据时间 ${nowBJ()} ${TZ_LABEL}`") {
		t.Fatal("页头「数据时间」必须显示带 (UTC+8) 标注的北京时间")
	}
	// 运行状态里的启动时间 / 服务器时间同样换算。
	if strings.Contains(code, "服务器时间 ${escapeHtml(rt.serverTime||'—')}（UTC）") {
		t.Fatal("运行状态的服务器时间还在直接显示 UTC")
	}
}

// 缺陷 ④：写操作之后审计列表要手动刷新才看得到刚刚那一行。
// 后端会留痕的动作共 5 类（config.set / product.update / style.update /
// style.status / user.status），前端对应 7 个写入口都必须调 refreshAudit()。
func TestAdminHTMLRefreshesAuditAfterWrites(t *testing.T) {
	html := adminHTML(t)
	code := adminCode(t)

	if !strings.Contains(html, "async function refreshAudit(){") {
		t.Fatal("缺少 refreshAudit()")
	}
	// 还没打开过运营页就不发请求（showTab 第一次切过去时会 loadOps）。
	if !strings.Contains(html, "if (!tabState.ops) return;") {
		t.Fatal("refreshAudit() 必须在运营页未加载时直接返回，不为看不见的面板发请求")
	}
	// 10 个写入口：用户禁用/启用、配置保存、配置恢复默认、风格保存、
	// 风格上下架、商品保存、商品上下架，外加 2026-09-12 第四轮新增的
	// 任务重试、购买重验、反馈标记已处理。
	//
	// 🔴 这个数字必须跟着后端的「留痕动作」一起涨。新加一个会写审计的后台动作
	// 却忘了刷新审计表，现象是运营点完之后去审计页看不到自己那一行 ——
	// 然后他会以为「这次没留痕」，再点一次。
	const wantCalls = 10
	if n := strings.Count(code, "refreshAudit();"); n != wantCalls {
		t.Fatalf("refreshAudit() 调用点应为 %d 个（每个留痕写入口一个），实际 %d", wantCalls, n)
	}
	// 逐个写入口确认：调用必须紧跟在那次写的成功分支里。
	for _, anchor := range []string{
		"    loadUsers();\n    refreshAudit();",
		"    toast('已保存');\n    refreshAudit();",
		"    toast('已恢复默认');\n    refreshAudit();",
		"    loadOpsStyles();\n    refreshAudit();",
		"    loadOpsProducts();\n    refreshAudit();",
		"    loadJobsTable();\n    refreshAudit();",
		"    loadPurchasesTable();\n    refreshAudit();",
		"    loadFeedbackTable();\n    refreshAudit();",
	} {
		if !strings.Contains(html, anchor) {
			t.Fatalf("写操作成功后没有接上 refreshAudit()：%q", anchor)
		}
	}
}

// ---- 2026-09-12 第四轮：全链路可见性的前端侧回归 --------------------------

// 三个新标签页必须完整存在：按钮、TABS 数组、section、路由分支、加载函数。
// 漏掉任何一环的表现都是「点了标签页没反应」或「空白页」，而那只有人工点才发现。
func TestAdminHTMLHasVisibilityTabs(t *testing.T) {
	html := adminHTML(t)
	for _, tab := range []string{"events", "assets", "health"} {
		if !strings.Contains(html, `data-tab="`+tab+`"`) {
			t.Errorf("缺少 %s 标签按钮", tab)
		}
		if !strings.Contains(html, `id="tab-`+tab+`"`) {
			t.Errorf("缺少 %s 的 section", tab)
		}
		if !strings.Contains(html, `'`+tab+`'`) {
			t.Errorf("TABS 数组里缺少 %q", tab)
		}
	}
	for _, fn := range []string{"loadEvents", "loadAssets", "loadHealth", "openUserDetail", "exportCsv"} {
		if !strings.Contains(html, "function "+fn+"(") {
			t.Errorf("缺少 %s()", fn)
		}
	}
	for _, branch := range []string{
		`if (name === 'events') return loadEvents();`,
		`if (name === 'assets') return loadAssets();`,
		`if (name === 'health') return loadHealth();`,
	} {
		if !strings.Contains(html, branch) {
			t.Errorf("loadTabByName 缺少路由分支：%s", branch)
		}
	}
}

// 每个视图顶部那句说明必须来自**后端**（note 字段），不能在前端硬编码。
// 说明里写的是口径（窗口多长、按什么时区切、哪些行被排除、进程内还是全站），
// 而口径是后端决定的 —— 前端抄一份等于留一份一定会过期的副本。
func TestAdminHTMLRendersBackendSuppliedNotes(t *testing.T) {
	html := adminHTML(t)
	for _, id := range []string{
		"eventsNote", "assetsNote", "healthNote", "emailLogNote",
		"jobsNote", "purchasesNote", "feedbackNote", "usersNote",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("缺少说明占位元素 #%s", id)
		}
		if !strings.Contains(html, `$('`+id+`').textContent`) {
			t.Errorf("#%s 没有从后端的 note 字段赋值", id)
		}
	}
}

// 🔴 CSV 导出必须走 fetch + blob，绝不能用 <a href download> 或 window.open：
// 管理员令牌走请求头，浏览器发起的导航带不上头 —— 那样的链接一律回 401，
// 而运维看到的是「点了导出，下载了一个 5 字节的文件」。
func TestAdminHTMLExportsCSVViaFetchNotNavigation(t *testing.T) {
	code := adminCode(t)
	if !strings.Contains(code, `fetch(url, { headers: { 'x-admin-token': TOK } })`) {
		t.Fatal("CSV 导出必须用 fetch 并把令牌放在请求头里")
	}
	if strings.Contains(code, "window.open('/v1/admin/export") ||
		strings.Contains(code, `href="/v1/admin/export`) {
		t.Fatal("CSV 导出不能用导航（window.open / <a href>）—— 带不上 x-admin-token 头")
	}
	// 六个导出按钮，和后端的 exportKinds 一一对应。
	for _, id := range []string{
		"eventsExport", "assetsExport", "jobsExport", "purchasesExport", "feedbackExport", "usersExport",
	} {
		if !strings.Contains(code, `id="`+id+`"`) {
			t.Errorf("缺少导出按钮 #%s", id)
		}
	}
	for _, kind := range exportKinds {
		if !strings.Contains(code, `exportCsv('`+kind+`'`) {
			t.Errorf("没有调用 exportCsv(%q)", kind)
		}
	}
}

// 反馈表必须显示**用户写的正文**。这是这一轮审计抓到的黑洞：
// comment 从建库起就在落库，后台此前从来没显示过它。
func TestAdminHTMLShowsFeedbackCommentAndHandledToggle(t *testing.T) {
	code := adminCode(t)
	if !strings.Contains(code, "f.comment") {
		t.Fatal("反馈表没有显示用户写的正文（f.comment）")
	}
	// 字段名是 camelCase 的 reasonCodes（不是旧的 reason_codes）——
	// 用错的那个在页面上的表现是原因码一列永远空。
	if !strings.Contains(code, "parseReasonCodes(f.reasonCodes)") {
		t.Fatal("反馈表的原因码应当读 f.reasonCodes")
	}
	if !strings.Contains(code, "markFeedback(") {
		t.Fatal("缺少「标为已处理」入口")
	}
	if !strings.Contains(code, "f.handledAt") || !strings.Contains(code, "f.handledNote") {
		t.Fatal("反馈表必须显示处理时间与处理备注")
	}
}

// 任务表要有重试入口与上游返回摘要；购买表要有平台 / 交易号 / 入账额度与重验入口。
func TestAdminHTMLHasRetryAndReverifyEntries(t *testing.T) {
	code := adminCode(t)
	for _, want := range []string{
		"retryJob(", "x.upstreamSummary", "x.retryCount",
		"reverifyPurchase(", "p.txId", "p.unitsGranted", "p.platform",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("admin.html 缺少 %q", want)
		}
	}
	// 「漏入账」的判据必须和后端的状态词汇表一致：verified（不是 active）。
	if !strings.Contains(code, `p.status === 'verified'`) {
		t.Fatal("购买表的状态判据必须用 verified —— purchases.status 里没有 active 这个值")
	}
	if strings.Contains(code, `p.status === 'active'`) {
		t.Fatal("购买表里出现了 active 状态判据，但 purchases.status 只有 pending / verified")
	}
}
