package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

// seedFailedJob 造一个失败任务（带完整的 reserve + release 台账，
// 也就是 worker.failJob 跑完之后的真实状态）。
func (e *testEnv) seedFailedJob(email string, units int) (userID, jobID, projectID string) {
	e.t.Helper()
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs(email, units)
	jobID = "job-failed"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "queued", Stage: "preparing",
		Controls:      []byte(`{"strength":"balanced"}`),
		Output:        []byte(`{"aspectRatio":"4:5","qualityTier":"standard"}`),
		ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		e.t.Fatal(err)
	}
	// reserve（建任务时那一笔）+ release（失败时退回那一笔），和生产一致。
	if err := e.st.InTx(ctx, func(q store.Queryer) error {
		if err := ledger.Reserve(ctx, q, e.nextID, uid, jobID, 1, e.now); err != nil {
			return err
		}
		return ledger.Release(ctx, q, e.nextID, uid, jobID, e.now)
	}); err != nil {
		e.t.Fatal(err)
	}
	if err := store.FinishJobFailed(ctx, e.st.Q(), jobID, "PROVIDER_ERROR", e.now); err != nil {
		e.t.Fatal(err)
	}
	return uid, jobID, pid
}

// 🔴 这条测试是整个重试入口存在的理由。
//
// 重试**必须新建一条任务**，不能把原任务改回 queued 再入队。原因在额度台账：
// 失败时 ledger.Release 写的是补偿分录，原来的 reserve 行还留在表里。于是重排原任务时
//
//	ledger.Reserve   看到 `job:<id>:reserve` 前缀已存在 -> 直接 return nil（不扣）
//	LedgerHasReserve 看到 reserve 行还在              -> 放行执行
//
// 两个守卫都通过，而钱在失败那一刻已经退给用户了 —— 净结果是一张免费的图，
// 而且额度对账表上看不出任何异常。
func TestJobRetryCreatesNewJobAndChargesAgain(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, jobID, _ := e.seedFailedJob("retry@example.com", 2)

	before, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		OK             bool   `json:"ok"`
		JobID          string `json:"jobId"`
		ParentJobID    string `json:"parentJobId"`
		ReservedUnits  int    `json:"reservedUnits"`
		AvailableUnits int    `json:"availableUnits"`
		AuditLogged    bool   `json:"auditLogged"`
	}
	r := e.do("POST", "/v1/admin/jobs/"+jobID+"/retry", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("重试应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)

	// 新任务，不是原来那一条。
	if out.JobID == jobID {
		t.Fatalf("重试必须新建任务，不能复用原 id %q", jobID)
	}
	if out.ParentJobID != jobID {
		t.Errorf("新任务的 parentJobId 应当指向被重试的那一条，实际 %q", out.ParentJobID)
	}
	if out.ReservedUnits != 1 {
		t.Errorf("应当重新预留 1 份，实际 %d", out.ReservedUnits)
	}

	// 🔴 变异验证：把 hAdminJobRetry 改成「SetJobStage(queued) + Enqueue 原任务」，
	// 这一行会红 —— 余额不会变（Reserve 命中已存在的前缀直接 return nil），
	// 而那张图会照样生成出来。这就是那张免费图的探针。
	after, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if after != before-1 {
		t.Errorf("重试应当重新扣 1 份额度：之前 %d，之后 %d", before, after)
	}
	if out.AvailableUnits != after {
		t.Errorf("出参的 availableUnits（%d）应当等于实际余额（%d）", out.AvailableUnits, after)
	}

	// 新任务自己有一笔 reserve —— 没有它 worker 会拒绝执行。
	has, err := store.LedgerHasReserve(ctx, e.st.Q(), out.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Errorf("新任务必须有自己的 reserve 分录，否则 worker 会判 INTERNAL_ERROR")
	}

	// 原任务保持 failed（重试不是「把失败抹掉」）。
	old, err := store.GetJob(ctx, e.st.Q(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "failed" {
		t.Errorf("原任务应当仍是 failed，实际 %q", old.Status)
	}

	// 新任务排队中，控制参数照抄。
	fresh, err := store.GetJob(ctx, e.st.Q(), out.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "queued" {
		t.Errorf("新任务应当是 queued，实际 %q", fresh.Status)
	}
	// jsonb 不保留原始字节（PG 会重排键并在冒号后加空格），所以按**语义**比。
	var freshCtl, oldCtl map[string]any
	if err := json.Unmarshal(fresh.Controls, &freshCtl); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(old.Controls, &oldCtl); err != nil {
		t.Fatal(err)
	}
	if len(freshCtl) == 0 || freshCtl["strength"] != oldCtl["strength"] {
		t.Errorf("新任务应当沿用原来的生成参数：原 %v，新 %v", oldCtl, freshCtl)
	}

	// 审计 + 列表里的「被重试次数」。
	if !out.AuditLogged {
		t.Errorf("重试应当留一条审计")
	}
	var audit struct {
		Audit []struct {
			Action string         `json:"action"`
			Props  map[string]any `json:"props"`
		} `json:"audit"`
	}
	e.do("GET", "/v1/admin/audit", nil, e.admin()).JSON(t, &audit)
	found := false
	for _, a := range audit.Audit {
		if a.Action == "job_retry" && a.Props["jobId"] == jobID && a.Props["newJobId"] == out.JobID {
			found = true
		}
	}
	if !found {
		t.Errorf("审计里应当有一条 job_retry，实际 %+v", audit.Audit)
	}

	var jobs struct {
		Jobs []struct {
			ID         string `json:"id"`
			RetryCount int    `json:"retryCount"`
		} `json:"jobs"`
	}
	e.do("GET", "/v1/admin/jobs", nil, e.admin()).JSON(t, &jobs)
	for _, j := range jobs.Jobs {
		if j.ID == jobID && j.RetryCount != 1 {
			t.Errorf("原任务的被重试次数应当是 1，实际 %d", j.RetryCount)
		}
	}
}

// 余额不够必须拒绝并说清怎么办，绝不能悄悄跳过扣减。
func TestJobRetryRefusesWithoutBalance(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	// units=1：注册送的会被按掉，只有这 1 份；它在 seedFailedJob 里被 reserve
	// 然后 release 回来，所以此刻余额恰好 1 —— 先把它花掉。
	uid, jobID, _ := e.seedFailedJob("nobalance@example.com", 1)
	bal, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if bal > 0 {
		// 用一个不相关的 jobID 把余额吃光。
		if err := e.st.InTx(ctx, func(q store.Queryer) error {
			return ledger.Reserve(ctx, q, e.nextID, uid, "job-drain", bal, e.now)
		}); err != nil {
			t.Fatal(err)
		}
	}

	r := e.do("POST", "/v1/admin/jobs/"+jobID+"/retry", nil, e.admin())
	// 🔴 变异验证：在 hAdminJobRetry 里把 `if units > 0` 改成 `if false`
	// （跳过预留），这行会变成 200 —— 而那就是一张免费的图。
	if r.Code != 409 {
		t.Fatalf("余额不足应当 409，实际 %d: %s", r.Code, r.Body)
	}
	body := string(r.Body)
	if !strings.Contains(body, "INSUFFICIENT_ENTITLEMENT") {
		t.Errorf("错误码应当是 INSUFFICIENT_ENTITLEMENT，实际 %s", body)
	}
	// 拒绝之后不该留下半个任务（整笔事务回滚）。
	var jobs struct {
		Jobs []struct{ ID string } `json:"jobs"`
	}
	e.do("GET", "/v1/admin/jobs", nil, e.admin()).JSON(t, &jobs)
	if len(jobs.Jobs) != 1 {
		t.Errorf("拒绝后不该多出任务行，实际 %d 条：%+v", len(jobs.Jobs), jobs.Jobs)
	}
}

// 只有终态的 failed / cancelled 能重试。
func TestJobRetryRejectsNonTerminalJobs(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("running@example.com", 3)
	for _, st := range []string{"queued", "running", "succeeded"} {
		id := "job-" + st
		if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
			ID: id, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
			Status: st, Stage: "preparing", Controls: []byte(`{}`), Output: []byte(`{}`),
			ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
		}); err != nil {
			t.Fatal(err)
		}
		r := e.do("POST", "/v1/admin/jobs/"+id+"/retry", nil, e.admin())
		if r.Code != 409 {
			t.Errorf("状态 %s 的任务重试应当 409，实际 %d: %s", st, r.Code, r.Body)
		}
	}
	if r := e.do("POST", "/v1/admin/jobs/no-such-job/retry", nil, e.admin()); r.Code != 404 {
		t.Errorf("未知任务应当 404，实际 %d", r.Code)
	}
}

// 取消的任务也能重试（用户取消后反悔，或运维批量取消后重放）。
func TestJobRetryAcceptsCancelledJob(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("cancelled@example.com", 3)
	id := "job-cancelled"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: id, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "cancelled", Stage: "failed", Controls: []byte(`{}`), Output: []byte(`{}`),
		ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	if r := e.do("POST", "/v1/admin/jobs/"+id+"/retry", nil, e.admin()); r.Code != 200 {
		t.Fatalf("cancelled 任务应当可重试，实际 %d: %s", r.Code, r.Body)
	}
}

// ---- 购买重验 --------------------------------------------------------------

// 重验的真实用途：一笔 verified 的购买漏入账了（钱收了、额度没发），
// 第一次重验补上，第二次重验是幂等的（不会再多发一轮）。
//
// 🔴 这条测试顺带钉住 purchases.status 的词汇表是 **verified**，不是 active。
// 判据最初写的是 status != "active"，那会让每一笔真实购买都被拒成 409 ——
// 一个点了没反应的按钮，而漏入账的单子永远补不上。
func TestPurchaseReverifyBackfillsThenIsIdempotent(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _ := e.signUp("buyer@example.com")

	// 造一笔 verified 但**没有任何额度入账**的购买 —— 也就是那起事故的现场。
	pid := e.nextID()
	if _, err := e.st.Q().Exec(ctx, `
		INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id,
		                       status, amount_minor, currency, purchased_at, created_at)
		SELECT $1, $2, p.id, 'web', 'tx-missed-credit', 'verified', p.price_minor, p.currency, $3, $3
		FROM products p WHERE p.internal_key = 'pack_10'`, pid, uid, e.now); err != nil {
		t.Fatal(err)
	}

	// 后台列表必须把这笔事故显示出来：verified 而入账额度 0。
	var list struct {
		Purchases []struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			Platform     string `json:"platform"`
			TxID         string `json:"txId"`
			AmountMinor  *int64 `json:"amountMinor"`
			UnitsGranted int    `json:"unitsGranted"`
		} `json:"purchases"`
	}
	e.do("GET", "/v1/admin/purchases", nil, e.admin()).JSON(t, &list)
	if len(list.Purchases) != 1 {
		t.Fatalf("应当有 1 笔购买，实际 %d", len(list.Purchases))
	}
	p := list.Purchases[0]
	// 审计矩阵要求的四列：平台 / 交易号 / 状态 / 金额。
	if p.Platform != "web" || p.TxID != "tx-missed-credit" || p.Status != "verified" || p.AmountMinor == nil {
		t.Errorf("购买行应当带平台 / 交易号 / 状态 / 金额，实际 %+v", p)
	}
	// 🔴 变异验证：把 ListAdminPurchasesFull 里那条算 unitsGranted 的子查询
	// 去掉（或改成常量），这行红 —— 而「钱收了、额度没发」就只剩人工翻台账能发现。
	if p.UnitsGranted != 0 {
		t.Fatalf("这笔构造的事故单应当入账 0 份，实际 %d", p.UnitsGranted)
	}

	before, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}

	// 第一次重验：补发。
	var first struct {
		OK          bool   `json:"ok"`
		Granted     int    `json:"granted"`
		UnitsBefore int    `json:"unitsBefore"`
		UnitsAfter  int    `json:"unitsAfter"`
		Message     string `json:"message"`
		AuditLogged bool   `json:"auditLogged"`
	}
	r := e.do("POST", "/v1/admin/purchases/"+pid+"/reverify", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("重验 verified 购买应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &first)
	if first.Granted <= 0 {
		t.Fatalf("漏入账的单子重验应当补发额度，实际 %d", first.Granted)
	}
	if first.UnitsBefore != before {
		t.Errorf("unitsBefore（%d）应当等于重验前余额（%d）", first.UnitsBefore, before)
	}
	mid, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if mid != before+first.Granted {
		t.Errorf("余额应当增加 %d：之前 %d，之后 %d", first.Granted, before, mid)
	}
	if !first.AuditLogged {
		t.Errorf("重验应当留一条审计")
	}

	// 列表里的入账额度跟着变。
	e.do("GET", "/v1/admin/purchases", nil, e.admin()).JSON(t, &list)
	if list.Purchases[0].UnitsGranted != first.Granted {
		t.Errorf("列表的入账额度应当变成 %d，实际 %d", first.Granted, list.Purchases[0].UnitsGranted)
	}

	// 第二次重验：幂等，一份都不多发。
	var second struct {
		Granted int    `json:"granted"`
		Message string `json:"message"`
	}
	r2 := e.do("POST", "/v1/admin/purchases/"+pid+"/reverify", nil, e.admin())
	if r2.Code != 200 {
		t.Fatalf("第二次重验应当 200，实际 %d: %s", r2.Code, r2.Body)
	}
	r2.JSON(t, &second)
	// 🔴 变异验证：把 finalizePurchase 换成一条无条件发放，
	// 这行红 —— 每点一次「重验」就白送一轮额度。
	if second.Granted != 0 {
		t.Errorf("第二次重验不该再发额度，实际补发了 %d", second.Granted)
	}
	end, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if end != mid {
		t.Errorf("幂等命中时余额不该变：之前 %d，之后 %d", mid, end)
	}
	// 「额度没变」是正常结果，必须说出来 —— 否则运营会把 granted:0 读成失败然后一直点。
	if second.Message == "" {
		t.Errorf("granted=0 时必须给一句解释，否则会被读成重验失败")
	}
}

func TestPurchaseReverifyRejectsUnknownAndPending(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	if r := e.do("POST", "/v1/admin/purchases/nope/reverify", nil, e.admin()); r.Code != 404 {
		t.Errorf("未知购买应当 404，实际 %d", r.Code)
	}
	// 一笔 pending 的单子：还没核验过，补额度等于白送。
	uid, _ := e.signUp("pendingbuyer@example.com")
	pid := e.nextID()
	if _, err := e.st.Q().Exec(ctx, `
		INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id,
		                       status, purchased_at, created_at)
		SELECT $1, $2, p.id, 'google', 'tx-pending', 'pending', $3, $3
		FROM products p WHERE p.internal_key = 'pack_10'`, pid, uid, e.now); err != nil {
		t.Fatal(err)
	}
	r := e.do("POST", "/v1/admin/purchases/"+pid+"/reverify", nil, e.admin())
	// 🔴 变异验证：删掉 hAdminPurchaseReverify 里的状态判据，
	// 这行会变成 200 并给一笔还没核验的单子发额度。
	if r.Code != 409 {
		t.Fatalf("pending 购买重验应当 409，实际 %d: %s", r.Code, r.Body)
	}
	before, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	e.do("POST", "/v1/admin/purchases/"+pid+"/reverify", nil, e.admin())
	after, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("被拒的重验不该动额度：之前 %d，之后 %d", before, after)
	}
}

// ---- 任务视图筛选 ----------------------------------------------------------

func TestAdminJobsFilters(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("jobfilter@example.com", 5)
	mk := func(id, status string) {
		if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
			ID: id, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
			Status: status, Stage: "preparing", Controls: []byte(`{}`), Output: []byte(`{}`),
			ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("j-ok", "succeeded")
	mk("j-bad1", "failed")
	mk("j-bad2", "failed")

	var all struct {
		Note         string                `json:"note"`
		Jobs         []struct{ ID string } `json:"jobs"`
		StatusCounts map[string]int        `json:"statusCounts"`
		MaxAttempts  int                   `json:"maxAttempts"`
	}
	r := e.do("GET", "/v1/admin/jobs", nil, e.admin())
	r.JSON(t, &all)
	if all.Note == "" {
		t.Errorf("视图说明不能空")
	}
	if len(all.Jobs) != 3 {
		t.Errorf("不筛应当 3 条，实际 %d", len(all.Jobs))
	}
	if all.StatusCounts["failed"] != 2 || all.StatusCounts["succeeded"] != 1 {
		t.Errorf("状态分布应当 failed=2 / succeeded=1，实际 %+v", all.StatusCounts)
	}
	if all.MaxAttempts <= 0 {
		t.Errorf("maxAttempts 应当回生效值，实际 %d", all.MaxAttempts)
	}

	var failed struct {
		Jobs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	e.do("GET", "/v1/admin/jobs?status=failed", nil, e.admin()).JSON(t, &failed)
	if len(failed.Jobs) != 2 {
		t.Fatalf("status=failed 应当 2 条，实际 %d", len(failed.Jobs))
	}
	for _, j := range failed.Jobs {
		if j.Status != "failed" {
			t.Errorf("筛后不该出现 %q", j.Status)
		}
	}

	// 未知 status 当「不筛」，不是 422。
	var bogus struct {
		Jobs []struct{ ID string } `json:"jobs"`
	}
	rr := e.do("GET", "/v1/admin/jobs?status=succeded", nil, e.admin())
	if rr.Code != 200 {
		t.Fatalf("拼错的 status 应当 200（不筛），实际 %d", rr.Code)
	}
	rr.JSON(t, &bogus)
	if len(bogus.Jobs) != 3 {
		t.Errorf("拼错的 status 应当不筛，实际 %d 条", len(bogus.Jobs))
	}

	// 时间窗：sinceHours=1 在固定时钟下仍然含全部（都是 e.now 创建的）。
	var recent struct {
		Jobs   []struct{ ID string } `json:"jobs"`
		Filter struct {
			SinceHours int `json:"sinceHours"`
		} `json:"filter"`
	}
	e.do("GET", "/v1/admin/jobs?sinceHours=1", nil, e.admin()).JSON(t, &recent)
	if recent.Filter.SinceHours != 1 {
		t.Errorf("filter 应当回显 sinceHours=1，实际 %d", recent.Filter.SinceHours)
	}
	if len(recent.Jobs) != 3 {
		t.Errorf("固定时钟下 1 小时窗口应含全部 3 条，实际 %d", len(recent.Jobs))
	}
}
