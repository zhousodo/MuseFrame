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
	"time"

	"museframe-api/internal/cfgstore"
)

// ErrNotConfigured：SMTP 未配置。
var ErrNotConfigured = errors.New("SMTP_NOT_CONFIGURED")

// Mailer 是 SMTP 发信器。
type Mailer struct {
	rt   *cfgstore.Store
	pass string
	// dial 可注入，测试用。
	dial func(addr string, timeout time.Duration) (net.Conn, error)
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

// Send 发一封信，返回被接收的收件人。
func (m *Mailer) Send(to, subject, text, html string) ([]string, error) {
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
func (m *Mailer) SendLoginCode(to, code string) error {
	subject := code + " 是你的 MuseFrame 登录验证码"
	text := "你的 MuseFrame 登录验证码是：" + code + "\n\n验证码 10 分钟内有效。如果不是你本人操作，请忽略此邮件。"
	html := `<div style="font-family:-apple-system,'PingFang SC',sans-serif;max-width:420px;margin:0 auto;padding:24px">` +
		`<div style="font:600 22px Georgia,serif;letter-spacing:2px;color:#171717">MUSEFRAME</div>` +
		`<p style="color:#6E6B66;font-size:14px">你的登录验证码：</p>` +
		`<div style="font:700 34px ui-monospace,monospace;letter-spacing:8px;color:#1C49D8;padding:8px 0">` + code + `</div>` +
		`<p style="color:#6E6B66;font-size:12px">10 分钟内有效。如果不是你本人操作，请忽略此邮件。</p></div>`
	_, err := m.Send(to, subject, text, html)
	return err
}

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
