package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/store"
)

// 安全问题 2 的端到端断言：/v1/admin/db/table/{name} 逐列不得泄漏。
func TestAdminDBTableRedactionEndToEnd(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()

	// 造出全部敏感行。
	uid, tok := e.signUp("leak@example.com")
	_ = tok
	secretVal := "SUPER-SECRET-SALT-VALUE-DO-NOT-LEAK"
	email := "leak@example.com"
	orderID := "GPA.3312-1234-5678-90123"
	exec := func(sql string, args ...any) {
		if _, err := e.st.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO server_secrets (key, value, created_at) VALUES ('ip_hash_salt',$1,$2)`, secretVal, e.now)
	exec(`INSERT INTO purchases (id,user_id,product_id,platform,external_transaction_id,status,amount_minor,currency,purchased_at,created_at)
	      VALUES ('p1',$1,'prod-pack_10','google',$2,'verified',499,'USD',$3,$3)`, uid, orderID, e.now)
	exec(`INSERT INTO email_codes (email, code_hash, expires_at, attempts, created_at, issue_count)
	      VALUES ('c@example.com','8f14e45fceea167a5a36dedd4bea2543',$1,0,$1,1)`, e.now)
	exec(`INSERT INTO app_config (key, value, updated_at) VALUES ('image_provider_api_key','sk-LEAKED-KEY-VALUE',$1)`, e.now)

	// server_secrets 整表不可浏览。
	if r := e.do("GET", "/v1/admin/db/table/server_secrets", nil, e.admin()); r.Code != 404 {
		t.Fatalf("server_secrets 必须整表拒绝，实际 %d %s", r.Code, r.Body)
	}

	// 逐张表拉出来，断言敏感明文一个都不出现。
	forbidden := []string{secretVal, email, "email:" + email, orderID,
		"8f14e45fceea167a5a36dedd4bea2543", "sk-LEAKED-KEY-VALUE", "guesttoken"}
	for _, tbl := range store.BrowsableTableNames() {
		r := e.do("GET", "/v1/admin/db/table/"+tbl+"?limit=200", nil, e.admin())
		if r.Code != 200 {
			t.Fatalf("浏览 %s 失败: %d %s", tbl, r.Code, r.Body)
		}
		body := string(r.Body)
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Errorf("表 %s 的响应里出现了明文敏感值 %q", tbl, f)
			}
		}
	}

	// sessions.token 只留前 6 位。
	r := e.do("GET", "/v1/admin/db/table/sessions", nil, e.admin())
	var page store.TablePage
	r.JSON(t, &page)
	tokIdx := -1
	for i, c := range page.Columns {
		if c == "token" {
			tokIdx = i
		}
	}
	if tokIdx < 0 || len(page.Rows) == 0 {
		t.Fatal("sessions 表应有 token 列与至少一行")
	}
	got, _ := page.Rows[0][tokIdx].(string)
	if !strings.HasSuffix(got, "\u2026") || len([]rune(got)) != 7 {
		t.Fatalf("sessions.token 应只留前 6 位 + 省略号，实际 %q", got)
	}

	// SQL 控制台必须拒绝触达凭据表。
	for _, sql := range []string{
		"select * from server_secrets",
		"select value from app_config",
		"select email_normalized from auth_identities",
		"select token from sessions",
		"select response_body from idempotency_records",
		"select code_hash from email_codes",
	} {
		rr := e.do("POST", "/v1/admin/db/query", map[string]any{"sql": sql}, e.admin())
		if rr.Code != 422 {
			t.Errorf("SQL 控制台必须拒绝 %q，实际 %d %s", sql, rr.Code, rr.Body)
		}
	}
	// 写操作一律拒绝。
	for _, sql := range []string{
		"delete from users",
		"update products set price_minor = 1",
		"select 1; drop table users",
		"with recursive t as (select 1) select * from t",
	} {
		rr := e.do("POST", "/v1/admin/db/query", map[string]any{"sql": sql}, e.admin())
		if rr.Code != 422 {
			t.Errorf("SQL 控制台必须拒绝 %q，实际 %d %s", sql, rr.Code, rr.Body)
		}
	}
	// 负向：一条正常只读查询必须成功 —— 否则上面全是假绿。
	ok := e.do("POST", "/v1/admin/db/query", map[string]any{"sql": "select internal_key from products order by internal_key"}, e.admin())
	if ok.Code != 200 {
		t.Fatalf("普通只读查询必须可用，实际 %d %s", ok.Code, ok.Body)
	}
	var qr store.QueryResult
	ok.JSON(t, &qr)
	if qr.RowCount != 6 {
		t.Fatalf("应查到 6 个商品，实际 %d", qr.RowCount)
	}
}

// 安全问题 1 的端到端断言：后台改密钥必须被拒，且 /admin/config 不泄漏值。
func TestAdminConfigSecretWriteRejected(t *testing.T) {
	e := newTestEnv(t)
	r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "image_provider_api_key", "value": "sk-hot-swapped-key"}, e.admin())
	if r.Code != 422 {
		t.Fatalf("后台热改密钥必须 422（这是 Node 版把密钥写进 app_config 的入口），实际 %d %s", r.Code, r.Body)
	}
	var n int
	if err := e.st.Pool().QueryRow(nil2ctx(),
		`SELECT count(*) FROM app_config WHERE key='image_provider_api_key'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("密钥不得落进 app_config 表")
	}
	// 非密钥项照常可改 —— 否则上面是假绿。
	if r := e.do("PUT", "/v1/admin/config", map[string]any{"key": "free_units", "value": 5}, e.admin()); r.Code != 200 {
		t.Fatalf("非密钥项应可热改，实际 %d %s", r.Code, r.Body)
	}
	// 配置清单里密钥只出现掩码，且 source 不是 db。
	cfgResp := e.do("GET", "/v1/admin/config", nil, e.admin())
	if strings.Contains(string(cfgResp.Body), "sk-test-key-not-in-db") {
		t.Fatalf("配置清单泄漏了密钥值：%s", cfgResp.Body)
	}
	var out AdminConfigResult
	cfgResp.JSON(t, &out)
	for _, s := range out.Settings {
		if s.Key == "image_provider_api_key" {
			if s.Source == "db" {
				t.Fatal("密钥项的 source 不得为 db")
			}
			v, _ := s.Value.(string)
			if !strings.HasPrefix(v, "\u2022\u2022\u2022\u2022") {
				t.Fatalf("密钥项必须掩码为 4 个圆点 + 末 4 位，实际 %q", v)
			}
		}
	}
}

