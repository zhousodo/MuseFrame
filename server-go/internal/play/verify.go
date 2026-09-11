package play

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

// Verify 校验一笔购买。kind 为 "subscription" 或 "product"。
func (c *Client) Verify(ctx context.Context, productID, purchaseToken, kind string) (*Result, error) {
	if err := AssertToken(purchaseToken, productID); err != nil {
		return nil, err
	}
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	seg := "products"
	if kind == "subscription" {
		seg = "subscriptions"
	}
	api := "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/" +
		url.PathEscape(c.packageName) + "/purchases/" + seg + "/" +
		url.PathEscape(productID) + "/tokens/" + url.PathEscape(purchaseToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, ErrUnavailable
	}
	// 🔴「Play 说不行」与「我们问不到 Play」是两个不同的答案。
	// 只有前者是永久性的 402；超时 / DNS / 5xx 让订单行留在 pending 并回可重试码。
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return &Result{Valid: false}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrUnavailable
	}

	return parsePurchase(kind, body, c.now())
}

// parsePurchase 把 Play 的应答解成 Result —— 纯函数，不碰 HTTP / 服务账号，
// 便于把「什么算有效购买」这条最值钱的判断单独测到。
func parsePurchase(kind string, body []byte, now time.Time) (*Result, error) {
	var raw struct {
		OrderID              string `json:"orderId"`
		PurchaseState        *int   `json:"purchaseState"`
		AcknowledgementState *int   `json:"acknowledgementState"`
		ExpiryTimeMillis     string `json:"expiryTimeMillis"`
		PaymentState         *int   `json:"paymentState"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, ErrUnavailable
	}
	res := &Result{OrderID: raw.OrderID, ExpiresAt: parseMillis(raw.ExpiryTimeMillis)}
	if raw.AcknowledgementState != nil {
		res.AcknowledgementState = *raw.AcknowledgementState
	}
	if kind == "subscription" {
		// paymentState: 0 待付款 · 1 已付款 · 2 免费试用期 · 3 延期升降级待生效。
		//
		// 🔴 必须含 3。Node 版（server/verify.js:218-222）的注释写得很清楚：
		// 「只认 1 会把每一个免费试用用户和每一个改套餐的用户都拒掉 —— 而这些人
		// 在 Google 看来是完全有权益的」。Go 版第一版只认 {1,2}，等于把那个
		// 已经修好的 bug 又放回来了，而且后果比 Node 当年更重：
		// public_purchases.go:136-138 在 !Valid 时会把订单标成 invalid，
		// 而补偿扫描只重试 status='pending'，所以这一标就是**永久**的 ——
		// 一个正在改套餐的付费用户会彻底失去权益，只能人工改库救回来。
		paid := raw.PaymentState != nil &&
			(*raw.PaymentState == 1 || *raw.PaymentState == 2 || *raw.PaymentState == 3)
		// 🔴 还必须校验有效期。Node 版是 `valid: expiry > Date.now() && paid`，
		// Go 版第一版把 expiryTimeMillis 解析出来却从来不比 —— 于是一个早就过期、
		// Google 已经停止计费的订阅 token 照样验成 valid，finalizePurchase 会
		// 照着商品的 granted_units 白发一次额度（而 userPlan 认过期时间，
		// 所以运营那边看不到套餐变化，只有额度莫名其妙多出来）。
		res.Valid = paid && res.ExpiresAt != nil && res.ExpiresAt.After(now)
	} else {
		// purchaseState 0 = 已购买。
		res.Valid = raw.PurchaseState != nil && *raw.PurchaseState == 0
	}
	return res, nil
}

// Acknowledge 向 Play 确认一笔购买。
// Play 会在 3 天后自动退款一笔未被确认的购买。
func (c *Client) Acknowledge(ctx context.Context, productID, purchaseToken, kind string) error {
	if err := AssertToken(purchaseToken, productID); err != nil {
		return err
	}
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	seg := "products"
	if kind == "subscription" {
		seg = "subscriptions"
	}
	api := "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/" +
		url.PathEscape(c.packageName) + "/purchases/" + seg + "/" +
		url.PathEscape(productID) + "/tokens/" + url.PathEscape(purchaseToken) + ":acknowledge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, bytes.NewReader([]byte("{}")))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnavailable
	}
	return nil
}
