// museframe-api —— 留影（MuseFrame）后端（Go + PostgreSQL），替换原 Node.js + SQLite 实现。
//
// 逐条复刻 50 条路由（公开 30 + 管理 20）。与 Node 版的有意差异全部登记在 README，
// 其中三条是本轮要修的安全问题：
//  1. 上游密钥不再可能来自数据库（cfgstore 对 secret 键不走 DB，PUT /admin/config 拒绝）；
//  2. /v1/admin/db/table/{name} 改成表白名单 + 显式列级脱敏清单；
//  3. 额度四道闸与消费侧 10 步按序复刻，每条都有「应该被拒绝」的测试。
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/config"
	"museframe-api/internal/httpapi"
	"museframe-api/internal/logx"
	"museframe-api/internal/mailer"
	"museframe-api/internal/play"
	"museframe-api/internal/provider"
	"museframe-api/internal/store"
	"museframe-api/internal/worker"
)

// version 由 -ldflags "-X main.version=<tag>" 注入。
//
// 🔴 这个变量**不要直接用**，一律走 resolveVersion()：2026-09-12 的验收里后台
// 「运行状态 · 后端版本」显示的是字面量 "dev" —— 本地交叉编译那一步漏了 -X
// （或者 $TAG 当时是空串），而镜像本身是有 tag 的。一条只在构建命令正确时才对
// 的展示等于没有展示：运维拿它回答「线上跑的是哪个版本」，显示 dev 比不显示更坏。
var version = "dev"

// versionEnvVars 是回退读的环境变量，按优先级。IMAGE_TAG 是 platformctl 的
// project.env 里本来就有的那个键（compose 把它透给容器），所以哪怕 ldflags
// 再漏一次，后台显示的也会是真实的镜像 tag 而不是 "dev"。
var versionEnvVars = []string{"MUSEFRAME_IMAGE_TAG", "IMAGE_TAG"}

// resolveVersion 决定后台「后端版本」那一行显示什么：
// ldflags 注入值优先，注入缺失（空 / 占位 "dev"）时回退读镜像 tag 环境变量，
// 两路都没有才回到 "dev"（此时显示 dev 是诚实的：确实无从得知）。
func resolveVersion(injected string, getenv func(string) string) string {
	if v := strings.TrimSpace(injected); v != "" && v != "dev" {
		return v
	}
	for _, name := range versionEnvVars {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
	}
	return "dev"
}

// seedProviderHealth 用 generation_jobs 的历史收口行给上游健康账本播种。
// 失败不致命：播不上只是回到「零样本 + 探针」，不该让进程起不来。
func seedProviderHealth(ctx context.Context, st *store.Store, prov *provider.Adapter, lg *logx.Logger) {
	since := time.Now().UTC().Add(-provider.HealthWindow)
	rows, err := store.ListRecentProviderOutcomes(ctx, st.Q(), provider.HealthRing, since)
	if err != nil {
		lg.Warn("开机播种上游健康账本失败（退化为零样本 + 探针判据）", map[string]any{"error": err.Error()})
		return
	}
	out := make([]provider.Outcome, 0, len(rows))
	for _, r := range rows {
		out = append(out, provider.Outcome{At: r.At, OK: r.OK, Code: r.Code, Msg: "历史收口行（重启前的任务，上游原话未落库）"})
	}
	n := prov.HealthLedger().Seed(out)
	lg.Info("上游健康账本已播种", map[string]any{"samples": n})
}

