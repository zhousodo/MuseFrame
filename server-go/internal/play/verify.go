package play

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
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
		// paymentState 1 = 已付款，2 = 免费试用期。
		res.Valid = raw.PaymentState != nil && (*raw.PaymentState == 1 || *raw.PaymentState == 2)
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
