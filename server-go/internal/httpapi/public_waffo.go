package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
	"museframe-api/internal/waffo"
)

// Waffo Pancake 网页端结账（2026-09-23）。
//
// 流程：客户端 POST /v1/purchases/web/checkout → 我们先落一条 pending 的 purchases 行
// （platform='waffo'，external_transaction_id = 这条行自己的 id），再向 Waffo 建结账会话，
// 把这个 id 作为 orderMerchantExternalId 送出去 → 客户端跳到托管收银台 →
// Waffo 的 webhook 把同一个 id 原样回传 → webhook_waffo.go 按它找回这条行并发额度。
//
// 🔴 额度只在 webhook 验签通过之后才发。successUrl 回跳只是「买家点了完成」，
// 不是付款证明 —— 前端回来只做轮询，不带任何「我付了」的语义。

// WebCheckoutResult 是 POST /v1/purchases/web/checkout 的出参。
type WebCheckoutResult struct {
	CheckoutURL string `json:"checkoutUrl"`
	SessionID   string `json:"sessionId"`
	ExpiresAt   string `json:"expiresAt"`
	PurchaseID  string `json:"purchaseId"`
}

// WebCancelResult 是 POST /v1/purchases/web/subscription/cancel 的出参。
type WebCancelResult struct {
	Status    string  `json:"status"`
	ExpiresAt *string `json:"expiresAt"`
}

// waffoReady 判断网页端结账是否可用：商户私钥 + 店铺号齐全。
// （店铺号本身不参与建会话 —— 会话由 productId 决定店铺 —— 但播种 / 对账都要它，
// 缺了就当没配好，免得半套配置在线上跑出「能下单不能对账」。）
func (a *App) waffoReady() bool {
	return a.waffo != nil && a.waffo.Configured() && a.cfg != nil && strings.TrimSpace(a.cfg.WaffoStoreID) != ""
}

// waffoLanguage 把 users.locale 映射到收银台支持的 BCP 47 枚举
// （en, zh-Hans, zh-Hant-TW, zh-Hant-HK, ja-JP, ko-KR, …）。命不中一律 en，
// 买家在收银台上还能自己切。
func waffoLanguage(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	l = strings.ReplaceAll(l, "_", "-")
	switch {
	case l == "":
		return "en"
	case strings.HasPrefix(l, "zh"):
		// 繁体：zh-hant*、zh-tw、zh-hk、zh-mo；其余中文一律简体。
		if strings.Contains(l, "hant") || strings.HasSuffix(l, "-tw") || strings.HasSuffix(l, "-mo") {
			return "zh-Hant-TW"
		}
		if strings.HasSuffix(l, "-hk") {
			return "zh-Hant-HK"
		}
		return "zh-Hans"
	case strings.HasPrefix(l, "ja"):
		return "ja-JP"
	case strings.HasPrefix(l, "ko"):
		return "ko-KR"
	default:
		return "en"
	}
}

// webCheckoutCurrencies 是网页端允许的币种。CNY 只对加购包开放（文档：CNY 仅微信支付、
// 且不支持订阅），订阅恒为 USD。
var webCheckoutCurrencies = map[string]bool{"USD": true, "CNY": true}

