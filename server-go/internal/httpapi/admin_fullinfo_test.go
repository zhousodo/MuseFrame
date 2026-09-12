package httpapi

import (
	"strings"
	"testing"
	"time"

	"museframe-api/internal/aigc"
	"museframe-api/internal/store"
)

// 🔴 这个文件钉的是 2026-09-12 第七轮的那一条决定：
//
//	**这是自家后台，用户资料一律完整显示。**
//
// 邮箱就是邮箱、用户 id 就是完整 id、交易号就是交易号、IP 就是那个 IP。
// 此前这些东西在后台被打码/截断，而代价不是「更安全」——
// 客服照样需要联系用户、照样需要对账，于是真实发生的事是有人 SSH 上去 psql。
// 仍然一个字节都不给的只有**凭据**（会话令牌、验证码哈希、密钥），
// 那几条在 redaction_test.go 与 dbbrowser_test.go 里各有反向断言。

// 用户列表 + 用户详情：完整邮箱、完整 id、登录身份。
func TestAdminUsersAndDetailShowFullEmailAndFullID(t *testing.T) {
	e := newTestEnv(t)
	const email = "full.info@example.com"
	uid, _ := e.signUp(email)

	var list struct {
		Users []struct {
			UserID string  `json:"userId"`
			ID     string  `json:"id"`
			Email  *string `json:"email"`
		} `json:"users"`
	}
	e.do("GET", "/v1/admin/users", nil, e.admin()).JSON(t, &list)
	if len(list.Users) != 1 {
		t.Fatalf("应当有 1 个用户，实际 %d", len(list.Users))
	}
	u := list.Users[0]
	if u.Email == nil || *u.Email != email {
		t.Errorf("列表里的邮箱应当完整，实际 %v", u.Email)
	}
	if u.UserID != uid || u.ID != uid {
		t.Errorf("列表里的 userId/id 都应当是完整 id %q，实际 %q / %q", uid, u.UserID, u.ID)
	}

	// 详情页：用完整 id 进，也必须能用前缀进（工单里常常只抄了几位）。
	var det struct {
		User struct {
			ID    string  `json:"id"`
			Email *string `json:"email"`
		} `json:"user"`
		Identities []struct {
			Provider string  `json:"provider"`
			Email    *string `json:"email"`
			Subject  string  `json:"subject"`
		} `json:"identities"`
		FreeGrants []struct {
			IP     *string `json:"ip"`
			IPHash *string `json:"ipHash"`
			Units  int     `json:"units"`
		} `json:"freeGrants"`
	}
	r := e.do("GET", "/v1/admin/user-detail?userId="+uid, nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("用户详情应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &det)
	if det.User.ID != uid {
		t.Errorf("详情页应回完整 id %q，实际 %q", uid, det.User.ID)
	}
	// 🔴 这是这一轮最直接的那个缺口：详情页此前**一个邮箱都不显示**，
	// 于是客服在这一页确认完「就是这个人」之后，还得回列表页去抄邮箱才能回信。
	if det.User.Email == nil || *det.User.Email != email {
		t.Errorf("详情页必须显示完整邮箱，实际 %v", det.User.Email)
	}
	if len(det.Identities) != 1 {
		t.Fatalf("应当有 1 条登录身份，实际 %d", len(det.Identities))
	}
	id0 := det.Identities[0]
	if id0.Provider != "email" || id0.Email == nil || *id0.Email != email {
		t.Errorf("登录身份应当是 email + 完整地址，实际 %+v", id0)
	}
	// provider_subject 是向平台提工单时唯一认的那个 id，必须完整。
	if !strings.Contains(id0.Subject, email) {
		t.Errorf("provider_subject 应当完整（含邮箱），实际 %q", id0.Subject)
	}
	// 前缀也能进（匹配唯一时）。
	if r := e.do("GET", "/v1/admin/user-detail?userId="+uid[:8], nil, e.admin()); r.Code != 200 {
		t.Errorf("8 位前缀应当仍然能进详情页，实际 %d %s", r.Code, r.Body)
	}
}

// 🔴 免费额度发放必须记**明文 IP**（free_grants.ip，migrations/005）。
//
// 此前只有 ip_hash。哈希够用来数「这个 IP 今天领了几张」，但运营要回答的是
// 「这 40 个账号是不是同一个人」「要不要把这个地址报给 CDN 拦一下」——
// 哈希对这两个问题一个字也答不出来；盐一换，连历史行的可比性都没了。
func TestFreeGrantRecordsPlainClientIP(t *testing.T) {
	e := newTestEnv(t)
	uid, _ := e.signUp("ipgrant@example.com")

	// 库里那一行：明文 IP 必须是这次请求的来源地址，哈希也必须还在。
	var ip, ipHash *string
	if err := e.st.Pool().QueryRow(nil2ctx(),
		`SELECT ip, ip_hash FROM free_grants WHERE user_id = $1 AND units > 0`, uid).
		Scan(&ip, &ipHash); err != nil {
		t.Fatalf("免费额度发放台账应当有一行带 units 的记录: %v", err)
	}
	if ip == nil || *ip != "127.0.0.1" {
		t.Fatalf("free_grants.ip 应当是明文客户端地址 127.0.0.1，实际 %v", ip)
	}
	// 🔴 哈希不能被顶掉：24h 滚动上限的计数与去重还走它。
	if ipHash == nil || *ipHash == "" {
		t.Fatal("ip_hash 必须继续写（限流计数走它）")
	}
	if *ipHash == *ip {
		t.Fatal("ip_hash 被写成了明文 —— 限流键必须仍然是加盐哈希")
	}

	// 后台看得见：用户详情里的免费额度发放小节。
	var det struct {
		FreeGrants []struct {
			IP    *string `json:"ip"`
			Units int     `json:"units"`
		} `json:"freeGrants"`
	}
	e.do("GET", "/v1/admin/user-detail?userId="+uid, nil, e.admin()).JSON(t, &det)
	if len(det.FreeGrants) == 0 {
		t.Fatal("用户详情必须列出免费额度发放台账")
	}
	seen := false
	for _, g := range det.FreeGrants {
		if g.IP != nil && *g.IP == "127.0.0.1" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("用户详情里应当看得见明文 IP，实际 %+v", det.FreeGrants)
	}
}

// 任务详情必须列出**全部**候选，按 created_at 升序。
//
// 任务列表那一行只显示第一张候选，于是「后面几张是不是也这样」
// 此前只能去数据库浏览器翻 generation_candidates（而那张表里没有归属、没有顺序概念）。
func TestAdminJobDetailListsAllCandidatesByCreated(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("jobdetail@example.com", 3)
	jobID := "job-cands"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "succeeded", Stage: "complete", Controls: []byte(`{"strength":"bold"}`),
		Output: []byte(`{"aspectRatio":"4:5"}`), ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	// 三张候选。🔴 candidate_index 与产出时间**刻意反序**：这样「按 index 排」
	// 和「按 created 排」会给出不同答案，下面的断言才真的在钉排序键。
	type cand struct {
		id    string
		index int
		min   int
	}
	for _, c := range []cand{{"cand-c", 2, 0}, {"cand-b", 1, 5}, {"cand-a", 0, 10}} {
		asset := e.nextID()
		if err := store.InsertCandidateAsset(ctx, e.st.Q(), asset, uid, pid, asset+".jpg",
			1000, 800, 1000, aigc.MarkVisibleMeta, e.now); err != nil {
			t.Fatal(err)
		}
		at := e.now.Add(time.Duration(c.min) * time.Minute)
		if err := store.InsertCandidate(ctx, e.st.Q(), c.id, jobID, c.index, asset, at); err != nil {
			t.Fatal(err)
		}
	}

	var out struct {
		Note string `json:"note"`
		Job  struct {
			ID     string  `json:"id"`
			User   string  `json:"user"`
			Email  *string `json:"email"`
			Status string  `json:"status"`
		} `json:"job"`
		Candidates []struct {
			ID            string `json:"id"`
			Index         int    `json:"index"`
			AssetID       string `json:"assetId"`
			CreatedAt     string `json:"createdAt"`
			QualityPassed bool   `json:"qualityPassed"`
		} `json:"candidates"`
	}
	r := e.do("GET", "/v1/admin/job-detail?jobId="+jobID, nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("任务详情应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Error("视图说明不能空（口径由后端给）")
	}
	// 任务行与列表同源：完整用户 id + 完整邮箱。
	if out.Job.ID != jobID || out.Job.User != uid {
		t.Errorf("任务行应当是 %q / 完整用户 id %q，实际 %+v", jobID, uid, out.Job)
	}
	if out.Job.Email == nil || *out.Job.Email != "jobdetail@example.com" {
		t.Errorf("任务行应当带完整邮箱，实际 %v", out.Job.Email)
	}
	if len(out.Candidates) != 3 {
		t.Fatalf("应当列出全部 3 张候选，实际 %d: %+v", len(out.Candidates), out.Candidates)
	}
	// 🔴 变异验证：把 ListJobCandidates 的 ORDER BY 换成 candidate_index ASC，
	// 这里的顺序会变成 cand-a / cand-b / cand-c，这行红。
	want := []string{"cand-c", "cand-b", "cand-a"}
	for i, w := range want {
		if out.Candidates[i].ID != w {
			t.Errorf("第 %d 张候选应当是 %q（按产出时间升序），实际 %q", i, w, out.Candidates[i].ID)
		}
	}
	if out.Candidates[0].CreatedAt >= out.Candidates[2].CreatedAt {
		t.Errorf("createdAt 必须是升序，实际 %q … %q",
			out.Candidates[0].CreatedAt, out.Candidates[2].CreatedAt)
	}
	// 不存在的任务是 404，不是空详情页。
	if r := e.do("GET", "/v1/admin/job-detail?jobId=nope", nil, e.admin()); r.Code != 404 {
		t.Errorf("未知任务应当 404，实际 %d", r.Code)
	}
	if r := e.do("GET", "/v1/admin/job-detail", nil, e.admin()); r.Code != 422 {
		t.Errorf("缺 jobId 应当 422，实际 %d", r.Code)
	}
}

// photo_analyses 的只读视图：带归属（完整 id + 完整邮箱）与分析原文。
func TestAdminPhotoAnalysesView(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, _, aid := e.prepareJobInputs("analysis@example.com", 1)
	if err := store.EnsureAnalysisRow(ctx, e.st.Q(), e.nextID(), aid, e.now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAnalysisReady(ctx, e.st.Q(), aid, "person", 2, 0.71, 0.48,
		[]byte(`["LOW_LIGHT"]`), []byte(`["靠窗再拍一张"]`), e.now); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Note     string         `json:"note"`
		Counts   map[string]int `json:"statusCounts"`
		Analyses []struct {
			AssetID         string   `json:"assetId"`
			Status          string   `json:"status"`
			SubjectType     *string  `json:"subjectType"`
			PersonCount     *int     `json:"personCount"`
			Sharpness       *float64 `json:"sharpness"`
			Warnings        []string `json:"warnings"`
			Recommendations []string `json:"recommendations"`
			User            string   `json:"user"`
			Email           *string  `json:"email"`
		} `json:"analyses"`
	}
	r := e.do("GET", "/v1/admin/photo-analyses", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("画面分析视图应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Error("视图说明不能空")
	}
	if len(out.Analyses) != 1 {
		t.Fatalf("应当有 1 条分析，实际 %d", len(out.Analyses))
	}
	a := out.Analyses[0]
	if a.AssetID != aid || a.Status != "ready" {
		t.Errorf("分析行对不上：%+v", a)
	}
	if a.SubjectType == nil || *a.SubjectType != "person" || a.PersonCount == nil || *a.PersonCount != 2 {
		t.Errorf("主体与人数应当如实回，实际 %+v", a)
	}
	if a.Sharpness == nil {
		t.Error("清晰度不该丢")
	}
	if len(a.Warnings) != 1 || len(a.Recommendations) != 1 {
		t.Errorf("提示与建议原文必须回，实际 %+v / %+v", a.Warnings, a.Recommendations)
	}
	// 归属是这个视图存在的理由之一：数据库浏览器看不到「这是谁的图」。
	if a.User != uid {
		t.Errorf("归属应当是完整用户 id %q，实际 %q", uid, a.User)
	}
	if a.Email == nil || *a.Email != "analysis@example.com" {
		t.Errorf("归属应当带完整邮箱，实际 %v", a.Email)
	}
	if out.Counts["ready"] != 1 {
		t.Errorf("状态分布应当有 1 条 ready，实际 %+v", out.Counts)
	}
	// 按用户前缀筛：筛不到的用户回 0 行（不是报错、也不是全表）。
	var empty struct {
		Analyses []struct{} `json:"analyses"`
	}
	e.do("GET", "/v1/admin/photo-analyses?userId=zzzzzzzz", nil, e.admin()).JSON(t, &empty)
	if len(empty.Analyses) != 0 {
		t.Errorf("筛一个不存在的用户应当 0 行，实际 %d", len(empty.Analyses))
	}
}

// style_versions.spec 的只读视图：完整 spec + 服役任务数。
func TestAdminStyleVersionsExposeFullSpec(t *testing.T) {
	e := newTestEnv(t)

	var out struct {
		Note     string `json:"note"`
		Versions []struct {
			ID      string         `json:"id"`
			StyleID string         `json:"styleId"`
			Version int            `json:"version"`
			Status  string         `json:"status"`
			Spec    map[string]any `json:"spec"`
			Jobs    int            `json:"jobs"`
		} `json:"versions"`
	}
	r := e.do("GET", "/v1/admin/style-versions?styleId=style-free", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("风格版本视图应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	if out.Note == "" {
		t.Error("视图说明不能空")
	}
	if len(out.Versions) != 1 {
		t.Fatalf("style-free 应当有 1 个版本，实际 %d", len(out.Versions))
	}
	v := out.Versions[0]
	if v.ID != "ver-style-free" || v.Version != 1 || v.Status != "published" {
		t.Errorf("版本行对不上：%+v", v)
	}
	// 🔴 spec 必须是**完整**的 jsonb，不是摘要：提示词模板与负面词就在里面，
	// 而「为什么这个风格出图变了」只有对照它才能回答。
	for _, key := range []string{"promptAssembly", "controls", "compatibility", "identity"} {
		if _, ok := v.Spec[key]; !ok {
			t.Errorf("spec 里缺 %q —— 必须是完整 spec 而不是摘要：%+v", key, v.Spec)
		}
	}
	pa, _ := v.Spec["promptAssembly"].(map[string]any)
	if pa == nil || pa["negativeConstraints"] == nil {
		t.Errorf("spec.promptAssembly.negativeConstraints 必须在，实际 %+v", pa)
	}
	// 不带 styleId 时回全部风格的版本（三个风格各一个）。
	var all struct {
		Versions []struct{} `json:"versions"`
	}
	e.do("GET", "/v1/admin/style-versions", nil, e.admin()).JSON(t, &all)
	if len(all.Versions) != 3 {
		t.Errorf("不带 styleId 应当回全部 3 个版本，实际 %d", len(all.Versions))
	}
}
