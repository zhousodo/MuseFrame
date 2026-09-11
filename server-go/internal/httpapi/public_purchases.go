package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/ledger"
	"museframe-api/internal/play"
	"museframe-api/internal/store"
)

// VerifyResult 是 POST /v1/purchases/verify 的出参。
type VerifyResult struct {
	PurchaseID   string       `json:"purchaseId"`
	Status       string       `json:"status"`
	Entitlements Entitlements `json:"entitlements"`
}

// assertPurchaseClaim：一笔商店交易永久属于它的第一个账号与商品。
func assertPurchaseClaim(p *store.Purchase, userID, productID string) error {
	if p != nil && (p.UserID != userID || p.ProductID != productID) {
		return apierr.New(409, apierr.CodePurchaseAlreadyClaimed,
			"This purchase is already linked to another account or product.")
	}
	return nil
}

// grantPurchaseUnits 发放一笔购买的额度。
// 商品可能合法地不带额度（只解锁 premium 风格的订阅），而 credit_buckets 有
// CHECK (granted_units > 0)，所以零的情况必须跳过而不是硬试。
func (a *App) grantPurchaseUnits(ctx context.Context, q store.Queryer, userID string, p *store.Product, purchaseID string, expires *time.Time, referenceID *string) error {
	if p.GrantedUnits <= 0 {
		return nil
	}
	unitExpiry := expires
	if p.ProductType == "pack" {
		// 🔴 此前这里是写死的 `90 * 24 * time.Hour`，而 App 的加购卡片文案写的是
		//    「never expire」（web/app.js 的 '{n} artworks · {each} each · never expire'）。
		//    两边对不上：用户买了 100 张，90 天后发现少了一批，而界面从没提过有效期。
		//    现在读注册表热键 pack_credit_expiry_days（0 = 永不过期），运营可以二选一：
		//    设 0 让后端对齐文案，或者保留 90 天并改 App 文案。
		//    nil（永不过期）和 now（发出来就是死的）是两件完全不同的事，
		//    所以 0 必须走 CreditExpiry 里那条返回 nil 的分支。
		unitExpiry = a.rt.CreditExpiry("pack_credit_expiry_days", a.now())
	}
	_, err := ledger.Grant(ctx, q, a.newID, userID, p.GrantedUnits, "purchase", &purchaseID, unitExpiry, referenceID, a.now())
	return err
}

