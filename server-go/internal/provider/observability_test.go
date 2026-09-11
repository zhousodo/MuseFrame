package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/imaging"
	"museframe-api/internal/logx"
)

// 本轮故障的上游原话，逐字节照抄（HTTP 503）。
const supply503Body = `{"error":{"message":"No available compatible accounts","type":"api_error"}}`

// 一个真实的密钥形态：它绝不允许出现在任何一行日志里。
const fakeKey = "sk-test-0123456789abcdefghijklmnopqrstuvwxyz"

// newProbe 起一个假上游 + 一个写进 buffer 的 logger，返回 (adapter, buffer)。
func newProbe(t *testing.T, h http.HandlerFunc) (*Adapter, *bytes.Buffer, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	restoreHTTPClient(t)
	a := New(cfgstore.NewForTest(map[string]string{
		"IMAGE_PROVIDER_BASE_URL":   srv.URL,
		"IMAGE_PROVIDER_MODEL":      "gpt-image-2",
		"IMAGE_PROVIDER_TIMEOUT_MS": "3000",
	}), "remote", fakeKey)
	buf := &bytes.Buffer{}
	ticks := 0
	a.SetNow(func() time.Time {
		// 每次取时间推进 40ms：耗时字段因此是确定的正数，不依赖真实时钟。
		ticks++
		return time.Date(2026, 9, 12, 3, 4, 5, 0, time.UTC).Add(time.Duration(ticks) * 40 * time.Millisecond)
	})
	a.SetLogger(logx.NewWith(buf, func() time.Time { return time.Date(2026, 9, 12, 3, 4, 5, 0, time.UTC) }))
	a.SetProbeEnabled(false)
	return a, buf, srv
}

func lastLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("一行日志都没有 —— 上游失败必须留痕（本轮故障就是 docker logs 零条记录）")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("日志不是合法 JSON 行: %v / %q", err, lines[len(lines)-1])
	}
	return m
}

func oneJPEG(t *testing.T) []byte {
	t.Helper()
	img := &imaging.RGBA{Width: 64, Height: 64, Data: make([]byte, 64*64*4)}
	for i := range img.Data {
		img.Data[i] = 128
	}
	b, err := imaging.EncodeJPEG(img, 80)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func editReq() EditRequest {
	return EditRequest{
		SourceJPEG: []byte{0xff, 0xd8, 0xff}, SourceW: 1000, SourceH: 1000,
		AspectRatio: "1:1", QualityTier: "standard",
		Instruction: "make it look like a woodcut", JobID: "job-abc-123",
	}
}

// 🔴 上游失败必须记一行日志，且字段齐全。
// 修之前：runJob 里 CreateEdit 出错直接 failJob，docker logs 零条记录 ——
// 一周 100% 失败而没有任何一行可查的痕迹。
func TestUpstreamFailureIsLoggedWithAllFields(t *testing.T) {
	a, buf, _ := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(supply503Body))
	})

	_, err := a.CreateEdit(context.Background(), editReq())
	if err == nil {
		t.Fatal("503 必须报错")
	}

	m := lastLogLine(t, buf)
	if m["level"] != "warn" {
		t.Fatalf("上游失败应为 warn，实际 %v", m["level"])
	}
	if m["jobId"] != "job-abc-123" {
		t.Fatalf("必须带 job id，实际 %#v", m["jobId"])
	}
	if m["model"] != "gpt-image-2" {
		t.Fatalf("必须带模型，实际 %#v", m["model"])
	}
	if m["size"] != "1024x1024" {
		t.Fatalf("必须带尺寸，实际 %#v", m["size"])
	}
	if s, ok := m["status"].(float64); !ok || int(s) != 503 {
		t.Fatalf("必须带上游状态码 503，实际 %#v", m["status"])
	}
	if m["code"] != CodeProviderUnavailable {
		t.Fatalf("必须带稳定码，实际 %#v", m["code"])
	}
	if m["retryable"] != false {
		t.Fatalf("供给类错误必须标成不可重试，实际 %#v", m["retryable"])
	}
	if ms, ok := m["ms"].(float64); !ok || ms <= 0 {
		t.Fatalf("必须带耗时且为正数，实际 %#v", m["ms"])
	}
	if msg, _ := m["upstreamMessage"].(string); msg != "No available compatible accounts" {
		t.Fatalf("必须带上游原话，实际 %#v", m["upstreamMessage"])
	}
	// 🔴 密钥红线：整行日志里不得出现密钥的任何片段。
	if strings.Contains(buf.String(), fakeKey) || strings.Contains(buf.String(), "sk-test") {
		t.Fatal("日志里出现了密钥")
	}
}

