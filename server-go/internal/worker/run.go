package worker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"museframe-api/internal/imaging"
	"museframe-api/internal/ledger"
	"museframe-api/internal/provider"
	"museframe-api/internal/store"
)

func (w *Worker) runJob(ctx context.Context, job *store.Job) error {
	// 硬闸。没有配置图像供应商时，本服务不得产出任何东西：
	// 本地像素引擎是一次滤镜，不是模型。让它顶上会让一套没配好的部署
	// 看起来完全正常，却照样扣一张额度。这里再查一次（不只在建任务时查），
	// 好覆盖密钥被清空之前排进队、或开机恢复回来的任务。
	if gen := w.prov.Status(); !gen.Available {
		w.failJob(ctx, job, "GENERATION_UNAVAILABLE")
		return nil
	}
	// 供给熔断。连着 3 次上游「没有可用账号 / 渠道」之后，冷却期（60s）内的任务
	// 直接失败退额，**不再打上游、也不再付提示词编译那次 LLM 的钱** ——
	// 供给耗尽是不可重试的，再打一遍只会拿到同一条 503。
	// 冷却期一过就放一个任务过去探路，所以上游恢复不需要任何人工动作。
	if w.prov.SupplyDown(w.now()) {
		w.lg.Warn("worker: 上游供给熔断中，任务直接失败退额（不打上游）", map[string]any{
			"jobId": job.ID, "code": provider.CodeProviderUnavailable,
		})
		w.failJob(ctx, job, provider.CodeProviderUnavailable)
		return nil
	}

	_ = store.SetJobStage(ctx, w.st.Q(), job.ID, "running", "preparing", w.now())
	time.Sleep(stageDelays["preparing"])

	src, err := store.GetAssetByID(ctx, w.st.Q(), job.SourceAssetID)
	if err != nil || src == nil || src.Status != "ready" {
		w.failJob(ctx, job, "ASSET_NOT_READY")
		return nil
	}
	specRaw, err := store.GetStyleVersionSpec(ctx, w.st.Q(), job.StyleVersionID)
	if err != nil {
		w.failJob(ctx, job, "ASSET_NOT_READY")
		return nil
	}

	_ = store.SetJobStage(ctx, w.st.Q(), job.ID, "running", "building", w.now())
	time.Sleep(stageDelays["building"])

	controls := jsonMap(job.Controls)
	output := jsonMap(job.Output)

	_ = store.SetJobStage(ctx, w.st.Q(), job.ID, "running", "making", w.now())
	buf, err := w.readAsset(src.StorageKey)
	if err != nil {
		w.failJob(ctx, job, "ASSET_NOT_READY")
		return nil
	}
	subjectType := "person"
	personCount, exposure := (*int)(nil), (*float64)(nil)
	if st, pc, ex, err := store.GetAnalysisBrief(ctx, w.st.Q(), src.ID); err == nil {
		if st != nil && *st != "" {
			subjectType = *st
		}
		personCount, exposure = pc, ex
	}

	// 发送前先降采样：在供应商 1024/1536 的输出网格上看不出质量损失，
	// 却能明显降低上游延迟与输入 token（= 真金白银）。
	sendBuf, sendW, sendH := buf, intOr(src.Width, 0), intOr(src.Height, 0)
	if maxDim := maxInt(sendW, sendH); maxDim > 1024 {
		if decoded, err := imaging.DecodeJPEG(buf); err == nil {
			k := 1024.0 / float64(maxDim)
			small := imaging.Resize(decoded, roundInt(float64(sendW)*k), roundInt(float64(sendH)*k))
			if enc, err := imaging.EncodeJPEG(small, 88); err == nil {
				sendBuf, sendW, sendH = enc, small.Width, small.Height
			}
		}
	}

	instruction := buildInstruction(specRaw, controls, subjectType)
	// 「设计型」风格（zine / editorial / reportage）先按**这张照片**编译一条指令：
	// 隐喻、版式配方、从画面内容推导出来的短标注。编译失败一律回落静态拼装，
	// 绝不因此让任务失败（与 Node 版 compileInstruction 返回 null 的语义一致）。
	if key := compilerKeyOf(specRaw); key != "" {
		_ = store.SetJobStage(ctx, w.st.Q(), job.ID, "running", "building", w.now())
		compiled := w.prov.CompileInstruction(ctx, key, baseDirectionOf(specRaw),
			map[string]string{
				"strength": str(controls["strength"]), "fidelity": str(controls["fidelity"]),
				"composition": str(controls["composition"]),
			}, subjectType,
			provider.PhotoFacts{
				PersonCount: personCount,
				Orientation: orientationOf(intOr(src.Width, 0), intOr(src.Height, 0)),
				Exposure:    exposure,
			}, sendBuf) // 视觉编译器看的是降采样后的同一张图
		_ = store.SetJobStage(ctx, w.st.Q(), job.ID, "running", "making", w.now())
		if compiled != "" {
			instruction = compiled
		}
	}
	attempt := job.AttemptCount + 1
	_ = store.SetJobAttempts(ctx, w.st.Q(), job.ID, attempt)

	aspect, _ := output["aspectRatio"].(string)
	tier, _ := output["qualityTier"].(string)
	res, err := w.prov.CreateEdit(ctx, provider.EditRequest{
		SourceJPEG: sendBuf, SourceW: sendW, SourceH: sendH,
		AspectRatio: aspect, QualityTier: tier, Instruction: instruction,
		JobID: job.ID,
	})
	if err != nil {
		// 内容策略拒绝直接失败：备份引擎绝不能用来绕过安全闸（spec §14.3）。
		// 上游失败的那一行详细日志（状态码 / 上游原话 / 模型 / 尺寸 / 耗时）
		// 由 provider.failed 记录；这里只补「任务侧怎么收口的」。
		code := provider.CodeOf(err)
		w.lg.Warn("worker: 任务因上游失败收口", map[string]any{
			"jobId": job.ID, "code": code, "retryable": provider.Retryable(err),
			"attempt": attempt,
		})
		w.failJob(ctx, job, code)
		return nil
	}

	// 供应商的尺寸网格 != 请求比例：居中裁到目标比例。
	// original 跟随源图比例。少了这一步，candidate.width/height 会与 Node 版不同。
	out := res.Image
	if rw, rh := imaging.AspectRatioOf(aspect); rw > 0 {
		out = imaging.CropTo(out, rw, rh)
	} else {
		out = imaging.CropTo(out, intOr(src.Width, 1), intOr(src.Height, 1))
	}
	res.Image = out

	_ = store.SetJobStage(ctx, w.st.Q(), job.ID, "quality_check", "checking", w.now())
	time.Sleep(stageDelays["checking"])
	if reason := qualityGate(res.Image); reason != "" {
		w.failJob(ctx, job, reason)
		return nil
	}

	// 落盘先于写库（DB 行指向的就是这个文件）；随后任何一步 DB 写失败，
	// 都把孤儿文件删掉并收口任务、退还预留，而不是留下一个用户已被扣费的半成品。
	jpg, err := imaging.EncodeJPEG(res.Image, 90)
	if err != nil {
		w.failJob(ctx, job, "INTERNAL_ERROR")
		return nil
	}
	assetID := w.newID()
	storageKey := assetID + ".jpg"
	outPath := w.assetPath(storageKey)
	if err := os.WriteFile(outPath, jpg, 0o640); err != nil {
		w.failJob(ctx, job, "INTERNAL_ERROR")
		return nil
	}
	t := w.now()
	candidateID := w.newID()
	err = w.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.InsertCandidateAsset(ctx, q, assetID, job.UserID, job.ProjectID, storageKey,
			int64(len(jpg)), res.Image.Width, res.Image.Height, t); err != nil {
			return err
		}
		if err := store.InsertCandidate(ctx, q, candidateID, job.ID, 0, assetID, t); err != nil {
			return err
		}
		if err := ledger.Commit(ctx, q, w.newID, job.UserID, job.ID, t); err != nil {
			return err
		}
		if err := store.FinishJobSucceeded(ctx, q, job.ID, res.TotalTokens(), t); err != nil {
			return err
		}
		return store.SetProjectCandidate(ctx, q, job.ProjectID, candidateID, t)
	})
	if err != nil {
		_ = os.Remove(outPath)
		w.failJob(ctx, job, "INTERNAL_ERROR")
		return err
	}
	return nil
}

