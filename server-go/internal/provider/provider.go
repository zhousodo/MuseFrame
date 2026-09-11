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

	"museframe-api/internal/cfgstore"
)

// 稳定错误码（worker 会把它们写进 generation_jobs.error_code）。
const (
	CodeProviderTimeout  = "PROVIDER_TIMEOUT"
	CodeProviderError    = "PROVIDER_ERROR"
	CodeGenerationReject = "GENERATION_REJECTED"
	CodeStyleUnavailable = "STYLE_UNAVAILABLE"
)

// Err 是带稳定 code 的上游错误。
type Err struct {
	Code string
	Msg  string
}

func (e *Err) Error() string { return e.Code + ": " + e.Msg }

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
}

// New 构造适配器。
func New(cfg *cfgstore.Store, providerName, apiKey string) *Adapter {
	p := strings.ToLower(strings.TrimSpace(providerName))
	if p == "" {
		p = "remote"
	}
	return &Adapter{cfg: cfg, providerName: p, apiKey: apiKey}
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
