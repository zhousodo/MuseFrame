package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
	"museframe-api/internal/waffo"
)

// Waffo webhook（POST /v1/webhooks/waffo）。
//
// 顺序是硬性的：验签 → 解析 → mode 过滤 → 按载荷 id 去重落库 → 处理 → 标记完成。
// 没验签之前不碰数据库；验签失败 401（Waffo 会重试，但重试也验不过，最终标 failed，
// 后台 webhook 日志能看到）；公钥没配 503。
//
// 🔴 响应码的含义：
//   - 2xx = 收下了。业务层面的异常（找不到订单、商品对不上）也是 2xx —— 重投不会
//     让它变对，只会让 Waffo 把这条标成 failed 然后运维看不出到底哪条没处理；
//     这些异常写进 webhook_events.error，运营在后台「数据库浏览」里看。
//   - 5xx = 我们这边的瞬时故障（DB 事务失败）。processed_at 留 NULL，Waffo 重投时
//     ClaimWebhookEvent 回 WebhookPending，重新处理一遍。
//
// 全部处理都是纯 DB 工作（不出网），远在 10 秒上限之内。

// waffoPeriodGrace 是订阅到期时间在 currentPeriodEnd 之上的宽限。
//
// 🔴 currentPeriodEnd 是一个**日期**（"2026-05-10"），不带时刻；ParseDate 把它解成
// 当天 00:00Z。真正的续费发生在当天某个时刻、续费 webhook 再晚几分钟到 ——
// 所以到期时间 = 日期零点 + 24h（覆盖整天）+ 24h（给续费与投递留余量）。
// 取消（subscription.canceled）到达时会把 expires_at 直接压到 now，宽限不会让
// 已取消的订阅多活；它只避免「续费成功了、权益却先掉线两小时」。
const waffoPeriodGrace = 48 * time.Hour

// note 是 processWaffoEvent 记进 webhook_events.error 的业务异常说明。
func note(s string) *string { return &s }

