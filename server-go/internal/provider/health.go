// 上游生成链路的健康判据。
//
// 🔴 为什么不能只看「配了密钥 + 配了地址」：
//
//	2026-09-05 起上游（gpt.lenscript.cn）对 gpt-image-* 一律回 503
//	{"error":{"message":"No available compatible accounts"}}，生成 100% 失败，
//	而 /v1/health 与后台总览依旧报 generation.available:true —— 因为旧判据只问
//	「配置齐不齐」。配置齐备从来不等于能产出一张图。
//
// 新判据的真相来源按可信度排序：
//
//  1. 最近 N 次**真实**上游调用的结果（in-process 环形缓冲，开机时用
//     generation_jobs 的历史收口行播种，所以重启不会把证据清零）；
//  2. 轻量探针 GET {BASE_URL}/v1/models（带 60s 缓存），**只能否决、不能背书**：
//     实测这次故障里 /v1/models 返回 200 且列表里有 gpt-image-2 ——
//     网关的账号池耗尽只在 /v1/images/edits 上暴露。所以探针通过什么都不证明，
//     探针失败才是硬证据（地址写错、密钥失效、上游整体挂掉）。
//
// 判据**不**接到「能不能接任务」那条闸上（Adapter.Status 仍然只看配置）：
// 供给恢复只能靠真实调用来发现，把接单闸压在健康判据上会形成死锁 ——
// 不可用 → 不接任务 → 没有新证据 → 永远不可用。
// 烧时间的那一半由 SupplyDown 的 60 秒熔断处理，它每分钟放一个任务过去探路。
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// HealthRing 是环形缓冲容量：只看最近 20 次上游调用。
	HealthRing = 20
	// HealthWindow 之外的证据一概不算（比如上周那次成功）。
	HealthWindow = 24 * time.Hour
	// HealthMinSamples 是下「不可用」结论所需的最小样本数。
	// 1 次失败可能只是抖动；连着 3 次一次没成，就不是抖动。
	HealthMinSamples = 3
	// HealthFailRate 是「样本够多时」的失败率阈值。
	HealthFailRate = 0.8
	// ProbeTTL 是探针缓存期。探针只在缺少真实调用证据时才跑，
	// 再加 60s 缓存，绝不会给上游加压。
	ProbeTTL = 60 * time.Second
	// ProbeTimeout 是探针自己的超时：/v1/health 不允许被上游拖住。
	ProbeTimeout = 5 * time.Second
	// SupplyBreakerStreak / SupplyBreakerCooldown：连着这么多次供给类失败之后，
	// 冷却期内的任务直接快速失败、不再打上游，也不再付提示词编译的钱。
	// 冷却期一过就放一个任务过去探路（半开），所以供给恢复会自动被发现。
	//
	// 🔴 2026-09-12 起这两个数字是**注册表热键**（provider_breaker_streak /
	// provider_breaker_cooldown_seconds），这里的常量退化成「cfg 缺席时的默认值」。
	// 理由很具体：上游换一家供应商、或者故障形态从「整体 503」变成「偶发 503」时，
	// 阈值要能当场调 —— 阈值配得过松等于每个任务都去白烧一次上游调用的钱，
	// 配得过紧等于上游只抖一下就把整条生成链路停 60 秒。两种都等不起一次发版。
	SupplyBreakerStreak   = 3
	SupplyBreakerCooldown = 60 * time.Second
	// MaxUpstreamMsg 是上游错误文本进日志 / 进健康出参时的截断长度（按字符，不是字节）。
	MaxUpstreamMsg = 300
)

// Outcome 是一次上游调用的结果（或一条从 generation_jobs 播种来的历史结果）。
type Outcome struct {
	At     time.Time
	OK     bool
	Code   string
	Status int
	Msg    string
}

// Health 是上游调用的健康账本。并发安全。
type Health struct {
	mu   sync.Mutex
	ring []Outcome

	probeAt      time.Time
	probeDone    bool
	probeOK      bool
	probeReason  string
	probeRunning bool
}

// NewHealth 构造空账本。
func NewHealth() *Health { return &Health{} }

// Record 记一次真实上游调用的结果。
func (h *Health) Record(o Outcome) {
	o.Msg = TruncateRunes(o.Msg, MaxUpstreamMsg)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ring = append(h.ring, o)
	if len(h.ring) > HealthRing {
		h.ring = h.ring[len(h.ring)-HealthRing:]
	}
}

