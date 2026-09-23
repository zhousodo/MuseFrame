package apierr

import "testing"

// 错误码全集：按源码抛点穷举原是 25 个（契约写「26 个」但只列了 25 个名字）；
// 2026-09-23 新增 PAYMENTS_NOT_READY 后为 26 个；同日价目表 v2 新增 TRIAL_ALREADY_USED 后为 27 个。
func TestErrorCodeSet(t *testing.T) {
	codes := []string{
		CodeValidation, CodeAuthRequired, CodeAuthInvalid, CodeNotFound, CodeStyleUnavailable,
		CodeAssetNotReady, CodeAssetUnsupported, CodeStorageQuotaExceeded, CodeInsufficientEntitle,
		CodeIdempotencyKeyRequired, CodeIdempotencyConflict, CodeIdempotencyMismatch, CodeAmbiguous,
		CodeRateLimited, CodeCodeInvalid, CodeCodeExpired, CodeCodeLocked, CodePurchaseInvalid,
		CodePurchaseAlreadyClaimed, CodeProviderNotConfigured, CodeVerificationUnavail, CodePaymentsNotReady, CodeTrialAlreadyUsed,
		CodeGenerationUnavailable, CodeEmailSendFailed, CodeSMTPNotConfigured, CodeInternal,
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if c == "" {
			t.Fatal("错误码不得为空串")
		}
		if seen[c] {
			t.Fatalf("错误码重复：%s", c)
		}
		seen[c] = true
	}
	if len(seen) != 27 {
		t.Fatalf("错误码全集应为 27 个，实际 %d", len(seen))
	}
}

// details 未设置时该键必须从 JSON 里消失（不是空对象）。
func TestEnvelopeOmitsEmptyDetails(t *testing.T) {
	e := New(404, CodeNotFound, "Project not found.")
	if e.Details != nil {
		t.Fatal("未设置 details 时应为 nil")
	}
	d := WithDetails(402, CodeInsufficientEntitle, "x", map[string]any{"requiredUnits": 1})
	if d.Details["requiredUnits"] != 1 {
		t.Fatal("details 应原样带出")
	}
}
