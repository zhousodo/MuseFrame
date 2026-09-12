// 后台「全链路可见性」补齐（2026-09-12 审计）。
//
// 审计的判据是一句话：**App 上报到后端的每一类数据，后台都要能看。**
// 对照 web/app.js + web/native.js 的全部出站调用，这一批补的是四个黑洞
// （埋点 / 资产 / 反馈正文 / 发信记录）、一个纵向视图（按用户把六张表拉到一起）、
// 三个写入口（反馈标已处理 / 任务重试 / 购买重验）、一个自观测视图（接口健康），
// 以及六类数据的 CSV 导出（2026-09-12 第七轮起**完整显示**，不再打码）。
//
// 🔴 每个视图的「一句说明」不在前端硬编码，而是由后端随数据一起回（note 字段）。
// 理由：说明里写的是**口径**（窗口多长、按什么时区切、哪些行被排除、
// 进程内还是全站）。口径是后端决定的，让前端抄一遍等于留一份一定会过期的副本。
package httpapi

import (
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// clampDays 把 ?days= 夹到 [1, max]。
func clampDays(raw string, def, max int) int {
	return clampLimit(raw, def, max)
}

// pickEnum 读一个枚举型查询参数：空串或不在白名单里一律当「不筛」。
//
// 🔴 不在白名单里必须当「不筛」而**不是**报 422：这些值进 SQL 是走 $N 占位符的，
// 不存在注入；而一个拼错的 ?status=succeded 如果报 422，运营看到的是一个
// 红色报错页面而不是「这个状态没有任务」—— 前者会被当成后台坏了。
// 真正重要的是：绝不把未知值拼进 SQL 字符串。
func pickEnum(raw string, allowed ...string) string {
	v := strings.ToLower(trimSpace(raw))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return ""
}

// ---- 埋点 events -----------------------------------------------------------

const eventsNote = "App 通过 POST /v1/events 上报的全部埋点。窗口内按事件名 / 天 / App 版本聚合，" +
	"外加最近的原始样本。天是按 UTC 切的（与「统计」页同一口径），" +
	"后台管理操作的审计行（admin.*）与发信记录（email.send）已排除。"

func (a *App) hAdminEvents(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	days := clampDays(c.URL.Query().Get("days"), 7, 90)
	limit := clampLimit(c.URL.Query().Get("limit"), 100, 500)
	name := truncateRunes(trimSpace(c.URL.Query().Get("name")), 120)
	since := a.now().Add(-time.Duration(days) * 24 * time.Hour)

	names, err := store.ListEventNames(ctx, a.st.Q(), since)
	if err != nil {
		return nil, err
	}
	byDay, err := store.ListEventsByDay(ctx, a.st.Q(), since)
	if err != nil {
		return nil, err
	}
	versions, err := store.ListEventVersions(ctx, a.st.Q(), since)
	if err != nil {
		return nil, err
	}
	tr, err := parseTimeRange(c.URL.Query())
	if err != nil {
		return nil, err
	}
	samples, err := store.ListEventSamples(ctx, a.st.Q(), since, name, tr, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"note": eventsNote + timeRangeNote(tr), "days": days, "names": names, "byDay": byDay,
		"versions": versions, "samples": samples, "sampleFilter": name,
		// 保留期是热键：埋点页看不到比它更早的数据，这个数字必须和生效值一致。
		"retentionDays": a.rt.EventRetentionDays(),
		// App 当前打的 16 个事件名，用来区分「已知埋点」和老版本/脏数据留下的名字。
		"knownClientEvents": knownClientEvents,
	}, nil
}

// knownClientEvents 是**当前 App 版本**（web/app.js 的 track() 调用点）会打的事件名。
//
// 🔴 它是一份对照清单，不是白名单 —— 服务端对 /v1/events 的 name 没有枚举校验
// （旧版 App 还在跑，拒收未知事件名等于把老版本的埋点全丢掉）。
// 后台把「观测到的名字」和这份清单对照，运营一眼能看出哪些名字来自已知客户端。
var knownClientEvents = []string{
	"analysis_completed", "auth_opened", "discover_viewed", "exhibition_opened",
	"generation_failed", "generation_submitted", "generation_succeeded",
	"onboarding_completed", "paywall_viewed", "photo_import_completed",
	"photo_import_started", "purchase_completed", "result_shared",
	"signin_completed", "style_opened",
}

// ---- 资产 assets -----------------------------------------------------------

const assetsNote = "App 上传的每一张源图（PUT /v1/assets/{id}/upload）与后端生成的每一张成品 / 导出图。" +
	"缩略图点开看大图走的是短时图片令牌，管理员令牌不进 URL。" +
	"sha256 由资产迁移工具回填，历史行可能为空。storage_key 刻意不回（磁盘布局不外传）。" +
	"「AI 标识」列是《人工智能生成合成内容标识办法》的标识状态：" +
	"「水印+元数据」= 显式角标画进了像素且文件元数据里有 GB 45438-2025 的标识字段；" +
	"「仅元数据」= 运营把 aigc_label_enabled 关掉了，只剩隐式标识；" +
	"「未标识」= 源图（本来就不需要标识）或本版之前产出的历史成品（刻意不回溯）。"

func (a *App) hAdminAssets(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	q := c.URL.Query()
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, err
	}
	f := store.AssetFilter{
		Kind:   pickEnum(q.Get("kind"), "source", "candidate", "thumbnail", "export"),
		Status: pickEnum(q.Get("status"), "pending", "ready", "quarantined", "deleted"),
		UserID: truncateRunes(trimSpace(q.Get("userId")), 64),
		Range:  tr,
		Limit:  clampLimit(q.Get("limit"), 100, 500),
	}
	rows, err := store.ListAdminAssets(ctx, a.st.Q(), f)
	if err != nil {
		return nil, err
	}
	totals, err := store.SumAssetsByKind(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": assetsNote + timeRangeNote(tr), "assets": rows, "totals": totals}, nil
}

// ---- 用户纵向详情 ----------------------------------------------------------

const userDetailNote = "一个用户的全部足迹：登录身份（完整邮箱）、额度账本、免费额度发放（含明文 IP）、" +
	"项目、任务、资产、购买、会话、反馈。userId 填完整 id 或任意长度的前缀都行。" +
	"会话**一个字节的令牌都不回**（设备、最近活跃、到期足够对上一台设备）。"

func (a *App) hAdminUserDetail(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	raw := truncateRunes(trimSpace(c.URL.Query().Get("userId")), 64)
	if raw == "" {
		return nil, apierr.New(422, apierr.CodeValidation, "userId 必填（可以只填列表里的 8 位前缀）。")
	}
	userID, matches, err := store.GetUserByPrefix(ctx, a.st.Q(), raw)
	if err != nil {
		return nil, err
	}
	if matches == 0 {
		return nil, notFound("Unknown user.")
	}
	// 前缀撞了就必须报错而不是取第一个：后台会把这个详情页当成「发额度 / 封号」
	// 之前的确认页，认错人的代价是动到另一个账号。
	if matches > 1 {
		return nil, apierr.New(409, apierr.CodeAmbiguous,
			"这个前缀匹配到了多个用户（"+itoa(matches)+"+），请填更长的 id。")
	}

	// GetUserAny 而不是 GetUser：后台必须看得见被软删和被封的账号 ——
	// 「用户说他登不上去」的第一步就是确认他是不是 deleted_at 非空。
	user, err := store.GetUserAny(ctx, a.st.Q(), userID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown user.")
		}
		return nil, err
	}
	now := a.now()
	ledger, err := store.ListUserLedger(ctx, a.st.Q(), userID, a.now(), 200)
	if err != nil {
		return nil, err
	}
	projects, err := store.ListUserProjects(ctx, a.st.Q(), userID, 100)
	if err != nil {
		return nil, err
	}
	jobs, err := store.ListAdminJobsFiltered(ctx, a.st.Q(), store.JobFilter{UserID: userID, Limit: 100}, now)
	if err != nil {
		return nil, err
	}
	assets, err := store.ListAdminAssets(ctx, a.st.Q(), store.AssetFilter{UserID: userID, Limit: 200})
	if err != nil {
		return nil, err
	}
	purchases, err := store.ListUserPurchases(ctx, a.st.Q(), userID, 100)
	if err != nil {
		return nil, err
	}
	sessions, err := store.ListUserSessions(ctx, a.st.Q(), userID, now, 50)
	if err != nil {
		return nil, err
	}
	bal, err := store.AvailableUnits(ctx, a.st.Q(), userID, now)
	if err != nil {
		return nil, err
	}
	// 🔴 登录身份（= 完整邮箱）。此前详情页一个邮箱都不显示，于是客服确认完
	// 「就是这个人」之后还得回列表页去抄邮箱才能回信 —— 而详情页本来就是
	// 「发额度 / 封号」之前的那张确认页，联系方式必须在这一页上。
	identities, err := store.ListUserIdentities(ctx, a.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	// 免费额度发放台账：带**明文 IP**（见 migrations/005_free_grant_ip.sql）。
	// 「这一批账号是不是同一个人在刷」只有这一列能答。
	freeGrants, err := store.ListUserFreeGrants(ctx, a.st.Q(), userID, 50)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"note": userDetailNote,
		"user": map[string]any{
			// 完整 id 与完整邮箱：发额度 / 封号的接口要 id，回信要邮箱，
			// 两样都只能从这一页复制。
			"id": user.ID, "displayName": user.DisplayName, "status": user.Status,
			"isGuest": user.IsGuest, "locale": user.Locale, "timezone": user.Timezone,
			"createdAt": store.ISO(user.CreatedAt),
			"deletedAt": store.ISOPtr(user.DeletedAt),
			"email":     store.PrimaryEmail(identities),
		},
		"availableUnits": bal,
		"identities":     identities,
		"freeGrants":     freeGrants,
		"ledger":         ledger, "projects": projects, "jobs": jobs,
		"assets": assets, "purchases": purchases, "sessions": sessions,
	}, nil
}

