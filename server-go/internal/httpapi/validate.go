package httpapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"museframe-api/internal/apierr"
)

// JSON body 没有类型，而每个 handler 都把字段直接绑进 SQL。
// 传对象/数组进来本该是字符串的位置，在 Node 里会从语句里抛 TypeError ——
// 一个带解码器消息的 500，而且有时前一条语句已经提交了。这里一律先拒。

// optionalString 拒绝「存在但类型不对」的可选字符串。
func optionalString(body map[string]any, field string, max int) (*string, error) {
	v, ok := body[field]
	if !ok || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, apierr.New(422, apierr.CodeValidation, field+" must be a string.")
	}
	if len(s) > max {
		return nil, apierr.New(422, apierr.CodeValidation, field+" is too long.")
	}
	return &s, nil
}

// requiredString 在同样的类型边界之上再拒绝缺失/空白。
func requiredString(body map[string]any, field string, max int) (string, error) {
	s, err := optionalString(body, field, max)
	if err != nil {
		return "", err
	}
	if s == nil || trimSpace(*s) == "" {
		return "", apierr.New(422, apierr.CodeValidation, field+" is required.")
	}
	return *s, nil
}

// optionalObject 拒绝「存在但不是普通对象」的可选字段。
// 注意 `controls: null` 在 JS 里会打败 `= {}` 默认值（null 是被提供的值），
// 一路走到 controls.strength 抛 TypeError -> 付费闸之后的 500。
func optionalObject(body map[string]any, field string) (map[string]any, error) {
	v, ok := body[field]
	if !ok || v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, apierr.New(422, apierr.CodeValidation, field+" must be an object.")
	}
	return m, nil
}

// optionalInt 读一个可选整数字段。JSON 数字是 float64，非整数一律拒绝。
func optionalInt(body map[string]any, field string) (*int64, bool, error) {
	v, ok := body[field]
	if !ok || v == nil {
		return nil, ok, nil
	}
	f, ok2 := v.(float64)
	if !ok2 || f != float64(int64(f)) {
		return nil, ok, apierr.New(422, apierr.CodeValidation, field+" must be an integer.")
	}
	n := int64(f)
	return &n, ok, nil
}

// optionalBool 读一个可选布尔字段。
func optionalBool(body map[string]any, field string) (*bool, error) {
	v, ok := body[field]
	if !ok || v == nil {
		return nil, nil
	}
	b, ok2 := v.(bool)
	if !ok2 {
		return nil, apierr.New(422, apierr.CodeValidation, field+" must be a boolean.")
	}
	return &b, nil
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// notFound 是统一的 404。
func notFound(msg string) error { return apierr.New(http.StatusNotFound, apierr.CodeNotFound, msg) }

// truncateRunes 按**字符（rune）**截断，而不是按字节，上限也是**字符数**。
//
// 🔴 按字节切会把一个多字节字符切成两半，产出**非法 UTF-8**。
// Node 版用的是 String(x).slice(0, n)（UTF-16 码元），永远切不出非法字符串；
// Go 的 s[:n] 会。后果是这串非法字节被原样递给 pgx，PostgreSQL 直接拒：
// `invalid byte sequence for encoding "UTF8"` —— 于是一条正常的中文长评论
// 会让 POST /v1/candidates/{id}/feedback 回 500，而 Node 版是收下的。
//
// 🔴 上限为什么是字符数而不是字节数：Node 的 slice(0, 1000) 数的是字符，
// 一条 1000 字的中文评论在 Node 里**整条收下**；按 1000 *字节* 截断只留 333 个字，
// 同一个请求在两版后端产出不同的数据 —— 而 user_feedback.comment 是无长度约束的
// text，没有任何列宽理由要按字节收紧。admin 搜索词的 120 同理（server/admin.js:148）。
// 所以这里按 rune 计数，和 Node 逐字对齐。
//
// 返回值保证是合法 UTF-8：截断只在 rune 边界上发生，而输入里万一已经带了非法
// 字节（不经 encoding/json 的路径），先换成 U+FFFD —— 那串字节原样进 pgx 同样是 500。
func truncateRunes(s string, maxRunes int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if maxRunes <= 0 {
		return ""
	}
	n := 0
	// range 的下标 i 总落在 rune 起始字节上，所以 s[:i] 永远是完整的 rune 序列。
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}
