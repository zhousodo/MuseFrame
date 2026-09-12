package httpapi

import (
	"sort"
	"strings"
	"testing"

	"museframe-api/internal/store"
)

// 50 条路由冒烟：每条至少打一次，断言**没有 5xx**、且鉴权档位符合预期。
// 这条测试同时充当「路由表没漏注册」的守门人。
func TestSmokeAllRoutes(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, tok, pid, aid := e.prepareJobInputs("smoke@example.com", 5)

	// 造一个候选，让 feedback / export / job 详情都有真数据。
	candAsset := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), candAsset, uid, pid, candAsset+".jpg", 1000, 800, 1000, e.now); err != nil {
		t.Fatal(err)
	}
	jobID := "job-smoke"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "succeeded", Stage: "complete", Controls: []byte(`{"strength":"balanced"}`),
		Output: []byte(`{"aspectRatio":"4:5","qualityTier":"standard"}`), ReservedUnits: 1,
		CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	candID := "cand-smoke"
	if err := store.InsertCandidate(ctx, e.st.Q(), candID, jobID, 0, candAsset, e.now); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAnalysisRow(ctx, e.st.Q(), e.nextID(), aid, e.now); err != nil {
		t.Fatal(err)
	}

	user := bearer(tok)
	admin := e.admin()
	userIdem := bearer(tok)
	userIdem["Idempotency-Key"] = "smoke-key-1"

	type call struct {
		method, path string
		body         any
		headers      map[string]string
		wantMax      int // 允许的最大状态码（一律不接受 5xx）
	}
	calls := []call{
		{"POST", "/v1/auth/exchange", map[string]any{"provider": "guest"}, nil, 403},
		{"POST", "/v1/auth/email/request", map[string]any{"email": "smoke2@example.com"}, nil, 200},
		{"POST", "/v1/auth/email/verify", map[string]any{"email": "smoke2@example.com", "code": "000000"}, nil, 429},
		{"GET", "/v1/auth/config", nil, nil, 200},
		{"GET", "/v1/discover", nil, nil, 200},
		{"GET", "/v1/styles", nil, nil, 200},
		{"GET", "/v1/styles/style-free", nil, nil, 200},
		{"POST", "/v1/assets/upload-intents", map[string]any{"contentType": "image/jpeg", "byteSize": 1000}, user, 200},
		{"PUT", "/v1/assets/" + aid + "/upload", nil, user, 409},
		{"POST", "/v1/assets/" + aid + "/complete", nil, user, 409},
		{"GET", "/v1/assets/" + aid + "/analysis", nil, user, 200},
		{"GET", "/v1/assets/img-token", nil, user, 200},
		{"GET", "/v1/assets/" + candAsset + "/file", nil, user, 404},
		{"POST", "/v1/projects", map[string]any{"title": "t"}, user, 200},
		{"GET", "/v1/projects", nil, user, 200},
		{"GET", "/v1/projects/" + pid, nil, user, 200},
		{"PATCH", "/v1/projects/" + pid, map[string]any{"title": "t2"}, user, 200},
		{"DELETE", "/v1/projects/" + pid, nil, user, 200},
		{"POST", "/v1/generation-jobs", jobBody(pid, aid, "ver-style-free"), userIdem, 404},
		{"GET", "/v1/generation-jobs/" + jobID, nil, user, 200},
		{"POST", "/v1/generation-jobs/" + jobID + "/cancel", nil, user, 200},
		{"POST", "/v1/candidates/" + candID + "/feedback", map[string]any{"rating": "positive"}, user, 200},
		{"POST", "/v1/candidates/" + candID + "/export", nil, user, 200},
		{"GET", "/v1/entitlements/me", nil, user, 200},
		{"GET", "/v1/products", nil, nil, 200},
		{"POST", "/v1/purchases/verify", map[string]any{"productKey": "pack_10", "platform": "web"}, user, 422},
		{"GET", "/v1/purchases", nil, user, 200},
		{"POST", "/v1/events", map[string]any{"events": []any{map[string]any{"name": "discover_viewed"}}}, user, 200},
		{"DELETE", "/v1/auth/session", nil, user, 200},
		{"GET", "/v1/health", nil, nil, 200},
		{"GET", "/v1/ready", nil, nil, 200},

		{"GET", "/v1/admin/overview", nil, admin, 200},
		{"GET", "/v1/admin/jobs", nil, admin, 200},
		{"GET", "/v1/admin/feedback", nil, admin, 200},
		{"GET", "/v1/admin/purchases", nil, admin, 200},
		{"GET", "/v1/admin/users?q=smoke", nil, admin, 200},
		{"POST", "/v1/admin/users/grant", map[string]any{"userId": uid, "units": 2, "note": "smoke"}, admin, 200},
		{"POST", "/v1/admin/email/test", map[string]any{"to": "ops@example.com"}, admin, 200},
		{"GET", "/v1/admin/img-token", nil, admin, 200},
		{"GET", "/v1/admin/assets/" + candAsset + "/file", nil, admin, 404},
		{"GET", "/v1/admin/stats/daily?days=7", nil, admin, 200},
		{"GET", "/v1/admin/stats/styles", nil, admin, 200},
		{"GET", "/v1/admin/db/tables", nil, admin, 200},
		{"GET", "/v1/admin/db/table/users", nil, admin, 200},
		{"POST", "/v1/admin/db/query", map[string]any{"sql": "select 1 as n"}, admin, 200},
		{"GET", "/v1/admin/config", nil, admin, 200},
		{"PUT", "/v1/admin/config", map[string]any{"key": "free_units", "value": 3}, admin, 200},
		{"GET", "/v1/admin/products-admin", nil, admin, 200},
		{"PATCH", "/v1/admin/products-admin/pack_10", map[string]any{"active": true}, admin, 200},
		{"GET", "/v1/admin/styles-admin", nil, admin, 200},
		{"POST", "/v1/admin/styles-admin/style-other/status", map[string]any{"status": "published"}, admin, 200},
		// 2026-09-12 第三轮新增的 5 条。
		{"PATCH", "/v1/admin/styles-admin/style-other", map[string]any{"premium": false}, admin, 200},
		{"POST", "/v1/admin/users/" + uid + "/status", map[string]any{"status": "active"}, admin, 200},
		{"GET", "/v1/admin/user-facts", nil, admin, 200},
		{"GET", "/v1/admin/audit", nil, admin, 200},
		{"GET", "/v1/admin/feedback-reasons", nil, admin, 200},
		// 2026-09-12 第四轮新增的 9 条（全链路可见性）。
		{"GET", "/v1/admin/events?days=7", nil, admin, 200},
		{"GET", "/v1/admin/assets?kind=candidate", nil, admin, 200},
		{"GET", "/v1/admin/user-detail?userId=" + uid, nil, admin, 200},
		{"GET", "/v1/admin/email-log", nil, admin, 200},
		{"GET", "/v1/admin/api-health?hours=24", nil, admin, 200},
		{"GET", "/v1/admin/export/users.csv", nil, admin, 200},
		// 反馈行是上面 POST /v1/candidates/{id}/feedback 刚建的，id 由 nextID 决定，
		// 所以这里用一个**不存在**的 id 打 404 —— 冒烟只负责「路由活着且不 5xx」，
		// 标记往返的正确性由 TestFeedbackHandledRoundTrip 断言。
		{"POST", "/v1/admin/feedback/nope/handled", map[string]any{"handled": true}, admin, 404},
		{"POST", "/v1/admin/jobs/" + jobID + "/retry", nil, admin, 409}, // 该任务是 succeeded
		{"POST", "/v1/admin/purchases/nope/reverify", nil, admin, 404},
	}

	hit := map[string]bool{}
	for _, c := range calls {
		r := e.do(c.method, c.path, c.body, c.headers)
		if r.Code >= 500 {
			t.Errorf("%s %s 返回 5xx（%d）：%s", c.method, c.path, r.Code, r.Body)
		}
		if r.Code > c.wantMax {
			t.Errorf("%s %s 期望状态码 <= %d，实际 %d：%s", c.method, c.path, c.wantMax, r.Code, r.Body)
		}
		hit[c.method+" "+matchRoute(e, c.method, c.path)] = true
	}

	// 每条注册的路由都必须被冒烟到。
	var missed []string
	for _, rt := range e.app.routes {
		name := rt.method + " " + rt.pattern.String()
		if !hit[name] {
			missed = append(missed, name)
		}
	}
	sort.Strings(missed)
	if len(missed) > 0 {
		t.Fatalf("以下路由没有被冒烟覆盖：\n%s", strings.Join(missed, "\n"))
	}
}

// matchRoute 找出某个请求命中的路由模式（用于覆盖率统计）。
func matchRoute(e *testEnv, method, path string) string {
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	for _, rt := range e.app.routes {
		if rt.method == method && rt.pattern.MatchString(path) {
			return rt.pattern.String()
		}
	}
	return "<未命中>"
}