// ---- 任务详情（全部候选） --------------------------------------------------

const jobDetailNote = "一条任务的全部事实：参数、上游返回、以及**全部候选**（按产出时间 created_at 升序）。" +
	"任务列表那一行只显示第一张候选，所以「后面几张是不是也这样」此前只能去数据库浏览器翻 " +
	"generation_candidates。质检未通过的候选同样列出来 —— 它们是「为什么用户只拿到 1 张」的答案。"

func (a *App) hAdminJobDetail(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	jobID := truncateRunes(trimSpace(c.URL.Query().Get("jobId")), 64)
	if jobID == "" {
		return nil, apierr.New(422, apierr.CodeValidation, "jobId 必填。")
	}
	// 复用列表查询（同一个 store 函数）：详情页的每个字段都和列表里那一行同源。
	jobs, err := store.ListAdminJobsFiltered(ctx, a.st.Q(), store.JobFilter{JobID: jobID, Limit: 1}, a.now())
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, notFound("Unknown job.")
	}
	candidates, err := store.ListJobCandidates(ctx, a.st.Q(), jobID)
	if err != nil {
		return nil, err
	}
	analyses, err := store.ListAdminPhotoAnalyses(ctx, a.st.Q(),
		store.PhotoAnalysisFilter{AssetID: jobs[0].SourceAssetID, Limit: 1})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"note": jobDetailNote, "job": jobs[0], "candidates": candidates}
	// 源图的画面分析就挂在这里：失败任务里「上游说图太糊」的旁证。
	if len(analyses) > 0 {
		out["sourceAnalysis"] = analyses[0]
	}
	return out, nil
}

