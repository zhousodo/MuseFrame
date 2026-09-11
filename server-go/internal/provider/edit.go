package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"

	"museframe-api/internal/imaging"
)

// EditRequest 是一次图生图请求。
// JobID 只用于日志串联（一次失败要能从 docker logs 直接定位到 generation_jobs 行）。
type EditRequest struct {
	SourceJPEG  []byte
	SourceW     int
	SourceH     int
	AspectRatio string
	QualityTier string
	Instruction string
	JobID       string
}

// EditResult 是解码后的产出与用量。
type EditResult struct {
	Image *imaging.RGBA
	Usage map[string]any
}

var policyRe = regexp.MustCompile(`(?i)safety|policy|content|reject`)

// supplyRe 认的是「上游自己没有产能了」这一类话术。
// 🔴 本轮故障的原话就是 "No available compatible accounts"（HTTP 503）。
// 旧代码把它和 timeout 塞进同一条 retryRe，再配合 `status>=500 → PROVIDER_TIMEOUT`，
// 于是一周的 503 全部以 PROVIDER_TIMEOUT 入库，排障一直在查网络与超时。
var supplyRe = regexp.MustCompile(`(?i)no available|not available|no compatible|no channel|no quota|insufficient quota|out of (?:quota|capacity|credit)|overloaded|capacity|account`)

// timeoutRe 只认真正的超时话术。
var timeoutRe = regexp.MustCompile(`(?i)timeout|timed out|deadline|ETIMEDOUT`)

// classify 把（状态码, 上游错误文本）映射成稳定码与可重试性。
// 顺序即优先级，改动顺序就是改动契约。
func classify(status int, msg string) (code string, retry bool) {
	switch {
	// 内容策略：永久失败，重试只是再被拒一次（spec §14.3 禁止绕闸）。
	case policyRe.MatchString(msg):
		return CodeGenerationReject, false
	// 真超时：上游 504 / 408，或它自己说 timeout。这一类才允许重试。
	case status == http.StatusGatewayTimeout || status == http.StatusRequestTimeout || timeoutRe.MatchString(msg):
		return CodeProviderTimeout, true
	// 供给耗尽：503 / 429 本身就是「暂时没产能」，或错误文本自述没有可用账号 / 渠道 / 配额。
	case status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests:
		return CodeProviderUnavailable, false
	case status >= 500 && supplyRe.MatchString(msg):
		return CodeProviderUnavailable, false
	// 其余 5xx：上游自己炸了，可重试。
	case status >= 500:
		return CodeProviderError, true
	}
	return CodeProviderError, false
}

// HTTPClient 允许测试注入。
var HTTPClient = &http.Client{}

// CreateEdit 跑一次图生图。失败一律返回带稳定 code 的 *Err。
func (a *Adapter) CreateEdit(ctx context.Context, req EditRequest) (*EditResult, error) {
	if strings.TrimSpace(req.Instruction) == "" {
		return nil, &Err{Code: CodeStyleUnavailable, Msg: "StyleSpec has no promptAssembly"}
	}
	tier := "standard"
	if req.QualityTier == "high" {
		tier = "high"
	}

	model := a.ModelFor(tier)
	size := PickSize(req.AspectRatio, req.SourceW, req.SourceH)
	started := a.now()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", model)
	fw, err := mw.CreateFormFile("image", "source.jpg")
	if err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "无法构造请求体"}
	}
	if _, err := fw.Write(req.SourceJPEG); err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "无法写入请求体"}
	}
	_ = mw.WriteField("prompt", req.Instruction)
	_ = mw.WriteField("size", size)
	_ = mw.WriteField("quality", a.QualityFor(tier))
	if err := mw.Close(); err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "无法收尾请求体"}
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutMS())*time.Millisecond)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, a.BaseURL()+"/v1/images/edits", &buf)
	if err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "无法构造请求"}
	}
	// 密钥只出现在这一行，绝不进日志。
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := HTTPClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, a.failed(req.JobID, model, size, started,
				&Err{Code: CodeProviderTimeout, Msg: "本地 deadline 到点，上游未返回", Retry: true})
		}
		// 连不上不等于超时。文本里绝不含密钥：只报形态，不回传 err.Error()
		// （它可能含完整 URL / 代理串）。
		return nil, a.failed(req.JobID, model, size, started,
			&Err{Code: CodeProviderError, Msg: "上游不可达（DNS / 连接 / TLS 失败）", Retry: true})
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, a.failed(req.JobID, model, size, started,
			&Err{Code: CodeProviderError, Msg: "读取上游响应失败", Status: resp.StatusCode, Retry: true})
	}

	var body struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || len(body.Data) == 0 || body.Data[0].B64JSON == "" {
		msg := strings.TrimSpace(body.Error.Message)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d（响应体里没有 error.message）", resp.StatusCode)
		}
		code, retry := classify(resp.StatusCode, msg)
		return nil, a.failed(req.JobID, model, size, started,
			&Err{Code: code, Msg: TruncateRunes(msg, MaxUpstreamMsg), Status: resp.StatusCode, Retry: retry})
	}

	bin, err := base64.StdEncoding.DecodeString(body.Data[0].B64JSON)
	if err != nil {
		return nil, a.failed(req.JobID, model, size, started,
			&Err{Code: CodeProviderError, Msg: "上游返回的不是合法 base64", Status: resp.StatusCode})
	}
	img, err := imaging.DecodeAny(bin)
	if err != nil {
		return nil, a.failed(req.JobID, model, size, started,
			&Err{Code: CodeProviderError, Msg: "上游返回了未知图片格式", Status: resp.StatusCode})
	}

	// 成功也记一行：没有成功基线，失败日志无从判断「是这次坏了还是一直坏着」。
	ms := a.now().Sub(started).Milliseconds()
	a.HealthLedger().Record(Outcome{At: a.now(), OK: true, Status: resp.StatusCode})
	a.info("provider: 上游图生图成功", map[string]any{
		"jobId": req.JobID, "model": model, "size": size, "ms": ms,
		"status": resp.StatusCode, "width": img.Width, "height": img.Height,
	})
	return &EditResult{Image: img, Usage: body.Usage}, nil
}

// failed 是所有失败出口的唯一收口：记账本 + 记一行 warn 日志，然后把 err 原样返回。
//
// 🔴 日志里只有状态码 / 上游原话（截断 300 字符并过 Redact）/ 模型 / 尺寸 / 耗时 /
// job id / 稳定码 / 可否重试。绝不含密钥、绝不含 Authorization 头、
// 绝不含指令正文（提示词是商业资产，且可能含用户照片推导出的内容）。
func (a *Adapter) failed(jobID, model, size string, started time.Time, e *Err) *Err {
	at := a.now()
	ms := at.Sub(started).Milliseconds()
	a.HealthLedger().Record(Outcome{At: at, OK: false, Code: e.Code, Status: e.Status, Msg: e.Msg})
	a.warn("provider: 上游图生图失败", map[string]any{
		"jobId": jobID, "model": model, "size": size, "ms": ms,
		"status": e.Status, "code": e.Code, "retryable": e.Retry,
		"upstreamMessage": TruncateRunes(e.Msg, MaxUpstreamMsg),
	})
	return e
}

// TotalTokens 从 usage 里取 total_tokens（写进 generation_jobs.cost_minor）。
func (r *EditResult) TotalTokens() int64 {
	if r == nil || r.Usage == nil {
		return 0
	}
	if v, ok := r.Usage["total_tokens"]; ok {
		if f, ok := v.(float64); ok {
			return int64(f)
		}
	}
	return 0
}
