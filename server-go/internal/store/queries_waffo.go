package store

import (
	"context"
	"time"
)

// ---- Waffo 网页端结账：商品映射 / 订单 / webhook 去重 -------------------------

// SetProductWaffoID 回填本地商品对应的 Waffo 商品号（播种 CLI 用）。
func SetProductWaffoID(ctx context.Context, q Queryer, productID, waffoProductID string) error {
	_, err := q.Exec(ctx, `UPDATE products SET waffo_product_id = $1 WHERE id = $2`, waffoProductID, productID)
	return err
}

// GetPurchaseByID 按主键取订单。
func GetPurchaseByID(ctx context.Context, q Queryer, id string) (*Purchase, error) {
	return scanPurchase(q.QueryRow(ctx, `SELECT `+purchaseCols+` FROM purchases WHERE id = $1`, id))
}

// GetPurchaseByProviderOrder 按 (platform, provider_order_id) 取订单。
// 取消 / 退款类事件按平台订单号反查时用；正常路径仍按 external_transaction_id。
func GetPurchaseByProviderOrder(ctx context.Context, q Queryer, platform, orderID string) (*Purchase, error) {
	return scanPurchase(q.QueryRow(ctx,
		`SELECT `+purchaseCols+` FROM purchases WHERE platform = $1 AND provider_order_id = $2
		 ORDER BY purchased_at DESC LIMIT 1`, platform, orderID))
}

// MarkPurchasePaid 把订单标为已核验并记下平台订单号与实收金额。
// amount / currency 为 nil 时保留原值（续期事件不改首付金额）；purchasedAt 同理。
func MarkPurchasePaid(ctx context.Context, q Queryer, id, providerOrderID string, amount *int64, currency *string, expires *time.Time, purchasedAt *time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE purchases SET status = 'verified',
		       provider_order_id = COALESCE(NULLIF($2, ''), provider_order_id),
		       amount_minor = COALESCE($3, amount_minor),
		       currency     = COALESCE($4, currency),
		       expires_at   = $5,
		       purchased_at = COALESCE($6, purchased_at)
		 WHERE id = $1`, id, providerOrderID, amount, currency, expires, purchasedAt)
	return err
}

// SetPurchaseStatusExpiry 把订单改成终态（canceled / refunded）并同时改到期时间。
func SetPurchaseStatusExpiry(ctx context.Context, q Queryer, id, status string, expires *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE purchases SET status = $2, expires_at = $3 WHERE id = $1`, id, status, expires)
	return err
}

// ActiveWaffoSubscription 取用户当前有效的 Waffo 订阅（已核验、未到期、带平台订单号）。
// 一次性通行证（products.one_time）不算：它在 Waffo 那边是一次性订单，没有可取消的订阅，
// 拿它的 ORD_ 去调 cancel-order 只会被拒。
func ActiveWaffoSubscription(ctx context.Context, q Queryer, userID string, now time.Time) (*Purchase, error) {
	return scanPurchase(q.QueryRow(ctx, `
		SELECT `+purchaseColsPU+` FROM purchases pu
		JOIN products p ON p.id = pu.product_id
		WHERE pu.user_id = $1 AND pu.platform = 'waffo' AND pu.status = 'verified'
		  AND p.product_type = 'subscription' AND NOT p.one_time AND pu.provider_order_id IS NOT NULL
		  AND (pu.expires_at IS NULL OR pu.expires_at > $2)
		ORDER BY pu.purchased_at DESC LIMIT 1`, userID, now))
}

// UserEmail 取用户任一带邮箱的登录身份（结账页预填用）。没有返回空串。
func UserEmail(ctx context.Context, q Queryer, userID string) (string, error) {
	var email string
	err := q.QueryRow(ctx,
		`SELECT email_normalized FROM auth_identities
		 WHERE user_id = $1 AND email_normalized IS NOT NULL ORDER BY created_at ASC LIMIT 1`, userID).Scan(&email)
	if IsNoRows(err) {
		return "", nil
	}
	return email, err
}

// PurchaseBucketBalances 是某笔购买发出的额度桶及其余额（退款撤销用）。
// 口径与 BucketBalances 一致：余额 = 台账 units 之和，过期桶不计，只回余额 > 0 的。
// 预留中的（reserve 分录已扣）自然不在余额里，所以撤销永远不会把在跑的任务扣成负数。
func PurchaseBucketBalances(ctx context.Context, q Queryer, userID, purchaseID string, now time.Time) ([]Bucket, error) {
	rows, err := q.Query(ctx, `
		SELECT b.id, b.expires_at, b.created_at, COALESCE(SUM(l.units), 0) AS balance
		FROM credit_buckets b
		LEFT JOIN credit_ledger l ON l.balance_bucket_id = b.id
		WHERE b.user_id = $1 AND b.source_type = 'purchase' AND b.source_id = $2
		  AND (b.expires_at IS NULL OR b.expires_at > $3)
		GROUP BY b.id, b.expires_at, b.created_at
		HAVING COALESCE(SUM(l.units), 0) > 0
		ORDER BY b.created_at ASC`, userID, purchaseID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		var bal int64
		if err := rows.Scan(&b.ID, &b.ExpiresAt, &b.CreatedAt, &bal); err != nil {
			return nil, err
		}
		b.Balance = int(bal)
		out = append(out, b)
	}
	return out, rows.Err()
}

// WebhookClaim 是 ClaimWebhookEvent 的结论。
type WebhookClaim int

const (
	// WebhookNew：第一次见到这条事件，已落库，去处理。
	WebhookNew WebhookClaim = iota
	// WebhookPending：之前收到过但业务处理没完成（processed_at 为 NULL），重跑。
	WebhookPending
	// WebhookProcessed：已处理过，纯重放，直接 200。
	WebhookProcessed
)

// ClaimWebhookEvent 按载荷 id 去重落库。
//
// 🔴 去重键是载荷 id 而不是 (eventType, eventId)：文档两处口径不一，但 id 在
// 每一种事件上都唯一且重试不变（PAY_ / ORD_ / REF_ 各自带时间后缀的规则由平台保证），
// 而 eventId 在 subscription.canceling / canceled 上都等于订单号，两种事件会撞键。
func ClaimWebhookEvent(ctx context.Context, q Queryer, id, provider, eventType, mode string, payload []byte, t time.Time) (WebhookClaim, error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO webhook_events (id, provider, event_type, mode, payload, received_at)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`, id, provider, eventType, mode, payload, t)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 1 {
		return WebhookNew, nil
	}
	var processed *time.Time
	if err := q.QueryRow(ctx, `SELECT processed_at FROM webhook_events WHERE id = $1`, id).Scan(&processed); err != nil {
		return 0, err
	}
	if processed == nil {
		return WebhookPending, nil
	}
	return WebhookProcessed, nil
}

// MarkWebhookEventProcessed 记下处理完成（note 非空 = 业务层面的异常说明）。
func MarkWebhookEventProcessed(ctx context.Context, q Queryer, id string, t time.Time, note *string) error {
	_, err := q.Exec(ctx, `UPDATE webhook_events SET processed_at = $2, error = $3 WHERE id = $1`, id, t, note)
	return err
}

// HasVerifiedPurchaseOfProduct 判断用户是否已有该商品的一笔已核验购买（任意平台）。
// 体验包限购一次用（trial_*）。
func HasVerifiedPurchaseOfProduct(ctx context.Context, q Queryer, userID, productID string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM purchases
		WHERE user_id = $1 AND product_id = $2 AND status = 'verified')`, userID, productID).Scan(&ok)
	return ok, err
}
