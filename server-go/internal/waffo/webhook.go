package waffo

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Webhook（http 通道）：
//
//	X-Waffo-Signature: t=<毫秒时间戳>,v1=<base64 签名>
//	X-Waffo-Event:     <eventType>
//	签名输入 = "<t>.<原始请求体>"，RSA-SHA256 PKCS1v15，用 Dashboard 上的平台级
//	Webhook Public Key 核验（测试 / 生产各一把，按事件的 mode 选）。
//
// 🔴 时间窗是 45 分钟而不是 5 分钟：重试会原样重放最初的签名头（t 不会重打），
// 最后一次重试落在首投 31 分钟之后；收紧到 5 分钟等于把自己的故障恢复变成 401。
// 真正的重放保护是按载荷 id 去重（httpapi 侧的 webhook_events 表）。
const WebhookTolerance = 45 * time.Minute

// Webhook 校验的稳定错误。
var (
	ErrWebhookNoKey     = errors.New("WAFFO_WEBHOOK_KEY_MISSING")
	ErrWebhookBadHeader = errors.New("WAFFO_WEBHOOK_BAD_HEADER")
	ErrWebhookExpired   = errors.New("WAFFO_WEBHOOK_EXPIRED")
	ErrWebhookBadSig    = errors.New("WAFFO_WEBHOOK_BAD_SIGNATURE")
)

// ParsePublicKey 解析 PEM 公钥（PKIX "PUBLIC KEY"，也接受 PKCS1 "RSA PUBLIC KEY"）。
// 同样接受 `\n` 转义的单行环境变量值。
func ParsePublicKey(pemStr string) (*rsa.PublicKey, error) {
	s := strings.TrimSpace(pemStr)
	if s == "" {
		return nil, ErrWebhookNoKey
	}
	if !strings.Contains(s, "\n") && strings.Contains(s, `\n`) {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("waffo: 公钥不是合法 PEM")
	}
	if k, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return k, nil
	}
	anyKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("waffo: 公钥既不是 PKIX 也不是 PKCS1")
	}
	k, ok := anyKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("waffo: 公钥不是 RSA")
	}
	return k, nil
}

// ParseSignatureHeader 拆 `t=…,v1=…`。顺序无关，未知键忽略。
func ParseSignatureHeader(header string) (t int64, v1 []byte, err error) {
	var ts, sig string
	for _, pair := range strings.Split(header, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "t":
			ts = strings.TrimSpace(kv[1])
		case "v1":
			sig = strings.TrimSpace(kv[1])
		}
	}
	if ts == "" || sig == "" {
		return 0, nil, ErrWebhookBadHeader
	}
	t, err = strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0, nil, ErrWebhookBadHeader
	}
	v1, err = base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return 0, nil, ErrWebhookBadHeader
	}
	return t, v1, nil
}

// VerifyWebhook 核验一条 webhook。rawBody 必须是**未经任何解析 / 重排**的原始请求体。
func VerifyWebhook(rawBody []byte, sigHeader string, pub *rsa.PublicKey, now time.Time) error {
	if pub == nil {
		return ErrWebhookNoKey
	}
	t, sig, err := ParseSignatureHeader(sigHeader)
	if err != nil {
		return err
	}
	delta := now.UnixMilli() - t
	if delta < 0 {
		delta = -delta
	}
	if time.Duration(delta)*time.Millisecond > WebhookTolerance {
		return ErrWebhookExpired
	}
	input := make([]byte, 0, len(rawBody)+24)
	input = append(input, strconv.FormatInt(t, 10)...)
	input = append(input, '.')
	input = append(input, rawBody...)
	sum := sha256.Sum256(input)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return ErrWebhookBadSig
	}
	return nil
}

// SignWebhook 按 Waffo 的格式给一条载荷签名（只给测试用：生成合法的 X-Waffo-Signature）。
func SignWebhook(rawBody []byte, key *rsa.PrivateKey, at time.Time) (string, error) {
	t := at.UnixMilli()
	input := strconv.FormatInt(t, 10) + "." + string(rawBody)
	sig, err := Sign(key, input)
	if err != nil {
		return "", err
	}
	return "t=" + strconv.FormatInt(t, 10) + ",v1=" + sig, nil
}