// 上游原话按**字符**截断到 300（不是字节）：含中文的错误文本按字节切会切出半个字。
func TestUpstreamMessageTruncatedToRunes(t *testing.T) {
	long := strings.Repeat("测", 500)
	a, buf, _ := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": long}})
	})
	_, err := a.CreateEdit(context.Background(), editReq())
	if err == nil {
		t.Fatal("502 必须报错")
	}
	msg, _ := lastLogLine(t, buf)["upstreamMessage"].(string)
	if n := len([]rune(strings.TrimSuffix(msg, "…"))); n != MaxUpstreamMsg {
		t.Fatalf("应截断到 %d 个字符，实际 %d", MaxUpstreamMsg, n)
	}
	if !strings.HasSuffix(msg, "…") {
		t.Fatal("截断后应带省略号，好让读日志的人知道还有后文")
	}
	var e *Err
	if !asErr(err, &e) || len([]rune(strings.TrimSuffix(e.Msg, "…"))) != MaxUpstreamMsg {
		t.Fatalf("错误对象里的 Msg 也必须截断，实际 %d 字符", len([]rune(e.Msg)))
	}
}

// 成功也要记一行：没有成功基线，就没法判断「是这次坏了还是一直坏着」。
func TestUpstreamSuccessIsLogged(t *testing.T) {
	jpg := oneJPEG(t)
	a, buf, _ := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(jpg)}},
			"usage": map[string]any{"total_tokens": 1234},
		})
	})
	res, err := a.CreateEdit(context.Background(), editReq())
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if res.TotalTokens() != 1234 {
		t.Fatalf("usage.total_tokens 应为 1234，实际 %d", res.TotalTokens())
	}
	m := lastLogLine(t, buf)
	if m["level"] != "info" || m["jobId"] != "job-abc-123" {
		t.Fatalf("成功日志应为 info 且带 job id，实际 %#v", m)
	}
	if m["model"] != "gpt-image-2" || m["size"] != "1024x1024" {
		t.Fatalf("成功日志必须带模型与尺寸，实际 %#v", m)
	}
	if ms, ok := m["ms"].(float64); !ok || ms <= 0 {
		t.Fatalf("成功日志必须带耗时，实际 %#v", m["ms"])
	}
	if w, ok := m["width"].(float64); !ok || int(w) != 64 {
		t.Fatalf("成功日志应带产出宽度，实际 %#v", m["width"])
	}
}

// 🔴 错误映射表。修之前：`status>=500 → PROVIDER_TIMEOUT` + retryRe 含 "account"，
// 于是一周的 503「No available compatible accounts」全部以 PROVIDER_TIMEOUT 入库，
// 排障一直在查网络与超时。
func TestClassifyUpstreamErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		msg    string
		code   string
		retry  bool
	}{
		{"供给耗尽（本轮故障原话）", 503, "No available compatible accounts", CodeProviderUnavailable, false},
		{"503 但没有原话", 503, "HTTP 503", CodeProviderUnavailable, false},
		{"上游限流", 429, "Rate limit reached for this key", CodeProviderUnavailable, false},
		{"没有可用渠道", 500, "no channel available for model gpt-image-2", CodeProviderUnavailable, false},
		{"配额用尽", 500, "insufficient quota", CodeProviderUnavailable, false},
		{"网关超时才算超时", 504, "gateway timeout", CodeProviderTimeout, true},
		{"上游自述超时", 500, "upstream request timed out", CodeProviderTimeout, true},
		{"408 也是超时", 408, "", CodeProviderTimeout, true},
		{"普通 5xx 可重试", 502, "bad gateway", CodeProviderError, true},
		{"内容策略拒绝不可重试", 400, "Your request was rejected by our safety system", CodeGenerationReject, false},
		{"参数错不可重试", 400, "Invalid value for size", CodeProviderError, false},
	}
	for _, c := range cases {
		code, retry := classify(c.status, c.msg)
		if code != c.code || retry != c.retry {
			t.Errorf("%s: classify(%d,%q) = (%s,%v)，期望 (%s,%v)",
				c.name, c.status, c.msg, code, retry, c.code, c.retry)
		}
	}
}

