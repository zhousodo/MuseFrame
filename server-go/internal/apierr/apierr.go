// 统一错误信封。形状逐字对应原实现 index.js 的 catch 分支：
//
//	{"error":{"code":"...","message":"...","requestId":"req_xxxxxxxx","details":{}}}
//
// details 在原实现里是 `e.details`（未设置时为 undefined → JSON 中该键消失），
// 所以这里用 `omitempty` + 指针语义，不能写成恒为 {} 的空对象。
package apierr

import "fmt"

// 错误码全集。常量化的意义：漏写一个码在编译期就能发现，而不是在回归比对时才发现。
//
// ⚠️ 契约 11 号文件第三章写的是「共 26 个」，但它自己列出的名字**只有 25 个**。
// 这里按**实际枚举出的 25 个**落地，差异已在 45 号报告里标出。
const (
	CodeValidation             = "VALIDATION"
	CodeAuthRequired           = "AUTH_REQUIRED"
	CodeAuthInvalid            = "AUTH_INVALID"
	CodeNotFound               = "NOT_FOUND"
	CodeStyleUnavailable       = "STYLE_UNAVAILABLE"
	CodeAssetNotReady          = "ASSET_NOT_READY"
	CodeAssetUnsupported       = "ASSET_UNSUPPORTED"
	CodeStorageQuotaExceeded   = "STORAGE_QUOTA_EXCEEDED"
	CodeInsufficientEntitle    = "INSUFFICIENT_ENTITLEMENT"
	CodeIdempotencyKeyRequired = "IDEMPOTENCY_KEY_REQUIRED"
	CodeIdempotencyConflict    = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyMismatch    = "IDEMPOTENCY_MISMATCH"
	CodeAmbiguous              = "AMBIGUOUS"
	CodeRateLimited            = "RATE_LIMITED"
	CodeCodeInvalid            = "CODE_INVALID"
	CodeCodeExpired            = "CODE_EXPIRED"
	CodeCodeLocked             = "CODE_LOCKED"
	CodePurchaseInvalid        = "PURCHASE_INVALID"
	CodePurchaseAlreadyClaimed = "PURCHASE_ALREADY_CLAIMED"
	CodeProviderNotConfigured  = "PROVIDER_NOT_CONFIGURED"
	CodeVerificationUnavail    = "VERIFICATION_UNAVAILABLE"
	CodeGenerationUnavailable  = "GENERATION_UNAVAILABLE"
	CodeEmailSendFailed        = "EMAIL_SEND_FAILED"
	CodeSMTPNotConfigured      = "SMTP_NOT_CONFIGURED"
	CodeInternal               = "INTERNAL_ERROR"
)

// Error 对应原实现的 ApiError。
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// New 构造一个带状态码的 API 错误。
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WithDetails 构造带 details 的 API 错误（details 会原样进响应体）。
func WithDetails(status int, code, message string, details map[string]any) *Error {
	return &Error{Status: status, Code: code, Message: message, Details: details}
}

// Envelope 是写出去的 JSON 形状；字段顺序即 JSON 键序，必须与原实现一致。
type Envelope struct {
	Error EnvelopeBody `json:"error"`
}

// EnvelopeBody 的键序：code, message, requestId, details。
type EnvelopeBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId"`
	Details   map[string]any `json:"details,omitempty"`
}