// hWaffoWebhook 是 POST /v1/webhooks/waffo。
func (a *App) hWaffoWebhook(c *Ctx) (any, error) {
	ctx := c.R.Context()
	if a.waffoPub == nil {
		return nil, apierr.New(http.StatusServiceUnavailable, apierr.CodeProviderNotConfigured, "Webhook verification key is not configured.")
	}
	if err := waffo.VerifyWebhook(c.Raw, c.R.Header.Get("X-Waffo-Signature"), a.waffoPub, a.now()); err != nil {
		a.lg.Warn("waffo webhook: 验签失败", map[string]any{"reason": err.Error(), "event": c.R.Header.Get("X-Waffo-Event")})
		return nil, apierr.New(http.StatusUnauthorized, apierr.CodeAuthInvalid, "Invalid webhook signature.")
	}
	ev, err := waffo.ParseEvent(c.Raw)
	if err != nil {
		return nil, apierr.New(http.StatusBadRequest, apierr.CodeValidation, "Malformed webhook payload.")
	}
	if ev.Mode != "" && a.cfg != nil && ev.Mode != a.cfg.WaffoMode {
		// 测试事件打到生产（或反过来）：签名是对的（同一把钥只会验过自己环境的事件，
		// 但运维可能把测试钥填进了生产），一律不发额度。
		a.lg.Warn("waffo webhook: 环境不符，已忽略", map[string]any{
			"eventId": ev.ID, "eventType": ev.EventType, "mode": ev.Mode, "expected": a.cfg.WaffoMode,
		})
		return map[string]any{"ok": true, "ignored": "mode"}, nil
	}

	claim, err := store.ClaimWebhookEvent(ctx, a.st.Q(), ev.ID, "waffo", ev.EventType, ev.Mode, c.Raw, a.now())
	if err != nil {
		return nil, err
	}
	if claim == store.WebhookProcessed {
		return map[string]any{"ok": true, "duplicate": true}, nil
	}
	n, err := a.processWaffoEvent(ctx, ev)
	if err != nil {
		a.lg.Warn("waffo webhook: 处理失败，等待重投", map[string]any{
			"eventId": ev.ID, "eventType": ev.EventType, "error": err.Error(),
		})
		return nil, err
	}
	if n != nil {
		a.lg.Warn("waffo webhook: 业务异常（已 200，见 webhook_events.error）", map[string]any{
			"eventId": ev.ID, "eventType": ev.EventType, "note": *n,
		})
	}
	if err := store.MarkWebhookEventProcessed(ctx, a.st.Q(), ev.ID, a.now(), n); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// processWaffoEvent 按事件类型分发。返回 (业务异常说明, 瞬时错误)。
func (a *App) processWaffoEvent(ctx context.Context, ev *waffo.Event) (*string, error) {
	switch ev.EventType {
	case waffo.EventOrderCompleted:
		return a.waffoOrderCompleted(ctx, ev)
	case waffo.EventSubscriptionActivated, waffo.EventSubscriptionRenewed, waffo.EventSubscriptionRecovered:
		return a.waffoSubscriptionPeriod(ctx, ev, true)
	case waffo.EventSubscriptionPaymentOK:
		// 纯支付事件：没有周期信息，发额度的是同批到达的 activated / renewed / recovered。
		// 这里只把 pending 行标成已核验（两条到达顺序不保证，谁先到谁标）。
		return a.waffoSubscriptionPeriod(ctx, ev, false)
	case waffo.EventSubscriptionCanceled:
		return a.waffoSubscriptionCanceled(ctx, ev)
	case waffo.EventRefundSucceeded:
		return a.waffoRefundSucceeded(ctx, ev)
	case waffo.EventSubscriptionCanceling, waffo.EventSubscriptionUncanceled, waffo.EventSubscriptionPastDue,
		waffo.EventSubscriptionPlanChanged, waffo.EventSubscriptionPlanChangeSc, waffo.EventSubscriptionPlanChangeFa,
		waffo.EventRefundFailed:
		// 只记录（载荷已整条落库）。canceling：权益保留到期末，不动；past_due：Waffo 还会
		// 重试扣款，不动；refund.failed：钱没动，不动；plan_change*：本店只有一个订阅商品，
		// 理论上不会出现，出现了也不自动改权益 —— 留给运营看。
		p, _, n, err := a.findWaffoPurchase(ctx, ev)
		if err != nil {
			return nil, err
		}
		if n != nil {
			return n, nil
		}
		a.lg.Info("waffo webhook: 状态事件已记录", map[string]any{
			"eventType": ev.EventType, "purchaseId": p.ID, "orderId": ev.Data.OrderID, "orderStatus": ev.Data.OrderStatus,
		})
		return nil, nil
	default:
		return note("unhandled event type: " + ev.EventType), nil
	}
}

// findWaffoPurchase 按 orderMerchantExternalId（= 我们的 purchases.id）找回订单；
// 缺失时依次回落到 orderMetadata.purchaseId、平台订单号。找不到 / 对不上是业务异常，
// 不是瞬时错误。
func (a *App) findWaffoPurchase(ctx context.Context, ev *waffo.Event) (*store.Purchase, *store.Product, *string, error) {
	q := a.st.Q()
	ext := strings.TrimSpace(ev.Data.OrderMerchantExternalID)
	if ext == "" {
		ext = strings.TrimSpace(ev.Data.OrderMetadata["purchaseId"])
	}
	var p *store.Purchase
	if ext != "" {
		got, err := store.GetPurchaseByExternal(ctx, q, "waffo", ext)
		if err != nil && !store.IsNoRows(err) {
			return nil, nil, nil, err
		}
		p = got
	}
	if p == nil && strings.TrimSpace(ev.Data.OrderID) != "" {
		got, err := store.GetPurchaseByProviderOrder(ctx, q, "waffo", ev.Data.OrderID)
		if err != nil && !store.IsNoRows(err) {
			return nil, nil, nil, err
		}
		p = got
	}
	if p == nil {
		return nil, nil, note("no matching purchase (externalId=" + ext + ", orderId=" + ev.Data.OrderID + ")"), nil
	}
	product, err := store.GetProductByID(ctx, q, p.ProductID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, nil, note("purchase " + p.ID + " points at a missing product"), nil
		}
		return nil, nil, nil, err
	}
	if key := ev.Data.OrderMetadata["productKey"]; key != "" && key != product.InternalKey {
		return nil, nil, note("product mismatch: purchase=" + product.InternalKey + " metadata=" + key), nil
	}
	if uid := ev.Data.OrderMetadata["userId"]; uid != "" && uid != p.UserID {
		return nil, nil, note("user mismatch: purchase=" + p.UserID + " metadata=" + uid), nil
	}
	return p, product, nil, nil
}

