package httpapi

import (
	"context"
	"time"

	"museframe-api/internal/store"
)

// recordPendingPurchase 写下「我们正准备就这笔向商店求证」的那一行。幂等。
func (a *App) recordPendingPurchase(ctx context.Context, userID string, p *store.Product, platform, externalTxID string) error {
	existing, err := store.GetPurchaseByExternal(ctx, a.st.Q(), platform, externalTxID)
	if err != nil && !store.IsNoRows(err) {
		return err
	}
	if existing != nil {
		return assertPurchaseClaim(existing, userID, p.ID)
	}
	t := a.now()
	amount := p.PriceMinor
	currency := p.Currency
	// pending 这条对齐 Node 的 INSERT OR IGNORE：并发下重复是正常的，不是错误。
	return store.InsertPurchaseIfAbsent(ctx, a.st.Q(), &store.Purchase{
		ID: a.newID(), UserID: userID, ProductID: p.ID, Platform: platform,
		ExternalTransactionID: externalTxID, Status: "pending",
		AmountMinor: &amount, Currency: &currency, PurchasedAt: t, CreatedAt: t,
	})
}

// adoptCanonicalTxID 把按客户端 purchaseToken 建的 pending 行改指到 Google 自己的
// 订单号上，并返回后续流程该去重的那个 id。
// 如果订单号对应的行已经存在（重放，或超时后的重试），就丢掉按令牌建的占位行 ——
// 两条都留着会让同一笔付款被算两次。
func (a *App) adoptCanonicalTxID(ctx context.Context, platform, purchaseToken, orderID, userID, productID string) (string, error) {
	if orderID == "" || orderID == purchaseToken {
		return purchaseToken, nil
	}
	err := a.st.InTx(ctx, func(q store.Queryer) error {
		canonical, err := store.GetPurchaseByExternal(ctx, q, platform, orderID)
		if err != nil && !store.IsNoRows(err) {
			return err
		}
		pending, err := store.GetPurchaseByExternal(ctx, q, platform, purchaseToken)
		if err != nil && !store.IsNoRows(err) {
			return err
		}
		if err := assertPurchaseClaim(canonical, userID, productID); err != nil {
			return err
		}
		if err := assertPurchaseClaim(pending, userID, productID); err != nil {
			return err
		}
		switch {
		case canonical != nil && pending != nil && pending.ID != canonical.ID:
			if pending.Status == "pending" {
				return store.DeletePurchase(ctx, q, pending.ID)
			}
		case canonical == nil && pending != nil:
			return store.SetPurchaseExternalID(ctx, q, pending.ID, orderID)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return orderID, nil
}

// finalizePurchase 落订单并发放额度。
func (a *App) finalizePurchase(ctx context.Context, userID string, product *store.Product, platform, externalTxID string, expiresAt *time.Time) (any, error) {
	t := a.now()
	expires := expiresAt
	if expires == nil {
		expires = periodExpiry(product, t)
	}
	existing, err := store.GetPurchaseByExternal(ctx, a.st.Q(), platform, externalTxID)
	if err != nil && !store.IsNoRows(err) {
		return nil, err
	}
	if err := assertPurchaseClaim(existing, userID, product.ID); err != nil {
		return nil, err
	}

	// 对外部交易号幂等：重放永远不会重复发放。但自动续订的 Play 订阅**每个计费
	// 周期都会重新递交同一个 purchaseToken**：只按 id 短路会让第 2 期起什么都不发、
	// 也不把 expires_at 往前推，于是 userPlan() 把一个还在付费的订阅用户打回 free，
	// 而 Play 照收钱。只有「已核验且新到期时间不晚于已知的」才是真正的空操作。
	if existing != nil && existing.Status == "verified" {
		fresher := expires != nil && (existing.ExpiresAt == nil || expires.After(*existing.ExpiresAt))
		if fresher {
			ref := existing.ID + ":" + store.ISO(*expires)
			err := a.st.InTx(ctx, func(q store.Queryer) error {
				if err := store.SetPurchaseExpiry(ctx, q, existing.ID, expires); err != nil {
					return err
				}
				// 按周期限定的 reference_key：每个计费周期恰好发一次。
				return a.grantPurchaseUnits(ctx, q, userID, product, existing.ID, expires, &ref)
			})
			if err != nil {
				return nil, err
			}
		}
		ent, err := a.entitlements(ctx, userID)
		if err != nil {
			return nil, err
		}
		return VerifyResult{PurchaseID: existing.ID, Status: "verified", Entitlements: ent}, nil
	}

	purchaseID := a.newID()
	if existing != nil {
		purchaseID = existing.ID
	}
	// 一个事务。INSERT 曾经自己提交、grantUnits 在它之后跑，于是一个配成
	// grantedUnits=0 的商品会撞上 credit_buckets 的 CHECK，留下一条 verified 行，
	// 之后每次重试都返回 200 而余额纹丝不动 —— 钱收了，什么也没发。
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if existing != nil {
			if err := store.MarkPurchaseVerified(ctx, q, purchaseID, expires, t); err != nil {
				return err
			}
		} else {
			amount := product.PriceMinor
			currency := product.Currency
			if err := store.InsertPurchase(ctx, q, &store.Purchase{
				ID: purchaseID, UserID: userID, ProductID: product.ID, Platform: platform,
				ExternalTransactionID: externalTxID, Status: "verified",
				AmountMinor: &amount, Currency: &currency,
				PurchasedAt: t, ExpiresAt: expires, CreatedAt: t,
			}); err != nil {
				return err
			}
		}
		return a.grantPurchaseUnits(ctx, q, userID, product, purchaseID, expires, nil)
	})
	if err != nil {
		return nil, err
	}
	ent, err := a.entitlements(ctx, userID)
	if err != nil {
		return nil, err
	}
	return VerifyResult{PurchaseID: purchaseID, Status: "verified", Entitlements: ent}, nil
}

// PurchaseItem 是 GET /v1/purchases 的一行。
type PurchaseItem struct {
	ID          string  `json:"id"`
	Product     string  `json:"product"`
	AmountMinor *int64  `json:"amountMinor"`
	Currency    *string `json:"currency"`
	PurchasedAt string  `json:"purchasedAt"`
	ExpiresAt   *string `json:"expiresAt"`
}

// hListPurchases 是订单历史，ORDER BY purchased_at DESC，无 LIMIT。
func (a *App) hListPurchases(c *Ctx) (any, error) {
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListPurchasesOfUser(c.R.Context(), a.st.Q(), u.ID)
	if err != nil {
		return nil, err
	}
	out := make([]PurchaseItem, 0, len(rows))
	for _, p := range rows {
		out = append(out, PurchaseItem{
			ID: p.ID, Product: p.ProductName, AmountMinor: p.AmountMinor, Currency: p.Currency,
			PurchasedAt: store.ISO(p.PurchasedAt), ExpiresAt: store.ISOPtr(p.ExpiresAt),
		})
	}
	return map[string]any{"purchases": out}, nil
}
