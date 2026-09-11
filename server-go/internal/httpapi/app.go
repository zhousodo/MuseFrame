// HTTP 层：路由表 + 横切中间层。零框架，路由表就是一个切片 + 正则，
// 与 Node 版 server/index.js + api.js 的 routes 数组逐条对照。
package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/cfgstore"
	"museframe-api/internal/config"
	"museframe-api/internal/logx"
	"museframe-api/internal/netx"
	"museframe-api/internal/oidc"
	"museframe-api/internal/play"
	"museframe-api/internal/provider"
	"museframe-api/internal/ratelimit"
	"museframe-api/internal/store"
	"museframe-api/internal/worker"
)

// Ctx 是一次请求的上下文，字段与 Node 版 handler 的 ctx 一一对应。
type Ctx struct {
	W         http.ResponseWriter
	R         *http.Request
	URL       *url.URL
	User      *store.User
	Params    []string
	Body      map[string]any
	Raw       []byte
	ClientIP  string
	RequestID string
	// Handled 置为 true 表示 handler 已经自己写完了响应（二进制图片等）。
	Handled bool
}

// HandlerFunc 返回要序列化成 JSON 的值；返回 nil 且 Handled 为真表示已自行写出。
type HandlerFunc func(c *Ctx) (any, error)

type route struct {
	method  string
	pattern *regexp.Regexp
	handler HandlerFunc
	// name 用于日志（避免把含令牌的原始路径写进日志）。
	name string
}

// App 持有全部依赖。
type App struct {
	cfg        *config.Config
	rt         *cfgstore.Store
	st         *store.Store
	lg         *logx.Logger
	limiter    *ratelimit.Limiter
	ipr        *netx.IPResolver
	prov       *provider.Adapter
	worker     *worker.Worker
	mailer     Mailer
	verifier   *oidc.Verifier
	playClient *play.Client
	routes     []route
	now        func() time.Time
	newID      func() string
	imgHMAC    []byte
	ipSalt     string
	version    string
	adminTok   []byte
	// startedAt 是进程起来的时刻（用注入的时钟取，测试里是确定值）。
	// 后台据此显示「跑了多久」—— 「改完配置到底有没有重启」此前在面板上无从得知。
	startedAt time.Time
}

// Mailer 是发信口；生产用 SMTP 实现，测试注入假实现。
type Mailer interface {
	Configured() bool
	SendLoginCode(to, code string) error
	Send(to, subject, text, html string) ([]string, error)
}

// Options 是构造参数。
type Options struct {
	Config      *config.Config
	Runtime     *cfgstore.Store
	Store       *store.Store
	Logger      *logx.Logger
	Provider    *provider.Adapter
	Worker      *worker.Worker
	Mailer      Mailer
	Play        *play.Client
	Verifier    *oidc.Verifier
	Version     string
	Now         func() time.Time
	NewID       func() string
	ImgTokenKey []byte
}

// New 构造 App 并注册全部 50 条路由。
func New(o Options) *App {
	now := o.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := o.NewID
	if newID == nil {
		newID = NewUUID
	}
	verifier := o.Verifier
	if verifier == nil {
		verifier = oidc.New()
	}
	playClient := o.Play
	if playClient == nil {
		playClient = play.New("", "")
	}
	a := &App{
		cfg: o.Config, rt: o.Runtime, st: o.Store, lg: o.Logger,
		prov: o.Provider, worker: o.Worker, mailer: o.Mailer,
		verifier: verifier, playClient: playClient,
		now: now, newID: newID, version: o.Version, imgHMAC: o.ImgTokenKey,
		ipSalt:   o.Config.IPHashSalt,
		adminTok: []byte(o.Config.AdminToken),
		limiter:  ratelimit.New(o.Config.RateLimitMaxKeys, func() time.Time { return now() }),
		ipr:      netx.NewIPResolver(o.Config.TrustedProxy, o.Config.TrustCFIP),
	}
	a.startedAt = now()
	a.registerPublicRoutes()
	a.registerAdminRoutes()
	return a
}

// RouteCount 返回已注册路由数（回归用：公开 30 + 管理 20 = 50）。
func (a *App) RouteCount() (public, admin int) {
	for _, r := range a.routes {
		if strings.Contains(r.pattern.String(), "/v1/admin/") {
			admin++
		} else {
			public++
		}
	}
	return
}

func (a *App) add(method, pattern string, h HandlerFunc) {
	a.routes = append(a.routes, route{
		method:  method,
		pattern: regexp.MustCompile("^" + pattern + "$"),
		handler: h,
		name:    method + " " + pattern,
	})
}

// 限流规则表（index.js:118-132）。键 = 规则源串 + 客户端 IP。
type rlRule struct {
	re     *regexp.Regexp
	method string
	limit  int
	window time.Duration
}

var rlRules = []rlRule{
	{regexp.MustCompile(`^/v1/auth/exchange$`), "", 20, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/auth/email/request$`), "", 5, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/auth/email/verify$`), "", 20, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/generation-jobs$`), "POST", 40, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/purchases/verify$`), "", 30, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/assets/upload-intents$`), "", 60, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/events$`), "POST", 120, 10 * time.Minute},
	{regexp.MustCompile(`^/v1/admin/`), "", 120, time.Minute},
}

// SweepRateLimiter 清掉过期的限流键（调用方每 60 秒跑一次）。
func (a *App) SweepRateLimiter() int { return a.limiter.Sweep() }

// NewUUID 生成 RFC 4122 v4 UUID（与 Node 的 randomUUID 同形态）。
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("系统随机数不可用")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[j] = '-'
			j++
		}
		out[j] = hex[b[i]>>4]
		out[j+1] = hex[b[i]&0x0f]
		j += 2
	}
	return string(out)
}

// randomToken 生成会话令牌：randomBytes(24) 的 base64url = 32 字符（与 Node 一致）。
func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("系统随机数不可用")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// jsonUnmarshal 把请求体解成 map（非对象体一律按空 map 处理，与 JS 的
// `ctx.body || {}` 等价；但畸形 JSON 必须报 400 VALIDATION）。
func jsonUnmarshal(raw []byte) (map[string]any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, apierr.New(http.StatusBadRequest, apierr.CodeValidation, "Invalid JSON body.")
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	return map[string]any{}, nil
}

// readBody 复刻 index.js 的 readBody：超限时不立刻断链，在预算内继续丢弃读取，
// 以便把 413 写在健康连接上（否则客户端只看到网络错误并重试同一个大包）。
// 这是行为契约，不是实现细节。
func readBody(r *http.Request, limit int64) ([]byte, error) {
	tooLarge := apierr.New(http.StatusRequestEntityTooLarge, apierr.CodeAssetUnsupported, "Payload too large.")
	if r.ContentLength > limit {
		_, _ = io.CopyN(io.Discard, r.Body, netx.DrainBudget(limit))
		return nil, tooLarge
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, apierr.New(http.StatusBadRequest, apierr.CodeValidation, "Request aborted.")
	}
	if int64(len(buf)) > limit {
		_, _ = io.CopyN(io.Discard, r.Body, netx.DrainBudget(limit))
		return nil, tooLarge
	}
	return buf, nil
}

// asAPIError 把任意错误翻成 (status, code, message, details)。
func asAPIError(err error) *apierr.Error {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e
	}
	return apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Internal error.")
}
