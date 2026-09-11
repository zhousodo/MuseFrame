// 管理后台的只读数据浏览。
//
// 🔴 安全问题 2 的修复点。Node 版的实况是：
//   - QUERY_DENY_TABLES（sessions / auth_identities / app_config / idempotency_records）
//     **只拦 POST /db/query**，对 GET /db/table/{name} 完全不生效；
//   - maskCell 只有 3 条通用规则（sessions.token 留前 6 位 / app_config.value 且 key
//     属于 SECRET_KEYS / 任意 string > 300 截断）。
//
// 于是 server_secrets.value、auth_identities.email_normalized、
// purchases.external_transaction_id 在当前生产实现里是**明文返回**的
// （前两张表现在 0 行，那是「暂时没数据」不是「安全」）。
//
// Go 版改成：**表白名单 + 显式的列级脱敏清单**，不是黑名单。
// 白名单之外的表一律 404；清单里的列一律脱敏。每一条都有对应的
// 「不应出现在响应里」测试。
package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// browsableTables 是允许浏览的表白名单（24 张业务表去掉 server_secrets）。
// server_secrets 存的是服务端持久随机盐与图片令牌 HMAC 密钥，
// 运维没有任何正当理由在面板里看它 —— 整表拒绝。
var browsableTables = map[string]bool{
	"app_config": true, "assets": true, "auth_identities": true, "credit_buckets": true,
	"credit_ledger": true, "email_codes": true, "events": true, "exhibition_styles": true,
	"exhibitions": true, "free_grants": true, "generation_candidates": true, "generation_jobs": true,
	"idempotency_records": true, "manual_grants": true, "photo_analyses": true, "products": true,
	"projects": true, "purchases": true, "sessions": true, "style_versions": true,
	"styles": true, "user_feedback": true, "users": true,
	// server_secrets 刻意不在白名单里。
}

// queryDeniedTables 是自由 SQL 控制台的拒绝表集合。
// 在 Node 版四张表的基础上补上 server_secrets 与 email_codes。
var queryDeniedTables = []string{
	"sessions", "auth_identities", "app_config", "idempotency_records", "server_secrets", "email_codes",
}

// IsBrowsableTable 判断表是否允许浏览。
func IsBrowsableTable(name string) bool { return browsableTables[name] }

