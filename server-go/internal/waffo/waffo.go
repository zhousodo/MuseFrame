// Waffo Pancake（商户记录 MoR 收款）客户端。直接实现 @waffo/pancake-ts 走的那套协议：
// 每个请求用商户 RSA 私钥做 RSA-SHA256 PKCS1v15 签名，对方用 Dashboard 上登记的公钥核验。
//
// 只用标准库（crypto/rsa + net/http + encoding/json），与 internal/play 同一取向。
//
// 🔴 私钥只从环境变量 WAFFO_PRIVATE_KEY 读（config），绝不进 DB、绝不进日志。
// 本包任何错误文本都不带请求体与签名。
//
// 协议要点（对照 docs.waffo.ai/api-reference/authentication）：
//
//	canonicalRequest = METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + base64(sha256(BODY))
//	X-Signature      = base64(RSA-SHA256-PKCS1v15(canonicalRequest))
//	X-Timestamp      = unix 秒；对方接受「落后 ≤5 分钟、超前 ≤1 分钟」
//
// 请求体必须与参与哈希的字节**逐字节一致**，所以这里先 Marshal 成 []byte、
// 拿它算哈希、再把同一个切片发出去。
package waffo

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL 是生产 API 地址。测试环境与生产共用同一个域名，靠 API Key 区分。
const DefaultBaseURL = "https://api.waffo.ai"

// 稳定错误码。
var (
	// ErrNotConfigured：商户号或私钥缺失 / 不可解析。运维问题，不是一笔坏订单。
	ErrNotConfigured = errors.New("WAFFO_NOT_CONFIGURED")
	// ErrUnavailable：够不到 Waffo（超时 / DNS / 5xx）—— 可重试。
	ErrUnavailable = errors.New("WAFFO_UNAVAILABLE")
)

// APIError 是 Waffo 明确回的 4xx：请求本身有问题，重试无意义。
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("waffo %d: %s", e.Status, e.Message) }

// IsAPIError 判断错误是否为 Waffo 回的业务错误。
func IsAPIError(err error) (*APIError, bool) {
	var e *APIError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Config 是客户端构造参数。
type Config struct {
	MerchantID string
	// PrivateKeyPEM 接受 PKCS1（"RSA PRIVATE KEY"）与 PKCS8（"PRIVATE KEY"），
	// 也接受把换行写成字面量 `\n` 的单行环境变量值。
	PrivateKeyPEM string
	BaseURL       string
	HTTPClient    *http.Client
	Now           func() time.Time
}

// Client 是签名 HTTP 客户端。零值不可用，用 New 构造。
type Client struct {
	merchantID string
	key        *rsa.PrivateKey
	baseURL    string
	http       *http.Client
	now        func() time.Time
}

// New 构造客户端。商户号或私钥缺失 / 不可解析时返回的客户端 Configured() 为 false，
// 所有调用回 ErrNotConfigured —— 与 play.Client 同一约定：服务照常起，支付路由回 501。
func New(cfg Config) *Client {
	c := &Client{
		merchantID: strings.TrimSpace(cfg.MerchantID),
		baseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		http:       cfg.HTTPClient,
		now:        cfg.Now,
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 15 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if k, err := ParsePrivateKey(cfg.PrivateKeyPEM); err == nil {
		c.key = k
	}
	return c
}

// Configured 判断商户号与私钥是否齐全且可用。
func (c *Client) Configured() bool { return c != nil && c.merchantID != "" && c.key != nil }

// ParsePrivateKey 解析 PEM 私钥。接受 PKCS1 / PKCS8，接受 `\n` 转义的单行值。
func ParsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	s := strings.TrimSpace(pemStr)
	if s == "" {
		return nil, ErrNotConfigured
	}
	// 环境变量里常见的写法：整段 PEM 压成一行，换行写成字面量 \n。
	if !strings.Contains(s, "\n") && strings.Contains(s, `\n`) {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("waffo: 私钥不是合法 PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	anyKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("waffo: 私钥既不是 PKCS1 也不是 PKCS8")
	}
	k, ok := anyKey.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("waffo: 私钥不是 RSA")
	}
	return k, nil
}

// CanonicalRequest 拼待签名串。body 是**将要发出去的那份字节**。
func CanonicalRequest(method, path string, timestamp int64, body []byte) string {
	sum := sha256.Sum256(body)
	return method + "\n" + path + "\n" + strconv.FormatInt(timestamp, 10) + "\n" +
		base64.StdEncoding.EncodeToString(sum[:])
}

// Sign 对 canonical 串做 RSA-SHA256 PKCS1v15 签名并 base64。
func Sign(key *rsa.PrivateKey, canonical string) (string, error) {
	sum := sha256.Sum256([]byte(canonical))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// envelope 是 Waffo 所有响应的外壳：{data:..., errors:[{message, layer}]}。
type envelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Layer   string `json:"layer"`
	} `json:"errors"`
}

