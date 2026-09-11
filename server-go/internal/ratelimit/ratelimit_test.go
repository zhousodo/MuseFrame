package ratelimit

import (
	"testing"
	"time"
)

func TestSlidingWindow(t *testing.T) {
	now := time.Date(2026, 9, 11, 4, 0, 0, 0, time.UTC)
	l := New(100, func() time.Time { return now })
	// 邮件验证码是全表最严的一条：5 次 / 10 分钟。
	for i := 0; i < 5; i++ {
		if retry := l.Hit("1.2.3.4", "email", 5, 10*time.Minute); retry != 0 {
			t.Fatalf("第 %d 次不应被限流", i+1)
		}
	}
	retry := l.Hit("1.2.3.4", "email", 5, 10*time.Minute)
	if retry <= 0 || retry > 600 {
		t.Fatalf("第 6 次应被限流并给出 Retry-After，实际 %d", retry)
	}
	// 换一个 IP 不受影响（键 = 规则源串 + IP）。
	if r := l.Hit("5.6.7.8", "email", 5, 10*time.Minute); r != 0 {
		t.Fatalf("不同 IP 不应被牵连，实际 %d", r)
	}
	// 窗口过去后放行。
	now = now.Add(11 * time.Minute)
	if r := l.Hit("1.2.3.4", "email", 5, 10*time.Minute); r != 0 {
		t.Fatalf("窗口过后应放行，实际 %d", r)
	}
}

func TestSweepAndKeyCap(t *testing.T) {
	now := time.Date(2026, 9, 11, 4, 0, 0, 0, time.UTC)
	l := New(10, func() time.Time { return now })
	for i := 0; i < 50; i++ {
		l.Hit(string(rune('a'+i%26))+string(rune('0'+i/26)), "b", 100, time.Minute)
	}
	if l.Size() > 10 {
		t.Fatalf("键数必须有上限（它本身是一条内存增长杠杆），实际 %d", l.Size())
	}
	now = now.Add(2 * time.Minute)
	l.Sweep()
	if l.Size() != 0 {
		t.Fatalf("过期键应被清掉，实际 %d", l.Size())
	}
}
