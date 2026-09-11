package httpapi

import (
	"testing"

	"museframe-api/internal/store"
)

// 校验点 1：游客令牌永不解锁账号数据、图片、余额、生成。
func TestCheck01_GuestTokenRejectedEverywhere(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	// 直接造一个游客账号与会话（allow_guest=false，走不到 exchange）。
	if err := store.CreateUser(ctx, e.st.Q(), "guest-1", nil, true, "en", e.now); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, e.st.Q(), "guesttoken", "guest-1", nil, e.now, 90); err != nil {
		t.Fatal(err)
	}
	h := bearer("guesttoken")
	cases := []struct{ method, path string }{
		{"GET", "/v1/projects"},
		{"GET", "/v1/entitlements/me"},
		{"GET", "/v1/purchases"},
		{"GET", "/v1/assets/img-token"},
		{"GET", "/v1/assets/abc/file"},
		{"GET", "/v1/assets/abc/analysis"},
		{"POST", "/v1/projects"},
		{"POST", "/v1/assets/upload-intents"},
		{"POST", "/v1/generation-jobs"},
		{"POST", "/v1/purchases/verify"},
		{"GET", "/v1/generation-jobs/abc"},
	}
	for _, c := range cases {
		var body any
		if c.method != "GET" {
			body = map[string]any{}
		}
		r := e.do(c.method, c.path, body, h)
		if r.Code != 401 {
			t.Errorf("%s %s 用游客令牌应 401，实际 %d %s", c.method, c.path, r.Code, r.Body)
		}
	}
	// 负向：同一批路由用正式账号令牌不应是 401，否则上面的断言毫无意义。
	_, tok := e.signUp("a@example.com")
	if r := e.do("GET", "/v1/entitlements/me", nil, bearer(tok)); r.Code != 200 {
		t.Fatalf("正式账号应能读 entitlements，实际 %d %s", r.Code, r.Body)
	}
}

