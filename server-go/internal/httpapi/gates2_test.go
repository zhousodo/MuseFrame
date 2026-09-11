package httpapi

import (
	"testing"

	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

// prepareJobInputs 建好一个能提交生成任务的用户 + 资产 + 项目。
func (e *testEnv) prepareJobInputs(email string, units int) (userID, token, projectID, assetID string) {
	e.t.Helper()
	ctx := nil2ctx()
	// units==0 表示「这个账号一张额度都没有」：注册送的 3 张也要按掉，
	// 否则测的根本不是余额不足那条路径。
	if units == 0 {
		if err := e.rt.Set(ctx, "free_units", 0); err != nil {
			e.t.Fatal(err)
		}
		defer func() { _ = e.rt.Set(ctx, "free_units", 3) }()
	}
	userID, token = e.signUp(email)
	if units > 0 {
		e.grantUnits(userID, units)
	}
	assetID = e.nextID()
	size := int64(12345)
	w, h := 1024, 1280
	if err := store.InsertAsset(ctx, e.st.Q(), &store.Asset{
		ID: assetID, UserID: userID, Kind: "source", Status: "ready",
		StorageKey: assetID + ".jpg", ContentType: "image/jpeg", ByteSize: &size,
		Width: &w, Height: &h, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		e.t.Fatal(err)
	}
	projectID = e.nextID()
	if err := store.InsertProject(ctx, e.st.Q(), projectID, userID, nil, &assetID, e.now); err != nil {
		e.t.Fatal(err)
	}
	return
}

func jobBody(projectID, assetID, versionID string) map[string]any {
	return map[string]any{
		"projectId": projectID, "sourceAssetId": assetID, "styleVersionId": versionID,
		"controls": map[string]any{"strength": "balanced"},
		"output":   map[string]any{"aspectRatio": "4:5", "qualityTier": "standard"},
	}
}

// 校验点 2：Idempotency-Key 必填 + request_hash 比对 + 响应回放（不重复计费）。
func TestCheck02_IdempotencyKey(t *testing.T) {
	e := newTestEnv(t)
	uid, tok, pid, aid := e.prepareJobInputs("idem@example.com", 5)
	body := jobBody(pid, aid, "ver-style-free")

	// 不带 key -> 400
	if r := e.do("POST", "/v1/generation-jobs", body, bearer(tok)); r.Code != 400 {
		t.Fatalf("缺 Idempotency-Key 应 400 IDEMPOTENCY_KEY_REQUIRED，实际 %d %s", r.Code, r.Body)
	}
	// 非法格式 -> 422
	bad := bearer(tok)
	bad["Idempotency-Key"] = "not a valid key!!"
	if r := e.do("POST", "/v1/generation-jobs", body, bad); r.Code != 422 {
		t.Fatalf("非法 Idempotency-Key 应 422，实际 %d %s", r.Code, r.Body)
	}

	h := bearer(tok)
	h["Idempotency-Key"] = "click-0001"
	first := e.do("POST", "/v1/generation-jobs", body, h)
	if first.Code != 200 {
		t.Fatalf("首次提交应 200，实际 %d %s", first.Code, first.Body)
	}
	balAfterFirst := e.balance(uid)

	// 同 key 同 body -> 回放同一响应，且额度只扣一次。
	second := e.do("POST", "/v1/generation-jobs", body, h)
	if second.Code != 200 {
		t.Fatalf("重放应 200，实际 %d %s", second.Code, second.Body)
	}
	if string(first.Body) != string(second.Body) {
		t.Fatalf("重放必须逐字节回放原响应\n第一次: %s\n第二次: %s", first.Body, second.Body)
	}
	if got := e.balance(uid); got != balAfterFirst {
		t.Fatalf("重放不得重复计费：期望余额 %d，实际 %d", balAfterFirst, got)
	}

	// 同 key 不同 body -> 409
	other := jobBody(pid, aid, "ver-style-other")
	if r := e.do("POST", "/v1/generation-jobs", other, h); r.Code != 409 {
		t.Fatalf("同 key 不同 body 应 409 IDEMPOTENCY_CONFLICT，实际 %d %s", r.Code, r.Body)
	}
}

func (e *testEnv) balance(userID string) int {
	e.t.Helper()
	n, err := store.AvailableUnits(nil2ctx(), e.st.Q(), userID, e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// 校验点 6：建任务 + 预留额度 + 改项目状态必须在同一事务。
// 注入一个必然失败的预留（余额为 0），断言 generation_jobs 无残留行。
func TestCheck06_JobAndReserveAreOneTransaction(t *testing.T) {
	e := newTestEnv(t)
	_, tok, pid, aid := e.prepareJobInputs("tx@example.com", 0) // 0 额度
	h := bearer(tok)
	h["Idempotency-Key"] = "click-tx-1"
	r := e.do("POST", "/v1/generation-jobs", jobBody(pid, aid, "ver-style-free"), h)
	if r.Code != 402 {
		t.Fatalf("余额不足应 402 INSUFFICIENT_ENTITLEMENT，实际 %d %s", r.Code, r.Body)
	}
	var n int
	if err := e.st.Pool().QueryRow(nil2ctx(), `SELECT count(*) FROM generation_jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("预留失败时不得留下任务行（那就是白嫖任务），实际残留 %d 行", n)
	}
	var proj string
	if err := e.st.Pool().QueryRow(nil2ctx(), `SELECT status FROM projects WHERE id=$1`, pid).Scan(&proj); err != nil {
		t.Fatal(err)
	}
	if proj != "draft" {
		t.Fatalf("项目状态不得被改成 generating，实际 %q", proj)
	}
	// 幂等记录也不该写（失败的请求不能被回放成功）。
	if err := e.st.Pool().QueryRow(nil2ctx(), `SELECT count(*) FROM idempotency_records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("失败请求不得写幂等记录，实际 %d 行", n)
	}
}

// 校验点 7：reserve -> commit 与 reserve -> release 两条路径都必须幂等，
// 且 release 要先检查是否已 commit（否则「成功后再取消」会退两次钱）。
func TestCheck07_CommitAndReleaseIdempotent(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _ := e.signUp("cr@example.com")
	e.grantUnits(uid, 5)
	jobID := "job-cr-1"
	base := e.balance(uid) // 注册送的 3 张 + 手工加的 5 张

	if err := ledger.Reserve(ctx, e.st.Q(), e.nextID, uid, jobID, 1, e.now); err != nil {
		t.Fatal(err)
	}
	if got := e.balance(uid); got != base-1 {
		t.Fatalf("预留后余额应为 %d，实际 %d", base-1, got)
	}
	// 重复预留是空操作。
	if err := ledger.Reserve(ctx, e.st.Q(), e.nextID, uid, jobID, 1, e.now); err != nil {
		t.Fatal(err)
	}
	if got := e.balance(uid); got != base-1 {
		t.Fatalf("重复预留不得再扣，实际 %d", got)
	}
	// commit 两次仍是 4（commit 分录 units 恒为 0）。
	for i := 0; i < 2; i++ {
		if err := ledger.Commit(ctx, e.st.Q(), e.nextID, uid, jobID, e.now); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.balance(uid); got != base-1 {
		t.Fatalf("重复核销不得改变余额，实际 %d", got)
	}
	// 已 commit 之后 release 必须是空操作 —— 否则钱退两次。
	for i := 0; i < 2; i++ {
		if err := ledger.Release(ctx, e.st.Q(), e.nextID, uid, jobID, e.now); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.balance(uid); got != base-1 {
		t.Fatalf("已核销的任务不得被退款，实际余额 %d（多退了 %d）", got, got-(base-1))
	}

	// 另一条路径：预留后直接 release，重复调用只退一次。
	job2 := "job-cr-2"
	if err := ledger.Reserve(ctx, e.st.Q(), e.nextID, uid, job2, 1, e.now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := ledger.Release(ctx, e.st.Q(), e.nextID, uid, job2, e.now); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.balance(uid); got != base-1 {
		t.Fatalf("重复释放只应退一次，实际 %d", got)
	}
}
