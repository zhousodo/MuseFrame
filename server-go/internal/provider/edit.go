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
type EditRequest struct {
	SourceJPEG  []byte
	SourceW     int
	SourceH     int
	AspectRatio string
	QualityTier string
	Instruction string
}

// EditResult 是解码后的产出与用量。
type EditResult struct {
	Image *imaging.RGBA
	Usage map[string]any
}

var policyRe = regexp.MustCompile(`(?i)safety|policy|content|reject`)
var retryRe = regexp.MustCompile(`(?i)account|unavailable|timeout`)

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

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", a.ModelFor(tier))
	fw, err := mw.CreateFormFile("image", "source.jpg")
	if err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "无法构造请求体"}
	}
	if _, err := fw.Write(req.SourceJPEG); err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "无法写入请求体"}
	}
	_ = mw.WriteField("prompt", req.Instruction)
	_ = mw.WriteField("size", PickSize(req.AspectRatio, req.SourceW, req.SourceH))
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
			return nil, &Err{Code: CodeProviderTimeout, Msg: "上游超时"}
		}
		return nil, &Err{Code: CodeProviderError, Msg: "上游不可达"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "读取上游响应失败"}
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
		msg := body.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		code := CodeProviderError
		switch {
		case policyRe.MatchString(msg):
			code = CodeGenerationReject
		case resp.StatusCode >= 500 || retryRe.MatchString(msg):
			code = CodeProviderTimeout
		}
		return nil, &Err{Code: code, Msg: msg}
	}

	bin, err := base64.StdEncoding.DecodeString(body.Data[0].B64JSON)
	if err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "上游返回的不是合法 base64"}
	}
	img, err := imaging.DecodeAny(bin)
	if err != nil {
		return nil, &Err{Code: CodeProviderError, Msg: "上游返回了未知图片格式"}
	}
	return &EditResult{Image: img, Usage: body.Usage}, nil
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
