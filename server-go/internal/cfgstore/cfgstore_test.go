package cfgstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeBackend 模拟一张 app_config 表，且**故意**塞进历史遗留的明文密钥行。
type fakeBackend struct {
	rows map[string]string
}

func (f *fakeBackend) LoadAppConfig(context.Context) (map[string]string, error) { return f.rows, nil }
func (f *fakeBackend) UpsertAppConfig(_ context.Context, k, v string) error {
	f.rows[k] = v
	return nil
}
func (f *fakeBackend) DeleteAppConfig(_ context.Context, k string) error {
	delete(f.rows, k)
	return nil
}

const leakedKey = "sk-THIS-MUST-NEVER-BE-USED-OR-RETURNED"

// 安全问题 1：库里存着密钥时，它既不得生效，也不得出现在任何出参里。
func TestSecretNeverReadFromDB(t *testing.T) {
	be := &fakeBackend{rows: map[string]string{
		"image_provider_api_key": leakedKey,
		"free_units":             "7",
	}}
	s, err := New(context.Background(), be)
	if err != nil {
		t.Fatal(err)
	}
	s.lookup = func(k string) (string, bool) { return "", false }

	if got := s.String("image_provider_api_key"); got != "" {
		t.Fatalf("密钥不得从数据库读出，实际得到长度 %d 的值", len(got))
	}
	if src := s.Source("image_provider_api_key"); src == "db" {
		t.Fatalf("密钥项的 source 不得为 db，实际 %q", src)
	}
	// 非密钥项照常生效，证明跳过逻辑只针对密钥。
	if s.Int("free_units") != 7 {
		t.Fatalf("非密钥项的 DB 覆盖应当生效，实际 %d", s.Int("free_units"))
	}
	if len(s.SkippedSecretRows) != 1 || s.SkippedSecretRows[0] != "image_provider_api_key" {
		t.Fatalf("应记录被跳过的密钥键名，实际 %v", s.SkippedSecretRows)
	}
	// 出参里一个字节都不能出现。
	for _, it := range s.List() {
		if v, ok := it.Value.(string); ok && strings.Contains(v, leakedKey) {
			t.Fatalf("List() 泄漏了密钥值：%s", it.Key)
		}
	}
}

// 安全问题 1：后台热改密钥这条写入口必须被堵死。
func TestSetSecretRejected(t *testing.T) {
	be := &fakeBackend{rows: map[string]string{}}
	s, _ := New(context.Background(), be)
	for _, k := range SecretKeys() {
		if err := s.Set(context.Background(), k, "whatever"); !errors.Is(err, ErrSecretNotWritable) {
			t.Fatalf("Set(%s) 应返回 ErrSecretNotWritable，实际 %v", k, err)
		}
		if _, ok := be.rows[k]; ok {
			t.Fatalf("Set(%s) 不得写入数据库", k)
		}
	}
}

// 环境变量是密钥的唯一来源。
func TestSecretFromEnvOnly(t *testing.T) {
	s := NewForTest(map[string]string{"IMAGE_PROVIDER_API_KEY": "env-key-1234"})
	if s.String("image_provider_api_key") != "env-key-1234" {
		t.Fatal("密钥应当从环境变量读出")
	}
	if s.Source("image_provider_api_key") != "env" {
		t.Fatal("source 应为 env")
	}
	var masked string
	for _, it := range s.List() {
		if it.Key == "image_provider_api_key" {
			masked, _ = it.Value.(string)
		}
	}
	// 🔴 掩码里一个原文字符都不能有：末 4 位（旧行为 "••••1234"）足够让人在泄漏库里
	// 做匹配确认，而后台页面是能被截图的。掩码只回答「配没配」。
	if masked != MaskedSecret {
		t.Fatalf("掩码应为固定 8 个圆点，实际 %q", masked)
	}
	if strings.Contains(masked, "1234") || strings.Contains(masked, "env-key") {
		t.Fatalf("掩码泄漏了原文片段：%q", masked)
	}
	// 长度也不能泄漏：长短不同的密钥掩码必须一致。
	short := NewForTest(map[string]string{"IMAGE_PROVIDER_API_KEY": "ab"})
	if got := MaskSecret(short.String("image_provider_api_key")); got != MaskedSecret {
		t.Fatalf("短密钥掩码应与长密钥一致，实际 %q", got)
	}
	if MaskSecret("") != nil {
		t.Fatal("空值应为 null（后台显示未配置）")
	}
}

