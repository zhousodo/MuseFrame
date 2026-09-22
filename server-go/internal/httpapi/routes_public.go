package httpapi

import "net/http"

// registerPublicRoutes 注册 33 条公开路由：前 30 条顺序与 Node 版 api.js 的
// route() 调用顺序逐条一致（首个匹配即终结，顺序会影响 404 归因），
// 31–33 是 2026-09-23 Waffo 网页端结账新增的（追加在末尾，不影响前面的归因）。
func (a *App) registerPublicRoutes() {
	a.add(http.MethodPost, `/v1/auth/exchange`, a.hAuthExchange)                // 1
	a.add(http.MethodPost, `/v1/auth/email/request`, a.hEmailRequest)           // 2
	a.add(http.MethodPost, `/v1/auth/email/verify`, a.hEmailVerify)             // 3
	a.add(http.MethodGet, `/v1/auth/config`, a.hAuthConfig)                     // 4
	a.add(http.MethodGet, `/v1/discover`, a.hDiscover)                          // 5
	a.add(http.MethodGet, `/v1/styles`, a.hStyles)                              // 6
	a.add(http.MethodGet, `/v1/styles/([\w-]+)`, a.hStyleDetail)                // 7
	a.add(http.MethodPost, `/v1/assets/upload-intents`, a.hUploadIntent)        // 8
	a.add(http.MethodPut, `/v1/assets/([\w-]+)/upload`, a.hUpload)              // 9
	a.add(http.MethodPost, `/v1/assets/([\w-]+)/complete`, a.hComplete)         // 10
	a.add(http.MethodGet, `/v1/assets/([\w-]+)/analysis`, a.hAnalysis)          // 11
	a.add(http.MethodGet, `/v1/assets/img-token`, a.hAssetImgToken)             // 12
	a.add(http.MethodGet, `/v1/assets/([\w-]+)/file`, a.hAssetFile)             // 13
	a.add(http.MethodPost, `/v1/projects`, a.hCreateProject)                    // 14
	a.add(http.MethodGet, `/v1/projects`, a.hListProjects)                      // 15
	a.add(http.MethodGet, `/v1/projects/([\w-]+)`, a.hGetProject)               // 16
	a.add(http.MethodPatch, `/v1/projects/([\w-]+)`, a.hPatchProject)           // 17
	a.add(http.MethodDelete, `/v1/projects/([\w-]+)`, a.hDeleteProject)         // 18
	a.add(http.MethodPost, `/v1/generation-jobs`, a.hCreateJob)                 // 19
	a.add(http.MethodGet, `/v1/generation-jobs/([\w-]+)`, a.hGetJob)            // 20
	a.add(http.MethodPost, `/v1/generation-jobs/([\w-]+)/cancel`, a.hCancelJob) // 21
	a.add(http.MethodPost, `/v1/candidates/([\w-]+)/feedback`, a.hFeedback)     // 22
	a.add(http.MethodPost, `/v1/candidates/([\w-]+)/export`, a.hExport)         // 23
	a.add(http.MethodGet, `/v1/entitlements/me`, a.hEntitlements)               // 24
	a.add(http.MethodGet, `/v1/products`, a.hProducts)                          // 25
	a.add(http.MethodPost, `/v1/purchases/verify`, a.hVerifyPurchase)           // 26
	a.add(http.MethodGet, `/v1/purchases`, a.hListPurchases)                    // 27
	a.add(http.MethodPost, `/v1/events`, a.hEvents)                             // 28
	a.add(http.MethodDelete, `/v1/auth/session`, a.hLogout)                     // 29
	a.add(http.MethodGet, `/v1/health`, a.hHealth)                              // 30
	// Waffo Pancake 网页端结账（2026-09-23）。webhook 不鉴权、靠 RSA 验签；
	// 另两条要真实账号。见 public_waffo.go / webhook_waffo.go。
	a.add(http.MethodPost, `/v1/purchases/web/checkout`, a.hWebCheckout)                      // 31
	a.add(http.MethodPost, `/v1/purchases/web/subscription/cancel`, a.hWebSubscriptionCancel) // 32
	a.add(http.MethodPost, `/v1/webhooks/waffo`, a.hWaffoWebhook)                             // 33
	// 目标架构统一探针：/v1/ready 是**只读**探活（SELECT 1，绝不写库）。
	// 它不在公开契约里 —— 边缘代理不把它回源，公网打不到；
	// compose healthcheck 与运维用它，行为差异已在 README 登记。
	a.add(http.MethodGet, `/v1/ready`, a.hReady)
}
