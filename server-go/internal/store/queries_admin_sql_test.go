package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// 🔴 这两条只做一件事：把后台每一个被改过的列表查询真的**打到 Postgres 上**。
// 这些 SQL 是字符串拼出来的（可选筛选条件 + 占位符序号），拼错不会被编译器发现，
// 也不会被任何 mock 发现 —— 只有生产上那一次 500 会发现。
// 没有 MUSEFRAME_TEST_DATABASE_URL 时整体 skip（不是静默通过）。
func TestAdminListQueriesRunOnPostgres(t *testing.T) {
	dsn := os.Getenv("MUSEFRAME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no dsn")
	}
	st, err := Open(context.Background(), dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pool := st.Q()
	if _, err := ListAdminFeedbackFull(context.Background(), pool, FeedbackFilter{Limit: 10}); err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if _, err := ListAdminAssets(context.Background(), pool, AssetFilter{Limit: 10}); err != nil {
		t.Fatalf("assets: %v", err)
	}
	if _, err := ListAdminUsers(context.Background(), pool, 10, "", TimeRange{}); err != nil {
		t.Fatalf("users: %v", err)
	}
	if _, err := ListAdminPurchasesFull(context.Background(), pool, PurchaseFilter{Limit: 10}); err != nil {
		t.Fatalf("purchases: %v", err)
	}
}

// 带 from/to 的那一路也要真的打到库，不然 SQL 拼错只有生产才发现。
func TestAdminListQueriesWithTimeRangeRunOnPostgres(t *testing.T) {
	dsn := os.Getenv("MUSEFRAME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no dsn")
	}
	st, err := Open(context.Background(), dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := st.Q()
	ctx := context.Background()
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	tr := TimeRange{From: &from, To: &to}
	if _, err := ListAdminFeedbackFull(ctx, q, FeedbackFilter{Rating: "positive", Handled: "no", Range: tr, Limit: 5}); err != nil {
		t.Fatalf("feedback+range: %v", err)
	}
	if _, err := ListAdminAssets(ctx, q, AssetFilter{Kind: "source", Status: "ready", UserID: "abc", Range: tr, Limit: 5}); err != nil {
		t.Fatalf("assets+range: %v", err)
	}
	if _, err := ListAdminUsers(ctx, q, 5, "someone", tr); err != nil {
		t.Fatalf("users+range: %v", err)
	}
	if _, err := ListAdminPurchasesFull(ctx, q, PurchaseFilter{Status: "verified", Platform: "web", Range: tr, Limit: 5}); err != nil {
		t.Fatalf("purchases+range: %v", err)
	}
	if _, err := ListAdminJobsFiltered(ctx, q, JobFilter{Status: "failed", SinceHours: 24, UserID: "abc", Range: tr, Limit: 5}, time.Now()); err != nil {
		t.Fatalf("jobs+range: %v", err)
	}
	if _, err := ListEventSamples(ctx, q, from, "generation_failed", tr, 5); err != nil {
		t.Fatalf("events+range: %v", err)
	}
	if _, err := ListUserLedger(ctx, q, "nobody", time.Now(), 5); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if _, err := ListUserSessions(ctx, q, "nobody", time.Now(), 5); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if _, err := ListAdminJobs(ctx, q, 5); err != nil {
		t.Fatalf("jobs: %v", err)
	}
}
