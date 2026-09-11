// 后台「运营」页的写入口：风格完整编辑、商品完整编辑、用户禁用/启用、
// 操作审计、反馈原因码观测。
//
// 🔴 这一组和 admin_config.go 的分工是刻意的：
//
//	admin_config.go 管**注册表热键**（一个键一个值，范围/白名单校验在 cfgstore 里）；
//	本文件管**业务行**（styles / products / users 的列），校验必须在这里做 ——
//	它们没有注册表那张表可以靠。每个写入口都落一条审计。
package httpapi

import (
	"encoding/json"
	"unicode/utf8"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// ---- 审计 ------------------------------------------------------------------

// audit 记一条后台操作审计。
//
// 🔴 审计写失败**不能**让业务动作失败：写审计是在业务写之后单独做的，
// 此时价格已经改了、用户已经封了。让整个请求回 500 会让运营以为「没改成」
// 然后再点一次 —— 真实后果是改了两次、审计还是 0 条。
// 所以这里只记一行 warn 日志，并在响应里带 auditLogged 字段，把「这次没留痕」
// 这件事显式告诉页面，而不是假装一切正常。
func (a *App) audit(c *Ctx, action string, props map[string]any) bool {
	err := store.InsertAudit(c.R.Context(), a.st.Q(), a.newID(), action, props, a.now())
	if err != nil {
		a.lg.Warn("audit: 审计写入失败（业务动作已生效）", map[string]any{
			"action": action, "error": err.Error(),
		})
		return false
	}
	return true
}

func (a *App) hAdminAudit(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	limit := clampLimit(c.URL.Query().Get("limit"), 100, 500)
	rows, err := store.ListAudit(c.R.Context(), a.st.Q(), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"audit": rows,
		// 保留期是热键，所以这个数字要实时回 —— 面板上那句「N 天后自动删除」
		// 必须和真正在生效的值一致。
		"retentionDays": a.rt.EventRetentionDays(),
	}, nil
}

// ---- 反馈原因码观测 --------------------------------------------------------

// knownClientReasonCodes 是**当前 App 版本**里硬编码的四个原因码
// （web/app.js 的差评芯片）。后台把「观测到的码」和这份清单对照，
// 是为了让运营一眼看出哪些码来自已知客户端、哪些是老版本或脏数据留下的。
//
// 🔴 它不是一个「可配字典」。服务端对 reasonCodes 没有枚举校验（开放字符串数组），
// 而 App 也不会去读服务端下发的字典 —— 做成后台可配等于做一个改了不会有任何
// 效果的开关。真正要改原因码得发 App 版本。
var knownClientReasonCodes = []string{"FACE_CHANGED", "WRONG_STYLE", "BAD_DETAILS", "TOO_STRONG"}

func (a *App) hAdminReasonCodes(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListReasonCodes(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{"reasonCodes": rows, "knownClientCodes": knownClientReasonCodes}, nil
}

// ---- 校验小工具 ------------------------------------------------------------

// adminText 读一个后台文本字段，按**字符数**限长，并且**拒绝**超长而不是截断。
//
// 🔴 与 public 侧的 truncateRunes 刻意相反。公开接口截断是为了不把一个长评论
// 变成 500；后台这边悄悄截断意味着运营输入的风格名被砍掉一半而页面显示「已保存」，
// 下一次刷新才看见被砍过的名字。运营的输入必须么全收、要么明确报错。
func adminText(body map[string]any, field string, maxRunes int) (*string, error) {
	raw, ok := body[field]
	if !ok {
		return nil, nil
	}
	if raw == nil {
		return nil, apierr.New(422, apierr.CodeValidation, field+" 不能是 null。")
	}
	s, ok := raw.(string)
	if !ok {
		return nil, apierr.New(422, apierr.CodeValidation, field+" 必须是字符串。")
	}
	s = trimSpace(s)
	if utf8.RuneCountInString(s) > maxRunes {
		return nil, apierr.New(422, apierr.CodeValidation,
			field+" 太长了（最多 "+itoa(maxRunes)+" 个字）。")
	}
	return &s, nil
}

// adminNonEmptyText 是 adminText 再加一条「给了就不能是空白」。
func adminNonEmptyText(body map[string]any, field string, maxRunes int) (*string, error) {
	s, err := adminText(body, field, maxRunes)
	if err != nil || s == nil {
		return s, err
	}
	if *s == "" {
		return nil, apierr.New(422, apierr.CodeValidation, field+" 不能为空。")
	}
	return s, nil
}

// adminNullableText 读一个「可以显式清空」的文本字段。
// 返回 (set, value)：set 为真才写，value 为 nil 表示写 SQL NULL。
func adminNullableText(body map[string]any, field string, maxRunes int) (bool, *string, error) {
	raw, ok := body[field]
	if !ok {
		return false, nil, nil
	}
	if raw == nil {
		return true, nil, nil
	}
	s, ok := raw.(string)
	if !ok {
		return false, nil, apierr.New(422, apierr.CodeValidation, field+" 必须是字符串或 null。")
	}
	s = trimSpace(s)
	if utf8.RuneCountInString(s) > maxRunes {
		return false, nil, apierr.New(422, apierr.CodeValidation,
			field+" 太长了（最多 "+itoa(maxRunes)+" 个字）。")
	}
	// 空串按「清空」处理：前端的文本框清空之后交上来的就是 ""，
	// 把它当成「存一个空字符串」会让 SKU 从 NULL 变成 ''，而那两个值在
	// `WHERE google_product_id IS NULL` 这类判据下表现不同。
	if s == "" {
		return true, nil, nil
	}
	return true, &s, nil
}

// adminRangedInt 读一个带闭区间的整数字段。
func adminRangedInt(body map[string]any, field string, min, max int64) (*int64, error) {
	n, ok, err := optionalInt(body, field)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if n == nil {
		return nil, apierr.New(422, apierr.CodeValidation, field+" 不能是 null。")
	}
	if *n < min || *n > max {
		return nil, apierr.New(422, apierr.CodeValidation,
			field+" 必须在 "+itoa64(min)+".."+itoa64(max)+" 之间（收到 "+itoa64(*n)+"）。")
	}
	return n, nil
}

func itoa(n int) string { return itoa64(int64(n)) }

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---- 风格完整编辑 ----------------------------------------------------------

// MaxStyleTags 是 suitability_tags 的条数上限（与 StyleCard 出参的实际用量一致）。
const MaxStyleTags = 8

// AdminStyleView 是 GET /v1/admin/styles-admin 的一行：库里的字段 + 两条
// 「后台必须说出来」的事实（封面文件在不在、这个风格会不会真的出现在 App 里）。
type AdminStyleView struct {
	store.AdminStyleFull
	// CoverURL 非空表示 web/covers/<internal_key>.jpg 这个文件确实存在。
	// 🔴 封面不是数据库列，是磁盘上的文件，所以后台只能展示+预览，不能编辑 ——
	//    给一个「封面」输入框等于给一个存不进任何地方的框。
	CoverURL *string `json:"coverUrl"`
	// VisibleInApp 是「这个风格现在会不会出现在 /v1/styles 与 /v1/discover 里」。
	// 三个条件同时成立才为真：自身已发布、有已发布版本、挂在某个展览上。
	// 少任何一个，App 里都看不到它 —— 而 status 那一列只反映第一个条件。
	VisibleInApp bool `json:"visibleInApp"`
	// HiddenReason 在 VisibleInApp 为假时说明缺了哪一条。
	HiddenReason *string `json:"hiddenReason"`
}

func (a *App) styleView(r store.AdminStyleFull) AdminStyleView {
	v := AdminStyleView{AdminStyleFull: r}
	if u := a.coverURLFor(r.InternalKey); u != "" {
		v.CoverURL = &u
	}
	switch {
	case r.Status != "published":
		v.HiddenReason = textPtr("风格自身已下架（status=" + r.Status + "）")
	case r.PublishedVersions == 0:
		v.HiddenReason = textPtr("没有任何已发布的 style_versions 行 —— 目录查询 JOIN 的是已发布版本，" +
			"所以即使状态是 published，App 里也看不到它")
	case r.ExhibitionID == nil:
		v.HiddenReason = textPtr("没有挂在任何展览上（exhibition_styles 里没有这一行），" +
			"而目录是按展览分架渲染的")
	default:
		v.VisibleInApp = true
	}
	return v
}

func (a *App) hAdminStylesFull(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListAdminStylesFull(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	out := make([]AdminStyleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.styleView(r))
	}
	return map[string]any{"styles": out}, nil
}

// hAdminPatchStyle 改一个风格的可运营字段。
func (a *App) hAdminPatchStyle(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	id := c.Params[0]

	var u store.StyleUpdate
	var err error
	if u.PublicName, err = adminNonEmptyText(c.Body, "name", 80); err != nil {
		return nil, err
	}
	// shortCaption 允许为空串：目录卡片上它只是一行副标题，留空是合法的排版选择。
	if u.ShortCaption, err = adminText(c.Body, "shortCaption", 140); err != nil {
		return nil, err
	}
	if u.Theme, err = adminNonEmptyText(c.Body, "theme", 40); err != nil {
		return nil, err
	}
	if u.Premium, err = optionalBool(c.Body, "premium"); err != nil {
		return nil, apierr.New(422, apierr.CodeValidation, "premium 必须是布尔值。")
	}
	if raw, ok := c.Body["status"]; ok {
		s, _ := raw.(string)
		if s != "published" && s != "disabled" {
			return nil, apierr.New(422, apierr.CodeValidation, "status 只能是 published 或 disabled。")
		}
		u.Status = &s
	}
	if pos, err := adminRangedInt(c.Body, "position", 0, 9999); err != nil {
		return nil, err
	} else if pos != nil {
		n := int(*pos)
		u.Position = &n
	}
	if raw, ok := c.Body["tags"]; ok {
		tags, err := parseStyleTags(raw)
		if err != nil {
			return nil, err
		}
		u.Tags = tags
	}

	found, err := store.UpdateStyleFields(ctx, a.st, id, u)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, notFound("Unknown style.")
	}
	logged := a.audit(c, "style.update", map[string]any{
		"styleId": id, "fields": styleChangedFields(u),
	})
	row, err := store.GetAdminStyle(ctx, a.st.Q(), id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, notFound("Unknown style.")
	}
	return map[string]any{"ok": true, "auditLogged": logged, "style": a.styleView(*row)}, nil
}

// parseStyleTags 把入参的 tags 数组编码成 suitability_tags 的 JSON 字节。
func parseStyleTags(raw any) ([]byte, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, apierr.New(422, apierr.CodeValidation, "tags 必须是字符串数组。")
	}
	if len(arr) > MaxStyleTags {
		return nil, apierr.New(422, apierr.CodeValidation,
			"tags 最多 "+itoa(MaxStyleTags)+" 个。")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, apierr.New(422, apierr.CodeValidation, "tags 里每一项都必须是字符串。")
		}
		s = trimSpace(s)
		if s == "" {
			continue
		}
		if utf8.RuneCountInString(s) > 40 {
			return nil, apierr.New(422, apierr.CodeValidation, "tags 里每一项最多 40 个字。")
		}
		// 去重：重复的标签在 App 的「Best with —」那一行会原样重复渲染两遍。
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return json.Marshal(out)
}

