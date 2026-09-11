package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// 防重复发放的地基是 purchases 上的 UNIQUE (platform, external_transaction_id)。
// Node 版靠的是：pending 那条用 INSERT OR IGNORE，已核验那条用**裸 INSERT** ——
// 冲突会把整个 tx() 炸掉，于是重复的那次发放被回滚。
//
// Go 第一版两处共用了带 ON CONFLICT DO NOTHING 的同一个函数，把这块地基拆了：
// 冲突被静默吞掉，调用方以为插入成功，继续用**自己那个新 uuid** 去
// grantPurchaseUnits，算出的 reference_key 与先到的那次不同，
// credit_ledger 的 UNIQUE (user_id, reference_key) 拦不住 —— 一笔支付发两份额度。
func openPurchaseTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MUSEFRAME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 MUSEFRAME_TEST_DATABASE_URL，跳过需要 PostgreSQL 的测试")
	}
	st, err := Open(context.Background(), dsn, 2)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func seedPurchaseFixture(t *testing.T, st *Store) (userID, productID string) {
	t.Helper()
	ctx := context.Background()
	userID, productID = "zzpu-user-1", "zzpu-prod-1"
	now := time.Now().UTC()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("执行 %.50s 失败: %v", sql, err)
		}
	}
	mustExec(`DELETE FROM purchases WHERE user_id = $1`, userID)
	mustExec(`DELETE FROM users WHERE id = $1`, userID)
	mustExec(`DELETE FROM products WHERE id = $1`, productID)
	mustExec(`INSERT INTO users (id, is_guest, created_at, updated_at) VALUES ($1,false,$2,$2)`, userID, now)
	mustExec(`INSERT INTO products (id, internal_key, product_type, price_minor, currency,
	            display_name, granted_units, active)
	          VALUES ($1,$1,'pack',100,'USD','t',1,true)`, productID)
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM purchases WHERE user_id = $1`, userID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
	})
	return userID, productID
}

func newPurchase(id, userID, productID, extTxID, status string) *Purchase {
	now := time.Now().UTC()
	amount := int64(100)
	currency := "USD"
	return &Purchase{
		ID: id, UserID: userID, ProductID: productID, Platform: "google_play",
		ExternalTransactionID: extTxID, Status: status,
		AmountMinor: &amount, Currency: &currency, PurchasedAt: now, CreatedAt: now,
	}
}

// TestInsertPurchaseRejectsDuplicateExternalTxID 🔴 已核验路径上的重复订单必须报错。
// 报错才能让外层 InTx 回滚，重复的那份额度才不会发出去。
func TestInsertPurchaseRejectsDuplicateExternalTxID(t *testing.T) {
	st := openPurchaseTestStore(t)
	ctx := context.Background()
	userID, productID := seedPurchaseFixture(t, st)

	if err := InsertPurchase(ctx, st.Q(), newPurchase("zzpu-a", userID, productID, "GPA.dup-1", "verified")); err != nil {
		t.Fatalf("第一次插入应成功: %v", err)
	}
	// 并发的第二次：不同的 purchaseID（a.newID() 每次都新生成），同一个外部订单号。
	err := InsertPurchase(ctx, st.Q(), newPurchase("zzpu-b", userID, productID, "GPA.dup-1", "verified"))
	if err == nil {
		t.Fatal("🔴 重复的 (platform, external_transaction_id) 必须报错，" +
			"否则调用方会拿着一个新 purchaseID 继续发额度 —— 一笔支付发两份")
	}

	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM purchases WHERE platform='google_play' AND external_transaction_id='GPA.dup-1'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("库里应只有 1 条订单，实得 %d", n)
	}
}

// TestInsertPurchaseIfAbsentStaysSilent pending 路径保持 Node 的 INSERT OR IGNORE：
// 重复是正常的（客户端会重试），不能报错。
func TestInsertPurchaseIfAbsentStaysSilent(t *testing.T) {
	st := openPurchaseTestStore(t)
	ctx := context.Background()
	userID, productID := seedPurchaseFixture(t, st)

	for i, id := range []string{"zzpu-p1", "zzpu-p2"} {
		if err := InsertPurchaseIfAbsent(ctx, st.Q(),
			newPurchase(id, userID, productID, "GPA.pend-1", "pending")); err != nil {
			t.Fatalf("第 %d 次 IfAbsent 插入不该报错: %v", i+1, err)
		}
	}
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM purchases WHERE external_transaction_id='GPA.pend-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("IfAbsent 重复插入后应只有 1 条，实得 %d", n)
	}
}
