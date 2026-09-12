package cfgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 每一项都必须有分组。没有分组的项在后台会落到「未分组」里（好一点的情况）
// 或者干脆不渲染 —— 而「注册表里有、面板上看不见」的配置项等于不存在：
// 运营改不了它，却以为面板上就是全部。
func TestEveryItemHasGroup(t *testing.T) {
	known := map[string]bool{
		GroupGeneration: true, GroupCredits: true, GroupAuth: true, GroupRetention: true,
		GroupEmail: true, GroupStorage: true, GroupSupport: true, GroupAIGC: true,
		GroupDeploy: true,
	}
	for _, it := range Registry {
		if it.Group == "" {
			t.Errorf("🔴 注册项 %s 没有 Group —— 它不会出现在后台任何一个小节里", it.Key)
			continue
		}
		if !known[it.Group] {
			t.Errorf("🔴 注册项 %s 的 Group=%q 不是已知分组，后台会把它渲染到一个空小节里",
				it.Key, it.Group)
		}
	}
	// List() 必须把 Group 带出去，否则前端拿不到它，分组在后端存了也没用。
	s := NewForTest(nil)
	for _, st := range s.List() {
		if st.Group == "" {
			t.Fatalf("🔴 %s 的 Setting.Group 是空的 —— 分组没有出现在 /v1/admin/config 的出参里", st.Key)
		}
	}
}

// strEnums 里登记的键必须真的存在且是字符串型。
// 拼错一个键名 = 那一项实际上毫无白名单校验，而看代码像有（numRanges 同款陷阱）。
func TestStrEnumsPointAtRealStringKeys(t *testing.T) {
	for k, vals := range strEnums {
		it, ok := byKey[k]
		if !ok {
			t.Errorf("🔴 strEnums 登记了不存在的键 %s —— 那一项实际上没有任何白名单校验", k)
			continue
		}
		if it.Type != KindString {
			t.Errorf("🔴 strEnums 登记了非字符串项 %s（类型 %s）", k, it.Type)
		}
		// 默认值自己必须在白名单里，否则「恢复默认」之后这一项立刻带 warning。
		def, _ := it.Default.(string)
		found := false
		for _, v := range vals {
			if v == def {
				found = true
			}
		}
		if !found {
			t.Errorf("🔴 %s 的默认值 %q 不在自己的白名单 %v 里", k, def, vals)
		}
	}
}

// 白名单必须同时管「写」和「读」两头。
//
// 写：后台保存一个白名单外的值要当场 422，而不是存进去。
// 读：库里/env 里已经躺着脏值时，EnumString 回落默认值，且 List() 必须把
//
//	「源里写的和实际生效的不一样」这件事标出来 —— 否则面板显示 "ultra"、
//	实际发给上游的是 "medium"，而没有任何地方说过这件事。
func TestEnumRejectedOnWriteAndFlaggedOnRead(t *testing.T) {
	ctx := context.Background()
	be := &fakeBackend{rows: map[string]string{}}
	s, err := New(ctx, be)
	if err != nil {
		t.Fatal(err)
	}
	s.lookup = func(string) (string, bool) { return "", false }

	// ① 写：白名单外直接拒绝，且**没有**落库。
	if err := s.Set(ctx, "image_quality_standard", "ultra"); err == nil {
		t.Fatal("🔴 白名单外的质量档位必须被拒绝 —— 存进去之后面板和上游会永久不一致")
	} else if !strings.Contains(err.Error(), "low") {
		t.Fatalf("错误信息里必须写出合法取值，实际 %q", err.Error())
	}
	if _, ok := be.rows["image_quality_standard"]; ok {
		t.Fatal("🔴 被拒绝的值不许落库（校验必须在 UpsertAppConfig 之前）")
	}
	// 白名单内的值照常收下。
	if err := s.Set(ctx, "image_quality_standard", "low"); err != nil {
		t.Fatalf("合法值应能保存：%v", err)
	}
	if got := s.EnumString("image_quality_standard"); got != "low" {
		t.Fatalf("保存后生效值应为 low，实际 %q", got)
	}

	// ② 读：库里已经躺着 Node 版写下的脏值（或有人手改了 app_config）。
	be2 := &fakeBackend{rows: map[string]string{"image_size_portrait": "999x999"}}
	s2, err := New(ctx, be2)
	if err != nil {
		t.Fatal(err)
	}
	s2.lookup = func(string) (string, bool) { return "", false }
	if got := s2.EnumString("image_size_portrait"); got != "1024x1536" {
		t.Fatalf("🔴 脏值必须回落默认值（否则 999x999 会被原样发给上游 → 400），实际 %q", got)
	}
	var found bool
	for _, st := range s2.List() {
		if st.Key != "image_size_portrait" {
			continue
		}
		found = true
		if st.Warning == "" {
			t.Fatal("🔴 源里写的值和实际生效的不一样时必须挂 warning —— " +
				"不标出来的话面板会展示一个看似正常的值而真正生效的是另一个")
		}
		if st.Value != "1024x1536" {
			t.Fatalf("Value 必须是**实际生效**的值，实际 %v", st.Value)
		}
		if len(st.Enum) == 0 {
			t.Fatal("带白名单的项必须把 Enum 带给前端，否则前端只能渲染自由文本框")
		}
	}
	if !found {
		t.Fatal("List() 里找不到 image_size_portrait")
	}
}

