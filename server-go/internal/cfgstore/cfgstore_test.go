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
	if masked != "••••1234" {
		t.Fatalf("掩码应为 4 个圆点 + 末 4 位，实际 %q", masked)
	}
}

// 🔴 这个数字是**契约断言**，不是计数练习：Node 版那 26 个键必须一个不少地
// 存在（少一个就是静默丢配置），而新增键必须是有意为之、连同这里的数字一起改。
//
// 2026-09-12：26 → 27，新增 email_code_ttl_seconds（把验证码有效期从四处
// 写死的字面量收敛成一个注册表项，见 Registry 里那条注释）。
// 密钥项仍必须恰为 2 个 —— 这条一旦变大就说明有人往注册表里加了新密钥，
// 而注册表是后台可写面，新密钥必须先确认 Secret:true。
func TestRegistryHasExpectedKeys(t *testing.T) {
	const wantKeys = 27
	if len(Registry) != wantKeys {
		t.Fatalf("注册表应有 %d 个键，实际 %d（Node 版那 26 个必须一个不少）", wantKeys, len(Registry))
	}
	for _, k := range []string{"free_units", "support_email", "support_qq_group",
		"smtp_host", "smtp_from", "email_login_enabled", "email_code_ttl_seconds"} {
		if _, ok := byKey[k]; !ok {
			t.Errorf("注册表缺键 %s", k)
		}
	}
	if got := SecretKeys(); len(got) != 2 {
		t.Fatalf("密钥项应恰为 2 个，实际 %v", got)
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