// 🔴 这个数字是**契约断言**，不是计数练习：Node 版那 26 个键必须一个不少地
// 存在（少一个就是静默丢配置），而新增键必须是有意为之、连同这里的数字一起改。
//
// 2026-09-12（第一批）：26 → 27，新增 email_code_ttl_seconds（把验证码有效期从
// 四处写死的字面量收敛成一个注册表项，见 Registry 里那条注释）。
// 2026-09-12（第二批）：27 → 30，新增 email_code_max_attempts、
// email_code_max_issues_per_window（原先是 public_email.go 里两个裸 5）、
// max_user_storage_bytes（原先只活在 env、改一次要重启容器）。
// 2026-09-12（第三批，产品特有可运营项）：30 → 41，新增 11 项 ——
//
//	image_size_square / image_size_landscape / image_size_portrait
//	  （原先是 PickSize() 里三个写死的字符串，直接决定分辨率与上游单价）
//	max_job_attempts / provider_breaker_streak / provider_breaker_cooldown_seconds
//	  （前者原先是 env，后两者原先是 health.go 里两个常量）
//	pack_credit_expiry_days / free_credit_expiry_days
//	  （前者原先是 public_purchases.go 里写死的 90 天，且与 App 文案矛盾）
//	event_retention_days / idempotency_retention_days / session_ttl_days
//	  （原先是 env + 部署级只读行）
//
// 密钥项仍必须恰为 2 个 —— 这条一旦变大就说明有人往注册表里加了新密钥，
// 而注册表是后台可写面，新密钥必须先确认 Secret:true。
func TestRegistryHasExpectedKeys(t *testing.T) {
	const wantKeys = 47
	if len(Registry) != wantKeys {
		t.Fatalf("注册表应有 %d 个键，实际 %d（Node 版那 26 个必须一个不少）", wantKeys, len(Registry))
	}
	for _, k := range []string{"free_units", "support_email", "support_qq_group",
		"smtp_host", "smtp_from", "email_login_enabled", "email_code_ttl_seconds",
		"email_code_max_attempts", "email_code_max_issues_per_window", "max_user_storage_bytes",
		"image_size_square", "image_size_landscape", "image_size_portrait",
		"max_job_attempts", "provider_breaker_streak", "provider_breaker_cooldown_seconds",
		"pack_credit_expiry_days", "free_credit_expiry_days",
		"event_retention_days", "idempotency_retention_days", "session_ttl_days",
		// AIGC 标识（《人工智能生成合成内容标识办法》）。少一个键就意味着
		// 那一维度回到了「只能改代码 + 发版」，而这一组全是合规旋钮。
		"aigc_label_enabled", "aigc_label_text", "aigc_label_position",
		"aigc_label_opacity", "aigc_label_size_pct", "aigc_content_producer"} {
		if _, ok := byKey[k]; !ok {
			t.Errorf("注册表缺键 %s", k)
		}
	}
	if got := SecretKeys(); len(got) != 2 {
		t.Fatalf("密钥项应恰为 2 个，实际 %v", got)
	}
	// 🔴 每个登记了区间的键都必须真的在注册表里存在，且必须是数值型。
	//    numRanges 里留一个拼错的键名 = 那一项实际上毫无校验，而看代码像有。
	for k := range numRanges {
		it, ok := byKey[k]
		if !ok {
			t.Errorf("🔴 numRanges 登记了不存在的键 %s —— 那一项实际上没有任何区间校验", k)
			continue
		}
		if it.Type != KindNumber {
			t.Errorf("🔴 numRanges 登记了非数值项 %s（类型 %s）", k, it.Type)
		}
	}
}

// TestSetRejectsOutOfRange 🔴 越界的写必须被**拒绝**，不能静默夹。
//
//	夹一下再存是更糟的选择：运营输 0、库里变成 1，页面刷新后显示 1，
//	没人知道刚才那次保存其实没按要求生效。错误消息里必须带上合法区间，
//	否则运营只能靠二分猜。
func TestSetRejectsOutOfRange(t *testing.T) {
	s := NewForTest(nil)
	ctx := context.Background()
	for _, c := range []struct {
		key string
		bad any
	}{
		{"email_code_ttl_seconds", 0},
		{"email_code_ttl_seconds", 86400},
		{"email_code_max_attempts", 0},
		{"email_code_max_attempts", 999},
		{"email_code_max_issues_per_window", 0},
		{"worker_concurrency", 0},
		{"worker_concurrency", 99},
		{"max_user_storage_bytes", 1},
		{"smtp_port", 70000},
	} {
		err := s.Set(ctx, c.key, c.bad)
		if err == nil {
			t.Errorf("🔴 %s=%v 越界却被接受了", c.key, c.bad)
			continue
		}
		if !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s 的错误消息里应点名该键，实际 %q", c.key, err.Error())
		}
	}
	// 区间内的值必须照常可写。
	for _, c := range []struct {
		key string
		ok  any
	}{
		{"email_code_ttl_seconds", 900},
		{"email_code_max_attempts", 3},
		{"email_code_max_issues_per_window", 1},
		{"worker_concurrency", 8},
		{"max_user_storage_bytes", 64 * 1024 * 1024},
	} {
		if err := s.Set(ctx, c.key, c.ok); err != nil {
			t.Errorf("%s=%v 在区间内却被拒：%v", c.key, c.ok, err)
		}
	}
}