// 事件类型全集（订阅了的每一种都要有分支，未知的也要 200）。
const (
	EventOrderCompleted           = "order.completed"
	EventSubscriptionActivated    = "subscription.activated"
	EventSubscriptionPaymentOK    = "subscription.payment_succeeded"
	EventSubscriptionRenewed      = "subscription.renewed"
	EventSubscriptionRecovered    = "subscription.recovered"
	EventSubscriptionPlanChanged  = "subscription.plan_changed"
	EventSubscriptionCanceling    = "subscription.canceling"
	EventSubscriptionUncanceled   = "subscription.uncanceled"
	EventSubscriptionCanceled     = "subscription.canceled"
	EventSubscriptionPastDue      = "subscription.past_due"
	EventRefundSucceeded          = "refund.succeeded"
	EventRefundFailed             = "refund.failed"
	EventSubscriptionPlanChangeSc = "subscription.plan_change_scheduled"
	EventSubscriptionPlanChangeFa = "subscription.plan_change_failed"
)

// Event 是 webhook 载荷外壳。
type Event struct {
	ID        string    `json:"id"`
	Timestamp string    `json:"timestamp"`
	EventType string    `json:"eventType"`
	EventID   string    `json:"eventId"`
	StoreID   string    `json:"storeId"`
	StoreName string    `json:"storeName"`
	Mode      string    `json:"mode"`
	Data      EventData `json:"data"`
}

// EventData 是我们用得到的 data 字段子集；未列出的键被忽略（原始载荷整条落库）。
//
// 金额一律是展示格式字符串（"29.00"），用 ParseAmountMinor 转 minor。
// currentPeriodStart / currentPeriodEnd 是 ISO 日期（"2026-05-10"），用 ParseDate。
type EventData struct {
	OrderID                 string            `json:"orderId"`
	OrderStatus             string            `json:"orderStatus"`
	BuyerEmail              string            `json:"buyerEmail"`
	Currency                string            `json:"currency"`
	OrderMetadata           map[string]string `json:"orderMetadata"`
	OrderMerchantExternalID string            `json:"orderMerchantExternalId"`
	ChargedAmount           string            `json:"chargedAmount"`
	RefundedAmount          string            `json:"refundedAmount"`
	Amount                  string            `json:"amount"`
	ProductName             string            `json:"productName"`
	PaymentID               string            `json:"paymentId"`
	PaymentStatus           string            `json:"paymentStatus"`
	PaymentDate             string            `json:"paymentDate"`
	PeriodNumber            int               `json:"periodNumber"`
	BillingPeriod           string            `json:"billingPeriod"`
	CurrentPeriodStart      string            `json:"currentPeriodStart"`
	CurrentPeriodEnd        string            `json:"currentPeriodEnd"`
	CanceledAt              string            `json:"canceledAt"`
	RefundStatus            string            `json:"refundStatus"`
	RefundReason            string            `json:"refundReason"`
}

// UnmarshalJSON 容忍 orderMetadata 里的非字符串值（我们只写字符串，但对方的
// 契约是 object，不能因为别处塞了个数字就让整条事件解析失败）。
func (d *EventData) UnmarshalJSON(b []byte) error {
	type plain EventData
	var aux struct {
		plain
		OrderMetadata map[string]any `json:"orderMetadata"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*d = EventData(aux.plain)
	if len(aux.OrderMetadata) > 0 {
		d.OrderMetadata = make(map[string]string, len(aux.OrderMetadata))
		for k, v := range aux.OrderMetadata {
			if s, ok := v.(string); ok {
				d.OrderMetadata[k] = s
			}
		}
	}
	return nil
}

// ParseEvent 解析载荷。id / eventType 缺失即拒绝：没有 id 无法去重。
func ParseEvent(raw []byte) (*Event, error) {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, err
	}
	if strings.TrimSpace(ev.ID) == "" || strings.TrimSpace(ev.EventType) == "" {
		return nil, errors.New("waffo: 事件缺少 id 或 eventType")
	}
	return &ev, nil
}

// ChargedMinor 返回本次事件实际收的钱（minor）。优先 chargedAmount，缺失时回落
// 到已弃用的 amount（文档：channel 没报金额时 chargedAmount 省略、amount 等于标价）。
func (d EventData) ChargedMinor() (int64, bool) {
	for _, s := range []string{d.ChargedAmount, d.Amount} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		n, err := ParseAmountMinor(s, d.Currency)
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}

// ParseDate 解析 ISO 日期（"2026-05-10"）或完整 RFC3339 时间戳。日期按 UTC 零点。
func ParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if len(s) == len("2006-01-02") {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, false
		}
		return t.UTC(), true
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
