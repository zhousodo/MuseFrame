package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"museframe-api/internal/config"
)

// 回归：静态壳绝不能对以 /index.html 结尾的 URL 做 301。
//
// 事故史：net/http 的 serveFile 有一条无条件的规范化跳转（URL 以 /index.html
// 结尾 → 301 "./"）。museframe.caddy 的 @app 块把 /app、/app/、/app/* 统一
// `rewrite * /index.html` 再打后端，于是 2026-09-11 第一次 Node→Go 切换时
// 整个 Web App 入口在边缘层集体 301，只能回滚。旧 Node 后端这里是 200。
// 这条测试就是不让它再回来。
func TestStaticIndexHTMLNeverRedirects(t *testing.T) {
	dir := t.TempDir()
	shell := []byte("<!doctype html><title>museframe shell</title>")
	if err := os.WriteFile(filepath.Join(dir, "index.html"), shell, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("export const x=1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: &config.Config{WebDir: dir}}

	for _, tc := range []struct {
		path    string
		wantLen int
		wantCT  string
	}{
		{"/index.html", len(shell), "text/html; charset=utf-8"},        // 边缘 @app rewrite 后的真实形态
		{"/index.html?x=1", len(shell), "text/html; charset=utf-8"},    // 带 query（Caddy 的 rewrite 保留 query）
		{"/", len(shell), "text/html; charset=utf-8"},                  // 根
		{"/app/index.html", len(shell), "text/html; charset=utf-8"},    // 深链也以 /index.html 结尾 → 走兜底分支
		{"/anything/deep/index.html", len(shell), "text/html; charset=utf-8"},
		{"/app.js", len("export const x=1;\n"), "text/javascript; charset=utf-8"},
		{"/no-such-file", len(shell), "text/html; charset=utf-8"},      // SPA 兜底
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		status, err := a.serveStatic(w, r)
		if err != nil {
			t.Fatalf("%s: 意外错误 %v", tc.path, err)
		}
		res := w.Result()
		if res.StatusCode != http.StatusOK || status != http.StatusOK {
			t.Errorf("%s: 期望 200，实得 %d（serveStatic 返回 %d）；Location=%q",
				tc.path, res.StatusCode, status, res.Header.Get("Location"))
		}
		if loc := res.Header.Get("Location"); loc != "" {
			t.Errorf("%s: 不该有 Location 头，实得 %q", tc.path, loc)
		}
		if got := w.Body.Len(); got != tc.wantLen {
			t.Errorf("%s: 体长度期望 %d，实得 %d", tc.path, tc.wantLen, got)
		}
		if got := res.Header.Get("Content-Type"); got != tc.wantCT {
			t.Errorf("%s: Content-Type 期望 %q，实得 %q", tc.path, tc.wantCT, got)
		}
	}
}

// WebDir 为空时一律 404，绝不返回 200 软 404。
func TestStaticNoWebDirIs404(t *testing.T) {
	a := &App{cfg: &config.Config{WebDir: ""}}
	r := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	w := httptest.NewRecorder()
	if _, err := a.serveStatic(w, r); err == nil {
		t.Fatal("期望返回错误（404），实得 nil")
	}
}

// 路径穿越必须落回 index.html，不能读到根目录之外。
func TestStaticPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("shell"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)

	a := &App{cfg: &config.Config{WebDir: dir}}
	r := httptest.NewRequest(http.MethodGet, "/../"+filepath.Base(outside), nil)
	w := httptest.NewRecorder()
	if _, err := a.serveStatic(w, r); err != nil {
		t.Fatalf("意外错误 %v", err)
	}
	if b := w.Body.String(); b != "shell" {
		t.Fatalf("穿越未被挡住，实得 %q", b)
	}
}