// 路由总数：公开 30 + 管理 25 = 55（另加一条不在公开契约里的 /v1/ready）。
// 管理路由 2026-09-12 从 20 加到 25：PATCH styles-admin/{id}、
// POST users/{id}/status、GET user-facts、GET audit、GET feedback-reasons。
func TestRouteCount(t *testing.T) {
	e := newTestEnv(t)
	pub, adm := e.app.RouteCount()
	if adm != 25 {
		t.Fatalf("管理路由应为 25 条，实际 %d", adm)
	}
	if pub != 31 {
		t.Fatalf("公开路由应为 30 条契约路由 + 1 条内部 /v1/ready = 31，实际 %d", pub)
	}
}

var _ = json.Marshal

// 🔴 /db/table 与 /db/query 的时间列必须是 UTC ISO-8601 带 Z，
// 绝不能是进程本地时区的 +08:00（pgx 扫出来的 time.Time 默认就是本地时区）。
func TestAdminDBTimestampsAreUTC(t *testing.T) {
	e := newTestEnv(t)
	e.signUp("tz@example.com")

	r := e.do("GET", "/v1/admin/db/table/users", nil, e.admin())
	var page store.TablePage
	r.JSON(t, &page)
	idx := -1
	for i, c := range page.Columns {
		if c == "created_at" {
			idx = i
		}
	}
	if idx < 0 || len(page.Rows) == 0 {
		t.Fatal("users 表应有 created_at 列与至少一行")
	}
	got, _ := page.Rows[0][idx].(string)
	if got != "2026-09-11T04:26:12.396Z" {
		t.Fatalf("created_at 必须是 UTC ISO-8601 带毫秒与 Z，实际 %q", got)
	}
	if strings.Contains(string(r.Body), "+08:00") || strings.Contains(string(r.Body), "+00:00") {
		t.Fatalf("响应里出现了时区偏移写法：%s", r.Body)
	}

	q := e.do("POST", "/v1/admin/db/query", map[string]any{"sql": "select created_at from users"}, e.admin())
	if strings.Contains(string(q.Body), "+08:00") || strings.Contains(string(q.Body), "+00:00") {
		t.Fatalf("SQL 控制台的时间列也必须是 Z：%s", q.Body)
	}
}

