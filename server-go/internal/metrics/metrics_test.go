package metrics

import (
	"sync"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)

func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestObserveCountsStatusClasses(t *testing.T) {
	r := New(fixed(base))
	r.Observe("GET", "GET /v1/projects", 200, 10)
	r.Observe("GET", "GET /v1/projects", 404, 12)
	r.Observe("GET", "GET /v1/projects", 500, 14)
	r.Observe("GET", "GET /v1/projects", 503, 16)

	s := r.Snapshot(24)
	if len(s) != 1 {
		t.Fatalf("应当只有一条路由，实际 %d", len(s))
	}
	got := s[0]
	if got.Total != 4 {
		t.Errorf("total 应为 4，实际 %d", got.Total)
	}
	// 🔴 变异验证：把 Observe 里的 `status >= 400` 写成 `status > 400`，
	// 这个 404 就会掉出 4xx 计数，下面这行会红。
	if got.Count4xx != 1 {
		t.Errorf("4xx 应为 1，实际 %d", got.Count4xx)
	}
	// 🔴 变异验证：把 5xx 和 4xx 的判断次序对调（先判 >= 400），
	// 两个 5xx 会被算进 4xx，这两行会同时红。
	if got.Count5xx != 2 {
		t.Errorf("5xx 应为 2，实际 %d", got.Count5xx)
	}
	if got.Method != "GET" {
		t.Errorf("method 应为 GET，实际 %q", got.Method)
	}
}

// P95 的单样本行为是 percentile 里那个 ceil 的全部理由。
func TestP95OfSingleSlowRequestIsNotTheFirstBucket(t *testing.T) {
	r := New(fixed(base))
	r.Observe("POST", "POST /v1/generation-jobs", 200, 8000)

	s := r.Snapshot(24)
	if len(s) != 1 {
		t.Fatalf("应当有一条路由")
	}
	// 🔴 单样本必须落在它自己那一桶，不能落到第一个桶。
	// （这里的下限由 percentile 里的 `if target < 1 { target = 1 }` 兜住；
	//  ceil 本身的作用由 TestP95With10SamplesRoundsUpToTheSlowOne 钉住 ——
	//  两道守卫各自有探针，缺一条都会被照出来。）
	if s[0].P95MS != 10000 {
		t.Errorf("单次 8000ms 请求的 P95 应落在 10000ms 桶，实际 %d", s[0].P95MS)
	}
	if s[0].P95Capped {
		t.Errorf("8000ms <= 10000ms，不该标成开口桶")
	}
	if s[0].MaxMS != 8000 {
		t.Errorf("max 应为 8000，实际 %d", s[0].MaxMS)
	}
	if s[0].AvgMS != 8000 {
		t.Errorf("avg 应为 8000，实际 %d", s[0].AvgMS)
	}
}

// 10 个样本里 1 个慢的：P95 必须落在那个慢的上（ceil(0.95*10)=10），
// 而截断会算出 9 -> 命中最后一个快样本，把「每 20 次请求就有一次 5 秒」藏起来。
func TestP95With10SamplesRoundsUpToTheSlowOne(t *testing.T) {
	r := New(fixed(base))
	for i := 0; i < 9; i++ {
		r.Observe("GET", "GET /v1/tenth", 200, 10)
	}
	r.Observe("GET", "GET /v1/tenth", 200, 5000)
	s := r.Snapshot(24)
	// 🔴 变异验证：把 percentile 的 `+ 0.999999` 去掉（退化成截断），
	// target 从 10 变成 9 -> P95 报成 10ms。这一行是那个 bug 的探针。
	// （单样本那条用例被 `target < 1` 的下限兜住了，照不出这个差异。）
	if s[0].P95MS != 5000 {
		t.Errorf("9 快 + 1 慢的 P95 应当是 5000ms，实际 %d", s[0].P95MS)
	}
}

func TestP95BeyondLastBucketIsCapped(t *testing.T) {
	r := New(fixed(base))
	r.Observe("GET", "GET /v1/slow", 200, 60000)
	s := r.Snapshot(24)
	if !s[0].P95Capped {
		t.Errorf("60000ms 落在开口桶，P95Capped 应为真")
	}
	if s[0].MaxMS != 60000 {
		t.Errorf("max 应保留真实值 60000，实际 %d", s[0].MaxMS)
	}
}

func TestP95With100SamplesPicksNinetyFifth(t *testing.T) {
	r := New(fixed(base))
	// 95 个 10ms + 5 个 5000ms：第 95 个次序统计量是最后一个 10ms。
	for i := 0; i < 95; i++ {
		r.Observe("GET", "GET /v1/mixed", 200, 10)
	}
	for i := 0; i < 5; i++ {
		r.Observe("GET", "GET /v1/mixed", 200, 5000)
	}
	s := r.Snapshot(24)
	if s[0].P95MS != 10 {
		t.Errorf("P95 应为 10ms（第 95 个样本），实际 %d", s[0].P95MS)
	}
	if s[0].Total != 100 {
		t.Errorf("total 应为 100，实际 %d", s[0].Total)
	}
}

