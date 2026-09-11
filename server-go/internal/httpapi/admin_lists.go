package httpapi

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// MaxAdminSearch 是 GET /v1/admin/users?q= 的搜索词上限，按**字符数**算，
// 与 Node 的 .slice(0, 120)（server/admin.js:148）一致。
const MaxAdminSearch = 120

func clampLimit(raw string, def, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n == 0 {
		n = def
	}
	// 🔴 Node 版是 Math.min(200, Number(limit) || 60)，**负数不被夹**
	// （Math.min(200,-5) = -5 -> SQL LIMIT -5 = 无限制）。Go 版显式夹到 [1, max]。
	if n < 1 {
		n = 1
	}
	if n > max {
		n = max
	}
	return n
}

// AdminOverviewResult 是 GET /v1/admin/overview 的出参，键序固定。
type AdminOverviewResult struct {
	Generation          GenerationSummary    `json:"generation"`
	Abuse               AbuseSummary         `json:"abuse"`
	Users               int                  `json:"users"`
	UsersToday          int                  `json:"usersToday"`
	JobsByStatus        map[string]int       `json:"jobsByStatus"`
	SucceededAvgSeconds *int                 `json:"succeededAvgSeconds"`
	UnitsGranted        int                  `json:"unitsGranted"`
	UnitsConsumed       int                  `json:"unitsConsumed"`
	RevenueMinor        int64                `json:"revenueMinor"`
	Purchases           []store.ProductCount `json:"purchases"`
	Feedback            FeedbackCounts       `json:"feedback"`
	TopStyles           []store.StyleCount   `json:"topStyles"`
}

// FeedbackCounts 是好评 / 差评计数。
type FeedbackCounts struct {
	Positive int `json:"positive"`
	Negative int `json:"negative"`
}

func (a *App) hAdminOverview(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	o, err := store.GetAdminOverview(ctx, a.st.Q(), a.now())
	if err != nil {
		return nil, err
	}
	abuse, err := a.abuseSummary(ctx)
	if err != nil {
		return nil, err
	}
	return AdminOverviewResult{
		Generation: a.generationSummary(ctx), Abuse: abuse,
		Users: o.Users, UsersToday: o.UsersToday, JobsByStatus: o.JobsByStatus,
		SucceededAvgSeconds: o.SucceededAvgSeconds, UnitsGranted: o.UnitsGranted,
		UnitsConsumed: o.UnitsConsumed, RevenueMinor: o.RevenueMinor, Purchases: o.Purchases,
		Feedback:  FeedbackCounts{Positive: o.FeedbackPositive, Negative: o.FeedbackNegative},
		TopStyles: o.TopStyles,
	}, nil
}

func (a *App) hAdminJobs(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	limit := clampLimit(c.URL.Query().Get("limit"), 60, 200)
	rows, err := store.ListAdminJobs(c.R.Context(), a.st.Q(), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"jobs": rows}, nil
}

func (a *App) hAdminFeedback(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListAdminFeedback(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{"feedback": rows}, nil
}

func (a *App) hAdminPurchases(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListAdminPurchases(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{"purchases": rows}, nil
}

func (a *App) hAdminUsers(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	limit := clampLimit(c.URL.Query().Get("limit"), 100, 200)
	// 🔴 必须按 rune 截断。原来是 search[:120]：按**字节**切，第 120 个字节一旦落在
	// 某个多字节字符中间，就把它切成两半 -> 非法 UTF-8 -> 拼进 LIKE 参数递给 pgx ->
	// `invalid byte sequence for encoding "UTF8"` -> GET /v1/admin/users?q=… 回 500。
	// （纯汉字恰好 3 字节对齐、120 整除，看着没事；但只要混进 ASCII ——
	// 客服最常干的就是拿「中文昵称 + 邮箱片段」一起搜 —— 对齐立刻被打破。）
	// Node 侧是 .slice(0, 120)（server/admin.js:148），数的是字符，这里也按字符算。
	search := truncateRunes(strings.ToLower(trimSpace(c.URL.Query().Get("q"))), MaxAdminSearch)
	rows, err := store.ListAdminUsers(c.R.Context(), a.st.Q(), limit, search)
	if err != nil {
		return nil, err
	}
	return map[string]any{"users": rows}, nil
}

func (a *App) hAdminImgToken(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	return map[string]any{"token": a.adminImgToken(a.now().UnixMilli() / 3600000), "ttlSeconds": 3600}, nil
}

// hAdminAssetFile 接受管理员令牌（请求头）**或**一个有效的短时图片令牌 ——
// 后者才是 <img> 标签会带的东西，这样真正的管理员令牌永远不进 URL。
func (a *App) hAdminAssetFile(c *Ctx) (any, error) {
	if it := c.URL.Query().Get("img_token"); !a.adminImgTokenValid(it) {
		if err := a.requireAdmin(c); err != nil {
			return nil, err
		}
	}
	asset, err := store.GetAssetByID(c.R.Context(), a.st.Q(), c.Params[0])
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown asset.")
		}
		return nil, err
	}
	raw, err := os.ReadFile(a.assetPath(asset.StorageKey))
	if err != nil {
		return nil, notFound("File missing.")
	}
	c.W.Header().Set("Content-Type", asset.ContentType)
	c.W.Header().Set("Cache-Control", "private, max-age=300")
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(raw)
	c.Handled = true
	return nil, nil
}

var _ = apierr.CodeNotFound
