package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

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

// 路由总数：公开 30 + 管理 20 = 50（另加一条不在公开契约里的 /v1/ready）。
func TestRouteCount(t *testing.T) {
	e := newTestEnv(t)
	pub, adm := e.app.RouteCount()
	if adm != 20 {
		t.Fatalf("管理路由应为 20 条，实际 %d", adm)
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
