package provider

import (
	"testing"

	"museframe-api/internal/cfgstore"
)

// 没有密钥 / 没有地址就绝不生成 —— 而且密钥只可能来自环境变量。
func TestStatusGating(t *testing.T) {
	rt := cfgstore.NewForTest(map[string]string{"IMAGE_PROVIDER_BASE_URL": "https://p.invalid"})

	noKey := New(rt, "remote", "")
	s := noKey.Status()
	if s.Available || s.Mode != "none" || s.Reason == nil || *s.Reason != "PROVIDER_NOT_CONFIGURED" {
		t.Fatalf("缺密钥必须不可用，实际 %#v", s)
	}
	if len(s.Missing) != 1 || s.Missing[0] != "image_provider_api_key" {
		t.Fatalf("应点名缺失的设置，实际 %v", s.Missing)
	}

	noURL := New(cfgstore.NewForTest(nil), "remote", "sk-x")
	if s := noURL.Status(); s.Available {
		t.Fatal("缺地址也必须不可用")
	}

	ok := New(rt, "remote", "sk-x")
	if s := ok.Status(); !s.Available || s.Mode != "remote" || s.Reason != nil {
		t.Fatalf("密钥 + 地址齐备时应可用且 reason 为 nil，实际 %#v", s)
	}

	// IMAGE_PROVIDER 是 env-only：空值必须默认 remote，而不是静默降级成本地像素引擎。
	if New(rt, "", "sk-x").ProviderName() != "remote" {
		t.Fatal("IMAGE_PROVIDER 留空必须默认 remote，绝不能降级 local")
	}
	if s := New(rt, "local", "").Status(); !s.Available || s.Mode != "local" {
		t.Fatalf("显式 local 才走本地，实际 %#v", s)
	}
	if s := New(rt, "weird", "sk-x").Status(); s.Available || *s.Reason != "PROVIDER_UNKNOWN" {
		t.Fatalf("未知 provider 必须拒绝，实际 %#v", s)
	}
}

// 超时被夹到 undici 口径的上限，让后台显示、用户估时与代码三者一致。
func TestTimeoutClamped(t *testing.T) {
	a := New(cfgstore.NewForTest(map[string]string{"IMAGE_PROVIDER_TIMEOUT_MS": "420000"}), "remote", "k")
	if got := a.TimeoutMS(); got != 290000 {
		t.Fatalf("420000 应被夹到 290000，实际 %d", got)
	}
	b := New(cfgstore.NewForTest(map[string]string{"IMAGE_PROVIDER_TIMEOUT_MS": "60000"}), "remote", "k")
	if got := b.TimeoutMS(); got != 60000 {
		t.Fatalf("小于上限的值应原样保留，实际 %d", got)
	}
}

// 尺寸网格与档位映射。
func TestPickSizeAndQuality(t *testing.T) {
	cases := []struct {
		ratio string
		w, h  int
		want  string
	}{
		{"1:1", 100, 100, "1024x1024"},
		{"16:9", 100, 100, "1536x1024"},
		{"4:5", 100, 100, "1024x1536"},
		{"original", 2000, 1000, "1536x1024"},
		{"original", 1000, 2000, "1024x1536"},
		{"original", 1000, 1000, "1024x1024"},
	}
	// 默认配置下 PickSize 必须逐字复现 2026-09-12 之前那三个写死的字符串 ——
	// 把尺寸做成可配项不能顺手改掉默认输出（那等于给所有人换了一次分辨率与单价）。
	def := New(cfgstore.NewForTest(nil), "remote", "k")
	for _, c := range cases {
		if got := def.PickSize(c.ratio, c.w, c.h); got != c.want {
			t.Errorf("PickSize(%s,%d,%d)=%s，期望 %s", c.ratio, c.w, c.h, got, c.want)
		}
	}
	a := New(cfgstore.NewForTest(nil), "remote", "k")
	if a.QualityFor("standard") != "medium" || a.QualityFor("high") != "high" {
		t.Fatal("档位默认值应为 medium / high")
	}
	bad := New(cfgstore.NewForTest(map[string]string{"IMAGE_QUALITY_STANDARD": "ultra"}), "remote", "k")
	if bad.QualityFor("standard") != "medium" {
		t.Fatal("非法档位必须回落，而不是原样发给上游")
	}
}
