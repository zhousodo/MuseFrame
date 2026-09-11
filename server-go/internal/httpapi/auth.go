package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

var assetFileRe = regexp.MustCompile(`^/v1/assets/[\w-]+/file$`)

// authenticate 是会真过期的会话查找。
//
// `?token=` 仍被接受，但**只在取图那条路由上**：那是已发布客户端唯一会把它
// 放进 URL 的地方（一个 <img src>）。到处都接受就等于每个 query string 都是凭据。
func (a *App) authenticate(r *http.Request, u *url.URL) (*store.User, error) {
	header := r.Header.Get("Authorization")
	var token string
	switch {
	case len(header) > 7 && header[:7] == "Bearer ":
		token = header[7:]
	case r.Method == http.MethodGet && assetFileRe.MatchString(u.Path):
		token = u.Query().Get("token")
	}
	if token == "" {
		return nil, nil
	}
	ctx := r.Context()
	s, err := store.GetSession(ctx, a.st.Q(), token)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	now := a.now()
	if s.ExpiresAt != nil && !s.ExpiresAt.After(now) {
		return nil, nil
	}
	// 限到每小时一次，免得轮询把每次读都变成一次写。
	if now.Sub(s.LastSeenAt) > time.Hour {
		if err := store.TouchSession(ctx, a.st.Q(), token, now, a.cfg.SessionTTLDays); err != nil {
			return nil, err
		}
	}
	user, err := store.GetUser(ctx, a.st.Q(), s.UserID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, nil // 合并过的游客账号 deleted_at 非空，自动失效
		}
		return nil, err
	}
	return user, nil
}

func requireUser(c *Ctx) (*store.User, error) {
	if c.User == nil {
		return nil, apierr.New(http.StatusUnauthorized, apierr.CodeAuthRequired, "Sign in to continue.")
	}
	return c.User, nil
}

// requireAccount：游客令牌只标识一个匿名浏览器/设备，**永远不解锁账号数据**。
// 早期版本的游客行里可能还留着作品与台账；把那些行当成已登录账号，
// 会让一个看起来已登出的浏览器读到图片和余额。
func requireAccount(c *Ctx) (*store.User, error) {
	u, err := requireUser(c)
	if err != nil {
		return nil, err
	}
	if u.IsGuest {
		return nil, apierr.New(http.StatusUnauthorized, apierr.CodeAuthRequired, "Sign in to continue.")
	}
	return u, nil
}

// isAdminRequest 判断请求是否带着运维的管理员令牌。
//
// 🔴 只读请求头。`?admin_token=` 曾经也被接受，那会把这个能开 SQL 控制台的
// 长期凭据写进 Caddy 访问日志、浏览器历史与 Referer —— 已修，重写不得恢复。
func (a *App) isAdminRequest(r *http.Request) bool {
	if len(a.adminTok) == 0 {
		return false
	}
	given := []byte(r.Header.Get("X-Admin-Token"))
	if len(given) != len(a.adminTok) {
		return false
	}
	return subtle.ConstantTimeCompare(given, a.adminTok) == 1
}

func (a *App) requireAdmin(c *Ctx) error {
	if !a.isAdminRequest(c.R) {
		return apierr.New(http.StatusUnauthorized, apierr.CodeAuthRequired, "Admin token required.")
	}
	return nil
}

// hash24 = sha256(salt + v) 取前 24 位十六进制。
// free_grants 的 device_hash / ip_hash 用它脱敏：这张表永远不带能直接映射回
// 访问者地址的东西。旧版的回落盐是字面量 museframe —— 在 2^32 的 IPv4 空间上
// 等于无盐，拿到库副本的人可以直接反查。
func hash24(v, salt string) string {
	sum := sha256.Sum256([]byte(salt + v))
	return hex.EncodeToString(sum[:])[:24]
}

// assetImgToken 是给 <img src> 用的短时、按账号绑定的 HMAC 令牌。
// 按小时桶签发，校验时接受当前与上一小时两个桶（实际有效期 1~2 小时）。
func (a *App) assetImgToken(userID string, bucket int64) string {
	m := hmac.New(sha256.New, a.imgHMAC)
	m.Write([]byte("img:" + userID + ":" + strconv.FormatInt(bucket, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:24]
}

// verifyAssetImgToken 校验图片令牌是否属于某账号。
// 令牌本身不携带用户名，所以是拿「资产拥有者」去反算比对。
func (a *App) verifyAssetImgToken(token, userID string) bool {
	if len(token) != 24 {
		return false
	}
	h := a.now().UnixMilli() / 3600000
	for _, b := range []int64{h, h - 1} {
		exp := a.assetImgToken(userID, b)
		if subtle.ConstantTimeCompare([]byte(token), []byte(exp)) == 1 {
			return true
		}
	}
	return false
}

// adminImgToken 是管理后台的图片令牌：HMAC-SHA256(ADMIN_TOKEN, "img:"+小时桶) 前 24 位。
func (a *App) adminImgToken(bucket int64) string {
	m := hmac.New(sha256.New, a.adminTok)
	m.Write([]byte("img:" + strconv.FormatInt(bucket, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:24]
}

func (a *App) adminImgTokenValid(token string) bool {
	if token == "" || len(a.adminTok) == 0 {
		return false
	}
	h := a.now().UnixMilli() / 3600000
	for _, b := range []int64{h, h - 1} {
		exp := a.adminImgToken(b)
		if len(token) == len(exp) && subtle.ConstantTimeCompare([]byte(token), []byte(exp)) == 1 {
			return true
		}
	}
	return false
}
