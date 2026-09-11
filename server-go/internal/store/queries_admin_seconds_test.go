package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

// 回归锁：/v1/admin/jobs 的 seconds 列必须是 floor，不能退回 CAST(... AS integer)。
//
// 背景（agent #32 的 Node/Go 双跑比对，唯一一处真差异）：
//   - Node 版跑 SQLite：CAST((julianday(a)-julianday(b))*86400 AS INTEGER) —— 截断
//   - Go  版跑 PG    ：CAST(EXTRACT(EPOCH FROM (a-b))    AS integer)      —— 四舍五入
//
// 小数部分 >= 0.5 的任务两端差 1 秒，Go 侧恒大 1。
// 这条测试不连库也能跑，保证「有人把 floor 改回 CAST」当场红。
func TestAdminJobSecondsExprIsFloorNotRound(t *testing.T) {
	e := strings.ToLower(adminJobSecondsExpr)
	if !strings.Contains(e, "floor(") {
		t.Fatalf("seconds 表达式必须用 floor() 截断，实际: %s", adminJobSecondsExpr)
	}
	// PG 的 CAST(numeric AS integer) / numeric::integer 会四舍五入。
	// floor() 的结果再 ::integer 是安全的（已经是整数），所以这里只拦
	// 「直接把 EXTRACT 结果转整数」这一种写法。
	if strings.Contains(e, "cast(extract(") {
		t.Fatalf("不能用 CAST(EXTRACT(...) AS integer)：PG 会四舍五入，与 Node/SQLite 的截断不一致。实际: %s", adminJobSecondsExpr)
	}
	if !strings.Contains(e, "coalesce(j.finished_at, j.updated_at)") || !strings.Contains(e, "j.created_at") {
		t.Fatalf("seconds 表达式的取数口径被改动了，与 Node 版不再逐字对应: %s", adminJobSecondsExpr)
	}
}

// 引擎级验证：把真实的 seconds 表达式拿到真 PG 上跑，断言它走的是截断语义。
// 纯 SELECT 字面量，不碰任何表。没有 MUSEFRAME_TEST_DATABASE_URL 时 skip（不是静默通过）。
//
// 口径说明（2026-09-11 在 PostgreSQL 18.4 与 sqlite3 上逐条实测）：
// 契约是 Node 那句 SQL 的**意图** —— 截断到整秒。Node 实际跑出来的值还额外带一点
// julianday 的浮点噪声：julianday 返回的是 ~2.46e6 量级的天数 double，1 秒只有
// 1.16e-5 天，做差再乘 86400 之后误差量级 ~1e-5 秒，于是**整秒**耗时经常被截低 1
// （实测 1s->0、2s->1、3s->2、10s->9，4s->4 纯属运气）。这是 SQLite 侧的浮点瑕疵，
// 不是契约，Go 侧**刻意不复刻**。因此整秒用例的期望值按截断语义写 3，而不是 Node
// 当场吐出来的 2。小数用例两端无歧义，才是这条测试真正盯的东西。
func TestAdminJobSecondsMatchesNodeTruncation(t *testing.T) {
	dsn := os.Getenv("MUSEFRAME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 MUSEFRAME_TEST_DATABASE_URL，跳过引擎级比对")
	}
	ctx := context.Background()
	st, err := Open(ctx, dsn, 2)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	defer st.Close()

	// 把被测表达式里的两个列名换成占位符，表达式本体逐字不动。
	expr := strings.ReplaceAll(adminJobSecondsExpr,
		"COALESCE(j.finished_at, j.updated_at)", "$2::timestamptz")
	expr = strings.ReplaceAll(expr, "j.created_at", "$1::timestamptz")
	// 修复前的写法，用来证明这条测试确实抓得住回归。
	old := "CAST(EXTRACT(EPOCH FROM ($2::timestamptz - $1::timestamptz)) AS integer)"

	cases := []struct {
		name       string
		start, end string
		want       int  // 截断语义的期望值
		oldDiffers bool // 修复前的 PG 写法（四舍五入）是否会给出不同的值
	}{
		{"小数 .6 会被 PG 四舍五入上去", "2026-09-11T00:00:00Z", "2026-09-11T00:00:01.6Z", 1, true},
		{"小数 .5 边界", "2026-09-11T00:00:00Z", "2026-09-11T00:00:02.5Z", 2, true},
		{"小数 .999", "2026-09-11T00:00:00Z", "2026-09-11T00:00:00.999Z", 0, true},
		{"小数 .4 两端本来就一致", "2026-09-11T00:00:00Z", "2026-09-11T00:00:01.4Z", 1, false},
		{"整秒", "2026-09-11T00:00:00Z", "2026-09-11T00:00:03Z", 3, false},
		{"零耗时", "2026-09-11T00:00:00Z", "2026-09-11T00:00:00Z", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got, before int
			err := st.Pool().QueryRow(ctx,
				"SELECT "+expr+", "+old, c.start, c.end).Scan(&got, &before)
			if err != nil {
				t.Fatalf("查询失败: %v", err)
			}
			if got != c.want {
				t.Fatalf("seconds = %d，截断口径应为 %d（%s -> %s）", got, c.want, c.start, c.end)
			}
			if c.oldDiffers && before == got {
				t.Fatalf("这条用例本该能区分新旧写法，但旧写法也给出 %d —— 用例失去意义", before)
			}
			if !c.oldDiffers && before != got {
				t.Fatalf("这条用例新旧写法本应一致，实际旧=%d 新=%d", before, got)
			}
		})
	}
}
