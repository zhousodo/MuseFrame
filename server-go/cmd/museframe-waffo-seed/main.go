// museframe-waffo-seed —— 本地商品 ↔ Waffo Pancake 商品的映射工具。
//
// 两种模式：
//
//  1. **映射（默认）**：把已经在 Dashboard 上建好的 Waffo 商品号写进 products.waffo_product_id。
//     生产四个商品的映射已经固化在 migrations/006_waffo.sql 里，正常情况下**不需要跑本工具**；
//     它留给「后台又上了一个新商品、Dashboard 上手工建好之后要接上」这种场景。
//
//     museframe-waffo-seed -map pack_10=PROD_xxx,pack_30=PROD_yyy [-apply]
//
//  2. **创建（-create）**：对每个 active 且 waffo_product_id 为空的本地商品，用商户 API
//     在 Waffo 上建对应商品（加购包 = 一次性，USD 来自 price_minor、CNY 来自 price_cny_minor；
//     订阅 = 按月，只有 USD），再把返回的 PROD_ 写回。🔴 会真的在 Waffo 上建东西，
//     生产上四个商品已经存在，再跑就是重复商品 —— 只给新商品或新店铺用。
//
//     WAFFO_MERCHANT_ID=… WAFFO_PRIVATE_KEY=… WAFFO_STORE_ID=… \
//     museframe-waffo-seed -create [-apply] [-publish]
//
// 不带 -apply 一律是演练：只打印将要执行的 SQL / 将要发出的请求，不写库、不出网。
// 连接串用 owner 或 app 角色都行（只做 UPDATE products）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"museframe-api/internal/store"
	"museframe-api/internal/waffo"
)

func main() {
	var (
		dsn        = flag.String("dsn", os.Getenv("MUSEFRAME_DATABASE_URL"), "PostgreSQL 连接串（缺省读 MUSEFRAME_DATABASE_URL）")
		mapping    = flag.String("map", "", "映射模式：internal_key=PROD_xxx，逗号分隔")
		create     = flag.Bool("create", false, "创建模式：在 Waffo 上建缺失的商品（🔴 会产生真实商品）")
		publish    = flag.Bool("publish", false, "创建模式：建完后调用 publish-product（用生产 API Key 直接建的商品通常不需要）")
		apply      = flag.Bool("apply", false, "真正写库 / 出网（缺省只演练）")
		successURL = flag.String("success-url", envOr("WAFFO_SUCCESS_URL", "https://museframe.lenscript.cn/app?checkout=success"), "商品级 successUrl（创建模式）")
	)
	flag.Parse()
	if *dsn == "" {
		fail(2, "缺少 -dsn / MUSEFRAME_DATABASE_URL")
	}
	if (*mapping == "") == !*create {
		fail(2, "用法：museframe-waffo-seed -map key=PROD_xxx,... [-apply]   或   museframe-waffo-seed -create [-apply] [-publish]")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, *dsn, 2)
	if err != nil {
		fail(1, "连接数据库失败")
	}
	defer st.Close()

	products, err := store.ListAllProducts(ctx, st.Q())
	if err != nil {
		fail(1, "读取 products 失败: "+err.Error())
	}
	byKey := map[string]store.Product{}
	for _, p := range products {
		byKey[p.InternalKey] = p
	}

	if *mapping != "" {
		runMap(ctx, st, byKey, *mapping, *apply)
		return
	}
	runCreate(ctx, st, products, *successURL, *apply, *publish)
}