// ---- 画面分析 photo_analyses -----------------------------------------------

const photoAnalysesNote = "App 上传源图后服务端做的画面分析（photo_analyses 表）：主体类型、人数、" +
	"清晰度、曝光，以及给用户的提示与建议原文。此前后台对这张表只有数据库浏览器一条读法，" +
	"看不到归属用户。status=failed 的行意味着分析本身挂了（不影响生成，但会让 App 少给一条建议）。"

func (a *App) hAdminPhotoAnalyses(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	q := c.URL.Query()
	tr, err := parseTimeRange(q)
	if err != nil {
		return nil, err
	}
	f := store.PhotoAnalysisFilter{
		Status:  pickEnum(q.Get("status"), "pending", "ready", "failed"),
		UserID:  truncateRunes(trimSpace(q.Get("userId")), 64),
		AssetID: truncateRunes(trimSpace(q.Get("assetId")), 64),
		Range:   tr,
		Limit:   clampLimit(q.Get("limit"), 100, 500),
	}
	rows, err := store.ListAdminPhotoAnalyses(ctx, a.st.Q(), f)
	if err != nil {
		return nil, err
	}
	counts, err := store.CountPhotoAnalysesByStatus(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"note": photoAnalysesNote + timeRangeNote(tr), "analyses": rows, "statusCounts": counts,
		"filter": map[string]any{"status": f.Status, "userId": f.UserID, "assetId": f.AssetID,
			"from": q.Get("from"), "to": q.Get("to")},
	}, nil
}

