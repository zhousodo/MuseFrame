package httpapi

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateRunesNeverProducesInvalidUTF8 🔴 截断后必须始终是合法 UTF-8。
//
// 按字节切（原实现 s[:n]）会把多字节字符切成两半，产出非法 UTF-8；
// 那串字节原样交给 pgx 后 PostgreSQL 直接拒绝
// （invalid byte sequence for encoding "UTF8"），于是一条正常的中文长评论
// 让 POST /v1/candidates/{id}/feedback 回 500 —— 而 Node 版是收下的。
func TestTruncateRunesNeverProducesInvalidUTF8(t *testing.T) {
	inputs := []string{
		strings.Repeat("留影测试", 400), // 纯中文，3 字节/字
		strings.Repeat("a留影", 400),  // 中英混排，错开字节边界
		strings.Repeat("🎞", 400),    // 4 字节 emoji
		strings.Repeat("é", 900),    // 2 字节
		"短",
		"",
	}
	// 覆盖极小上限和两个真实上限；配合上面错开字节边界的输入，按字节切必然切坏。
	for _, limit := range []int{0, 1, 2, 3, 4, 40, 64, MaxAdminSearch, MaxFeedbackComment} {
		for _, in := range inputs {
			got := truncateRunes(in, limit)
			if !utf8.ValidString(got) {
				t.Fatalf("🔴 limit=%d 截断出非法 UTF-8：%q", limit, got)
			}
			// 上限是**字符数**，不是字节数 —— 与 Node 的 slice(0, n) 同义。
			if n := utf8.RuneCountInString(got); n > limit {
				t.Fatalf("limit=%d 截断后仍有 %d 个字符", limit, n)
			}
			if !strings.HasPrefix(in, got) {
				t.Fatalf("截断结果必须是原串的前缀：%q -> %q", in, got)
			}
			if utf8.RuneCountInString(in) <= limit && got != in {
				t.Fatalf("不超过 %d 个字符的输入不该被改动：%q -> %q", limit, in, got)
			}
		}
	}
}

// TestTruncateRunesCountsCharsNotBytes 🔴 上限按字符算，必须和 Node 一字不差。
//
// Node 侧是 String(comment).slice(0, 1000)（server/api.js:955）、
// 搜索词 .slice(0, 120)（server/admin.js:148），数的都是字符。
// 第一版修复虽然不再切出非法 UTF-8，但把 limit 当成了**字节**上限：
// 一条 1000 字的中文评论只剩 333 个字，同一个请求在两版后端落库的数据不一样，
// 而 user_feedback.comment 是无长度约束的 text，根本没有按字节收紧的理由。
func TestTruncateRunesCountsCharsNotBytes(t *testing.T) {
	// 正好 1000 个汉字 = 3000 字节：Node 整条收下，这里也必须整条收下。
	full := strings.Repeat("留", MaxFeedbackComment)
	if got := truncateRunes(full, MaxFeedbackComment); got != full {
		t.Fatalf("🔴 1000 字的中文评论被截短了：%d 字（Node 是 %d 字，一个都不掉）",
			utf8.RuneCountInString(got), MaxFeedbackComment)
	}
	// 超一个字就切掉一个字，且不多不少。
	if got := truncateRunes(full+"多", MaxFeedbackComment); got != full {
		t.Fatalf("超长时应恰好留 %d 字，实际 %d 字", MaxFeedbackComment, utf8.RuneCountInString(got))
	}
	// emoji 同理：按字符数，不按 4 字节。
	if got := truncateRunes(strings.Repeat("🎞", 40), 40); utf8.RuneCountInString(got) != 40 {
		t.Fatalf("40 个 emoji 应全留，实际 %d 个", utf8.RuneCountInString(got))
	}
	// 搜索词上限同样是字符：120 个汉字原样通过。
	q := strings.Repeat("影", MaxAdminSearch)
	if got := truncateRunes(q, MaxAdminSearch); got != q {
		t.Fatalf("🔴 120 字的搜索词被截短了：%d 字", utf8.RuneCountInString(got))
	}
}