func runMap(ctx context.Context, st *store.Store, byKey map[string]store.Product, mapping string, apply bool) {
	type pair struct{ key, id string }
	var pairs []pair
	for _, item := range strings.Split(mapping, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" || !strings.HasPrefix(strings.TrimSpace(kv[1]), "PROD_") {
			fail(2, "映射项格式应为 internal_key=PROD_xxx，实得 "+item)
		}
		pairs = append(pairs, pair{strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
	for _, pr := range pairs {
		p, ok := byKey[pr.key]
		if !ok {
			fail(2, "本地没有 internal_key="+pr.key+" 的商品")
		}
		current := "<NULL>"
		if p.WaffoProductID != nil {
			current = *p.WaffoProductID
		}
		fmt.Printf("%-16s %s -> %s   (UPDATE products SET waffo_product_id = '%s' WHERE id = '%s')\n",
			pr.key, current, pr.id, pr.id, p.ID)
		if !apply {
			continue
		}
		if err := store.SetProductWaffoID(ctx, st.Q(), p.ID, pr.id); err != nil {
			fail(1, "写入失败: "+err.Error())
		}
	}
	if !apply {
		fmt.Println("（演练：未写库。加 -apply 执行。）")
	}
}

func runCreate(ctx context.Context, st *store.Store, products []store.Product, successURL string, apply, publish bool) {
	storeID := strings.TrimSpace(os.Getenv("WAFFO_STORE_ID"))
	client := waffo.New(waffo.Config{
		MerchantID: os.Getenv("WAFFO_MERCHANT_ID"), PrivateKeyPEM: os.Getenv("WAFFO_PRIVATE_KEY"),
		BaseURL: os.Getenv("WAFFO_API_BASE_URL"),
	})
	if apply && (!client.Configured() || storeID == "") {
		fail(2, "创建模式需要 WAFFO_MERCHANT_ID / WAFFO_PRIVATE_KEY / WAFFO_STORE_ID")
	}
	if storeID == "" {
		storeID = "STO_<未设置>"
	}
	pending := 0
	for _, p := range products {
		if !p.Active || (p.WaffoProductID != nil && *p.WaffoProductID != "") {
			continue
		}
		pending++
		name := p.DisplayName + " · MuseFrame"
		if len([]rune(name)) > 64 {
			name = string([]rune(name)[:64])
		}
		meta := map[string]string{"productKey": p.InternalKey, "grantedUnits": fmt.Sprint(p.GrantedUnits)}
		var (
			req  any
			path string
			sub  = p.ProductType == "subscription"
		)
		if sub {
			period := "monthly"
			if p.Period != nil && *p.Period == "year" {
				period = "yearly"
			}
			req = waffo.SubscriptionProductRequest{
				StoreID: storeID, Name: name, BillingPeriod: period,
				Prices: map[string]waffo.Price{
					"USD": {Amount: waffo.FormatAmount(p.PriceMinor, "USD"), TaxIncluded: true, TaxCategory: "saas"},
				},
				Description: fmt.Sprintf("%d artworks every %s · all directions · high tier", p.GrantedUnits, strings.TrimSuffix(period, "ly")),
				SuccessURL:  successURL, Metadata: meta,
			}
			path = "/v1/actions/subscription-product/create-product"
		} else {
			prices := map[string]waffo.Price{
				"USD": {Amount: waffo.FormatAmount(p.PriceMinor, "USD"), TaxIncluded: true, TaxCategory: "digital_goods"},
			}
			if p.PriceCnyMinor != nil && *p.PriceCnyMinor > 0 {
				prices["CNY"] = waffo.Price{Amount: waffo.FormatAmount(*p.PriceCnyMinor, "CNY"), TaxIncluded: true, TaxCategory: "digital_goods"}
			}
			req = waffo.OnetimeProductRequest{
				StoreID: storeID, Name: name, Prices: prices,
				Description: fmt.Sprintf("%d artworks · never expire", p.GrantedUnits),
				SuccessURL:  successURL, Metadata: meta,
			}
			path = "/v1/actions/onetime-product/create-product"
		}
		body, _ := json.MarshalIndent(req, "", "  ")
		fmt.Printf("== %s (%s)\nPOST %s\n%s\n", p.InternalKey, p.ProductType, path, body)
		if !apply {
			continue
		}
		var created *waffo.Product
		var err error
		if sub {
			created, err = client.CreateSubscriptionProduct(ctx, req.(waffo.SubscriptionProductRequest))
		} else {
			created, err = client.CreateOnetimeProduct(ctx, req.(waffo.OnetimeProductRequest))
		}
		if err != nil {
			fail(1, "创建 "+p.InternalKey+" 失败: "+err.Error())
		}
		fmt.Printf("   -> %s (status=%s)\n", created.ID, created.Status)
		if publish {
			if _, err := client.PublishProduct(ctx, created.ID, sub); err != nil && !waffo.IsAlreadyPublished(err) {
				fail(1, "发布 "+created.ID+" 失败: "+err.Error())
			}
			fmt.Printf("   -> published\n")
		}
		if err := store.SetProductWaffoID(ctx, st.Q(), p.ID, created.ID); err != nil {
			fail(1, "回填 "+p.InternalKey+" 失败（🔴 Waffo 上已建好 "+created.ID+"，请用 -map 手工接上）: "+err.Error())
		}
	}
	if pending == 0 {
		fmt.Println("所有 active 商品都已有 waffo_product_id，无事可做。")
	} else if !apply {
		fmt.Println("（演练：未出网、未写库。加 -apply 执行。）")
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func fail(code int, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}