// 映射必须在真实调用路径上成立（不只是在 classify 这个纯函数里）。
func TestCreateEditMapsSupply503AndTimeout504(t *testing.T) {
	a, _, _ := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(supply503Body))
	})
	_, err := a.CreateEdit(context.Background(), editReq())
	if CodeOf(err) != CodeProviderUnavailable {
		t.Fatalf("503 供给类必须是 PROVIDER_UNAVAILABLE，实际 %s", CodeOf(err))
	}
	if Retryable(err) {
		t.Fatal("供给类错误必须不可重试 —— 再打一遍只会拿到同一条 503")
	}
	if StatusOf(err) != 503 {
		t.Fatalf("错误里应带上游状态码，实际 %d", StatusOf(err))
	}
	if UserMessage(CodeOf(err)) != "生成服务暂时不可用，额度已退回" {
		t.Fatalf("用户文案不对: %q", UserMessage(CodeOf(err)))
	}

	b, _, _ := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":{"message":"gateway timeout"}}`))
	})
	_, err = b.CreateEdit(context.Background(), editReq())
	if CodeOf(err) != CodeProviderTimeout {
		t.Fatalf("504 必须是 PROVIDER_TIMEOUT，实际 %s", CodeOf(err))
	}
	if !Retryable(err) {
		t.Fatal("真超时是可重试的")
	}
}

// 本地 deadline 到点才算 PROVIDER_TIMEOUT。
func TestLocalDeadlineIsTimeout(t *testing.T) {
	a, _, _ := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	a.cfg = cfgstore.NewForTest(map[string]string{
		"IMAGE_PROVIDER_BASE_URL":   a.BaseURL(),
		"IMAGE_PROVIDER_TIMEOUT_MS": "50",
	})
	_, err := a.CreateEdit(context.Background(), editReq())
	if CodeOf(err) != CodeProviderTimeout {
		t.Fatalf("本地 deadline 到点应为 PROVIDER_TIMEOUT，实际 %s", CodeOf(err))
	}
}

// 🔴 健康判据三态。修之前：只要配了密钥 + 地址就报 available:true，
// 于是上游 100% 失败的一周里 /v1/health 与后台总览一直是绿的。
func TestHealthStatusThreeStates(t *testing.T) {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	rt := cfgstore.NewForTest(map[string]string{"IMAGE_PROVIDER_BASE_URL": "https://p.example"})

	// 态 1：没配置 —— 与旧行为一致（运维在面板里修的配置态）。
	notCfg := New(rt, "remote", "")
	notCfg.SetNow(func() time.Time { return now })
	notCfg.SetProbeEnabled(false)
	if h := notCfg.HealthStatus(context.Background()); h.Available || h.Reason == nil || *h.Reason != "PROVIDER_NOT_CONFIGURED" {
		t.Fatalf("缺密钥必须不可用且点名原因，实际 %#v", h)
	}

	// 态 2：配置齐备 + 最近 3 次上游调用一次没成 → 不可用，reason 是真实的失败码，
	// 并且 lastError 能自证「为什么判成不可用」。
	down := New(rt, "remote", fakeKey)
	down.SetNow(func() time.Time { return now })
	down.SetProbeEnabled(false)
	for i := 0; i < 3; i++ {
		down.HealthLedger().Record(Outcome{
			// 按时间升序写入（环形缓冲要求最新的在最后）。
			At: now.Add(time.Duration(i-2) * time.Minute), OK: false,
			Code: CodeProviderUnavailable, Status: 503, Msg: "No available compatible accounts",
		})
	}
	h := down.HealthStatus(context.Background())
	if h.Available {
		t.Fatal("最近 3 次上游调用全失败，必须报不可用")
	}
	if h.Reason == nil || *h.Reason != CodeProviderUnavailable {
		t.Fatalf("reason 应为 PROVIDER_UNAVAILABLE，实际 %#v", h.Reason)
	}
	if h.LastError == nil || !strings.Contains(*h.LastError, "No available compatible accounts") {
		t.Fatalf("lastError 必须带上游原话，实际 %#v", h.LastError)
	}
	if h.LastErrorAt == nil || h.RecentCalls != 3 || h.RecentFailures != 3 {
		t.Fatalf("应给出样本计数与失败时间，实际 %#v", h)
	}
	if h.LastSuccessAt != nil {
		t.Fatalf("从来没成功过，lastSuccessAt 必须缺省，实际 %#v", h.LastSuccessAt)
	}
	// 连续 3 次供给类失败 → 熔断（冷却期内不再打上游、不再付提示词编译的钱）。
	if !down.SupplyDown(now) {
		t.Fatal("连续 3 次供给类失败应触发熔断")
	}
	// 冷却期一过自动半开，否则上游恢复了也没人去发现。
	if down.SupplyDown(now.Add(SupplyBreakerCooldown + time.Second)) {
		t.Fatal("冷却期过后必须放行一个任务去探路")
	}

	// 态 3：有成功样本 → 可用，且把最近一次成功时间摆出来。
	up := New(rt, "remote", fakeKey)
	up.SetNow(func() time.Time { return now })
	up.SetProbeEnabled(false)
	up.HealthLedger().Record(Outcome{At: now.Add(-3 * time.Minute), OK: false, Code: CodeProviderTimeout, Status: 504})
	up.HealthLedger().Record(Outcome{At: now.Add(-2 * time.Minute), OK: true, Status: 200})
	up.HealthLedger().Record(Outcome{At: now.Add(-1 * time.Minute), OK: true, Status: 200})
	h = up.HealthStatus(context.Background())
	if !h.Available || h.Reason != nil {
		t.Fatalf("有成功样本应报可用且 reason 为 nil，实际 %#v", h)
	}
	if h.LastSuccessAt == nil || *h.LastSuccessAt != "2026-09-12T02:59:00.000Z" {
		t.Fatalf("应给出最近一次成功时间，实际 %#v", h.LastSuccessAt)
	}

	// 单次失败不算证据（样本不足），否则一次网络抖动就让后台变红。
	blip := New(rt, "remote", fakeKey)
	blip.SetNow(func() time.Time { return now })
	blip.SetProbeEnabled(false)
	blip.HealthLedger().Record(Outcome{At: now, OK: false, Code: CodeProviderError, Status: 502})
	if h := blip.HealthStatus(context.Background()); !h.Available {
		t.Fatal("只失败 1 次不该判成不可用（样本不足）")
	}

	// 窗口外的证据不算：上周那次成功不能给今天背书，上周那次失败也不能压今天。
	stale := New(rt, "remote", fakeKey)
	stale.SetNow(func() time.Time { return now })
	stale.SetProbeEnabled(false)
	for i := 0; i < 5; i++ {
		stale.HealthLedger().Record(Outcome{At: now.Add(-48 * time.Hour), OK: false, Code: CodeProviderUnavailable})
	}
	if h := stale.HealthStatus(context.Background()); !h.Available || h.RecentCalls != 0 {
		t.Fatalf("24h 窗口外的证据必须被忽略，实际 %#v", h)
	}
}

