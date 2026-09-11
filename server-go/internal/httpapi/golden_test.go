package httpapi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"museframe-api/internal/store"
)

// 🔴 价格黄金测试（D-14）。
// 单位必须是 minor（美分 / 人民币分），类型必须是 JSON 整数，
// priceCnyMinor 可以是 null，priceMinor 永不为 null，
// currency 恒为 USD 且只描述 priceMinor。
func TestGoldenProductPricing(t *testing.T) {
	e := newTestEnv(t)
	r := e.do("GET", "/v1/products", nil, nil)
	if r.Code != 200 {
		t.Fatalf("状态码 %d: %s", r.Code, r.Body)
	}
	// 用 json.RawMessage 逐字节看类型：转成字符串再比会掩盖「整数变字符串/浮点」。
	var raw struct {
		Products []map[string]json.RawMessage `json:"products"`
	}
	r.JSON(t, &raw)
	if len(raw.Products) != 4 {
		t.Fatalf("上架商品应为 4 个（下架的 mini_pack / creator_annual 不出现），实际 %d", len(raw.Products))
	}
	want := []struct {
		key   string
		minor string
		cny   string
	}{
		{"creator_monthly", "799", "4900"},
		{"pack_10", "499", "2900"},
		{"pack_30", "999", "6900"},
		{"pack_100", "2999", "19900"},
	}
	for i, w := range want {
		p := raw.Products[i]
		if string(p["internalKey"]) != `"`+w.key+`"` {
			t.Fatalf("第 %d 个商品应是 %s，实际 %s", i, w.key, p["internalKey"])
		}
		if string(p["priceMinor"]) != w.minor {
			t.Errorf("%s.priceMinor 应是裸整数 %s（美分），实际 %s", w.key, w.minor, p["priceMinor"])
		}
		if string(p["priceCnyMinor"]) != w.cny {
			t.Errorf("%s.priceCnyMinor 应是裸整数 %s（人民币分），实际 %s", w.key, w.cny, p["priceCnyMinor"])
		}
		if string(p["currency"]) != `"USD"` {
			t.Errorf("%s.currency 应恒为 USD（它只描述 priceMinor），实际 %s", w.key, p["currency"])
		}
	}
}

// 🔴 排序黄金测试。
// Node 版 SQL 没有 ORDER BY，行序靠 SQLite rowid 巧合；PG 的堆表顺序会随 UPDATE 漂。
// 这里先改一次价（在 PG 里通常会把行挪到堆尾），再断言行序不变。
func TestGoldenProductOrderStableAfterUpdate(t *testing.T) {
	e := newTestEnv(t)
	order := func() []string {
		r := e.do("GET", "/v1/products", nil, nil)
		var out struct {
			Products []struct {
				InternalKey string `json:"internalKey"`
			} `json:"products"`
		}
		r.JSON(t, &out)
		keys := make([]string, 0, len(out.Products))
		for _, p := range out.Products {
			keys = append(keys, p.InternalKey)
		}
		return keys
	}
	want := []string{"creator_monthly", "pack_10", "pack_30", "pack_100"}
	before := order()
	for i := range want {
		if before[i] != want[i] {
			t.Fatalf("初始行序应为 %v，实际 %v", want, before)
		}
	}
	// 改 pack_10 的人民币价：PG 的 UPDATE 是「删旧行 + 追加新行」，堆序必然变。
	r := e.do("PATCH", "/v1/admin/products-admin/pack_10", map[string]any{"priceCnyMinor": 3100}, e.admin())
	if r.Code != 200 {
		t.Fatalf("改价失败: %d %s", r.Code, r.Body)
	}
	after := order()
	for i := range want {
		if after[i] != want[i] {
			t.Fatalf("改价后行序漂了：期望 %v，实际 %v（说明没有显式 ORDER BY）", want, after)
		}
	}
}

