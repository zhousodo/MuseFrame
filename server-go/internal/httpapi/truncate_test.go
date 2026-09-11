package httpapi

import (
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
	// 这些上限刚好落在多字节字符中间，是最容易切坏的位置。
	for _, limit := range []int{1, 2, 3, 4, 40, 64, MaxFeedbackComment} {
		for _, in := range inputs {
			got := truncateRunes(in, limit)
			if !utf8.ValidString(got) {
				t.Fatalf("🔴 limit=%d 截断出非法 UTF-8：%q", limit, got)
			}
			if len(got) > limit {
				t.Fatalf("limit=%d 截断后仍有 %d 字节", limit, len(got))
			}
			if len(in) <= limit && got != in {
				t.Fatalf("未超长的输入不该被改动：%q -> %q", in, got)
			}
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
}
