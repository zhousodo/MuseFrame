package waffo

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func pkixPEM(t *testing.T, k any) string {
	t.Helper()
	b, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}))
}

const sampleEvent = `{
  "id": "PAY_6eYCunG3IMmIgcQOnaXdoA",
  "timestamp": "2026-03-10T08:30:00.000Z",
  "eventType": "order.completed",
  "eventId": "PAY_6eYCunG3IMmIgcQOnaXdoA",
  "storeId": "STO_3bVzrkD0FJjFdZNLk8Ualx",
  "storeName": "My Store",
  "mode": "prod",
  "data": {
    "orderId": "ORD_5dXBtmF2HLlHfbPNm0Wcnz",
    "orderStatus": "completed",
    "buyerEmail": "customer@example.com",
    "currency": "USD",
    "orderMetadata": { "purchaseId": "p1", "userId": "u1", "n": 3 },
    "productMetadata": {},
    "orderMerchantExternalId": "p1",
    "chargedAmount": "98.00",
    "amount": "98.00",
    "listPrice": { "total": "108.00" },
    "productName": "Pro Plan",
    "paymentId": "PAY_6eYCunG3IMmIgcQOnaXdoA",
    "paymentStatus": "succeeded",
    "paymentDate": "2026-03-10"
  }
}`