// hVerifyPurchase 校验购买并发放额度。
// 权益只在平台自己的签名记录确认之后才发 —— 客户端说「我买了 Creator」永远不够。
func (a *App) hVerifyPurchase(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	productKey, err := requiredString(c.Body, "productKey", 100)
	if err != nil {
		return nil, err
	}
	purchaseToken, err := optionalString(c.Body, "purchaseToken", 4096)
	if err != nil {
		return nil, err
	}
	transactionID, err := optionalString(c.Body, "transactionId", 128)
	if err != nil {
		return nil, err
	}
	platformPtr, err := optionalString(c.Body, "platform", 20)
	if err != nil {
		return nil, err
	}
	platform := "web"
	if platformPtr != nil && *platformPtr != "" {
		platform = *platformPtr
	}
	if platform != "google" && platform != "apple" && platform != "web" {
		return nil, apierr.New(422, apierr.CodeValidation, "Unsupported platform for verification.")
	}
	product, err := store.GetActiveProductByKey(ctx, a.st.Q(), productKey)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown product.")
		}
		return nil, err
	}

	storeProductID := product.InternalKey
	if platform == "apple" && product.AppleProductID != nil && *product.AppleProductID != "" {
		storeProductID = *product.AppleProductID
	} else if platform == "google" && product.GoogleProductID != nil && *product.GoogleProductID != "" {
		storeProductID = *product.GoogleProductID
	}

	verified := false
	var expiresAt *time.Time
	externalTxID := ""
	if transactionID != nil {
		externalTxID = *transactionID
	} else if purchaseToken != nil {
		externalTxID = *purchaseToken
	}

	switch platform {
	case "google":
		if !a.playClient.Configured() {
			return nil, apierr.New(501, apierr.CodeProviderNotConfigured, "Google Play verification is not configured on the server.")
		}
		if purchaseToken == nil || *purchaseToken == "" {
			return nil, apierr.New(422, apierr.CodeValidation, "purchaseToken is required.")
		}
		kind := "product"
		if product.ProductType == "subscription" {
			kind = "subscription"
		}
		if err := play.AssertToken(*purchaseToken, storeProductID); err != nil {
			return nil, apierr.New(422, apierr.CodeValidation, "purchaseToken is not a valid Google Play token.")
		}
		externalTxID = *purchaseToken
		// 在跟 Play 讲话**之前**先记下这次尝试。此刻 Google 已经收了用户的钱；
		// 如果我们够不到 Play API，必须有一行留给清扫任务重试，否则权益就这么丢了。
		if err := a.recordPendingPurchase(ctx, u.ID, product, platform, externalTxID); err != nil {
			return nil, err
		}
		r, err := a.playClient.Verify(ctx, storeProductID, *purchaseToken, kind)
		if err != nil {
			switch {
			case errors.Is(err, play.ErrNotConfigured):
				return nil, apierr.New(501, apierr.CodeProviderNotConfigured, "Google Play verification is not configured on the server.")
			case errors.Is(err, play.ErrTokenMalformed):
				return nil, apierr.New(422, apierr.CodeValidation, "purchaseToken is not a valid Google Play token.")
			default:
				a.lg.Warn("purchase: Play 校验暂时不可用", nil)
				return nil, apierr.New(503, apierr.CodeVerificationUnavail,
					"The store could not be reached. Your purchase is recorded and will be verified automatically.")
			}
		}
		if !r.Valid {
			_ = store.MarkPurchaseStatus(ctx, a.st.Q(), platform, externalTxID, "invalid")
			return nil, apierr.New(402, apierr.CodePurchaseInvalid, "This purchase is not active.")
		}
		// 优先用 Google 自己的交易号：它是服务端签发的（客户端变不了它），
		// 而且每次续订都往前滚，正是台账要的「按计费周期」的键。
		if r.OrderID != "" && play.OrderRe.MatchString(r.OrderID) {
			id, err := a.adoptCanonicalTxID(ctx, platform, *purchaseToken, r.OrderID, u.ID, product.ID)
			if err != nil {
				return nil, err
			}
			externalTxID = id
		}
		if r.AcknowledgementState == 0 && a.cfg.PlayAcknowledge {
			if err := a.playClient.Acknowledge(ctx, storeProductID, *purchaseToken, kind); err != nil {
				a.lg.Warn("purchase: Play 确认失败", nil)
			}
		}
		verified = true
		expiresAt = r.ExpiresAt

	case "apple":
		return nil, apierr.New(501, apierr.CodeProviderNotConfigured, "App Store verification is not configured on the server.")

	default: // web
		// 🔴 仅限开发。**旗标 + 运维的管理员令牌**双闸：单靠旗标就是一个敞开的
		// 水龙头，任何调用方都能「买」一个点数包、白拿付费额度 ——
		// 那比免费额度循环的窟窿还大。
		if !(a.cfg.AllowMockPurchases && a.isAdminRequest(c.R)) {
			return nil, apierr.New(422, apierr.CodeValidation, "Unsupported platform for verification.")
		}
		verified = true
		if externalTxID == "" {
			externalTxID = "mock_" + a.newID()
		}
		expiresAt = periodExpiry(product, a.now())
	}

	if !verified {
		return nil, apierr.New(402, apierr.CodePurchaseInvalid, "Purchase not verified.")
	}
	return a.finalizePurchase(ctx, u.ID, product, platform, externalTxID, expiresAt)
}

func periodExpiry(p *store.Product, now time.Time) *time.Time {
	if p.Period == nil || *p.Period == "" {
		return nil
	}
	days := 365
	if *p.Period == "month" {
		days = 30
	}
	t := now.Add(time.Duration(days) * 24 * time.Hour)
	return &t
}

var _ = http.StatusOK
