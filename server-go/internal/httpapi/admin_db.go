package httpapi

import (
	"regexp"
	"strconv"
	"strings"

	"museframe-api/internal/apierr"
	"museframe-api/internal/cfgstore"
	"museframe-api/internal/store"
)

func (a *App) hAdminStatsDaily(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	days := clampLimit(c.URL.Query().Get("days"), 30, 90)
	rows, err := store.GetDailyStats(c.R.Context(), a.st.Q(), a.now(), days)
	if err != nil {
		return nil, err
	}
	return map[string]any{"days": rows}, nil
}

func (a *App) hAdminStatsStyles(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.GetStyleStats(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{"styles": rows}, nil
}

// hAdminDBTables 列出**白名单**表与行数。
func (a *App) hAdminDBTables(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	rows, err := store.ListBrowsableTables(c.R.Context(), a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{"tables": rows}, nil
}

// hAdminDBTable 浏览单表。
//
// 🔴 安全问题 2 的修复面：白名单之外的表（server_secrets）一律 404；
// 白名单内的表逐单元格过显式的列级脱敏清单（2026-09-12 起那张清单只挡凭据与密钥，
// 邮箱 / 交易号 / 设备与 IP 哈希一律完整 —— 见 store.redactColumns）。
func (a *App) hAdminDBTable(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	name := c.Params[0]
	if !store.IsBrowsableTable(name) {
		return nil, notFound("Unknown table.")
	}
	limit := clampLimit(c.URL.Query().Get("limit"), 50, 200)
	offset, err := strconv.Atoi(strings.TrimSpace(c.URL.Query().Get("offset")))
	if err != nil || offset < 0 {
		offset = 0
	}
	page, err := store.ReadTablePage(c.R.Context(), a.st.Q(), name, limit, offset, cfgstore.IsSecret)
	if err != nil {
		return nil, err
	}
	return page, nil
}

var writeVerbRe = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|create|attach|vacuum|reindex|replace|truncate|grant|revoke|copy|call|do)\b`)
var recursiveRe = regexp.MustCompile(`(?i)\brecursive\b`)
var selectStartRe = regexp.MustCompile(`(?i)^(select|with)\b`)

// hAdminDBQuery 是只读 SQL 控制台。
//
// 四道闸：必须以 select/with 开头 / 只允许单条语句 / 禁写动词与 recursive /
// 拒绝表清单（在 Node 版四张表的基础上补上 server_secrets 与 email_codes）。
// 再加两道 PG 侧保险：连接角色只读 + SET TRANSACTION READ ONLY。
func (a *App) hAdminDBQuery(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	sql := trimSpace(str(c.Body["sql"]))
	if !selectStartRe.MatchString(sql) {
		return nil, apierr.New(422, apierr.CodeValidation, "Only SELECT/WITH queries are allowed.")
	}
	withoutSemi := strings.TrimRight(sql, "; \t\r\n")
	if strings.Contains(withoutSemi, ";") {
		return nil, apierr.New(422, apierr.CodeValidation, "Only a single statement is allowed.")
	}
	if writeVerbRe.MatchString(withoutSemi) {
		return nil, apierr.New(422, apierr.CodeValidation, "Read-only queries only.")
	}
	// 无界递归 CTE = 单线程 DoS。
	if recursiveRe.MatchString(withoutSemi) {
		return nil, apierr.New(422, apierr.CodeValidation, "Recursive queries are not allowed here.")
	}
	// 凭据 / 密钥表对自由控制台一律关门：SELECT 可以靠别名绕开列级脱敏。
	if store.QueryTouchesDeniedTable(withoutSemi) {
		return nil, apierr.New(422, apierr.CodeValidation, "This table is not queryable from the console.")
	}
	res, err := a.st.RunReadOnlyQuery(c.R.Context(), withoutSemi)
	if err != nil {
		// 不回传数据库原始错误文本（可能含表结构细节）。
		return nil, apierr.New(422, apierr.CodeValidation, "Query failed.")
	}
	return res, nil
}
