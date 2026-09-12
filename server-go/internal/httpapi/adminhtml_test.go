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
// 下面每一条都是浏览器验收实际扫出来的缺陷，不是风格偏好 ——
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

// adminViews 是左侧导航的 15 个视图。每加一个视图，这里、导航、section、
// loadTabByName 分支必须同时加 —— 漏掉任何一环的表现都是「点了导航没反应」。
var adminViews = []string{
	"overview", "stats", "users", "purchases", "jobs", "assets", "styles",
	"products", "feedback", "events", "email", "config", "health", "audit", "db",
}

// 缺陷 ③：总览上有 1 个空图元素。真凶是大图查看器里那个常驻的 <img>
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
	// 缩略图 / 封面为空时渲染占位方块或「无封面」，不渲染 img。
	if !strings.Contains(html, `if (!aid) return '<div class="nothumb"`) {
		t.Fatal("thumb() 必须在资产 id 为空时渲染占位方块而不是 <img>")
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
		t.Fatalf("带 (UTC+8) 标注的「时间」列头至少 4 个（任务/购买/反馈/审计），实际 %d", n)
	}
	if !strings.Contains(html, "<th>注册时间 (UTC+8)</th>") {
		t.Fatal("用户表「注册时间」列头少了 (UTC+8) 标注")
	}
	if strings.Contains(code, "<th>时间</th>") || strings.Contains(code, "<th>注册时间</th>") {
		t.Fatal("还有没带时区标注的时间列头")
	}
	// 顶栏那行「数据时间」也必须是北京时间 + 标注（它就是验收里那个 04:41）。
	if !strings.Contains(html, "`数据时间 ${nowBJ()} ${TZ_LABEL}`") {
		t.Fatal("顶栏「数据时间」必须显示带 (UTC+8) 标注的北京时间")
	}
	// 运行状态里的启动时间 / 服务器时间同样换算。
	if strings.Contains(code, "服务器时间 ${escapeHtml(rt.serverTime||'—')}（UTC）") {
		t.Fatal("运行状态的服务器时间还在直接显示 UTC")
	}
}

