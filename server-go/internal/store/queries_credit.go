package store

import (
	"context"
	"time"
)

// ---- 免费额度去重台账 ------------------------------------------------------

// FreeGrantDedupeExists 查某个去重键是否已经领过。
func FreeGrantDedupeExists(ctx context.Context, q Queryer, key string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT 1 FROM free_grants WHERE dedupe_key = $1 LIMIT 1`, key).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// LedgerReferenceExists 查台账里是否已有该 reference_key。
// 老库里的历史发放先于 free_grants 表存在，这条保证它们的去重键继续有效。
func LedgerReferenceExists(ctx context.Context, q Queryer, referenceKey string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT 1 FROM credit_ledger WHERE reference_key = $1 LIMIT 1`, referenceKey).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// InsertFreeGrant 写一行去重台账。
// 一次发放对 dedupeId / deviceHash / userId 每个键各写一行，
// 只有主键行带 units，其余 units=0 —— 修的是「游客登录后去重键切换导致再领一张」的真 bug。
func InsertFreeGrant(ctx context.Context, q Queryer, id, userID, dedupeKey string, deviceHash, ipHash *string, units int, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO free_grants (id, user_id, dedupe_key, device_hash, ip_hash, units, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, userID, dedupeKey, deviceHash, ipHash, units, t)
	return err
}

