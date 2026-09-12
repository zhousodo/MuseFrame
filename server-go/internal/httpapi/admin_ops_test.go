package httpapi

import (
	"strings"
	"testing"

	"museframe-api/internal/aigc"
	"museframe-api/internal/cfgstore"
	"museframe-api/internal/store"
)

// 🔴 这一条是本轮最重要的测试：在 2026-09-12 之前它**必然失败**。
//
// users.status 这一列从第一版 schema 起就存在（DEFAULT 'active'），但没有任何
// 代码读过它 —— 把它改成 suspended，被封的账号照样能登录、生成、花额度。
// 「禁用用户」因此不是「缺一个后台按钮」，而是整条判据不存在。
//
// 变异验证：把 auth.go 里 `if user.Status != UserStatusActive` 那一段删掉，
// 这条测试的第 ② 步会拿到 200 而不是 401。
func TestSuspendedUserTokenRejectedAndRestorable(t *testing.T) {
	e := newTestEnv(t)
	uid, tok := e.signUp("suspend-me@example.com")

	// ① 正常账号：带令牌能读到自己的权益。
	if r := e.do("GET", "/v1/entitlements/me", nil, bearer(tok)); r.Code != 200 {
		t.Fatalf("封禁前应 200，实际 %d %s", r.Code, r.Body)
	}

	// 封禁必须填原因 —— 不填直接 422。
	r := e.do("POST", "/v1/admin/users/"+uid+"/status",
		map[string]any{"status": "suspended"}, e.admin())
	if r.Code != 422 {
		t.Fatalf("🔴 禁用不填原因必须 422（三个月后没人说得清为什么这人进不来），实际 %d %s", r.Code, r.Body)
	}
	r = e.do("POST", "/v1/admin/users/"+uid+"/status",
		map[string]any{"status": "banned-lol", "reason": "x"}, e.admin())
	if r.Code != 422 {
		t.Fatalf("未知 status 必须 422，实际 %d %s", r.Code, r.Body)
	}

	// ② 封禁：所有带令牌的接口立刻按「未登录」处理。
	r = e.do("POST", "/v1/admin/users/"+uid+"/status",
		map[string]any{"status": "suspended", "reason": "刷单：同一设备 40 次免费额度"}, e.admin())
	if r.Code != 200 {
		t.Fatalf("禁用应 200，实际 %d %s", r.Code, r.Body)
	}
	m := r.Map(t)
	if m["auditLogged"] != true {
		t.Error("禁用必须留痕（auditLogged 应为 true）")
	}
	if m["sessionsRevoked"] != false {
		t.Error("会话行刻意不删，响应必须如实说明（否则运营会以为解封后用户要重新登录）")
	}
	if r := e.do("GET", "/v1/entitlements/me", nil, bearer(tok)); r.Code != 401 {
		t.Fatalf("🔴 禁用后原令牌必须 401 —— 实际 %d %s。"+
			"users.status 没有被 authenticate() 读的话，这一步会拿到 200，"+
			"也就是说「禁用」在面板上看起来成功了而那个人什么都没被挡住", r.Code, r.Body)
	}
	// 生成入口同样被挡（它走的是 requireAccount，底下还是 authenticate）。
	if r := e.do("GET", "/v1/projects", nil, bearer(tok)); r.Code != 401 {
		t.Fatalf("禁用后作品列表也必须 401，实际 %d", r.Code)
	}

	// ③ 解封：**原令牌**立刻恢复可用，不需要重新登录。
	r = e.do("POST", "/v1/admin/users/"+uid+"/status",
		map[string]any{"status": "active", "reason": "误判，已核实"}, e.admin())
	if r.Code != 200 {
		t.Fatalf("启用应 200，实际 %d %s", r.Code, r.Body)
	}
	if r := e.do("GET", "/v1/entitlements/me", nil, bearer(tok)); r.Code != 200 {
		t.Fatalf("🔴 解封后原令牌必须立刻可用（会话没删），实际 %d %s", r.Code, r.Body)
	}

	// ④ 不存在的用户 404，而不是静默成功。
	if r := e.do("POST", "/v1/admin/users/no-such-user/status",
		map[string]any{"status": "active"}, e.admin()); r.Code != 404 {
		t.Fatalf("未知用户应 404，实际 %d %s", r.Code, r.Body)
	}

	// ⑤ 审计里必须能查到这两次动作，且原因在里面。
	au := e.do("GET", "/v1/admin/audit", nil, e.admin()).Map(t)
	rows, _ := au["audit"].([]any)
	var sawSuspend bool
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["action"] != "user.status" {
			continue
		}
		props, _ := row["props"].(map[string]any)
		if props["to"] == "suspended" && strings.Contains(str(props["reason"]), "刷单") {
			sawSuspend = true
			if props["from"] != "active" {
				t.Errorf("审计必须记下原状态，实际 %v", props["from"])
			}
		}
	}
	if !sawSuspend {
		t.Fatalf("🔴 审计里找不到那次禁用 —— 封禁理由只存在于操作者的记忆里了。audit=%v", au["audit"])
	}

	// ⑥ 用户列表要带 status，否则后台看不出谁被封了。
	us := e.do("GET", "/v1/admin/users", nil, e.admin()).Map(t)
	ulist, _ := us["users"].([]any)
	if len(ulist) == 0 {
		t.Fatal("用户列表不该为空")
	}
	if _, ok := ulist[0].(map[string]any)["status"]; !ok {
		t.Error("用户列表必须带 status 列")
	}
}