// TestAdminConfigExposesDeployLevelReadOnly 2026-09-12 新增：后台要能看见
// **部署级**配置，但一行都不许改、一个密钥值都不许露。
//
// 🔴 为什么这条测试值得写：部署级那批值（会话有效期、保留期、存储上限、
// 测试登录逃生口……）此前在后台完全不可见，运营查「为什么用户被登出了」
// 只能去 SSH 读 project.env。把它们搬进 /v1/admin/config 的同时，必须
// 同时钉死两件事：① 全部 readOnly（前端据此不渲染保存按钮，且 rt.Set
// 对未知键返回 422，所以没有新写入口）；② 密钥只给布尔，不给值。
func TestAdminConfigExposesDeployLevelReadOnly(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do("GET", "/v1/admin/config", nil, e.admin())
	if resp.Code != 200 {
		t.Fatalf("应 200，实际 %d %s", resp.Code, resp.Body)
	}
	var out AdminConfigResult
	resp.JSON(t, &out)

	byKey := map[string]cfgstore.Setting{}
	for _, s := range out.Settings {
		byKey[s.Key] = s
	}
	// 🔴 session_ttl / event_retention / idempotency_retention / max_job_attempts
	//    这四行 2026-09-12 已从部署级只读**升格成注册表热键**，所以它们不再出现在
	//    这份清单里 —— 同一个键同时有一个可改行和一个只读行，是最容易让运营
	//    改错地方的布局（见 deployOnlySettings 的注释）。
	want := []string{
		"deploy_image_provider",
		"deploy_shutdown_grace_seconds",
		"deploy_db_pool_max_conns", "deploy_trusted_proxy", "deploy_trust_cf_connecting_ip",
		"deploy_play_acknowledge", "deploy_allow_test_login",
		"deploy_admin_token_configured", "deploy_ip_hash_salt_configured",
		"deploy_google_web_client_id_configured",
	}
	for _, k := range want {
		s, ok := byKey[k]
		if !ok {
			t.Errorf("配置清单缺部署级项 %s", k)
			continue
		}
		// ① 一律只读。
		if !s.ReadOnly {
			t.Errorf("🔴 %s 必须是 readOnly —— 它在进程启动时就固化了，"+
				"后台能改的假象比看不见更糟（改完没生效，页面显示已保存）", k)
		}
	}
	// ② 管理令牌 / IP 盐只给布尔，**值一个字节都不许出现在响应里**。
	//    testenv 里这两个值是已知的，所以可以直接 grep。
	body := string(resp.Body)
	for _, secret := range []string{adminToken, "test-ip-salt-not-the-admin-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("🔴 /v1/admin/config 响应里出现了密钥值（%d 字节的那个）", len(secret))
		}
	}
	if v, _ := byKey["deploy_admin_token_configured"].Value.(bool); !v {
		t.Error("testenv 配了 ADMIN_TOKEN，该项应为 true")
	}
	if v, _ := byKey["deploy_ip_hash_salt_configured"].Value.(bool); !v {
		t.Error("testenv 配了 IP_HASH_SALT，该项应为 true")
	}
	// ③ 部署级键不在注册表里，所以 PUT 必须被拒 —— 这是「没有新写入口」的硬证据。
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "deploy_session_ttl_days", "value": 1}, e.admin()); r.Code != 422 {
		t.Fatalf("🔴 部署级项可写了（%d %s）—— 写了也不会生效，必须拒绝", r.Code, r.Body)
	}
	// ④ 正常情况下不该有遗留明文密钥告警项。
	if _, ok := byKey["leftover_secret_rows_in_db"]; ok {
		t.Error("干净库里不该出现 leftover_secret_rows_in_db 告警项")
	}
	// ⑤ 验证码有效期作为**热键**出现，且可改（本次把它从四处字面量收敛进注册表）。
	ttl, ok := byKey["email_code_ttl_seconds"]
	if !ok {
		t.Fatal("配置清单里应有 email_code_ttl_seconds")
	}
	if ttl.ReadOnly || ttl.Secret {
		t.Error("验证码有效期应当是可热改的非密钥项")
	}
	if r := e.do("PUT", "/v1/admin/config",
		map[string]any{"key": "email_code_ttl_seconds", "value": 300}, e.admin()); r.Code != 200 {
		t.Fatalf("验证码有效期应可热改，实际 %d %s", r.Code, r.Body)
	}
	// ⑥ 2026-09-12 第二批收进注册表的运营旋钮：必须是**可热改的非密钥热键**，
	//    必须带区间供前端展示，且越界的 PUT 必须 422（而不是静默夹）。
	//    max_user_storage_bytes 此前是部署级只读行，现在升级成热键 ——
	//    所以上面那份 want 里刻意不再有 deploy_max_user_storage_bytes：
	//    同一个键既有可改行又有只读行，是最容易让运营改错地方的布局。
	for _, tc := range []struct {
		key      string
		good     any
		tooSmall any
		tooBig   any
	}{
		{"email_code_max_attempts", 3, 0, 999},
		{"email_code_max_issues_per_window", 2, 0, 999},
		{"max_user_storage_bytes", 64 * 1024 * 1024, 1, 1 << 40},
	} {
		s, ok := byKey[tc.key]
		if !ok {
			t.Errorf("配置清单缺热键 %s", tc.key)
			continue
		}
		if s.ReadOnly || s.Secret {
			t.Errorf("%s 应当是可热改的非密钥项", tc.key)
		}
		if s.Min == nil || s.Max == nil {
			t.Errorf("%s 应带 min/max 供前端展示（省掉「保存→422→猜区间」这一轮）", tc.key)
		}
		if r := e.do("PUT", "/v1/admin/config",
			map[string]any{"key": tc.key, "value": tc.good}, e.admin()); r.Code != 200 {
			t.Errorf("%s 区间内的值应可热改，实际 %d %s", tc.key, r.Code, r.Body)
		}
		for _, bad := range []any{tc.tooSmall, tc.tooBig} {
			if r := e.do("PUT", "/v1/admin/config",
				map[string]any{"key": tc.key, "value": bad}, e.admin()); r.Code != 422 {
				t.Errorf("🔴 %s=%v 越界应 422（静默夹会让页面显示的和生效的不是一回事），实际 %d %s",
					tc.key, bad, r.Code, r.Body)
			}
		}
	}
	// ⑦ 运行状态块必须有值：它是「改完配置到底生效没 / SMTP 通不通 / 队列堵没堵」
	//    唯一不用 SSH 的查法。全 0 / 空串等于这一块白给。
	rt := out.Runtime
	if rt.Version == "" || rt.StartedAt == "" || rt.ServerTime == "" {
		t.Errorf("🔴 runtime 必须给出版本与启动时间，实际 %+v", rt)
	}
	if !rt.DB.OK || rt.DB.MaxConns <= 0 {
		t.Errorf("🔴 runtime.db 必须反映真实连接池，实际 %+v", rt.DB)
	}
	if rt.SMTP.CodeTTLSeconds <= 0 {
		t.Errorf("runtime.smtp 应带上当前生效的验证码有效期，实际 %+v", rt.SMTP)
	}
	if rt.Support.Email == "" && rt.Support.QQGroup == "" {
		t.Error("runtime.support 应给出客服入口当前值（配错=付费转化直接断掉）")
	}
	// 响应体里不得出现 SMTP 口令相关的任何值 —— 只给布尔。
	if strings.Contains(body, "\"passConfigured\":") == false {
		t.Error("runtime.smtp 应只给 passConfigured 布尔，不给口令")
	}
}
