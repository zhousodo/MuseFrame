// 后台 CSV 导出。一个路由覆盖六类数据：GET /v1/admin/export/{kind}.csv
//
// 🔴 一律脱敏，而且脱敏点只有一处（store.CSVMaskEmail / store.CSVText）。
// 理由：导出文件会离开这台机器 —— 进运营的下载目录、微信、某个表格。
// 页面上显示完整邮箱还能靠「只有持令牌的人打得开」兜住，一个躺在下载目录里的
// CSV 兜不住。把打码散在六个 handler 里写六遍，等于保证总有一个会漏。
//
// 🔴 导出的行数与口径必须和页面上那张表**完全一致**（同一个 store 函数、
// 同一套筛选参数）。一个「导出比页面多几行」的 CSV 会被当成页面漏了数据，
// 然后有人按 CSV 去做对账。
package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// exportKinds 是允许导出的六类数据，和后台的六个列表一一对应。
var exportKinds = []string{"users", "jobs", "purchases", "feedback", "events", "assets"}

// hAdminExportCSV 是 GET /v1/admin/export/{kind}.csv。
func (a *App) hAdminExportCSV(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	kind := pickEnum(c.Params[0], exportKinds...)
	if kind == "" {
		return nil, apierr.WithDetails(404, apierr.CodeNotFound,
			"没有这类导出。", map[string]any{"supported": exportKinds})
	}
	q := c.URL.Query()
	// 导出上限刻意比页面高一档（页面 500、导出 5000）：导出的用处就是
	// 拿走比一屏更多的东西。但仍然有上限 —— 没有上限的导出是一个
	// 任何持令牌的人都能触发的 OOM。
	limit := clampLimit(q.Get("limit"), 1000, 5000)

	var header []string
	var rows [][]string
	var err error
	switch kind {
	case "users":
		header, rows, err = a.exportUsers(ctx, q, limit)
	case "jobs":
		header, rows, err = a.exportJobs(ctx, q, limit)
	case "purchases":
		header, rows, err = a.exportPurchases(ctx, q, limit)
	case "feedback":
		header, rows, err = a.exportFeedback(ctx, q, limit)
	case "events":
		header, rows, err = a.exportEvents(ctx, q, limit)
	case "assets":
		header, rows, err = a.exportAssets(ctx, q, limit)
	}
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	// UTF-8 BOM：不带它，Excel（中文 Windows 默认 GBK）会把每一个中文列
	// 读成乱码 —— 而这个后台的风格名、反馈正文、备注全是中文。
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}

	name := "museframe-" + kind + "-" + a.now().UTC().Format("20060102T150405Z") + ".csv"
	c.W.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// 导出里有用户数据，绝不让任何中间层缓存。
	c.W.Header().Set("Cache-Control", "no-store")
	// X-Row-Count 让调用方（和验收脚本）不用解析 CSV 就能核对行数。
	c.W.Header().Set("X-Row-Count", strconv.Itoa(len(rows)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(buf.Bytes())
	c.Handled = true
	return nil, nil
}

func (a *App) exportUsers(ctx context.Context, q urlValues, limit int) ([]string, [][]string, error) {
	search := truncateRunes(strings.ToLower(trimSpace(q.Get("q"))), MaxAdminSearch)
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, nil, err
	}
	rows, err := store.ListAdminUsers(ctx, a.st.Q(), limit, search, tr)
	if err != nil {
		return nil, nil, err
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			// id 只导前 8 位 —— 和页面同一个脱敏口径。
			r.ID, store.CSVText(r.DisplayName), store.CSVMaskEmail(r.Email),
			store.CSVText(r.Providers), r.Status, csvBoolStr(r.IsGuest),
			itoa(r.Units), itoa(r.Jobs), r.CreatedAt,
		})
	}
	return []string{"用户id前8位", "昵称", "邮箱(已打码)", "登录方式", "状态", "是否游客", "额度净额", "任务数", "注册时间UTC"}, out, nil
}

func (a *App) exportJobs(ctx context.Context, q urlValues, limit int) ([]string, [][]string, error) {
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, nil, err
	}
	f := store.JobFilter{
		Status:     pickEnum(q.Get("status"), "created", "queued", "running", "quality_check", "succeeded", "failed", "cancelled"),
		SinceHours: clampOptionalHours(q.Get("sinceHours")),
		UserID:     truncateRunes(trimSpace(q.Get("userId")), 64),
		Range:      tr,
		Limit:      limit,
	}
	rows, err := store.ListAdminJobsFiltered(ctx, a.st.Q(), f, a.now())
	if err != nil {
		return nil, nil, err
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.ID, r.Status, r.Stage, derefStr(r.ErrorCode), itoa(r.AttemptCount), itoa(r.RetryCount),
			r.Style, r.User, store.CSVMaskEmail(r.Email), intPtrStr(r.Seconds),
			itoa(r.ReservedUnits), itoa64(r.CostMinor), r.CreatedAt, rawJSONStr(r.UpstreamSummary),
		})
	}
	return []string{"任务id", "状态", "阶段", "失败原因码", "尝试次数", "被重试次数", "风格", "用户id前8位",
		"邮箱(已打码)", "耗时秒", "预留额度", "成本(分)", "创建时间UTC", "上游返回摘要"}, out, nil
}

