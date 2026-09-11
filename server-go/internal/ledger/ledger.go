// append-only 额度台账。余额是分录之上的投影；reserve → commit（成功）
// 或 reserve → release（失败/取消）。每条路径都靠
// UNIQUE (user_id, reference_key) 幂等。逐条复刻 Node 版 server/ledger.js。
//
// reference_key 命名约定（幂等的唯一依据，改了就会重复发放）：
//
//	发放       grant:<sourceType>:<referenceId 或 sourceId 或 bucketId>
//	预留       job:<jobId>:reserve:<partIndex>   （跨桶拆分时 part 递增）
//	核销       job:<jobId>:commit
//	释放       job:<jobId>:release:<i>
//	订阅续期   grant:purchase:<purchaseId>:<periodEnd>
package ledger

import (
	"context"
	"errors"
	"time"

	"museframe-api/internal/store"
)

// ErrInsufficient 是余额不足。调用方把它翻成 402 INSUFFICIENT_ENTITLEMENT。
type ErrInsufficient struct {
	RequiredUnits  int
	AvailableUnits int
}

func (e *ErrInsufficient) Error() string { return "A standard image is required." }

// IsInsufficient 判断错误是否为余额不足。
func IsInsufficient(err error) (*ErrInsufficient, bool) {
	var e *ErrInsufficient
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// NewID 由调用方注入（生产用 uuid.v4，测试可固定）。
type NewID func() string

// Grant 发放额度：建桶 + 写 grant 分录。返回 bucketId。
//
// referenceID 独立于 sourceID 限定幂等键：续期的订阅保持同一条 purchases 行，
// 但必须每个计费周期发一次，所以传 "<purchaseId>:<periodEnd>"，
// 而 source_id 仍指向这笔订单。
func Grant(ctx context.Context, q store.Queryer, newID NewID, userID string, units int, sourceType string, sourceID *string, expiresAt *time.Time, referenceID *string, now time.Time) (string, error) {
	bucketID := newID()
	if err := store.InsertBucket(ctx, q, bucketID, userID, sourceType, sourceID, units, expiresAt, now); err != nil {
		return "", err
	}
	ref := bucketID
	switch {
	case referenceID != nil && *referenceID != "":
		ref = *referenceID
	case sourceID != nil && *sourceID != "":
		ref = *sourceID
	}
	var purchaseID *string
	if sourceType == "purchase" {
		purchaseID = sourceID
	}
	err := store.InsertLedger(ctx, q, newID(), userID, "grant", units, bucketID, nil, purchaseID,
		"grant:"+sourceType+":"+ref, now)
	if err != nil {
		return "", err
	}
	return bucketID, nil
}

// Reserve 为一个任务预留 units。最早过期的桶先扣。
// 余额不足抛 ErrInsufficient。对同一任务重复调用是空操作。
//
// 🔴 调用方必须把它和「INSERT generation_jobs」「UPDATE projects.status」放进
// 同一个事务：Node 版曾经分两次提交，留下「行已建、预留失败」的白嫖任务。
func Reserve(ctx context.Context, q store.Queryer, newID NewID, userID, jobID string, units int, now time.Time) error {
	// 🔴 先上锁再读余额。不上锁时「读余额 → 判断 → 写扣减」是可交错的，
	// 两个并发请求会拿同一份余额各扣一次，把桶扣成负数（详见 store.LockUserCredits）。
	if err := store.LockUserCredits(ctx, q, userID); err != nil {
		return err
	}
	exists, err := store.LedgerRefPrefixExists(ctx, q, userID, "job:"+jobID+":reserve")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	buckets, err := store.BucketBalances(ctx, q, userID, now)
	if err != nil {
		return err
	}
	total := 0
	for _, b := range buckets {
		total += b.Balance
	}
	if total < units {
		return &ErrInsufficient{RequiredUnits: units, AvailableUnits: total}
	}
	remaining, part := units, 0
	for _, b := range buckets {
		if remaining <= 0 {
			break
		}
		take := b.Balance
		if take > remaining {
			take = remaining
		}
		ref := "job:" + jobID + ":reserve:" + itoa(part)
		if err := store.InsertLedger(ctx, q, newID(), userID, "reserve", -take, b.ID, &jobID, nil, ref, now); err != nil {
			return err
		}
		part++
		remaining -= take
	}
	return nil
}

// Commit 核销一个成功的任务：预留变消耗（commit 分录 units 恒为 0，DB 有 CHECK）。
func Commit(ctx context.Context, q store.Queryer, newID NewID, userID, jobID string, now time.Time) error {
	ref := "job:" + jobID + ":commit"
	done, err := store.LedgerRefExistsForUser(ctx, q, userID, ref)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	reserves, err := store.ReserveEntries(ctx, q, userID, jobID)
	if err != nil {
		return err
	}
	if len(reserves) == 0 {
		return nil
	}
	return store.InsertLedger(ctx, q, newID(), userID, "commit", 0, reserves[0].BucketID, &jobID, nil, ref, now)
}

// Release 退还一个失败 / 取消任务的全部预留。
// 🔴 必须先检查是否已 commit —— 否则「成功后再取消」会把钱退两次。
func Release(ctx context.Context, q store.Queryer, newID NewID, userID, jobID string, now time.Time) error {
	released, err := store.LedgerRefPrefixExists(ctx, q, userID, "job:"+jobID+":release")
	if err != nil {
		return err
	}
	if released {
		return nil
	}
	committed, err := store.LedgerRefExistsForUser(ctx, q, userID, "job:"+jobID+":commit")
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	reserves, err := store.ReserveEntries(ctx, q, userID, jobID)
	if err != nil {
		return err
	}
	for i, r := range reserves {
		ref := "job:" + jobID + ":release:" + itoa(i)
		if err := store.InsertLedger(ctx, q, newID(), userID, "release", -r.Units, r.BucketID, &jobID, nil, ref, now); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
