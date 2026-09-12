package httpapi

import (
	"context"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/mailer"
	"museframe-api/internal/store"
	"museframe-api/internal/worker"
)

// GenerationSummary 是面向运维的「这套部署现在能不能生成、用什么生成」。
// 把缺的设置点名说出来，好让一套没配好的服务器在面板上一眼可见，
// 而不是看起来健康、却在背地里拒绝每一个任务。
// available 与 /v1/health 同判据（最近真实上游调用 + 轻量探针），
// 所以后台横幅不会再在「100% 失败」的一周里显示绿色。
type GenerationSummary struct {
	Available      bool     `json:"available"`
	Mode           string   `json:"mode"`
	Provider       string   `json:"provider"`
	Reason         *string  `json:"reason"`
	Missing        []string `json:"missing"`
	LocalFallback  bool     `json:"localFallback"`
	LastError      *string  `json:"lastError"`
	LastErrorAt    *string  `json:"lastErrorAt"`
	LastSuccessAt  *string  `json:"lastSuccessAt"`
	RecentCalls    int      `json:"recentCalls"`
	RecentFailures int      `json:"recentFailures"`
}

func (a *App) generationSummary(ctx context.Context) GenerationSummary {
	g := a.prov.HealthStatus(ctx)
	missing := g.Missing
	if missing == nil {
		missing = []string{}
	}
	return GenerationSummary{
		Available: g.Available, Mode: g.Mode, Provider: a.prov.ProviderName(),
		Reason: g.Reason, Missing: missing, LocalFallback: a.rt.Bool("local_engine_fallback"),
		LastError: g.LastError, LastErrorAt: g.LastErrorAt, LastSuccessAt: g.LastSuccessAt,
		RecentCalls: g.RecentCalls, RecentFailures: g.RecentFailures,
	}
}

// AbuseSummary 是反白嫖状态。游客令牌铸造成本为零，所以免费额度按设备 / IP /
// 全站三层封顶。把离天花板还有多远摆出来，运维才能在把付费密钥放回去之前看清楚。
type AbuseSummary struct {
	FreeGrants24h    int  `json:"freeGrants24h"`
	FreeGrantIps24h  int  `json:"freeGrantIps24h"`
	PerIPCap         int  `json:"perIpCap"`
	PerDayCap        int  `json:"perDayCap"`
	CapReached       bool `json:"capReached"`
	GrantsDisabled   bool `json:"grantsDisabled"`
	GuestAllowed     bool `json:"guestAllowed"`
	FreeRequiresAuth bool `json:"freeRequiresAuth"`
	FreeUnits        int  `json:"freeUnits"`
	MockPurchases    bool `json:"mockPurchases"`
	TestLogin        bool `json:"testLogin"`
}

func (a *App) abuseSummary(ctx context.Context) (AbuseSummary, error) {
	w, err := store.GetFreeGrantWindow(ctx, a.st.Q(), nil, a.now())
	if err != nil {
		return AbuseSummary{}, err
	}
	perIP := a.rt.Int("free_grants_per_ip_day")
	perDay := a.rt.Int("free_grants_per_day")
	return AbuseSummary{
		FreeGrants24h: w.Today, FreeGrantIps24h: w.IPs,
		PerIPCap: perIP, PerDayCap: perDay,
		CapReached:     perDay > 0 && w.Today >= perDay,
		GrantsDisabled: perDay <= 0 || perIP <= 0 || a.rt.Int("free_units") <= 0,
		GuestAllowed:   a.rt.Bool("allow_guest"), FreeRequiresAuth: a.rt.Bool("free_requires_auth"),
		FreeUnits: a.rt.Int("free_units"),
		// 开发水龙头。两者现在都额外要求管理员令牌，但生产里忘关的旗标依然值得点名。
		MockPurchases: a.cfg.AllowMockPurchases, TestLogin: a.cfg.AllowTestLogin,
	}, nil
}

// AdminConfigResult 是 GET/PUT /v1/admin/config 的出参。
type AdminConfigResult struct {
	Settings   []cfgstore.Setting `json:"settings"`
	Generation GenerationSummary  `json:"generation"`
	Abuse      AbuseSummary       `json:"abuse"`
	// Runtime 2026-09-12 新增，见 RuntimeSummary 的说明。
	Runtime RuntimeSummary `json:"runtime"`
}