// 缺陷 ④：写操作之后审计列表要手动刷新才看得到刚刚那一行。
func TestAdminHTMLRefreshesAuditAfterWrites(t *testing.T) {
	html := adminHTML(t)
	code := adminCode(t)

	if !strings.Contains(html, "async function refreshAudit(){") {
		t.Fatal("缺少 refreshAudit()")
	}
	// 还没打开过审计视图就不发请求（showTab 第一次切过去时会 loadAudit）。
	if !strings.Contains(html, "if (!tabState.audit) return;") {
		t.Fatal("refreshAudit() 必须在审计视图未加载时直接返回，不为看不见的面板发请求")
	}
	// 10 个写入口：用户禁用/启用、配置保存、配置恢复默认、风格保存、
	// 风格上下架、商品保存、商品上下架、任务重试、购买重验、反馈标记已处理。
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

// 15 个视图必须完整存在：导航项、TABS 数组、section、路由分支。
// 漏掉任何一环的表现都是「点了导航没反应」或「空白页」，而那只有人工点才发现。
func TestAdminHTMLHasAllViews(t *testing.T) {
	html := adminHTML(t)
	for _, v := range adminViews {
		if !strings.Contains(html, `data-view="`+v+`"`) {
			t.Errorf("左侧导航缺少 %s", v)
		}
		if !strings.Contains(html, `id="view-`+v+`"`) {
			t.Errorf("缺少 %s 的 section", v)
		}
		if !strings.Contains(html, `'`+v+`'`) {
			t.Errorf("TABS 数组里缺少 %q", v)
		}
		if !strings.Contains(html, `if (name === '`+v+`')`) {
			t.Errorf("loadTabByName 缺少路由分支：%s", v)
		}
	}
	for _, fn := range []string{
		"loadEvents", "loadAssets", "loadHealth", "loadEmailLog", "loadAudit",
		"openUserDetail", "exportCsv", "loadJobsTable", "loadPurchasesTable", "loadFeedbackTable",
	} {
		if !strings.Contains(html, "function "+fn+"(") {
			t.Errorf("缺少 %s()", fn)
		}
	}
	// 旧书签（#ops 这类）必须还能落到某个视图，不能白屏。
	if !strings.Contains(html, "const TAB_ALIASES") {
		t.Error("缺少旧 hash 的兼容表 TAB_ALIASES")
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

// 反馈表必须显示**用户写的正文**。这是审计抓到过的黑洞：
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

// 🔴 占位文字不许直接写在 <table> 里。HTML 解析器对 table 内的裸文本做
// foster parenting：把它挪到 <table> **前面**（成了外层容器的子节点），
// 而渲染函数只改 table.innerHTML —— 于是「加载中…」永久钉在表头上方。
// 正确写法是包进 <tbody><tr><td colspan=N>，这样它是表的子节点，第一次渲染就被换掉。
func TestAdminHTMLHasNoFosterParentedLoadingText(t *testing.T) {
	code := adminCode(t)

	// 静态标记与 JS 模板串里都不许有。`[^>]*` 故意不跨 `>`，所以只会命中
	// 「开标签紧跟文字」这一种形态 —— 正是会被 foster-parent 的那一种。
	fostered := regexp.MustCompile(`(?s)<table[^>]*>\s*加载中`)
	if m := fostered.FindAllString(code, -1); len(m) != 0 {
		t.Fatalf("有 %d 处把「加载中…」直接写在 <table> 里（会被 foster-parent 到表外）：%q", len(m), m)
	}
	// 同一个坑的 JS 版：table.innerHTML = '加载中…' 也是表里的裸文本。
	for _, id := range []string{"usersTable", "assetsTable", "healthTable", "emailSends", "emailCodes", "jobs", "purchases", "feedback"} {
		if strings.Contains(code, `$('`+id+`').innerHTML = '加载中…'`) {
			t.Errorf("#%s 的加载占位是表里的裸文本，必须包进 <tbody><tr><td>", id)
		}
	}
	// 每张表的占位必须带上和表头一样宽的 colspan，否则占位行只占第一列。
	for _, want := range []struct {
		id   string
		cols string
	}{
		{"recentJobs", "6"}, {"jobs", "11"}, {"purchases", "9"}, {"feedback", "9"},
		{"usersTable", "10"}, {"assetsTable", "10"}, {"stylesRank", "9"},
		{"healthTable", "7"}, {"emailSends", "5"}, {"emailCodes", "5"},
	} {
		ph := `<table id="` + want.id + `"><tbody><tr><td colspan="` + want.cols + `" class="loading">加载中…</td></tr></tbody></table>`
		if !strings.Contains(code, ph) {
			t.Errorf("#%s 缺少 colspan=%s 的 tbody 占位行", want.id, want.cols)
		}
	}
	// email-log 的两张表共用一个 catch：只给 emailSends 落错误，
	// emailCodes 就会永久停在「加载中…」。
	if !strings.Contains(code, "$('emailCodes').innerHTML = errRow(5, e.message);") {
		t.Error("email-log 出错时 emailCodes 也必须落错误，不能停在「加载中…」")
	}
}

// 🔴 自由文本筛选框只绑 Enter，而「导出 CSV」读输入框的**当前值** ——
// 「填了用户 ID 但没按回车就点导出」会得到一份和表里不一致的 CSV。
func TestAdminHTMLFilterInputsApplyOnBlurAndSyncBeforeExport(t *testing.T) {
	code := adminCode(t)

	if !strings.Contains(code, "function bindFilterInput(id, reload){") {
		t.Fatal("缺少 bindFilterInput()：自由文本筛选框必须走统一绑定")
	}
	for _, ev := range []string{
		`el.addEventListener('keydown', e => { if (e.key === 'Enter') apply(); });`,
		`el.addEventListener('change', apply);`,
		`el.addEventListener('blur', apply);`,
	} {
		if !strings.Contains(code, ev) {
			t.Errorf("bindFilterInput() 少绑了事件：%s", ev)
		}
	}
	// 去重是必需的：Enter 之后浏览器自己还会补一个 change，不去重就发两次请求。
	if !strings.Contains(code, "if (v === applied) return false;") {
		t.Error("bindFilterInput() 必须在值没变时不重新拉数据（Enter 会再触发一次 change）")
	}
	// 四个自由文本筛选框全部走它；任何一个漏掉就又回到「只有回车生效」。
	for _, b := range []string{
		"bindFilterInput('eventNameFilter', loadEvents);",
		"bindFilterInput('assetUser', loadAssets);",
		"bindFilterInput('jobUser', loadJobsTable);",
		"bindFilterInput('userSearch', loadUsers);",
	} {
		if !strings.Contains(code, b) {
			t.Errorf("缺少筛选框绑定：%s", b)
		}
	}
	// 不许再用内联 onkeydown 偷偷绕过统一绑定。
	if strings.Contains(code, "onkeydown=\"if(event.key==='Enter')loadUsers()\"") {
		t.Error("#userSearch 还在用内联 onkeydown，没走 bindFilterInput()")
	}
	// 导出前同步；且导出参数必须惰性求值，否则同步发生在求值之后、改不了已取到的值。
	if !strings.Contains(code, "function syncFilterInputs(){ filterApplies.forEach(f => f()); }") {
		t.Fatal("缺少 syncFilterInputs()")
	}
	if !strings.Contains(code, "async function exportCsv(kind, params){\n  syncFilterInputs();") {
		t.Fatal("exportCsv() 第一件事必须是 syncFilterInputs()")
	}
	if !strings.Contains(code, "const qs = new URLSearchParams((typeof params === 'function' ? params() : params) || {});") {
		t.Fatal("exportCsv() 必须支持惰性参数（函数），否则同步前就已经把输入框的旧值读走了")
	}
	for _, lazy := range []string{
		"exportCsv('assets', assetFilterParams)",
		"exportCsv('jobs', jobFilterParams)",
		"exportCsv('purchases', purchaseFilterParams)",
		"exportCsv('feedback', fbFilterParams)",
	} {
		if !strings.Contains(code, lazy) {
			t.Errorf("导出按钮必须传惰性参数取值函数：%s", lazy)
		}
	}
}

// ---- 2026-09-12 第六轮：四个后台统一视觉规范 ------------------------------

// 🔴 原生 confirm()/prompt()/alert() 一律不许再用：
//  1. 自动化（验收脚本、浏览器代理）点不到它，于是每一个危险动作都只能靠人手点；
//  2. 部分内嵌浏览器直接禁用原生弹窗，prompt() 返回 null —— 表现是
//     「点了禁用没反应」，而禁用必须填原因这条规则就这么被静默跳过了；
//  3. 样式与后台完全脱节，红色二次确认根本做不出来。
func TestAdminHTMLUsesInPageDialogsNotNativePrompts(t *testing.T) {
	code := adminCode(t)
	for _, bad := range []string{"confirm(", "prompt(", "alert("} {
		// confirmBox( / promptBox( 是页面内对话框，要排除掉再找原生调用。
		cleaned := strings.ReplaceAll(code, "confirmBox(", "")
		cleaned = strings.ReplaceAll(cleaned, "promptBox(", "")
		if strings.Contains(cleaned, bad) {
			t.Errorf("admin.html 仍在用原生 %s)：危险动作必须走页面内对话框", strings.TrimSuffix(bad, "("))
		}
	}
	for _, want := range []string{
		"function openModal(opts)",
		"function confirmBox(title, bodyHtml, opts)",
		"function promptBox(title, bodyHtml, input, opts)",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("缺少页面内对话框：%s", want)
		}
	}
	// 危险动作用红色确认按钮（danger:true 或对话框默认 danger）。
	if !strings.Contains(code, `danger:true`) {
		t.Error("危险操作必须有红色二次确认（danger:true）")
	}
	// 禁用账号那条：原因必填，且必填是在对话框里挡住的。
	if !strings.Contains(code, "required:true") {
		t.Error("禁用账号的原因必须在对话框里就挡住（required:true）")
	}
}

// 缩略图必须按显示尺寸取图。后端支持 ?w=，前端 36×45 的格子请求 96px。
//
// 🔴 线上原图 400KB–1MB，一屏任务表 22 张 ≈ 12MB —— 这就是验收里
// 「缩略图加载慢」的全部原因。大图查看器仍然取原图（data-full）。
func TestAdminHTMLRequestsDownscaledThumbnails(t *testing.T) {
	code := adminCode(t)
	if !strings.Contains(code, "const THUMB_W = 96;") {
		t.Fatal("缺少缩略图请求宽度常量 THUMB_W")
	}
	if !strings.Contains(code, "`&w=${w}`") {
		t.Fatal("assetUrl() 必须把 w= 拼进缩略图 URL")
	}
	if !strings.Contains(code, "assetUrl(aid, THUMB_W)") {
		t.Fatal("thumb() 必须请求下采样后的缩略图，而不是原图")
	}
	if !strings.Contains(code, `data-full="${assetUrl(aid, 0)}"`) {
		t.Fatal("缩略图必须带 data-full（大图查看器用它取原图）")
	}
	if !strings.Contains(code, `loading="lazy"`) {
		t.Error("缩略图应当 loading=\"lazy\"")
	}
}

// 统一视觉规范的骨架：左侧固定导航 220px、顶部栏（产品名 + 环境 + 登出）、
// 内容区 ≤1280px、≤768px 抽屉式导航。
func TestAdminHTMLFollowsSharedLayoutSpec(t *testing.T) {
	code := adminCode(t)
	for _, want := range []string{
		"--nav-w:220px",
		"--content:1280px",
		"--primary:#2563eb",
		"--ok:#16a34a",
		"--warn:#d97706",
		"--err:#dc2626",
		"--muted:#6b7280",
		"--line:#e5e7eb",
		`-apple-system,"PingFang SC","Microsoft YaHei","Noto Sans CJK SC","Segoe UI",sans-serif`,
		"@media (max-width:768px)",
		"body.navopen .nav",
		`id="logoutBtn"`,
		`id="envBadge"`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("不符合统一视觉规范，缺少：%s", want)
		}
	}
	// 🔴 表头吸顶必须让 .tw 自己成为纵向滚动容器。只写 overflow-x:auto 时
	// overflow-y 会被规范提升成 auto，sticky 于是相对这个不滚动的盒子定位 ——
	// 表现是表头被往下推、压住第一行数据（验收当场看到过）。
	if !strings.Contains(code, ".tw{overflow:auto;max-height:") {
		t.Error(".tw 必须是纵向可滚动容器（否则 sticky 表头会压住第一行）")
	}
	if !strings.Contains(code, "th{position:sticky;top:0;") {
		t.Error("表头必须吸顶（position:sticky; top:0）")
	}
	// 数字列右对齐 + 等宽。
	if !strings.Contains(code, "td.num,th.num{text-align:right;font-variant-numeric:tabular-nums;") {
		t.Error("数字列必须右对齐且等宽")
	}
	// 斑马纹。
	if !strings.Contains(code, "tbody tr:nth-child(even) td") {
		t.Error("表格必须有斑马纹")
	}
}

// 🔴 页面上绝不允许出现「—」「undefined」「NaN」这类占位：
// 「—」既可能是「这里本来就没有」也可能是「取数取挂了」，而这两件事的
// 处理方式完全相反。空值一律换成一句人话（「无」「未记录」「游客（无邮箱）」）。
func TestAdminHTMLHasNoDashPlaceholders(t *testing.T) {
	code := adminCode(t)
	if strings.Contains(code, "'—'") || strings.Contains(code, `"—"`) ||
		strings.Contains(code, ">—<") || strings.Contains(code, "|| '—'") {
		t.Error("页面上仍有「—」占位，必须换成说明性的空态文案")
	}
	// isNaN(...) 是**防止**渲染出 NaN 的那段代码本身，摘掉再找裸 NaN。
	noGuards := strings.ReplaceAll(code, "isNaN(", "")
	for _, bad := range []string{"${undefined}", "NaN"} {
		if strings.Contains(noGuards, bad) {
			t.Errorf("页面上可能渲染出 %s", bad)
		}
	}
	// 空态统一走这三个辅助件，避免每处各写各的。
	for _, want := range []string{
		"function none(word)", "function txt(v, word)", "function numCell(v, suffix, word)",
		"function emptyRow(cols, word)", "function errRow(cols, msg)",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("缺少统一空态辅助件：%s", want)
		}
	}
}

