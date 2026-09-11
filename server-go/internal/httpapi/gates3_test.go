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
		NewID: e.nextID, Now: e.clock,
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
//
// 🔴 2026-09-12 起这条有**两道闸**，两道都要测：
//   - 写闸：PUT 一个越界值直接 422（cfgstore 的 numRanges 区间校验）。
//     夹一下再存是更糟的选择 —— 页面显示「已保存」，而生效的是另一个数。
//   - 读闸：库里**已经躺着**的越界行（Node 版写下的 / 手动 SQL 改的）仍要被夹。
//     写闸管不住历史数据，所以读闸不能撤。这里用 UpsertAppConfig 直接绕过写闸
//     塞脏值，模拟的就是那种历史行。
func TestCheck12_WorkerConcurrencyClamped(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	wk := worker.New(worker.Options{Store: e.st, Runtime: e.rt, Logger: newNopLogger(), NewID: e.nextID, Now: e.clock})

	// 写闸：越界必须被拒，且**不得**落库。
	for _, bad := range []int{0, -5, 99} {
		if err := e.rt.Set(ctx, "worker_concurrency", bad); err == nil {
			t.Errorf("🔴 worker_concurrency=%d 越界却被接受（设 0 会让队列静默冻结且额度仍被预留）", bad)
		}
	}
	if got := wk.Concurrency(); got != 3 {
		t.Errorf("被拒的写不该改变生效值，应仍是默认 3，实际 %d", got)
	}
	// 区间内的值照常生效。
	for _, c := range []struct{ set, want int }{{1, 1}, {3, 3}, {8, 8}} {
		if err := e.rt.Set(ctx, "worker_concurrency", c.set); err != nil {
			t.Fatal(err)
		}
		if got := wk.Concurrency(); got != c.want {
			t.Errorf("worker_concurrency=%d 应生效为 %d，实际 %d", c.set, c.want, got)
		}
	}
	// 读闸：绕过 Set 往库里塞历史越界行，Reload 之后仍必须被夹。
	for _, c := range []struct {
		raw  string
		want int
	}{{"0", 1}, {"-5", 1}, {"99", 8}, {"不是数字", 3}} {
		if err := e.st.UpsertAppConfig(ctx, "worker_concurrency", c.raw); err != nil {
			t.Fatal(err)
		}
		if err := e.rt.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if got := wk.Concurrency(); got != c.want {
			t.Errorf("库里的历史脏值 %q 应被夹到 %d，实际 %d", c.raw, c.want, got)
		}
	}
}

// TestOTPLimitsAreHotConfigurable 🔴 2026-09-12：OTP 的两个上限此前是
// public_email.go 里两个**裸 5**。
//
//	刷码攻击是分钟级的，而「把每窗口签发次数压到 1」在改之前需要改代码 + 交叉编译
//	+ 推镜像 + 重启容器。现在它们是注册表热键（email_code_max_issues_per_window /
//	email_code_max_attempts，区间 1..20 / 1..10），改完下一个请求立即生效。
//	这条测试钉死的就是「改了之后真的变了」—— 光有配置项而消费方还读着字面量，
//	是比没有配置项更坏的状态（后台显示已保存，攻击照旧）。
func TestOTPLimitsAreHotConfigurable(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()

	// ① 每窗口签发次数：压到 2，第 3 次必须 429。
	//    （/v1/auth/email/request 的 per-IP 限流是 5 次 / 10 分钟，这里只打 3 次，
	//     所以命中的 429 只可能来自签发上限这条闸，不含糊。）
	if err := e.rt.Set(ctx, "email_code_max_issues_per_window", 2); err != nil {
		t.Fatal(err)
	}
	const addr = "otp-limit@example.com"
	for i := 1; i <= 2; i++ {
		if r := e.do("POST", "/v1/auth/email/request", map[string]any{"email": addr}, nil); r.Code != 200 {
			t.Fatalf("第 %d 次签发应 200，实际 %d %s", i, r.Code, r.Body)
		}
	}
	r := e.do("POST", "/v1/auth/email/request", map[string]any{"email": addr}, nil)
	if r.Code != 429 {
		t.Fatalf("🔴 签发上限已调到 2，第 3 次必须 429（否则消费方还在读字面量 5），实际 %d %s", r.Code, r.Body)
	}

	// ② 同一个码的猜测次数：压到 1，猜错一次之后连**正确的码**也必须被锁。
	if err := e.rt.Set(ctx, "email_code_max_attempts", 1); err != nil {
		t.Fatal(err)
	}
	const addr2 = "otp-attempts@example.com"
	if r := e.do("POST", "/v1/auth/email/request", map[string]any{"email": addr2}, nil); r.Code != 200 {
		t.Fatalf("签发应 200，实际 %d %s", r.Code, r.Body)
	}
	good := e.mail.lastCode
	wrong := "000000"
	if good == wrong {
		wrong = "111111"
	}
	if r := e.do("POST", "/v1/auth/email/verify",
		map[string]any{"email": addr2, "code": wrong}, nil); r.Code != 422 {
		t.Fatalf("猜错一次应 422 CODE_INVALID，实际 %d %s", r.Code, r.Body)
	}
	r = e.do("POST", "/v1/auth/email/verify", map[string]any{"email": addr2, "code": good}, nil)
	if r.Code != 429 {
		t.Fatalf("🔴 猜测上限已调到 1，第 2 次（哪怕码是对的）必须 429 锁死，实际 %d %s", r.Code, r.Body)
	}

	// ③ 放宽到 5 之后，同一个码在猜错一次后仍然可用 —— 证明读的是配置而不是缓存。
	if err := e.rt.Set(ctx, "email_code_max_attempts", 5); err != nil {
		t.Fatal(err)
	}
	const addr3 = "otp-relaxed@example.com"
	if r := e.do("POST", "/v1/auth/email/request", map[string]any{"email": addr3}, nil); r.Code != 200 {
		t.Fatalf("签发应 200，实际 %d %s", r.Code, r.Body)
	}
	good3 := e.mail.lastCode
	if r := e.do("POST", "/v1/auth/email/verify",
		map[string]any{"email": addr3, "code": "000001"}, nil); r.Code != 422 {
		t.Fatalf("猜错一次应 422，实际 %d %s", r.Code, r.Body)
	}
	if r := e.do("POST", "/v1/auth/email/verify",
		map[string]any{"email": addr3, "code": good3}, nil); r.Code != 200 {
		t.Fatalf("上限放宽到 5 后，猜错一次不该锁死，实际 %d %s", r.Code, r.Body)
	}
}