// buildInstruction 从 StyleSpec 拼装供应商无关的指令（spec §11.2）。
// 绝不包含任何厂商字段；可以记长度，绝不记内容。
func buildInstruction(specRaw []byte, controls map[string]any, subjectType string) string {
	var spec struct {
		PromptAssembly *struct {
			BaseDirection    string            `json:"baseDirection"`
			SubjectRules     map[string]string `json:"subjectRules"`
			ControlFragments struct {
				Strength    map[string]string `json:"strength"`
				Fidelity    map[string]string `json:"fidelity"`
				Composition map[string]string `json:"composition"`
			} `json:"controlFragments"`
			NegativeConstraints []string `json:"negativeConstraints"`
		} `json:"promptAssembly"`
	}
	if err := json.Unmarshal(specRaw, &spec); err != nil || spec.PromptAssembly == nil {
		return ""
	}
	pa := spec.PromptAssembly
	if pa.BaseDirection == "" {
		return ""
	}
	parts := []string{
		pa.BaseDirection,
		pickOr(pa.SubjectRules, subjectType, "person"),
		pickOr(pa.ControlFragments.Strength, str(controls["strength"]), "balanced"),
		pickOr(pa.ControlFragments.Fidelity, str(controls["fidelity"]), "high"),
		pickOr(pa.ControlFragments.Composition, str(controls["composition"]), "keep"),
		strings.Join(pa.NegativeConstraints, " "),
	}
	return strings.Join(parts, " ")
}

// compilerKeyOf 读 spec.promptAssembly.compiler。
func compilerKeyOf(specRaw []byte) string {
	var spec struct {
		PromptAssembly *struct {
			Compiler string `json:"compiler"`
		} `json:"promptAssembly"`
	}
	if err := json.Unmarshal(specRaw, &spec); err != nil || spec.PromptAssembly == nil {
		return ""
	}
	return spec.PromptAssembly.Compiler
}

// baseDirectionOf 读 spec.promptAssembly.baseDirection（作为编译器的风格方向摘要）。
func baseDirectionOf(specRaw []byte) string {
	var spec struct {
		PromptAssembly *struct {
			BaseDirection string `json:"baseDirection"`
		} `json:"promptAssembly"`
	}
	if err := json.Unmarshal(specRaw, &spec); err != nil || spec.PromptAssembly == nil {
		return ""
	}
	return spec.PromptAssembly.BaseDirection
}

// orientationOf 与 Node 版一致：landscape / portrait / square。
func orientationOf(w, h int) string {
	switch {
	case w > h:
		return "landscape"
	case h > w:
		return "portrait"
	}
	return "square"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func roundInt(v float64) int { return int(v + 0.5) }

func pickOr(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return m[fallback]
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
