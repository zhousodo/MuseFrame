// Package metrics 是进程内的**每接口**请求计数器，专门喂后台的「接口健康」视图。
//
// 为什么不用日志聚合：每条请求已经有一行 LogRequest，但那行只进 docker logs ——
// 运营看不到，而且 logs 会轮转。"App 的 /v1/generation-jobs 过去 24h 有多少 5xx"
// 此前唯一的查法是 SSH + grep，等于没有。
//
// 为什么不引 Prometheus：这套平台没有 metrics 抓取端，装一个 /metrics 端点
// 等于做一个没人抓的页面。后台要的是一个 JSON 视图，直接在进程里算出来最短。
//
// 内存上界是**固定**的：路由数 × 24 个小时桶 × 每桶一个定长直方图。
// 路由名来自注册时的静态路由表（不是请求 URL），所以攻击者没法靠乱打路径把表撑爆。
package metrics

import (
	"sort"
	"sync"
	"time"
)

// Buckets 是延迟直方图的上界（毫秒），最后一格是 +Inf。
//
// 🔴 刻意用固定桶而不是留样本：P95 要在 24h 窗口上算，留样本意味着要么留全部
// （无上界），要么抽样（同一份数据每次刷新出的 P95 不一样，运营会以为在抖动）。
// 固定桶是确定的：同样的请求序列永远给出同一个 P95。
// 代价是 P95 只精确到桶边界之间的线性插值，这对「这个接口是不是慢了」够用。
var Buckets = [nBuckets]int64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// nBuckets 是有限桶的个数；直方图一共 nBuckets+1 格（末格是 +Inf）。
// 必须是常量 —— Go 的数组长度不接受 len(slice)。
const nBuckets = 11

// nHisto 是直方图的格数。
const nHisto = nBuckets + 1

// hourBucket 是一个路由在一个整点小时内的计数。
type hourBucket struct {
	// hour 是这个桶代表的整点（UTC 截断到小时）。用它判断桶是否过期，
	// 省掉一个后台清理 goroutine。
	hour  time.Time
	total int64
	c4xx  int64
	c5xx  int64
	sumMS int64
	maxMS int64
	histo [nHisto]int64
}

// Registry 收集每个路由的 24 个小时桶。
type Registry struct {
	mu   sync.Mutex
	rows map[string]*[24]hourBucket
	// methods 记住路由名对应的 HTTP 方法，纯粹为了后台展示。
	methods map[string]string
	now     func() time.Time
}

// New 构造一个注册表。now 可注入（测试里是确定时钟）。
func New(now func() time.Time) *Registry {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Registry{rows: map[string]*[24]hourBucket{}, methods: map[string]string{}, now: now}
}

// bucketIndex 把时刻映射到 0..23。用「自 Unix 纪元起的小时数 mod 24」而不是
// t.Hour()：两者在 UTC 下等价，但前者对任何时钟偏移都成立，不依赖日界。
func bucketIndex(t time.Time) int { return int((t.Unix() / 3600) % 24) }

// Observe 记一次请求。route 必须是**静态路由名**（如 `GET /v1/generation-jobs/{id}`），
// 绝不能是原始 URL —— 原始 URL 含 id 和令牌，既会撑爆表也会泄露。
func (r *Registry) Observe(method, route string, status int, ms int64) {
	if r == nil || route == "" {
		return
	}
	now := r.now().UTC()
	hour := now.Truncate(time.Hour)
	idx := bucketIndex(now)

	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[route]
	if !ok {
		row = &[24]hourBucket{}
		r.rows[route] = row
		r.methods[route] = method
	}
	b := &row[idx]
	// 桶换代：这一格上次写的是 24 小时前（或更早）的同一个钟点，整格清零。
	if !b.hour.Equal(hour) {
		*b = hourBucket{hour: hour}
	}
	b.total++
	if status >= 500 {
		b.c5xx++
	} else if status >= 400 {
		b.c4xx++
	}
	if ms < 0 {
		ms = 0
	}
	b.sumMS += ms
	if ms > b.maxMS {
		b.maxMS = ms
	}
	b.histo[histoIndex(ms)]++
}

func histoIndex(ms int64) int {
	for i, ub := range Buckets {
		if ms <= ub {
			return i
		}
	}
	return nBuckets
}

// RouteStat 是一个路由在窗口内的汇总，直接序列化给后台。
type RouteStat struct {
	Route    string `json:"route"`
	Method   string `json:"method"`
	Total    int64  `json:"total"`
	Count4xx int64  `json:"count4xx"`
	Count5xx int64  `json:"count5xx"`
	AvgMS    int64  `json:"avgMs"`
	P95MS    int64  `json:"p95Ms"`
	MaxMS    int64  `json:"maxMs"`
	// P95Capped 为真表示 P95 落在最后一个开口桶里（> 10s），展示时要标成「>10000」
	// 而不是一个看起来精确的数字。
	P95Capped bool `json:"p95Capped"`
}

// Snapshot 返回过去 windowHours 小时（含当前小时）内每个路由的汇总，按请求数倒序。
// windowHours 被夹到 [1, 24]。
func (r *Registry) Snapshot(windowHours int) []RouteStat {
	if r == nil {
		return []RouteStat{}
	}
	if windowHours < 1 {
		windowHours = 1
	}
	if windowHours > 24 {
		windowHours = 24
	}
	now := r.now().UTC()
	cutoff := now.Truncate(time.Hour).Add(-time.Duration(windowHours-1) * time.Hour)

	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RouteStat, 0, len(r.rows))
	for route, row := range r.rows {
		var agg hourBucket
		for i := range row {
			b := &row[i]
			// 只收窗口内的桶。!Before(cutoff) 而不是 After：cutoff 那一格本身要算进来。
			if b.total == 0 || b.hour.Before(cutoff) {
				continue
			}
			agg.total += b.total
			agg.c4xx += b.c4xx
			agg.c5xx += b.c5xx
			agg.sumMS += b.sumMS
			if b.maxMS > agg.maxMS {
				agg.maxMS = b.maxMS
			}
			for j := range b.histo {
				agg.histo[j] += b.histo[j]
			}
		}
		if agg.total == 0 {
			continue
		}
		p95, capped := percentile(agg.histo, agg.total, 0.95)
		out = append(out, RouteStat{
			Route: route, Method: r.methods[route], Total: agg.total,
			Count4xx: agg.c4xx, Count5xx: agg.c5xx,
			AvgMS: agg.sumMS / agg.total, P95MS: p95, MaxMS: agg.maxMS, P95Capped: capped,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Route < out[j].Route
	})
	return out
}

// percentile 从直方图算分位数，返回 (毫秒, 是否落在开口桶)。
//
// 🔴 目标次序必须是 ceil(q*total) 并且至少 1：用 int64(q*total) 会在
// total=1 时算出 0，然后第一个桶的累计计数 1 >= 0 立刻命中 —— 哪怕那一次请求
// 花了 8 秒，P95 也会报成 5ms（第一个桶的上界）。
func percentile(histo [nHisto]int64, total int64, q float64) (int64, bool) {
	if total <= 0 {
		return 0, false
	}
	target := int64(float64(total)*q + 0.999999)
	if target < 1 {
		target = 1
	}
	if target > total {
		target = total
	}
	var cum int64
	for i, n := range histo {
		cum += n
		if cum >= target {
			if i == nBuckets {
				return Buckets[nBuckets-1], true
			}
			return Buckets[i], false
		}
	}
	return Buckets[nBuckets-1], true
}
