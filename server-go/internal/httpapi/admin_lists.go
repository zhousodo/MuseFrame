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

// jobsNote 是任务视图的口径说明。
const jobsNote = "App 通过 POST /v1/generation-jobs 创建、GET /v1/generation-jobs/{id} 轮询的生成任务。" +
	"可按状态与时间窗筛选；失败原因码、上游返回摘要、生成参数、被重试次数都在行里。" +
	"「重试」新建一条带 parent_job_id 的任务并重新预留额度（失败时已退过，净额不变）。"

func (a *App) hAdminJobs(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	q := c.URL.Query()
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, err
	}
	f := store.JobFilter{
		Status:     pickEnum(q.Get("status"), "created", "queued", "running", "quality_check", "succeeded", "failed", "cancelled"),
		SinceHours: clampOptionalHours(q.Get("sinceHours")),
		UserID:     truncateRunes(trimSpace(q.Get("userId")), 64),
		Range:      tr,
		Limit:      clampLimit(q.Get("limit"), 60, 500),
	}
	rows, err := store.ListAdminJobsFiltered(ctx, a.st.Q(), f, a.now())
	if err != nil {
		return nil, err
	}
	counts, err := store.CountJobsByStatus(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"note": jobsNote + timeRangeNote(tr), "jobs": rows, "statusCounts": counts,
		"filter": map[string]any{"status": f.Status, "sinceHours": f.SinceHours, "userId": f.UserID,
			"from": q.Get("from"), "to": q.Get("to")},
		// 上限是热键：达到它的任务会被 worker 判死，运营据此判断「还会不会自己重试」。
		"maxAttempts": a.worker.MaxAttempts(),
	}, nil
}

// feedbackNote 是反馈视图的口径说明。
const feedbackNote = "App 通过 POST /v1/candidates/{id}/feedback 提交的评价。" +
	"🔴 2026-09-12 起这张表才显示**用户写的正文**（comment 从建库起就在落库，此前后台从未 SELECT 它）。" +
	"「标记已处理」会记一条审计；取消标记会把备注一起清掉。"

func (a *App) hAdminFeedback(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	q := c.URL.Query()
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, err
	}
	f := store.FeedbackFilter{
		Rating:  pickEnum(q.Get("rating"), "positive", "negative"),
		Handled: pickEnum(q.Get("handled"), "yes", "no"),
		Range:   tr,
		Limit:   clampLimit(q.Get("limit"), 100, 500),
	}
	rows, err := store.ListAdminFeedbackFull(ctx, a.st.Q(), f)
	if err != nil {
		return nil, err
	}
	unhandled, err := store.CountFeedbackUnhandled(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"note": feedbackNote + timeRangeNote(tr), "feedback": rows, "unhandled": unhandled,
		"filter": map[string]any{"rating": f.Rating, "handled": f.Handled,
			"from": q.Get("from"), "to": q.Get("to")},
	}, nil
}

// purchaseStatuses 是 purchases.status 的**实际**词汇表。
//
// 🔴 只有这两个值。别按直觉补上 active / refunded / expired ——
// 那会让筛选下拉里出现三个永远筛出 0 行的选项，而运营会据此以为
// 「没有退款单」（真相是退款这件事在这套后端里根本没有状态位）。
var purchaseStatuses = []string{"pending", "verified"}

// purchasesNote 是购买视图的口径说明。
const purchasesNote = "App 通过 POST /v1/purchases/verify 报上来的购买。" +
	"平台 / 交易号 / 金额 / 状态齐全，「入账额度」是这笔钱实际发出去的份数 —— " +
	"status=verified 而入账额度为 0 就是一笔漏入账的事故。" +
	"「重验」只补发缺失的额度（幂等，重复点不会多发），只对 verified 的单子生效；" +
	"它**不会**重新向商店要收据 —— 购买令牌只存在于 App 那一次请求里，从不落库。" +
	"状态只有 pending / verified 两个值。"

func (a *App) hAdminPurchases(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	q := c.URL.Query()
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, err
	}
	f := store.PurchaseFilter{
		Status:   pickEnum(q.Get("status"), purchaseStatuses...),
		Platform: pickEnum(q.Get("platform"), "google", "apple", "web"),
		Range:    tr,
		Limit:    clampLimit(q.Get("limit"), 100, 500),
	}
	rows, err := store.ListAdminPurchasesFull(c.R.Context(), a.st.Q(), f)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"note": purchasesNote + timeRangeNote(tr), "purchases": rows,
		"filter": map[string]any{"status": f.Status, "platform": f.Platform,
			"from": q.Get("from"), "to": q.Get("to")},
	}, nil
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
	tr, err := parseTimeRange(c.URL.Query())
	if err != nil {
		return nil, err
	}
	rows, err := store.ListAdminUsers(c.R.Context(), a.st.Q(), limit, search, tr)
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": usersNote + timeRangeNote(tr), "users": rows}, nil
}

// usersNote 是用户视图的口径说明。
const usersNote = "注册 / 登录（/v1/auth/exchange、/v1/auth/email/verify）产生的账号。" +
	"可按 id 前缀、昵称、邮箱搜索。点一行进详情：额度账本、项目、任务、资产、购买、会话、反馈。" +
	"列表里的 id 只有前 8 位（脱敏），完整 id 在详情页。"

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
	// ?w=96 这样的请求来自后台表格里的 36×45 缩略图：按最长边缩完再发，
	// 省掉「为了画几十个像素推一张 1MB 原图」。不带 w= 的（大图查看器）走原图。
	body, ct := a.thumbnail(asset.ID, parseThumbWidth(c.URL.Query().Get("w")), raw)
	if ct == "" {
		ct = asset.ContentType
	}
	c.W.Header().Set("Content-Type", ct)
	c.W.Header().Set("Cache-Control", "private, max-age=300")
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(body)
	c.Handled = true
	return nil, nil
}

var _ = apierr.CodeNotFound