// 封禁不碰额度台账与作品：它是「挡住登录」，不是「删账号」。
// 🔴 这条是防止将来有人「顺手」在封禁里加一句 DELETE —— 封禁有很大概率是误判。
func TestSuspendKeepsLedgerAndProjects(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, _, _ := e.prepareJobInputs("suspend-keeps@example.com", 5)

	before, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if before <= 0 {
		t.Fatalf("前置条件坏了：余额应 > 0，实际 %d", before)
	}
	if r := e.do("POST", "/v1/admin/users/"+uid+"/status",
		map[string]any{"status": "suspended", "reason": "测试"}, e.admin()); r.Code != 200 {
		t.Fatalf("禁用应 200，实际 %d %s", r.Code, r.Body)
	}
	after, err := store.AvailableUnits(ctx, e.st.Q(), uid, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("🔴 禁用不该动额度（%d → %d）——封禁是可逆动作，额度没法还回来", before, after)
	}
}

// 后台改 image_size_* 之后，**下一个任务发给上游的 size 字段**必须跟着变。
//
// 🔴 变异验证：把 edit.go 里的 a.PickSize(...) 换回包级的定值，
// 或者把 PickSize 里读 cfg 的那一行去掉，这条测试的第 ② 步会继续拿到 1024x1536。
// 那正是 2026-09-12 之前的状态：面板上看起来改了尺寸，上游收到的还是老的。
func TestImageSizeHotConfigChangesUpstreamRequest(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()

	// ① 默认：4:5 → 竖图 → 1024x1536（与改造前写死的值逐字一致）。
	if got := e.prov.PickSize("4:5", 1000, 1000); got != "1024x1536" {
		t.Fatalf("默认竖图尺寸必须保持 1024x1536，实际 %q", got)
	}

	// ② 后台改成方图尺寸（走真正的 PUT /v1/admin/config，连校验一起过一遍）。
	r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "image_size_portrait", "value": "1024x1024"}, e.admin())
	if r.Code != 200 {
		t.Fatalf("保存应 200，实际 %d %s", r.Code, r.Body)
	}
	if got := e.prov.PickSize("4:5", 1000, 1000); got != "1024x1024" {
		t.Fatalf("🔴 改完之后下一个任务就该用新尺寸，实际 %q —— "+
			"这一项不热生效的话，降低分辨率/降本只能靠发版", got)
	}
	// 质量档位同理（它和尺寸一起决定上游单价）。
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "image_quality_standard", "value": "low"}, e.admin()); r.Code != 200 {
		t.Fatalf("保存应 200，实际 %d %s", r.Code, r.Body)
	}
	if got := e.prov.QualityFor("standard"); got != "low" {
		t.Fatalf("质量档位应热生效为 low，实际 %q", got)
	}

	// ③ 白名单外的尺寸必须 422，且**不改变生效值**。
	//    它直接进 multipart 的 size 字段：写错一个值就是所有生成都 400 失败，
	//    而面板上那一行看起来完全正常。
	r = e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "image_size_portrait", "value": "4096x4096"}, e.admin())
	if r.Code != 422 {
		t.Fatalf("🔴 白名单外的尺寸必须 422，实际 %d %s", r.Code, r.Body)
	}
	if got := e.prov.PickSize("4:5", 1000, 1000); got != "1024x1024" {
		t.Fatalf("被拒的保存不该改变生效值，实际 %q", got)
	}

	// ④ 恢复默认（value:null）之后回到 1024x1536。
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "image_size_portrait", "value": nil}, e.admin()); r.Code != 200 {
		t.Fatalf("恢复默认应 200，实际 %d %s", r.Code, r.Body)
	}
	if got := e.prov.PickSize("4:5", 1000, 1000); got != "1024x1536" {
		t.Fatalf("恢复默认后应回到 1024x1536，实际 %q", got)
	}
	_ = e.rt.Set(ctx, "image_quality_standard", nil)

	// ⑤ 熔断阈值与重试次数同样热生效（它们都是真金白银的闸）。
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "max_job_attempts", "value": float64(1)}, e.admin()); r.Code != 200 {
		t.Fatalf("保存应 200，实际 %d %s", r.Code, r.Body)
	}
	if got := e.wk.MaxAttempts(); got != 1 {
		t.Fatalf("🔴 重试次数必须热生效（上游按次计费炸了要能立刻压到 1），实际 %d", got)
	}
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "provider_breaker_cooldown_seconds", "value": float64(300)}, e.admin()); r.Code != 200 {
		t.Fatalf("保存应 200，实际 %d %s", r.Code, r.Body)
	}
	if got := e.prov.BreakerCooldown().Seconds(); got != 300 {
		t.Fatalf("熔断冷却应热生效为 300s，实际 %v", got)
	}
	_ = e.rt.Set(ctx, "max_job_attempts", nil)
	_ = e.rt.Set(ctx, "provider_breaker_cooldown_seconds", nil)
}