// styleChangedFields 列出这次补丁真正动了哪些字段（进审计）。
// 🔴 只记字段名与新值，不记旧值全量 —— 旧值要查就去 events 里按时间往前翻，
// 而把两份全量都写进每条审计会让 events 表膨胀得毫无必要。
func styleChangedFields(u store.StyleUpdate) map[string]any {
	m := map[string]any{}
	if u.PublicName != nil {
		m["name"] = *u.PublicName
	}
	if u.ShortCaption != nil {
		m["shortCaption"] = *u.ShortCaption
	}
	if u.Theme != nil {
		m["theme"] = *u.Theme
	}
	if u.Premium != nil {
		m["premium"] = *u.Premium
	}
	if u.Status != nil {
		m["status"] = *u.Status
	}
	if u.Position != nil {
		m["position"] = *u.Position
	}
	if u.Tags != nil {
		m["tags"] = string(u.Tags)
	}
	return m
}

// ---- 用户禁用 / 启用 -------------------------------------------------------

// UserStatusActive / UserStatusSuspended 是后台能设的两个 users.status 值。
//
// 🔴 'merged' 是第三个会出现在库里的值，但它由游客合并逻辑写入并且**总是**
// 伴随 deleted_at 非空（见 MergeGuestInto），所以它落在 GetUser 的
// `deleted_at IS NULL` 之外，和封禁判据不冲突。后台不给设它。
const (
	UserStatusActive    = "active"
	UserStatusSuspended = "suspended"
)

