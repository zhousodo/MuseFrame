package httpapi

import (
	"strings"
	"testing"

	"museframe-api/internal/store"
	"museframe-api/internal/worker"
)

// 校验点 8：开机恢复 —— 无 reserve 台账的 created 任务标失败、不重跑；
// attempt_count 达上限直接失败。
func TestCheck08_BootRecovery(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("recover@example.com", 3)

	// 造一条孤儿任务：status=created 且没有任何 reserve 分录。
	orphan := "job-orphan"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: orphan, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "created", Stage: "preparing", Controls: []byte(`{}`), Output: []byte(`{}`),
		ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	// 再造一条尝试次数超限的排队任务（有 reserve）。
	burned := "job-burned"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: burned, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "queued", Stage: "preparing", Controls: []byte(`{}`), Output: []byte(`{}`),
		ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	// InsertJob 不写 attempt_count（新任务从 0 开始），这里显式推到上限。
	if err := store.SetJobAttempts(ctx, e.st.Q(), burned, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertLedger(ctx, e.st.Q(), e.nextID(), uid, "reserve", -1,
		mustBucket(t, e, uid), &burned, nil, "job:"+burned+":reserve:0", e.now); err != nil {
		t.Fatal(err)
	}

	wk := worker.New(worker.Options{
		Store: e.st, Runtime: e.rt, Provider: nil, Logger: newNopLogger(), AssetDir: e.assets,
		MaxAttempts: 3, NewID: e.nextID, Now: e.clock,
	})
	failed, requeued, err := wk.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if failed != 2 {
		t.Fatalf("孤儿任务与超次任务都应被标失败，实际 failed=%d requeued=%d", failed, requeued)
	}

	var status, stage string
	var code *string
	if err := e.st.Pool().QueryRow(ctx,
		`SELECT status, stage, error_code FROM generation_jobs WHERE id=$1`, orphan).Scan(&status, &stage, &code); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || stage != "failed" {
		t.Fatalf("无 reserve 的 created 任务必须标失败且**不重跑**，实际 %s/%s", status, stage)
	}
	// 它也不能进队列。
	if d := wk.Depth(); d.Queued != 0 {
		t.Fatalf("孤儿任务不得被重新排队（重跑就是一次没人付钱的生成），实际队列 %d", d.Queued)
	}
	if err := e.st.Pool().QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1`, burned).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("attempt_count 达上限必须直接失败（崩溃循环护栏），实际 %s", status)
	}
}

func mustBucket(t *testing.T, e *testEnv, uid string) string {
	t.Helper()
	var id string
	if err := e.st.Pool().QueryRow(nil2ctx(),
		`SELECT id FROM credit_buckets WHERE user_id=$1 ORDER BY created_at LIMIT 1`, uid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// 校验点 10：三个开发逃生口一律双闸（旗标 + 管理员令牌）。
func TestCheck10_DevHatchesDoubleGated(t *testing.T) {
	e := newTestEnv(t)
	// 旗标关闭时，即便带管理员令牌也不生效。
	r := e.do("POST", "/v1/auth/exchange", map[string]any{"provider": "dev", "email": "x@example.com"}, e.admin())
	if r.Code != 403 {
		t.Fatalf("ALLOW_TEST_LOGIN 未开时 dev 登录应 403，实际 %d %s", r.Code, r.Body)
	}
	// devCode 只在旗标 + 管理员令牌同时成立时回显。
	r = e.do("POST", "/v1/auth/email/request", map[string]any{"email": "dev@example.com"}, nil)
	if strings.Contains(string(r.Body), "devCode") {
		t.Fatalf("未开旗标时绝不能回显 devCode：%s", r.Body)
	}
	// mock 购买：旗标未开 -> 422（不是「买成了」）。
	_, tok := e.signUp("mock@example.com")
	r = e.do("POST", "/v1/purchases/verify",
		map[string]any{"productKey": "pack_10", "platform": "web", "transactionId": "t1"}, bearer(tok))
	if r.Code != 422 {
		t.Fatalf("ALLOW_MOCK_PURCHASES 未开时 web 购买应 422，实际 %d %s", r.Code, r.Body)
	}
}

// 校验点 10（另一半）：旗标开但**不带**管理员令牌 -> 必须不生效。
func TestCheck10_FlagAloneIsNotEnough(t *testing.T) {
	t.Setenv("ALLOW_TEST_LOGIN", "true")
	t.Setenv("ALLOW_MOCK_PURCHASES", "true")
	e := newTestEnv(t)
	if !e.cfg.AllowTestLogin || !e.cfg.AllowMockPurchases {
		t.Fatal("这条测试要求两个旗标都开着才有意义")
	}
	// 旗标开，但不带管理员令牌。
	r := e.do("POST", "/v1/auth/exchange", map[string]any{"provider": "dev", "email": "x@example.com"}, nil)
	if r.Code != 403 {
		t.Fatalf("只有旗标、没有管理员令牌时 dev 登录必须 403，实际 %d %s", r.Code, r.Body)
	}
	r = e.do("POST", "/v1/auth/email/request", map[string]any{"email": "dev2@example.com"}, nil)
	if strings.Contains(string(r.Body), "devCode") {
		t.Fatalf("只有旗标时不得回显 devCode（那是任意邮箱账号的接管）：%s", r.Body)
	}
	_, tok := e.signUp("mock2@example.com")
	r = e.do("POST", "/v1/purchases/verify",
		map[string]any{"productKey": "pack_10", "platform": "web", "transactionId": "t2"}, bearer(tok))
	if r.Code != 422 {
		t.Fatalf("只有旗标时 mock 购买必须不生效，实际 %d %s", r.Code, r.Body)
	}
	// 负向：旗标 + 管理员令牌 -> 生效（否则上面是假绿）。
	admin := e.admin()
	admin["Authorization"] = "Bearer " + tok
	r = e.do("POST", "/v1/purchases/verify",
		map[string]any{"productKey": "pack_10", "platform": "web", "transactionId": "t3"}, admin)
	if r.Code != 200 {
		t.Fatalf("双闸齐备时 mock 购买应成功，实际 %d %s", r.Code, r.Body)
	}
}

// 校验点 11：管理员令牌只从请求头读；query 传一律无效。
func TestCheck11_AdminTokenHeaderOnly(t *testing.T) {
	e := newTestEnv(t)
	if r := e.do("GET", "/v1/admin/overview?admin_token="+adminToken, nil, nil); r.Code != 401 {
		t.Fatalf("query 传 admin_token 必须无效（它会进访问日志 / 浏览器历史 / Referer），实际 %d", r.Code)
	}
	if r := e.do("GET", "/v1/admin/overview", nil, map[string]string{"X-Admin-Token": "wrong"}); r.Code != 401 {
		t.Fatalf("错误令牌应 401，实际 %d", r.Code)
	}
	if r := e.do("GET", "/v1/admin/overview", nil, e.admin()); r.Code != 200 {
		t.Fatalf("正确请求头应 200，实际 %d %s", r.Code, r.Body)
	}
}

// 校验点 12：worker_concurrency 必须夹在 [1, 8]。
func TestCheck12_WorkerConcurrencyClamped(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	wk := worker.New(worker.Options{Store: e.st, Runtime: e.rt, Logger: newNopLogger(), NewID: e.nextID, Now: e.clock})
	for _, c := range []struct {
		set  int
		want int
	}{{0, 1}, {-5, 1}, {1, 1}, {3, 3}, {8, 8}, {99, 8}} {
		if err := e.rt.Set(ctx, "worker_concurrency", c.set); err != nil {
			t.Fatal(err)
		}
		if got := wk.Concurrency(); got != c.want {
			t.Errorf("worker_concurrency=%d 应夹到 %d，实际 %d（设 0 会让队列静默冻结且额度仍被预留）", c.set, c.want, got)
		}
	}
}