// 校验点 3：UNIQUE (user_id, reference_key) 必须是**数据库级**唯一约束。
func TestCheck03_LedgerUniqueIsDBConstraint(t *testing.T) {
	e := newTestEnv(t)
	var n int
	err := e.st.Pool().QueryRow(nil2ctx(), `
		SELECT count(*) FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage k
		  ON k.constraint_name = tc.constraint_name AND k.table_name = tc.table_name
		WHERE tc.table_name='credit_ledger' AND tc.constraint_type='UNIQUE'
		  AND k.column_name IN ('user_id','reference_key')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("credit_ledger 上必须有 UNIQUE(user_id, reference_key)，实际命中 %d 列", n)
	}
	// 实打实撞一次，确认约束真的会拦。
	uid, _ := e.signUp("uniq@example.com")
	e.grantUnits(uid, 1)
	ctx := nil2ctx()
	bucketID := e.nextID()
	if err := store.InsertBucket(ctx, e.st.Q(), bucketID, uid, "manual", nil, 1, nil, e.now); err != nil {
		t.Fatal(err)
	}
	ref := "grant:manual:dup-key"
	if err := store.InsertLedger(ctx, e.st.Q(), e.nextID(), uid, "grant", 1, bucketID, nil, nil, ref, e.now); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertLedger(ctx, e.st.Q(), e.nextID(), uid, "grant", 1, bucketID, nil, nil, ref, e.now); err == nil {
		t.Fatal("重复 reference_key 必须被数据库拒绝 —— 否则重复发放")
	}
}

// 校验点 4：发放四道闸的六个分支各造一次，断言拒绝原因。
func TestCheck04_FreeGrantGates(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	dev := "device-abc"

	mk := func(id string) string {
		if err := store.CreateUser(ctx, e.st.Q(), id, nil, false, "en", e.now); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 闸 0：free_units <= 0
	if err := e.rt.Set(ctx, "free_units", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u0"), false, &dev, "1.1.1.1"); got != OutcomeFreeUnitsZero {
		t.Errorf("free_units=0 应为 FREE_UNITS_ZERO，实际 %s", got)
	}
	if err := e.rt.Set(ctx, "free_units", 3); err != nil {
		t.Fatal(err)
	}

	// 闸 0'：游客 + free_requires_auth
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u1"), true, &dev, "1.1.1.1"); got != OutcomeRequiresAuth {
		t.Errorf("游客 + free_requires_auth 应为 REQUIRES_AUTH，实际 %s", got)
	}

	// 闸 1：游客无 deviceId（🔴 绝不回落 user id）
	if err := e.rt.Set(ctx, "free_requires_auth", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u2"), true, nil, "1.1.1.1"); got != OutcomeNoDeviceID {
		t.Errorf("游客无设备指纹应为 NO_DEVICE_ID，实际 %s", got)
	}
	if err := e.rt.Set(ctx, "free_requires_auth", true); err != nil {
		t.Fatal(err)
	}

	// 正常发放一次（作为后续 ALREADY_CLAIMED 的前提）。
	u3 := mk("u3")
	if got, err := e.app.MaybeGrantFree(ctx, e.st.Q(), u3, false, &dev, "1.1.1.1"); err != nil || got != OutcomeGranted {
		t.Fatalf("正常路径应发放，实际 %s %v", got, err)
	}

	// 闸 2：同一设备换账号再领
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u4"), false, &dev, "1.1.1.1"); got != OutcomeAlreadyClaimed {
		t.Errorf("同一设备再领应为 ALREADY_CLAIMED，实际 %s", got)
	}
	// 闸 2 另一个方向：同一账号换设备再领
	dev2 := "device-xyz"
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), u3, false, &dev2, "1.1.1.1"); got != OutcomeAlreadyClaimed {
		t.Errorf("同一账号换设备再领应为 ALREADY_CLAIMED，实际 %s", got)
	}

	// 闸 3：per-IP 24h 上限（已发 1 次，把上限压到 1）
	if err := e.rt.Set(ctx, "free_grants_per_ip_day", 1); err != nil {
		t.Fatal(err)
	}
	d5 := "device-5"
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u5"), false, &d5, "1.1.1.1"); got != OutcomeIPCap {
		t.Errorf("per-IP 上限应为 IP_CAP，实际 %s", got)
	}
	if err := e.rt.Set(ctx, "free_grants_per_ip_day", 3); err != nil {
		t.Fatal(err)
	}

	// 闸 4：全站 24h 熔断（换 IP 也挡住）
	if err := e.rt.Set(ctx, "free_grants_per_day", 1); err != nil {
		t.Fatal(err)
	}
	d6 := "device-6"
	if got, _ := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u6"), false, &d6, "9.9.9.9"); got != OutcomeSiteCap {
		t.Errorf("全站上限应为 SITE_CAP（攻击者轮换 IP 的总熔断），实际 %s", got)
	}
	if err := e.rt.Set(ctx, "free_grants_per_day", 50); err != nil {
		t.Fatal(err)
	}

	// 负向：把闸都松开后必须能发 —— 否则上面全是假绿。
	d7 := "device-7"
	if got, err := e.app.MaybeGrantFree(ctx, e.st.Q(), mk("u7"), false, &d7, "8.8.8.8"); got != OutcomeGranted {
		t.Fatalf("闸放开后应能发放，实际 %s %v", got, err)
	}
}

// 校验点 5：发放时每个去重键各写一行，且只有主键行带 units；24h 计数带 units > 0。
func TestCheck05_FreeGrantWritesOneRowPerKey(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	if err := store.CreateUser(ctx, e.st.Q(), "ux", nil, false, "en", e.now); err != nil {
		t.Fatal(err)
	}
	dev := "device-multi"
	if got, err := e.app.MaybeGrantFree(ctx, e.st.Q(), "ux", false, &dev, "2.2.2.2"); err != nil || got != OutcomeGranted {
		t.Fatalf("应发放，实际 %s %v", got, err)
	}
	var rows, withUnits int
	if err := e.st.Pool().QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE units > 0) FROM free_grants WHERE user_id='ux'`).
		Scan(&rows, &withUnits); err != nil {
		t.Fatal(err)
	}
	// 两个键：dedupeId(=userId，非游客) 与 deviceHash。
	if rows != 2 {
		t.Fatalf("应对每个去重键各写一行（user id + 设备哈希），实际 %d 行", rows)
	}
	if withUnits != 1 {
		t.Fatalf("只有主键行带 units，其余是 units=0 的占位行，实际 %d 行带 units", withUnits)
	}
	// 24h 计数只算 units > 0 的行，否则占位行会把上限吃光。
	w, err := store.GetFreeGrantWindow(ctx, e.st.Q(), nil, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Today != 1 {
		t.Fatalf("24h 计数必须带 units > 0（应为 1），实际 %d", w.Today)
	}
}
