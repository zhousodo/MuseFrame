package worker

import (
	"context"
	"os"
	"time"

	"museframe-api/internal/imaging"
	"museframe-api/internal/ledger"
	"museframe-api/internal/provider"
	"museframe-api/internal/store"
)

// Recover 是开机恢复（jobs.js:58）。
//
//	created 且没有 reserve 台账的行：直接标 failed，不重跑 ——
//	重跑它就是一次没人付钱的生成。
//	created/queued/running/quality_check 的行：attempt_count 达上限就 fail，否则重排队。
func (w *Worker) Recover(ctx context.Context) (failed, requeued int, err error) {
	orphans, err := store.ListJobsByStatuses(ctx, w.st.Q(), []string{"created"})
	if err != nil {
		return 0, 0, err
	}
	for i := range orphans {
		j := orphans[i]
		has, err := store.LedgerHasReserve(ctx, w.st.Q(), j.ID)
		if err != nil {
			return failed, requeued, err
		}
		if has {
			continue
		}
		t := w.now()
		err = w.st.InTx(ctx, func(q store.Queryer) error {
			if err := store.FinishJobFailed(ctx, q, j.ID, "INTERNAL_ERROR", t); err != nil {
				return err
			}
			return store.SetProjectStatus(ctx, q, j.ProjectID, "draft", t)
		})
		if err != nil {
			return failed, requeued, err
		}
		failed++
		w.lg.Warn("worker: 任务无 reserve 台账，标失败不重跑", map[string]any{"jobId": j.ID})
	}

	stuck, err := store.ListJobsByStatuses(ctx, w.st.Q(), []string{"created", "queued", "running", "quality_check"})
	if err != nil {
		return failed, requeued, err
	}
	for i := range stuck {
		j := stuck[i]
		if j.AttemptCount >= w.MaxAttempts() {
			w.lg.Warn("worker: 尝试次数达上限，直接失败（崩溃循环护栏）",
				map[string]any{"jobId": j.ID, "attempts": j.AttemptCount})
			w.failJob(ctx, &j, provider.CodeProviderError)
			failed++
			continue
		}
		if err := store.SetJobStage(ctx, w.st.Q(), j.ID, "queued", "preparing", w.now()); err != nil {
			return failed, requeued, err
		}
		w.Enqueue(j.ID)
		requeued++
	}
	return failed, requeued, nil
}

func (w *Worker) failByID(ctx context.Context, jobID, code string) {
	job, err := store.GetJob(ctx, w.st.Q(), jobID)
	if err != nil {
		return
	}
	w.failJob(ctx, job, code)
}

// failJob 释放预留并把任务标失败。
// 失败的重试绝不能盖掉更早的成功：已经选好候选的项目仍然是 ready。
func (w *Worker) failJob(ctx context.Context, job *store.Job, code string) {
	t := w.now()
	err := w.st.InTx(ctx, func(q store.Queryer) error {
		if err := ledger.Release(ctx, q, w.newID, job.UserID, job.ID, t); err != nil {
			return err
		}
		if err := store.FinishJobFailed(ctx, q, job.ID, code, t); err != nil {
			return err
		}
		sel, err := store.ProjectSelectedCandidate(ctx, q, job.ProjectID)
		if err != nil {
			return err
		}
		status := "draft"
		if sel != nil && *sel != "" {
			status = "ready"
		}
		return store.SetProjectStatus(ctx, q, job.ProjectID, status, t)
	})
	if err != nil {
		w.lg.Warn("worker: 收口失败任务时出错", map[string]any{"jobId": job.ID, "error": err.Error()})
	}
}

func (w *Worker) processJob(ctx context.Context, jobID string) error {
	job, err := store.GetJob(ctx, w.st.Q(), jobID)
	if err != nil {
		return err
	}
	switch job.Status {
	case "succeeded", "failed", "cancelled":
		return nil
	}
	if job.AttemptCount >= w.MaxAttempts() {
		w.failJob(ctx, job, provider.CodeProviderError)
		return nil
	}
	// reserve 分录就是「这次生成付过钱」的凭据。没有 reserve 就不生成 ——
	// 否则一次丢失/回滚的扣款就变成一张免费的图。
	has, err := store.LedgerHasReserve(ctx, w.st.Q(), jobID)
	if err != nil {
		return err
	}
	if !has {
		w.lg.Warn("worker: 任务无 reserve 分录，拒绝执行", map[string]any{"jobId": jobID})
		w.failJob(ctx, job, "INTERNAL_ERROR")
		return nil
	}
	return w.runJob(ctx, job)
}

// qualityGate 是自动化质量闸（spec §9.4 子集）：可解码、尺寸不小于 64x64、非空白。
func qualityGate(img *imaging.RGBA) string {
	if img == nil || img.Width < 64 || img.Height < 64 {
		return "BAD_DIMENSIONS"
	}
	var sum float64
	n := 0
	for i := 0; i+2 < len(img.Data); i += 401 * 4 {
		sum += 0.299*float64(img.Data[i]) + 0.587*float64(img.Data[i+1]) + 0.114*float64(img.Data[i+2])
		n++
	}
	if n == 0 {
		return "BAD_DIMENSIONS"
	}
	mean := sum / float64(n)
	if mean < 2 || mean > 253 {
		return "BLANK_OUTPUT"
	}
	return ""
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

var _ = os.Remove
var _ = time.Second