// ImageSizeFor 必须逐字复现 2026-09-12 之前 PickSize 里那三个写死的字符串。
// 把尺寸做成可配项不能顺手改掉默认输出 —— 那等于给所有人换了一次分辨率与单价。
func TestImageSizeDefaultsUnchanged(t *testing.T) {
	s := NewForTest(nil)
	for _, c := range []struct{ orientation, want string }{
		{"square", "1024x1024"},
		{"landscape", "1536x1024"},
		{"portrait", "1024x1536"},
		// 未知朝向回落方图（与 Orientation 的兜底一致）。
		{"", "1024x1024"},
	} {
		if got := s.ImageSizeFor(c.orientation); got != c.want {
			t.Errorf("ImageSizeFor(%q)=%q，期望 %q", c.orientation, got, c.want)
		}
	}
}

// 🔴 0 天必须映射成 nil（永不过期），而不是 now（发出来就是死的）。
// 这两件事只差一个 Add(0)，后果差一整笔额度。
func TestCreditExpiryZeroIsNilNotNow(t *testing.T) {
	s := NewForTest(nil)
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

	// free_credit_expiry_days 默认 0 → nil（等于 2026-09-12 之前写死的 nil，零行为变更）。
	if got := s.CreditExpiry("free_credit_expiry_days", now); got != nil {
		t.Fatalf("🔴 0 天必须是「永不过期」（nil），实际拿到 %v —— "+
			"返回 now 会让每一笔免费额度一发出来就是过期的", got)
	}
	// pack_credit_expiry_days 默认 90 → now+90d（等于此前写死的 90*24h）。
	got := s.CreditExpiry("pack_credit_expiry_days", now)
	if got == nil {
		t.Fatal("90 天不该是 nil")
	}
	if want := now.Add(90 * 24 * time.Hour); !got.Equal(want) {
		t.Fatalf("加购有效期应为 %v，实际 %v", want, *got)
	}

	// 显式设成 0 之后也必须是 nil（这是运营为了对齐 App 的「永不过期」文案会做的动作）。
	ctx := context.Background()
	be := &fakeBackend{rows: map[string]string{}}
	s2, err := New(ctx, be)
	if err != nil {
		t.Fatal(err)
	}
	s2.lookup = func(string) (string, bool) { return "", false }
	if err := s2.Set(ctx, "pack_credit_expiry_days", float64(0)); err != nil {
		t.Fatalf("0 是合法值（区间下界就是 0）：%v", err)
	}
	if got := s2.CreditExpiry("pack_credit_expiry_days", now); got != nil {
		t.Fatalf("🔴 设成 0 之后加购额度必须永不过期，实际 %v", got)
	}
}

// 熔断阈值 / 冷却 / 重试次数的夹取：任何来源的 0 都不能把服务打死。
func TestBreakerAndAttemptsClamped(t *testing.T) {
	s := NewForTest(map[string]string{
		// 有人手写了 0：重试 0 次 = worker 在第一次尝试前就判定超限 → 所有任务立刻失败。
		"MAX_JOB_ATTEMPTS": "0",
		// 冷却 0 秒 = 熔断退化成「每个任务都去打一次已知挂掉的上游」。
		"PROVIDER_BREAKER_COOLDOWN_SECONDS": "0",
		"PROVIDER_BREAKER_STREAK":           "0",
	})
	if got := s.MaxJobAttempts(); got != 1 {
		t.Fatalf("🔴 MAX_JOB_ATTEMPTS=0 必须被夹到 1，实际 %d（0 会让所有生成任务立刻失败）", got)
	}
	if got := s.BreakerStreak(); got != 1 {
		t.Fatalf("PROVIDER_BREAKER_STREAK=0 应夹到 1，实际 %d", got)
	}
	if got := s.BreakerCooldown(); got != 5*time.Second {
		t.Fatalf("冷却 0 秒应夹到下界 5 秒，实际 %v", got)
	}
	// 默认值必须等于 2026-09-12 之前 health.go 里那两个常量。
	def := NewForTest(nil)
	if def.BreakerStreak() != 3 || def.BreakerCooldown() != 60*time.Second {
		t.Fatalf("默认熔断参数必须保持 3 次 / 60 秒，实际 %d / %v",
			def.BreakerStreak(), def.BreakerCooldown())
	}
	if def.MaxJobAttempts() != 3 {
		t.Fatalf("默认重试次数必须保持 3，实际 %d", def.MaxJobAttempts())
	}
}

// SettingValue 给审计用：它必须和 List() 里那一项的值一致，且密钥只出掩码。
func TestSettingValueMatchesListAndMasksSecrets(t *testing.T) {
	s := NewForTest(map[string]string{
		"IMAGE_PROVIDER_API_KEY": leakedKey,
		"FREE_UNITS":             "9",
	})
	byk := map[string]any{}
	for _, st := range s.List() {
		byk[st.Key] = st.Value
	}
	for _, k := range []string{"free_units", "allow_guest", "image_quality_standard", "support_email"} {
		if got, want := s.SettingValue(k), byk[k]; got != want {
			t.Errorf("SettingValue(%s)=%v 与 List 里的 %v 不一致 —— 审计会记下一个和面板不同的值", k, got, want)
		}
	}
	v, _ := s.SettingValue("image_provider_api_key").(string)
	if v == "" || strings.Contains(v, leakedKey) {
		t.Fatalf("🔴 审计取值也必须掩码，实际 %q —— 否则每改一次密钥就往 events 表里写一行明文", v)
	}
	if s.SettingValue("no_such_key") != nil {
		t.Fatal("未知键应回 nil")
	}
}