// post 签名并发送一个 POST，把 data 解进 out。
func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ts := c.now().Unix()
	sig, err := Sign(c.key, CanonicalRequest(http.MethodPost, path, ts, raw))
	if err != nil {
		return ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Merchant-Id", c.merchantID)
	req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Signature", sig)
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ErrUnavailable
	}
	if resp.StatusCode >= 500 {
		return ErrUnavailable
	}
	var env envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		if resp.StatusCode >= 400 {
			return &APIError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		}
		return ErrUnavailable
	}
	if resp.StatusCode >= 400 || len(env.Errors) > 0 {
		msg := http.StatusText(resp.StatusCode)
		if len(env.Errors) > 0 && env.Errors[0].Message != "" {
			msg = env.Errors[0].Message
		}
		status := resp.StatusCode
		if status < 400 {
			status = http.StatusBadRequest
		}
		return &APIError{Status: status, Message: msg}
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return ErrUnavailable
	}
	return nil
}

// ---- 结账会话 ----------------------------------------------------------------

// CheckoutSessionRequest 是 POST /v1/actions/checkout/create-session 的入参。
// 指针 / omitempty 字段留空即不发送，让 Waffo 用默认值。
type CheckoutSessionRequest struct {
	ProductID               string            `json:"productId"`
	Currency                string            `json:"currency"`
	BuyerEmail              string            `json:"buyerEmail,omitempty"`
	SuccessURL              string            `json:"successUrl,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
	OrderMerchantExternalID string            `json:"orderMerchantExternalId,omitempty"`
	Language                string            `json:"language,omitempty"`
	ExpiresInSeconds        int               `json:"expiresInSeconds,omitempty"`
}

// CheckoutSession 是出参 data。
type CheckoutSession struct {
	SessionID   string `json:"sessionId"`
	CheckoutURL string `json:"checkoutUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

// CreateCheckoutSession 建一个结账会话，返回托管收银台链接。
func (c *Client) CreateCheckoutSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSession, error) {
	var out CheckoutSession
	if err := c.post(ctx, "/v1/actions/checkout/create-session", req, &out); err != nil {
		return nil, err
	}
	if out.CheckoutURL == "" {
		return nil, ErrUnavailable
	}
	return &out, nil
}

// ---- 商品 --------------------------------------------------------------------

// Price 是一个币种的定价。Amount 是展示格式字符串（"4.99"），不是 minor。
type Price struct {
	Amount      string `json:"amount"`
	TaxIncluded bool   `json:"taxIncluded"`
	TaxCategory string `json:"taxCategory"`
}

