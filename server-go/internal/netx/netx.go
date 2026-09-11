// 传输层策略：请求体上限与客户端地址解析。逐条复刻原实现 server/net.js。
// 拆成独立包是为了不启服务就能单测 —— 这两条都是反白嫖的地基。
package netx

import (
	"net"
	"net/http"
	"regexp"
	"strings"
)

// 请求体上限（net.js:14）。只有真正驮照片的那条路由给大额度。
const (
	MaxUploadBody = 26 * 1024 * 1024 // PUT /v1/assets/{id}/upload
	MaxAdminBody  = 1024 * 1024      // /v1/admin/*
	MaxJSONBody   = 64 * 1024        // 其余 /v1/*
)

var uploadPathRe = regexp.MustCompile(`^/v1/assets/[\w-]+/upload$`)

// BodyLimitFor 返回该方法 + 路径允许的请求体字节上限。
func BodyLimitFor(method, pathname string) int64 {
	if method == http.MethodPut && uploadPathRe.MatchString(pathname) {
		return MaxUploadBody
	}
	if strings.HasPrefix(pathname, "/v1/admin/") {
		return MaxAdminBody
	}
	return MaxJSONBody
}

// DrainBudget 复刻 readBody 的 min(limit*4, 8MB)：超限后不立刻断链，
// 在预算内继续丢弃读取，以便把 413 写在健康连接上。
func DrainBudget(limit int64) int64 {
	b := limit * 4
	if b > 8*1024*1024 {
		b = 8 * 1024 * 1024
	}
	return b
}

// IPResolver 决定限流与免费额度 IP 上限该按哪个地址计数。
//
// cf-connecting-ip 与 x-forwarded-for 的**第一项**都是调用方可伪造的。
// 规则：只有当 TCP 对端本身是可信代理时才读头，且只取 x-forwarded-for 的
// **最右一项**（可信代理自己追加的那个，客户端选不了它）。
type IPResolver struct {
	setting string
	list    []string
	trustCF bool
}

// NewIPResolver 按 TRUSTED_PROXY / TRUST_CF_CONNECTING_IP 构造解析器。
// TRUSTED_PROXY: "private"（默认）/ "none" / "*" / 逗号分隔的对端地址清单。
func NewIPResolver(trustedProxy string, trustCF bool) *IPResolver {
	setting := strings.TrimSpace(trustedProxy)
	if setting == "" {
		setting = "private"
	}
	var list []string
	for _, s := range strings.Split(setting, ",") {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, s)
		}
	}
	return &IPResolver{setting: setting, list: list, trustCF: trustCF}
}

// Bare 去掉 IPv6 映射前缀，与原实现的 bare() 等价。
func Bare(ip string) string { return strings.TrimPrefix(ip, "::ffff:") }

// IsTrustedPeer 判断 TCP 对端是否是可信代理。
func (r *IPResolver) IsTrustedPeer(addr string) bool {
	ip := Bare(addr)
	if r.setting == "none" || ip == "" {
		return false
	}
	if r.setting == "*" {
		return true
	}
	if r.setting != "private" {
		for _, v := range r.list {
			if v == ip {
				return true
			}
		}
		return false
	}
	return strings.HasPrefix(ip, "127.") || ip == "::1" ||
		strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "192.168.") ||
		private172.MatchString(ip) ||
		strings.HasPrefix(ip, "fd") || strings.HasPrefix(ip, "fc") || strings.HasPrefix(ip, "fe80:")
}

var private172 = regexp.MustCompile(`^172\.(1[6-9]|2\d|3[01])\.`)

// Resolve 返回本次请求应当计数的客户端地址。
func (r *IPResolver) Resolve(req *http.Request) string {
	peer := Bare(hostOf(req.RemoteAddr))
	if !r.IsTrustedPeer(peer) {
		return peer
	}
	if r.trustCF {
		if v := strings.TrimSpace(req.Header.Get("CF-Connecting-IP")); v != "" {
			return Bare(v)
		}
	}
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		parts := []string{}
		for _, s := range strings.Split(xff, ",") {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			// 最右项 = 可信代理追加的那个 = 客户端唯一选不了的那个。
			// 原实现早期取 parts[0]，那是纯客户端输入，等于没有任何 per-IP 限制。
			return Bare(parts[len(parts)-1])
		}
	}
	if v := strings.TrimSpace(req.Header.Get("X-Real-Ip")); v != "" {
		return Bare(v)
	}
	return peer
}

func hostOf(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
}