// TestStorageQuotaIsHotConfigurable 🔴 每账号存储上限从 config（env-only、
// 改一次要重启整个容器）挪到注册表热键之后，必须证明**它仍然拦得住**。
//
//	这条是「改了读取来源」这类改动唯一有意义的回归：上限本身很好测
//	（把它压到一张图之下，上传必须 413），而一旦接错（比如读成 0 或读成 int32
//	溢出的负数），现象是所有上传全部 413，或者所有上传都不再受限 —— 两种都要能照出来。
func TestStorageQuotaIsHotConfigurable(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	_, tok := e.signUp("quota@example.com")
	img := fakeJPEG(4096)

	// 上限压到下界 1 MiB：4 KiB 的图应当照常上传成功。
	if err := e.rt.Set(ctx, "max_user_storage_bytes", 1<<20); err != nil {
		t.Fatal(err)
	}
	if code, body := e.putRaw("/v1/assets/"+e.newIntent(tok)+"/upload", img, tok); code != 200 {
		t.Fatalf("1 MiB 上限下 4 KiB 的图应上传成功，实际 %d %s", code, body)
	}

	// 直接往库里塞一个越界小值（绕过写闸，模拟历史脏行）：读闸必须把它夹到 1 MiB，
	// 而不是让它变成「0 字节上限 = 所有上传全 413」。
	if err := e.st.UpsertAppConfig(ctx, "max_user_storage_bytes", "0"); err != nil {
		t.Fatal(err)
	}
	if err := e.rt.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.rt.MaxUserStorageBytes(); got != 1<<20 {
		t.Fatalf("🔴 库里的 0 应被夹到下界 1 MiB，实际 %d —— 0 会让所有上传全部 413", got)
	}

	// 把上限压到「比已用量还小」：下一张图必须 413 STORAGE_QUOTA_EXCEEDED。
	// 1 MiB 是下界，所以这里靠「已用 4 KiB + 再传 4 KiB」凑不过 1 MiB——
	// 改用把图放大到超过上限本身。
	big := fakeJPEG(2 << 20) // 2 MiB > 1 MiB 上限
	code, body := e.putRaw("/v1/assets/"+e.newIntent(tok)+"/upload", big, tok)
	if code != 413 {
		t.Fatalf("🔴 超过每账号上限必须 413（否则一个账号能把数据盘写满），实际 %d %s", code, body)
	}
	if !strings.Contains(string(body), "STORAGE_QUOTA_EXCEEDED") {
		t.Errorf("错误码应为 STORAGE_QUOTA_EXCEEDED，实际 %s", body)
	}
}
