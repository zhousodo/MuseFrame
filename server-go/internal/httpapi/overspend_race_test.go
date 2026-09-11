package httpapi

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentJobsCannotOverspendCredits 🔴 并发提交不能把额度扣成负数。
//
// 场景就是用户双击「生成」，或客户端超时后带**新的** Idempotency-Key 重试：
// 两个请求的幂等键不同，所以幂等那条路拦不住，真正该拦住的是余额检查。
//
// Node 版白拿了进程内互斥（同步 handler + BEGIN IMMEDIATE），
// Go 版是并发 + READ COMMITTED + 零行锁，于是两个请求读到同一份余额、各扣一次。
// 更阴的是它不报错也看不出来：BucketBalances 的 HAVING ... > 0 会把负数桶滤掉，
// 余额显示 0 而不是 -1，用户白拿一张图，账上只收了一张的钱。
//
// 断言：给 1 张额度、并发打 N 个请求，只能有 1 个成功，其余必须 402，
// 且最终余额恰好 0、绝不为负。
func TestConcurrentJobsCannotOverspendCredits(t *testing.T) {
	e := newTestEnv(t)
	uid, tok, pid, aid := e.prepareJobInputs("overspend@example.com", 0)
	// 精确给 1 张额度。
	e.grantUnits(uid, 1)
	if got := e.balance(uid); got != 1 {
		t.Fatalf("前置条件：余额应为 1，实得 %d", got)
	}
	body := jobBody(pid, aid, "ver-style-free")

	const n = 6
	var wg sync.WaitGroup
	results := make([]resp, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := bearer(tok)
			// 每个请求一个**不同**的幂等键：刻意绕开幂等回放，
			// 逼着余额检查自己顶住并发。
			h["Idempotency-Key"] = fmt.Sprintf("overspend-%03d", i)
			<-start
			results[i] = e.do("POST", "/v1/generation-jobs", body, h)
		}(i)
	}
	close(start)
	wg.Wait()

	ok, paymentRequired, other := 0, 0, 0
	for i, r := range results {
		switch r.Code {
		case 200:
			ok++
		case 402:
			paymentRequired++
		default:
			other++
			t.Errorf("第 %d 个请求返回意外状态 %d: %s", i, r.Code, r.Body)
		}
	}
	if ok != 1 {
		t.Errorf("🔴 1 张额度应只允许 1 个任务成功，实得 %d 个 200（%d 个 402）",
			ok, paymentRequired)
	}

	// 最硬的断言：余额不能为负。
	if got := e.balance(uid); got != 0 {
		t.Fatalf("🔴 最终余额应为 0，实得 %d（负数 = 额度被超卖，用户白拿了图）", got)
	}
	// 直接看台账总和，绕过 HAVING > 0 的过滤 —— 负数桶正是被它藏起来的。
	var sum int
	if err := e.st.Pool().QueryRow(nil2ctx(),
		`SELECT COALESCE(SUM(units), 0) FROM credit_ledger WHERE user_id = $1`, uid).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum < 0 {
		t.Fatalf("🔴 credit_ledger 总和为 %d（负数 = 超卖；BucketBalances 的 HAVING>0 会把它藏起来）", sum)
	}
	var jobs int
	if err := e.st.Pool().QueryRow(nil2ctx(),
		`SELECT count(*) FROM generation_jobs WHERE user_id = $1`, uid).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("🔴 应只建 1 条任务，实得 %d", jobs)
	}
}
