// 上游图像模型适配器。逐条复刻 Node 版 engine/remoteAdapter.js。
//
// 与 Node 版的关键差异 —— 安全问题 1 的落点：
//
//	Node 版 remoteConfig.apiKey 读的是 cfg('image_provider_api_key')，
//	而 cfg() 的优先级是 DB > env > 默认，所以密钥可以躺在 app_config 表里。
//	Go 版的 apiKey 只从 config.Config（环境变量）来，cfgstore 那一侧
//	对 secret 键根本不返回 DB 值 —— 两处都堵，密钥只可能来自 project.env。
package provider

import (
	"errors"
	"strings"
	"sync"
	"time"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/logx"
)

// 稳定错误码（worker 会把它们写进 generation_jobs.error_code）。
const (
	// CodeProviderTimeout 只用于**真的超时**：本地 deadline 到点、上游 504/408、
	// 或上游错误文本自述 timeout。2026-09-05 起上游对 gpt-image-* 一律 503
	// 「No available compatible accounts」，旧映射（>=500 → TIMEOUT）把这种
	// 供给耗尽误报成超时，排障因此走了一周弯路。
	CodeProviderTimeout = "PROVIDER_TIMEOUT"
	// CodeProviderUnavailable 是供给类不可用：上游自己说没有可用账号 / 渠道 /
	// 容量，或直接回 503/429。对用户的文案是「生成服务暂时不可用，额度已退回」。
	// 🔴 它**不可重试** —— 再打一遍只是把同一条 503 再换一次，纯烧时间与钱
	//（每次失败的设计型风格任务都已经付过一次提示词编译的 LLM 费用）。
	CodeProviderUnavailable = "PROVIDER_UNAVAILABLE"
	CodeProviderError       = "PROVIDER_ERROR"
	CodeGenerationReject    = "GENERATION_REJECTED"
	CodeStyleUnavailable    = "STYLE_UNAVAILABLE"
)

// UserMessage 把稳定码翻成给用户看的中文短句。
// 额度在 failJob 里一律 Release（退回），所以文案可以把「已退回」写死。
func UserMessage(code string) string {
	switch code {
	case CodeProviderUnavailable:
		return "生成服务暂时不可用，额度已退回"
	case CodeProviderTimeout:
		return "生成超时了，额度已退回"
	case CodeGenerationReject:
		return "这张照片或这条指令无法生成，额度已退回"
	case CodeStyleUnavailable:
		return "这个风格暂时不可用，额度已退回"
	case CodeProviderError:
		return "生成失败了，额度已退回"
	}
	return ""
}

// Err 是带稳定 code 的上游错误。
//
// Status 是上游 HTTP 状态码（0 = 还没拿到响应：DNS / 连接 / 本地超时）。
// Retry 为假表示**不许重试**（供给耗尽、内容策略拒绝、参数错）。
type Err struct {
	Code   string
	Msg    string
	Status int
	Retry  bool
}

func (e *Err) Error() string { return e.Code + ": " + e.Msg }

// Retryable 回答「这个错误再打一次有意义吗」。
// 非 *Err 一律按可重试处理（未知故障给它一次机会），
// 但供给类 / 策略类 / 风格缺失一律为假。
func Retryable(err error) bool {
	var e *Err
	if errors.As(err, &e) {
		return e.Retry
	}
	return true
}

// StatusOf 取上游 HTTP 状态码，拿不到返回 0。
func StatusOf(err error) int {
	var e *Err
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// CodeOf 取错误的稳定码，非上游错误返回 PROVIDER_ERROR。
func CodeOf(err error) string {
	var e *Err
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeProviderError
}

// Status 是 generationStatus() 的返回值。
//
//	mode remote —— 已配置的付费模型
//	mode local  —— 运维显式 IMAGE_PROVIDER=local（开发/离线）
//	mode none   —— 什么都没配，拒绝生成
type Status struct {
	Available bool
	Provider  string
	Mode      string
	Missing   []string
	Reason    *string
}

// Adapter 持有「能不能生成」的判据与上游调用。
type Adapter struct {
	cfg *cfgstore.Store
	// providerName 是 env-only 的 IMAGE_PROVIDER（换供应商是运维动作，不给后台热改）。
	providerName string
	// apiKey 只来自环境变量，永不来自数据库。
	apiKey string
	// lg 可为 nil（单测里构造的裸 Adapter）；一律走 a.warn / a.info 这两个哨兵。
	lg *logx.Logger
	// hl 是上游调用的健康账本（近 N 次结果 + 轻量探针缓存）。
	hl *Health
	// nowFn 只给测试注入；生产恒为 time.Now().UTC()。
	nowFn func() time.Time
	// probeOff 关掉轻量探针（集成测试里 BASE_URL 是 provider.invalid，
	// 不该让测试进程真去做 DNS 查询）。
	probeOff bool

	mu sync.Mutex
}

// New 构造适配器。
func New(cfg *cfgstore.Store, providerName, apiKey string) *Adapter {
	p := strings.ToLower(strings.TrimSpace(providerName))
	if p == "" {
		p = "remote"
	}
	return &Adapter{cfg: cfg, providerName: p, apiKey: apiKey, hl: NewHealth()}
}

// SetLogger 注入日志器（main 在构造完 logx 之后调用一次）。
func (a *Adapter) SetLogger(lg *logx.Logger) {
	a.mu.Lock()
	a.lg = lg
	a.mu.Unlock()
}

// SetNow 只给测试用：固定时钟。
func (a *Adapter) SetNow(f func() time.Time) {
	a.mu.Lock()
	a.nowFn = f
	a.mu.Unlock()
}

// SetProbeEnabled 开关轻量探针。生产默认开；测试关。
func (a *Adapter) SetProbeEnabled(on bool) {
	a.mu.Lock()
	a.probeOff = !on
	a.mu.Unlock()
}

func (a *Adapter) probeEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.probeOff
}