// hWebCheckout 是 POST /v1/purchases/web/checkout。
func (a *App) hWebCheckout(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	productKey, err := requiredString(c.Body, "productKey", 100)
	if err != nil {
		return nil, err
	}
	currencyPtr, err := optionalString(c.Body, "currency", 3)
	if err != nil {
		return nil, err
	}
	currency := "USD"
	if currencyPtr != nil && strings.TrimSpace(*currencyPtr) != "" {
		currency = strings.ToUpper(strings.TrimSpace(*currencyPtr))
	}
	if !webCheckoutCurrencies[currency] {
		return nil, apierr.New(422, apierr.CodeValidation, "currency must be USD or CNY.")
	}
	if !a.waffoReady() {
		return nil, apierr.New(http.StatusNotImplemented, apierr.CodeProviderNotConfigured, "Web checkout is not configured on the server.")
	}
	product, err := store.GetActiveProductByKey(ctx, a.st.Q(), productKey)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown product.")
		}
		return nil, err
	}
	if product.WaffoProductID == nil || strings.TrimSpace(*product.WaffoProductID) == "" {
		return nil, apierr.New(http.StatusNotImplemented, apierr.CodeProviderNotConfigured, "This product is not available for web checkout yet.")
	}
	amount := product.PriceMinor
	if currency == "CNY" {
		if product.ProductType != "pack" {
			return nil, apierr.New(422, apierr.CodeValidation, "CNY checkout is only available for packs.")
		}
		if product.PriceCnyMinor == nil {
			return nil, apierr.New(422, apierr.CodeValidation, "This product has no CNY price.")
		}
		amount = *product.PriceCnyMinor
	}

	// 先落 pending 行再出网（与 Play 路径同一次序）：Waffo 那边一旦建了会话、买家付了钱，
	// 我们这边必须已经有一行可以被 webhook 找回来。
	purchaseID := a.newID()
	t := a.now()
	cur := currency
	if err := store.InsertPurchase(ctx, a.st.Q(), &store.Purchase{
		ID: purchaseID, UserID: u.ID, ProductID: product.ID, Platform: "waffo",
		ExternalTransactionID: purchaseID, Status: "pending",
		AmountMinor: &amount, Currency: &cur, PurchasedAt: t, CreatedAt: t,
	}); err != nil {
		return nil, err
	}
	email, err := store.UserEmail(ctx, a.st.Q(), u.ID)
	if err != nil {
		a.lg.Warn("waffo: 读用户邮箱失败，结账页不预填", map[string]any{"userId": u.ID})
		email = ""
	}
	sess, err := a.waffo.CreateCheckoutSession(ctx, waffo.CheckoutSessionRequest{
		ProductID:  *product.WaffoProductID,
		Currency:   currency,
		BuyerEmail: email,
		SuccessURL: a.cfg.WaffoSuccessURL,
		Metadata: map[string]string{
			"userId": u.ID, "productKey": product.InternalKey, "purchaseId": purchaseID,
		},
		OrderMerchantExternalID: purchaseID,
		Language:                waffoLanguage(u.Locale),
	})
	if err != nil {
		// 会话没建起来，这条 pending 行永远不会被任何 webhook 找回，删掉免得后台里堆占位行。
		_ = store.DeletePurchase(ctx, a.st.Q(), purchaseID)
		return nil, a.waffoCallError(err, "checkout")
	}
	a.lg.Info("waffo: 结账会话已建", map[string]any{
		"userId": u.ID, "purchaseId": purchaseID, "productKey": product.InternalKey,
		"currency": currency, "sessionId": sess.SessionID,
	})
	return WebCheckoutResult{
		CheckoutURL: sess.CheckoutURL, SessionID: sess.SessionID, ExpiresAt: sess.ExpiresAt, PurchaseID: purchaseID,
	}, nil
}

// hWebSubscriptionCancel 是 POST /v1/purchases/web/subscription/cancel。
// 以商户身份向 Waffo 请求取消：active → canceling，本期末真正结束（届时 webhook
// subscription.canceled 到达，那时才把权益关掉）。这里**不改**本地状态。
func (a *App) hWebSubscriptionCancel(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	p, err := store.ActiveWaffoSubscription(ctx, a.st.Q(), u.ID, a.now())
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("No active web subscription.")
		}
		return nil, err
	}
	if !a.waffoReady() {
		return nil, apierr.New(http.StatusNotImplemented, apierr.CodeProviderNotConfigured, "Web checkout is not configured on the server.")
	}
	res, err := a.waffo.CancelSubscription(ctx, *p.ProviderOrderID)
	if err != nil {
		return nil, a.waffoCallError(err, "cancel")
	}
	expires := p.ExpiresAt
	if res.Status == "canceled" {
		// 只有 pending 的订阅会被立即取消；我们只对 verified 行调这个接口，
		// 所以正常到不了这里 —— 真到了就按 Waffo 的口径立刻关掉。
		now := a.now()
		if err := store.SetPurchaseStatusExpiry(ctx, a.st.Q(), p.ID, "canceled", &now); err != nil {
			return nil, err
		}
		expires = &now
	}
	a.lg.Info("waffo: 已请求取消订阅", map[string]any{
		"userId": u.ID, "purchaseId": p.ID, "orderId": *p.ProviderOrderID, "status": res.Status,
	})
	return WebCancelResult{Status: res.Status, ExpiresAt: store.ISOPtr(expires)}, nil
}

// waffoCallError 把 Waffo 客户端的错误翻成 API 错误。4xx 的原话进日志不进响应
// （里面可能带对方的内部字段名），买家只需要知道「没开起来」。
func (a *App) waffoCallError(err error, op string) error {
	switch {
	case errors.Is(err, waffo.ErrNotConfigured):
		return apierr.New(http.StatusNotImplemented, apierr.CodeProviderNotConfigured, "Web checkout is not configured on the server.")
	case errors.Is(err, waffo.ErrUnavailable):
		a.lg.Warn("waffo: 平台暂时不可达", map[string]any{"op": op})
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeVerificationUnavail, "The payment provider could not be reached. Please try again in a moment.")
	}
	if e, ok := waffo.IsAPIError(err); ok {
		a.lg.Warn("waffo: 平台拒绝了请求", map[string]any{"op": op, "status": e.Status, "message": e.Message})
		if op == "cancel" {
			return apierr.New(422, apierr.CodeValidation, "This subscription can no longer be canceled.")
		}
		// 403 = 店铺还没被 Waffo 放行做正式收款（审核中 / 被暂停）。这是「通道没开」而不是
		// 请求写错了，单独给一个码，前端据此提示「支付通道正在审核中，请稍后再试」。
		if e.Status == http.StatusForbidden {
			return apierr.New(http.StatusServiceUnavailable, apierr.CodePaymentsNotReady, "Online payments are being activated. Please try again later.")
		}
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeVerificationUnavail, "The payment provider declined the checkout request. Please contact us.")
	}
	return err
}
