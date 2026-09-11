package httpapi

import (
	"net/http"

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