// GetFreeGrantWindow 统计滚动 24 小时的发放次数。
// 三个计数一律带 units > 0：占位行不计入上限。
func GetFreeGrantWindow(ctx context.Context, q Queryer, ipHash *string, now time.Time) (FreeGrantWindow, error) {
	var w FreeGrantWindow
	since := now.Add(-24 * time.Hour)
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM free_grants WHERE units > 0 AND created_at >= $1`, since).Scan(&w.Today); err != nil {
		return w, err
	}
	if err := q.QueryRow(ctx,
		`SELECT count(DISTINCT ip_hash) FROM free_grants WHERE units > 0 AND created_at >= $1`, since).Scan(&w.IPs); err != nil {
		return w, err
	}
	if ipHash != nil && *ipHash != "" {
		if err := q.QueryRow(ctx,
			`SELECT count(*) FROM free_grants WHERE units > 0 AND ip_hash = $1 AND created_at >= $2`,
			*ipHash, since).Scan(&w.ForIP); err != nil {
			return w, err
		}
	}
	return w, nil
}

// ---- 额度桶与台账 ----------------------------------------------------------

// LockUserCredits 取一把**事务级**的 per-user 排他锁，用于串行化该用户的额度变更。
//
// 🔴 为什么必须有它：Node 版是靠「同步 handler + BEGIN IMMEDIATE 的 SQLite 句柄」
// 白拿的进程内互斥（server/db.js:372、server/api.js:797），
// 「读余额 → 判断够不够 → 写扣减」不可能被另一个请求插进来。
// Go 版是并发的，而 BucketBalances 只是一条普通聚合 SELECT，事务是 READ COMMITTED，
// 全仓没有任何行锁。于是两个并发的 POST /v1/generation-jobs（不同 Idempotency-Key，
// 双击或客户端超时重试都会产生）会双双读到 balance = 1，各写一条 units = -1：
// reference_key 分别是 job:<A>:reserve:0 和 job:<B>:reserve:0，
// credit_ledger 的 UNIQUE (user_id, reference_key) 拦不住，桶掉到 -1，
// 两个任务都跑、两张图都出，只收了一张的钱。
// 而且它**看不见**：BucketBalances 的 HAVING ... > 0 会把负数桶直接过滤掉，
// availableUnits 显示 0 而不是 -1。
//
// 用 advisory lock 而不是 SELECT ... FOR UPDATE 的原因有两个：
// 一是 PG 不允许 FOR UPDATE 和 GROUP BY 同用（余额是聚合出来的）；
// 二是新用户可能一个桶都还没有，没有行可锁，而发放路径要建桶 —— advisory lock
// 锁的是「这个用户的额度」这件事本身，空桶也照样串行。
// 事务级：提交或回滚时自动释放，不会泄漏。
//
// 🔴 而「事务级」也正是它唯一的危险点：如果 q 是连接池（Store.Q()）而不是事务，
// 这条 SELECT 走自动提交，语句一结束锁就没了 —— 返回 nil、没有告警、互斥为零。
// 所以先用 RequireTx 拦住；宁可 500，也不要无声地放回双扣那条路。
func LockUserCredits(ctx context.Context, q Queryer, userID string) error {
	if err := RequireTx(q, "store.LockUserCredits"); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, userID)
	return err
}

// BucketBalances 复刻 ledger.js:44 bucketBalances()：
// 余额 = 按桶聚合的台账 units 之和；过期桶不计；扣减时最早过期的桶先扣。
//
// 排序 `b.expires_at IS NULL, b.expires_at, b.created_at` 在 SQLite 里
// 「IS NULL」求值为 0/1，NULL 排在后面。PG 用 NULLS LAST 表达同一语义。
func BucketBalances(ctx context.Context, q Queryer, userID string, now time.Time) ([]Bucket, error) {
	rows, err := q.Query(ctx, `
		SELECT b.id, b.expires_at, b.created_at, COALESCE(SUM(l.units), 0) AS balance
		FROM credit_buckets b
		LEFT JOIN credit_ledger l ON l.balance_bucket_id = b.id
		WHERE b.user_id = $1 AND (b.expires_at IS NULL OR b.expires_at > $2)
		GROUP BY b.id, b.expires_at, b.created_at
		HAVING COALESCE(SUM(l.units), 0) > 0
		ORDER BY b.expires_at ASC NULLS LAST, b.created_at ASC`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		var bal int64
		if err := rows.Scan(&b.ID, &b.ExpiresAt, &b.CreatedAt, &bal); err != nil {
			return nil, err
		}
		b.Balance = int(bal)
		out = append(out, b)
	}
	return out, rows.Err()
}

// AvailableUnits 是可用余额。
func AvailableUnits(ctx context.Context, q Queryer, userID string, now time.Time) (int, error) {
	buckets, err := BucketBalances(ctx, q, userID, now)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, b := range buckets {
		total += b.Balance
	}
	return total, nil
}

// InsertBucket 建一个额度桶。granted_units > 0 是 DB 级 CHECK。
func InsertBucket(ctx context.Context, q Queryer, id, userID, sourceType string, sourceID *string, units int, expiresAt *time.Time, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO credit_buckets (id, user_id, source_type, source_id, granted_units, expires_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, userID, sourceType, sourceID, units, expiresAt, t)
	return err
}

// InsertLedger 写一条台账分录。
// UNIQUE (user_id, reference_key) 会在重复时报错 —— 这是幂等的地基，不要吞掉它。
func InsertLedger(ctx context.Context, q Queryer, id, userID, entryType string, units int, bucketID string, jobID, purchaseID *string, referenceKey string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO credit_ledger (id, user_id, entry_type, units, balance_bucket_id, job_id, purchase_id, reference_key, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, userID, entryType, units, bucketID, jobID, purchaseID, referenceKey, t)
	return err
}

// LedgerHasReserve 判断某任务是否已有 reserve 分录（worker 的付费凭据）。
func LedgerHasReserve(ctx context.Context, q Queryer, jobID string) (bool, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT 1 FROM credit_ledger WHERE job_id = $1 AND entry_type = 'reserve' LIMIT 1`, jobID).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// LedgerRefPrefixExists 判断 reference_key 是否以某前缀开头（reserve / release 的幂等判据）。
func LedgerRefPrefixExists(ctx context.Context, q Queryer, userID, prefix string) (bool, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT 1 FROM credit_ledger WHERE user_id = $1 AND reference_key LIKE $2 || '%' LIMIT 1`, userID, prefix).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// LedgerRefExistsForUser 判断某用户是否已有该 reference_key。
func LedgerRefExistsForUser(ctx context.Context, q Queryer, userID, referenceKey string) (bool, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT 1 FROM credit_ledger WHERE user_id = $1 AND reference_key = $2 LIMIT 1`, userID, referenceKey).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// ReserveEntries 取某任务的全部 reserve 分录（commit / release 要用）。
func ReserveEntries(ctx context.Context, q Queryer, userID, jobID string) ([]struct {
	BucketID string
	Units    int
}, error) {
	rows, err := q.Query(ctx,
		`SELECT balance_bucket_id, units FROM credit_ledger
		 WHERE user_id = $1 AND job_id = $2 AND entry_type = 'reserve' ORDER BY reference_key`, userID, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		BucketID string
		Units    int
	}
	for rows.Next() {
		var e struct {
			BucketID string
			Units    int
		}
		if err := rows.Scan(&e.BucketID, &e.Units); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CommittedFreeCount 统计 commit 分录条数（entitlements 的 freeCompletedImagesRemaining 要用）。
func CommittedFreeCount(ctx context.Context, q Queryer, userID string) (int, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND entry_type = 'commit'`, userID).Scan(&n)
	return n, err
}