// 价格写入校验：非整数 / 负数一律 422。
func TestProductPriceWriteValidation(t *testing.T) {
	e := newTestEnv(t)
	for _, body := range []map[string]any{
		{"priceMinor": 4.99},
		{"priceMinor": -1},
		{"priceCnyMinor": 29.0000001},
		{"grantedUnits": -3},
		{"active": "yes"},
	} {
		r := e.do("PATCH", "/v1/admin/products-admin/pack_10", body, e.admin())
		if r.Code != 422 {
			t.Errorf("入参 %v 应被拒为 422，实际 %d %s", body, r.Code, r.Body)
		}
	}
	// null 是合法的 priceCnyMinor（该商品不卖人民币）。
	r := e.do("PATCH", "/v1/admin/products-admin/pack_10", map[string]any{"priceCnyMinor": nil}, e.admin())
	if r.Code != 200 {
		t.Fatalf("priceCnyMinor=null 应被接受，实际 %d %s", r.Code, r.Body)
	}
	var out struct {
		Products []map[string]json.RawMessage `json:"products"`
	}
	e.do("GET", "/v1/products", nil, nil).JSON(t, &out)
	for _, p := range out.Products {
		if string(p["internalKey"]) == `"pack_10"` && string(p["priceCnyMinor"]) != "null" {
			t.Fatalf("priceCnyMinor 应为 null，实际 %s", p["priceCnyMinor"])
		}
	}
}

// 🔴 时区黄金测试：出参时间一律 UTC ISO-8601 带毫秒与 Z（25 字符）。
func TestGoldenTimeFormatIsUTCWithMillis(t *testing.T) {
	e := newTestEnv(t)
	r := e.do("GET", "/v1/health", nil, nil)
	m := r.Map(t)
	ts, _ := m["time"].(string)
	if ts != "2026-09-11T04:26:12.396Z" {
		t.Fatalf("time 必须逐字节等于 2026-09-11T04:26:12.396Z，实际 %q", ts)
	}
	// reason 必须是 null 不是空串。
	gen, _ := m["generation"].(map[string]any)
	if v, ok := gen["reason"]; !ok || v != nil {
		t.Fatalf("generation.reason 必须是 null，实际 %#v", gen["reason"])
	}
	if gen["available"] != true || gen["mode"] != "remote" {
		t.Fatalf("generation 应为 available=true mode=remote，实际 %#v", gen)
	}
	q, _ := m["queue"].(map[string]any)
	for _, k := range []string{"queued", "active", "oldestQueuedAgeSec"} {
		if _, ok := q[k].(float64); !ok {
			t.Fatalf("queue.%s 必须是数字，实际 %#v", k, q[k])
		}
	}
	if _, ok := q["draining"].(bool); !ok {
		t.Fatalf("queue.draining 必须是布尔")
	}
}

// 🔴 统计的日期分桶必须按 UTC 日，且**在非 UTC 会话下也成立**。
// 这一条刻意用 Asia/Shanghai 会话跑：写成 created_at::date 的实现会在这里当场翻车
// （UTC 20:00 会被算进第二天）。
func TestGoldenDailyStatsUTCBucketUnderShanghaiSession(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	// 两个边界用户：UTC 前一天 20:00（+08 会跳到第二天）、UTC 当天 00:30。
	if _, err := e.st.Pool().Exec(ctx, `
		INSERT INTO users (id, is_guest, locale, created_at, updated_at) VALUES
		  ('u-late', false, 'en', TIMESTAMPTZ '2026-09-10 20:00:00+00', TIMESTAMPTZ '2026-09-10 20:00:00+00'),
		  ('u-early', false, 'en', TIMESTAMPTZ '2026-09-11 00:30:00+00', TIMESTAMPTZ '2026-09-11 00:30:00+00')`); err != nil {
		t.Fatal(err)
	}

	dsn := os.Getenv("MUSEFRAME_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	shSt, err := store.Open(ctx, dsn+sep+"timezone=Asia/Shanghai", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer shSt.Close()
	var tz string
	if err := shSt.Pool().QueryRow(ctx, "SHOW TimeZone").Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if tz != "Asia/Shanghai" {
		t.Fatalf("这条测试必须跑在非 UTC 会话上才有意义，实际 TimeZone=%q", tz)
	}

	days, err := store.GetDailyStats(ctx, shSt.Q(), e.now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("应补齐 2 天（缺数据补 0 而不是跳过），实际 %d", len(days))
	}
	if days[0].Date != "2026-09-10" || days[1].Date != "2026-09-11" {
		t.Fatalf("日期轴必须是 UTC 日，实际 %s / %s", days[0].Date, days[1].Date)
	}
	if days[0].NewUsers != 1 {
		t.Fatalf("2026-09-10T20:00Z 的用户必须落在 09-10（按 +08 会错到 09-11），实际 09-10=%d 09-11=%d",
			days[0].NewUsers, days[1].NewUsers)
	}
	if days[1].NewUsers != 1 {
		t.Fatalf("2026-09-11T00:30Z 的用户必须落在 09-11，实际 %d", days[1].NewUsers)
	}
}
