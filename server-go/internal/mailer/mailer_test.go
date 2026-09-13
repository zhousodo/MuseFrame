package mailer

import (
	"bufio"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
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

// ---- SMTP 会话（真的把一次会话跑到线上字节） --------------------------------
//
// 🔴 这一组是 2026-09-13 补的，对应 README 的 U-2。U-2 原文是「SMTP 从未对
// Brevo 实打实发过一封信」，当天已在生产实发验证（后台测试信 + 真实登录验证码
// 各一封，email-log 的 sends 由 0 变 2）。但那是一次**手工**验证 —— 它证明
// 今天是好的，挡不住明天有人改坏 send()。本包此前的测试全部停在
// loginCodeBody / Configured / record 这些**不碰网络**的部分，
// 也就是说 send() 里真正会让用户收不到信的那几行（信封发件人、STARTTLS、
// 隐式 TLS、口令保护）一行都没被覆盖。下面用假 SMTP 服务端把它们钉死。

// fakeSMTP 是一个只够跑完一次会话的假 SMTP 服务端。
// 返回监听地址、以及一个取回「服务端收到的全部字节」的函数。
type fakeSMTP struct {
	ln        net.Listener
	advertise []string // EHLO 后要广播的扩展行（不含 250- 前缀）
	mu        sync.Mutex
	wire      strings.Builder
	raw       []byte // 收到的头几个原始字节（用来认 TLS ClientHello）
}

func newFakeSMTP(t *testing.T, advertise ...string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	f := &fakeSMTP{ln: ln, advertise: advertise}
	t.Cleanup(func() { _ = ln.Close() })
	go f.serve()
	return f
}

func (f *fakeSMTP) addr() string { return f.ln.Addr().String() }

func (f *fakeSMTP) port() int {
	_, p, _ := net.SplitHostPort(f.addr())
	n, _ := strconv.Atoi(p)
	return n
}

func (f *fakeSMTP) transcript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wire.String()
}

func (f *fakeSMTP) firstBytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.raw...)
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	// 先偷看第一个字节：隐式 TLS（465）时客户端**不等问候语**就直接发
	// ClientHello（0x16 = TLS handshake record）。这是分辨「包了 TLS 没有」
	// 最直接的证据，不用去伪造一张证书。
	// 这一眼必须**限时**：明文客户端正等着我们的 220 问候语，它在收到之前
	// 一个字节都不会发。无限等 = 两边互等，测试挂满超时。超时落空就说明
	// 对面在等问候语，也就是明文。
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if b, err := br.Peek(1); err == nil {
		f.mu.Lock()
		f.raw = append(f.raw, b...)
		f.mu.Unlock()
		if b[0] == 0x16 { // TLS ClientHello：这个假服务端不会握手，到此为止
			return
		}
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	_, _ = conn.Write([]byte("220 fake ESMTP\r\n"))
	inData := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		f.mu.Lock()
		f.wire.WriteString(line)
		f.mu.Unlock()

		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				inData = false
				_, _ = conn.Write([]byte("250 2.0.0 Ok: queued as FAKE123\r\n"))
			}
			continue
		}
		cmd := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			_, _ = conn.Write([]byte("250-fake greets you\r\n"))
			for i, ext := range f.advertise {
				pre := "250-"
				if i == len(f.advertise)-1 {
					pre = "250 "
				}
				_, _ = conn.Write([]byte(pre + ext + "\r\n"))
			}
			if len(f.advertise) == 0 {
				_, _ = conn.Write([]byte("250 SIZE 10240000\r\n"))
			}
		case strings.HasPrefix(cmd, "AUTH"):
			_, _ = conn.Write([]byte("235 2.7.0 Authentication successful\r\n"))
		case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"):
			_, _ = conn.Write([]byte("250 2.1.0 Ok\r\n"))
		case strings.HasPrefix(cmd, "DATA"):
			inData = true
			_, _ = conn.Write([]byte("354 End data with <CR><LF>.<CR><LF>\r\n"))
		case strings.HasPrefix(cmd, "QUIT"):
			_, _ = conn.Write([]byte("221 2.0.0 Bye\r\n"))
			return
		default:
			_, _ = conn.Write([]byte("250 2.0.0 Ok\r\n"))
		}
	}
}

