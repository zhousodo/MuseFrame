// SMTP 发信。服务商无关：在配置面板换 host/user/pass 即可换邮件服务，无需重部署。
//
// 🔴 smtp_pass 是 secret 项，只从环境变量读（见 cfgstore）。这里也只接收
// 一个已经取好的口令字符串，绝不自己去查数据库。
package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/logx"
)

// ErrNotConfigured：SMTP 未配置。
var ErrNotConfigured = errors.New("SMTP_NOT_CONFIGURED")

// SendStatus 是最近一次发送尝试的结果，供后台「应用配置」页显示。
//
// 🔴 这里**刻意没有 Subject 字段**。验证码信的主题是
// `"123456 是你的 MuseFrame 登录验证码"` —— 主题行**以明文验证码开头**。
// 把它记进一个管理员接口返回的结构里，等于给任何拿到管理令牌（或任何能读到
// 这个响应的中间环节）的人一条「最近这个邮箱的验证码是多少」的旁路。
// 所以只记一个**类别**（login_code / manual），正文与主题一个字都不留。
type SendStatus struct {
	At   time.Time
	OK   bool
	Kind string // login_code | manual
	// To 是**打码后**的收件地址（a***@example.com）。后台只需要知道
	// 「刚才那封发给谁了」，不需要完整地址，也不该把用户邮箱摊在面板上。
	To string
	// Error 是失败原因，已过 logx.Redact 并按字符截断 300。
	Error string
}

// Mailer 是 SMTP 发信器。
type Mailer struct {
	rt   *cfgstore.Store
	pass string
	// dial 可注入，测试用。
	dial func(addr string, timeout time.Duration) (net.Conn, error)

	// 最近一次发送结果。SMTP 故障（口令过期、服务商封端口、DNS 改了）此前只在
	// 一行 `a.lg.Warn("email: 验证码发送失败")` 里留痕，而运营看不到 docker logs ——
	// 现象是「用户说收不到验证码」，查法是 SSH。这把锁后的三个字段就是为了
	// 让「最近一次发送成功了吗、什么时候、为什么失败」在后台一眼可见。
	statusMu sync.Mutex
	last     *SendStatus
}

// New 构造发信器。pass 来自环境变量。
func New(rt *cfgstore.Store, pass string) *Mailer {
	return &Mailer{rt: rt, pass: pass, dial: func(addr string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", addr, timeout)
	}}
}

// Configured 判断 host / user / pass 是否齐备。
func (m *Mailer) Configured() bool {
	return m.rt.String("smtp_host") != "" && m.rt.String("smtp_user") != "" && m.pass != ""
}

// LastSend 返回最近一次发送尝试的结果。ok 为假表示本进程还没发过信。
func (m *Mailer) LastSend() (SendStatus, bool) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if m.last == nil {
		return SendStatus{}, false
	}
	return *m.last, true
}

func (m *Mailer) record(to, kind string, err error) {
	st := SendStatus{At: time.Now().UTC(), OK: err == nil, Kind: kind, To: MaskEmail(to)}
	if err != nil {
		st.Error = truncRunes(logx.Redact(err.Error()), 300)
	}
	m.statusMu.Lock()
	m.last = &st
	m.statusMu.Unlock()
}

// MaskEmail 把收件地址打码成 a***@example.com。空串原样返回。
//
// 🔴 域名保留、本地部分只留首字符：后台要能区分「发到 qq.com 失败了」和
// 「发到 gmail.com 失败了」（这是判断是不是被某个服务商拒收的关键），
// 但不需要、也不该展示完整的用户邮箱。
func MaskEmail(to string) string {
	to = strings.TrimSpace(to)
	i := strings.LastIndex(to, "@")
	if i <= 0 {
		if to == "" {
			return ""
		}
		return "***"
	}
	return to[:1] + "***" + to[i:]
}

// truncRunes 按**字符**截断（不是字节）：SMTP 服务商的错误文本常含中文，
// 按字节切会切出半个字，进 JSON 就是一个替换字符。
func truncRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return strings.TrimSpace(s)
	}
	return string(r[:max]) + "…"
}

// Send 发一封信，返回被接收的收件人。kind 记作 manual（后台测试邮件走这条）。
func (m *Mailer) Send(to, subject, text, html string) ([]string, error) {
	accepted, err := m.send(to, subject, text, html)
	m.record(to, "manual", err)
	return accepted, err
}

