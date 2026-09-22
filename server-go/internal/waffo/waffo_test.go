package waffo

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var tNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pkcs1PEM(k *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func pkcs8PEM(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
}

// TestParsePrivateKeyFormats 🔴 PKCS1 / PKCS8 / `\n` 转义单行三种写法都要能读。
// 运维把 PEM 塞进 app.env 时最常见的就是压成一行 —— 读不了等于支付整条不可用。
func TestParsePrivateKeyFormats(t *testing.T) {
	k := genKey(t)
	cases := map[string]string{
		"pkcs1":         pkcs1PEM(k),
		"pkcs8":         pkcs8PEM(t, k),
		"pkcs1-escaped": strings.ReplaceAll(pkcs1PEM(k), "\n", `\n`),
		"pkcs8-escaped": strings.ReplaceAll(pkcs8PEM(t, k), "\n", `\n`),
	}
	for name, pemStr := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParsePrivateKey(pemStr)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got.N.Cmp(k.N) != 0 {
				t.Fatal("解析出的密钥不是同一把")
			}
		})
	}
	if _, err := ParsePrivateKey(""); err != ErrNotConfigured {
		t.Fatalf("空值应为 ErrNotConfigured，实得 %v", err)
	}
	if _, err := ParsePrivateKey("not a pem"); err == nil {
		t.Fatal("垃圾输入应报错")
	}
	if New(Config{MerchantID: "MER_x", PrivateKeyPEM: "garbage"}).Configured() {
		t.Fatal("私钥不可解析时 Configured 应为 false")
	}
	if New(Config{MerchantID: "", PrivateKeyPEM: pkcs1PEM(k)}).Configured() {
		t.Fatal("商户号缺失时 Configured 应为 false")
	}
	if !New(Config{MerchantID: "MER_x", PrivateKeyPEM: pkcs1PEM(k)}).Configured() {
		t.Fatal("齐全时 Configured 应为 true")
	}
}

// TestCanonicalRequestAndSignature 钉住文档里的签名算法：
// canonical = METHOD\nPATH\nTS\nbase64(sha256(body))，签名 = RSA-SHA256 PKCS1v15。
func TestCanonicalRequestAndSignature(t *testing.T) {
	body := []byte(`{"name":"My Store"}`)
	sum := sha256.Sum256(body)
	want := "POST\n/v1/actions/store/create-store\n1705312200\n" + base64.StdEncoding.EncodeToString(sum[:])
	got := CanonicalRequest("POST", "/v1/actions/store/create-store", 1705312200, body)
	if got != want {
		t.Fatalf("canonical 不一致:\n%q\n%q", got, want)
	}
	k := genKey(t)
	sig, err := Sign(k, got)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(got))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, h[:], raw); err != nil {
		t.Fatalf("公钥验签失败: %v", err)
	}
}