// 配置出参必须带分组，且此前的部署级只读行里那四个已升级的键**不再出现** ——
// 同一个键既有可改行又有只读行，是最容易让运营改错地方的布局。
func TestAdminConfigGroupsAndNoDuplicateRows(t *testing.T) {
	e := newTestEnv(t)
	var out AdminConfigResult
	e.do("GET", "/v1/admin/config", nil, e.admin()).JSON(t, &out)

	seen := map[string]int{}
	groups := map[string]int{}
	for _, s := range out.Settings {
		seen[s.Key]++
		// 🔴 **每一项**都必须有分组，合成的只读行也算。前端按分组渲染小节：
		//    没有分组的项会掉进一个没有说明文案的兜底小节里，而那正是
		//    「面板上有这一行但没人知道它管什么」的来源。
		if s.Group == "" {
			t.Errorf("🔴 配置项 %s 没有 group —— 它会落进前端的兜底小节（无说明文案）", s.Key)
			continue
		}
		groups[s.Group]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("🔴 配置项 %s 在出参里出现了 %d 次 —— 面板上会有两个输入框写同一个值", k, n)
		}
	}
	// 升级成热键的那四个：热键名必须在，旧的 deploy_ 只读名必须不在。
	for _, k := range []string{"session_ttl_days", "event_retention_days",
		"idempotency_retention_days", "max_job_attempts"} {
		if seen[k] == 0 {
			t.Errorf("热键 %s 应出现在配置清单里", k)
		}
		if seen["deploy_"+k] > 0 {
			t.Errorf("🔴 deploy_%s 不该还在 —— 它已经升级成热键，留着就是第二个编辑入口", k)
		}
	}
	for _, g := range []string{cfgstore.GroupGeneration, cfgstore.GroupCredits,
		cfgstore.GroupAuth, cfgstore.GroupRetention, cfgstore.GroupEmail,
		cfgstore.GroupStorage, cfgstore.GroupSupport, cfgstore.GroupDeploy} {
		if groups[g] == 0 {
			t.Errorf("分组 %s 里一项都没有 —— 面板上会出现一个空小节", g)
		}
	}
}