// Seed 用历史结果播种（按时间升序传入）。
// 只在账本为空时生效 —— 真实调用的证据永远比历史行新鲜，不许被覆盖。
func (h *Health) Seed(rows []Outcome) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ring) > 0 {
		return 0
	}
	if len(rows) > HealthRing {
		rows = rows[len(rows)-HealthRing:]
	}
	h.ring = append(h.ring, rows...)
	return len(h.ring)
}

// View 是账本在某个时刻的快照。
type View struct {
	Samples       int
	Failures      int
	SupplyStreak  int // 末尾连续的供给类失败次数
	LastAt        time.Time
	LastSuccessAt time.Time
	LastErr       *Outcome
}

// View 汇总 HealthWindow 之内的证据。
func (h *Health) View(now time.Time) View {
	h.mu.Lock()
	defer h.mu.Unlock()
	var v View
	cut := now.Add(-HealthWindow)
	for i := range h.ring {
		o := h.ring[i]
		if o.At.Before(cut) {
			continue
		}
		v.Samples++
		if o.OK {
			v.LastSuccessAt = o.At
		} else {
			v.Failures++
			oc := o
			v.LastErr = &oc
		}
		v.LastAt = o.At
	}
	// 末尾连续供给类失败：从最新往前数，遇到非供给类结果即止。
	for i := len(h.ring) - 1; i >= 0; i-- {
		o := h.ring[i]
		if o.OK || o.Code != CodeProviderUnavailable || o.At.Before(cut) {
			break
		}
		v.SupplyStreak++
	}
	return v
}

// HealthStatus 是 /v1/health 的 generation 块与后台总览共用的「现在到底能不能产图」。
type HealthStatus struct {
	Available bool
	Mode      string
	Reason    *string
	// LastError 是最近一次失败的「CODE: 上游原话（截断 300 字符、已脱敏）」。
	LastError     *string
	LastErrorAt   *string
	LastSuccessAt *string
	// RecentCalls / RecentFailures 是 24 小时窗口内的真实调用样本。
	RecentCalls    int
	RecentFailures int
	Missing        []string
}

// HealthStatus 给出新判据。它**只读**账本与探针缓存，不改任何业务状态。
func (a *Adapter) HealthStatus(_ context.Context) HealthStatus {
	cfgSt := a.Status()
	out := HealthStatus{
		Available: cfgSt.Available, Mode: cfgSt.Mode,
		Reason: cfgSt.Reason, Missing: cfgSt.Missing,
	}
	// 配置不齐 / provider 取值非法 / 显式 local：没有上游可判，原样返回。
	if !cfgSt.Available || cfgSt.Mode != "remote" {
		return out
	}

	now := a.now()
	v := a.HealthLedger().View(now)
	out.RecentCalls, out.RecentFailures = v.Samples, v.Failures
	if !v.LastSuccessAt.IsZero() {
		out.LastSuccessAt = strPtr(iso(v.LastSuccessAt))
	}
	if v.LastErr != nil {
		out.LastError = strPtr(v.LastErr.Code + ": " + v.LastErr.Msg)
		out.LastErrorAt = strPtr(iso(v.LastErr.At))
	}

	// 判据 1：真实调用证据。一次没成 = 不可用；成功率低于阈值且最近一次还是失败 = 不可用。
	lastFailed := v.LastErr != nil && v.LastErr.At.Equal(v.LastAt)
	switch {
	case v.Samples >= HealthMinSamples && v.Failures == v.Samples:
		out.Available = false
		out.Reason = strPtr(reasonOf(v.LastErr))
		return out
	case v.Samples >= 5 && lastFailed && float64(v.Failures)/float64(v.Samples) >= HealthFailRate:
		out.Available = false
		out.Reason = strPtr(reasonOf(v.LastErr))
		return out
	case v.Samples >= HealthMinSamples:
		// 有成功样本：可用。探针此时无权否决（真实成功比探针可信）。
		return out
	}

	// 判据 2：样本不足，退到轻量探针。探针只能否决。
	if done, ok, reason := a.probeModels(now); done && !ok {
		out.Available = false
		out.Reason = strPtr("PROVIDER_PROBE_FAILED")
		if out.LastError == nil {
			out.LastError = strPtr(reason)
		}
	}
	return out
}