// 长 id 必须「截断 + 悬停看全 + 一键复制」：验收里运维要把用户 id 粘到
// 数据库查询台，而页面上只有前 8 位、还不能选中复制。
func TestAdminHTMLLongIDsAreCopyable(t *testing.T) {
	code := adminCode(t)
	for _, want := range []string{
		"function idCell(id, word)", "function userCell(userId, email)",
		`data-copy="${escapeHtml(s)}"`, "navigator.clipboard.writeText(v)",
		".idc code{max-width:13ch;overflow:hidden;text-overflow:ellipsis;",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("长 id 的截断/复制没做全，缺少：%s", want)
		}
	}
}

// 配置页有 50+ 项，一条长滚动没法用：必须有搜索、分组锚点、就地保存提示。
func TestAdminHTMLConfigIsNavigable(t *testing.T) {
	code := adminCode(t)
	for _, want := range []string{
		`id="cfgSearch"`, `id="cfgJump"`, "function filterConfig()", "function cfgSaved(key, msg, isErr)",
		"合法区间 ", "（越界会被拒绝保存，不会悄悄夹一下）",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("配置页缺少：%s", want)
		}
	}
	// 风格 30 个，同样要能搜、能按状态筛。
	for _, want := range []string{`id="styleSearch"`, `id="styleStatusFilter"`, "function styleMatches(s, kw, status)"} {
		if !strings.Contains(code, want) {
			t.Errorf("风格页缺少：%s", want)
		}
	}
}