// 24 小时之外的同一个钟点必须被整格清掉，不能把一天前的请求算进来。
func TestBucketRollsOverAfter24Hours(t *testing.T) {
	now := base
	r := New(func() time.Time { return now })
	r.Observe("GET", "GET /v1/projects", 200, 10)
	if got := r.Snapshot(24); len(got) != 1 || got[0].Total != 1 {
		t.Fatalf("刚记的那一条应当可见")
	}
	// 往后走 24 小时整：落回同一个桶下标，但 hour 不同。
	now = base.Add(24 * time.Hour)
	r.Observe("GET", "GET /v1/projects", 200, 20)
	s := r.Snapshot(24)
	// 🔴 变异验证：删掉 Observe 里的 `if !b.hour.Equal(hour) { *b = ... }` 换代，
	// total 会变成 2 —— 一天前的请求永远留在「过去 24 小时」里。
	if s[0].Total != 1 {
		t.Errorf("换代后 total 应为 1（旧格清零），实际 %d", s[0].Total)
	}
	if s[0].MaxMS != 20 {
		t.Errorf("换代后 max 应为 20（旧值清掉），实际 %d", s[0].MaxMS)
	}
}

// 窗口边界：cutoff 那一格本身要算进来（!Before 而不是 After）。
func TestWindowIncludesCutoffHour(t *testing.T) {
	now := base
	r := New(func() time.Time { return now })
	r.Observe("GET", "GET /v1/a", 200, 10) // 10:30
	now = base.Add(time.Hour)
	r.Observe("GET", "GET /v1/a", 200, 10) // 11:30
	now = base.Add(2 * time.Hour)          // 12:30

	// 窗口 3 小时 = 10:00 / 11:00 / 12:00 三格，两条都在内。
	if got := r.Snapshot(3); got[0].Total != 2 {
		t.Errorf("3 小时窗口应含两条，实际 %d", got[0].Total)
	}
	// 窗口 2 小时 = 11:00 / 12:00，只剩 11:30 那一条。
	// 🔴 变异验证：把 `b.hour.Before(cutoff)` 改成 `b.hour.Before(cutoff.Add(time.Hour))`
	// 或把 cutoff 的 windowHours-1 写成 windowHours，这行会红。
	if got := r.Snapshot(2); got[0].Total != 1 {
		t.Errorf("2 小时窗口应只含一条，实际 %d", got[0].Total)
	}
	// 窗口 1 小时 = 只有 12:00 那一格，里面一条都没有 —— Snapshot 会把
	// total 为 0 的路由整条略掉，所以回的是**空切片**（不是一条 total=0 的行）。
	if got := r.Snapshot(1); len(got) != 0 {
		t.Errorf("1 小时窗口（12:00）应当回空切片，实际 %+v", got)
	}
}

func TestEmptyRouteNameIsIgnored(t *testing.T) {
	r := New(fixed(base))
	r.Observe("GET", "", 200, 10)
	if got := r.Snapshot(24); len(got) != 0 {
		t.Errorf("空路由名不该进表，实际 %+v", got)
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var r *Registry
	r.Observe("GET", "GET /v1/x", 200, 1) // 不该 panic
	if got := r.Snapshot(24); len(got) != 0 {
		t.Errorf("nil 注册表应当回空切片")
	}
}

func TestSnapshotSortsByTotalDesc(t *testing.T) {
	r := New(fixed(base))
	r.Observe("GET", "GET /v1/b", 200, 1)
	for i := 0; i < 3; i++ {
		r.Observe("GET", "GET /v1/a", 200, 1)
	}
	s := r.Snapshot(24)
	if len(s) != 2 || s[0].Route != "GET /v1/a" {
		t.Errorf("应当按请求数倒序，实际 %+v", s)
	}
}

func TestConcurrentObserveIsRaceFree(t *testing.T) {
	r := New(fixed(base))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.Observe("GET", "GET /v1/hot", 200, int64(j))
			}
		}()
	}
	wg.Wait()
	if got := r.Snapshot(24); got[0].Total != 1000 {
		t.Errorf("total 应为 1000，实际 %d", got[0].Total)
	}
}

func TestNegativeLatencyIsClampedToZero(t *testing.T) {
	r := New(fixed(base))
	// 时钟回拨（NTP 校时）会让 end-start 变成负数。
	r.Observe("GET", "GET /v1/x", 200, -5)
	s := r.Snapshot(24)
	if s[0].AvgMS != 0 || s[0].MaxMS != 0 {
		t.Errorf("负延迟应夹到 0，实际 avg=%d max=%d", s[0].AvgMS, s[0].MaxMS)
	}
}
