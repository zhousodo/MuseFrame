package httpapi

import (
	"errors"
	"strings"

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
			Group: cfgstore.GroupAuth,
		},
		cfgstore.Setting{
			Key: "play_billing_configured", Value: a.cfg.GoogleServiceAccountJSON != "", Source: "env", Type: "boolean",
			Description: "Google Play 收据校验服务账号是否已配置", RequiresRestart: true, ReadOnly: true,
			Group: cfgstore.GroupAuth,
		},
	)
	settings = append(settings, a.deployOnlySettings()...)
	return AdminConfigResult{
		Settings: settings, Generation: a.generationSummary(c.R.Context()),
		Abuse: abuse, Runtime: a.runtimeSummary(c.R.Context()),
	}, nil
}

// deployOnlySettings 是**部署级**配置的只读视图。
//
// 🔴 为什么要把它们搬到后台来（2026-09-12 盘点结论）：
//
//	注册表里的 27 个键是「热键」——后台能看能改。但 internal/config 里还有
//	十几个同样影响产品行为的值（会话有效期、事件保留期、每账号存储上限、
//	图像引擎走远端还是本地、是否信任 CF-Connecting-IP、测试登录逃生口……），
//	后台对它们**一个字都看不到**。于是「为什么用户 91 天后被登出了」
//	「为什么 /v1/admin/db 里三个月前的事件没了」这类问题，唯一的查法是
//	SSH 上服务器读 project.env —— 而运营没有那台机器的权限。
//
// 🔴 它们一律 ReadOnly + RequiresRestart：这些值在进程启动时读一次就固化了
//
//	（连接池、盐、目录这类东西没法热换），后台能改的假象比看不见更糟 ——
//	改完没生效，而页面显示「已保存」。前端对 readOnly 项不渲染保存按钮
//	（admin.html 的 configRowHtml），所以这里加行不会多出任何写入口。
//
// 🔴 密钥仍然只给「已配置 / 未配置」：ADMIN_TOKEN、IP_HASH_SALT、
//
//	GOOGLE_SERVICE_ACCOUNT_JSON、数据库连接串的**值**一个字节都不进响应。
func (a *App) deployOnlySettings() []cfgstore.Setting {
	ro := func(key string, value any, desc string) cfgstore.Setting {
		return cfgstore.Setting{
			Key: key, Value: value, Source: "env", Description: desc,
			RequiresRestart: true, ReadOnly: true, Group: cfgstore.GroupDeploy,
		}
	}
	// 🔴 刻意不叫 str/num：本包已有一个包级 str()（见 hAdminConfigPut），
	//    同名局部闭包会把它遮掉，是个纯粹自找的阅读陷阱。
	roText := func(key, v, desc string) cfgstore.Setting {
		s := ro(key, v, desc)
		s.Type = string(cfgstore.KindString)
		return s
	}
	roNum := func(key string, v int, desc string) cfgstore.Setting {
		s := ro(key, float64(v), desc)
		s.Type = string(cfgstore.KindNumber)
		return s
	}
	roFlag := func(key string, v bool, desc string) cfgstore.Setting {
		s := ro(key, v, desc)
		s.Type = string(cfgstore.KindBool)
		return s
	}

	out := []cfgstore.Setting{
		roText("deploy_image_provider", a.cfg.ImageProvider,
			"【部署级】图像引擎：remote = 调上游模型，local = 本地像素引擎（IMAGE_PROVIDER）"),
		// 注：session_ttl_days / event_retention_days / idempotency_retention_days /
		// max_job_attempts / max_user_storage_bytes 这五行 2026-09-12 已从部署级只读
		// **升级成注册表热键**（见 cfgstore.Registry），所以这里不再列它们 ——
		// 同一个键同时出现一个可改行和一个只读行，是最容易让运营改错地方的布局。
		roNum("deploy_shutdown_grace_seconds", a.cfg.ShutdownGraceS,
			"【部署级】收到 SIGTERM 后的排空窗口（秒，SHUTDOWN_GRACE_SECONDS）"),
		roNum("deploy_db_pool_max_conns", int(a.cfg.PoolMaxConns),
			"【部署级】数据库连接池上限（统一 PG 硬顶 4，MUSEFRAME_DATABASE_POOL_MAX_CONNS）"),
		roText("deploy_trusted_proxy", a.cfg.TrustedProxy,
			"【部署级】哪些对端允许设置转发头（TRUSTED_PROXY）"),
		roFlag("deploy_trust_cf_connecting_ip", a.cfg.TrustCFIP,
			"【部署级】是否信任 cf-connecting-ip（TRUST_CF_CONNECTING_IP）"),
		roFlag("deploy_play_acknowledge", a.cfg.PlayAcknowledge,
			"【部署级】服务端确认 Google Play 购买（PLAY_ACKNOWLEDGE）"),
		// 🔴 这一项是测试逃生口：开着它 + 管理员身份就能拿到**任意邮箱**的明文
		//    验证码。生产必须为 false，所以它必须在后台一眼能看见。
		roFlag("deploy_allow_test_login", a.cfg.AllowTestLogin,
			"【部署级】🔴 测试登录逃生口（回显明文验证码，生产必须为 false，ALLOW_TEST_LOGIN）"),
		roFlag("deploy_admin_token_configured", a.cfg.AdminToken != "",
			"【部署级】管理令牌是否已配置（值不显示；ADMIN_TOKEN）"),
		roFlag("deploy_ip_hash_salt_configured", a.cfg.IPHashSalt != "",
			"【部署级】per-IP 免费额度盐是否已配置（值不显示；IP_HASH_SALT）"),
		roFlag("deploy_google_web_client_id_configured", a.cfg.GoogleWebClientID != "",
			"【部署级】Google 网页端 Client ID 是否已配置（GOOGLE_WEB_CLIENT_ID）"),
	}

	// 🔴 遗留明文密钥告警。cfgstore.Load 会把 app_config 里 secret 键的行
	//    丢掉（不生效），但**不会删**它们 —— 那几行是 Node 版「后台热改密钥」
	//    留下的明文，仍然随每日备份 tar 落盘。这个信号此前只存在于
	//    Store.SkippedSecretRows 字段里，没有任何人看得到。
	if rows := a.rt.SkippedSecretRows; len(rows) > 0 {
		out = append(out, cfgstore.Setting{
			Key: "leftover_secret_rows_in_db", Value: strings.Join(rows, ","),
			Source: "db", Type: string(cfgstore.KindString), ReadOnly: true, RequiresRestart: false,
			Group: cfgstore.GroupDeploy,
			Description: "🔴 app_config 表里还有这些密钥键的遗留明文行（已不生效，但仍随备份落盘）。" +
				"请直接在库里 DELETE 掉它们。正常情况下这一项应当不出现。",
		})
	}
	return out
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
	// 旧值要在写之前取：审计里「从 3 改成 8」比「改成了 8」有用得多，
	// 而写完之后就再也拿不到旧值了。
	before := a.rt.SettingValue(key)
	if err := a.rt.Set(c.R.Context(), key, value); err != nil {
		if errors.Is(err, cfgstore.ErrSecretNotWritable) {
			return nil, apierr.New(422, apierr.CodeValidation, err.Error())
		}
		return nil, apierr.New(422, apierr.CodeValidation, err.Error())
	}
	// 🔴 审计里只可能出现非密钥键：secret 键在上面那一步已经被
	//    ErrSecretNotWritable 挡成 422 并 return 了，走到这里的 key 一定不是密钥。
	//    （如果哪天放开了 secret 写入，这条审计会变成一个明文密钥的落盘点。）
	logged := a.audit(c, "config.set", map[string]any{
		"key": key, "from": before, "to": a.rt.SettingValue(key), "cleared": value == nil,
	})
	res, err := a.adminConfigResult(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "auditLogged": logged,
		"settings": res.Settings, "generation": res.Generation,
		"abuse": res.Abuse, "runtime": res.Runtime}, nil
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

// MaxPriceMinor 是价格上限（minor 单位）。
//
// 🔴 必须有上限。原来只校验 >= 0，于是「29.99 美元」手滑打成 299900000 会被
// 原样收下并立刻出现在 /v1/products 里。Google Play 那一侧的真实价格是商店配的，
// 所以这个数字不会真的收到钱 —— 它只会让 App 的价目表显示一个荒谬的金额，
// 而最容易被误当成「后端坏了」。上限取 1000 万 minor（10 万美元 / 10 万元）。
const MaxPriceMinor int64 = 10_000_000

// MaxGrantedUnits 是单个商品发放张数上限。同理：手滑多打几个 0 等于白送。
const MaxGrantedUnits int64 = 100_000

// hAdminPatchProduct 改商品：名称 / 张数 / 美元价 / 人民币价 / 商店 SKU / 上下架。
//
// 🔴 priceMinor / priceCnyMinor 是**非负整数的 minor 单位**（美分 / 人民币分），
// 且 currency 列只描述 price_minor —— price_cny_minor 的币种是隐含的 CNY，
// 没有对应列。任何「按 currency 换算」的写法都会把人民币价当成美元算。
func (a *App) hAdminPatchProduct(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	key := c.Params[0]
	before, err := store.GetProductByKey(ctx, a.st.Q(), key)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown product.")
		}
		return nil, err
	}

	var u store.ProductUpdate
	if u.DisplayName, err = adminNonEmptyText(c.Body, "displayName", 80); err != nil {
		return nil, err
	}
	if n, err := adminRangedInt(c.Body, "grantedUnits", 0, MaxGrantedUnits); err != nil {
		return nil, err
	} else if n != nil {
		v := int(*n)
		u.GrantedUnits = &v
	}
	if u.PriceMinor, err = adminRangedInt(c.Body, "priceMinor", 0, MaxPriceMinor); err != nil {
		return nil, err
	}
	// 🔴 priceCnyMinor 的 null 是**有意义**的值（= 不卖人民币），所以它必须走
	//    (set, value) 二元组而不能只看指针。后果不对称得厉害：清成 null 会让这个
	//    商品对**所有中文用户彻底消失**（App 的 offeredProducts() 过滤掉
	//    priceCnyMinor == null 的行），而不是「显示美元价」。
	if raw, ok := c.Body["priceCnyMinor"]; ok {
		u.SetCny = true
		if raw != nil {
			n, err := adminRangedInt(c.Body, "priceCnyMinor", 0, MaxPriceMinor)
			if err != nil {
				return nil, err
			}
			u.PriceCnyMinor = n
		}
	}
	// 商店 SKU 映射：Play / App Store 后台配好的商品 id，App 拿它去发起内购。
	// 空串与 null 都按「清空」处理（见 adminNullableText 的注释）。
	if u.SetGoogleSKU, u.GoogleSKU, err = adminNullableText(c.Body, "googleProductId", 120); err != nil {
		return nil, err
	}
	if u.SetAppleSKU, u.AppleSKU, err = adminNullableText(c.Body, "appleProductId", 120); err != nil {
		return nil, err
	}
	if u.Active, err = optionalBool(c.Body, "active"); err != nil {
		return nil, apierr.New(422, apierr.CodeValidation, "active must be a boolean.")
	}
	if err := store.UpdateProductFields(ctx, a.st, key, u); err != nil {
		return nil, err
	}
	logged := a.audit(c, "product.update", productAuditProps(key, before, u))
	return map[string]any{"ok": true, "auditLogged": logged}, nil
}