// 🔴 AI 生成内容标识那一列 / 那一组配置是合规要求（《人工智能生成合成内容标识办法》），
// 不是装饰。后台重构时最容易悄悄丢掉的就是这种「别人刚加进来的一列」——
// 丢了之后没有任何报错，只是再也没人能逐行核对哪张成品标了什么。
func TestAdminHTMLKeepsAIGCLabelSurface(t *testing.T) {
	code := adminCode(t)
	for _, want := range []string{
		"function aigcCell(a){", "a.aigcLabel", "'visible+meta'", "<th>AI 标识</th>",
		"['aigc', 'AI 生成内容标识（合规）'",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("AI 标识功能被弄丢了，缺少：%s", want)
		}
	}
}

// 🔴 六类导出都必须有 from/to 时间筛选，而且用的是**和列表同一个**取值函数。
// 此前只有任务表有「最近 N 小时」，其余四类只能整段导出再在表格里删行 ——
// 导出有 5000 行上限，被截断之后删到最后得到的是一份静悄悄少了几天的账。
func TestAdminHTMLHasTimeRangeFiltersOnEveryExport(t *testing.T) {
	code := adminCode(t)
	if !strings.Contains(code, "function rangeParams(prefix){") {
		t.Fatal("缺少 rangeParams()：起止日期必须走统一取值")
	}
	for _, prefix := range []string{"job", "purchase", "fb", "asset", "user", "event"} {
		for _, suffix := range []string{"From", "To"} {
			if !strings.Contains(code, `id="`+prefix+suffix+`"`) {
				t.Errorf("缺少日期输入框 #%s%s", prefix, suffix)
			}
		}
		if !strings.Contains(code, `rangeParams('`+prefix+`')`) {
			t.Errorf("%s 的筛选参数没有带上起止日期", prefix)
		}
	}
	// 日期一改就要重新拉数据，否则表还是旧的那一份而导出已经按新区间走了。
	for _, bind := range []string{
		"'jobFrom','jobTo'", "'purchaseFrom','purchaseTo'", "'fbFrom','fbTo'",
		"'assetFrom','assetTo'", "'userFrom','userTo'", "'eventFrom','eventTo'",
	} {
		if !strings.Contains(code, bind) {
			t.Errorf("日期输入框没有绑定重新加载：%s", bind)
		}
	}
	// 标签必须写明是北京时间、且「结束日期」含当天 —— 后端按 UTC+8 的半开区间解释。
	if !strings.Contains(code, "开始日期 (UTC+8)") || !strings.Contains(code, "结束日期 (UTC+8，含当天)") {
		t.Error("日期输入框必须标明时区与是否含当天")
	}
}

