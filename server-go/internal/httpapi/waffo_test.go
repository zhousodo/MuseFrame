package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"museframe-api/internal/config"
	"museframe-api/internal/logx"
	"museframe-api/internal/waffo"
)

// 这组测试**不需要 PostgreSQL**：webhook 的验签 / 环境过滤发生在任何 DB 访问之前，
// 而 route() 在没有 Authorization 头时也不会去查会话。这正是要钉住的顺序 ——
// 一条没验过签的回调绝不能碰到数据库。

var waffoTestNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func pemEncodePKCS1(k *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func newWaffoApp(t *testing.T, pub *rsa.PublicKey, mode string) *App {
	t.Helper()
	cfg := &config.Config{
		TrustedProxy: "none", RateLimitMaxKeys: 100, AdminToken: "test-admin", IPHashSalt: "test-salt",
		WaffoMode: mode,
	}
	return New(Options{
		Config: cfg, Logger: logx.NewWith(discardWriter{}, nil), Version: "test",
		Now: func() time.Time { return waffoTestNow }, WaffoWebhookKey: pub,
	})
}

func postWebhook(t *testing.T, a *App, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/waffo", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Waffo-Event", "order.completed")
	if sig != "" {
		req.Header.Set("X-Waffo-Signature", sig)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func webhookBody(mode string) []byte {
	return []byte(`{"id":"PAY_1","timestamp":"2026-09-23T12:00:00.000Z","eventType":"order.completed","eventId":"PAY_1",` +
		`"storeId":"STO_1","storeName":"s","mode":"` + mode + `","data":{"orderId":"ORD_1","currency":"USD","amount":"4.99","orderMerchantExternalId":"p1"}}`)
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是错误信封: %s", w.Body.String())
	}
	return env.Error.Code
}

// TestWaffoWebhookRequiresKey 公钥没配 → 503，不放行。
func TestWaffoWebhookRequiresKey(t *testing.T) {
	a := newWaffoApp(t, nil, "prod")
	w := postWebhook(t, a, webhookBody("prod"), "t=1,v1=AAAA")
	if w.Code != http.StatusServiceUnavailable || errCode(t, w) != "PROVIDER_NOT_CONFIGURED" {
		t.Fatalf("应 503 PROVIDER_NOT_CONFIGURED，实得 %d %s", w.Code, w.Body.String())
	}
}

// TestWaffoWebhookRejectsBadSignature 🔴 无签名 / 坏签名 / 过期 / 篡改正文 → 401，
// 且此时 App 里根本没有 Store（st 为 nil）—— 走到 DB 就会 panic，所以 401 本身
// 就证明了「验签在 DB 之前」。
func TestWaffoWebhookRejectsBadSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a := newWaffoApp(t, &key.PublicKey, "prod")
	body := webhookBody("prod")
	good, err := waffo.SignWebhook(body, key, waffoTestNow)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherSig, _ := waffo.SignWebhook(body, other, waffoTestNow)
	stale, _ := waffo.SignWebhook(body, key, waffoTestNow.Add(-50*time.Minute))

	cases := map[string]struct {
		body []byte
		sig  string
	}{
		"无签名头":     {body, ""},
		"头格式坏":     {body, "nonsense"},
		"别人的钥":     {body, otherSig},
		"过期 50 分钟": {body, stale},
		"正文被改":     {[]byte(strings.Replace(string(body), `"4.99"`, `"0.01"`, 1)), good},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := postWebhook(t, a, c.body, c.sig)
			if w.Code != http.StatusUnauthorized || errCode(t, w) != "AUTH_INVALID" {
				t.Fatalf("应 401 AUTH_INVALID，实得 %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestWaffoWebhookIgnoresOtherMode 签名对、但事件的 mode 与服务不符 → 200 且忽略，
// 不碰 DB（st 为 nil）。测试事件打到生产不能发额度。
func TestWaffoWebhookIgnoresOtherMode(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	a := newWaffoApp(t, &key.PublicKey, "prod")
	body := webhookBody("test")
	sig, _ := waffo.SignWebhook(body, key, waffoTestNow)
	w := postWebhook(t, a, body, sig)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实得 %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["ignored"] != "mode" || out["ok"] != true {
		t.Fatalf("应回 ignored=mode，实得 %s", w.Body.String())
	}
}

// TestWaffoWebhookMalformedPayload 签名对但不是合法事件（缺 id）→ 400，不碰 DB。
func TestWaffoWebhookMalformedPayload(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	a := newWaffoApp(t, &key.PublicKey, "prod")
	body := []byte(`{"eventType":"order.completed","data":{}}`)
	sig, _ := waffo.SignWebhook(body, key, waffoTestNow)
	w := postWebhook(t, a, body, sig)
	if w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION" {
		t.Fatalf("应 400 VALIDATION，实得 %d %s", w.Code, w.Body.String())
	}
}

// TestWaffoWebhookBodyLimit 超过 256 KB 的回调 413（与其他路由同一套 readBody 语义）。
func TestWaffoWebhookBodyLimit(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	a := newWaffoApp(t, &key.PublicKey, "prod")
	big := []byte(`{"id":"x","pad":"` + strings.Repeat("a", 300*1024) + `"}`)
	w := postWebhook(t, a, big, "t=1,v1=AAAA")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("应 413，实得 %d", w.Code)
	}
}

// TestWaffoLanguageMapping users.locale → 收银台语言枚举。
func TestWaffoLanguageMapping(t *testing.T) {
	cases := map[string]string{
		"": "en", "en": "en", "en-US": "en", "fr-FR": "en",
		"zh": "zh-Hans", "zh-CN": "zh-Hans", "zh_CN": "zh-Hans", "zh-Hans-CN": "zh-Hans", "zh-SG": "zh-Hans",
		"zh-TW": "zh-Hant-TW", "zh-Hant": "zh-Hant-TW", "zh-Hant-TW": "zh-Hant-TW", "zh-MO": "zh-Hant-TW",
		"zh-HK": "zh-Hant-HK",
		"ja":    "ja-JP", "ja-JP": "ja-JP", "ko-KR": "ko-KR",
	}
	for in, want := range cases {
		if got := waffoLanguage(in); got != want {
			t.Errorf("waffoLanguage(%q) = %q，应 %q", in, got, want)
		}
	}
}

// TestWaffoReadyNeedsStoreID 商户私钥齐全但店铺号缺失也算未配置（billing.web=false）。
func TestWaffoReadyNeedsStoreID(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemStr := string(pemEncodePKCS1(key))
	client := waffo.New(waffo.Config{MerchantID: "MER_x", PrivateKeyPEM: pemStr})
	if !client.Configured() {
		t.Fatal("客户端本身应为已配置")
	}
	cfg := &config.Config{TrustedProxy: "none", RateLimitMaxKeys: 10, AdminToken: "a", IPHashSalt: "b", WaffoMode: "prod"}
	a := New(Options{Config: cfg, Logger: logx.NewWith(discardWriter{}, nil), Waffo: client})
	if a.waffoReady() {
		t.Fatal("没有 WAFFO_STORE_ID 时不该 ready")
	}
	cfg.WaffoStoreID = "STO_x"
	if !a.waffoReady() {
		t.Fatal("店铺号齐全后应 ready")
	}
	if New(Options{Config: cfg, Logger: logx.NewWith(discardWriter{}, nil)}).waffoReady() {
		t.Fatal("未注入客户端时不该 ready")
	}
}
