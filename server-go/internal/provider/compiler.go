package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PhotoFacts 是喂给编译器的照片事实（都来自服务端的启发式分析，不是客户端输入）。
type PhotoFacts struct {
	PersonCount *int
	Orientation string
	Exposure    *float64
}

// CompileInstruction 为「设计型」风格按每张照片编译一条最终的图生图指令。
//
// 任何失败都返回空串（调用方回落到静态拼装）——**绝不因为编译失败而让任务失败**，
// 与 Node 版 `return null` 的语义一致。
func (a *Adapter) CompileInstruction(ctx context.Context, compilerKey string, baseDirection string, controls map[string]string, subjectType string, facts PhotoFacts, imageJPEG []byte) string {
	profile, ok := compilerProfiles[compilerKey]
	if !ok || !a.Enabled() {
		return ""
	}

	var lines []string
	lines = append(lines, "Subject type: "+subjectType+".")
	if facts.PersonCount != nil && *facts.PersonCount != 0 {
		lines = append(lines, "People in frame: "+strconv.Itoa(*facts.PersonCount)+".")
	}
	if facts.Orientation != "" {
		lines = append(lines, "Frame orientation: "+facts.Orientation+".")
	}
	if facts.Exposure != nil {
		label := "normal"
		switch {
		case *facts.Exposure < 0.3:
			label = "dark/low light"
		case *facts.Exposure > 0.7:
			label = "bright"
		}
		lines = append(lines, "Exposure: "+label+".")
	}
	lines = append(lines, "Style strength requested: "+controls["strength"]+
		". Subject fidelity: "+controls["fidelity"]+". Composition: "+controls["composition"]+".")

	userText := "Photo facts:\n" + strings.Join(lines, "\n") +
		"\n\nStyle direction summary: " + baseDirection +
		"\n\nLook at the attached photograph, then compile the final image-edit prompt: " +
		"base the fragment/layout plan and every annotation on what is actually in this photo."

	var userContent any = userText
	if len(imageJPEG) > 0 {
		userContent = []any{
			map[string]any{"type": "text", "text": userText},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imageJPEG),
			}},
		}
	}
	payload := map[string]any{
		"model": a.cfg.String("prompt_compiler_model"),
		"messages": []any{
			map[string]any{"role": "system", "content": profile},
			map[string]any{"role": "user", "content": userContent},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, a.BaseURL()+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	// 密钥只出现在这一行，绝不进日志。
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ""
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return ""
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	// 太短的产出当作失败，回落静态拼装（与 Node 版的 length < 80 一致）。
	if len(text) < 80 {
		return ""
	}
	return text
}

// CompilerKeys 返回已实现的编译器档案键（测试用）。
func CompilerKeys() []string {
	out := make([]string, 0, len(compilerProfiles))
	for k := range compilerProfiles {
		out = append(out, k)
	}
	return out
}
