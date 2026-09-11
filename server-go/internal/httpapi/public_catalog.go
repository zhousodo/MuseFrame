package httpapi

import (
	"encoding/json"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// AuthConfig 是 GET /v1/auth/config 的出参，九个顶层键，键序固定。
type AuthConfig struct {
	GuestAllowed     bool           `json:"guestAllowed"`
	Generation       GenerationInfo `json:"generation"`
	FreeRequiresAuth bool           `json:"freeRequiresAuth"`
	FreeUnits        int            `json:"freeUnits"`
	Support          SupportBlock   `json:"support"`
	Google           GoogleBlock    `json:"google"`
	Apple            EnabledBlock   `json:"apple"`
	Email            EnabledBlock   `json:"email"`
	Billing          BillingBlock   `json:"billing"`
}

// SupportBlock 是客服联系方式（空串一律回 null）。
type SupportBlock struct {
	Email   *string `json:"email"`
	QQGroup *string `json:"qqGroup"`
}

// GoogleBlock 是 Google 登录能力。
type GoogleBlock struct {
	Enabled     bool    `json:"enabled"`
	WebClientID *string `json:"webClientId"`
}

// EnabledBlock 只有一个 enabled。
type EnabledBlock struct {
	Enabled bool `json:"enabled"`
}

// BillingBlock 是三种支付渠道的开关。
type BillingBlock struct {
	Google bool `json:"google"`
	Apple  bool `json:"apple"`
	Mock   bool `json:"mock"`
}

func emptyToNil(s string) *string {
	s = trimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// hAuthConfig 是客户端启动时读的能力开关。
func (a *App) hAuthConfig(c *Ctx) (any, error) {
	// enabled 读的是实时配置（后台可改），webClientId 早期只读 env，
	// 于是在面板里配了 client id 会得到 enabled:true + webClientId:null，
	// 客户端显示一个根本点不动的 Google 按钮。
	webClientID := trimSpace(a.cfg.GoogleWebClientID)
	if webClientID == "" {
		ids := splitCSV(a.rt.String("google_client_ids"))
		if len(ids) > 0 {
			webClientID = ids[0]
		}
	}
	return AuthConfig{
		GuestAllowed:     a.rt.Bool("allow_guest"),
		Generation:       a.generationInfo(),
		FreeRequiresAuth: a.rt.Bool("free_requires_auth"),
		FreeUnits:        a.rt.Int("free_units"),
		Support: SupportBlock{
			Email:   emptyToNil(a.rt.String("support_email")),
			QQGroup: emptyToNil(a.rt.String("support_qq_group")),
		},
		Google: GoogleBlock{
			Enabled:     len(splitCSV(a.rt.String("google_client_ids"))) > 0,
			WebClientID: emptyToNil(webClientID),
		},
		Apple: EnabledBlock{Enabled: len(splitCSV(a.rt.String("apple_bundle_ids"))) > 0},
		Email: EnabledBlock{Enabled: a.rt.Bool("email_login_enabled") && a.mailer != nil && a.mailer.Configured()},
		Billing: BillingBlock{
			Google: a.cfg.GoogleServiceAccountJSON != "",
			Apple:  false, // App Store Server API 校验尚未配置
			// 只对运维展示：其他人看到的是「请到商店 App 内购买」。
			Mock: a.cfg.AllowMockPurchases && a.isAdminRequest(c.R),
		},
	}, nil
}

// Shelf 是 /v1/discover 里的一个展架。
type Shelf struct {
	ID             string      `json:"id"`
	Slug           string      `json:"slug"`
	Title          string      `json:"title"`
	CuratorialNote string      `json:"curatorialNote"`
	Edition        string      `json:"edition"`
	Styles         []StyleCard `json:"styles"`
}

// Discover 是 GET /v1/discover 的出参。
// heroExhibition 在没有展览时**该键直接消失**（与 JS 的 undefined 一致）。
type Discover struct {
	Edition        string         `json:"edition"`
	HeroExhibition *Shelf         `json:"heroExhibition,omitempty"`
	Shelves        []Shelf        `json:"shelves"`
	ConfigVersion  int            `json:"configVersion"`
	Generation     GenerationInfo `json:"generation"`
}

func (a *App) planOf(c *Ctx) (string, error) {
	if c.User == nil {
		return "free", nil
	}
	return store.UserPlan(c.R.Context(), a.st.Q(), c.User.ID, a.now())
}

// hDiscover 是首页策展。
func (a *App) hDiscover(c *Ctx) (any, error) {
	ctx := c.R.Context()
	plan, err := a.planOf(c)
	if err != nil {
		return nil, err
	}
	exhibitions, err := store.ListPublishedExhibitions(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	rows, err := store.PublishedStyleRows(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	byExh := map[string][]StyleCard{}
	for _, r := range rows {
		card, err := a.styleCard(r, plan)
		if err != nil {
			return nil, err
		}
		byExh[r.ExhibitionID] = append(byExh[r.ExhibitionID], card)
	}
	shelves := make([]Shelf, 0, len(exhibitions))
	for _, e := range exhibitions {
		styles := byExh[e.ID]
		if styles == nil {
			styles = []StyleCard{}
		}
		shelves = append(shelves, Shelf{
			ID: e.ID, Slug: e.Slug, Title: e.Title, CuratorialNote: e.CuratorialNote,
			Edition: e.Edition, Styles: styles,
		})
	}
	out := Discover{
		// 🔴 硬编码字面量，与 shelf 里的 edition 无关。客户端可能拿它当缓存键。
		Edition: "2026-W33", ConfigVersion: 1, Generation: a.generationInfo(),
		Shelves: []Shelf{},
	}
	if len(shelves) > 0 {
		hero := shelves[0]
		out.HeroExhibition = &hero
		out.Shelves = shelves[1:]
	}
	return out, nil
}

// hStyles 是 GET /v1/styles，只返回已发布风格；空结果必须是 [] 不是 null。
func (a *App) hStyles(c *Ctx) (any, error) {
	plan, err := a.planOf(c)
	if err != nil {
		return nil, err
	}
	rows, err := store.PublishedStyleRows(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	cards := make([]StyleCard, 0, len(rows))
	for _, r := range rows {
		card, err := a.styleCard(r, plan)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return map[string]any{"styles": cards}, nil
}

// StyleDetail 是 GET /v1/styles/{id} 的出参：styleCard 展开 + 四个附加字段。
type StyleDetail struct {
	StyleCard
	Theme         string   `json:"theme"`
	IntentSummary string   `json:"intentSummary"`
	WorksBestWith []string `json:"worksBestWith"`
	Version       int      `json:"version"`
}

// hStyleDetail 是 GET /v1/styles/{styleId}。未命中 -> 404 STYLE_UNAVAILABLE（不是 NOT_FOUND）。
func (a *App) hStyleDetail(c *Ctx) (any, error) {
	plan, err := a.planOf(c)
	if err != nil {
		return nil, err
	}
	rows, err := store.PublishedStyleRows(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.StyleID != c.Params[0] {
			continue
		}
		card, err := a.styleCard(r, plan)
		if err != nil {
			return nil, err
		}
		spec, err := parseSpec(r.Spec)
		if err != nil {
			return nil, err
		}
		var tags []string
		if err := json.Unmarshal(r.SuitabilityTags, &tags); err != nil || tags == nil {
			tags = []string{}
		}
		return StyleDetail{StyleCard: card, Theme: r.Theme, IntentSummary: spec.Intent.Summary,
			WorksBestWith: tags, Version: r.Version}, nil
	}
	return nil, apierr.New(404, apierr.CodeStyleUnavailable, "This direction is not available.")
}

// hEntitlements 是 GET /v1/entitlements/me。
func (a *App) hEntitlements(c *Ctx) (any, error) {
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	return a.entitlements(c.R.Context(), u.ID)
}