// RuntimeSummary 是「这个进程现在的实际状态」。
//
// 🔴 为什么配置页必须有这一块（2026-09-12 盘点结论）：
//
//	后台此前能看见**配置**，却看不见**这些配置有没有在起作用**。于是所有
//	「改完没生效 / 用户说收不到信 / 页面转圈」的问题都退化成同一个查法：
//	SSH 上服务器看 docker logs 和 psql —— 而运营没有那台机器的权限。
//	具体这四个真空区：
//	  ① 跑的是哪个镜像、起来多久了 —— 改完配置到底重启没重启，此前无从得知；
//	  ② SMTP 配没配、最近一次发送成功了吗 —— 验证码发不出去只在一行
//	     lg.Warn 里留痕，面板上一片祥和；
//	  ③ 连接池占用 / 队列深度 —— 「转圈」的两个最常见真因；
//	  ④ 最近 24h 的注册/生成/失败/购买 —— 判断「改动有没有把转化打挂」的最小盘。
//
// 🔴 这一块是**纯只读遥测**：没有任何字段会被写回，也没有任何密钥值。
//
//	SMTP 口令只给「已配置」布尔，错误文本过 logx.Redact；收件地址完整
//	（管理员要把它和用户报的地址对上，见 mailer.SendStatus）。
type RuntimeSummary struct {
	// Version 是构建版本（= 镜像 tag 里的 gitsha）。
	Version       string            `json:"version"`
	StartedAt     string            `json:"startedAt"`
	UptimeSeconds int               `json:"uptimeSeconds"`
	ServerTime    string            `json:"serverTime"`
	DB            DBSummary         `json:"db"`
	SMTP          SMTPSummary       `json:"smtp"`
	Queue         worker.Queue      `json:"queue"`
	Today         store.TodayCounts `json:"today"`
	Support       SupportSummary    `json:"support"`
}

// DBSummary 是数据库连通性 + 连接池占用。
type DBSummary struct {
	OK bool `json:"ok"`
	store.PoolStats
}

// SMTPSummary 是邮件通道状态 + 最近一次发送结果。
type SMTPSummary struct {
	Configured bool   `json:"configured"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	From       string `json:"from"`
	// UserConfigured / PassConfigured 只给布尔。smtp_user 虽然在配置清单里就是
	// 明文（它不是密钥），但这里给布尔更直接：运营要的答案是「齐了没」。
	UserConfigured bool `json:"userConfigured"`
	PassConfigured bool `json:"passConfigured"`
	// LoginEnabled 是邮箱验证码登录的总开关。SMTP 配好了但这个关着，
	// 用户照样登不进来 —— 这两件事必须摆在一起看。
	LoginEnabled bool `json:"loginEnabled"`
	// CodeTTLSeconds 是当前真正生效的验证码有效期（已夹过区间）。
	CodeTTLSeconds int `json:"codeTtlSeconds"`
	// LastSend 是最近一次发送尝试；本进程还没发过信时为 nil。
	LastSend *MailSendResult `json:"lastSend"`
}

// MailSendResult 是最近一次发信的结果。**没有主题和正文**：
// 验证码信的主题以明文验证码开头（见 mailer.SendStatus 的说明）。
type MailSendResult struct {
	At    string `json:"at"`
	OK    bool   `json:"ok"`
	Kind  string `json:"kind"`
	To    string `json:"to"`
	Error string `json:"error,omitempty"`
}

// SupportSummary 是展示给用户的求助入口当前值。额度用完时 App 把这两个值
// 摊给用户看，所以它们配错 = 付费转化直接断掉，必须在面板上一眼可见。
type SupportSummary struct {
	Email    string `json:"email"`
	QQGroup  string `json:"qqGroup"`
	Complete bool   `json:"complete"`
}

// MailStatusReporter 是**可选**口：发信器实现了它，后台就能显示最近一次发送结果。
// 做成可选是为了不逼着测试替身实现它（也不让 Mailer 接口变大影响别处）。
type MailStatusReporter interface {
	LastSend() (mailer.SendStatus, bool)
}

func (a *App) runtimeSummary(ctx context.Context) RuntimeSummary {
	now := a.now()
	out := RuntimeSummary{
		Version: a.version, StartedAt: store.ISO(a.startedAt),
		UptimeSeconds: int(now.Sub(a.startedAt).Seconds()),
		ServerTime:    store.ISO(now),
		DB:            DBSummary{OK: a.st.Ready(ctx), PoolStats: a.st.PoolStats()},
		SMTP:          a.smtpSummary(),
		Support: SupportSummary{
			Email:   a.rt.String("support_email"),
			QQGroup: a.rt.String("support_qq_group"),
		},
	}
	out.Support.Complete = out.Support.Email != "" || out.Support.QQGroup != ""
	if a.worker != nil {
		out.Queue = a.worker.Depth()
	}
	// 计数失败不该让整个配置页 500：宁可少一块数字，也不能让运营连配置都打不开。
	if t, err := store.GetTodayCounts(ctx, a.st.Q(), now); err == nil {
		out.Today = t
	} else {
		a.lg.Warn("admin: 24h 计数查询失败", nil)
	}
	return out
}

func (a *App) smtpSummary() SMTPSummary {
	s := SMTPSummary{
		Host: a.rt.String("smtp_host"), Port: a.rt.ClampedInt("smtp_port"),
		From: a.rt.String("smtp_from"), UserConfigured: a.rt.String("smtp_user") != "",
		PassConfigured: a.cfg.SMTPPass != "",
		LoginEnabled:   a.rt.Bool("email_login_enabled"),
		CodeTTLSeconds: int(a.rt.EmailCodeTTL().Seconds()),
	}
	if a.mailer != nil {
		s.Configured = a.mailer.Configured()
	}
	if rep, ok := a.mailer.(MailStatusReporter); ok {
		if last, has := rep.LastSend(); has {
			s.LastSend = &MailSendResult{
				At: store.ISO(last.At), OK: last.OK, Kind: last.Kind,
				To: last.To, Error: last.Error,
			}
		}
	}
	return s
}