// TestListFlagsClampedValueAsWarning 🔴 「后台显示的值」与「真正生效的值」
// 不一致时，必须在那一行上说出来。
//
//	写校验只管住新的写入，管不住**已经躺在库里**的越界行（Node 版写下的）
//	和 project.env 里手写的脏值。读的时候夹一次是对的，但如果不标注，
//	面板会展示一个看似正常的数，而实际生效的是另一个 —— 这正是本轮要消灭的
//	那类静默偏差。
func TestListFlagsClampedValueAsWarning(t *testing.T) {
	s := NewForTest(map[string]string{"EMAIL_CODE_MAX_ATTEMPTS": "999"})
	var got Setting
	for _, it := range s.List() {
		if it.Key == "email_code_max_attempts" {
			got = it
		}
	}
	if v, _ := got.Value.(float64); v != 10 {
		t.Fatalf("越界的 env 值应被夹到上界 10，实际 %v", got.Value)
	}
	if got.Warning == "" {
		t.Fatal("🔴 被夹过的项必须带 warning，否则后台显示的和实际生效的不是一回事")
	}
	if !strings.Contains(got.Warning, "EMAIL_CODE_MAX_ATTEMPTS") {
		t.Errorf("warning 应点名是哪个来源写错了，实际 %q", got.Warning)
	}
	if got.Min == nil || got.Max == nil || *got.Min != 1 || *got.Max != 10 {
		t.Errorf("数值项应带上区间供前端展示，实际 min=%v max=%v", got.Min, got.Max)
	}
	// 非数字同样要标注（env 里写了 "8GiB" 这种）。
	s2 := NewForTest(map[string]string{"MAX_USER_STORAGE_BYTES": "8GiB"})
	for _, it := range s2.List() {
		if it.Key == "max_user_storage_bytes" {
			if it.Warning == "" {
				t.Error("🔴 env 里不是数字时必须标注已回落默认值")
			}
			if v, _ := it.Value.(float64); v != 256*1024*1024 {
				t.Errorf("应回落默认值，实际 %v", it.Value)
			}
		}
	}
}

// TestEmailCodeTTLClampsToSafeRange 🔴 有效期必须被夹在区间内。
//
//	配成 0（手滑清空）= 每个码一签发就过期，邮箱登录整条路死掉，而后台
//	显示「已保存」、所有接口回归全绿；配成一天 = 一个泄漏的验证码一整天
//	都能登进账号，而它同时是「每窗口最多签 5 次」的窗口长度，等于一天
//	只能要 5 次码。上下界是安全边界，所以写死在代码里。
func TestEmailCodeTTLClampsToSafeRange(t *testing.T) {
	cases := []struct {
		env  string
		want int // 秒
	}{
		{"", 600},       // 默认 10 分钟
		{"600", 600},    // 正常值原样生效
		{"90", 90},      // 区间内的短值可用
		{"0", 60},       // 🔴 夹到下界，不是「立刻过期」
		{"-5", 60},      // 🔴 负数同理
		{"abc", 600},    // 非法值回落默认（Number 的既有行为）
		{"86400", 3600}, // 🔴 夹到上界
	}
	for _, c := range cases {
		env := map[string]string{}
		if c.env != "" {
			env["EMAIL_CODE_TTL_SECONDS"] = c.env
		}
		s := NewForTest(env)
		if got := int(s.EmailCodeTTL().Seconds()); got != c.want {
			t.Errorf("EMAIL_CODE_TTL_SECONDS=%q 应得 %d 秒，实得 %d", c.env, c.want, got)
		}
	}
}

// TestEmailCodeTTLIsHotEditable 它必须是**非密钥**项，否则后台改不了
// （本次改动的全部意义就是让运营能改这个值，而不用去碰 project.env）。
func TestEmailCodeTTLIsHotEditable(t *testing.T) {
	if IsSecret("email_code_ttl_seconds") {
		t.Fatal("验证码有效期不是密钥，不该被标成 Secret（那样后台会拒写）")
	}
	be := &fakeBackend{rows: map[string]string{"email_code_ttl_seconds": "120"}}
	s, err := New(context.Background(), be)
	if err != nil {
		t.Fatal(err)
	}
	s.lookup = func(string) (string, bool) { return "", false }
	if got := int(s.EmailCodeTTL().Seconds()); got != 120 {
		t.Fatalf("DB 覆盖应生效（热改），实得 %d 秒", got)
	}
	if err := s.Set(context.Background(), "email_code_ttl_seconds", float64(300)); err != nil {
		t.Fatalf("后台写入应成功: %v", err)
	}
	if got := int(s.EmailCodeTTL().Seconds()); got != 300 {
		t.Fatalf("写入后应立刻生效，实得 %d 秒", got)
	}
}

// 负向用例：确认「密钥不从 DB 读」这条断言真的会在实现回退时失败。
func TestNegativeControl_NonSecretStillReadsDB(t *testing.T) {
	be := &fakeBackend{rows: map[string]string{"image_provider_base_url": "https://example.invalid"}}
	s, _ := New(context.Background(), be)
	s.lookup = func(string) (string, bool) { return "", false }
	if s.String("image_provider_base_url") != "https://example.invalid" {
		t.Fatal("非密钥项必须能从 DB 读到 —— 否则上一条测试是假绿（跳过了所有键）")
	}
}
