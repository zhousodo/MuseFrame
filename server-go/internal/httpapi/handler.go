package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"museframe-api/internal/apierr"
	"museframe-api/internal/netx"
)

// CSP 与 Node 版 index.js:141 逐字一致（单引号必须带上）。
const cspValue = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; " +
	"script-src 'self' 'unsafe-inline'; connect-src 'self' https://museframe.lenscript.cn; " +
	"object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// Handler 返回 net/http 处理器。
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(a.serve)
}

func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	start := a.now()
	requestID := "req_" + a.newID()[:8]

	h := w.Header()
	// CORS：打包后的移动端是 WebView 源。
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key")
	// 反向代理缺席或过期时，应用边界这层基础防护仍然成立。
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
	h.Set("Content-Security-Policy", cspValue)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	status, err := a.route(w, r, requestID)
	if err != nil {
		e := asAPIError(err)
		status = e.Status
		if !headersWritten(w) {
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(e.Status)
			_ = json.NewEncoder(w).Encode(apierr.Envelope{Error: apierr.EnvelopeBody{
				Code: e.Code, Message: e.Message, RequestID: requestID, Details: e.Details,
			}})
		}
	}
	a.lg.LogRequest(r.Method, logPath(r.URL), status, a.now().Sub(start).Milliseconds(), requestID)
}

// logPath 只记 pathname，绝不记 query —— 图片令牌与旧版会话令牌都可能在 query 里。
func logPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Path
}

// headersWritten 在本实现里恒为 false：所有自写响应的 handler 都把 Handled 置真，
// 出错路径不会走到这里。保留函数是为了让意图显式。
func headersWritten(http.ResponseWriter) bool { return false }

func (a *App) route(w http.ResponseWriter, r *http.Request, requestID string) (int, error) {
	// 绝不用不可信的 Host 头当 URL 输入。`Host: [` 曾经在 catch 之外抛
	// ERR_INVALID_URL，一个未鉴权 TCP 请求就能杀掉整个进程。
	if host := r.Host; host != "" {
		if _, err := url.Parse("http://" + host); err != nil || strings.ContainsAny(host, " \t\r\n") {
			return 0, apierr.New(http.StatusBadRequest, apierr.CodeValidation, "Invalid Host header.")
		}
	}
	u := r.URL
	clientIP := a.ipr.Resolve(r)

	if !strings.HasPrefix(u.Path, "/v1/") {
		return a.serveStatic(w, r)
	}

	w.Header().Set("Cache-Control", "no-store")
	for _, rule := range rlRules {
		if rule.method != "" && rule.method != r.Method {
			continue
		}
		if !rule.re.MatchString(u.Path) {
			continue
		}
		if retry := a.limiter.Hit(clientIP, rule.re.String(), rule.limit, rule.window); retry > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(apierr.Envelope{Error: apierr.EnvelopeBody{
				Code: apierr.CodeRateLimited, Message: "Too many requests.", RequestID: requestID,
				Details: map[string]any{"retryAfterSeconds": retry},
			}})
			return http.StatusTooManyRequests, nil
		}
	}

	for _, rt := range a.routes {
		if rt.method != r.Method {
			continue
		}
		m := rt.pattern.FindStringSubmatch(u.Path)
		if m == nil {
			continue
		}
		ctx := &Ctx{W: w, R: r, URL: u, Params: m[1:], ClientIP: clientIP, RequestID: requestID}
		user, err := a.authenticate(r, u)
		if err != nil {
			return 0, err
		}
		ctx.User = user

		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			raw, err := readBody(r, netx.BodyLimitFor(r.Method, u.Path))
			if err != nil {
				return 0, err
			}
			ctx.Raw = raw
			if len(raw) > 0 && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
				body, err := jsonUnmarshal(raw)
				if err != nil {
					return 0, err
				}
				ctx.Body = body
			}
		}
		if ctx.Body == nil {
			ctx.Body = map[string]any{}
		}

		value, err := rt.handler(ctx)
		if err != nil {
			return 0, err
		}
		if ctx.Handled {
			return http.StatusOK, nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(value)
		return http.StatusOK, nil
	}
	return 0, apierr.New(http.StatusNotFound, apierr.CodeNotFound, "No such endpoint.")
}

var staticMIME = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".png": "image/png", ".jpg": "image/jpeg",
	".svg": "image/svg+xml", ".json": "application/json", ".woff2": "font/woff2",
}

// serveFileNoIndexRedirect 发送一个已定位到的静态文件。
//
// 🔴 为什么不用 http.ServeFile：net/http 的 serveFile 里有一条**无条件**的
// 规范化跳转 —— 只要 r.URL.Path 以 "/index.html" 结尾就 301 到 "./"，
// 与传进来的文件名无关。旧 Node 后端对 /index.html 是 200，Go 一上来就
// 变成 301，而 museframe.caddy 的 @app 块又恰好把 /app、/app/* 统一
// `rewrite * /index.html` 再打后端 —— 于是整个 Web App 入口在切换那一刻
// 集体 301。2026-09-11 第一次切换就炸在这里，已回滚。
//
// http.ServeContent 没有这条跳转，其余语义（Range、If-Modified-Since、
// Last-Modified、Content-Length）与 ServeFile 完全一致。路径穿越在调用方
// 已经用 filepath.Clean + 根前缀校验挡掉了，不依赖 ServeFile 的 containsDotDot。
func serveFileNoIndexRedirect(w http.ResponseWriter, r *http.Request, abs string, fi os.FileInfo) {
	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, "Not found.", http.StatusNotFound)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// serveStatic 提供 SPA 静态文件 + 兜底。
// MUSEFRAME_WEB_DIR 为空时（容器里由边缘代理托管静态资源）一律 404，
// 绝不返回 200 HTML 软 404 —— 那会让整站被搜索引擎判为低质量。
func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) (int, error) {
	if a.cfg.WebDir == "" {
		return 0, apierr.New(http.StatusNotFound, apierr.CodeNotFound, "Not found.")
	}
	p := r.URL.Path
	if p == "/" {
		p = "/index.html"
	}
	clean := filepath.Clean(strings.TrimPrefix(path.Clean(p), "/"))
	if clean == "." || strings.HasPrefix(clean, "..") {
		clean = "index.html"
	}
	file := filepath.Join(a.cfg.WebDir, clean)
	root, err := filepath.Abs(a.cfg.WebDir)
	if err != nil {
		return 0, apierr.New(http.StatusNotFound, apierr.CodeNotFound, "Not found.")
	}
	abs, err := filepath.Abs(file)
	if err != nil || !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		abs = filepath.Join(root, "index.html")
	}
	if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
		ext := strings.ToLower(filepath.Ext(abs))
		cache := "public, max-age=86400"
		if ext == ".html" || ext == ".js" || ext == ".css" {
			// HTML/JS/CSS 必须每次重新校验 —— 一份缓存住的 app.css 就是
			// 「桌面站以手机框渲染」那个 bug 的成因。
			cache = "no-cache"
		}
		ct := staticMIME[ext]
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", cache)
		serveFileNoIndexRedirect(w, r, abs, fi)
		return http.StatusOK, nil
	}
	index := filepath.Join(root, "index.html")
	ifi, err := os.Stat(index)
	if err != nil {
		return 0, apierr.New(http.StatusNotFound, apierr.CodeNotFound, "Not found.")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	serveFileNoIndexRedirect(w, r, index, ifi)
	return http.StatusOK, nil
}