// BrowsableTableNames 返回白名单表名（升序）。
func BrowsableTableNames() []string {
	out := make([]string, 0, len(browsableTables))
	for k := range browsableTables {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// QueryDeniedTables 返回 SQL 控制台的拒绝表清单。
func QueryDeniedTables() []string { return append([]string(nil), queryDeniedTables...) }

// QueryTouchesDeniedTable 判断一段 SQL 是否触达拒绝表（按词边界匹配）。
func QueryTouchesDeniedTable(sql string) bool {
	lower := strings.ToLower(sql)
	for _, t := range queryDeniedTables {
		if containsWord(lower, t) {
			return true
		}
	}
	return false
}

func containsWord(s, word string) bool {
	for i := 0; ; {
		idx := strings.Index(s[i:], word)
		if idx < 0 {
			return false
		}
		start := i + idx
		end := start + len(word)
		beforeOK := start == 0 || !isWordByte(s[start-1])
		afterOK := end == len(s) || !isWordByte(s[end])
		if beforeOK && afterOK {
			return true
		}
		i = end
	}
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// RedactKind 是一列的脱敏方式。
type RedactKind int

// 脱敏方式。
const (
	RedactNone RedactKind = iota
	// RedactPrefix6 只留前 6 字符 + 省略号（Node 版对 sessions.token 的做法）。
	RedactPrefix6
	// RedactFull 整体替换为固定掩码，一个字符都不回。
	RedactFull
	// RedactEmail 只留首字母与域名末段。
	RedactEmail
	// RedactSecretByKey 是 app_config.value：该行的 key 属于密钥项时整体掩码。
	RedactSecretByKey
)

// redactColumns 是显式的列级脱敏清单（逐表逐列）。
// 每一条都对应一个「不应出现在响应里」的测试。
var redactColumns = map[string]map[string]RedactKind{
	"sessions": {
		"token":     RedactPrefix6, // 会话令牌就是凭据本身
		"device_id": RedactFull,    // 设备指纹，Node 版明文返回
	},
	"auth_identities": {
		"email_normalized": RedactEmail, // Node 版明文（/db/query 拦得住，/db/table 拦不住）
		"provider_subject": RedactFull,  // 含 "email:<完整邮箱>"
	},
	"email_codes": {
		"code_hash": RedactFull, // 一次性验证码的哈希
	},
	"purchases": {
		"external_transaction_id": RedactFull, // Play / Apple 交易号
	},
	"idempotency_records": {
		"response_body": RedactFull, // Node 版只受 300 字符截断保护
		"request_hash":  RedactFull,
	},
	"free_grants": {
		"device_hash": RedactFull,
		"ip_hash":     RedactFull,
	},
	"manual_grants": {
		"idempotency_key": RedactFull,
	},
	"app_config": {
		"value": RedactSecretByKey,
	},
	"server_secrets": { // 整表已被拒，这里是纵深防御
		"value": RedactFull,
		"key":   RedactFull,
	},
}

// RedactionFor 返回某表某列的脱敏方式（测试会逐条断言）。
func RedactionFor(table, column string) RedactKind {
	if m, ok := redactColumns[table]; ok {
		if k, ok := m[column]; ok {
			return k
		}
	}
	return RedactNone
}

// MaskedValue 是脱敏后的固定掩码文本。
const MaskedValue = "••••(masked)"

// MaskedSecret 是 app_config 密钥项的掩码文本（与 Node 版逐字一致）。
const MaskedSecret = "••••(secret)"

func maskEmail(s string) string {
	at := strings.Index(s, "@")
	if at <= 0 {
		return MaskedValue
	}
	domain := s[at+1:]
	if dot := strings.LastIndex(domain, "."); dot > 0 {
		domain = "***" + domain[dot:]
	} else {
		domain = "***"
	}
	return s[:1] + "***@" + domain
}

// MaskCell 对一个单元格做脱敏。isSecretKey 由调用方按该行的 key 列判断。
//
// 🔴 时区：pgx 把 timestamptz 扫成 time.Time，直接交给 encoding/json 会按
// **进程本地时区**序列化成 2026-09-11T12:26:12.396+08:00。SQLite 版返回的是
// 库里那段 UTC 文本（…Z）。这里统一归一成 UTC ISO-8601 带毫秒与 Z。
func MaskCell(table, column string, v any, isSecretKey bool) any {
	if v == nil {
		return nil
	}
	if t, ok := v.(time.Time); ok {
		v = ISO(t)
	}
	switch RedactionFor(table, column) {
	case RedactPrefix6:
		if s, ok := v.(string); ok {
			if len(s) > 6 {
				return s[:6] + "\u2026"
			}
			return s + "\u2026"
		}
		return MaskedValue
	case RedactFull:
		return MaskedValue
	case RedactEmail:
		if s, ok := v.(string); ok {
			return maskEmail(s)
		}
		return MaskedValue
	case RedactSecretByKey:
		if isSecretKey {
			return MaskedSecret
		}
	}
	// 通用规则：任意超长字符串截到 300 字符（与 Node 版一致）。
	if s, ok := v.(string); ok && len(s) > 300 {
		return s[:300] + "\u2026"
	}
	if b, ok := v.([]byte); ok {
		s := string(b)
		if len(s) > 300 {
			return s[:300] + "\u2026"
		}
		return s
	}
	return v
}

// TableInfo 是 GET /v1/admin/db/tables 的一行。
type TableInfo struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// ListBrowsableTables 列出白名单表与行数。
func ListBrowsableTables(ctx context.Context, q Queryer) ([]TableInfo, error) {
	out := []TableInfo{}
	for _, name := range BrowsableTableNames() {
		var n int64
		// name 来自编译期白名单，不是用户输入；仍然加引号。
		if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %q`, name)).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, TableInfo{Name: name, Rows: n})
	}
	return out, nil
}