// TestVerifyWebhook 正例 / 篡改正文 / 换钥 / 过期 / 头格式坏 / 无钥。
func TestVerifyWebhook(t *testing.T) {
	k := genKey(t)
	pub, err := ParsePublicKey(pkixPEM(t, &k.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(sampleEvent)
	hdr, err := SignWebhook(body, k, tNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hdr, "t=") || !strings.Contains(hdr, ",v1=") {
		t.Fatalf("签名头格式不对: %q", hdr)
	}

	if err := VerifyWebhook(body, hdr, pub, tNow); err != nil {
		t.Fatalf("正例应通过: %v", err)
	}
	// 重试在 31+ 分钟后原样重放同一个头：44 分钟内必须仍然通过。
	if err := VerifyWebhook(body, hdr, pub, tNow.Add(44*time.Minute)); err != nil {
		t.Fatalf("44 分钟内的重放应通过: %v", err)
	}
	if err := VerifyWebhook(body, hdr, pub, tNow.Add(46*time.Minute)); err != ErrWebhookExpired {
		t.Fatalf("46 分钟应为过期，实得 %v", err)
	}
	if err := VerifyWebhook(body, hdr, pub, tNow.Add(-46*time.Minute)); err != ErrWebhookExpired {
		t.Fatalf("来自未来 46 分钟也应为过期，实得 %v", err)
	}
	tampered := []byte(strings.Replace(sampleEvent, `"98.00"`, `"0.01"`, 1))
	if err := VerifyWebhook(tampered, hdr, pub, tNow); err != ErrWebhookBadSig {
		t.Fatalf("篡改正文应为坏签名，实得 %v", err)
	}
	other := genKey(t)
	otherPub, _ := ParsePublicKey(pkixPEM(t, &other.PublicKey))
	if err := VerifyWebhook(body, hdr, otherPub, tNow); err != ErrWebhookBadSig {
		t.Fatalf("换钥（测试钥验生产事件）应为坏签名，实得 %v", err)
	}
	for _, bad := range []string{"", "t=123", "v1=abc", "t=abc,v1=abc", "t=1,v1=%%%"} {
		if err := VerifyWebhook(body, bad, pub, tNow); err != ErrWebhookBadHeader {
			t.Errorf("头 %q 应为 BadHeader，实得 %v", bad, err)
		}
	}
	if err := VerifyWebhook(body, hdr, nil, tNow); err != ErrWebhookNoKey {
		t.Fatalf("无钥应为 NoKey，实得 %v", err)
	}
	// 键序无关、带空格也行。
	parts := strings.SplitN(hdr, ",", 2)
	swapped := parts[1] + " , " + parts[0]
	if err := VerifyWebhook(body, swapped, pub, tNow); err != nil {
		t.Fatalf("交换键序应通过: %v", err)
	}
}

// TestParsePublicKeyEscaped `\n` 转义的单行公钥也要能读（与私钥同一运维写法）。
func TestParsePublicKeyEscaped(t *testing.T) {
	k := genKey(t)
	escaped := strings.ReplaceAll(pkixPEM(t, &k.PublicKey), "\n", `\n`)
	pub, err := ParsePublicKey(escaped)
	if err != nil {
		t.Fatal(err)
	}
	if pub.N.Cmp(k.N) != 0 {
		t.Fatal("不是同一把公钥")
	}
	if _, err := ParsePublicKey(""); err != ErrWebhookNoKey {
		t.Fatalf("空值应为 NoKey，实得 %v", err)
	}
}

// TestParseEvent 载荷解析：metadata 里的非字符串值被忽略而不是让整条失败；
// 缺 id 的事件拒绝（无法去重）。
func TestParseEvent(t *testing.T) {
	ev, err := ParseEvent([]byte(sampleEvent))
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "PAY_6eYCunG3IMmIgcQOnaXdoA" || ev.EventType != EventOrderCompleted || ev.Mode != "prod" {
		t.Fatalf("外壳解析不对: %+v", ev)
	}
	d := ev.Data
	if d.OrderID != "ORD_5dXBtmF2HLlHfbPNm0Wcnz" || d.OrderMerchantExternalID != "p1" || d.Currency != "USD" {
		t.Fatalf("data 解析不对: %+v", d)
	}
	if d.OrderMetadata["purchaseId"] != "p1" || d.OrderMetadata["userId"] != "u1" {
		t.Fatalf("orderMetadata 不对: %v", d.OrderMetadata)
	}
	if _, ok := d.OrderMetadata["n"]; ok {
		t.Fatal("非字符串 metadata 值应被丢弃")
	}
	if n, ok := d.ChargedMinor(); !ok || n != 9800 {
		t.Fatalf("chargedAmount 应为 9800，实得 %d %v", n, ok)
	}
	if _, err := ParseEvent([]byte(`{"eventType":"order.completed","data":{}}`)); err == nil {
		t.Fatal("缺 id 应拒绝")
	}
	if _, err := ParseEvent([]byte(`not json`)); err == nil {
		t.Fatal("非 JSON 应拒绝")
	}

	// chargedAmount 缺失 → 回落 amount；两个都没有 → false。
	noCharged := EventData{Amount: "29.00", Currency: "USD"}
	if n, ok := noCharged.ChargedMinor(); !ok || n != 2900 {
		t.Fatalf("回落 amount 失败: %d %v", n, ok)
	}
	if _, ok := (EventData{Currency: "USD"}).ChargedMinor(); ok {
		t.Fatal("无金额应为 false")
	}
}

func TestParseDate(t *testing.T) {
	d, ok := ParseDate("2026-05-10")
	if !ok || !d.Equal(time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("日期解析不对: %v %v", d, ok)
	}
	ts, ok := ParseDate("2026-03-25T11:02:00.000Z")
	if !ok || !ts.Equal(time.Date(2026, 3, 25, 11, 2, 0, 0, time.UTC)) {
		t.Fatalf("时间戳解析不对: %v %v", ts, ok)
	}
	if _, ok := ParseDate(""); ok {
		t.Fatal("空串应为 false")
	}
	if _, ok := ParseDate("2026-13-40"); ok {
		t.Fatal("非法日期应为 false")
	}
}

// TestEmbeddedProdWebhookKeyParses 🔴 内置的生产验签公钥必须可解析且是 2048 位 RSA。
// 抄错一个字符的后果是生产 webhook 全部 401、钱收了额度不发。
func TestEmbeddedProdWebhookKeyParses(t *testing.T) {
	pub, err := ParsePublicKey(ProdWebhookPublicKeyPEM)
	if err != nil {
		t.Fatalf("内置生产公钥解析失败: %v", err)
	}
	if pub.N.BitLen() != 2048 {
		t.Fatalf("内置生产公钥应为 2048 位，实得 %d", pub.N.BitLen())
	}
	if DefaultWebhookPublicKeyPEM("prod") != ProdWebhookPublicKeyPEM || DefaultWebhookPublicKeyPEM("PROD ") != ProdWebhookPublicKeyPEM {
		t.Fatal("prod 应返回内置公钥")
	}
	if DefaultWebhookPublicKeyPEM("test") != "" || DefaultWebhookPublicKeyPEM("") != "" {
		t.Fatal("非 prod 不该有内置公钥")
	}
}