func main() {
	// `museframe-api healthcheck` 子命令：给 compose healthcheck 用。
	// 镜像是 scratch，没有 curl / wget / sh —— 探针只能由二进制自己承担。
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	// `museframe-api version` 打一行解析后的版本号。发版时用它在**打包前**验证
	// -X 注入到底有没有生效 —— 这正是上一轮漏掉的那一步。
	if len(os.Args) > 1 && os.Args[1] == "version" {
		os.Stdout.WriteString(resolveVersion(version, os.Getenv) + "\n")
		os.Exit(0)
	}

	lg := logx.New()
	buildVersion := resolveVersion(version, os.Getenv)
	cfg, err := config.Load()
	if err != nil {
		lg.Warn("启动失败：配置不合法", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.AssetDir, 0o750); err != nil {
		lg.Warn("启动失败：资产目录不可写", map[string]any{"dir": cfg.AssetDir})
		os.Exit(1)
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	st, err := openStoreWithRetry(rootCtx, cfg, lg)
	if err != nil {
		lg.Warn("启动失败：无法连接 PostgreSQL", nil)
		os.Exit(1)
	}
	defer st.Close()

	// 🔴 运行角色 museframe_app 没有 DDL 权限，schema 由一次性 job 执行。
	// 这里**不建表**，只做一次存在性自检，缺表立刻失败而不是静默跑。
	if err := st.HealthProbe(rootCtx); err != nil {
		lg.Warn("启动失败：数据库自检不通过（schema 是否已由迁移 job 建好？）", nil)
		os.Exit(1)
	}

	rt, err := cfgstore.New(rootCtx, st)
	if err != nil {
		lg.Warn("启动失败：无法读取运行时配置", nil)
		os.Exit(1)
	}
	// 库里若还残留着历史写入的密钥行，点名键名（绝不打印值）提醒去清。
	if len(rt.SkippedSecretRows) > 0 {
		lg.Warn("app_config 里存在遗留的密钥行，已忽略（值不会被使用）；请在迁移收尾时删除这些行",
			map[string]any{"keys": rt.SkippedSecretRows})
	}

	prov := provider.New(rt, cfg.ImageProvider, cfg.ImageProviderAPIKey)
	// 上游每一次调用（成功与失败）都要有一行日志；密钥永不进日志（logx.Redact 兜底）。
	prov.SetLogger(lg)
	// 🔴 健康判据的证据必须活过重启：只看 in-process 环形缓冲的话，一个连着
	// 失败一周的部署在容器重启后会立刻报绿 —— 这正是本轮故障被瞒了一周的原因。
	// 这里用 generation_jobs 的历史收口行给账本播种（只取上游类码 + 成功）。
	seedProviderHealth(rootCtx, st, prov, lg)
	mail := mailer.New(rt, cfg.SMTPPass)
	playClient := play.New(cfg.GoogleServiceAccountJSON, rt.String("google_package_name"))

	wk := worker.New(worker.Options{
		Store: st, Runtime: rt, Provider: prov, Logger: lg, AssetDir: cfg.AssetDir,
		NewID: httpapi.NewUUID,
	})

	// 图片令牌的 HMAC 密钥必须活过重启，且绝不能是运维输入。
	imgKey, err := store.ServerSecret(rootCtx, st.Q(), "asset_img_token", randomSecret, time.Now().UTC())
	if err != nil {
		lg.Warn("启动失败：无法读取/生成 asset_img_token", nil)
		os.Exit(1)
	}

	app := httpapi.New(httpapi.Options{
		Config: cfg, Runtime: rt, Store: st, Logger: lg, Provider: prov, Worker: wk,
		Mailer: mail, Play: playClient, Version: buildVersion, ImgTokenKey: []byte(imgKey),
	})
	pub, adm := app.RouteCount()
	lg.Info("路由已注册", map[string]any{"public": pub, "admin": adm, "total": pub + adm})

	// 生产里忘关的旗标，单独就是一条水龙头；两个逃生口都还要管理员令牌才生效，
	// 但开机时仍然要喊一嗓子。
	if cfg.AllowMockPurchases {
		lg.Warn("ALLOW_MOCK_PURCHASES=true（演示购买）——仅限开发；生产请设为 false。当前需管理员令牌才可调用。", nil)
	}
	if cfg.AllowTestLogin {
		lg.Warn("ALLOW_TEST_LOGIN=true（测试登录）——仅限开发；生产请设为 false。当前需管理员令牌才可调用。", nil)
	}

	// 开机恢复：无 reserve 台账的 created 任务标失败、不重跑。
	if failed, requeued, err := wk.Recover(rootCtx); err != nil {
		lg.Warn("开机恢复失败", map[string]any{"error": err.Error()})
	} else {
		lg.Info("开机恢复完成", map[string]any{"failed": failed, "requeued": requeued})
	}
	go wk.Run(rootCtx)

	// 保留期清理：开机一次 + 每 24 小时一次。
	go retentionLoop(rootCtx, st, rt, lg)
	// 限流键清理：每 60 秒一次。
	go sweepLoop(rootCtx, app)

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		lg.Info("museframe-api listening", map[string]any{"host": cfg.Host, "port": cfg.Port, "version": buildVersion})
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	select {
	case err := <-errCh:
		lg.Warn("HTTP 服务异常退出", map[string]any{"error": err.Error()})
		os.Exit(1)
	case sig := <-sigCh:
		lg.Info("收到退出信号，开始优雅关闭", map[string]any{"signal": sig.String()})
	}

	// 优雅关闭：先停止接收新请求并等在途请求结束，再把在跑的生成任务放完。
	// 没有这一步，容器重启会切断在途 HTTP 响应，以及已经计费但未收货的上游调用。
	grace := time.Duration(cfg.ShutdownGraceS) * time.Second
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		lg.Warn("HTTP 优雅关闭超时，强制结束", nil)
	}
	if left := wk.Drain(grace - 5*time.Second); left > 0 {
		lg.Warn("仍有生成任务在跑，宽限期已到", map[string]any{"active": left})
	}
	rootCancel()
	lg.Info("已退出", nil)
}

func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("系统随机数不可用")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func openStoreWithRetry(ctx context.Context, cfg *config.Config, lg *logx.Logger) (*store.Store, error) {
	var lastErr error
	for attempt := 1; attempt <= 10; attempt++ {
		st, err := store.Open(ctx, cfg.DatabaseURL, cfg.PoolMaxConns)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ok := st.Ready(pingCtx)
			cancel()
			if ok {
				return st, nil
			}
			st.Close()
			lastErr = errors.New("PostgreSQL 未就绪")
		} else {
			lastErr = err
		}
		lg.Info("等待 PostgreSQL 就绪", map[string]any{"attempt": attempt})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, lastErr
}

// retentionLoop 每 24 小时跑一次保留期清理。
//
// 🔴 保留天数**每轮重新从注册表读**，不在函数入口取一次：这个循环的生命周期
// 等于进程的生命周期，取一次就等于「改完要重启才生效」—— 而它俩 2026-09-12 起
// 是后台热键，后台上改完不重启就该在下一轮生效。
func retentionLoop(ctx context.Context, st *store.Store, rt *cfgstore.Store, lg *logx.Logger) {
	run := func() {
		jobCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		res, err := store.PruneOldRows(jobCtx, st.Q(), time.Now().UTC(),
			rt.EventRetentionDays(), rt.IdempotencyRetentionDays())
		if err != nil {
			lg.Warn("保留期清理失败", map[string]any{"error": err.Error()})
			return
		}
		lg.Info("保留期清理完成", map[string]any{
			"events": res.Events, "sessions": res.Sessions,
			"idempotency": res.Idempotency, "emailCodes": res.EmailCodes,
		})
	}
	run()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

func sweepLoop(ctx context.Context, app *httpapi.App) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			app.SweepRateLimiter()
		}
	}
}

// runHealthcheck 打本进程的 /v1/ready，通返回 0、不通返回 1。
// 只读探针：/v1/ready 内部是 SELECT 1，不写库。
func runHealthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/v1/ready")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