// 密钥类的东西不露任何片段：会话表此前显示「令牌后 6 位」，现在一个字节都不显示。
func TestAdminHTMLShowsNoTokenFragment(t *testing.T) {
	code := adminCode(t)
	for _, bad := range []string{"tokenTail", "令牌后 6 位"} {
		if strings.Contains(code, bad) {
			t.Errorf("会话表仍在显示令牌片段：%s", bad)
		}
	}
	// 额度到期要在用户详情里看得见：「我买的张数怎么没了」最常见的真因就是 bucket 到期。
	if !strings.Contains(code, "额度到期 (UTC+8)") || !strings.Contains(code, "function ledgerExpiry(l){") {
		t.Error("用户详情的额度账本必须显示 bucket 到期时间")
	}
	// 🔴 字段缺失（后端镜像比这份页面旧）不能被显示成「永不过期」——
	// 那是一个会被客服直接转述给用户的错误答案。
	if !strings.Contains(code, "if (!('expiresAt' in l)) return") {
		t.Error("额度到期必须区分「没有到期时间」与「后端不回这个字段」")
	}
}

// 后台不引任何外部资源：引一个 CDN 就等于给后台加一个我们不控制的单点。
func TestAdminHTMLHasNoExternalResources(t *testing.T) {
	code := adminCode(t)
	for _, bad := range []string{"http://", "https://"} {
		// 允许 SVG 命名空间那一处（不是网络请求）。
		cleaned := strings.ReplaceAll(code, `xmlns="http://www.w3.org/2000/svg"`, "")
		if strings.Contains(cleaned, bad+"cdn") || strings.Contains(cleaned, bad+"unpkg") ||
			strings.Contains(cleaned, bad+"fonts.googleapis") {
			t.Errorf("admin.html 引了外部资源（%s…）", bad)
		}
	}
	if strings.Contains(code, "<script src=") || strings.Contains(code, "<link rel=\"stylesheet\"") {
		t.Error("admin.html 必须是单文件：不许有 <script src> / 外链样式表")
	}
}
