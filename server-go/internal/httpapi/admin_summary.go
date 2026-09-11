package httpapi

import (
	"context"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/store"
)

// GenerationSummary 是面向运维的「这套部署现在能不能生成、用什么生成」。
// 把缺的设置点名说出来，好让一套没配好的服务器在面板上一眼可见，
// 而不是看起来健康、却在背地里拒绝每一个任务。
type GenerationSummary struct {
	Available     bool     `json:"available"`
	Mode          string   `json:"mode"`
	Provider      string   `json:"provider"`
	Reason        *string  `json:"reason"`
	Missing       []string `json:"missing"`
	LocalFallback bool     `json:"localFallback"`
}

func (a *App) generationSummary() GenerationSummary {
	g := a.prov.Status()
	return GenerationSummary{
		Available: g.Available, Mode: g.Mode, Provider: a.prov.ProviderName(),
		Reason: g.Reason, Missing: g.Missing, LocalFallback: a.rt.Bool("local_engine_fallback"),
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
}
