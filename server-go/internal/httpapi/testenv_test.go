package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"museframe-api/internal/cfgstore"
	"museframe-api/internal/config"
	"museframe-api/internal/logx"
	"museframe-api/internal/provider"
	"museframe-api/internal/store"
	"museframe-api/internal/worker"
)

// fakeMailer 记住最后一次发出的验证码，不真发信。
type fakeMailer struct {
	configured bool
	lastCode   string
	failNext   bool
}

func (m *fakeMailer) Configured() bool { return m.configured }
func (m *fakeMailer) SendLoginCode(_, code string) error {
	if m.failNext {
		return errSendFailed
	}
	m.lastCode = code
	return nil
}
func (m *fakeMailer) Send(to, subject, text, html string) ([]string, error) {
	if m.failNext {
		return nil, errSendFailed
	}
	return []string{to}, nil
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

const errSendFailed = sentinelErr("send failed")

// testEnv 是一套连着真 PostgreSQL 的完整 App。
type testEnv struct {
	t      *testing.T
	app    *App
	st     *store.Store
	rt     *cfgstore.Store
	cfg    *config.Config
	mail   *fakeMailer
	now    time.Time
	seq    int
	assets string
}

const adminToken = "test-admin-token-0123456789abcd"

// 全部 24 张业务表，清库用。顺序无所谓：TRUNCATE ... CASCADE。
var allTables = []string{
	"credit_ledger", "credit_buckets", "generation_candidates", "generation_jobs",
	"photo_analyses", "assets", "projects", "purchases", "user_feedback",
	"idempotency_records", "events", "free_grants", "manual_grants",
	"auth_identities", "sessions", "users",
	"exhibition_styles", "style_versions", "styles", "exhibitions",
	"products", "app_config", "email_codes", "server_secrets",
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("MUSEFRAME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 MUSEFRAME_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn, 4)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	t.Cleanup(st.Close)

	sql := "TRUNCATE TABLE "
	for i, tb := range allTables {
		if i > 0 {
			sql += ", "
		}
		sql += tb
	}
	sql += " RESTART IDENTITY CASCADE"
	if _, err := st.Pool().Exec(ctx, sql); err != nil {
		t.Fatalf("清库失败: %v", err)
	}

	assetDir := t.TempDir()
	t.Setenv("MUSEFRAME_DATABASE_URL", dsn)
	t.Setenv("IP_HASH_SALT", "test-ip-salt-not-the-admin-token")
	t.Setenv("ADMIN_TOKEN", adminToken)
	t.Setenv("MUSEFRAME_ASSET_DIR", assetDir)
	t.Setenv("ALLOW_GUEST", "false")
	t.Setenv("FREE_REQUIRES_AUTH", "true")
	t.Setenv("FREE_UNITS", "3")
	t.Setenv("EMAIL_LOGIN_ENABLED", "true")
	t.Setenv("IMAGE_PROVIDER", "remote")
	t.Setenv("IMAGE_PROVIDER_BASE_URL", "https://provider.invalid")
	t.Setenv("IMAGE_PROVIDER_API_KEY", "sk-test-key-not-in-db")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("配置加载失败: %v", err)
	}
	rt, err := cfgstore.New(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	lg := logx.NewWith(discardWriter{}, nil)
	prov := provider.New(rt, cfg.ImageProvider, cfg.ImageProviderAPIKey)
	mail := &fakeMailer{configured: true}
	env := &testEnv{t: t, st: st, rt: rt, cfg: cfg, mail: mail, assets: assetDir,
		now: time.Date(2026, 9, 11, 4, 26, 12, 396e6, time.UTC)}
	wk := worker.New(worker.Options{
		Store: st, Runtime: rt, Provider: prov, Logger: lg, AssetDir: assetDir,
		MaxAttempts: 3, NewID: NewUUID, Now: env.clock,
	})
	env.app = New(Options{
		Config: cfg, Runtime: rt, Store: st, Logger: lg, Provider: prov, Worker: wk,
		Mailer: mail, Version: "test", ImgTokenKey: []byte("test-img-hmac-key"),
		Now: env.clock, NewID: env.nextID,
	})
	env.seedCatalog()
	return env
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (e *testEnv) clock() time.Time { return e.now }

// nextID 给确定性 id，方便断言。
func (e *testEnv) nextID() string {
	e.seq++
	return "id" + pad8(e.seq)
}

func pad8(n int) string {
	s := ""
	for i := 0; i < 8; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s + "-0000-4000-8000-000000000000"
}

type resp struct {
	Code int
	Body []byte
}

func (r resp) JSON(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / %s", err, string(r.Body))
	}
}

func (r resp) Map(t *testing.T) map[string]any {
	t.Helper()
	m := map[string]any{}
	r.JSON(t, &m)
	return m
}

// do 打一个请求。headers 形如 "Authorization: Bearer xxx"。
func (e *testEnv) do(method, path string, body any, headers map[string]string) resp {
	e.t.Helper()
	var rdr *bytesReader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = newBytesReader(raw)
	} else {
		rdr = newBytesReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "127.0.0.1:5000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.app.Handler().ServeHTTP(w, req)
	return resp{Code: w.Code, Body: w.Body.Bytes()}
}

func (e *testEnv) admin() map[string]string { return map[string]string{"X-Admin-Token": adminToken} }

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

var _ = http.MethodGet