// fakeWaffo 是一个会用公钥验签的假 Waffo：断言请求头齐全、签名对得上、
// 参与签名的正文与实际收到的字节一致。
func fakeWaffo(t *testing.T, pub *rsa.PublicKey, merchant string, handler func(path string, body []byte) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Merchant-Id") != merchant {
			t.Errorf("X-Merchant-Id 不对: %q", r.Header.Get("X-Merchant-Id"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type 不对: %q", r.Header.Get("Content-Type"))
		}
		ts, err := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
		if err != nil {
			t.Errorf("X-Timestamp 不是整数秒: %q", r.Header.Get("X-Timestamp"))
		}
		sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Signature"))
		if err != nil {
			t.Errorf("X-Signature 不是 base64")
		}
		canonical := CanonicalRequest(r.Method, r.URL.Path, ts, body)
		h := sha256.Sum256([]byte(canonical))
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig); err != nil {
			t.Errorf("签名验不过（正文与哈希不一致？）")
		}
		status, resp := handler(r.URL.Path, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
}

func TestCreateCheckoutSessionRoundTrip(t *testing.T) {
	k := genKey(t)
	var gotBody map[string]any
	srv := fakeWaffo(t, &k.PublicKey, "MER_test", func(path string, body []byte) (int, string) {
		if path != "/v1/actions/checkout/create-session" {
			return 404, `{"data":null,"errors":[{"message":"no route"}]}`
		}
		_ = json.Unmarshal(body, &gotBody)
		return 200, `{"data":{"sessionId":"cs_1","checkoutUrl":"https://pancake.waffo.ai/store/x/checkout/cs_1","expiresAt":"2026-09-23T12:45:00.000Z"}}`
	})
	defer srv.Close()
	c := New(Config{MerchantID: "MER_test", PrivateKeyPEM: pkcs8PEM(t, k), BaseURL: srv.URL, Now: func() time.Time { return tNow }})
	out, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		ProductID: "PROD_1", Currency: "USD", BuyerEmail: "a@b.c", SuccessURL: "https://x/app?checkout=success",
		Metadata: map[string]string{"purchaseId": "p1"}, OrderMerchantExternalID: "p1", Language: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "cs_1" || !strings.HasPrefix(out.CheckoutURL, "https://") || out.ExpiresAt == "" {
		t.Fatalf("出参不对: %+v", out)
	}
	for _, k := range []string{"productId", "currency", "buyerEmail", "successUrl", "metadata", "orderMerchantExternalId", "language"} {
		if _, ok := gotBody[k]; !ok {
			t.Errorf("请求体缺 %s", k)
		}
	}
	if _, ok := gotBody["expiresInSeconds"]; ok {
		t.Error("未设置的 expiresInSeconds 不该出现在请求体里")
	}
}

// TestAPIErrorAndUnavailable 4xx 是业务错误（带原话），5xx 是 ErrUnavailable，
// 未配置时根本不出网。
func TestAPIErrorAndUnavailable(t *testing.T) {
	k := genKey(t)
	srv := fakeWaffo(t, &k.PublicKey, "MER_test", func(path string, _ []byte) (int, string) {
		switch path {
		case "/v1/actions/subscription-order/cancel-order":
			return 400, `{"data":null,"errors":[{"message":"Subscription cannot be canceled, current status: canceled","layer":"service"}]}`
		case "/v1/actions/onetime-product/publish-product":
			return 400, `{"data":null,"errors":[{"message":"Already published to production"}]}`
		default:
			return 502, `bad gateway`
		}
	})
	defer srv.Close()
	c := New(Config{MerchantID: "MER_test", PrivateKeyPEM: pkcs1PEM(k), BaseURL: srv.URL})

	_, err := c.CancelSubscription(context.Background(), "ORD_1")
	e, ok := IsAPIError(err)
	if !ok || e.Status != 400 || !strings.Contains(e.Message, "cannot be canceled") {
		t.Fatalf("应为 400 业务错误，实得 %v", err)
	}
	_, err = c.PublishProduct(context.Background(), "PROD_1", false)
	if !IsAlreadyPublished(err) {
		t.Fatalf("「已发布」应被识别为空操作，实得 %v", err)
	}
	_, err = c.CreateOnetimeProduct(context.Background(), OnetimeProductRequest{})
	if err != ErrUnavailable {
		t.Fatalf("5xx 应为 ErrUnavailable，实得 %v", err)
	}

	unconfigured := New(Config{})
	if _, err := unconfigured.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{}); err != ErrNotConfigured {
		t.Fatalf("未配置应为 ErrNotConfigured，实得 %v", err)
	}
}

func TestPublishProductPathsBySubscription(t *testing.T) {
	k := genKey(t)
	var paths []string
	srv := fakeWaffo(t, &k.PublicKey, "MER_test", func(path string, _ []byte) (int, string) {
		paths = append(paths, path)
		return 200, `{"data":{"product":{"id":"PROD_1","status":"active"}}}`
	})
	defer srv.Close()
	c := New(Config{MerchantID: "MER_test", PrivateKeyPEM: pkcs1PEM(k), BaseURL: srv.URL})
	if _, err := c.PublishProduct(context.Background(), "PROD_1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PublishProduct(context.Background(), "PROD_1", true); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/actions/onetime-product/publish-product" || paths[1] != "/v1/actions/subscription-product/publish-product" {
		t.Fatalf("发布路径不对: %v", paths)
	}
}

// TestAmountConversion 🔴 金额只走字符串，JPY 零小数位；多余精度直接拒绝。
func TestAmountConversion(t *testing.T) {
	cases := []struct {
		display, cur string
		want         int64
		bad          bool
	}{
		{"29.00", "USD", 2900, false},
		{"4.99", "USD", 499, false},
		{"100", "USD", 10000, false},
		{"0.50", "EUR", 50, false},
		{"29.000", "USD", 2900, false},
		{"1000", "JPY", 1000, false},
		{"1000.00", "JPY", 1000, false},
		{"29.001", "USD", 0, true},
		{"12.5x", "USD", 0, true},
		{"", "USD", 0, true},
		{"-10.00", "USD", -1000, false},
	}
	for _, tc := range cases {
		got, err := ParseAmountMinor(tc.display, tc.cur)
		if tc.bad {
			if err == nil {
				t.Errorf("%q %s 应报错", tc.display, tc.cur)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q %s: 应 %d，实得 %d (%v)", tc.display, tc.cur, tc.want, got, err)
		}
	}
	for _, tc := range []struct {
		minor int64
		cur   string
		want  string
	}{
		{499, "USD", "4.99"}, {2900, "CNY", "29.00"}, {5, "USD", "0.05"}, {1000, "JPY", "1000"}, {0, "USD", "0.00"},
	} {
		if got := FormatAmount(tc.minor, tc.cur); got != tc.want {
			t.Errorf("FormatAmount(%d,%s) = %q，应 %q", tc.minor, tc.cur, got, tc.want)
		}
	}
}