// ---- 风格版本与 spec -------------------------------------------------------

const styleVersionsNote = "一个风格的全部版本与**完整 spec**（style_versions.spec，不可变的 StyleSpec）。" +
	"spec 里是提示词模板、控件取值域、负面词、后处理参数 —— 它是「这个风格到底怎么生成的」" +
	"唯一的答案，也是「为什么这个风格出图变了」的对照物。只读：spec 改一个字就该是一个新版本。" +
	"「任务数」是用这个版本跑过的任务数，0 表示这个版本从没服役过。"

func (a *App) hAdminStyleVersions(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	styleID := truncateRunes(trimSpace(c.URL.Query().Get("styleId")), 64)
	limit := clampLimit(c.URL.Query().Get("limit"), 50, 200)
	rows, err := store.ListStyleVersions(c.R.Context(), a.st.Q(), styleID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": styleVersionsNote, "versions": rows, "styleId": styleID}, nil
}

// ---- 邮件发送记录 ----------------------------------------------------------

const emailLogNote = "验证码与后台测试邮件的发送记录：**完整收件地址 + 主题**（2026-09-12 起不再打码）。" +
	"验证码信记的主题是一个不含验证码的常量 —— 真实主题以明文验证码开头，它一个字节都不入库。" +
	"记录落在 events 表里（复用审计那套），所以会随 event_retention_days 到期被清；" +
	"本版之前落的行没有主题，显示为「未记录」。" +
	"下半部分是验证码签发台账（email_codes），邮箱完整、只回元数据 —— 验证码哈希一个字节都不出库。"

func (a *App) hAdminEmailLog(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	limit := clampLimit(c.URL.Query().Get("limit"), 100, 500)
	sends, err := store.ListEmailSends(ctx, a.st.Q(), limit)
	if err != nil {
		return nil, err
	}
	codes, err := store.ListEmailCodes(ctx, a.st.Q(), a.now(), limit)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"note": emailLogNote, "sends": sends, "codes": codes,
		"configured":    a.mailer.Configured(),
		"retentionDays": a.rt.EventRetentionDays(),
	}
	// 进程内的「最近一次」仍然回：它比 DB 记录早一步存在（落库失败时也有），
	// 是判断「发信刚刚是不是炸了」最快的一格。复用 admin_summary.go 已有的
	// 可选口 MailStatusReporter —— 再声明一个结构镜像等于留两份会漂移的定义。
	if rep, ok := a.mailer.(MailStatusReporter); ok {
		if st, has := rep.LastSend(); has {
			out["lastSend"] = st
		}
	}
	return out, nil
}

// ---- 接口健康 --------------------------------------------------------------

const apiHealthNote = "过去 N 小时内每条接口的请求数 / 4xx / 5xx / 平均 / P95 / 最慢。" +
	"🔴 这是**本进程内**的计数：重启清零、多副本各算各的，不是全站统计。" +
	"P95 从固定延迟直方图算，精度到桶边界（5/10/25/50/100/250/500/1000/2500/5000/10000ms）；" +
	"标了「>」的表示落在最后一个开口桶里。未匹配到路由表的请求（扫描器、静态文件）不计。"

func (a *App) hAdminAPIHealth(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	hours := clampLimit(c.URL.Query().Get("hours"), 24, 24)
	rows := a.mx.Snapshot(hours)
	var total, c4, c5 int64
	for _, r := range rows {
		total += r.Total
		c4 += r.Count4xx
		c5 += r.Count5xx
	}
	return map[string]any{
		"note": apiHealthNote, "hours": hours, "routes": rows,
		"total": total, "count4xx": c4, "count5xx": c5,
		"startedAt":     store.ISO(a.startedAt),
		"uptimeSeconds": int(a.now().Sub(a.startedAt).Seconds()),
	}, nil
}
