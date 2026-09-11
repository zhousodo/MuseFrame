// 生成管线 worker。进程内队列 + 数据库真相（重启后 queued/running 会被重新排队）。
// 阶段与 Node 版 jobs.js 一致：preparing -> building -> making -> checking -> complete。
package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/imaging"
	"museframe-api/internal/ledger"
	"museframe-api/internal/logx"
	"museframe-api/internal/provider"
	"museframe-api/internal/store"
)

// 刻意的节奏延时，让进度条读起来诚实（Node 版 STAGE_DELAYS）。
var stageDelays = map[string]time.Duration{
	"preparing": 700 * time.Millisecond,
	"building":  900 * time.Millisecond,
	"checking":  600 * time.Millisecond,
}

// Queue 是 /v1/health 里的 queue 块，键序固定。
type Queue struct {
	Queued             int  `json:"queued"`
	Active             int  `json:"active"`
	OldestQueuedAgeSec int  `json:"oldestQueuedAgeSec"`
	Draining           bool `json:"draining"`
}

// Worker 是生成队列。
type Worker struct {
	st       *store.Store
	rt       *cfgstore.Store
	prov     *provider.Adapter
	lg       *logx.Logger
	assetDir string
	newID    func() string
	now      func() time.Time

	mu       sync.Mutex
	queue    []string
	queuedAt map[string]time.Time
	active   int
	draining bool
	wake     chan struct{}
	stopped  chan struct{}
}

// Options 是构造参数。
type Options struct {
	Store    *store.Store
	Runtime  *cfgstore.Store
	Provider *provider.Adapter
	Logger   *logx.Logger
	AssetDir string
	NewID    func() string
	Now      func() time.Time
}

// New 构造 worker。
func New(o Options) *Worker {
	now := o.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{
		st: o.Store, rt: o.Runtime, prov: o.Provider, lg: o.Logger, assetDir: o.AssetDir,
		newID: o.NewID, now: now,
		queuedAt: map[string]time.Time{}, wake: make(chan struct{}, 1), stopped: make(chan struct{}),
	}
}

// Enqueue 把任务排进队列。
func (w *Worker) Enqueue(jobID string) {
	w.mu.Lock()
	w.queue = append(w.queue, jobID)
	w.queuedAt[jobID] = w.now()
	w.mu.Unlock()
	w.signal()
}

func (w *Worker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Depth 是给 /v1/health 的队列快照 —— 「worker 到底还动不动」。
func (w *Worker) Depth() Queue {
	w.mu.Lock()
	defer w.mu.Unlock()
	oldest := 0
	now := w.now()
	for _, id := range w.queue {
		if at, ok := w.queuedAt[id]; ok {
			if sec := int(now.Sub(at).Seconds() + 0.5); sec > oldest {
				oldest = sec
			}
		}
	}
	return Queue{Queued: len(w.queue), Active: w.active, OldestQueuedAgeSec: oldest, Draining: w.draining}
}

// MaxAttempts 是崩溃循环护栏：单个任务最多尝试几次（含首次）。
//
// 🔴 它是崩溃循环护栏**也是**钱的闸。一个会把进程搞崩的任务在下次开机会被重新
// 排队，于是再崩一次 —— 没有上限就是无限重启循环，而对远程供应商每一圈都是
// 真实计费（设计型风格每圈还额外付一次提示词编译的 LLM 费）。
//
// 2026-09-12 起读注册表热键 max_job_attempts（原先是启动时从 MAX_JOB_ATTEMPTS
// 读一次固化在字段里）：上游按次计费炸掉的时候要能立刻压到 1，而不是等发版。
// 每次用时重新读，所以后台一改，下一个任务就按新值走。
func (w *Worker) MaxAttempts() int { return w.rt.MaxJobAttempts() }

// Concurrency 返回当前并发上限。
//
// 🔴 夹在 [1, 8]：后台曾经可以设 0，队列会静默冻结，
// 而每个排队任务的额度都已经被预留，除了重启没有出路。
func (w *Worker) Concurrency() int {
	n := w.rt.Int("worker_concurrency")
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}

// Run 是调度主循环，由 main 起一个 goroutine 跑。
func (w *Worker) Run(ctx context.Context) {
	defer close(w.stopped)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
		w.pump(ctx)
	}
}

func (w *Worker) pump(ctx context.Context) {
	maxConcurrent := w.Concurrency()
	for {
		w.mu.Lock()
		if w.draining || w.active >= maxConcurrent || len(w.queue) == 0 {
			w.mu.Unlock()
			return
		}
		jobID := w.queue[0]
		w.queue = w.queue[1:]
		delete(w.queuedAt, jobID)
		w.active++
		w.mu.Unlock()

		go func(id string) {
			defer func() {
				// 最后一道防线：processJob 自己有 catch，但如果连它都炸了，
				// 任务会永远停在 running 且额度已预留，开机恢复每次重启都会重跑它。
				if r := recover(); r != nil {
					w.lg.Warn("worker: 任务崩溃", map[string]any{"jobId": id})
					w.failByID(context.Background(), id, "INTERNAL_ERROR")
				}
				w.mu.Lock()
				w.active--
				w.mu.Unlock()
				w.signal()
			}()
			if err := w.processJob(ctx, id); err != nil {
				w.lg.Warn("worker: 任务失败", map[string]any{"jobId": id, "error": err.Error()})
			}
		}(jobID)
	}
}

// Drain 停止派发并等在跑的任务结束，最多等 timeout。
// 没有这个机制，容器重启会切断在途响应、切断已计费但未收货的上游调用。
func (w *Worker) Drain(timeout time.Duration) int {
	w.mu.Lock()
	w.draining = true
	w.mu.Unlock()
	deadline := w.now().Add(timeout)
	for {
		w.mu.Lock()
		active := w.active
		w.mu.Unlock()
		if active == 0 || w.now().After(deadline) {
			return active
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (w *Worker) assetPath(key string) string { return filepath.Join(w.assetDir, key) }

func (w *Worker) readAsset(key string) ([]byte, error) { return os.ReadFile(w.assetPath(key)) }

// jsonMap 解一个 jsonb 列。
func jsonMap(raw []byte) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

var _ = ledger.Commit
var _ = imaging.MaxSourcePixels
