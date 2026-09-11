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

func TestRegistryHasTwentySixKeys(t *testing.T) {
	if len(Registry) != 26 {
		t.Fatalf("注册表应有 26 个键（与 Node 版 configStore.js 一致），实际 %d", len(Registry))
	}
	if got := SecretKeys(); len(got) != 2 {
		t.Fatalf("密钥项应恰为 2 个，实际 %v", got)
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
