package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"museframe-api/internal/provider"
	"museframe-api/internal/store"
)

// 🔴 判据三态，端到端走一遍 /v1/health。
//
// 修之前：generation.available 只问「配了密钥 + 配了地址」，于是上游从
// 2026-09-05 起对 gpt-image-* 一律回 503「No available compatible accounts」、
// 生成 100% 失败的那一周里，/v1/health 与后台总览一直报绿。
func TestHealthGenerationThreeStates(t *testing.T) {
	e := newTestEnv(t)

	// 态 1（刚起、还没发生过生成）：出参必须与 11 号契约里那份字节基准**逐字节相同** ——
	// lastError / lastSuccessAt 是可选键，没有证据时整键消失。
	gen := healthGen(t, e)
	if string(gen) != `{"available":true,"mode":"remote","reason":null}` {
		t.Fatalf("零样本时 generation 块必须保持契约基准形状，实际 %s", gen)
	}

	// 态 2：最近 3 次上游调用一次没成 → 不可用，且 health 自己把原因说清楚。
	for i := 0; i < 3; i++ {
		e.prov.HealthLedger().Record(provider.Outcome{
			At: e.now.Add(time.Duration(i-2) * time.Minute), OK: false,
			Code: provider.CodeProviderUnavailable, Status: 503,
			Msg: "No available compatible accounts",
		})
	}
	var g struct {
		Available      bool    `json:"available"`
		Mode           string  `json:"mode"`
		Reason         *string `json:"reason"`
		LastError      *string `json:"lastError"`
		LastErrorAt    *string `json:"lastErrorAt"`
		LastSuccessAt  *string `json:"lastSuccessAt"`
		RecentCalls    int     `json:"recentCalls"`
		RecentFailures int     `json:"recentFailures"`
	}
	if err := json.Unmarshal(healthGen(t, e), &g); err != nil {
		t.Fatal(err)
	}
	if g.Available {
		t.Fatal("最近 3 次上游调用全失败，/v1/health 必须如实报 available:false")
	}
	if g.Reason == nil || *g.Reason != provider.CodeProviderUnavailable {
		t.Fatalf("reason 应为 PROVIDER_UNAVAILABLE，实际 %#v", g.Reason)
	}
	if g.LastError == nil || *g.LastError != "PROVIDER_UNAVAILABLE: No available compatible accounts" {
		t.Fatalf("lastError 必须带上游原话，实际 %#v", g.LastError)
	}
	if g.LastErrorAt == nil || g.RecentCalls != 3 || g.RecentFailures != 3 {
		t.Fatalf("应给出失败时间与样本计数，实际 %#v", g)
	}
	if g.LastSuccessAt != nil {
		t.Fatalf("从没成功过，lastSuccessAt 必须缺省，实际 %#v", g.LastSuccessAt)
	}
	// 生成不可用**仍然不得**让 /v1/health 变 503：那会让 Docker 去重启一个好进程。
	if r := e.do("GET", "/v1/health", nil, nil); r.Code != 200 {
		t.Fatalf("生成不可用不该让 /v1/health 变 503，实际 %d", r.Code)
	}

	// 后台总览必须同口径（横幅不能在 100% 失败的一周里还是绿的）。
	r := e.do("GET", "/v1/admin/overview", nil, e.admin())
	var ov struct {
		Generation struct {
			Available      bool    `json:"available"`
			Reason         *string `json:"reason"`
			LastError      *string `json:"lastError"`
			RecentFailures int     `json:"recentFailures"`
		} `json:"generation"`
	}
	if err := json.Unmarshal([]byte(r.Body), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.Generation.Available || ov.Generation.Reason == nil ||
		*ov.Generation.Reason != provider.CodeProviderUnavailable ||
		ov.Generation.LastError == nil || ov.Generation.RecentFailures != 3 {
		t.Fatalf("后台总览必须与 /v1/health 同判据，实际 %#v", ov.Generation)
	}

	// 态 3：来了一次成功 → 恢复可用，并把最近一次成功时间摆出来。
	e.prov.HealthLedger().Record(provider.Outcome{At: e.now, OK: true, Status: 200})
	if err := json.Unmarshal(healthGen(t, e), &g); err != nil {
		t.Fatal(err)
	}
	if !g.Available || g.Reason != nil || g.LastSuccessAt == nil {
		t.Fatalf("有成功样本应恢复可用并给出 lastSuccessAt，实际 %#v", g)
	}
}

// healthGen 取 /v1/health 的 generation 块原始字节（键序与可选键都要能断言）。
func healthGen(t *testing.T, e *testEnv) json.RawMessage {
	t.Helper()
	r := e.do("GET", "/v1/health", nil, nil)
	if r.Code != 200 {
		t.Fatalf("/v1/health 应 200，实际 %d %s", r.Code, r.Body)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatal(err)
	}
	return m["generation"]
}

// 🔴 健康判据的证据必须活过重启。
// 只看 in-process 环形缓冲的话，一个连着失败一周的部署在容器重启后会立刻报绿 ——
// 这正是本轮故障被瞒了一周的另一半原因。开机时用 generation_jobs 的收口行播种。
func TestProviderOutcomeSeedFromDB(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, aid := e.prepareJobInputs("seed@example.com", 5)

	// 三条上游类失败 + 一条「不是上游问题」的失败 + 一条更早的成功。
	mk := func(code string, status string, at time.Time) {
		id := e.nextID()
		if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
			ID: id, UserID: uid, ProjectID: pid, SourceAssetID: aid,
			StyleVersionID: "ver-style-free", Status: "created", Stage: "preparing",
			Controls: []byte(`{}`), Output: []byte(`{}`), ReservedUnits: 1,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			e.t.Fatal(err)
		}
		if status == "succeeded" {
			if err := store.FinishJobSucceeded(ctx, e.st.Q(), id, 0, at); err != nil {
				e.t.Fatal(err)
			}
			return
		}
		if err := store.FinishJobFailed(ctx, e.st.Q(), id, code, at); err != nil {
			e.t.Fatal(err)
		}
	}
	mk("", "succeeded", e.now.Add(-6*time.Hour))
	mk("ASSET_NOT_READY", "failed", e.now.Add(-5*time.Hour))
	mk("PROVIDER_TIMEOUT", "failed", e.now.Add(-3*time.Hour))
	mk("PROVIDER_UNAVAILABLE", "failed", e.now.Add(-2*time.Hour))
	mk("PROVIDER_UNAVAILABLE", "failed", e.now.Add(-1*time.Hour))

	rows, err := store.ListRecentProviderOutcomes(ctx, e.st.Q(), provider.HealthRing, e.now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("应只取「成功 + 上游类失败」4 条（ASSET_NOT_READY 不是上游的证据），实际 %d: %#v", len(rows), rows)
	}
	// 必须按时间**升序**返回：环形缓冲要求最新的在最后。
	for i := 1; i < len(rows); i++ {
		if rows[i].At.Before(rows[i-1].At) {
			t.Fatalf("必须按时间升序返回，实际 %#v", rows)
		}
	}
	if !rows[0].OK || rows[0].Code != "" {
		t.Fatalf("第一条应是那次成功，实际 %#v", rows[0])
	}
	if rows[3].OK || rows[3].Code != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("最后一条应是最近那次供给类失败，实际 %#v", rows[3])
	}

	// 播种进一个空账本 → 判据立刻变「不可用」，不必等第一个用户去踩雷。
	fresh := provider.New(e.rt, "remote", "sk-test-seed")
	fresh.SetProbeEnabled(false)
	fresh.SetNow(e.clock)
	out := make([]provider.Outcome, 0, len(rows))
	for _, r := range rows {
		out = append(out, provider.Outcome{At: r.At, OK: r.OK, Code: r.Code, Msg: r.Code})
	}
	if n := fresh.HealthLedger().Seed(out); n != 4 {
		t.Fatalf("播种应写入 4 条，实际 %d", n)
	}
	h := fresh.HealthStatus(ctx)
	// 4 条里 1 条成功 3 条失败（失败率 75% < 80%）→ 仍算可用；
	// 这正是「不要因为一次抖动就变红」的那条线。
	if !h.Available || h.RecentCalls != 4 || h.RecentFailures != 3 {
		t.Fatalf("播种后样本计数应落到 health 上，实际 %#v", h)
	}
	if h.LastSuccessAt == nil || h.LastError == nil {
		t.Fatalf("播种后应同时给出最近成功与最近失败，实际 %#v", h)
	}
}