// 风格完整编辑：校验、落库、以及「为什么我上线了 App 里还看不到」这个判据。
func TestAdminStylePatchAndVisibility(t *testing.T) {
	e := newTestEnv(t)

	list := e.do("GET", "/v1/admin/styles-admin", nil, e.admin()).Map(t)
	styles, _ := list["styles"].([]any)
	if len(styles) != 3 {
		t.Fatalf("播种了 3 个风格，实际 %d", len(styles))
	}
	first, _ := styles[0].(map[string]any)
	if first["visibleInApp"] != true {
		t.Fatalf("播种的风格应当对 App 可见，实际 %v / hiddenReason=%v",
			first["visibleInApp"], first["hiddenReason"])
	}
	// 位次与展览信息必须回出来：排序键是 (editorial_rank, position)，
	// 后台看不到它们就没法回答「为什么这个风格排在第 5 位」。
	if first["position"] == nil || first["exhibitionTitle"] == nil {
		t.Errorf("风格行必须带 position 与 exhibitionTitle，实际 %v", first)
	}

	// ① 校验：空名字、超长名字、坏 status、坏位次、坏标签一律 422。
	for _, bad := range []map[string]any{
		{"name": "   "},
		{"name": strings.Repeat("字", 81)},
		{"status": "archived"},
		{"position": float64(-1)},
		{"position": float64(1.5)},
		{"tags": "portrait"},
		{"tags": []any{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
		{"shortCaption": strings.Repeat("x", 141)},
		{"premium": "yes"},
	} {
		r := e.do("PATCH", "/v1/admin/styles-admin/style-free", bad, e.admin())
		if r.Code != 422 {
			t.Errorf("🔴 %v 应被拒（422），实际 %d %s", bad, r.Code, r.Body)
		}
	}

	// ② 正常编辑：名称可以直接改成中文（schema 里只有 public_name 一列，
	//    没有多语言列 —— 这就是后台唯一能做的「本地化」）。
	r := e.do("PATCH", "/v1/admin/styles-admin/style-free", map[string]any{
		"name": "柔光窗边", "shortCaption": "靠窗的自然光", "theme": "quiet-portraits",
		"premium": true, "position": float64(7),
		"tags": []any{"portrait", "portrait", " pet "},
	}, e.admin())
	if r.Code != 200 {
		t.Fatalf("编辑应 200，实际 %d %s", r.Code, r.Body)
	}
	got := r.Map(t)
	st, _ := got["style"].(map[string]any)
	if st["name"] != "柔光窗边" || st["shortCaption"] != "靠窗的自然光" {
		t.Fatalf("出参必须是回读库里的值，实际 %v", st)
	}
	if st["premium"] != true {
		t.Errorf("premium 应为 true，实际 %v", st["premium"])
	}
	if st["position"] != float64(7) {
		t.Errorf("position 应为 7，实际 %v", st["position"])
	}
	tags, _ := st["tags"].([]any)
	if len(tags) != 2 || tags[0] != "portrait" || tags[1] != "pet" {
		t.Errorf("tags 应去重 + trim 成 [portrait pet]，实际 %v", tags)
	}
	if got["auditLogged"] != true {
		t.Error("编辑风格必须留痕")
	}

	// ③ 改动立刻反映到 /v1/styles（premium:true 让它对免费账号上锁）。
	_, tok := e.signUp("style-edit@example.com")
	pub := e.do("GET", "/v1/styles", nil, bearer(tok)).Map(t)
	cards, _ := pub["styles"].([]any)
	var found bool
	for _, raw := range cards {
		c, _ := raw.(map[string]any)
		if c["styleId"] != "style-free" {
			continue
		}
		found = true
		if c["name"] != "柔光窗边" {
			t.Errorf("🔴 /v1/styles 应立刻返回新名字，实际 %v", c["name"])
		}
		if c["lockedForUser"] != true {
			t.Errorf("改成 premium 后免费账号应看到上锁，实际 %v", c["lockedForUser"])
		}
	}
	if !found {
		t.Fatal("/v1/styles 里找不到 style-free")
	}

	// ④ 下架之后 /v1/styles **立刻**不含它（这是紧急下架的核心保证）。
	if r := e.do("PATCH", "/v1/admin/styles-admin/style-free",
		map[string]any{"status": "disabled"}, e.admin()); r.Code != 200 {
		t.Fatalf("下架应 200，实际 %d %s", r.Code, r.Body)
	}
	pub = e.do("GET", "/v1/styles", nil, bearer(tok)).Map(t)
	cards, _ = pub["styles"].([]any)
	for _, raw := range cards {
		if c, _ := raw.(map[string]any); c["styleId"] == "style-free" {
			t.Fatal("🔴 下架后 /v1/styles 仍然含这个风格")
		}
	}
	// 后台那一行要把「为什么 App 里看不到」说出来。
	list = e.do("GET", "/v1/admin/styles-admin", nil, e.admin()).Map(t)
	styles, _ = list["styles"].([]any)
	for _, raw := range styles {
		s, _ := raw.(map[string]any)
		if s["id"] != "style-free" {
			continue
		}
		if s["visibleInApp"] != false || s["hiddenReason"] == nil {
			t.Fatalf("下架的风格必须带 hiddenReason，实际 %v", s)
		}
	}

	// ⑤ 未知风格 404。
	if r := e.do("PATCH", "/v1/admin/styles-admin/no-such-style",
		map[string]any{"premium": false}, e.admin()); r.Code != 404 {
		t.Fatalf("未知风格应 404，实际 %d %s", r.Code, r.Body)
	}
}

// 商品完整编辑：价格上限、人民币价的 null 语义、商店 SKU、以及立刻反映到 /v1/products。
func TestAdminProductPatchFullFields(t *testing.T) {
	e := newTestEnv(t)

	// ① 校验：负价、超上限、非整数、坏类型一律 422。
	for _, bad := range []map[string]any{
		{"priceMinor": float64(-1)},
		{"priceMinor": float64(MaxPriceMinor + 1)},
		{"priceMinor": 12.5},
		{"grantedUnits": float64(-1)},
		{"grantedUnits": float64(MaxGrantedUnits + 1)},
		{"displayName": ""},
		{"displayName": strings.Repeat("名", 81)},
		{"googleProductId": strings.Repeat("s", 121)},
		{"active": "true"},
	} {
		r := e.do("PATCH", "/v1/admin/products-admin/pack_10", bad, e.admin())
		if r.Code != 422 {
			t.Errorf("🔴 %v 应被拒（422），实际 %d %s", bad, r.Code, r.Body)
		}
	}

	// ② 改价 + 改名 + 改 SKU，一次过；/v1/products 必须立刻反映。
	r := e.do("PATCH", "/v1/admin/products-admin/pack_10", map[string]any{
		"displayName": "十张装", "priceMinor": float64(599), "priceCnyMinor": float64(3900),
		"grantedUnits": float64(12), "googleProductId": "mf_pack_10_v2",
	}, e.admin())
	if r.Code != 200 {
		t.Fatalf("改价应 200，实际 %d %s", r.Code, r.Body)
	}
	if r.Map(t)["auditLogged"] != true {
		t.Error("改价必须留痕")
	}
	prods := e.do("GET", "/v1/products", nil, nil).Map(t)
	items, _ := prods["products"].([]any)
	var hit map[string]any
	for _, raw := range items {
		p, _ := raw.(map[string]any)
		if p["internalKey"] == "pack_10" {
			hit = p
		}
	}
	if hit == nil {
		t.Fatal("/v1/products 里找不到 pack_10")
	}
	if hit["priceMinor"] != float64(599) || hit["priceCnyMinor"] != float64(3900) {
		t.Fatalf("🔴 /v1/products 应立刻反映新价格，实际 %v / %v", hit["priceMinor"], hit["priceCnyMinor"])
	}
	if hit["displayName"] != "十张装" || hit["grantedUnits"] != float64(12) {
		t.Fatalf("名称与张数也应立刻生效，实际 %v / %v", hit["displayName"], hit["grantedUnits"])
	}

	// ③ 🔴 人民币价显式清成 null：App 的 offeredProducts() 会把这个商品对
	//    **所有中文用户**整条隐藏掉。语义必须是「写 NULL」而不是「不改」。
	if r := e.do("PATCH", "/v1/admin/products-admin/pack_10",
		map[string]any{"priceCnyMinor": nil}, e.admin()); r.Code != 200 {
		t.Fatalf("清空人民币价应 200，实际 %d %s", r.Code, r.Body)
	}
	prods = e.do("GET", "/v1/products", nil, nil).Map(t)
	items, _ = prods["products"].([]any)
	for _, raw := range items {
		p, _ := raw.(map[string]any)
		if p["internalKey"] != "pack_10" {
			continue
		}
		if p["priceCnyMinor"] != nil {
			t.Fatalf("🔴 清空后必须是 null（不是 0，也不是保持原值），实际 %v", p["priceCnyMinor"])
		}
		// 美元价不该被这次动作带走。
		if p["priceMinor"] != float64(599) {
			t.Fatalf("只改人民币价不该动美元价，实际 %v", p["priceMinor"])
		}
	}

	// ④ 下架之后 /v1/products 立刻不含它。
	if r := e.do("PATCH", "/v1/admin/products-admin/pack_10",
		map[string]any{"active": false}, e.admin()); r.Code != 200 {
		t.Fatalf("下架应 200，实际 %d %s", r.Code, r.Body)
	}
	prods = e.do("GET", "/v1/products", nil, nil).Map(t)
	items, _ = prods["products"].([]any)
	for _, raw := range items {
		if p, _ := raw.(map[string]any); p["internalKey"] == "pack_10" {
			t.Fatal("🔴 下架后 /v1/products 仍然含这个商品")
		}
	}
	// 后台列表仍然能看到它（含下架），否则就再也上不回去了。
	adm := e.do("GET", "/v1/admin/products-admin", nil, e.admin()).Map(t)
	admItems, _ := adm["products"].([]any)
	var sawDisabled bool
	for _, raw := range admItems {
		p, _ := raw.(map[string]any)
		if p["internalKey"] == "pack_10" {
			sawDisabled = true
			if p["googleProductId"] != "mf_pack_10_v2" {
				t.Errorf("商店 SKU 应已更新，实际 %v", p["googleProductId"])
			}
		}
	}
	if !sawDisabled {
		t.Fatal("后台商品列表必须含下架商品")
	}
}

// 配置写入也要留痕，而且密钥**绝不能**出现在审计里。
func TestAuditCoversConfigWritesAndNeverLeaksSecrets(t *testing.T) {
	e := newTestEnv(t)

	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "free_units", "value": float64(7)}, e.admin()); r.Code != 200 {
		t.Fatalf("保存应 200，实际 %d %s", r.Code, r.Body)
	}
	// 密钥写入仍然 422（不是本轮放开的东西），且不该产生审计。
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "image_provider_api_key", "value": "sk-leak-me"}, e.admin()); r.Code != 422 {
		t.Fatalf("密钥写入必须 422，实际 %d %s", r.Code, r.Body)
	}

	out := e.do("GET", "/v1/admin/audit", nil, e.admin())
	if strings.Contains(string(out.Body), "sk-leak-me") {
		t.Fatal("🔴 审计出参里出现了密钥明文 —— 这就把 events 表变成了一个密钥落盘点")
	}
	m := out.Map(t)
	if m["retentionDays"] == nil {
		t.Error("审计出参必须带 retentionDays：events 受保留期清理管，面板上那句「N 天后删除」要说真话")
	}
	rows, _ := m["audit"].([]any)
	var sawConfig bool
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["action"] != "config.set" {
			continue
		}
		props, _ := row["props"].(map[string]any)
		if props["key"] == "free_units" {
			sawConfig = true
			if props["from"] != float64(3) || props["to"] != float64(7) {
				t.Errorf("审计必须记下 from/to，实际 %v → %v", props["from"], props["to"])
			}
		}
	}
	if !sawConfig {
		t.Fatalf("🔴 审计里找不到那次 free_units 改动。audit=%v", m["audit"])
	}
	_ = e.rt.Set(nil2ctx(), "free_units", 3)
}