// waffoOrderCompleted：一次性商品付款成功 → 标已核验 + 发额度（键 grant:purchase:<purchaseId>）。
func (a *App) waffoOrderCompleted(ctx context.Context, ev *waffo.Event) (*string, error) {
	p, product, n, err := a.findWaffoPurchase(ctx, ev)
	if err != nil || n != nil {
		return n, err
	}
	if p.Status == "verified" {
		return nil, nil // 同一订单只会触发一次；真重复也是空操作
	}
	if p.Status != "pending" {
		return note("order.completed on a " + p.Status + " purchase, left untouched"), nil
	}
	now := a.now()
	amount, currency := waffoChargedFields(ev)
	expires := periodExpiry(product, now)
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.MarkPurchasePaid(ctx, q, p.ID, ev.Data.OrderID, amount, currency, expires, &now); err != nil {
			return err
		}
		granted, err := store.LedgerRefExistsForUser(ctx, q, p.UserID, "grant:purchase:"+p.ID)
		if err != nil {
			return err
		}
		if granted {
			return nil
		}
		return a.grantPurchaseUnits(ctx, q, p.UserID, product, p.ID, expires, nil)
	})
	if err != nil {
		return nil, err
	}
	a.lg.Info("waffo webhook: 一次性订单已入账", map[string]any{
		"purchaseId": p.ID, "userId": p.UserID, "productKey": product.InternalKey,
		"orderId": ev.Data.OrderID, "units": product.GrantedUnits,
	})
	return nil, nil
}

// waffoSubscriptionPeriod：订阅激活 / 续期 / 逾期恢复 → 把 expires_at 推到本期末 + 宽限，
// 并按周期发一次额度（键 grant:purchase:<purchaseId>:<periodEnd>，与 Play 续期同形）。
// grant=false（payment_succeeded）只标核验不发额度。
func (a *App) waffoSubscriptionPeriod(ctx context.Context, ev *waffo.Event, grant bool) (*string, error) {
	p, product, n, err := a.findWaffoPurchase(ctx, ev)
	if err != nil || n != nil {
		return n, err
	}
	if product.ProductType != "subscription" {
		return note("subscription event on a " + product.ProductType + " purchase"), nil
	}
	if p.Status != "pending" && p.Status != "verified" {
		return note(ev.EventType + " on a " + p.Status + " purchase, left untouched"), nil
	}
	now := a.now()
	var expires *time.Time
	var ref *string
	if periodEnd, ok := waffo.ParseDate(ev.Data.CurrentPeriodEnd); ok {
		e := periodEnd.Add(waffoPeriodGrace)
		expires = &e
		r := p.ID + ":" + store.ISO(periodEnd)
		ref = &r
	} else if grant {
		// 文档说订阅事件都带 currentPeriodEnd；真缺了就按商品周期估一个，并留痕。
		e := periodExpiry(product, now)
		expires = e
		if e != nil {
			r := p.ID + ":" + store.ISO(*e)
			ref = &r
		}
		a.lg.Warn("waffo webhook: 订阅事件缺 currentPeriodEnd，按商品周期估算", map[string]any{
			"eventType": ev.EventType, "purchaseId": p.ID,
		})
	}
	// 到期时间只往后推，不往前拉（activated 与 renewed 的到达顺序不保证）。
	if expires == nil || (p.ExpiresAt != nil && p.ExpiresAt.After(*expires)) {
		expires = p.ExpiresAt
	}
	// 首付金额只在 pending → verified 那一次记；续期不改。
	var amount *int64
	var currency *string
	var purchasedAt *time.Time
	if p.Status == "pending" {
		amount, currency = waffoChargedFields(ev)
		purchasedAt = &now
	}
	units := 0
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.MarkPurchasePaid(ctx, q, p.ID, ev.Data.OrderID, amount, currency, expires, purchasedAt); err != nil {
			return err
		}
		if !grant || ref == nil {
			return nil
		}
		done, err := store.LedgerRefExistsForUser(ctx, q, p.UserID, "grant:purchase:"+*ref)
		if err != nil || done {
			return err
		}
		units = product.GrantedUnits
		return a.grantPurchaseUnits(ctx, q, p.UserID, product, p.ID, expires, ref)
	})
	if err != nil {
		return nil, err
	}
	a.lg.Info("waffo webhook: 订阅周期已入账", map[string]any{
		"eventType": ev.EventType, "purchaseId": p.ID, "userId": p.UserID, "orderId": ev.Data.OrderID,
		"periodEnd": ev.Data.CurrentPeriodEnd, "expiresAt": store.ISOPtr(expires), "units": units,
	})
	return nil, nil
}

