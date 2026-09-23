package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"museframe-api/internal/apierr"
	"museframe-api/internal/waffo"
)

// 店铺还在审核时 Waffo 回 403 "Store is not approved for production payments"。
// 这必须翻成一个前端认得的专用码（PAYMENTS_NOT_READY），而不是笼统的「平台拒绝」——
// 否则买家只看到一句「稍后再试」，分不清是通道没开还是自己操作有误。
func TestWaffoCallErrorMapsStoreNotApproved(t *testing.T) {
	a := &App{lg: newNopLogger()}
	cases := []struct {
		name   string
		err    error
		op     string
		status int
		code   string
	}{
		{"403 未放行", &waffo.APIError{Status: http.StatusForbidden, Message: "Store is not approved for production payments"}, "checkout", http.StatusServiceUnavailable, apierr.CodePaymentsNotReady},
		{"400 请求被拒", &waffo.APIError{Status: http.StatusBadRequest, Message: "bad"}, "checkout", http.StatusServiceUnavailable, apierr.CodeVerificationUnavail},
		{"不可达", waffo.ErrUnavailable, "checkout", http.StatusServiceUnavailable, apierr.CodeVerificationUnavail},
		{"未配置", waffo.ErrNotConfigured, "checkout", http.StatusNotImplemented, apierr.CodeProviderNotConfigured},
		{"取消 4xx", &waffo.APIError{Status: http.StatusForbidden, Message: "x"}, "cancel", 422, apierr.CodeValidation},
	}
	for _, tc := range cases {
		var e *apierr.Error
		if !errors.As(a.waffoCallError(tc.err, tc.op), &e) {
			t.Fatalf("%s: 应返回 *apierr.Error", tc.name)
		}
		if e.Status != tc.status || e.Code != tc.code {
			t.Fatalf("%s: got %d %s, want %d %s", tc.name, e.Status, e.Code, tc.status, tc.code)
		}
	}
}
