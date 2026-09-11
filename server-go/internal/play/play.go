// Google Play 收据校验。用服务账号对着 Play Developer API 核验 purchaseToken。
// 客户端伪造不了：只有 Google 自己的记录确认这笔购买真实且仍有效，才会通过。
//
// 只用标准库（crypto/rsa + net/http + encoding/json）。
package play

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// 稳定错误码。
var (
	// ErrNotConfigured：服务账号未配置或不可读。
	// 缺失/不可读/损坏的密钥文件是运维问题，不是一笔坏购买 ——
	// 原始 ENOENT 曾经冒到买家面前变成 402「你的钱没了，收据是假的」。
	ErrNotConfigured = errors.New("PROVIDER_NOT_CONFIGURED")
	// ErrPurchaseInvalid：Play 明确说这笔购买无效。
	ErrPurchaseInvalid = errors.New("PURCHASE_INVALID")
	// ErrUnavailable：够不到 Play（超时 / DNS / 5xx）—— 可重试，不是永久失败。
	ErrUnavailable = errors.New("VERIFICATION_UNAVAILABLE")
	// ErrTokenMalformed：purchaseToken 或 productId 不符合 Google 的字母表。
	ErrTokenMalformed = errors.New("PURCHASE_TOKEN_INVALID")
)

// 🔴 Play 的 purchaseToken 与 productId 会被直接拼进 Play API 的 URL 路径。
// Google 的令牌是 URL-safe base64（[A-Za-z0-9._-]），任何越界字符不是攻击就是
// 客户端 bug —— 而且是要命的：一个未编码的 ? # / 或 %xx 会产生一个 Google 解析到
// **同一笔购买**的 URL，而我们的 purchases 表看到的却是一个全新的
// external_transaction_id，于是同一笔真实付款可以靠往令牌后面加 ?x=1 / # / %2E
// 被无限次重新发放。
var (
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,512}$`)
	productRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	// OrderRe 是 Google 订单号的字母表（GPA.3312-1234-5678-90123 这种）。
	OrderRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// AssertToken 校验令牌与商品 id 的字母表。
func AssertToken(purchaseToken, productID string) error {
	if !tokenRe.MatchString(purchaseToken) || !productRe.MatchString(productID) {
		return ErrTokenMalformed
	}
	return nil
}

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// Result 是一次校验的结论。
type Result struct {
	Valid                bool
	OrderID              string
	ExpiresAt            *time.Time
	AcknowledgementState int
}

// Client 是 Play 校验客户端。
type Client struct {
	saPath      string
	packageName string
	http        *http.Client
	now         func() time.Time

	mu      sync.Mutex
	sa      *serviceAccount
	token   string
	tokenTo time.Time
}

// New 构造客户端。saPath 为空表示未配置。
func New(saPath, packageName string) *Client {
	return &Client{
		saPath: saPath, packageName: packageName,
		http: &http.Client{Timeout: 20 * time.Second}, now: time.Now,
	}
}

// Configured 判断是否配置了服务账号。
func (c *Client) Configured() bool { return c.saPath != "" }

func (c *Client) loadSA() (*serviceAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sa != nil {
		return c.sa, nil
	}
	if c.saPath == "" {
		return nil, ErrNotConfigured
	}
	raw, err := os.ReadFile(c.saPath)
	if err != nil {
		return nil, ErrNotConfigured
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, ErrNotConfigured
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	c.sa = &sa
	return c.sa, nil
}

// accessToken 用服务账号签一个 JWT 换 OAuth2 访问令牌，缓存到过期前 60 秒。
func (c *Client) accessToken(ctx context.Context) (string, error) {
	sa, err := c.loadSA()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.token != "" && c.tokenTo.After(c.now().Add(time.Minute)) {
		tok := c.token
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	now := c.now()
	header := base64url(`{"alg":"RS256","typ":"JWT"}`)
	claims := fmt.Sprintf(
		`{"iss":%q,"scope":"https://www.googleapis.com/auth/androidpublisher","aud":%q,"exp":%d,"iat":%d}`,
		sa.ClientEmail, sa.TokenURI, now.Add(time.Hour).Unix(), now.Unix())
	signingInput := header + "." + base64url(claims)
	key, err := parsePrivateKey(sa.PrivateKey)
	if err != nil {
		return "", ErrNotConfigured
	}
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", ErrNotConfigured
	}
	assertion := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI, stringsReader(form.Encode()))
	if err != nil {
		return "", ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", ErrUnavailable
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", ErrUnavailable
	}
	c.mu.Lock()
	c.token = out.AccessToken
	c.tokenTo = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	c.mu.Unlock()
	return out.AccessToken, nil
}

func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("bad pem")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := any.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not rsa")
	}
	return k, nil
}

func base64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func parseMillis(s string) *time.Time {
	if s == "" {
		return nil
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}
