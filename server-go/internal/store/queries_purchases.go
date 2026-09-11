package store

import (
	"context"
	"time"
)

const purchaseCols = `id, user_id, product_id, platform, external_transaction_id, status, amount_minor,
	currency, purchased_at, expires_at, created_at`

func scanPurchase(row interface{ Scan(...any) error }) (*Purchase, error) {
	var p Purchase
	err := row.Scan(&p.ID, &p.UserID, &p.ProductID, &p.Platform, &p.ExternalTransactionID, &p.Status,
		&p.AmountMinor, &p.Currency, &p.PurchasedAt, &p.ExpiresAt, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPurchaseByExternal 按 (platform, external_transaction_id) 取订单。
// 这个组合上的 UNIQUE 约束是防重复发放的地基。
func GetPurchaseByExternal(ctx context.Context, q Queryer, platform, externalTxID string) (*Purchase, error) {
	return scanPurchase(q.QueryRow(ctx,
		`SELECT `+purchaseCols+` FROM purchases WHERE platform = $1 AND external_transaction_id = $2`, platform, externalTxID))
}

const insertPurchaseSQL = `INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id, status,
		   amount_minor, currency, purchased_at, expires_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

// InsertPurchase 写一条订单，(platform, external_transaction_id) 撞唯一约束就**报错**。
//
// 🔴 这里绝不能加 ON CONFLICT DO NOTHING。对齐 Node 版：待核验的那条是
// `INSERT OR IGNORE`（api.js:1047），已核验的那条是**裸 INSERT**（api.js:1179），
// 靠唯一约束冲突把整个 tx() 炸掉，从而挡住重复发放。
//
// Go 版第一版两处共用了带 DO NOTHING 的同一个函数，于是「防重复发放的地基」
// 被悄悄拆掉了：/v1/purchases/verify 是 async 的，同一笔订单两个并发请求都能
// 读到 existing == nil，各自 a.newID() 生成**不同**的 purchaseID，于是
// grantPurchaseUnits 算出的 reference_key 是 grant:purchase:<uuidA> 和
// grant:purchase:<uuidB> —— 两个不同的键，credit_ledger 上
// UNIQUE (user_id, reference_key) 根本不会触发。结果一笔支付发两份额度。
// 冲突必须抛出来，让事务回滚。
func InsertPurchase(ctx context.Context, q Queryer, p *Purchase) error {
	_, err := q.Exec(ctx, insertPurchaseSQL,
		p.ID, p.UserID, p.ProductID, p.Platform, p.ExternalTransactionID, p.Status,
		p.AmountMinor, p.Currency, p.PurchasedAt, p.ExpiresAt, p.CreatedAt)
	return err
}

// InsertPurchaseIfAbsent 写一条订单，已存在就静默跳过（对齐 Node 的 INSERT OR IGNORE）。
// 只给「先落一条 pending 再去问 Play」那条路用：那里重复是正常的，不是错误。
func InsertPurchaseIfAbsent(ctx context.Context, q Queryer, p *Purchase) error {
	_, err := q.Exec(ctx,
		insertPurchaseSQL+` ON CONFLICT (platform, external_transaction_id) DO NOTHING`,
		p.ID, p.UserID, p.ProductID, p.Platform, p.ExternalTransactionID, p.Status,
		p.AmountMinor, p.Currency, p.PurchasedAt, p.ExpiresAt, p.CreatedAt)
	return err
}

// MarkPurchaseVerified 把订单标为已核验。
func MarkPurchaseVerified(ctx context.Context, q Queryer, id string, expires *time.Time, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE purchases SET status = 'verified', expires_at = $1, purchased_at = $2 WHERE id = $3`, expires, t, id)
	return err
}

// MarkPurchaseStatus 把 pending 订单改成指定状态（invalid 等）。
func MarkPurchaseStatus(ctx context.Context, q Queryer, platform, externalTxID, status string) error {
	_, err := q.Exec(ctx,
		`UPDATE purchases SET status = $1 WHERE platform = $2 AND external_transaction_id = $3 AND status = 'pending'`,
		status, platform, externalTxID)
	return err
}

// SetPurchaseExpiry 只更新到期时间（订阅续期）。
func SetPurchaseExpiry(ctx context.Context, q Queryer, id string, expires *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE purchases SET expires_at = $1 WHERE id = $2`, expires, id)
	return err
}

// PurchaseListItem 是 GET /v1/purchases 的一行。
type PurchaseListItem struct {
	ID          string
	ProductName string
	AmountMinor *int64
	Currency    *string
	PurchasedAt time.Time
	ExpiresAt   *time.Time
}

// ListPurchasesOfUser 订单历史，ORDER BY purchased_at DESC，无 LIMIT。
func ListPurchasesOfUser(ctx context.Context, q Queryer, userID string) ([]PurchaseListItem, error) {
	rows, err := q.Query(ctx,
		`SELECT pu.id, p.display_name, pu.amount_minor, pu.currency, pu.purchased_at, pu.expires_at
		 FROM purchases pu JOIN products p ON p.id = pu.product_id
		 WHERE pu.user_id = $1 ORDER BY pu.purchased_at DESC, pu.id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PurchaseListItem
	for rows.Next() {
		var it PurchaseListItem
		if err := rows.Scan(&it.ID, &it.ProductName, &it.AmountMinor, &it.Currency, &it.PurchasedAt, &it.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetIdempotency 取一条幂等记录。
func GetIdempotency(ctx context.Context, q Queryer, userID, key string) (*IdempotencyRecord, error) {
	var r IdempotencyRecord
	err := q.QueryRow(ctx,
		`SELECT user_id, idempotency_key, request_hash, response_status, response_body, created_at
		 FROM idempotency_records WHERE user_id = $1 AND idempotency_key = $2`, userID, key).
		Scan(&r.UserID, &r.Key, &r.RequestHash, &r.ResponseStatus, &r.ResponseBody, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// InsertIdempotency 写一条幂等记录。
func InsertIdempotency(ctx context.Context, q Queryer, userID, key, reqHash string, status int, body []byte, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO idempotency_records (user_id, idempotency_key, request_hash, response_status, response_body, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`, userID, key, reqHash, status, body, t)
	return err
}

// DeletePurchase 删掉一条按客户端令牌建的 pending 占位行。
func DeletePurchase(ctx context.Context, q Queryer, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM purchases WHERE id = $1`, id)
	return err
}

// SetPurchaseExternalID 把订单改指到 Google 自己的订单号上。
func SetPurchaseExternalID(ctx context.Context, q Queryer, id, externalTxID string) error {
	_, err := q.Exec(ctx, `UPDATE purchases SET external_transaction_id = $1 WHERE id = $2`, externalTxID, id)
	return err
}
