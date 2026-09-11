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
	// 7 个写入口：用户禁用/启用、配置保存、配置恢复默认、风格保存、
	// 风格上下架、商品保存、商品上下架。
	const wantCalls = 7
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
	} {
		if !strings.Contains(html, anchor) {
			t.Fatalf("写操作成功后没有接上 refreshAudit()：%q", anchor)
		}
	}
}