// MaxSuspendReason 是封禁原因的长度上限（按字符）。
const MaxSuspendReason = 300

// hAdminUserStatus 禁用 / 启用一个账号。
//
// 🔴 封禁必须填原因。没有原因的封禁在三个月后等于「不知道为什么这个人进不来」，
// 而客服收到的投诉里只有一句「我登不上」。原因进审计，客服能自己查。
func (a *App) hAdminUserStatus(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	userID := c.Params[0]

	status := trimSpace(str(c.Body["status"]))
	if status != UserStatusActive && status != UserStatusSuspended {
		return nil, apierr.New(422, apierr.CodeValidation,
			"status 只能是 "+UserStatusActive+" 或 "+UserStatusSuspended+"。")
	}
	reasonPtr, err := adminText(c.Body, "reason", MaxSuspendReason)
	if err != nil {
		return nil, err
	}
	reason := ""
	if reasonPtr != nil {
		reason = *reasonPtr
	}
	if status == UserStatusSuspended && reason == "" {
		return nil, apierr.New(422, apierr.CodeValidation, "禁用账号必须填写原因（会进操作审计）。")
	}

	// 先读一次：要在审计里记下「从什么状态改到什么状态」，也要把不存在 / 已软删
	// 的 id 拦在写之前（否则 RowsAffected=0 和「状态本来就一样」分不开）。
	before, err := store.GetUser(ctx, a.st.Q(), userID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown user.")
		}
		return nil, err
	}
	ok, err := store.SetUserStatus(ctx, a.st.Q(), userID, status, a.now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("Unknown user.")
	}
	logged := a.audit(c, "user.status", map[string]any{
		"userId": userID, "from": before.Status, "to": status, "reason": reason,
	})
	return map[string]any{
		"ok": true, "auditLogged": logged, "userId": userID,
		"from": before.Status, "to": status,
		// 封禁是通过鉴权判据生效的，会话行**没有删**：改回 active 之后
		// 用户手上原来的 token 立刻又能用。页面要把这件事说清楚。
		"sessionsRevoked": false,
	}, nil
}

