package play

import (
	"fmt"
	"testing"
	"time"
)

// internal/play 此前**一个测试都没有** —— 而它正是决定「这笔钱算不算有效购买」
// 的地方。下面三组用例钉住三条与 Node 版（server/verify.js:210-226）对齐的判断。

var tNow = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

func subBody(paymentState int, expiry time.Time) []byte {
	return []byte(fmt.Sprintf(`{"orderId":"GPA.1","paymentState":%d,"expiryTimeMillis":"%d"}`,
		paymentState, expiry.UnixMilli()))
}

// TestSubscriptionExpiryIsEnforced 🔴 过期订阅必须判为无效。
//
// 修复前 expiryTimeMillis 被解析出来却从来不参与判断，于是一个早就过期、
// Google 已停止计费的订阅 token 照样验成 valid，finalizePurchase 会按商品的
// granted_units 白发一次额度 —— 真金白银流出去，且运营看不到套餐变化。
func TestSubscriptionExpiryIsEnforced(t *testing.T) {
	cases := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"已过期 1 天", tNow.Add(-24 * time.Hour), false},
		{"刚过期 1 毫秒", tNow.Add(-time.Millisecond), false},
		{"正好等于 now（不算有效）", tNow, false},
		{"还有 1 毫秒", tNow.Add(time.Millisecond), true},
		{"还有 30 天", tNow.Add(30 * 24 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := parsePurchase("subscription", subBody(1, tc.expiry), tNow)
			if err != nil {
				t.Fatal(err)
			}
			if res.Valid != tc.want {
				t.Fatalf("expiry=%s valid 应为 %v，实得 %v", tc.expiry, tc.want, res.Valid)
			}
		})
	}
}

// TestSubscriptionPaymentStateMatchesNode 🔴 paymentState 必须接受 {1,2,3}。
//
// Node 版注释原话：只认 1 会把每一个免费试用用户和每一个改套餐的用户都拒掉，
// 而这些人在 Google 看来是完全有权益的。Go 第一版只认 {1,2}，把 3 拒掉了 ——
// 而 !Valid 会让订单被标成 invalid，补偿扫描只重试 pending，于是这一拒是永久的：
// 一个正在改套餐的付费用户彻底失去权益。
func TestSubscriptionPaymentStateMatchesNode(t *testing.T) {
	future := tNow.Add(30 * 24 * time.Hour)
	cases := []struct {
		state int
		want  bool
		why   string
	}{
		{0, false, "待付款，Google 还没收到钱"},
		{1, true, "已付款"},
		{2, true, "免费试用期 —— Google 认为有权益"},
		{3, true, "延期升降级待生效 —— 改套餐的付费用户，Google 认为有权益"},
		{4, false, "未知状态一律不放行"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("paymentState=%d", tc.state), func(t *testing.T) {
			res, err := parsePurchase("subscription", subBody(tc.state, future), tNow)
			if err != nil {
				t.Fatal(err)
			}
			if res.Valid != tc.want {
				t.Fatalf("%s：valid 应为 %v，实得 %v", tc.why, tc.want, res.Valid)
			}
		})
	}
}

// TestSubscriptionMissingExpiryIsInvalid 没有 expiryTimeMillis 的订阅不能放行
// （宁可让它留在 pending 重试，也不要凭空发额度）。
func TestSubscriptionMissingExpiryIsInvalid(t *testing.T) {
	res, err := parsePurchase("subscription", []byte(`{"orderId":"GPA.1","paymentState":1}`), tNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatal("🔴 缺 expiryTimeMillis 的订阅被判为有效")
	}
}

// TestOneTimePurchaseUnaffectedByExpiry 一次性商品只看 purchaseState，
// 不受有效期影响（它本来就没有 expiryTimeMillis）。
func TestOneTimePurchaseUnaffectedByExpiry(t *testing.T) {
	for state, want := range map[int]bool{0: true, 1: false, 2: false} {
		body := []byte(fmt.Sprintf(`{"orderId":"GPA.1","purchaseState":%d}`, state))
		res, err := parsePurchase("product", body, tNow)
		if err != nil {
			t.Fatal(err)
		}
		if res.Valid != want {
			t.Fatalf("purchaseState=%d valid 应为 %v，实得 %v", state, want, res.Valid)
		}
		if res.ExpiresAt != nil {
			t.Fatalf("一次性商品不该有 expiresAt，实得 %v", res.ExpiresAt)
		}
	}
}
