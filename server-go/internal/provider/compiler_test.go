package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"museframe-api/internal/cfgstore"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 三个编译器档案必须齐备（生产库里有 3 个风格带 compiler 字段）。
func TestCompilerProfilesPresent(t *testing.T) {
	keys := CompilerKeys()
	sort.Strings(keys)
	want := []string{"editorial", "reportage", "zine"}
	if len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("编译器档案应为 %v，实际 %v", want, keys)
	}
	for _, k := range keys {
		if len(compilerProfiles[k]) < 1000 {
			t.Errorf("%s 档案长度异常（%d），可能在移植时被截断", k, len(compilerProfiles[k]))
		}
	}
}

func newAdapterWithRT(handler roundTripFunc) *Adapter {
	// 🔴 注意：这里换掉的是**包级**单例。桩里会调 t.Fatal，所以一旦不还原，
	// 后面任何用例打到这个桩都会在一个已结束的测试上 Fatal → panic。
	// 用例自己负责还原（见 restoreHTTPClient）。
	HTTPClient = &http.Client{Transport: handler}
	rt := cfgstore.NewForTest(map[string]string{
		"IMAGE_PROVIDER_BASE_URL": "https://p.invalid",
		"PROMPT_COMPILER_MODEL":   "gpt-5.4-mini",
	})
	return New(rt, "remote", "sk-test")
}

// 正常路径：请求体形状正确，返回值被采纳。
func TestCompileInstruction(t *testing.T) {
	var captured map[string]any
	a := newAdapterWithRT(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("编译器应打 /v1/chat/completions，实际 %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("密钥必须走 Authorization 头，实际 %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		out := `{"choices":[{"message":{"content":"` + strings.Repeat("A compiled prompt. ", 10) + `"}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(out)), Header: http.Header{}}, nil
	})
	pc := 2
	ex := 0.12
	got := a.CompileInstruction(context.Background(), "zine", "Torn paper.",
		map[string]string{"strength": "bold", "fidelity": "high", "composition": "keep"},
		"person", PhotoFacts{PersonCount: &pc, Orientation: "portrait", Exposure: &ex}, []byte{0xFF, 0xD8, 0x01})
	if !strings.HasPrefix(got, "A compiled prompt.") {
		t.Fatalf("应采纳编译结果，实际 %q", got)
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应是 system + user 两条消息，实际 %d", len(msgs))
	}
	sys, _ := msgs[0].(map[string]any)
	if !strings.Contains(sys["content"].(string), "zine poster") {
		t.Error("system 消息应是 zine 档案")
	}
	usr, _ := msgs[1].(map[string]any)
	parts, ok := usr["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("带图时 user content 应是 [text, image_url] 两段，实际 %#v", usr["content"])
	}
	text := parts[0].(map[string]any)["text"].(string)
	for _, want := range []string{"People in frame: 2.", "Frame orientation: portrait.", "Exposure: dark/low light.", "Style strength requested: bold"} {
		if !strings.Contains(text, want) {
			t.Errorf("照片事实缺少 %q", want)
		}
	}
	if captured["model"] != "gpt-5.4-mini" {
		t.Errorf("应使用 prompt_compiler_model，实际 %v", captured["model"])
	}
}

// 失败一律回落静态拼装（返回空串），绝不让任务失败。
func TestCompileInstructionFallsBack(t *testing.T) {
	cases := []struct {
		name string
		fn   roundTripFunc
	}{
		{"上游 500", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
		}},
		{"产出太短", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"too short"}}]}`)), Header: http.Header{}}, nil
		}},
		{"空 choices", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: http.Header{}}, nil
		}},
		{"网络错误", func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }},
	}
	for _, c := range cases {
		a := newAdapterWithRT(c.fn)
		if got := a.CompileInstruction(context.Background(), "zine", "d", map[string]string{}, "person", PhotoFacts{}, nil); got != "" {
			t.Errorf("%s：应回落静态拼装（空串），实际 %q", c.name, got)
		}
	}
	// 未知 compiler 键 / 未配置上游也回落。
	a := newAdapterWithRT(func(*http.Request) (*http.Response, error) {
		t.Fatal("未知 compiler 键不应发起请求")
		return nil, nil
	})
	if got := a.CompileInstruction(context.Background(), "nope", "d", map[string]string{}, "person", PhotoFacts{}, nil); got != "" {
		t.Errorf("未知 compiler 键应回落，实际 %q", got)
	}
}