// 失败率判据：样本够多、最近一次还是失败、失败率过阈值 → 不可用。
func TestHealthStatusFailureRate(t *testing.T) {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	a := New(cfgstore.NewForTest(map[string]string{"IMAGE_PROVIDER_BASE_URL": "https://p.example"}), "remote", fakeKey)
	a.SetNow(func() time.Time { return now })
	a.SetProbeEnabled(false)
	a.HealthLedger().Record(Outcome{At: now.Add(-10 * time.Minute), OK: true, Status: 200})
	for i := 0; i < 9; i++ {
		a.HealthLedger().Record(Outcome{
			At: now.Add(time.Duration(i-9) * time.Minute), OK: false,
			Code: CodeProviderUnavailable, Status: 503, Msg: "No available compatible accounts",
		})
	}
	if h := a.HealthStatus(context.Background()); h.Available {
		t.Fatal("10 次里 9 次失败、最近一次仍是失败 → 必须报不可用")
	}
}

// 环形缓冲只留最近 HealthRing 条，且播种只在账本为空时生效。
func TestHealthLedgerRingAndSeed(t *testing.T) {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	h := NewHealth()
	for i := 0; i < HealthRing+7; i++ {
		h.Record(Outcome{At: now, OK: i%2 == 0})
	}
	if v := h.View(now); v.Samples != HealthRing {
		t.Fatalf("环形缓冲应只留 %d 条，实际 %d", HealthRing, v.Samples)
	}
	if n := h.Seed([]Outcome{{At: now, OK: true}}); n != 0 {
		t.Fatal("账本非空时播种必须是空操作 —— 真实调用的证据不许被历史行覆盖")
	}
	empty := NewHealth()
	if n := empty.Seed([]Outcome{{At: now, OK: false, Code: CodeProviderTimeout}}); n != 1 {
		t.Fatalf("空账本应接受播种，实际 %d", n)
	}
}

