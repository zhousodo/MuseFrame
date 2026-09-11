package store

import (
	"strings"
	"testing"
)

// 安全问题 2：server_secrets 整表不可浏览。
func TestServerSecretsNotBrowsable(t *testing.T) {
	if IsBrowsableTable("server_secrets") {
		t.Fatal("server_secrets 绝不能出现在浏览白名单里")
	}
	for _, n := range BrowsableTableNames() {
		if n == "server_secrets" {
			t.Fatal("白名单里混进了 server_secrets")
		}
	}
	if len(BrowsableTableNames()) != 23 {
		t.Fatalf("白名单应为 23 张表（24 张业务表去掉 server_secrets），实际 %d", len(BrowsableTableNames()))
	}
}

// 安全问题 2：**白名单**语义 —— 不认识的表一律拒绝，而不是「没被黑名单点名就放行」。
func TestUnknownTableRejected(t *testing.T) {
	for _, n := range []string{"pg_shadow", "information_schema.columns", "pg_authid", "sqlite_master", "nonexistent"} {
		if IsBrowsableTable(n) {
			t.Fatalf("表 %q 不在白名单里，必须拒绝", n)
		}
	}
}

// 安全问题 2 的核心：每个敏感列一个「不应出现在响应里」的断言。
func TestSensitiveColumnsNeverReturnedInClear(t *testing.T) {
	cases := []struct {
		table, column, raw string
	}{
		{"server_secrets", "value", "SUPER-SECRET-SALT-VALUE"},
		{"auth_identities", "email_normalized", "zhousodo@example.com"},
		{"auth_identities", "provider_subject", "email:zhousodo@example.com"},
		{"purchases", "external_transaction_id", "GPA.3312-1234-5678-90123"},
		{"sessions", "device_id", "device-fingerprint-abcdef"},
		{"email_codes", "code_hash", "8f14e45fceea167a5a36dedd4bea2543"},
		{"idempotency_records", "response_body", "{\"job\":{\"id\":\"secret-job\"}}"},
		{"idempotency_records", "request_hash", "deadbeefdeadbeefdeadbeefdeadbeef"},
		{"free_grants", "device_hash", "605fb71109d96a0c907711c9"},
		{"free_grants", "ip_hash", "a1b2c3d4e5f60718293a4b5c"},
		{"manual_grants", "idempotency_key", "panel-click-20260911-0001"},
	}
	for _, c := range cases {
		got := MaskCell(c.table, c.column, c.raw, false)
		s, _ := got.(string)
		if strings.Contains(s, c.raw) {
			t.Errorf("%s.%s 明文泄漏：%q", c.table, c.column, s)
		}
		if RedactionFor(c.table, c.column) == RedactNone {
			t.Errorf("%s.%s 未登记在列级脱敏清单里", c.table, c.column)
		}
	}
}

// sessions.token 保留前 6 位（与 Node 版一致），但剩下的必须没了。
func TestSessionTokenKeepsSixChars(t *testing.T) {
	raw := "abcdefghijklmnopqrstuvwxyz012345"
	got, _ := MaskCell("sessions", "token", raw, false).(string)
	if got != "abcdef\u2026" {
		t.Fatalf("期望前 6 位 + 省略号，实际 %q", got)
	}
	if strings.Contains(got, "ghij") {
		t.Fatal("令牌尾部泄漏")
	}
}

// app_config.value 只在该行 key 属于密钥项时掩码。
func TestAppConfigValueMaskedOnlyForSecretKeys(t *testing.T) {
	if got := MaskCell("app_config", "value", "sk-leaked", true); got != MaskedSecret {
		t.Fatalf("密钥行应掩码，实际 %v", got)
	}
	if got := MaskCell("app_config", "value", "false", false); got != "false" {
		t.Fatalf("非密钥行应原样返回，实际 %v", got)
	}
}

// 通用规则：超长字符串仍然截到 300。
func TestLongStringTruncated(t *testing.T) {
	raw := strings.Repeat("x", 400)
	got, _ := MaskCell("events", "props", raw, false).(string)
	if len([]rune(got)) != 301 {
		t.Fatalf("应截到 300 字符 + 省略号，实际 %d", len([]rune(got)))
	}
}

// 未登记的普通列必须原样返回 —— 否则上面的断言是假绿（因为什么都被掩码了）。
func TestNegativeControl_OrdinaryColumnNotMasked(t *testing.T) {
	if got := MaskCell("users", "display_name", "zhou", false); got != "zhou" {
		t.Fatalf("普通列必须原样返回，实际 %v —— 否则脱敏断言全是假绿", got)
	}
	if got := MaskCell("assets", "storage_key", "52c03f9a.jpg", false); got != "52c03f9a.jpg" {
		t.Fatalf("storage_key 是运维排障必需列，不应被掩码，实际 %v", got)
	}
}

// SQL 控制台的拒绝表清单（在 Node 版四张表的基础上补了两张）。
func TestQueryDenyTables(t *testing.T) {
	for _, tbl := range []string{"sessions", "auth_identities", "app_config", "idempotency_records", "server_secrets", "email_codes"} {
		if !QueryTouchesDeniedTable("select * from " + tbl + " limit 1") {
			t.Errorf("SQL 控制台必须拒绝触达 %s", tbl)
		}
	}
	// 词边界：不能误伤同前缀的合法表名。
	if QueryTouchesDeniedTable("select * from user_feedback") {
		t.Error("不应误伤 user_feedback")
	}
	if QueryTouchesDeniedTable("select count(*) from generation_jobs") {
		t.Error("不应误伤 generation_jobs —— 否则控制台形同虚设（全部拒绝）")
	}
}