// productAuditProps 记下这次改了哪些字段、从什么改成什么。
// 🔴 价格类动作必须同时记旧值：「谁把 pack_100 从 29.99 改成 2.99」是运营事故
// 复盘的第一个问题，而只记新值的审计回答不了它。
func productAuditProps(key string, before *store.Product, u store.ProductUpdate) map[string]any {
	p := map[string]any{"productKey": key}
	if u.DisplayName != nil {
		p["displayName"] = *u.DisplayName
		p["displayNameBefore"] = before.DisplayName
	}
	if u.GrantedUnits != nil {
		p["grantedUnits"] = *u.GrantedUnits
		p["grantedUnitsBefore"] = before.GrantedUnits
	}
	if u.PriceMinor != nil {
		p["priceMinor"] = *u.PriceMinor
		p["priceMinorBefore"] = before.PriceMinor
	}
	if u.SetCny {
		p["priceCnyMinor"] = u.PriceCnyMinor
		p["priceCnyMinorBefore"] = before.PriceCnyMinor
	}
	if u.SetGoogleSKU {
		p["googleProductId"] = u.GoogleSKU
	}
	if u.SetAppleSKU {
		p["appleProductId"] = u.AppleSKU
	}
	if u.Active != nil {
		p["active"] = *u.Active
		p["activeBefore"] = before.Active
	}
	return p
}

// hAdminStyleStatus 是紧急下架 / 重新上架的**单一动作**入口。
//
// 它和 PATCH /v1/admin/styles-admin/{id} 的 status 字段写的是同一列，刻意保留：
// 紧急下架要的是一个按一下就完事的按钮，而不是「在一张有 7 个输入框的表单里
// 把 status 改掉再点保存」——后者在着急的时候会顺手把别的字段一起带上。
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
	logged := a.audit(c, "style.status", map[string]any{"styleId": c.Params[0], "status": status})
	return map[string]any{"ok": true, "auditLogged": logged}, nil
}
