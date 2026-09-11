package httpapi

import (
	"sync"
	"testing"
)

// TestIdempotencyKeyRaceChargesOnce 🔴 并发抢同一个 Idempotency-Key 只能扣一次额度。
//
// 这是 Node 版白给的保证：那边的 handler 是**同步**箭头函数，
// 跑在 BEGIN IMMEDIATE 的 SQLite 句柄上（server/db.js:372，server/api.js:797），
// 「查幂等记录 → 建任务 → 扣额度」进程内互斥，两个并发请求里慢的那个
// 必然看得到记录并走回放。
//
// Go 版把它丢了：幂等记录原来在事务**之后**、用连接池单独写，于是两个并发请求
// 双双查不到记录 → 各建一个任务、各预留一份额度、各自入队 ——
// 一次用户点击扣两份额度出两张图，而慢的那个还会在写幂等记录时撞主键拿到 500
// （额度已经花掉了）。App 的超时重试和用户双击都能轻易触发。
//
// 修复是把幂等记录挪进同一个事务：PRIMARY KEY (user_id, idempotency_key)
// 本身就是串行化点，输的那个整笔回滚再回放赢家的响应。
func TestIdempotencyKeyRaceChargesOnce(t *testing.T) {
	e := newTestEnv(t)
	uid, tok, pid, aid := e.prepareJobInputs("idem-race@example.com", 5)
	body := jobBody(pid, aid, "ver-style-free")
	before := e.balance(uid)

	const n = 6
	h := bearer(tok)
	h["Idempotency-Key"] = "double-tap-0001"

	var wg sync.WaitGroup
	results := make([]resp, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 每个 goroutine 用自己的 header map —— 共享 map 并发读写会被 race 检测器抓。
			hh := bearer(tok)
			hh["Idempotency-Key"] = "double-tap-0001"
			<-start
			results[i] = e.do("POST", "/v1/generation-jobs", body, hh)
		}(i)
	}
	close(start)
	wg.Wait()

	// 每一个请求都必须拿到 200（要么首次成功，要么回放），不许出现 500。
	bodies := map[string]int{}
	for i, r := range results {
		if r.Code != 200 {
			t.Errorf("🔴 第 %d 个并发请求返回 %d（期望 200 —— 竞态不该暴露成错误）: %s",
				i, r.Code, r.Body)
			continue
		}
		bodies[string(r.Body)]++
	}
	// 所有 200 的响应体必须逐字节相同 —— 说明大家看到的是同一个任务。
	if len(bodies) > 1 {
		t.Errorf("🔴 %d 个不同的响应体，说明建了多个任务：", len(bodies))
		for b, c := range bodies {
			t.Errorf("  x%d %s", c, b)
		}
	}

	// 最硬的断言：额度只能扣 1 份。
	after := e.balance(uid)
	if before-after != 1 {
		t.Fatalf("🔴 一个 Idempotency-Key 扣了 %d 份额度（期望恰好 1）：扣前 %d，扣后 %d",
			before-after, before, after)
	}

	// 任务行也只能有一条。
	var jobs int
	if err := e.st.Pool().QueryRow(nil2ctx(),
		`SELECT count(*) FROM generation_jobs WHERE user_id = $1`, uid).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("🔴 建了 %d 条任务（期望 1）", jobs)
	}
}