// send 是真正的 SMTP 会话，不记账 —— 记账由 Send / SendLoginCode 做，
// 因为只有它们知道这封信属于哪个类别。
func (m *Mailer) send(to, subject, text, html string) ([]string, error) {
	if !m.Configured() {
		return nil, ErrNotConfigured
	}
	host := m.rt.String("smtp_host")
	port := m.rt.Int("smtp_port")
	if port <= 0 {
		port = 587
	}
	from := m.rt.String("smtp_from")
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	// 没有这些超时，会继承操作系统的 TCP 超时：一个被黑洞的 SMTP 主机
	// （服务商故障、防火墙变更、端口写错）会把 /v1/auth/email/request
	// 挂住两到十分钟，用户盯着转圈，请求还占着进程。
	conn, err := m.dial(addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if port == 465 { // 465 = 隐式 TLS
		conn = tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer func() { _ = client.Quit() }()

	if port != 465 { // 587 = STARTTLS
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return nil, err
			}
		}
	}
	auth := smtp.PlainAuth("", m.rt.String("smtp_user"), m.pass, host)
	if ok, _ := client.Extension("AUTH"); ok {
		if err := client.Auth(auth); err != nil {
			return nil, err
		}
	}
	if err := client.Mail(fromAddress(from)); err != nil {
		return nil, err
	}
	if err := client.Rcpt(to); err != nil {
		return nil, err
	}
	w, err := client.Data()
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(buildMessage(from, to, subject, text, html)); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return []string{to}, nil
}

// SendLoginCode 发登录验证码。
//
// 🔴 正文里的「N 分钟内有效」必须跟真实有效期**同源**。
// 原先这里把「10 分钟」写死在两段模板里（纯文本 + HTML），而真实有效期由
// public_email.go 的 emailWindow 决定。任何人把有效期调短，用户都会照信里
// 写的 10 分钟去慢慢输码，然后拿到「验证码已过期」—— 而接口回归全绿，
// 现象和「收不到验证码」混在一起没法区分。
// 现在两段模板与有效期一起来自 cfgstore.Store.EmailCodeTTL()。
func (m *Mailer) SendLoginCode(to, code string) error {
	subject, text, html := m.loginCodeBody(code)
	// 🔴 走 m.send 而不是 m.Send：记账时要把类别写成 login_code，
	//    且**绝不能**把 subject 传进账本（它以明文验证码开头）。
	_, err := m.send(to, subject, text, html)
	m.record(to, "login_code", err)
	return err
}

// loginCodeBody 组装验证码信的主题与两份正文。
//
// 🔴 拆出来是为了能在单测里直接断言「信里写的分钟数 == 真实有效期」，
// 不用去假扮一整段 SMTP 会话。两份正文（纯文本 / HTML）共用同一个
// validity 字符串 —— 它们曾经是两处独立的「10 分钟」。
func (m *Mailer) loginCodeBody(code string) (subject, text, html string) {
	validity := itoa(m.codeTTLMinutes()) + " 分钟内有效"
	subject = code + " 是你的 MuseFrame 登录验证码"
	text = "你的 MuseFrame 登录验证码是：" + code + "\n\n验证码 " + validity + "。如果不是你本人操作，请忽略此邮件。"
	html = `<div style="font-family:-apple-system,'PingFang SC',sans-serif;max-width:420px;margin:0 auto;padding:24px">` +
		`<div style="font:600 22px Georgia,serif;letter-spacing:2px;color:#171717">MUSEFRAME</div>` +
		`<p style="color:#6E6B66;font-size:14px">你的登录验证码：</p>` +
		`<div style="font:700 34px ui-monospace,monospace;letter-spacing:8px;color:#1C49D8;padding:8px 0">` + code + `</div>` +
		`<p style="color:#6E6B66;font-size:12px">` + validity + `。如果不是你本人操作，请忽略此邮件。</p></div>`
	return subject, text, html
}

// codeTTLMinutes 是正文里要写的分钟数，四舍五入且至少 1
// （区间下界是 60 秒，所以 1 分钟是真的能取到的值）。
func (m *Mailer) codeTTLMinutes() int {
	d := m.rt.EmailCodeTTL()
	mins := int((d + 30*time.Second) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	return mins
}

// itoa 避免为一个数字引入 strconv（本包此前只用 fmt 拼端口）。
func itoa(n int) string { return fmt.Sprintf("%d", n) }

func fromAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.Index(from[i:], ">"); j > 0 {
			return from[i+1 : i+j]
		}
	}
	return from
}

func buildMessage(from, to, subject, text, html string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + encodeHeader(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	if html != "" {
		boundary := "mf-boundary-7f3a"
		b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + text + "\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + html + "\r\n")
		b.WriteString("--" + boundary + "--\r\n")
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(text + "\r\n")
	}
	return []byte(b.String())
}

// encodeHeader 用 RFC 2047 Base64 编码非 ASCII 主题。
func encodeHeader(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	return "=?UTF-8?B?" + base64Std(s) + "?="
}
