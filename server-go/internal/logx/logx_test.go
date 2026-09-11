package logx

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// 绝不打印密钥：Redact 是最后一道保险。
func TestRedact(t *testing.T) {
	cases := []string{
		"postgres://museframe_app:hunter2@127.0.0.1:5432/museframe",
		"Authorization: Bearer abcdefghijklmnop",
		"x-admin-token: 0123456789abcdef0123456789abcdef",
		"image_provider_api_key=sk-abcdefghijklmnop",
		`{"api_key":"sk-abcdefghijklmnop"}`,
	}
	leaks := []string{"hunter2", "abcdefghijklmnop", "0123456789abcdef0123456789abcdef"}
	for _, c := range cases {
		got := Redact(c)
		for _, leak := range leaks {
			if strings.Contains(got, leak) {
				t.Errorf("Redact 泄漏：输入 %q 输出 %q", c, got)
			}
		}
	}
}

// 负向：普通文本不应被改写，否则日志就没法看了（也说明上一条是假绿）。
func TestNegativeControl_PlainTextUntouched(t *testing.T) {
	s := "worker: 任务无 reserve 台账，标失败不重跑 jobId=462a1e75"
	if Redact(s) != s {
		t.Fatalf("普通文本被误改：%q", Redact(s))
	}
}

// 请求日志不带 ip / ua / header / body。
func TestRequestLogFields(t *testing.T) {
	var buf bytes.Buffer
	lg := NewWith(&buf, func() time.Time { return time.Date(2026, 9, 11, 4, 26, 12, 396e6, time.UTC) })
	lg.LogRequest("POST", "/v1/generation-jobs", 200, 42, "req_deadbeef")
	out := buf.String()
	for _, banned := range []string{"ip", "ua", "authorization", "cookie"} {
		if strings.Contains(strings.ToLower(out), `"`+banned+`"`) {
			t.Fatalf("请求日志不得含 %s：%s", banned, out)
		}
	}
	if !strings.Contains(out, `"ts":"2026-09-11T04:26:12.396Z"`) {
		t.Fatalf("时间格式必须是 UTC ISO-8601 带毫秒与 Z：%s", out)
	}
}
