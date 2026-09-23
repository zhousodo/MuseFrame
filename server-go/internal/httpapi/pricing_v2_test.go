package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"museframe-api/internal/logx"
	"museframe-api/internal/store"
	"museframe-api/internal/waffo"
)

// 价目表 v2（2026-09-23，迁移 007）的集成测试：真 PostgreSQL + 假 Waffo 收银台 + 自签 webhook。
//
// 目录用**真迁移文件**铺：先走 seedCatalog 的旧目录（与改价前的生产一致），再把
// migrations/007_pricing_v2.sql 的 DML 原样执行一遍 —— 断言的是「迁移跑完之后线上长什么样」，
// 而不是测试里另抄一份价目表。

// applyPricingV2 执行 007 的 DML（去掉 BEGIN/COMMIT 与 ALTER TABLE：列由建测试库时的迁移建好，
// 测试连接可能只是 museframe_app，没有 DDL 权限），再补上 006 的四个 Waffo 映射
// （seedCatalog 里这四行的 waffo_product_id 是 NULL）。
func (e *testEnv) applyPricingV2() {
	e.t.Helper()
	raw, err := os.ReadFile("../../migrations/007_pricing_v2.sql")
	if err != nil {
		e.t.Fatal(err)
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		l := strings.TrimSpace(line)
		if l == "BEGIN;" || l == "COMMIT;" || strings.HasPrefix(l, "ALTER TABLE") {
			continue
		}
		keep = append(keep, line)
	}
	ctx := context.Background()
	if _, err := e.st.Pool().Exec(ctx, strings.Join(keep, "\n")); err != nil {
		e.t.Fatalf("执行 007 失败: %v", err)
	}
	for key, id := range map[string]string{
		"pack_10": "PROD_p10", "pack_30": "PROD_p30", "pack_100": "PROD_p100", "creator_monthly": "PROD_mo",
	} {
		if _, err := e.st.Pool().Exec(ctx, `UPDATE products SET waffo_product_id = $1 WHERE internal_key = $2`, id, key); err != nil {
			e.t.Fatal(err)
		}
	}
}

// fakeCashier 是假的 Waffo 建会话接口：记下每一次请求，回一个固定会话。
type fakeCashier struct {
	srv  *httptest.Server
	reqs []waffo.CheckoutSessionRequest
}

