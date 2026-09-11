package netx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 校验点 9：只信可信对端追加的 X-Forwarded-For **最右项**，默认忽略 cf-connecting-ip。
func TestClientIPTrustsRightmostXFFOnly(t *testing.T) {
	r := NewIPResolver("private", false)
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.RemoteAddr = "172.18.0.1:5000" // docker 网桥，属于可信私网
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	if got := r.Resolve(req); got != "203.0.113.9" {
		t.Fatalf("应取最右项 203.0.113.9，实际 %q（取第一项就是纯客户端输入，等于没有 per-IP 限制）", got)
	}
}

// 对端不可信时，一律用 TCP 地址，任何头都不看。
func TestClientIPIgnoresHeadersFromUntrustedPeer(t *testing.T) {
	r := NewIPResolver("private", false)
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.RemoteAddr = "203.0.113.7:5000"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	req.Header.Set("CF-Connecting-IP", "8.8.8.8")
	if got := r.Resolve(req); got != "203.0.113.7" {
		t.Fatalf("不可信对端应回落 TCP 地址，实际 %q", got)
	}
}

// cf-connecting-ip 默认被忽略；只有显式打开才读。
func TestCFConnectingIPIgnoredByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("CF-Connecting-IP", "8.8.8.8")

	if got := NewIPResolver("private", false).Resolve(req); got == "8.8.8.8" {
		t.Fatal("默认必须忽略 cf-connecting-ip（客户端可伪造）")
	}
	if got := NewIPResolver("private", true).Resolve(req); got != "8.8.8.8" {
		t.Fatalf("显式打开后应读 cf-connecting-ip，实际 %q —— 否则上一条是假绿", got)
	}
}

// 请求体上限表（net.js:14）。
func TestBodyLimits(t *testing.T) {
	cases := []struct {
		method, path string
		want         int64
	}{
		{http.MethodPut, "/v1/assets/abc-123/upload", MaxUploadBody},
		{http.MethodPost, "/v1/admin/db/query", MaxAdminBody},
		{http.MethodPost, "/v1/generation-jobs", MaxJSONBody},
		{http.MethodPost, "/v1/events", MaxJSONBody},
		// PUT 之外的方法打同一路径不给大额度。
		{http.MethodPost, "/v1/assets/abc-123/upload", MaxJSONBody},
	}
	for _, c := range cases {
		if got := BodyLimitFor(c.method, c.path); got != c.want {
			t.Errorf("%s %s: 期望 %d，实际 %d", c.method, c.path, c.want, got)
		}
	}
}

func TestDrainBudget(t *testing.T) {
	if got := DrainBudget(MaxJSONBody); got != MaxJSONBody*4 {
		t.Fatalf("小额度时预算是 4 倍，实际 %d", got)
	}
	if got := DrainBudget(MaxUploadBody); got != 8*1024*1024 {
		t.Fatalf("大额度时预算封顶 8MB，实际 %d", got)
	}
}
