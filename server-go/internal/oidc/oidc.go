// OIDC ID Token 校验（Google 登录 / Sign in with Apple）。
// 客户端对「我是谁」的任何声称都在这里对着签发方的密码学签名核验，
// 绝不按客户端的说法采信。只用标准库。
package oidc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 稳定错误码。调用方据此区分 401（令牌坏）与 503（签发方不可达）。
var (
	// ErrNotConfigured：本部署没有配置该登录方式。
	ErrNotConfigured = errors.New("PROVIDER_NOT_CONFIGURED")
	// ErrJWKSUnavailable：够不到签发方的公钥服务。
	//
	// 🔴「我们够不到签发方的密钥服务」不等于「你的令牌是伪造的」。
	// 把它报成 AUTH_INVALID，会让 Google 的一次故障在每个用户眼里变成
	// 「我的账号被拒了」，而客户端的重试逻辑把 401 当成终态。
	ErrJWKSUnavailable = errors.New("JWKS_UNAVAILABLE")
	// ErrInvalid：令牌本身不合法。
	ErrInvalid = errors.New("AUTH_INVALID")
)

// Claims 是校验通过后返回的身份。
type Claims struct {
	Subject string
	Email   string
	Name    string
}

type cacheEntry struct {
	keys []jwk
	exp  time.Time
}

type jwk struct {
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// Verifier 带一个一小时的 JWKS 缓存。
type Verifier struct {
	mu     sync.Mutex
	cache  map[string]cacheEntry
	client *http.Client
	now    func() time.Time
}

// New 构造校验器。
func New() *Verifier {
	return &Verifier{
		cache:  map[string]cacheEntry{},
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
}

// ResetCache 是测试钩子。
func (v *Verifier) ResetCache() {
	v.mu.Lock()
	v.cache = map[string]cacheEntry{}
	v.mu.Unlock()
}

// fetchJWKS 取签发方公钥集。
// 🔴 失败**绝不进缓存**：曾经一次 502 把 keys=undefined 存了整整一小时，
// 那一小时内每次登录都死在 keys.find 上，用户看到的是「你的账号不对」。
func (v *Verifier) fetchJWKS(ctx context.Context, url string, force bool) ([]jwk, error) {
	v.mu.Lock()
	if c, ok := v.cache[url]; ok && !force && c.exp.After(v.now()) {
		v.mu.Unlock()
		return c.keys, nil
	}
	v.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ErrJWKSUnavailable
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, ErrJWKSUnavailable
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrJWKSUnavailable
	}
	var body struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Keys) == 0 {
		return nil, ErrJWKSUnavailable
	}
	v.mu.Lock()
	v.cache[url] = cacheEntry{keys: body.Keys, exp: v.now().Add(time.Hour)}
	v.mu.Unlock()
	return body.Keys, nil
}

func b64urlJSON(s string, dst any) error {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

type rawClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Sub           string          `json:"sub"`
	Exp           int64           `json:"exp"`
	Email         string          `json:"email"`
	EmailVerified any             `json:"email_verified"`
	Name          string          `json:"name"`
}

// verifyIDToken 校验签名、iss、aud、exp。
func (v *Verifier) verifyIDToken(ctx context.Context, idToken, jwksURL string, issuers, audience []string) (*rawClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, ErrInvalid
	}
	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	if err := b64urlJSON(parts[0], &header); err != nil {
		return nil, ErrInvalid
	}
	if header.Alg == "" {
		header.Alg = "RS256"
	}
	if header.Alg != "RS256" {
		return nil, ErrInvalid
	}
	var claims rawClaims
	if err := b64urlJSON(parts[1], &claims); err != nil {
		return nil, ErrInvalid
	}

	keys, err := v.fetchJWKS(ctx, jwksURL, false)
	if err != nil {
		return nil, err
	}
	key := matchKey(keys, header.Kid, header.Alg)
	if key == nil {
		// kid 对不上通常是密钥轮换，不是伪造：绕过一小时缓存重取一次再判。
		keys, err = v.fetchJWKS(ctx, jwksURL, true)
		if err != nil {
			return nil, err
		}
		key = matchKey(keys, header.Kid, header.Alg)
	}
	if key == nil {
		return nil, ErrInvalid
	}
	pub, err := toRSAPublicKey(key)
	if err != nil {
		return nil, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err != nil {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, ErrInvalid
	}
	if !contains(issuers, claims.Iss) {
		return nil, ErrInvalid
	}
	if !audMatches(claims.Aud, audience) {
		return nil, ErrInvalid
	}
	if time.Unix(claims.Exp, 0).Before(v.now()) {
		return nil, ErrInvalid
	}
	return &claims, nil
}

func matchKey(keys []jwk, kid, alg string) *jwk {
	for i := range keys {
		k := keys[i]
		if k.Kid == kid && (k.Alg == alg || k.Alg == "") {
			return &keys[i]
		}
	}
	return nil
}

func toRSAPublicKey(k *jwk) (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, fmt.Errorf("unsupported kty")
	}
	nb, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.N, "="))
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.E, "="))
	if err != nil {
		return nil, err
	}
	if len(eb) > 8 {
		return nil, fmt.Errorf("exponent too large")
	}
	var padded [8]byte
	copy(padded[8-len(eb):], eb)
	e := binary.BigEndian.Uint64(padded[:])
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e)}, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// audMatches 兼容 aud 是字符串或字符串数组两种形态。
func audMatches(raw json.RawMessage, audience []string) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return contains(audience, s)
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, a := range arr {
			if contains(audience, a) {
				return true
			}
		}
	}
	return false
}

// VerifyGoogle 校验 Google ID Token。
func (v *Verifier) VerifyGoogle(ctx context.Context, idToken string, clientIDs []string) (*Claims, error) {
	if len(clientIDs) == 0 {
		return nil, ErrNotConfigured
	}
	c, err := v.verifyIDToken(ctx, idToken, "https://www.googleapis.com/oauth2/v3/certs",
		[]string{"accounts.google.com", "https://accounts.google.com"}, clientIDs)
	if err != nil {
		return nil, err
	}
	email := ""
	if truthy(c.EmailVerified) {
		email = c.Email
	}
	return &Claims{Subject: c.Sub, Email: email, Name: c.Name}, nil
}

// VerifyApple 校验 Apple ID Token。
func (v *Verifier) VerifyApple(ctx context.Context, idToken string, bundleIDs []string) (*Claims, error) {
	if len(bundleIDs) == 0 {
		return nil, ErrNotConfigured
	}
	c, err := v.verifyIDToken(ctx, idToken, "https://appleid.apple.com/auth/keys",
		[]string{"https://appleid.apple.com"}, bundleIDs)
	if err != nil {
		return nil, err
	}
	return &Claims{Subject: c.Sub, Email: c.Email}, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}