// reasonOf 把最近一次失败翻成 health 的 reason 码。
func reasonOf(last *Outcome) string {
	if last == nil || last.Code == "" {
		return "PROVIDER_RECENT_FAILURES"
	}
	return last.Code
}

// SupplyDown 回答「现在该不该直接跳过上游」。
// 连着 SupplyBreakerStreak 次供给类失败、且最近一次就在冷却期内 → 真。
// 冷却期一过自动放行（半开），所以上游恢复不需要任何人工动作。
func (a *Adapter) SupplyDown(now time.Time) bool {
	v := a.HealthLedger().View(now)
	if v.SupplyStreak < a.BreakerStreak() || v.LastErr == nil {
		return false
	}
	return now.Sub(v.LastErr.At) < a.BreakerCooldown()
}

// probeModels 返回**缓存里**的探针结论，并在缓存过期时后台刷新一次。
//
// 🔴 刻意不在请求里同步打上游：/v1/health 是 compose healthcheck 与边缘探活的
// 入口，每秒都在被调。让它同步等一个 5 秒超时的外网请求，等于把「上游慢」
// 升级成「容器不健康」。所以这里永远立刻返回，最坏情况是头一次调用还没有结论。
// 并发只允许一个 in-flight 刷新（probeRunning），探针不会给上游加压。
func (a *Adapter) probeModels(now time.Time) (bool, bool, string) {
	h := a.HealthLedger()
	h.mu.Lock()
	done, ok, reason := h.probeDone, h.probeOK, h.probeReason
	had := !h.probeAt.IsZero()
	fresh := had && now.Sub(h.probeAt) < ProbeTTL
	if !fresh && !h.probeRunning && a.probeEnabled() {
		h.probeRunning = true
		go a.refreshProbe(context.Background())
	}
	h.mu.Unlock()
	if !had {
		// 头一次调用：还没有任何结论，按「探针无话可说」处理。
		return false, false, ""
	}
	return done, ok, reason
}

// RunProbe 同步跑一次探针并写进缓存。生产不走这条路（只有后台刷新会），
// 单测用它把「探针说了什么」变成确定性事实。
func (a *Adapter) RunProbe(ctx context.Context) (bool, bool, string) {
	h := a.HealthLedger()
	done, ok, reason := a.runProbe(ctx)
	h.mu.Lock()
	h.probeAt, h.probeDone, h.probeOK, h.probeReason = a.now(), done, ok, reason
	h.mu.Unlock()
	return done, ok, reason
}

func (a *Adapter) refreshProbe(ctx context.Context) {
	h := a.HealthLedger()
	done, ok, reason := a.runProbe(ctx)
	h.mu.Lock()
	h.probeAt, h.probeDone, h.probeOK, h.probeReason = a.now(), done, ok, reason
	h.probeRunning = false
	h.mu.Unlock()
}

func (a *Adapter) runProbe(ctx context.Context) (done bool, ok bool, reason string) {
	base := a.BaseURL()
	if base == "" || !a.HasAPIKey() {
		return false, false, ""
	}
	reqCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return false, false, ""
	}
	// 密钥只出现在这一行，绝不进日志。
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return true, false, "PROBE_UNREACHABLE: 探针请求失败（超时 / DNS / 连接被拒）"
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return true, false, "PROBE_AUTH: 上游拒绝了我们的密钥（HTTP " + strconv.Itoa(resp.StatusCode) + "）"
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		// 上游没有 /v1/models 这条路由 —— 探针无权下结论。
		return false, false, ""
	case resp.StatusCode >= 500:
		return true, false, "PROBE_UNREACHABLE: 上游 HTTP " + strconv.Itoa(resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, false, ""
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Data) == 0 {
		// 形状不认识：不下结论，而不是误判成挂了。
		return false, false, ""
	}
	want := map[string]bool{a.ModelFor("standard"): true, a.ModelFor("high"): true}
	for _, m := range body.Data {
		if want[m.ID] {
			return true, true, ""
		}
	}
	return true, false, "PROBE_MODEL_MISSING: 上游模型列表里没有 " + a.ModelFor("standard")
}

func strPtr(s string) *string { return &s }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// TruncateRunes 按**字符**截断（不是字节）：上游错误文本常含中文，
// 按字节切会切出半个字，进 JSON 就是一个替换字符。
func TruncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