// ---- 运营页的只读事实 ------------------------------------------------------

// AdminProductFacts 是后台商品表旁边要显示的产品事实。
type AdminProductFacts struct {
	// UnitsPerJob 是每个生成任务扣的张数，恒为 1（public_jobs.go 里的 units = 1）。
	UnitsPerJob int `json:"unitsPerJob"`
	// CnyNullHidesProduct 说明「人民币价留空」的真实后果。
	CnyNullHidesProduct bool `json:"cnyNullHidesProduct"`
	// StoreBillingEnabled 是商店内购通道的实际状态（App 据此决定是否显示内购区）。
	GooglePlayConfigured bool `json:"googlePlayConfigured"`
	AppleConfigured      bool `json:"appleConfigured"`
	MockPurchases        bool `json:"mockPurchases"`
	// PackExpiryDays 是加购额度有效期（0 = 永不过期），热键。
	PackExpiryDays int `json:"packExpiryDays"`
}

func (a *App) productFacts() AdminProductFacts {
	return AdminProductFacts{
		UnitsPerJob:          1,
		CnyNullHidesProduct:  true,
		GooglePlayConfigured: a.cfg.GoogleServiceAccountJSON != "",
		// Apple 侧的 App Store Server API 校验尚未接（public_purchases.go 直接 501，
		// /v1/auth/config 里 billing.apple 恒为 false）。
		AppleConfigured: false,
		MockPurchases:   a.cfg.AllowMockPurchases,
		PackExpiryDays:  a.rt.ClampedInt("pack_credit_expiry_days"),
	}
}

// AdminUserFacts 是后台用户表旁边要显示的事实。
type AdminUserFacts struct {
	// StatusCounts 是各 status 的未软删用户数。
	StatusCounts map[string]int `json:"statusCounts"`
	// SuspendedBlocks 说明禁用到底拦住了什么。
	SuspendedBlocks string `json:"suspendedBlocks"`
	SessionTTLDays  int    `json:"sessionTtlDays"`
}

func (a *App) hAdminUserFacts(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	counts, err := store.CountUsersByStatus(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	return AdminUserFacts{
		StatusCounts: counts,
		SuspendedBlocks: "禁用后该账号的所有令牌立刻按「未登录」处理（任何带 Bearer 的接口回 401 AUTH_REQUIRED）。" +
			"会话行没有删，改回启用后原令牌立刻恢复可用。已生成的作品与额度台账不动。",
		SessionTTLDays: a.rt.SessionTTLDays(),
	}, nil
}

// textPtr 是 &s 的行内写法。
//
// 🔴 刻意不叫 strp：测试包里已经有一个 strp（testseed_test.go），
// 同名会在 go test 时变成重复声明 —— 而 `go build` 是绿的，
// 所以这种冲突只在跑测试的那一刻才暴露。
func textPtr(s string) *string { return &s }
