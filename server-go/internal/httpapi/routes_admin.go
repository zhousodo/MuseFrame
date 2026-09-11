package httpapi

import "net/http"

// registerAdminRoutes 注册 25 条管理后台路由，全部 requireAdmin。
// 鉴权：只读 X-Admin-Token 请求头，常数时间比对，限流 120/min。
func (a *App) registerAdminRoutes() {
	a.add(http.MethodGet, `/v1/admin/overview`, a.hAdminOverview)                          // 31
	a.add(http.MethodGet, `/v1/admin/jobs`, a.hAdminJobs)                                  // 32
	a.add(http.MethodGet, `/v1/admin/feedback`, a.hAdminFeedback)                          // 33
	a.add(http.MethodGet, `/v1/admin/purchases`, a.hAdminPurchases)                        // 34
	a.add(http.MethodGet, `/v1/admin/users`, a.hAdminUsers)                                // 35
	a.add(http.MethodPost, `/v1/admin/users/grant`, a.hAdminGrant)                         // 36
	a.add(http.MethodPost, `/v1/admin/email/test`, a.hAdminEmailTest)                      // 37
	a.add(http.MethodGet, `/v1/admin/img-token`, a.hAdminImgToken)                         // 38
	a.add(http.MethodGet, `/v1/admin/assets/([\w-]+)/file`, a.hAdminAssetFile)             // 39
	a.add(http.MethodGet, `/v1/admin/stats/daily`, a.hAdminStatsDaily)                     // 40
	a.add(http.MethodGet, `/v1/admin/stats/styles`, a.hAdminStatsStyles)                   // 41
	a.add(http.MethodGet, `/v1/admin/db/tables`, a.hAdminDBTables)                         // 42
	a.add(http.MethodGet, `/v1/admin/db/table/([A-Za-z0-9_]+)`, a.hAdminDBTable)           // 43
	a.add(http.MethodPost, `/v1/admin/db/query`, a.hAdminDBQuery)                          // 44
	a.add(http.MethodGet, `/v1/admin/config`, a.hAdminConfigGet)                           // 45
	a.add(http.MethodPut, `/v1/admin/config`, a.hAdminConfigPut)                           // 46
	a.add(http.MethodGet, `/v1/admin/products-admin`, a.hAdminProducts)                    // 47
	a.add(http.MethodPatch, `/v1/admin/products-admin/([a-z0-9_]+)`, a.hAdminPatchProduct) // 48
	a.add(http.MethodGet, `/v1/admin/styles-admin`, a.hAdminStylesFull)                    // 49
	a.add(http.MethodPost, `/v1/admin/styles-admin/([\w-]+)/status`, a.hAdminStyleStatus)  // 50

	// ---- 2026-09-12 第三轮：产品特有可运营项 --------------------------------
	// 🔴 两条 users 路由不会互相遮：add 把 pattern 锚成 ^…$，所以
	//    `/v1/admin/users/([\w-]+)/status` 匹配不上 `/v1/admin/users/grant`。
	a.add(http.MethodPatch, `/v1/admin/styles-admin/([\w-]+)`, a.hAdminPatchStyle) // 51
	a.add(http.MethodPost, `/v1/admin/users/([\w-]+)/status`, a.hAdminUserStatus)  // 52
	a.add(http.MethodGet, `/v1/admin/user-facts`, a.hAdminUserFacts)               // 53
	a.add(http.MethodGet, `/v1/admin/audit`, a.hAdminAudit)                        // 54
	a.add(http.MethodGet, `/v1/admin/feedback-reasons`, a.hAdminReasonCodes)       // 55
}
