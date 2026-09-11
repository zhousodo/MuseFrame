package httpapi

import "museframe-api/internal/store"

// ProductItem 是 GET /v1/products 的一行。
//
// 🔴 价格红线（D-14 四重确认）：
//   - priceMinor    = 美分，JSON **整数**，永不为 null
//   - priceCnyMinor = 人民币分，JSON 整数或 **null**
//   - currency 恒为 "USD"，且**只描述 priceMinor**；priceCnyMinor 的币种是隐含
//     的 CNY，**没有对应字段**。任何「按 currency 统一换算」的写法都会把人民币
//     价当成美元算。换算只在客户端 /100，服务端全链路 minor。
type ProductItem struct {
	InternalKey     string  `json:"internalKey"`
	ProductType     string  `json:"productType"`
	DisplayName     string  `json:"displayName"`
	DisplayNameZh   string  `json:"displayNameZh"`
	GrantedUnits    int     `json:"grantedUnits"`
	PriceMinor      int64   `json:"priceMinor"`
	PriceCnyMinor   *int64  `json:"priceCnyMinor"`
	Currency        string  `json:"currency"`
	Period          *string `json:"period"`
	GoogleProductID string  `json:"googleProductId"`
	AppleProductID  string  `json:"appleProductId"`
}

// zhProductNames 对应 Node 版 styles.js 的 PRODUCTS 里的 displayNameZh。
// 库里没有这一列（products 表无 display_name_zh），Node 版也是代码里的静态表，
// 所以这里照搬；命中不到时回落 display_name（与 Node 的 `|| p.display_name` 一致）。
var zhProductNames = map[string]string{
	"pack_10":         "10 张包",
	"pack_30":         "30 张包",
	"pack_100":        "100 张包",
	"creator_monthly": "Creator 月订",
}

func productItem(p store.Product) ProductItem {
	zh := zhProductNames[p.InternalKey]
	if zh == "" {
		zh = p.DisplayName
	}
	google := p.InternalKey
	if p.GoogleProductID != nil && *p.GoogleProductID != "" {
		google = *p.GoogleProductID
	}
	apple := p.InternalKey
	if p.AppleProductID != nil && *p.AppleProductID != "" {
		apple = *p.AppleProductID
	}
	return ProductItem{
		InternalKey: p.InternalKey, ProductType: p.ProductType, DisplayName: p.DisplayName,
		DisplayNameZh: zh, GrantedUnits: p.GrantedUnits,
		PriceMinor: p.PriceMinor, PriceCnyMinor: p.PriceCnyMinor,
		Currency: p.Currency, Period: p.Period,
		GoogleProductID: google, AppleProductID: apple,
	}
}

// hProducts 是 GET /v1/products。
// 🔴 行序是契约：Node 版没有 ORDER BY，靠 SQLite rowid 巧合成序；
// PG 侧由 store.ListActiveProducts 显式固定（见那里的注释与黄金测试）。
func (a *App) hProducts(c *Ctx) (any, error) {
	rows, err := store.ListActiveProducts(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	out := make([]ProductItem, 0, len(rows))
	for _, p := range rows {
		out = append(out, productItem(p))
	}
	return map[string]any{"products": out}, nil
}