// HealthLedger 暴露健康账本（main 用它做开机播种，测试用它断言）。
func (a *Adapter) HealthLedger() *Health {
	if a.hl == nil {
		a.hl = NewHealth()
	}
	return a.hl
}

func (a *Adapter) now() time.Time {
	a.mu.Lock()
	f := a.nowFn
	a.mu.Unlock()
	if f != nil {
		return f()
	}
	return time.Now().UTC()
}

func (a *Adapter) logger() *logx.Logger {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lg
}

func (a *Adapter) warn(msg string, extra map[string]any) {
	if lg := a.logger(); lg != nil {
		lg.Warn(msg, extra)
	}
}

func (a *Adapter) info(msg string, extra map[string]any) {
	if lg := a.logger(); lg != nil {
		lg.Info(msg, extra)
	}
}

// ProviderName 返回 env-only 的 IMAGE_PROVIDER。
func (a *Adapter) ProviderName() string { return a.providerName }

// BaseURL 是上游地址（可后台热改，非密钥）。
func (a *Adapter) BaseURL() string {
	return strings.TrimRight(a.cfg.String("image_provider_base_url"), "/")
}

// HasAPIKey 只回答「有没有」，绝不回传值。
func (a *Adapter) HasAPIKey() bool { return a.apiKey != "" }

// TimeoutMS 上游超时。Node 侧被 undici 的 300s headersTimeout 夹住过，
// 这里保留同样的上限口径，让后台显示、用户看到的估时与代码三者一致。
func (a *Adapter) TimeoutMS() int {
	v := a.cfg.Int("image_provider_timeout_ms")
	if v <= 0 {
		v = 420000
	}
	if v > 290000 {
		v = 290000
	}
	return v
}

// ModelFor 返回某档位使用的模型。
func (a *Adapter) ModelFor(tier string) string {
	if tier == "high" {
		if m := strings.TrimSpace(a.cfg.String("image_provider_model_high")); m != "" {
			return m
		}
	}
	if m := a.cfg.String("image_provider_model"); m != "" {
		return m
	}
	return "gpt-image-2"
}

// QualityFor 返回某档位的 images/edits quality 参数。
func (a *Adapter) QualityFor(tier string) string {
	key, fallback := "image_quality_standard", "medium"
	if tier == "high" {
		key, fallback = "image_quality_high", "high"
	}
	switch raw := strings.ToLower(strings.TrimSpace(a.cfg.String(key))); raw {
	case "low", "medium", "high", "auto":
		return raw
	}
	return fallback
}

// Enabled 判断远程模式是否可用（密钥与地址缺一不可）。
func (a *Adapter) Enabled() bool {
	return a.providerName == "remote" && a.HasAPIKey() && a.BaseURL() != ""
}

// Status 是「这套部署到底能不能产出一张图」的唯一判据。
// API 在接受任务前查一次（不预留额度），worker 在跑之前再查一次
// （覆盖密钥被清空之前排进队的任务）。
func (a *Adapter) Status() Status {
	if a.providerName == "local" {
		return Status{Available: true, Provider: a.providerName, Mode: "local", Missing: []string{}}
	}
	if a.providerName != "remote" {
		reason := "PROVIDER_UNKNOWN"
		return Status{Available: false, Provider: a.providerName, Mode: "none", Missing: []string{}, Reason: &reason}
	}
	missing := []string{}
	if !a.HasAPIKey() {
		missing = append(missing, "image_provider_api_key")
	}
	if a.BaseURL() == "" {
		missing = append(missing, "image_provider_base_url")
	}
	if len(missing) > 0 {
		reason := "PROVIDER_NOT_CONFIGURED"
		return Status{Available: false, Provider: a.providerName, Mode: "none", Missing: missing, Reason: &reason}
	}
	return Status{Available: true, Provider: a.providerName, Mode: "remote", Missing: []string{}}
}

// PickSize 按请求比例 / 源图朝向选供应商尺寸网格。
func PickSize(aspectRatio string, srcW, srcH int) string {
	switch aspectRatio {
	case "1:1":
		return "1024x1024"
	case "16:9":
		return "1536x1024"
	case "4:5":
		return "1024x1536"
	}
	if float64(srcW) > float64(srcH)*1.15 {
		return "1536x1024"
	}
	if float64(srcH) > float64(srcW)*1.15 {
		return "1024x1536"
	}
	return "1024x1024"
}
