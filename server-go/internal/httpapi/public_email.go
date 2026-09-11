package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// emailWindow 是验证码有效期，同时也是「每窗口最多签 5 次」的窗口长度。
//
// 🔴 2026-09-12：原先这里是 `const emailWindow = 10 * time.Minute`，而邮件
// 正文里的「10 分钟内有效」和出参里的 ExpiresInSeconds: 600 是另外三份独立
// 的字面量。改一处就会让信里写的时间和真实有效期对不上，用户照信里写的时间
// 慢慢输码 → 「验证码已过期」，而所有接口回归全绿。现在四处同源，
// 唯一真相源是注册表项 email_code_ttl_seconds（见 cfgstore.Store.EmailCodeTTL）。
func (a *App) emailWindow() time.Duration { return a.rt.EmailCodeTTL() }

// EmailRequestResult 是 POST /v1/auth/email/request 的出参。
type EmailRequestResult struct {
	OK               bool   `json:"ok"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
	DevCode          string `json:"devCode,omitempty"`
}

// hEmailRequest 发一封 6 位验证码。
func (a *App) hEmailRequest(c *Ctx) (any, error) {
	ctx := c.R.Context()
	raw, err := optionalString(c.Body, "email", 254)
	if err != nil {
		return nil, err
	}
	email := ""
	if raw != nil {
		email = strings.ToLower(trimSpace(*raw))
	}
	if email == "" || !emailRe.MatchString(email) {
		return nil, apierr.New(422, apierr.CodeValidation, "请输入有效的邮箱地址。")
	}
	if !a.rt.Bool("email_login_enabled") {
		return nil, apierr.New(501, apierr.CodeProviderNotConfigured, "邮箱登录未开启。")
	}

	now := a.now()
	// 按地址的签发次数上限。仅靠 per-IP 限流不够：那个键是调用方能影响的，
	// 而且重新申请验证码会把 attempts 清零 —— 等于无限新码 × 每码 5 次猜测。
	prior, err := store.GetEmailCode(ctx, a.st.Q(), email)
	if err != nil && !store.IsNoRows(err) {
		return nil, err
	}
	issued := 0
	windowStart := now
	window := a.emailWindow()
	if prior != nil && prior.WindowStart != nil && now.Sub(*prior.WindowStart) < window {
		issued = prior.IssueCount
		windowStart = *prior.WindowStart
	}
	// 🔴 2026-09-12：这里原先是裸字面量 `issued >= 5`。刷码攻击来的时候，把它压到 1
	// 需要改代码 + 发版，而攻击是分钟级的。现在读注册表项 email_code_max_issues_per_window
	// （后台可热改、带 1..20 区间校验），改完下一个请求立即生效。
	if issued >= a.rt.EmailCodeMaxIssuesPerWindow() {
		retry := int((window - now.Sub(windowStart)).Seconds()) + 1
		return nil, apierr.WithDetails(429, apierr.CodeRateLimited, "验证码请求过于频繁，请稍后再试。",
			map[string]any{"retryAfterSeconds": retry})
	}

	// 6 位验证码，落库前哈希；一个邮箱同时只有一个有效码。
	// 必须用 CSPRNG —— 这个码是一个完整的账号凭据。
	code, err := sixDigitCode()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(email + ":" + code))
	codeHash := hex.EncodeToString(sum[:])

	testMode := a.cfg.AllowTestLogin
	if a.mailer == nil || !a.mailer.Configured() {
		return nil, apierr.New(501, apierr.CodeProviderNotConfigured, "邮件服务未配置。")
	}
	if err := a.mailer.SendLoginCode(email, code); err != nil {
		a.lg.Warn("email: 验证码发送失败", nil)
		// 非测试模式下**在 upsert 之前**退出：发送失败还去覆盖存储的哈希，
		// 会把用户手上那个还能用的验证码作废掉。
		if !testMode {
			return nil, apierr.New(502, apierr.CodeEmailSendFailed, "验证码发送失败，请稍后重试。")
		}
	}
	if err := store.UpsertEmailCode(ctx, a.st.Q(), email, codeHash, now.Add(window), now, issued+1, windowStart); err != nil {
		return nil, err
	}

	// 🔴 告诉 App 的数字必须是**刚才真的用掉的那个 window**，不是再算一遍：
	// 中途有人改了配置时，重算会让出参和库里那一行的 expires_at 对不上。
	out := EmailRequestResult{OK: true, ExpiresInSeconds: int(window.Seconds())}
	// 🔴 与另外两个开发逃生口一样是**双闸**：只有旗标时，任何未鉴权调用方
	// 都能拿到**任意地址**的明文验证码 —— 那是所有邮箱账号的接管。
	if testMode && a.isAdminRequest(c.R) {
		out.DevCode = code
	}
	return out, nil
}

func sixDigitCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", err
	}
	return itoa6(int(n.Int64()) + 100000), nil
}

func itoa6(n int) string {
	var b [6]byte
	for i := 5; i >= 0; i-- {
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[:])
}

var sixDigitRe = regexp.MustCompile(`^\d{6}$`)

// hEmailVerify 校验验证码并换取会话。
func (a *App) hEmailVerify(c *Ctx) (any, error) {
	ctx := c.R.Context()
	rawEmail, err := optionalString(c.Body, "email", 254)
	if err != nil {
		return nil, err
	}
	rawCode, err := optionalString(c.Body, "code", 6)
	if err != nil {
		return nil, err
	}
	deviceID, err := optionalString(c.Body, "deviceId", 200)
	if err != nil {
		return nil, err
	}
	locale, err := optionalString(c.Body, "locale", 40)
	if err != nil {
		return nil, err
	}
	email := ""
	if rawEmail != nil {
		email = strings.ToLower(trimSpace(*rawEmail))
	}
	code := ""
	if rawCode != nil {
		code = trimSpace(*rawCode)
	}
	if email == "" || !emailRe.MatchString(email) {
		return nil, apierr.New(422, apierr.CodeValidation, "请输入有效的邮箱地址。")
	}
	if !sixDigitRe.MatchString(code) {
		return nil, apierr.New(422, apierr.CodeCodeInvalid, "验证码不正确。")
	}
	if !a.rt.Bool("email_login_enabled") {
		return nil, apierr.New(501, apierr.CodeProviderNotConfigured, "邮箱登录未开启。")
	}
	rec, err := store.GetEmailCode(ctx, a.st.Q(), email)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, apierr.New(422, apierr.CodeCodeInvalid, "请先获取验证码。")
		}
		return nil, err
	}
	now := a.now()
	if rec.ExpiresAt.Before(now) {
		return nil, apierr.New(422, apierr.CodeCodeExpired, "验证码已过期，请重新获取。")
	}
	// 同上：原先也是裸 5。注册表项 email_code_max_attempts 可热改（区间 1..10）。
	if rec.Attempts >= a.rt.EmailCodeMaxAttempts() {
		return nil, apierr.New(429, apierr.CodeCodeLocked, "尝试次数过多，请重新获取验证码。")
	}
	sum := sha256.Sum256([]byte(email + ":" + code))
	given := []byte(hex.EncodeToString(sum[:]))
	stored := []byte(rec.CodeHash)
	// 常数时间比对。
	if len(given) != len(stored) || subtle.ConstantTimeCompare(given, stored) != 1 {
		if err := store.BumpEmailCodeAttempts(ctx, a.st.Q(), email); err != nil {
			return nil, err
		}
		return nil, apierr.New(422, apierr.CodeCodeInvalid, "验证码不正确。")
	}
	if err := store.DeleteEmailCode(ctx, a.st.Q(), email); err != nil { // 一次性
		return nil, err
	}

	name := email
	if i := strings.Index(email, "@"); i > 0 {
		name = email[:i]
	}
	localeVal := ""
	if locale != nil {
		localeVal = *locale
	}
	// 🔴 provider_subject 的构造规则是 "email:" + lower(trim(邮箱))。
	// 改了它，老用户会解析到新的 user_id，作品 / 额度 / 订单全部丢失。
	userID, err := a.resolveIdentity(ctx, c, "email", "email:"+email, email, name, localeVal, deviceID)
	if err != nil {
		return nil, err
	}
	token := randomToken()
	if err := store.CreateSession(ctx, a.st.Q(), token, userID, deviceID, now, a.rt.SessionTTLDays()); err != nil {
		return nil, err
	}
	u, err := store.GetUserAny(ctx, a.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	return ExchangeResult{AccessToken: token, User: ExchangeUser{
		ID: u.ID, IsGuest: u.IsGuest, DisplayName: u.DisplayName, Email: email,
	}}, nil
}

// hLogout 是 DELETE /v1/auth/session。
// 在这之前，停止使用一个令牌的唯一办法是把它忘掉 —— 那一行在库的生命周期内一直有效。
func (a *App) hLogout(c *Ctx) (any, error) {
	if _, err := requireUser(c); err != nil {
		return nil, err
	}
	header := c.R.Header.Get("Authorization")
	if len(header) > 7 && header[:7] == "Bearer " {
		if err := store.DeleteSession(c.R.Context(), a.st.Q(), header[7:]); err != nil {
			return nil, err
		}
	}
	return map[string]any{"ok": true}, nil
}

var _ = http.StatusOK