// OnetimeProductRequest 是一次性商品入参。
type OnetimeProductRequest struct {
	StoreID     string            `json:"storeId"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Prices      map[string]Price  `json:"prices"`
	SuccessURL  string            `json:"successUrl,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// SubscriptionProductRequest 是订阅商品入参。BillingPeriod: weekly|monthly|quarterly|yearly。
type SubscriptionProductRequest struct {
	StoreID       string            `json:"storeId"`
	Name          string            `json:"name"`
	BillingPeriod string            `json:"billingPeriod"`
	Prices        map[string]Price  `json:"prices"`
	Description   string            `json:"description,omitempty"`
	SuccessURL    string            `json:"successUrl,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Product 是商品出参（data.product 的子集）。
type Product struct {
	ID      string `json:"id"`
	StoreID string `json:"storeId"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}

type productEnvelope struct {
	Product Product `json:"product"`
}

// CreateOnetimeProduct 建一次性商品。
func (c *Client) CreateOnetimeProduct(ctx context.Context, req OnetimeProductRequest) (*Product, error) {
	var out productEnvelope
	if err := c.post(ctx, "/v1/actions/onetime-product/create-product", req, &out); err != nil {
		return nil, err
	}
	if out.Product.ID == "" {
		return nil, ErrUnavailable
	}
	return &out.Product, nil
}

// CreateSubscriptionProduct 建订阅商品。
func (c *Client) CreateSubscriptionProduct(ctx context.Context, req SubscriptionProductRequest) (*Product, error) {
	var out productEnvelope
	if err := c.post(ctx, "/v1/actions/subscription-product/create-product", req, &out); err != nil {
		return nil, err
	}
	if out.Product.ID == "" {
		return nil, ErrUnavailable
	}
	return &out.Product, nil
}

// PublishProduct 把商品从测试环境发布到生产（一次性、单向）。
// subscription 为真走订阅商品那条路径。
func (c *Client) PublishProduct(ctx context.Context, id string, subscription bool) (*Product, error) {
	path := "/v1/actions/onetime-product/publish-product"
	if subscription {
		path = "/v1/actions/subscription-product/publish-product"
	}
	var out productEnvelope
	if err := c.post(ctx, path, map[string]string{"id": id}, &out); err != nil {
		return nil, err
	}
	return &out.Product, nil
}

// IsAlreadyPublished 判断 PublishProduct 的错误是否只是「已在生产 / 没有测试版可发」——
// 用生产 API Key 直接建的商品本来就在生产，这两种 400 对播种脚本都是空操作，不是失败。
func IsAlreadyPublished(err error) bool {
	e, ok := IsAPIError(err)
	if !ok || e.Status != http.StatusBadRequest {
		return false
	}
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "already published") || strings.Contains(m, "no test version")
}

// ---- 订阅 --------------------------------------------------------------------

// CancelResult 是取消订阅的出参。Status: canceling（期末生效）| canceled（立即）。
type CancelResult struct {
	OrderID string `json:"orderId"`
	Status  string `json:"status"`
}

// CancelSubscription 以商户身份取消订阅：active → canceling，本期末真正结束。
func (c *Client) CancelSubscription(ctx context.Context, orderID string) (*CancelResult, error) {
	var out CancelResult
	if err := c.post(ctx, "/v1/actions/subscription-order/cancel-order", map[string]string{"orderId": orderID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- 金额 --------------------------------------------------------------------

// zeroDecimalCurrencies 是 ISO 4217 里没有小数位的币种（Waffo 当前支持的只有 JPY）。
var zeroDecimalCurrencies = map[string]bool{"JPY": true, "KRW": true, "VND": true}

// MinorUnits 返回币种的小数位数。
func MinorUnits(currency string) int {
	if zeroDecimalCurrencies[strings.ToUpper(currency)] {
		return 0
	}
	return 2
}

// ParseAmountMinor 把 Waffo 的展示格式金额（"29.00" / "1000"）转成 minor 单位整数。
// 不用浮点：按字符串切小数点，多出的小数位直接拒绝而不是四舍五入。
func ParseAmountMinor(display, currency string) (int64, error) {
	s := strings.TrimSpace(display)
	if s == "" {
		return 0, errors.New("waffo: 金额为空")
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" {
		whole = "0"
	}
	digits := MinorUnits(currency)
	if len(frac) > digits {
		// 允许尾随零（"29.000"），不允许真正的多余精度。
		if strings.TrimRight(frac[digits:], "0") != "" {
			return 0, fmt.Errorf("waffo: 金额 %q 的小数位超过 %s 的 %d 位", display, currency, digits)
		}
		frac = frac[:digits]
	}
	for len(frac) < digits {
		frac += "0"
	}
	for _, ch := range whole + frac {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("waffo: 金额 %q 不是数字", display)
		}
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, err
	}
	if neg {
		n = -n
	}
	return n, nil
}

// FormatAmount 把 minor 整数格式化成 Waffo 要的展示字符串（499 → "4.99"，JPY 1000 → "1000"）。
func FormatAmount(minor int64, currency string) string {
	digits := MinorUnits(currency)
	if digits == 0 {
		return strconv.FormatInt(minor, 10)
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := strconv.FormatInt(minor, 10)
	for len(s) <= digits {
		s = "0" + s
	}
	out := s[:len(s)-digits] + "." + s[len(s)-digits:]
	if neg {
		out = "-" + out
	}
	return out
}