// 反馈原因码是**观测**而不是字典配置：后台只回答「用户实际点了哪些码、各多少次」。
func TestAdminFeedbackReasonsAreObserved(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, tok, pid, aid := e.prepareJobInputs("reasons@example.com", 5)

	candAsset := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), candAsset, uid, pid, candAsset+".jpg",
		1000, 800, 1000, aigc.MarkVisibleMeta, e.now); err != nil {
		t.Fatal(err)
	}
	jobID := "job-reasons"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid, StyleVersionID: "ver-style-free",
		Status: "succeeded", Stage: "complete", Controls: []byte(`{"strength":"balanced"}`),
		Output: []byte(`{"aspectRatio":"4:5","qualityTier":"standard"}`), ReservedUnits: 1,
		CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	candID := "cand-reasons"
	if err := store.InsertCandidate(ctx, e.st.Q(), candID, jobID, 0, candAsset, e.now); err != nil {
		t.Fatal(err)
	}
	// App 真实发的形状：一个差评带**恰好一个**原因码。
	if r := e.do("POST", "/v1/candidates/"+candID+"/feedback",
		map[string]any{"rating": "negative", "reasonCodes": []any{"FACE_CHANGED"}},
		bearer(tok)); r.Code != 200 {
		t.Fatalf("反馈应 200，实际 %d %s", r.Code, r.Body)
	}

	m := e.do("GET", "/v1/admin/feedback-reasons", nil, e.admin()).Map(t)
	known, _ := m["knownClientCodes"].([]any)
	if len(known) != 4 {
		t.Fatalf("已知客户端原因码应为 4 个（web/app.js 里那四个芯片），实际 %v", known)
	}
	rows, _ := m["reasonCodes"].([]any)
	var hit bool
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["code"] == "FACE_CHANGED" {
			hit = true
			if row["negative"] != float64(1) {
				t.Errorf("FACE_CHANGED 的差评计数应为 1，实际 %v", row["negative"])
			}
		}
	}
	if !hit {
		t.Fatalf("🔴 聚合里找不到刚提交的原因码。rows=%v", rows)
	}
}

// 用户页的事实面板：status 分布 + 会话有效期。
// 🔴 status 分布是上线前那道闸：把 active 之外的一律当封禁之前，
// 必须先能证明生产库里没有第三种值在正常使用中。
func TestAdminUserFacts(t *testing.T) {
	e := newTestEnv(t)
	e.signUp("facts@example.com")
	var out AdminUserFacts
	e.do("GET", "/v1/admin/user-facts", nil, e.admin()).JSON(t, &out)
	if out.StatusCounts[UserStatusActive] < 1 {
		t.Fatalf("至少应有 1 个 active 用户，实际 %v", out.StatusCounts)
	}
	if out.SessionTTLDays != 90 {
		t.Errorf("默认会话有效期应为 90 天，实际 %d", out.SessionTTLDays)
	}
	if !strings.Contains(out.SuspendedBlocks, "401") {
		t.Error("说明里必须写清禁用到底拦住了什么")
	}
}
