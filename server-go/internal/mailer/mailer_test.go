package mailer

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

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

// TestLastSendKeepsFullRecipient 收件地址**完整**保留（2026-09-12 起）。
//
// 🔴 这条替换掉了原来的 TestMaskEmailKeepsDomainDropsLocal（断言打码成
// a***@example.cn）。这是一个只有管理员令牌打得开的自家后台，而这个字段
// 唯一的用处是「刚才那封信到底发给谁了」—— 它必须能和用户报的地址对上。
// 打码的版本只能答「某个 example.cn 的人」，于是客服还是得去 SSH 查库。
func TestLastSendKeepsFullRecipient(t *testing.T) {
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "127.0.0.1", "SMTP_USER": "bot@example.cn", "SMTP_PORT": "1",
	}), "pass")
	m.dial = func(string, time.Duration) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	_, _ = m.Send("  alice@example.cn  ", "MuseFrame 邮件配置测试", "t", "")
	last, ok := m.LastSend()
	if !ok {
		t.Fatal("发送失败也必须留痕")
	}
	if last.To != "alice@example.cn" {
		t.Fatalf("收件地址应完整保留（两端空白去掉），实际 %q", last.To)
	}
	if strings.Contains(last.To, "***") {
		t.Fatalf("收件地址不该再被打码，实际 %q", last.To)
	}
}

// TestLastSendNeverLeaksTheCode 🔴 这是本次新增的最重要的一条反向用例。
//
//	验证码信的主题是 `"123456 是你的 MuseFrame 登录验证码"` —— **以明文验证码开头**。
//	SendStatus 里如果有 Subject 字段，那么任何能读到 /v1/admin/config 响应的人
//	（管理令牌持有者、以及令牌泄漏后的任何人）都能拿到「最近这个邮箱的验证码」，
//	等于一条静默的账号接管旁路。所以账本里只记**类别**，主题与正文一个字都不留。
func TestLastSendNeverLeaksTheCode(t *testing.T) {
	const code = "424242"
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "127.0.0.1", "SMTP_USER": "bot@example.cn", "SMTP_PORT": "1",
	}), "the-smtp-password-must-not-leak")
	// dial 直接失败：不需要真的 SMTP 会话，记账路径与成功路径是同一条。
	m.dial = func(string, time.Duration) (net.Conn, error) {
		return nil, errors.New("dial tcp 127.0.0.1:1: connection refused")
	}
	if _, ok := m.LastSend(); ok {
		t.Fatal("还没发过信时 LastSend 应为不存在")
	}
	if err := m.SendLoginCode("alice@example.cn", code); err == nil {
		t.Fatal("dial 失败时应返回错误")
	}
	last, ok := m.LastSend()
	if !ok {
		t.Fatal("发送失败也必须留痕 —— 否则「用户收不到验证码」只能去翻 docker logs")
	}
	if last.OK {
		t.Error("dial 失败时 OK 应为假")
	}
	if last.Kind != "login_code" {
		t.Errorf("类别应为 login_code，实际 %q", last.Kind)
	}
	if last.To != "alice@example.cn" {
		t.Errorf("收件地址应完整回，实际 %q", last.To)
	}
	if last.Error == "" {
		t.Error("失败原因不能为空，否则运营看不出是 DNS、端口还是口令的问题")
	}
	// 🔴 整个账本（含错误文本）里不得出现验证码，也不得出现 SMTP 口令。
	blob := last.Kind + "|" + last.To + "|" + last.Error
	for _, secret := range []string{code, "the-smtp-password-must-not-leak"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("🔴 发送账本里出现了不该出现的值（%d 字节的那个）", len(secret))
		}
	}
	// 后台测试邮件走 Send，类别必须是 manual（成功路径这里也是 dial 失败，
	// 但类别与记账逻辑与成功路径共用同一段代码）。
	_, _ = m.Send("bob@qq.com", "MuseFrame 邮件配置测试", "t", "")
	last2, _ := m.LastSend()
	if last2.Kind != "manual" || last2.To != "bob@qq.com" {
		t.Errorf("后台测试邮件应记作 manual + 完整地址，实际 %+v", last2)
	}
}
