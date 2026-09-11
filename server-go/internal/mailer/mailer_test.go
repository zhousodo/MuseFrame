package mailer

import (
	"strings"
	"testing"

	"museframe-api/internal/cfgstore"
)

// TestLoginCodeBodyValidityFollowsConfig 🔴 信里写的「N 分钟内有效」必须跟
// 真实有效期同源。
//
//	原先「10 分钟」被写死在两段模板里（纯文本 + HTML），而真实有效期由
//	public_email.go 的 emailWindow 决定。任何人把有效期调短，用户都会照信里
//	写的 10 分钟去慢慢输码，然后拿到「验证码已过期」——而所有接口回归全绿，
//	现象和「收不到验证码」混在一起，没法区分是哪一端的问题。
func TestLoginCodeBodyValidityFollowsConfig(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", "10 分钟内有效"},      // 默认 600 秒
		{"600", "10 分钟内有效"},   // 正常值
		{"300", "5 分钟内有效"},    // 调短后信里也必须跟着变
		{"90", "2 分钟内有效"},     // 四舍五入（90s → 2 分钟）
		{"0", "1 分钟内有效"},      // 夹到下界 60 秒
		{"86400", "60 分钟内有效"}, // 夹到上界 1 小时
	}
	for _, c := range cases {
		env := map[string]string{}
		if c.env != "" {
			env["EMAIL_CODE_TTL_SECONDS"] = c.env
		}
		m := New(cfgstore.NewForTest(env), "irrelevant-for-body-assembly")
		_, text, html := m.loginCodeBody("123456")
		// 两份正文必须说同一句话 —— 它们曾经是两处独立的字面量。
		for name, body := range map[string]string{"纯文本": text, "HTML": html} {
			if !strings.Contains(body, c.want) {
				t.Errorf("TTL=%q 时 %s 正文应含 %q，实得:\n%s", c.env, name, c.want, body)
			}
		}
		if c.want != "10 分钟内有效" && (strings.Contains(text, "10 分钟") || strings.Contains(html, "10 分钟")) {
			t.Errorf("TTL=%q 时正文仍残留写死的 10 分钟", c.env)
		}
	}
}

// TestLoginCodeBodyCarriesCode 顺带钉住验证码真的在两份正文和主题里。
func TestLoginCodeBodyCarriesCode(t *testing.T) {
	m := New(cfgstore.NewForTest(map[string]string{}), "x")
	subject, text, html := m.loginCodeBody("987654")
	for name, s := range map[string]string{"主题": subject, "纯文本": text, "HTML": html} {
		if !strings.Contains(s, "987654") {
			t.Errorf("%s 里没有验证码: %s", name, s)
		}
	}
}

// TestConfiguredNeedsAllThree Configured() 是「能不能发信」的唯一判据，
// 三件套缺一不可 —— 缺了还往下走，表现是用户请求挂在 SMTP 超时上。
func TestConfiguredNeedsAllThree(t *testing.T) {
	full := map[string]string{"SMTP_HOST": "smtp.example.invalid", "SMTP_USER": "bot@example.cn"}
	if !New(cfgstore.NewForTest(full), "pass").Configured() {
		t.Fatal("三件套齐备时应为已配置")
	}
	if New(cfgstore.NewForTest(full), "").Configured() {
		t.Error("缺口令时不该算已配置")
	}
	if New(cfgstore.NewForTest(map[string]string{"SMTP_USER": "bot@example.cn"}), "pass").Configured() {
		t.Error("缺 host 时不该算已配置")
	}
	if New(cfgstore.NewForTest(map[string]string{"SMTP_HOST": "smtp.example.invalid"}), "pass").Configured() {
		t.Error("缺 user 时不该算已配置")
	}
}
