package httpapi

import (
	"errors"

	"museframe-api/internal/apierr"
	"museframe-api/internal/cfgstore"
	"museframe-api/internal/store"
)

func (a *App) adminConfigResult(c *Ctx) (AdminConfigResult, error) {
	abuse, err := a.abuseSummary(c.R.Context())
	if err != nil {
		return AdminConfigResult{}, err
	}
	settings := a.rt.List()
	// 末尾额外附两个只读合成项（与 Node 版一致）。
	settings = append(settings,
		cfgstore.Setting{
			Key: "allow_mock_purchases", Value: a.cfg.AllowMockPurchases, Source: "env", Type: "boolean",
			Description: "演示购买开关（生产环境必须为 false）", RequiresRestart: true, ReadOnly: true,
		},
		cfgstore.Setting{
			Key: "play_billing_configured", Value: a.cfg.GoogleServiceAccountJSON != "", Source: "env", Type: "boolean",
			Description: "Google Play 收据校验服务账号是否已配置", RequiresRestart: true, ReadOnly: true,
		},
	)
	return AdminConfigResult{Settings: settings, Generation: a.generationSummary(c.R.Context()), Abuse: abuse}, nil
}

func (a *App) hAdminConfigGet(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	return a.adminConfigResult(c)
}

// hAdminConfigPut 热改一个运行时配置项。
//
// 🔴 这条在 Node 版是「上游 API Key 明文进库」的**写入口**。
// Go 版对 secret 项直接拒绝（cfgstore.ErrSecretNotWritable -> 422），
// 密钥只能走环境变量 / project.env。这属于有意的行为变更，已在 README 登记。
func (a *App) hAdminConfigPut(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	key := str(c.Body["key"])
	value, present := c.Body["value"]
	if !present {
		value = nil
	}
	if err := a.rt.Set(c.R.Context(), key, value); err != nil {
		if errors.Is(err, cfgstore.ErrSecretNotWritable) {
			return nil, apierr.New(422, apierr.CodeValidation, err.Error())
		}
		return nil, apierr.New(422, apierr.CodeValidation, err.Error())
	}
	res, err := a.adminConfigResult(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "settings": res.Settings, "generation": res.Generation, "abuse": res.Abuse}, nil
}

// AdminProductItem 是 GET /v1/admin/products-admin 的一行。
// active 是**布尔**不是 0/1；价格与 /v1/products 同为 minor 整数。
type AdminProductItem struct {
	ID              string  `json:"id"`
	InternalKey     string  `json:"internalKey"`
	ProductType     string  `json:"productType"`
	DisplayName     string  `json:"displayName"`
	GrantedUnits    int     `json:"grantedUnits"`
	PriceMinor      int64   `json:"priceMinor"`
	PriceCnyMinor   *int64  `json:"priceCnyMinor"`
	Currency        string  `json:"currency"`
	Period          *string `json:"period"`
	Active          bool    `json:"active"`
	GoogleProductID *string `json:"googleProductId"`
	AppleProductID  *string `json:"appleProductId"`
}

func (a *App) hAdminProducts(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListAllProducts(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	out := make([]AdminProductItem, 0, len(rows))
	for _, p := range rows {
		out = append(out, AdminProductItem{
			ID: p.ID, InternalKey: p.InternalKey, ProductType: p.ProductType, DisplayName: p.DisplayName,
			GrantedUnits: p.GrantedUnits, PriceMinor: p.PriceMinor, PriceCnyMinor: p.PriceCnyMinor,
			Currency: p.Currency, Period: p.Period, Active: p.Active,
			GoogleProductID: p.GoogleProductID, AppleProductID: p.AppleProductID,
		})
	}
	return map[string]any{"products": out}, nil
}

// hAdminPatchProduct 改价 / 上下架。
// 🔴 priceMinor / priceCnyMinor 必须是**非负整数**（minor 单位）。
func (a *App) hAdminPatchProduct(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	key := c.Params[0]
	if _, err := store.GetProductByKey(ctx, a.st.Q(), key); err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown product.")
		}
		return nil, err
	}
	var grantedUnits *int
	if raw, ok := c.Body["grantedUnits"]; ok && raw != nil {
		n, _, err := optionalInt(c.Body, "grantedUnits")
		if err != nil || n == nil || *n < 0 {
			return nil, apierr.New(422, apierr.CodeValidation, "grantedUnits must be an integer >= 0.")
		}
		v := int(*n)
		grantedUnits = &v
	}
	var priceMinor *int64
	if raw, ok := c.Body["priceMinor"]; ok && raw != nil {
		n, _, err := optionalInt(c.Body, "priceMinor")
		if err != nil || n == nil || *n < 0 {
			return nil, apierr.New(422, apierr.CodeValidation, "priceMinor must be an integer >= 0.")
		}
		priceMinor = n
	}
	setCny := false
	var priceCny *int64
	if raw, ok := c.Body["priceCnyMinor"]; ok {
		setCny = true
		if raw != nil {
			n, _, err := optionalInt(c.Body, "priceCnyMinor")
			if err != nil || n == nil || *n < 0 {
				return nil, apierr.New(422, apierr.CodeValidation, "priceCnyMinor must be an integer >= 0 or null.")
			}
			priceCny = n
		}
	}
	active, err := optionalBool(c.Body, "active")
	if err != nil {
		return nil, apierr.New(422, apierr.CodeValidation, "active must be a boolean.")
	}
	if err := store.UpdateProductFields(ctx, a.st.Q(), key, grantedUnits, priceMinor, setCny, priceCny, active); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// AdminStyleItem 是 GET /v1/admin/styles-admin 的一行（含未发布风格）。
type AdminStyleItem struct {
	ID          string `json:"id"`
	InternalKey string `json:"internalKey"`
	Name        string `json:"name"`
	Theme       string `json:"theme"`
	Status      string `json:"status"`
	Premium     bool   `json:"premium"`
	Jobs        int    `json:"jobs"`
}

func (a *App) hAdminStyles(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListAdminStyles(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	out := make([]AdminStyleItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, AdminStyleItem(r))
	}
	return map[string]any{"styles": out}, nil
}

// hAdminStyleStatus 是紧急下架 / 重新上架。
// 目录查询已经过滤 status='published'，所以 disabled 会立刻在所有地方隐藏该风格。
func (a *App) hAdminStyleStatus(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	status := str(c.Body["status"])
	if status != "published" && status != "disabled" {
		return nil, apierr.New(422, apierr.CodeValidation, "status must be 'published' or 'disabled'.")
	}
	ok, err := store.SetStyleStatus(c.R.Context(), a.st.Q(), c.Params[0], status)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("Unknown style.")
	}
	return map[string]any{"ok": true}, nil
}
