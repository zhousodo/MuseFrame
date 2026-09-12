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

	// ---- 2026-09-12 第四轮：App↔后端↔后台全链路可见性 ----------------------
	// 审计判据：App 上报到后端的每一类数据，后台都要能看（列表 / 详情 / 搜索 / 导出）。
	// 这 8 条补的是四个黑洞（埋点 / 资产 / 反馈正文 / 发信记录）、一个纵向视图、
	// 三个写入口、一个自观测视图，以及六类数据的 CSV 导出。
	a.add(http.MethodGet, `/v1/admin/events`, a.hAdminEvents)          // 56
	a.add(http.MethodGet, `/v1/admin/assets`, a.hAdminAssets)          // 57
	a.add(http.MethodGet, `/v1/admin/user-detail`, a.hAdminUserDetail) // 58
	a.add(http.MethodGet, `/v1/admin/email-log`, a.hAdminEmailLog)     // 59
	a.add(http.MethodGet, `/v1/admin/api-health`, a.hAdminAPIHealth)   // 60
	// 🔴 这条必须排在 /v1/admin/assets/([\w-]+)/file（第 39 条）**之后**才安全吗？
	//    不需要 —— add() 把 pattern 锚成 ^…$，`/v1/admin/export/users.csv` 与
	//    任何 assets 路由都不可能同时匹配。顺序只影响 404 归因，不影响正确性。
	a.add(http.MethodGet, `/v1/admin/export/([a-z]+)\.csv`, a.hAdminExportCSV) // 61

	a.add(http.MethodPost, `/v1/admin/feedback/([\w-]+)/handled`, a.hAdminFeedbackHandled)    // 62
	a.add(http.MethodPost, `/v1/admin/jobs/([\w-]+)/retry`, a.hAdminJobRetry)                 // 63
	a.add(http.MethodPost, `/v1/admin/purchases/([\w-]+)/reverify`, a.hAdminPurchaseReverify) // 64
}