func (a *App) exportPurchases(ctx context.Context, q urlValues, limit int) ([]string, [][]string, error) {
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, nil, err
	}
	f := store.PurchaseFilter{
		Status:   pickEnum(q.Get("status"), purchaseStatuses...),
		Platform: pickEnum(q.Get("platform"), "google", "apple", "web"),
		Range:    tr,
		Limit:    limit,
	}
	rows, err := store.ListAdminPurchasesFull(ctx, a.st.Q(), f)
	if err != nil {
		return nil, nil, err
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.ID, r.Platform, r.TxID, r.Status, r.Product, r.ProductKey,
			int64PtrStr(r.AmountMinor), derefStr(r.Currency), itoa(r.UnitsGranted),
			r.User, store.CSVMaskEmail(r.Email), r.PurchasedAt, derefStr(r.ExpiresAt),
		})
	}
	return []string{"购买id", "平台", "交易号", "状态", "商品", "商品key", "金额(分)", "币种",
		"入账额度", "用户id前8位", "邮箱(已打码)", "购买时间UTC", "到期时间UTC"}, out, nil
}

func (a *App) exportFeedback(ctx context.Context, q urlValues, limit int) ([]string, [][]string, error) {
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, nil, err
	}
	f := store.FeedbackFilter{
		Rating:  pickEnum(q.Get("rating"), "positive", "negative"),
		Handled: pickEnum(q.Get("handled"), "yes", "no"),
		Range:   tr,
		Limit:   limit,
	}
	rows, err := store.ListAdminFeedbackFull(ctx, a.st.Q(), f)
	if err != nil {
		return nil, nil, err
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.ID, r.Rating, rawJSONStr(r.ReasonCodes), store.CSVText(r.Comment),
			store.CSVText(r.Style), r.User, store.CSVMaskEmail(r.Email),
			r.CreatedAt, derefStr(r.HandledAt), store.CSVText(r.HandledNote),
		})
	}
	return []string{"反馈id", "评价", "原因码", "用户正文", "风格", "用户id前8位", "邮箱(已打码)",
		"提交时间UTC", "处理时间UTC", "处理备注"}, out, nil
}

func (a *App) exportEvents(ctx context.Context, q urlValues, limit int) ([]string, [][]string, error) {
	days := clampDays(q.Get("days"), 7, 90)
	name := truncateRunes(trimSpace(q.Get("name")), 120)
	since := a.now().Add(-time.Duration(days) * 24 * time.Hour)
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, nil, err
	}
	rows, err := store.ListEventSamples(ctx, a.st.Q(), since, name, tr, limit)
	if err != nil {
		return nil, nil, err
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{r.At, r.Name, derefStr(r.User), rawJSONStr(r.Props)})
	}
	return []string{"时间UTC", "事件名", "用户id前8位", "props"}, out, nil
}

func (a *App) exportAssets(ctx context.Context, q urlValues, limit int) ([]string, [][]string, error) {
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, nil, err
	}
	f := store.AssetFilter{
		Kind:   pickEnum(q.Get("kind"), "source", "candidate", "thumbnail", "export"),
		Status: pickEnum(q.Get("status"), "pending", "ready", "quarantined", "deleted"),
		UserID: truncateRunes(trimSpace(q.Get("userId")), 64),
		Range:  tr,
		Limit:  limit,
	}
	rows, err := store.ListAdminAssets(ctx, a.st.Q(), f)
	if err != nil {
		return nil, nil, err
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.ID, r.Kind, r.Status, r.ContentType, int64PtrStr(r.ByteSize),
			intPtrStr(r.Width), intPtrStr(r.Height), derefStr(r.SHA256),
			r.User, store.CSVMaskEmail(r.Email), derefStr(r.ProjectID),
			derefStr(r.AIGCLabel), r.CreatedAt, derefStr(r.DeletedAt),
		})
	}
	// 「AI标识」列同样进 CSV：合规盘点（「线上还有多少张成品没标识」）是个
	// 离线统计动作，做在表格里，不该逼着人去后台一页一页翻。
	return []string{"资产id", "类型", "状态", "内容类型", "字节数", "宽", "高", "sha256",
		"用户id前8位", "邮箱(已打码)", "项目id", "AI标识", "创建时间UTC", "删除时间UTC"}, out, nil
}

// urlValues 是 url.Values 的最小接口，方便把 handler 拆成可单测的小函数。
type urlValues interface{ Get(string) string }

// clampOptionalHours 读 ?sinceHours=：缺省或非法一律 0（= 不按时间筛）。
// 上限 720 小时（30 天）—— 再往前就该用「统计」页的按天聚合，而不是拉原始行。
func clampOptionalHours(raw string) int {
	raw = trimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	if n > 720 {
		n = 720
	}
	return n
}

// csvBoolStr 把布尔渲染成中文「是/否」。
//
// 🔴 刻意不叫 boolStr：测试包里已经有一个 boolStr（testseed_test.go，回 "true"/"false"）。
// 同名会在 go test 时变成重复声明 —— 而 go build 是绿的，所以这种冲突只在跑测试
// 的那一刻才暴露（admin_ops.go 的 textPtr 注释里记过同一个坑）。
func csvBoolStr(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}
	return itoa(*p)
}

func int64PtrStr(p *int64) string {
	if p == nil {
		return ""
	}
	return itoa64(*p)
}

// rawJSONStr 把一个 jsonb 原文压成 CSV 安全的单行。nil/空 -> 空串。
func rawJSONStr(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	s := string(raw)
	return store.CSVText(&s)
}