// 轻量探针：200 且列表里有配置的模型 → 通过；没有 → 否决；
// 404（上游没这条路由）→ 不下结论，绝不因此误判成挂了。
//
// 🔴 探针**只能否决、不能背书**：本轮故障里 /v1/models 就是 200 且含 gpt-image-2，
// 账号池耗尽只在 /v1/images/edits 上暴露。所以「探针通过」不得覆盖真实失败证据。
func TestProbeModels(t *testing.T) {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	restoreHTTPClient(t)
	mk := func(h http.HandlerFunc) *Adapter {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		a := New(cfgstore.NewForTest(map[string]string{
			"IMAGE_PROVIDER_BASE_URL": srv.URL, "IMAGE_PROVIDER_MODEL": "gpt-image-2",
		}), "remote", fakeKey)
		a.SetNow(func() time.Time { return now })
		return a
	}

	ok := mk(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "gpt-5.6"}, map[string]any{"id": "gpt-image-2"},
		}})
	})
	if done, good, _ := ok.RunProbe(context.Background()); !done || !good {
		t.Fatalf("模型在列表里应通过，实际 done=%v ok=%v", done, good)
	}
	// 探针通过也不得给「真实调用全失败」背书。
	for i := 0; i < 3; i++ {
		ok.HealthLedger().Record(Outcome{At: now, OK: false, Code: CodeProviderUnavailable, Status: 503,
			Msg: "No available compatible accounts"})
	}
	if h := ok.HealthStatus(context.Background()); h.Available {
		t.Fatal("探针通过不得覆盖真实失败证据 —— 本轮故障里 /v1/models 正是 200")
	}

	missing := mk(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "gpt-5.6"}}})
	})
	if done, good, reason := missing.RunProbe(context.Background()); !done || good || !strings.Contains(reason, "PROBE_MODEL_MISSING") {
		t.Fatalf("配置的模型不在列表里应被否决，实际 done=%v ok=%v reason=%q", done, good, reason)
	}
	if h := missing.HealthStatus(context.Background()); h.Available || h.Reason == nil || *h.Reason != "PROVIDER_PROBE_FAILED" {
		t.Fatalf("探针否决应落到 health 上，实际 %#v", h)
	}

	noRoute := mk(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if done, _, _ := noRoute.RunProbe(context.Background()); done {
		t.Fatal("上游没有 /v1/models 时探针必须不下结论，而不是误判成挂了")
	}
	if h := noRoute.HealthStatus(context.Background()); !h.Available {
		t.Fatal("探针无话可说时不得改变结论")
	}

	auth := mk(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	if done, good, reason := auth.RunProbe(context.Background()); !done || good || !strings.Contains(reason, "PROBE_AUTH") {
		t.Fatalf("401 应被判成密钥失效，实际 done=%v ok=%v reason=%q", done, good, reason)
	}
	// 🔴 探针的 reason 也会进 health 出参：里面绝不允许出现密钥。
	if _, _, reason := auth.RunProbe(context.Background()); strings.Contains(reason, fakeKey) {
		t.Fatal("探针 reason 里出现了密钥")
	}
}

// 探针绝不在请求线程里同步打上游：/v1/health 每秒都在被 healthcheck 调，
// 让它等一个 5 秒外网请求等于把「上游慢」升级成「容器不健康」。
func TestProbeNeverBlocksHealthRequest(t *testing.T) {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	restoreHTTPClient(t)
	a := New(cfgstore.NewForTest(map[string]string{"IMAGE_PROVIDER_BASE_URL": srv.URL}), "remote", fakeKey)
	a.SetNow(func() time.Time { return now })

	done := make(chan HealthStatus, 1)
	go func() { done <- a.HealthStatus(context.Background()) }()
	select {
	case h := <-done:
		if !h.Available {
			t.Fatal("头一次调用还没有探针结论，不得因此报不可用")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HealthStatus 被探针阻塞了")
	}
	// 放后台探针走完再退出：否则它会在 t.Cleanup 还原 HTTPClient 之后才发请求。
	close(release)
	waitProbeIdle(t, a)
}

// waitProbeIdle 等后台探针收工（用例退出前必须回收它，不留悬挂 goroutine）。
func waitProbeIdle(t *testing.T, a *Adapter) {
	t.Helper()
	h := a.HealthLedger()
	for i := 0; i < 200; i++ {
		h.mu.Lock()
		running := h.probeRunning
		h.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("后台探针没有收工")
}

// restoreHTTPClient 把包级 HTTPClient 换成真客户端（compiler_test 会把它替成
// 一个会调用 t.Fatal 的桩，且不还原；这些用例要打的是本地 httptest 服务），
// 并在用例结束时还原，免得测试之间互相污染。
func restoreHTTPClient(t *testing.T) {
	t.Helper()
	prev := HTTPClient
	HTTPClient = &http.Client{}
	t.Cleanup(func() { HTTPClient = prev })
}

func asErr(err error, target **Err) bool {
	e, ok := err.(*Err)
	if ok {
		*target = e
	}
	return ok
}