// TestSendSessionUsesBareEnvelopeSenderAndCarriesBothParts 跑完一次完整会话。
//
// 🔴 信封发件人（MAIL FROM）必须是**裸地址**，不能带显示名。生产的
// smtp_from 正是显示名形态 `MuseFrame <no-reply@lenscript.cn>` —— 把整串塞进
// MAIL FROM 会被 Brevo 以 501 语法错退回，现象就是「所有人都收不到验证码」，
// 而后台只看得到一行语焉不详的失败。fromAddress 就是干这个的，这里把它钉在
// 真实的会话字节上，而不是单独测那个函数（单独测挡不住「函数对了但没被调用」）。
//
// 用 127.0.0.1 作 host 是有意的：Go 的 smtp.PlainAuth 只在 localhost 上
// 允许明文口令，这样才能在不伪造证书的前提下把整条命令序列跑到底。
// 非 localhost 的明文拒发由下面的 TestSendRefusesPlaintextCredentials 覆盖。
func TestSendSessionUsesBareEnvelopeSenderAndCarriesBothParts(t *testing.T) {
	f := newFakeSMTP(t, "AUTH PLAIN")
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "127.0.0.1",
		"SMTP_PORT": strconv.Itoa(f.port()),
		"SMTP_USER": "bot@example.cn",
		"SMTP_FROM": "MuseFrame <no-reply@lenscript.cn>",
	}), "pass")

	accepted, err := m.Send("user@example.cn", "主题带中文", "纯文本部分", "<b>HTML 部分</b>")
	if err != nil {
		t.Fatalf("会话应当跑通: %v", err)
	}
	if len(accepted) != 1 || accepted[0] != "user@example.cn" {
		t.Errorf("accepted 应为收件人本身，实得 %v", accepted)
	}

	tr := f.transcript()
	if !strings.Contains(tr, "MAIL FROM:<no-reply@lenscript.cn>") {
		t.Errorf("信封发件人必须是裸地址，实得会话:\n%s", tr)
	}
	if strings.Contains(tr, "MAIL FROM:<MuseFrame") {
		t.Errorf("显示名漏进了 MAIL FROM（Brevo 会 501 退回）:\n%s", tr)
	}
	if !strings.Contains(tr, "RCPT TO:<user@example.cn>") {
		t.Errorf("会话里没有正确的 RCPT TO:\n%s", tr)
	}
	// From 头部则**保留**显示名 —— 用户在收件箱里看到的是「MuseFrame」。
	if !strings.Contains(tr, "From: MuseFrame <no-reply@lenscript.cn>") {
		t.Errorf("From 头应保留显示名:\n%s", tr)
	}
	// 中文主题必须是 RFC 2047 编码过的，不能裸 UTF-8 进头部。
	if !strings.Contains(tr, "Subject: =?UTF-8?B?") {
		t.Errorf("非 ASCII 主题必须 RFC 2047 编码:\n%s", tr)
	}
	// 两份正文都要在信里 —— 只发 HTML 的话纯文本客户端看到空白信。
	for _, want := range []string{"text/plain; charset=utf-8", "text/html; charset=utf-8", "纯文本部分", "<b>HTML 部分</b>"} {
		if !strings.Contains(tr, want) {
			t.Errorf("信体缺少 %q:\n%s", want, tr)
		}
	}
	// 记账也得跟上（后台「发信记录」就是读这个）。
	if st, ok := m.LastSend(); !ok || !st.OK || st.Kind != "manual" {
		t.Errorf("成功的一次发送应记成 manual/OK，实得 %+v ok=%v", st, ok)
	}
}

// TestSendLoginCodeNeverPutsCodeInTheLedger 验证码可以上线（收件人要看），
// 但**不能**进 LastSend —— 那个结构会从管理员接口原样回出去。
func TestSendLoginCodeNeverPutsCodeInTheLedger(t *testing.T) {
	f := newFakeSMTP(t, "AUTH PLAIN")
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "127.0.0.1",
		"SMTP_PORT": strconv.Itoa(f.port()),
		"SMTP_USER": "bot@example.cn",
		"SMTP_FROM": "MuseFrame <no-reply@lenscript.cn>",
	}), "pass")

	if err := m.SendLoginCode("user@example.cn", "424242"); err != nil {
		t.Fatalf("验证码信应当发出: %v", err)
	}
	if tr := f.transcript(); !strings.Contains(tr, "424242") {
		t.Errorf("验证码没进信里，用户收到的是一封空信:\n%s", tr)
	}
	st, ok := m.LastSend()
	if !ok || st.Kind != "login_code" {
		t.Fatalf("应记成 login_code，实得 %+v ok=%v", st, ok)
	}
	// 🔴 SendStatus 会经管理员接口出网；主题以明文验证码开头，一个字节都不能带。
	if strings.Contains(st.To+st.Error+st.Kind, "424242") {
		t.Errorf("验证码漏进了发送台账: %+v", st)
	}
}