// withWaffo 把 e.app 换成一个接好 Waffo 的 App（同一个库 / 运行时 / 时钟）。
// 返回 webhook 签名私钥与假收银台。
func (e *testEnv) withWaffo() (*rsa.PrivateKey, *fakeCashier) {
	e.t.Helper()
	fc := &fakeCashier{}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req waffo.CheckoutSessionRequest
		_ = json.Unmarshal(body, &req)
		fc.reqs = append(fc.reqs, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"sessionId":"cs_1","checkoutUrl":"https://checkout.example/cs_1","expiresAt":"2026-09-12T00:00:00.000Z"}}`))
	}))
	e.t.Cleanup(fc.srv.Close)
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		e.t.Fatal(err)
	}
	hookKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := *e.cfg
	cfg.WaffoMode = "prod"
	cfg.WaffoStoreID = "STO_test"
	cfg.WaffoSuccessURL = "https://museframe.example/app?checkout=success"
	client := waffo.New(waffo.Config{
		MerchantID: "MER_test", PrivateKeyPEM: string(pemEncodePKCS1(merchantKey)), BaseURL: fc.srv.URL,
		Now: e.clock,
	})
	e.app = New(Options{
		Config: &cfg, Runtime: e.rt, Store: e.st, Logger: logx.NewWith(discardWriter{}, nil), Provider: e.prov,
		Worker: e.wk, Mailer: e.mail, Version: "test", ImgTokenKey: []byte("test-img-hmac-key"),
		Now: e.clock, NewID: e.nextID, Waffo: client, WaffoWebhookKey: &hookKey.PublicKey,
	})
	return hookKey, fc
}

// sendWaffoEvent 签名并投递一条 webhook，断言 200。
func (e *testEnv) sendWaffoEvent(key *rsa.PrivateKey, eventType, id string, data map[string]any) {
	e.t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "timestamp": "2026-09-11T04:26:12.396Z", "eventType": eventType, "eventId": id,
		"storeId": "STO_test", "storeName": "s", "mode": "prod", "data": data,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	sig, err := waffo.SignWebhook(body, key, e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/waffo", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Waffo-Event", eventType)
	req.Header.Set("X-Waffo-Signature", sig)
	w := httptest.NewRecorder()
	e.app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		e.t.Fatalf("webhook %s 应 200，实得 %d %s", eventType, w.Code, w.Body.String())
	}
	var note *string
	if err := e.st.Pool().QueryRow(context.Background(), `SELECT error FROM webhook_events WHERE id = $1`, id).Scan(&note); err != nil {
		e.t.Fatal(err)
	}
	if note != nil {
		e.t.Fatalf("webhook %s 记了业务异常: %s", eventType, *note)
	}
}

// checkout 调 POST /v1/purchases/web/checkout，返回响应。
func (e *testEnv) checkout(tok, key, currency string) resp {
	e.t.Helper()
	body := map[string]any{"productKey": key}
	if currency != "" {
		body["currency"] = currency
	}
	return e.do("POST", "/v1/purchases/web/checkout", body, bearer(tok))
}

// TestPricingV2Catalogue 迁移 007 之后的 GET /v1/products：7 行、既定行序、新价、
// oneTime / platforms 两个追加字段。
func TestPricingV2Catalogue(t *testing.T) {
	e := newTestEnv(t)
	e.applyPricingV2()
	var out struct {
		Products []map[string]json.RawMessage `json:"products"`
	}
	e.do("GET", "/v1/products", nil, nil).JSON(t, &out)
	want := []struct {
		key, typ, units, minor, cny, oneTime, platforms, period string
	}{
		{"trial_3", `"pack"`, "3", "199", "990", "false", `["web"]`, "null"},
		{"pack_10", `"pack"`, "10", "599", "1990", "false", `["web","android","ios"]`, "null"},
		{"pack_30", `"pack"`, "30", "1299", "4900", "false", `["web","android","ios"]`, "null"},
		{"pack_100", `"pack"`, "100", "3499", "12900", "false", `["web","android","ios"]`, "null"},
		{"creator_pass_30", `"subscription"`, "30", "799", "3900", "true", `["web"]`, `"month"`},
		{"creator_monthly", `"subscription"`, "30", "799", "4900", "false", `["web","android","ios"]`, `"month"`},
		{"creator_annual", `"subscription"`, "360", "5999", "null", "false", `["web","android","ios"]`, `"year"`},
	}
	if len(out.Products) != len(want) {
		t.Fatalf("上架商品应为 %d 个，实际 %d", len(want), len(out.Products))
	}
	for i, w := range want {
		p := out.Products[i]
		got := []string{string(p["internalKey"]), string(p["productType"]), string(p["grantedUnits"]), string(p["priceMinor"]),
			string(p["priceCnyMinor"]), string(p["oneTime"]), string(p["platforms"]), string(p["period"])}
		exp := []string{`"` + w.key + `"`, w.typ, w.units, w.minor, w.cny, w.oneTime, w.platforms, w.period}
		for j := range got {
			if got[j] != exp[j] {
				t.Errorf("第 %d 行（%s）字段 %d：应 %s，实际 %s", i, w.key, j, exp[j], got[j])
			}
		}
	}
	// 中文名：两个新商品 + 年订。
	zh := map[string]string{}
	for _, p := range out.Products {
		var k, n string
		_ = json.Unmarshal(p["internalKey"], &k)
		_ = json.Unmarshal(p["displayNameZh"], &n)
		zh[k] = n
	}
	for k, n := range map[string]string{"trial_3": "3 张体验包", "creator_pass_30": "Creator 30 天通行证", "creator_annual": "Creator 年订"} {
		if zh[k] != n {
			t.Errorf("%s.displayNameZh 应为 %q，实际 %q", k, n, zh[k])
		}
	}
	// 迁移重跑是幂等的：不多出行、不改价。
	e.applyPricingV2()
	var again struct {
		Products []map[string]json.RawMessage `json:"products"`
	}
	e.do("GET", "/v1/products", nil, nil).JSON(t, &again)
	if len(again.Products) != len(want) {
		t.Fatalf("重跑 007 后上架商品应仍为 %d 个，实际 %d", len(want), len(again.Products))
	}
}

// TestPricingV2CheckoutCurrencyRules 网页端币种规则。
func TestPricingV2CheckoutCurrencyRules(t *testing.T) {
	e := newTestEnv(t)
	e.applyPricingV2()
	_, fc := e.withWaffo()
	_, tok := e.signUp("cur@example.com")

	cases := []struct {
		key, currency string
		status        int
		msg           string
	}{
		{"creator_pass_30", "USD", 422, "This product is only sold in CNY."},
		{"creator_pass_30", "", 422, "This product is only sold in CNY."}, // 缺省币种 = USD
		{"creator_monthly", "CNY", 422, "CNY checkout is only available for packs and the 30-day pass."},
		{"creator_annual", "CNY", 422, "CNY checkout is only available for packs and the 30-day pass."},
		{"creator_pass_30", "CNY", 200, ""},
		{"creator_annual", "USD", 200, ""},
		{"creator_monthly", "USD", 200, ""},
		{"pack_30", "CNY", 200, ""},
		{"pack_30", "USD", 200, ""},
		{"trial_3", "CNY", 200, ""},
	}
	for _, c := range cases {
		r := e.checkout(tok, c.key, c.currency)
		if r.Code != c.status {
			t.Fatalf("%s/%s 应 %d，实得 %d %s", c.key, c.currency, c.status, r.Code, r.Body)
		}
		if c.msg != "" && !strings.Contains(string(r.Body), c.msg) {
			t.Fatalf("%s/%s 错误文案应含 %q，实得 %s", c.key, c.currency, c.msg, r.Body)
		}
	}
	// 发到 Waffo 的是新商品映射与本单币种。
	wantProd := map[string]string{"creator_pass_30": "PROD_0kjLl2SI11Y9Cp4R4JntHM", "creator_annual": "PROD_3D1CEZRO5lunet0eHPwHpG", "trial_3": "PROD_1ljuEsEEljhWUDU9ReU9Js"}
	for _, rq := range fc.reqs {
		if id, ok := wantProd[rq.Metadata["productKey"]]; ok && rq.ProductID != id {
			t.Errorf("%s 应映射到 %s，实际 %s", rq.Metadata["productKey"], id, rq.ProductID)
		}
	}
	// pending 行按目录价记金额与币种。
	var amount int64
	var cur string
	if err := e.st.Pool().QueryRow(context.Background(), `SELECT pu.amount_minor, pu.currency FROM purchases pu
		JOIN products p ON p.id = pu.product_id WHERE p.internal_key = 'creator_pass_30' AND pu.status = 'pending'`).Scan(&amount, &cur); err != nil {
		t.Fatal(err)
	}
	if amount != 3900 || cur != "CNY" {
		t.Fatalf("通行证 pending 行应为 3900 CNY，实际 %d %s", amount, cur)
	}
}

// TestPricingV2TrialOncePerAccount 体验包：pending（放弃付款）不算用过；付过一次后 409。
func TestPricingV2TrialOncePerAccount(t *testing.T) {
	e := newTestEnv(t)
	e.applyPricingV2()
	key, _ := e.withWaffo()
	uid, tok := e.signUp("trial@example.com")

	r := e.checkout(tok, "trial_3", "USD")
	if r.Code != 200 {
		t.Fatalf("首次结账应 200，实得 %d %s", r.Code, r.Body)
	}
	var first WebCheckoutResult
	r.JSON(t, &first)
	// 放弃付款后再开一次：仍然放行。
	r = e.checkout(tok, "trial_3", "CNY")
	if r.Code != 200 {
		t.Fatalf("只有 pending 时再次结账应 200，实得 %d %s", r.Code, r.Body)
	}
	before := e.balance(uid)
	e.sendWaffoEvent(key, "order.completed", "PAY_trial_1", map[string]any{
		"orderId": "ORD_trial_1", "currency": "USD", "chargedAmount": "1.99",
		"orderMerchantExternalId": first.PurchaseID,
		"orderMetadata":           map[string]any{"productKey": "trial_3", "userId": uid, "purchaseId": first.PurchaseID},
	})
	if got := e.balance(uid) - before; got != 3 {
		t.Fatalf("体验包应入账 3 张，实际 %d", got)
	}
	r = e.checkout(tok, "trial_3", "USD")
	if r.Code != 409 || errCodeOf(t, r) != "TRIAL_ALREADY_USED" {
		t.Fatalf("已买过体验包应 409 TRIAL_ALREADY_USED，实得 %d %s", r.Code, r.Body)
	}
	// 别的包不受影响；别的账号也不受影响。
	if r := e.checkout(tok, "pack_10", "USD"); r.Code != 200 {
		t.Fatalf("pack_10 应不受体验包限购影响，实得 %d %s", r.Code, r.Body)
	}
	_, tok2 := e.signUp("trial2@example.com")
	if r := e.checkout(tok2, "trial_3", "USD"); r.Code != 200 {
		t.Fatalf("另一个账号应能买体验包，实得 %d %s", r.Code, r.Body)
	}
}

// TestPricingV2PassOrderCompleted 🔴 一次性通行证走 order.completed：
// verified + expires_at = +30 天、30 张随之到期、期间 plan = creator（premium + 高清），
// 且**不**出现在「管理 / 取消订阅」里。
func TestPricingV2PassOrderCompleted(t *testing.T) {
	e := newTestEnv(t)
	e.applyPricingV2()
	key, _ := e.withWaffo()
	uid, tok := e.signUp("pass@example.com")

	r := e.checkout(tok, "creator_pass_30", "CNY")
	if r.Code != 200 {
		t.Fatalf("通行证结账应 200，实得 %d %s", r.Code, r.Body)
	}
	var co WebCheckoutResult
	r.JSON(t, &co)
	before := e.balance(uid)
	e.sendWaffoEvent(key, "order.completed", "PAY_pass_1", map[string]any{
		"orderId": "ORD_pass_1", "currency": "CNY", "chargedAmount": "39.00",
		"orderMerchantExternalId": co.PurchaseID,
		"orderMetadata":           map[string]any{"productKey": "creator_pass_30", "userId": uid, "purchaseId": co.PurchaseID},
	})

	p, err := store.GetPurchaseByExternal(context.Background(), e.st.Q(), "waffo", co.PurchaseID)
	if err != nil {
		t.Fatal(err)
	}
	wantExp := e.now.Add(30 * 24 * time.Hour)
	if p.Status != "verified" || p.ExpiresAt == nil || !p.ExpiresAt.Equal(wantExp) {
		t.Fatalf("通行证应 verified 且 expires_at = +30 天（%s），实际 %s %v", wantExp, p.Status, p.ExpiresAt)
	}
	if p.AmountMinor == nil || *p.AmountMinor != 3900 || p.Currency == nil || *p.Currency != "CNY" {
		t.Fatalf("实收应记 3900 CNY，实际 %v %v", p.AmountMinor, p.Currency)
	}
	if got := e.balance(uid) - before; got != 30 {
		t.Fatalf("通行证应入账 30 张，实际 %d", got)
	}
	var bucketExp *time.Time
	if err := e.st.Pool().QueryRow(context.Background(),
		`SELECT expires_at FROM credit_buckets WHERE user_id = $1 AND source_id = $2`, uid, co.PurchaseID).Scan(&bucketExp); err != nil {
		t.Fatal(err)
	}
	if bucketExp == nil || !bucketExp.Equal(wantExp) {
		t.Fatalf("通行证额度应随通行证到期（%s），实际 %v", wantExp, bucketExp)
	}

	var ent Entitlements
	e.do("GET", "/v1/entitlements/me", nil, bearer(tok)).JSON(t, &ent)
	if ent.Plan != "creator_pass_30" || !ent.Features.PremiumStyles || !ent.Features.HighResolution {
		t.Fatalf("通行证期间应为 Creator（premium + 高清），实际 %+v", ent)
	}
	// 不是可取消的订阅：取消接口 404，订单历史标 oneTime。
	if r := e.do("POST", "/v1/purchases/web/subscription/cancel", map[string]any{}, bearer(tok)); r.Code != 404 {
		t.Fatalf("通行证不应能被「取消订阅」，实得 %d %s", r.Code, r.Body)
	}
	var hist struct {
		Purchases []PurchaseItem `json:"purchases"`
	}
	e.do("GET", "/v1/purchases", nil, bearer(tok)).JSON(t, &hist)
	found := false
	for _, h := range hist.Purchases {
		if h.ID == co.PurchaseID {
			found = true
			if !h.OneTime || h.ProductType != "subscription" {
				t.Fatalf("订单历史里通行证应 oneTime=true / subscription，实际 %+v", h)
			}
		}
	}
	if !found {
		t.Fatal("订单历史里没有通行证")
	}
	// 重投同一事件：不重复发放。
	e.sendWaffoEvent(key, "order.completed", "PAY_pass_1", map[string]any{})
	if got := e.balance(uid) - before; got != 30 {
		t.Fatalf("重投后仍应是 30 张，实际 %d", got)
	}
	// 31 天后：回到 free，额度随之过期。
	e.now = e.now.Add(31 * 24 * time.Hour)
	e.do("GET", "/v1/entitlements/me", nil, bearer(tok)).JSON(t, &ent)
	if ent.Plan != "free" || ent.Features.PremiumStyles {
		t.Fatalf("通行证过期后应回到 free，实际 %+v", ent)
	}
	if got := e.balance(uid); got != before {
		t.Fatalf("通行证过期后其 30 张应一并过期（余额回到 %d），实际 %d", before, got)
	}
}

// TestPricingV2AnnualActivated 年订 subscription.activated（billingPeriod=yearly）：
// expires_at = currentPeriodEnd + 48h，发 360 张（按周期键，随周期到期），
// 同周期的 renewed 重复到达不重复发，下一期再发一次。
func TestPricingV2AnnualActivated(t *testing.T) {
	e := newTestEnv(t)
	e.applyPricingV2()
	key, _ := e.withWaffo()
	uid, tok := e.signUp("annual@example.com")

	r := e.checkout(tok, "creator_annual", "USD")
	if r.Code != 200 {
		t.Fatalf("年订结账应 200，实得 %d %s", r.Code, r.Body)
	}
	var co WebCheckoutResult
	r.JSON(t, &co)
	before := e.balance(uid)
	meta := map[string]any{"productKey": "creator_annual", "userId": uid, "purchaseId": co.PurchaseID}
	e.sendWaffoEvent(key, "subscription.activated", "SUB_act_1", map[string]any{
		"orderId": "ORD_sub_1", "currency": "USD", "chargedAmount": "59.99", "billingPeriod": "yearly",
		"currentPeriodStart": "2026-09-11", "currentPeriodEnd": "2027-09-11",
		"orderMerchantExternalId": co.PurchaseID, "orderMetadata": meta,
	})
	p, err := store.GetPurchaseByExternal(context.Background(), e.st.Q(), "waffo", co.PurchaseID)
	if err != nil {
		t.Fatal(err)
	}
	wantExp := time.Date(2027, 9, 13, 0, 0, 0, 0, time.UTC)
	if p.Status != "verified" || p.ExpiresAt == nil || !p.ExpiresAt.Equal(wantExp) {
		t.Fatalf("年订应 verified 且 expires_at = 2027-09-11 + 48h，实际 %s %v", p.Status, p.ExpiresAt)
	}
	if got := e.balance(uid) - before; got != 360 {
		t.Fatalf("年订应发 360 张，实际 %d", got)
	}
	var bucketExp *time.Time
	if err := e.st.Pool().QueryRow(context.Background(),
		`SELECT expires_at FROM credit_buckets WHERE user_id = $1 AND source_id = $2`, uid, co.PurchaseID).Scan(&bucketExp); err != nil {
		t.Fatal(err)
	}
	if bucketExp == nil || !bucketExp.Equal(wantExp) {
		t.Fatalf("年订额度应随周期到期（%s），实际 %v", wantExp, bucketExp)
	}
	var ent Entitlements
	e.do("GET", "/v1/entitlements/me", nil, bearer(tok)).JSON(t, &ent)
	if ent.Plan != "creator_annual" || !ent.Features.PremiumStyles {
		t.Fatalf("年订期间应为 Creator，实际 %+v", ent)
	}
	// 年订是可取消的订阅（ActiveWaffoSubscription 找得到它）。
	if _, err := store.ActiveWaffoSubscription(context.Background(), e.st.Q(), uid, e.now); err != nil {
		t.Fatalf("年订应算作有效的网页端订阅: %v", err)
	}
	// 同周期的 renewed 再到一次：不重复发。
	e.sendWaffoEvent(key, "subscription.renewed", "SUB_ren_dup", map[string]any{
		"orderId": "ORD_sub_1", "currency": "USD", "billingPeriod": "yearly",
		"currentPeriodEnd": "2027-09-11", "orderMerchantExternalId": co.PurchaseID, "orderMetadata": meta,
	})
	if got := e.balance(uid) - before; got != 360 {
		t.Fatalf("同周期重复事件不应再发，实际 %d", got)
	}
	// 下一年续费：再发 360，到期推到 2028-09-11 + 48h。
	e.sendWaffoEvent(key, "subscription.renewed", "SUB_ren_2", map[string]any{
		"orderId": "ORD_sub_1", "currency": "USD", "chargedAmount": "59.99", "billingPeriod": "yearly",
		"currentPeriodEnd": "2028-09-11", "orderMerchantExternalId": co.PurchaseID, "orderMetadata": meta,
	})
	p, _ = store.GetPurchaseByExternal(context.Background(), e.st.Q(), "waffo", co.PurchaseID)
	if want := time.Date(2028, 9, 13, 0, 0, 0, 0, time.UTC); p.ExpiresAt == nil || !p.ExpiresAt.Equal(want) {
		t.Fatalf("续费后 expires_at 应为 %s，实际 %v", want, p.ExpiresAt)
	}
	if got := e.balance(uid) - before; got != 720 {
		t.Fatalf("第二期应再发 360（共 720），实际 %d", got)
	}
}

// errCodeOf 取错误信封里的 code。
func errCodeOf(t *testing.T, r resp) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	r.JSON(t, &env)
	return env.Error.Code
}
