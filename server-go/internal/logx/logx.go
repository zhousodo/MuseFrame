// 一行 JSON 结构化日志。
//
// 🔴 隐私与密钥红线：
//   - 绝不打印任何密钥值。Redact() 把常见凭据形态（DSN 口令、sk- 开头的 key、
//     Bearer 令牌、Authorization / X-Admin-Token 头值）替换掉，作为最后一道保险。
//   - 请求日志只有 {ts, level, msg, method, path, status, ms, requestId}，
//     不记 header / body / 客户端 IP。客户端 IP 只作为限流与反白嫖的内存键存在。
package logx

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sync"
	"time"
)

type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

func New() *Logger { return NewWith(os.Stdout, time.Now) }

func NewWith(w io.Writer, now func() time.Time) *Logger {
	if now == nil {
		now = time.Now
	}
	return &Logger{w: w, now: now}
}

type requestEntry struct {
	TS        string `json:"ts"`
	Level     string `json:"level"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	MS        int64  `json:"ms"`
	RequestID string `json:"requestId"`
}

// LogRequest 记一行请求日志。path 已由调用方裁剪为路由模板或原始路径，
// 绝不带 query string —— 图片令牌与会话令牌都可能出现在 query 里。
func (l *Logger) LogRequest(method, path string, status int, ms int64, requestID string) {
	l.emit(requestEntry{
		TS: l.now().UTC().Format(TimeFormat), Level: "info",
		Method: method, Path: path, Status: status, MS: ms, RequestID: requestID,
	})
}

// Warn 记一行 level=warn 日志；extra 里的字符串一律过 Redact。
func (l *Logger) Warn(msg string, extra map[string]any) { l.leveled("warn", msg, extra) }

// Info 记一行 level=info 日志。
func (l *Logger) Info(msg string, extra map[string]any) { l.leveled("info", msg, extra) }

func (l *Logger) leveled(level, msg string, extra map[string]any) {
	m := map[string]any{"ts": l.now().UTC().Format(TimeFormat), "level": level, "msg": Redact(msg)}
	for k, v := range extra {
		if s, ok := v.(string); ok {
			m[k] = Redact(s)
			continue
		}
		m[k] = v
	}
	l.emit(m)
}

func (l *Logger) emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

// TimeFormat 与业务出参的时间格式一致：ISO-8601 UTC 带毫秒与 Z。
const TimeFormat = "2006-01-02T15:04:05.000Z"

var redactors = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:@\s]+:)[^@\s]+(@)`),
	regexp.MustCompile(`(?i)(password=)\S+`),
	regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]{8,}`),
	regexp.MustCompile(`(?i)(x-admin-token[:=]\s*)\S+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|apikey|token|secret|passwd|pass)["']?\s*[:=]\s*["']?)[^\s"',}]{6,}`),
}

// Redact 把文本里像凭据的部分替换成 <REDACTED>。
// 宁可多删一点，也不让一个密钥进日志。
func Redact(s string) string {
	out := s
	for i, re := range redactors {
		switch i {
		case 0:
			out = re.ReplaceAllString(out, "${1}<REDACTED>${2}")
		case 2:
			out = re.ReplaceAllString(out, "<REDACTED>")
		default:
			out = re.ReplaceAllString(out, "${1}<REDACTED>")
		}
	}
	return out
}