// TestSendRefusesPlaintextCredentials 🔴 服务端不广播 STARTTLS 时，
// 口令绝不能明文上线。
//
//	send() 里的 STARTTLS 是**条件执行**的（`if ok := Extension("STARTTLS")`）——
//	一个降级的（或被中间人剥掉 STARTTLS 广播的）服务端会让那段整块跳过。
//	兜底的是 Go 的 smtp.PlainAuth：它在非 localhost 的明文连接上拒绝交出口令。
//	这条测试钉住那层兜底还在：失败可以接受，口令上线不行。
func TestSendRefusesPlaintextCredentials(t *testing.T) {
	f := newFakeSMTP(t, "AUTH PLAIN") // 有意不广播 STARTTLS
	host, _, _ := net.SplitHostPort(f.addr())
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "smtp.example.invalid", // 非 localhost，触发 PlainAuth 的保护
		"SMTP_PORT": strconv.Itoa(f.port()),
		"SMTP_USER": "bot@example.cn",
		"SMTP_FROM": "MuseFrame <no-reply@lenscript.cn>",
	}), "super-secret-pass")
	// host 只用于 TLS/认证的服务器名，实际拨号仍打到假服务端。
	m.dial = func(_ string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(f.port())), timeout)
	}

	if _, err := m.Send("user@example.cn", "s", "t", ""); err == nil {
		t.Fatal("明文连接上不该发信成功")
	}
	if tr := f.transcript(); strings.Contains(tr, "super-secret-pass") ||
		strings.Contains(tr, base64Std("\x00bot@example.cn\x00super-secret-pass")) {
		t.Errorf("口令明文上线了:\n%s", tr)
	}
	// 失败原因也要经过脱敏后进台账，且不能带口令。
	if st, ok := m.LastSend(); ok && strings.Contains(st.Error, "super-secret-pass") {
		t.Errorf("口令漏进了失败记录: %q", st.Error)
	}
}

// TestPort465UsesImplicitTLS 465 是隐式 TLS：客户端必须**不等问候语**
// 直接开握手。写错成明文的话，会话会挂在「等 220」上直到超时 ——
// 现象是发信整体卡住，而不是一个能看懂的错误。
func TestPort465UsesImplicitTLS(t *testing.T) {
	f := newFakeSMTP(t)
	host, _, _ := net.SplitHostPort(f.addr())
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "smtp.example.invalid",
		"SMTP_PORT": "465",
		"SMTP_USER": "bot@example.cn",
	}), "pass")
	m.dial = func(_ string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(f.port())), timeout)
	}
	// 假服务端不会握手，所以这里必然失败；要断言的是**失败之前发了什么**。
	_, _ = m.Send("user@example.cn", "s", "t", "")

	got := f.firstBytes()
	if len(got) == 0 || got[0] != 0x16 {
		t.Errorf("465 的第一个字节应是 TLS ClientHello(0x16)，实得 %v；会话:\n%s", got, f.transcript())
	}
}

// TestDefaultPortIsSubmission 端口留空时落到 587（submission）。
// 落到 0 或 25 都会让发信在生产上整体失败。
func TestDefaultPortIsSubmission(t *testing.T) {
	var dialed string
	m := New(cfgstore.NewForTest(map[string]string{
		"SMTP_HOST": "smtp.example.invalid", "SMTP_USER": "bot@example.cn", "SMTP_PORT": "0",
	}), "pass")
	m.dial = func(addr string, _ time.Duration) (net.Conn, error) {
		dialed = addr
		return nil, errors.New("stop here")
	}
	_, _ = m.Send("user@example.cn", "s", "t", "")
	if dialed != "smtp.example.invalid:587" {
		t.Errorf("端口缺省应为 587，实得拨号地址 %q", dialed)
	}
}
