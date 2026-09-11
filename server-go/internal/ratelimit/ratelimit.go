// 滑动窗口限流。键 = 规则源串 + 客户端 IP，内存 map，上限 RATE_LIMIT_MAX_KEYS，
// 60 秒清一次过期键。逐条复刻原实现 index.js:104-132。
package ratelimit

import (
	"sync"
	"time"
)

type entry struct {
	count int
	reset time.Time
}

// Limiter 是进程内的限流器。map 本身曾是一条内存增长杠杆（键来自客户端地址），
// 所以必须有键数上限。
type Limiter struct {
	mu      sync.Mutex
	hits    map[string]*entry
	order   []string // 插入顺序，逼近 LRU（与原实现依赖 Map 插入序等价）
	maxKeys int
	now     func() time.Time
}

func New(maxKeys int, now func() time.Time) *Limiter {
	if maxKeys <= 0 {
		maxKeys = 50000
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{hits: make(map[string]*entry), maxKeys: maxKeys, now: now}
}

// Hit 记一次命中。返回 0 表示放行；返回正数表示超限，值是 Retry-After 秒数。
func (l *Limiter) Hit(ip, bucket string, limit int, window time.Duration) int {
	key := bucket + ":" + ip
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.hits[key]
	if !ok || e.reset.Before(now) {
		if len(l.hits) >= l.maxKeys {
			l.evictLocked()
		}
		e = &entry{reset: now.Add(window)}
		if _, existed := l.hits[key]; !existed {
			l.order = append(l.order, key)
		}
		l.hits[key] = e
	}
	e.count++
	if e.count <= limit {
		return 0
	}
	secs := int((e.reset.Sub(now) + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

func (l *Limiter) evictLocked() {
	target := int(float64(l.maxKeys) * 0.9)
	i := 0
	for ; i < len(l.order) && len(l.hits) >= target; i++ {
		delete(l.hits, l.order[i])
	}
	l.order = append([]string(nil), l.order[i:]...)
}

// Sweep 清掉已过期的键。调用方每 60 秒跑一次。
func (l *Limiter) Sweep() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	removed := 0
	kept := l.order[:0]
	for _, k := range l.order {
		if e, ok := l.hits[k]; ok {
			if e.reset.Before(now) {
				delete(l.hits, k)
				removed++
				continue
			}
			kept = append(kept, k)
		}
	}
	l.order = append([]string(nil), kept...)
	return removed
}

// Size 返回当前键数（测试与运维观测用）。
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}