// TestTruncateRunesRepairsInvalidInput 输入本身就非法时也不能把非法字节递下去 ——
// 不经 encoding/json 的路径（查询串、旧数据）没人保证 UTF-8 合法。
func TestTruncateRunesRepairsInvalidInput(t *testing.T) {
	bad := "留影" + string([]byte{0xff, 0xfe}) + "测试"
	for _, limit := range []int{1, 3, 10, MaxFeedbackComment} {
		if got := truncateRunes(bad, limit); !utf8.ValidString(got) {
			t.Fatalf("limit=%d 非法输入未被修正：%q", limit, got)
		}
	}
}

// TestByteSliceWouldHaveBeenInvalid 反向对照：证明原来的按字节切法确实产出非法
// UTF-8 —— 否则上面那条测试可能只是在验证一个不存在的问题。
func TestByteSliceWouldHaveBeenInvalid(t *testing.T) {
	s := strings.Repeat("留影测试", 400)
	if utf8.ValidString(s[:MaxFeedbackComment]) {
		t.Skip("这个上限恰好落在字符边界上，换一个输入再验")
	}
	if !utf8.ValidString(truncateRunes(s, MaxFeedbackComment)) {
		t.Fatal("修复后的截断仍然非法")
	}
	// 搜索词那条路径同理："ab" + 汉字 时第 120 个字节落在一个汉字中间
	// （2 + 3*39 = 119，第 120 字节是半个字），按字节切必出非法 UTF-8。
	q := "ab" + strings.Repeat("留", 60)
	if utf8.ValidString(q[:MaxAdminSearch]) {
		t.Fatal("构造的搜索词没能落在字符中间，换一个再验")
	}
	if !utf8.ValidString(truncateRunes(q, MaxAdminSearch)) {
		t.Fatal("修复后的搜索词截断仍然非法")
	}
}

// TestAdminUsersSearchLongChineseNot500 🔴 端到端：超长中文搜索词不得 500。
//
// 原实现是 search[:120]，按**字节**切：第 120 个字节落在多字节字符中间时就把它切成两半，
// 非法 UTF-8 拼进 LIKE 参数递给 pgx，PostgreSQL 回
// `invalid byte sequence for encoding "UTF8"`，GET /v1/admin/users?q=… 回 500。
// Node 侧是 .slice(0, 120) 数字符，200。
// 下面刻意同时覆盖「字节数恰好对齐」和「落在字符中间」两类输入 —— 纯汉字是前者
// （3 整除 120，看着没事），混了 ASCII 的才是后者，而客服搜人时两种都会输入。
func TestAdminUsersSearchLongChineseNot500(t *testing.T) {
	e := newTestEnv(t)
	e.signUp("search@example.com")

	for _, q := range []string{
		strings.Repeat("留", 40),                        // 120 字节，恰好对齐（原实现也能过）
		strings.Repeat("留", 41),                        // 123 字节，仍然对齐
		"ab" + strings.Repeat("留", 60),                 // 🔴 第 120 字节落在第 40 个汉字中间
		strings.Repeat("留影", 80),                       // 远超上限
		strings.Repeat("🎞", 60),                        // 4 字节字符
		strings.Repeat("留影a", 90),                      // 🔴 7 字节一组，120 不整除
		"zhou@example.com " + strings.Repeat("留影", 90), // 🔴 真实形态：邮箱 + 中文昵称
	} {
		r := e.do("GET", "/v1/admin/users?q="+url.QueryEscape(q), nil, e.admin())
		if r.Code != 200 {
			t.Fatalf("🔴 %d 个字符的中文搜索词应 200（Node 也是 200），实际 %d %s",
				utf8.RuneCountInString(q), r.Code, r.Body)
		}
		if !utf8.Valid(r.Body) {
			t.Fatalf("响应体不是合法 UTF-8：%q", r.Body)
		}
		m := r.Map(t)
		if _, ok := m["users"]; !ok {
			t.Fatalf("出参缺 users 键：%s", r.Body)
		}
	}
}
