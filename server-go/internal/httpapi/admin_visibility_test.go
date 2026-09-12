package httpapi

import (
	"encoding/csv"
	"strconv"
	"strings"
	"testing"

	"museframe-api/internal/aigc"
	"museframe-api/internal/store"
)

// ---- 埋点视图 --------------------------------------------------------------

// 埋点视图必须排除审计行。不排掉的话运营自己的每一次点击都会被算成用户行为，
// 而这张表正是用来判断 App 功能有没有人用的。
func TestAdminEventsExcludesAuditRows(t *testing.T) {
	e := newTestEnv(t)
	uid, tok := e.signUp("events@example.com")

	// 用户埋点两条。
	if r := e.do("POST", "/v1/events", map[string]any{"events": []any{
		map[string]any{"name": "discover_viewed", "props": map[string]any{"layout": "grid"}},
		map[string]any{"name": "style_opened"},
	}}, bearer(tok)); r.Code != 200 {
		t.Fatalf("埋点上报应当 200，实际 %d: %s", r.Code, r.Body)
	}
	// 一条审计（改配置会落 admin.config_set 之类）。
	if r := e.do("PUT", "/v1/admin/config", map[string]any{"key": "free_units", "value": 3}, e.admin()); r.Code != 200 {
		t.Fatalf("改配置应当 200，实际 %d: %s", r.Code, r.Body)
	}

	var out struct {
		Note              string `json:"note"`
		Days              int    `json:"days"`
		KnownClientEvents []string
		Names             []struct {
			Name  string `json:"name"`
			Total int    `json:"total"`
			Users int    `json:"users"`
		} `json:"names"`
		ByDay []struct {
			Day   string `json:"day"`
			Name  string `json:"name"`
			Total int    `json:"total"`
		} `json:"byDay"`
		Versions []struct {
			Version string `json:"version"`
			Total   int    `json:"total"`
		} `json:"versions"`
		Samples []struct {
			Name  string `json:"name"`
			User  *string
			Props map[string]any `json:"props"`
		} `json:"samples"`
	}
	r := e.do("GET", "/v1/admin/events?days=7", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("埋点视图应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)

	if out.Note == "" {
		t.Errorf("每个视图顶部都要有一句说明，note 不能空")
	}
	for _, n := range out.Names {
		// 🔴 变异验证：删掉 ListEventNames 里的 `name NOT LIKE AuditPrefix%`，
		// 这里会冒出一个 admin.* 名字，这行红。
		if strings.HasPrefix(n.Name, store.AuditPrefix) {
			t.Errorf("埋点聚合里不该出现审计行 %q", n.Name)
		}
	}
	if len(out.Names) != 2 {
		t.Fatalf("应当聚合出 2 个事件名（discover_viewed / style_opened），实际 %d: %+v", len(out.Names), out.Names)
	}
	for _, s := range out.Samples {
		if strings.HasPrefix(s.Name, store.AuditPrefix) {
			t.Errorf("原始样本里不该出现审计行 %q", s.Name)
		}
	}
	if len(out.Samples) != 2 {
		t.Errorf("原始样本应当 2 条，实际 %d", len(out.Samples))
	}
	// 🔴 用户 id 必须是**完整** id（2026-09-12 第七轮：后台不再截断）。
	// 变异验证：把 ListEventSamples 的 user_id 换回 substr(user_id,1,8)，这行红。
	for _, s := range out.Samples {
		if s.User != nil && *s.User != uid {
			t.Errorf("样本里的用户 id 应当是完整 id %q，实际 %q", uid, *s.User)
		}
	}
	// 当前 App 不报版本号，所以唯一那一行是「(未上报)」。
	if len(out.Versions) != 1 || out.Versions[0].Version != "(未上报)" {
		t.Errorf("App 当前不上报版本号，应当只有一行 (未上报)，实际 %+v", out.Versions)
	}
	if len(out.ByDay) == 0 {
		t.Errorf("按天聚合不该为空")
	}
}

func TestAdminEventsNameFilterNarrowsSamplesOnly(t *testing.T) {
	e := newTestEnv(t)
	_, tok := e.signUp("evfilter@example.com")
	e.do("POST", "/v1/events", map[string]any{"events": []any{
		map[string]any{"name": "discover_viewed"},
		map[string]any{"name": "style_opened"},
		map[string]any{"name": "style_opened"},
	}}, bearer(tok))

	var out struct {
		Names   []struct{ Name string } `json:"names"`
		Samples []struct{ Name string } `json:"samples"`
	}
	r := e.do("GET", "/v1/admin/events?name=style_opened", nil, e.admin())
	r.JSON(t, &out)
	// 🔴 筛选只作用于样本，聚合仍然是全量 —— 否则「这个事件占多少比例」就没法回答了。
	if len(out.Names) != 2 {
		t.Errorf("聚合应当仍是全量 2 个事件名，实际 %+v", out.Names)
	}
	if len(out.Samples) != 2 {
		t.Fatalf("样本应当只剩 style_opened 的 2 条，实际 %d", len(out.Samples))
	}
	for _, s := range out.Samples {
		if s.Name != "style_opened" {
			t.Errorf("样本被筛后不该出现 %q", s.Name)
		}
	}
}

// ---- 资产视图 --------------------------------------------------------------

func TestAdminAssetsListsAndFilters(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("assets@example.com", 2)
	candAsset := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), candAsset, uid, pid, candAsset+".jpg", 1000, 800, 1000, aigc.MarkVisibleMeta, e.now); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Note   string `json:"note"`
		Assets []struct {
			ID       string  `json:"id"`
			Kind     string  `json:"kind"`
			User     string  `json:"user"`
			ByteSize *int64  `json:"byteSize"`
			SHA256   *string `json:"sha256"`
		} `json:"assets"`
		Totals []struct {
			Kind  string `json:"kind"`
			Count int    `json:"count"`
			Bytes int64  `json:"bytes"`
		} `json:"totals"`
	}
	r := e.do("GET", "/v1/admin/assets", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("资产视图应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Errorf("视图说明不能空")
	}
	if len(out.Assets) != 2 {
		t.Fatalf("应当有 2 条资产（source + candidate），实际 %d", len(out.Assets))
	}
	// 归属必须是**完整** id + **完整**邮箱：资产页是「这张图是谁的」的唯一读法。
	for _, a := range out.Assets {
		if a.User != uid {
			t.Errorf("资产行的用户 id 应当是完整 id %q，实际 %q", uid, a.User)
		}
	}
	// 🔴 storage_key 绝不能出现在响应里（磁盘布局不外传）。
	if strings.Contains(string(r.Body), ".jpg") {
		t.Errorf("响应里出现了存储键（.jpg），storage_key 不该外传：%s", r.Body)
	}

	// kind 筛选。
	var only struct {
		Assets []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"assets"`
	}
	e.do("GET", "/v1/admin/assets?kind=source", nil, e.admin()).JSON(t, &only)
	if len(only.Assets) != 1 || only.Assets[0].ID != aid {
		t.Errorf("kind=source 应当只剩那一张源图，实际 %+v", only.Assets)
	}

	// 非法 kind 当「不筛」而不是 422 —— 一个拼错的参数不该让页面变成红色报错。
	var all struct {
		Assets []struct{ ID string } `json:"assets"`
	}
	rr := e.do("GET", "/v1/admin/assets?kind=bogus", nil, e.admin())
	if rr.Code != 200 {
		t.Fatalf("未知 kind 应当 200（不筛），实际 %d", rr.Code)
	}
	rr.JSON(t, &all)
	if len(all.Assets) != 2 {
		t.Errorf("未知 kind 应当不筛，实际 %d 条", len(all.Assets))
	}
}

// ---- 用户纵向详情 ----------------------------------------------------------

func TestAdminUserDetailAggregatesEverySurface(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, tok, pid, aid := e.prepareJobInputs("detail@example.com", 3)
	candAsset := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), candAsset, uid, pid, candAsset+".jpg", 1000, 800, 1000, aigc.MarkVisibleMeta, e.now); err != nil {
		t.Fatal(err)
	}
	jobID := "job-detail"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "failed", Stage: "failed", Controls: []byte(`{"strength":"balanced"}`),
		Output: []byte(`{"aspectRatio":"4:5"}`), ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	candID := "cand-detail"
	if err := store.InsertCandidate(ctx, e.st.Q(), candID, jobID, 0, candAsset, e.now); err != nil {
		t.Fatal(err)
	}
	// 一条带正文的反馈。
	if r := e.do("POST", "/v1/candidates/"+candID+"/feedback",
		map[string]any{"rating": "negative", "reasonCodes": []any{"FACE_CHANGED"}, "comment": "脸变了，不像我"},
		bearer(tok)); r.Code != 200 {
		t.Fatalf("提交反馈应当 200，实际 %d: %s", r.Code, r.Body)
	}

	var out struct {
		Note string `json:"note"`
		User struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"user"`
		AvailableUnits int `json:"availableUnits"`
		Ledger         []struct {
			EntryType string `json:"entryType"`
			Units     int    `json:"units"`
			Source    string `json:"source"`
		} `json:"ledger"`
		Projects []struct{ ID string } `json:"projects"`
		Jobs     []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"jobs"`
		Assets   []struct{ ID string } `json:"assets"`
		Sessions []struct {
			TokenTail string `json:"tokenTail"`
		} `json:"sessions"`
		Purchases []struct{ ID string } `json:"purchases"`
	}
	// 只给 8 位前缀 —— 这是运营从列表里唯一能复制到的东西。
	r := e.do("GET", "/v1/admin/user-detail?userId="+uid[:8], nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("用户详情应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Errorf("视图说明不能空")
	}
	// 详情页回完整 id（发额度 / 封号接口要它）。
	if out.User.ID != uid {
		t.Errorf("详情页应当回完整 id %q，实际 %q", uid, out.User.ID)
	}
	if len(out.Ledger) == 0 {
		t.Errorf("额度账本不该为空（注册送了 3 张 + grant 了 3 张）")
	}
	for _, l := range out.Ledger {
		if l.Source == "" {
			t.Errorf("账本每一行都要带来源类型（free_grant/purchase/manual），实际空")
		}
	}
	if len(out.Projects) != 1 {
		t.Errorf("应当有 1 个项目，实际 %d", len(out.Projects))
	}
	if len(out.Jobs) != 1 || out.Jobs[0].Status != "failed" {
		t.Errorf("应当有 1 条 failed 任务，实际 %+v", out.Jobs)
	}
	if len(out.Assets) != 2 {
		t.Errorf("应当有 2 条资产，实际 %d", len(out.Assets))
	}
	if len(out.Sessions) != 1 {
		t.Fatalf("应当有 1 个会话，实际 %d", len(out.Sessions))
	}
	// 🔴 2026-09-12 起会话出参里**一个字节的令牌都不回**（此前回「后 6 位」）。
	// 密钥类的东西不该露出任何片段，而「对上具体哪一个设备」看 device_id 与
	// 最近活跃时间就够 —— 那两列一直都在。
	if strings.Contains(string(r.Body), tok) {
		t.Errorf("响应里出现了完整会话令牌")
	}
	if len(tok) >= 6 && strings.Contains(string(r.Body), tok[len(tok)-6:]) {
		t.Errorf("响应里出现了会话令牌的尾段")
	}
	if strings.Contains(string(r.Body), "tokenTail") {
		t.Errorf("出参里还有 tokenTail 字段")
	}
}

func TestAdminUserDetailRejectsMissingAndUnknownUser(t *testing.T) {
	e := newTestEnv(t)
	if r := e.do("GET", "/v1/admin/user-detail", nil, e.admin()); r.Code != 422 {
		t.Errorf("不给 userId 应当 422，实际 %d", r.Code)
	}
	if r := e.do("GET", "/v1/admin/user-detail?userId=zzzzzzzz", nil, e.admin()); r.Code != 404 {
		t.Errorf("未知用户应当 404，实际 %d", r.Code)
	}
}

// 前缀撞了必须报 409 而不是悄悄取第一个 —— 认错人的代价是动到另一个账号。
func TestAdminUserDetailRefusesAmbiguousPrefix(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	// 两个共享 8 位前缀的用户。
	for _, id := range []string{"dupprefix-aaa", "dupprefix-bbb"} {
		if err := store.CreateUser(ctx, e.st.Q(), id, nil, false, "en", e.now); err != nil {
			t.Fatal(err)
		}
	}
	r := e.do("GET", "/v1/admin/user-detail?userId=dupprefi", nil, e.admin())
	// 🔴 变异验证：把 hAdminUserDetail 里的 `if matches > 1` 删掉，
	// 这里会变成 200（取了第一个），这行红。
	if r.Code != 409 {
		t.Fatalf("撞前缀应当 409 AMBIGUOUS，实际 %d: %s", r.Code, r.Body)
	}
	if !strings.Contains(string(r.Body), "AMBIGUOUS") {
		t.Errorf("错误码应当是 AMBIGUOUS，实际 %s", r.Body)
	}
	// 填完整 id 就能进。
	if r := e.do("GET", "/v1/admin/user-detail?userId=dupprefix-aaa", nil, e.admin()); r.Code != 200 {
		t.Errorf("完整 id 应当 200，实际 %d: %s", r.Code, r.Body)
	}
}

// ---- 反馈：正文可见 + 已处理往返 -------------------------------------------

// 用户写的正文从建库起就在落库，2026-09-12 之前后台从来没 SELECT 它。
func TestAdminFeedbackShowsUserComment(t *testing.T) {
	e := newTestEnv(t)
	candID, _ := e.seedFeedback("脸变了，完全不像我")

	var out struct {
		Note      string `json:"note"`
		Unhandled int    `json:"unhandled"`
		Feedback  []struct {
			ID          string  `json:"id"`
			Comment     *string `json:"comment"`
			Rating      string  `json:"rating"`
			CandidateID *string `json:"candidateId"`
			AssetID     *string `json:"assetId"`
			HandledAt   *string `json:"handledAt"`
		} `json:"feedback"`
	}
	r := e.do("GET", "/v1/admin/feedback", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("反馈视图应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Errorf("视图说明不能空")
	}
	if len(out.Feedback) != 1 {
		t.Fatalf("应当有 1 条反馈，实际 %d", len(out.Feedback))
	}
	f := out.Feedback[0]
	// 🔴 变异验证：把 ListAdminFeedbackFull 的 SELECT 里的 f.comment 去掉，
	// 这行立刻红 —— 它就是这次审计抓到的那个黑洞的探针。
	if f.Comment == nil || *f.Comment != "脸变了，完全不像我" {
		t.Errorf("后台必须看得见用户写的正文，实际 %v", f.Comment)
	}
	if f.CandidateID == nil || *f.CandidateID != candID {
		t.Errorf("反馈行应当带候选 id，实际 %v", f.CandidateID)
	}
	if f.AssetID == nil {
		t.Errorf("反馈行应当带资产 id（运营要看那张图）")
	}
	if f.HandledAt != nil {
		t.Errorf("新反馈不该是已处理")
	}
	if out.Unhandled != 1 {
		t.Errorf("未处理计数应当 1，实际 %d", out.Unhandled)
	}
}

// 已处理标记往返 + 审计。
func TestFeedbackHandledRoundTripAndAudit(t *testing.T) {
	e := newTestEnv(t)
	e.seedFeedback("导出的图有噪点")

	var list struct {
		Feedback []struct {
			ID string `json:"id"`
		} `json:"feedback"`
	}
	e.do("GET", "/v1/admin/feedback", nil, e.admin()).JSON(t, &list)
	if len(list.Feedback) != 1 {
		t.Fatalf("应当有 1 条反馈")
	}
	fid := list.Feedback[0].ID

	// 标记已处理 + 备注。
	var marked struct {
		OK          bool   `json:"ok"`
		Handled     bool   `json:"handled"`
		Unhandled   int    `json:"unhandled"`
		AuditLogged bool   `json:"auditLogged"`
		Err         string `json:"-"`
	}
	r := e.do("POST", "/v1/admin/feedback/"+fid+"/handled",
		map[string]any{"handled": true, "note": "已退额度并回访"}, e.admin())
	if r.Code != 200 {
		t.Fatalf("标记已处理应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &marked)
	if !marked.Handled || marked.Unhandled != 0 || !marked.AuditLogged {
		t.Errorf("标记后应当 handled=true / unhandled=0 / auditLogged=true，实际 %+v", marked)
	}

	// 读回来：handledAt 与备注都在。
	var after struct {
		Unhandled int `json:"unhandled"`
		Feedback  []struct {
			HandledAt   *string `json:"handledAt"`
			HandledNote *string `json:"handledNote"`
		} `json:"feedback"`
	}
	e.do("GET", "/v1/admin/feedback", nil, e.admin()).JSON(t, &after)
	if after.Feedback[0].HandledAt == nil {
		t.Fatalf("handledAt 应当非空")
	}
	if after.Feedback[0].HandledNote == nil || *after.Feedback[0].HandledNote != "已退额度并回访" {
		t.Errorf("备注应当存下来，实际 %v", after.Feedback[0].HandledNote)
	}
	if after.Unhandled != 0 {
		t.Errorf("未处理计数应当 0，实际 %d", after.Unhandled)
	}

	// handled=no 筛选应当为空，handled=yes 应当有一条。
	var no, yes struct {
		Feedback []struct{ ID string } `json:"feedback"`
	}
	e.do("GET", "/v1/admin/feedback?handled=no", nil, e.admin()).JSON(t, &no)
	e.do("GET", "/v1/admin/feedback?handled=yes", nil, e.admin()).JSON(t, &yes)
	if len(no.Feedback) != 0 {
		t.Errorf("handled=no 应当为空，实际 %d", len(no.Feedback))
	}
	if len(yes.Feedback) != 1 {
		t.Errorf("handled=yes 应当 1 条，实际 %d", len(yes.Feedback))
	}

	// 审计里有这条操作。
	var audit struct {
		Audit []struct {
			Action string         `json:"action"`
			Props  map[string]any `json:"props"`
		} `json:"audit"`
	}
	e.do("GET", "/v1/admin/audit", nil, e.admin()).JSON(t, &audit)
	found := false
	for _, a := range audit.Audit {
		if a.Action == "feedback_handled" && a.Props["feedbackId"] == fid {
			found = true
		}
	}
	if !found {
		t.Errorf("审计里应当有一条 feedback_handled，实际 %+v", audit.Audit)
	}

	// 取消标记：handled_at 与备注一起清掉。
	if r := e.do("POST", "/v1/admin/feedback/"+fid+"/handled",
		map[string]any{"handled": false, "note": "这个 note 应当被忽略"}, e.admin()); r.Code != 200 {
		t.Fatalf("取消标记应当 200，实际 %d: %s", r.Code, r.Body)
	}
	var back struct {
		Unhandled int `json:"unhandled"`
		Feedback  []struct {
			HandledAt   *string `json:"handledAt"`
			HandledNote *string `json:"handledNote"`
		} `json:"feedback"`
	}
	e.do("GET", "/v1/admin/feedback", nil, e.admin()).JSON(t, &back)
	if back.Feedback[0].HandledAt != nil {
		t.Errorf("取消标记后 handledAt 应当为空，实际 %v", back.Feedback[0].HandledAt)
	}
	// 🔴 变异验证：把 SetFeedbackHandled 的 handled=false 分支改成只清 handled_at
	// （不清 handled_note），这行红 —— 留着上一次的备注会让下一个人看到
	// 「未处理」却带着一条「已退额度」的备注。
	if back.Feedback[0].HandledNote != nil {
		t.Errorf("取消标记后备注应当一起清掉，实际 %v", back.Feedback[0].HandledNote)
	}
	if back.Unhandled != 1 {
		t.Errorf("取消后未处理计数应当回到 1，实际 %d", back.Unhandled)
	}
}

func TestFeedbackHandledRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	e.seedFeedback("x")
	var list struct {
		Feedback []struct{ ID string } `json:"feedback"`
	}
	e.do("GET", "/v1/admin/feedback", nil, e.admin()).JSON(t, &list)
	fid := list.Feedback[0].ID

	if r := e.do("POST", "/v1/admin/feedback/"+fid+"/handled", map[string]any{}, e.admin()); r.Code != 422 {
		t.Errorf("不给 handled 应当 422，实际 %d", r.Code)
	}
	if r := e.do("POST", "/v1/admin/feedback/"+fid+"/handled", map[string]any{"handled": "yes"}, e.admin()); r.Code != 422 {
		t.Errorf("handled 不是布尔应当 422，实际 %d", r.Code)
	}
	// 超长备注**拒绝**而不是截断（后台输入要么全收、要么明确报错）。
	long := strings.Repeat("长", MaxHandledNote+1)
	if r := e.do("POST", "/v1/admin/feedback/"+fid+"/handled",
		map[string]any{"handled": true, "note": long}, e.admin()); r.Code != 422 {
		t.Errorf("超长备注应当 422，实际 %d", r.Code)
	}
	// 不存在的 id 必须 404 而不是静默成功。
	if r := e.do("POST", "/v1/admin/feedback/no-such-row/handled",
		map[string]any{"handled": true}, e.admin()); r.Code != 404 {
		t.Errorf("未知反馈 id 应当 404，实际 %d", r.Code)
	}
}

// seedFeedback 造一个用户 + 任务 + 候选 + 一条带正文的差评，返回候选 id 与用户 id。
func (e *testEnv) seedFeedback(comment string) (candID, userID string) {
	e.t.Helper()
	ctx := nil2ctx()
	uid, tok, pid, aid := e.prepareJobInputs("fb@example.com", 2)
	candAsset := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), candAsset, uid, pid, candAsset+".jpg", 1000, 800, 1000, aigc.MarkVisibleMeta, e.now); err != nil {
		e.t.Fatal(err)
	}
	jobID := "job-fb"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "succeeded", Stage: "complete", Controls: []byte(`{}`), Output: []byte(`{}`),
		ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		e.t.Fatal(err)
	}
	candID = "cand-fb"
	if err := store.InsertCandidate(ctx, e.st.Q(), candID, jobID, 0, candAsset, e.now); err != nil {
		e.t.Fatal(err)
	}
	if r := e.do("POST", "/v1/candidates/"+candID+"/feedback",
		map[string]any{"rating": "negative", "reasonCodes": []any{"BAD_DETAILS"}, "comment": comment},
		bearer(tok)); r.Code != 200 {
		e.t.Fatalf("提交反馈应当 200，实际 %d: %s", r.Code, r.Body)
	}
	return candID, uid
}

// ---- CSV 导出 --------------------------------------------------------------

// 导出的邮箱与用户 id 必须**完整**，行数必须等于列表条数。
func TestExportCSVKeepsFullEmailAndMatchesRowCount(t *testing.T) {
	e := newTestEnv(t)
	uid, _ := e.signUp("exportme@example.com")

	var list struct {
		Users []struct {
			Email  *string `json:"email"`
			UserID string  `json:"userId"`
			ID     string  `json:"id"`
		} `json:"users"`
	}
	e.do("GET", "/v1/admin/users", nil, e.admin()).JSON(t, &list)
	if len(list.Users) != 1 {
		t.Fatalf("应当有 1 个用户，实际 %d", len(list.Users))
	}
	// 页面上是完整邮箱（只有持令牌的人看得见）。
	if list.Users[0].Email == nil || *list.Users[0].Email != "exportme@example.com" {
		t.Fatalf("列表里应当是完整邮箱，实际 %v", list.Users[0].Email)
	}
	// 🔴 列表里的 id 也必须是完整 id —— userId 与 id 两个字段都是。
	// 变异验证：把 ListAdminUsers 的第二列换回 substr(u.id,1,8)，这两行红。
	if list.Users[0].UserID != uid || list.Users[0].ID != uid {
		t.Errorf("列表里的 userId/id 都应当是完整 id %q，实际 %q / %q",
			uid, list.Users[0].UserID, list.Users[0].ID)
	}

	r := e.do("GET", "/v1/admin/export/users.csv", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("导出应当 200，实际 %d: %s", r.Code, r.Body)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type 应当是 text/csv，实际 %q", ct)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("应当是 attachment 下载，实际 %q", cd)
	}
	body := string(r.Body)
	// 🔴 2026-09-12 第七轮：导出里的邮箱与用户 id 必须**完整**。
	// 这条断言是反过来的（此前要求打码成 e***@example.com）：导出的全部用处
	// 就是拿它去对账、群发、挨个联系用户，打了码一件也做不了 ——
	// 实际发生的事是有人绕开导出直接去抄数据库。
	// 变异验证：把 exportUsers 里的 store.CSVText(r.Email) 换回打码，这行红。
	if !strings.Contains(body, "exportme@example.com") {
		t.Errorf("导出里应当是完整邮箱，实际 %s", body)
	}
	if strings.Contains(body, "e***@example.com") {
		t.Errorf("导出里的邮箱又被打码了：%s", body)
	}
	// 表头也必须跟着改：「邮箱(已打码)」会被当成事实去跟运营解释。
	if strings.Contains(body, "已打码") || strings.Contains(body, "前8位") {
		t.Errorf("CSV 表头还在宣称打码 / 只给前 8 位：%s", body)
	}
	if len(list.Users) > 0 && !strings.Contains(body, uid) {
		t.Errorf("导出里应当是完整用户 id %q，实际 %s", uid, body)
	}
	// BOM：不带它中文列在 Excel 里全是乱码。
	if !strings.HasPrefix(body, "\ufeff") {
		t.Errorf("CSV 应当以 UTF-8 BOM 开头")
	}

	// X-Row-Count == 数据行数 == 列表条数。
	rows := parseCSV(t, r.Body)
	if len(rows) < 2 {
		t.Fatalf("CSV 至少要有表头 + 1 行数据，实际 %d 行", len(rows))
	}
	dataRows := len(rows) - 1
	if got := r.Header.Get("X-Row-Count"); got != strconv.Itoa(dataRows) {
		t.Errorf("X-Row-Count（%s）应当等于数据行数（%d）", got, dataRows)
	}
	if dataRows != len(list.Users) {
		t.Errorf("CSV 数据行数（%d）应当等于列表条数（%d）", dataRows, len(list.Users))
	}
}

// 反馈正文里的换行不能把一行 CSV 变成两行。
func TestExportCSVFlattensNewlinesInFreeText(t *testing.T) {
	e := newTestEnv(t)
	e.seedFeedback("第一行\n第二行\r\n第三行")

	r := e.do("GET", "/v1/admin/export/feedback.csv", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("导出应当 200，实际 %d: %s", r.Code, r.Body)
	}
	rows := parseCSV(t, r.Body)
	// 🔴 变异验证：把 store.CSVText 换成 derefStr（不压换行），
	// encoding/csv 会给字段加引号、行数仍然是 2 —— 但任何按行切的下游脚本会错位。
	// 这里断言的是「字段里没有裸换行」，这才是那个风险的直接探针。
	if len(rows) != 2 {
		t.Fatalf("应当是表头 + 1 行数据，实际 %d 行：%q", len(rows), rows)
	}
	joined := strings.Join(rows[1], "|")
	if strings.ContainsAny(joined, "\n\r") {
		t.Errorf("CSV 字段里不该有裸换行：%q", joined)
	}
	if !strings.Contains(joined, "第一行 第二行 第三行") {
		t.Errorf("换行应当被压成空格，实际 %q", joined)
	}
}

func TestExportCSVRejectsUnknownKind(t *testing.T) {
	e := newTestEnv(t)
	if r := e.do("GET", "/v1/admin/export/secrets.csv", nil, e.admin()); r.Code != 404 {
		t.Errorf("未知导出类型应当 404，实际 %d", r.Code)
	}
	// 六类都要能导。
	for _, k := range exportKinds {
		if r := e.do("GET", "/v1/admin/export/"+k+".csv", nil, e.admin()); r.Code != 200 {
			t.Errorf("导出 %s 应当 200，实际 %d: %s", k, r.Code, r.Body)
		}
	}
}

func parseCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	rd := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), "\ufeff")))
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatalf("CSV 解析失败: %v", err)
	}
	return rows
}

// ---- 接口健康 --------------------------------------------------------------

func TestAdminAPIHealthCountsRealRequests(t *testing.T) {
	e := newTestEnv(t)
	// 打几次公开接口，制造计数。
	for i := 0; i < 3; i++ {
		e.do("GET", "/v1/health", nil, nil)
	}
	// 一次 401（无令牌打受保护接口）。
	e.do("GET", "/v1/entitlements/me", nil, nil)

	var out struct {
		Note     string `json:"note"`
		Hours    int    `json:"hours"`
		Total    int64  `json:"total"`
		Count4xx int64  `json:"count4xx"`
		Count5xx int64  `json:"count5xx"`
		Routes   []struct {
			Route    string `json:"route"`
			Method   string `json:"method"`
			Total    int64  `json:"total"`
			Count4xx int64  `json:"count4xx"`
			P95MS    int64  `json:"p95Ms"`
		} `json:"routes"`
	}
	r := e.do("GET", "/v1/admin/api-health", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("接口健康应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Errorf("视图说明不能空")
	}
	if out.Hours != 24 {
		t.Errorf("默认窗口应当 24 小时，实际 %d", out.Hours)
	}

	byRoute := map[string]int64{}
	fourxx := map[string]int64{}
	for _, rt := range out.Routes {
		byRoute[rt.Route] = rt.Total
		fourxx[rt.Route] = rt.Count4xx
	}
	if byRoute[`GET /v1/health`] != 3 {
		t.Errorf("/v1/health 应当计 3 次，实际 %d（%+v）", byRoute[`GET /v1/health`], byRoute)
	}
	// 「这个接口在回多少 401」正是接口健康最该回答的事，所以 requireAccount
	// 返回的 401 必须计入这条路由（而不是掉进「未匹配」）。
	//
	// 🔴 为什么 `*matched = rt.name` 要写在鉴权**之前**：不是为了这个 401
	// （authenticate 对「没令牌 / 令牌无效」一律回 (nil, nil)，401 是
	// requireAccount 在 handler 里给的，之后赋值也一样会被计上），而是为了
	// readBody 的 413 —— 它在 handler 跑之前就 return，那一档请求正是
	// 「App 传了超大图」的唯一痕迹。两条限制要写清楚：限流的 429 在路由循环
	// **之前**返回，不进计数器；未匹配路由一律不计。
	if fourxx[`GET /v1/entitlements/me`] != 1 {
		t.Errorf("/v1/entitlements/me 的 401 应当被计入 4xx，实际 %d（%+v）", fourxx[`GET /v1/entitlements/me`], fourxx)
	}
	if out.Count4xx < 1 {
		t.Errorf("总 4xx 至少 1，实际 %d", out.Count4xx)
	}
	if out.Count5xx != 0 {
		t.Errorf("这一串请求不该有 5xx，实际 %d", out.Count5xx)
	}
	// 路由名是静态模式，绝不能是带 id 的原始路径。
	for _, rt := range out.Routes {
		if strings.Contains(rt.Route, "img_token") {
			t.Errorf("路由名里不该出现 query（可能含令牌）：%q", rt.Route)
		}
	}
}

// 未匹配到路由表的请求不该进计数器（否则行数由攻击者控制）。
func TestAPIHealthIgnoresUnmatchedPaths(t *testing.T) {
	e := newTestEnv(t)
	e.do("GET", "/v1/no-such-endpoint", nil, nil)
	e.do("GET", "/v1/also/not/a/route", nil, nil)

	var out struct {
		Routes []struct {
			Route string `json:"route"`
		} `json:"routes"`
	}
	e.do("GET", "/v1/admin/api-health", nil, e.admin()).JSON(t, &out)
	for _, rt := range out.Routes {
		// 🔴 变异验证：把 serve 里的 `if matched != ""` 去掉（无条件 Observe），
		// 这两条 404 会各自变成一行，而路径是请求方随便写的 —— 表会无上界地涨。
		if strings.Contains(rt.Route, "no-such-endpoint") || strings.Contains(rt.Route, "not/a/route") {
			t.Errorf("未匹配路由不该进计数器，实际出现了 %q", rt.Route)
		}
	}
}

// ---- 邮件发送记录 ----------------------------------------------------------

func TestAdminEmailLogRecordsFullAddressesAndSubjects(t *testing.T) {
	e := newTestEnv(t)
	// 后台测试邮件（走 Mailer.Send）。
	if r := e.do("POST", "/v1/admin/email/test", map[string]any{"to": "ops@example.com"}, e.admin()); r.Code != 200 {
		t.Fatalf("测试邮件应当 200，实际 %d: %s", r.Code, r.Body)
	}
	// 验证码（走 SendLoginCode）。
	if r := e.do("POST", "/v1/auth/email/request", map[string]any{"email": "code@example.com"}, nil); r.Code != 200 {
		t.Fatalf("请求验证码应当 200，实际 %d: %s", r.Code, r.Body)
	}

	var out struct {
		Note       string `json:"note"`
		Configured bool   `json:"configured"`
		Sends      []struct {
			Kind    string  `json:"kind"`
			To      string  `json:"to"`
			Subject string  `json:"subject"`
			OK      bool    `json:"ok"`
			Error   *string `json:"error"`
		} `json:"sends"`
		Codes []struct {
			Email      string `json:"email"`
			IssueCount int    `json:"issueCount"`
		} `json:"codes"`
	}
	r := e.do("GET", "/v1/admin/email-log", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("邮件记录应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Errorf("视图说明不能空")
	}
	if len(out.Sends) != 2 {
		t.Fatalf("应当有 2 条发信记录（admin_test + login_code），实际 %d: %+v", len(out.Sends), out.Sends)
	}
	kinds := map[string]string{}
	for _, s := range out.Sends {
		kinds[s.Kind] = s.To
		if !s.OK {
			t.Errorf("两次发送都该成功，实际 %+v", s)
		}
		// 🔴 收件地址必须**完整**（2026-09-12 第七轮，与此前的断言相反）：
		// 这张表回答的是「用户说他没收到验证码」，而 a***@example.com
		// 对不上任何一个具体的人 —— 客服还是得去 SSH 查库。
		if strings.Contains(s.To, "***") {
			t.Errorf("发信记录里的地址又被打码了：%q", s.To)
		}
		if s.Subject == "" {
			t.Errorf("发信记录必须记主题，实际 %+v", s)
		}
	}
	if kinds["admin_test"] != "ops@example.com" {
		t.Errorf("后台测试邮件应记完整地址，实际 %q", kinds["admin_test"])
	}
	if kinds["login_code"] != "code@example.com" {
		t.Errorf("验证码信应记完整地址，实际 %q", kinds["login_code"])
	}
	// 🔴 但验证码**本体**绝不能进这张表。真实主题以明文验证码开头，
	// 所以验证码信记的主题必须是那个不含码的常量。
	code := e.mail.lastCode
	if code == "" {
		t.Fatal("测试替身应当记下刚发出的验证码，否则下面的反向断言是假绿")
	}
	if strings.Contains(string(r.Body), code) {
		t.Fatalf("🔴 发信记录里出现了明文验证码（%d 字节的那个）", len(code))
	}
	for _, s := range out.Sends {
		if s.Kind == "login_code" && s.Subject != loginCodeLogSubject {
			t.Errorf("验证码信的主题必须是不含码的常量，实际 %q", s.Subject)
		}
	}
	// 验证码台账：邮箱完整、绝不含哈希。
	if len(out.Codes) != 1 {
		t.Fatalf("应当有 1 条验证码台账，实际 %d", len(out.Codes))
	}
	if out.Codes[0].Email != "code@example.com" {
		t.Errorf("台账里的邮箱应当完整，实际 %q", out.Codes[0].Email)
	}
	if strings.Contains(string(r.Body), "code_hash") || strings.Contains(string(r.Body), "codeHash") {
		t.Errorf("响应里绝不能出现验证码哈希：%s", r.Body)
	}
	// 发信记录不该污染埋点视图。
	var ev struct {
		Names []struct {
			Name string `json:"name"`
		} `json:"names"`
	}
	e.do("GET", "/v1/admin/events", nil, e.admin()).JSON(t, &ev)
	for _, n := range ev.Names {
		if n.Name == store.EmailSendKind {
			t.Errorf("埋点视图不该把发信记录算成用户行为")
		}
	}
}

// ---- 鉴权：每条新路由都必须挡住无令牌 / 错令牌 -----------------------------

func TestNewAdminRoutesRequireAdminToken(t *testing.T) {
	e := newTestEnv(t)
	type probe struct{ method, path string }
	probes := []probe{
		{"GET", "/v1/admin/events"},
		{"GET", "/v1/admin/assets"},
		{"GET", "/v1/admin/user-detail?userId=x"},
		{"GET", "/v1/admin/email-log"},
		{"GET", "/v1/admin/api-health"},
		{"GET", "/v1/admin/export/users.csv"},
		{"POST", "/v1/admin/feedback/x/handled"},
		{"POST", "/v1/admin/jobs/x/retry"},
		{"POST", "/v1/admin/purchases/x/reverify"},
		{"GET", "/v1/admin/job-detail?jobId=x"},
		{"GET", "/v1/admin/photo-analyses"},
		{"GET", "/v1/admin/style-versions"},
	}
	for _, p := range probes {
		if r := e.do(p.method, p.path, map[string]any{"handled": true}, nil); r.Code != 401 {
			t.Errorf("%s %s 无令牌应当 401，实际 %d", p.method, p.path, r.Code)
		}
		bad := map[string]string{"X-Admin-Token": "wrong-token-entirely"}
		if r := e.do(p.method, p.path, map[string]any{"handled": true}, bad); r.Code != 401 {
			t.Errorf("%s %s 错令牌应当 401，实际 %d", p.method, p.path, r.Code)
		}
	}
}