// waffoSubscriptionCanceled：订阅终止（期末到了 / 逾期再次扣款失败）→ 立刻关权益。
func (a *App) waffoSubscriptionCanceled(ctx context.Context, ev *waffo.Event) (*string, error) {
	p, _, n, err := a.findWaffoPurchase(ctx, ev)
	if err != nil || n != nil {
		return n, err
	}
	if p.Status != "verified" {
		return note("subscription.canceled on a " + p.Status + " purchase, left untouched"), nil
	}
	now := a.now()
	expires := now
	if p.ExpiresAt != nil && p.ExpiresAt.Before(now) {
		expires = *p.ExpiresAt
	}
	if err := store.SetPurchaseStatusExpiry(ctx, a.st.Q(), p.ID, "canceled", &expires); err != nil {
		return nil, err
	}
	a.lg.Info("waffo webhook: 订阅已终止", map[string]any{
		"purchaseId": p.ID, "userId": p.UserID, "orderId": ev.Data.OrderID, "canceledAt": ev.Data.CanceledAt,
	})
	return nil, nil
}

// waffoRefundSucceeded：退款到账 → 订单标 refunded；订阅立刻关权益；加购包撤销**尚未消费**的额度
// （台账补负分录，见 ledger.Revoke）。
//
// 🔴 决策：部分退款也按全额处理（撤销全部未消费额度 + 订阅立即关）。载荷不带
// 「该笔支付累计退了多少」，我们也不做按比例撤销 —— 一笔真实退款几乎总是全额，
// 而按比例撤销一个已经用掉一半的加购包在数学上没有正确答案。运营要给部分退款
// 的用户保留额度，用后台「手工发放」补回去。
func (a *App) waffoRefundSucceeded(ctx context.Context, ev *waffo.Event) (*string, error) {
	p, product, n, err := a.findWaffoPurchase(ctx, ev)
	if err != nil || n != nil {
		return n, err
	}
	if p.Status == "refunded" {
		return nil, nil
	}
	if p.Status != "verified" && p.Status != "canceled" {
		return note("refund.succeeded on a " + p.Status + " purchase, left untouched"), nil
	}
	now := a.now()
	revoked := 0
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		var expires *time.Time
		if product.ProductType == "subscription" {
			expires = &now
			if p.ExpiresAt != nil && p.ExpiresAt.Before(now) {
				expires = p.ExpiresAt
			}
		} else {
			expires = p.ExpiresAt
		}
		if err := store.SetPurchaseStatusExpiry(ctx, q, p.ID, "refunded", expires); err != nil {
			return err
		}
		r, err := ledger.Revoke(ctx, q, a.newID, p.UserID, p.ID, now)
		if err != nil {
			return err
		}
		revoked = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.lg.Info("waffo webhook: 退款已处理", map[string]any{
		"purchaseId": p.ID, "userId": p.UserID, "orderId": ev.Data.OrderID,
		"refundedAmount": ev.Data.RefundedAmount, "currency": ev.Data.Currency, "unitsRevoked": revoked,
	})
	if ev.Data.RefundedAmount != "" && p.AmountMinor != nil {
		if minor, err := waffo.ParseAmountMinor(ev.Data.RefundedAmount, ev.Data.Currency); err == nil && minor < *p.AmountMinor {
			return note("partial refund " + ev.Data.RefundedAmount + " " + ev.Data.Currency + " handled as full: all unconsumed units revoked"), nil
		}
	}
	return nil, nil
}

// waffoChargedFields 取事件里实收金额与币种（minor）；金额缺失或不可解析时两者都为 nil，
// 保留建会话时按目录价写下的值。
func waffoChargedFields(ev *waffo.Event) (*int64, *string) {
	minor, ok := ev.Data.ChargedMinor()
	if !ok || strings.TrimSpace(ev.Data.Currency) == "" {
		return nil, nil
	}
	cur := strings.ToUpper(strings.TrimSpace(ev.Data.Currency))
	return &minor, &cur
}
