package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// AdminFeedbackRow 是 GET /v1/admin/feedback 的一行。
type AdminFeedbackRow struct {
	Rating      string          `json:"rating"`
	ReasonCodes json.RawMessage `json:"reason_codes"`
	CreatedAt   string          `json:"created_at"`
	Style       *string         `json:"style"`
}

// ListAdminFeedback 反馈列表，ORDER BY created_at DESC LIMIT 100（硬编码）。
func ListAdminFeedback(ctx context.Context, q Queryer) ([]AdminFeedbackRow, error) {
	rows, err := q.Query(ctx, `
		SELECT f.rating, f.reason_codes, f.created_at, s.public_name
		FROM user_feedback f
		LEFT JOIN generation_candidates c ON c.id=f.candidate_id
		LEFT JOIN generation_jobs j ON j.id=c.job_id
		LEFT JOIN style_versions v ON v.id=j.style_version_id LEFT JOIN styles s ON s.id=v.style_id
		ORDER BY f.created_at DESC, f.id ASC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminFeedbackRow{}
	for rows.Next() {
		var r AdminFeedbackRow
		var t time.Time
		var codes []byte
		if err := rows.Scan(&r.Rating, &codes, &t, &r.Style); err != nil {
			return nil, err
		}
		r.ReasonCodes = json.RawMessage(codes)
		r.CreatedAt = ISO(t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminPurchaseRow 是 GET /v1/admin/purchases 的一行。
type AdminPurchaseRow struct {
	AmountMinor *int64  `json:"amount_minor"`
	Currency    *string `json:"currency"`
	PurchasedAt string  `json:"purchased_at"`
	Status      string  `json:"status"`
	Product     string  `json:"product"`
	User        string  `json:"user"`
	Email       *string `json:"email"`
}

// ListAdminPurchases 订单列表，ORDER BY purchased_at DESC LIMIT 100（硬编码）。
func ListAdminPurchases(ctx context.Context, q Queryer) ([]AdminPurchaseRow, error) {
	rows, err := q.Query(ctx, `
		SELECT pu.amount_minor, pu.currency, pu.purchased_at, pu.status, p.display_name, substr(pu.user_id,1,8),
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=pu.user_id AND ai.email_normalized IS NOT NULL LIMIT 1)
		FROM purchases pu JOIN products p ON p.id=pu.product_id
		ORDER BY pu.purchased_at DESC, pu.id ASC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminPurchaseRow{}
	for rows.Next() {
		var r AdminPurchaseRow
		var t time.Time
		if err := rows.Scan(&r.AmountMinor, &r.Currency, &t, &r.Status, &r.Product, &r.User, &r.Email); err != nil {
			return nil, err
		}
		r.PurchasedAt = ISO(t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminUserRow 是 GET /v1/admin/users 的一行。id 只回前 8 位是脱敏契约。
type AdminUserRow struct {
	UserID      string  `json:"userId"`
	ID          string  `json:"id"`
	DisplayName *string `json:"displayName"`
	IsGuest     bool    `json:"isGuest"`
	CreatedAt   string  `json:"createdAt"`
	Email       *string `json:"email"`
	Providers   *string `json:"providers"`
	Units       int     `json:"units"`
	Jobs        int     `json:"jobs"`
}

// ListAdminUsers 用户列表。
// 🔴 LIKE 必须带 ESCAPE 转义：漏掉就是 SQL 通配注入（一个 % 能把全表拉出来）。
func ListAdminUsers(ctx context.Context, q Queryer, limit int, search string) ([]AdminUserRow, error) {
	base := `
		SELECT u.id, substr(u.id,1,8), u.display_name, u.is_guest, u.created_at,
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=u.id AND ai.email_normalized IS NOT NULL LIMIT 1),
		       (SELECT string_agg(DISTINCT ai.provider, ',') FROM auth_identities ai WHERE ai.user_id=u.id),
		       (SELECT COALESCE(SUM(l.units),0) FROM credit_ledger l WHERE l.user_id=u.id),
		       (SELECT count(*) FROM generation_jobs j WHERE j.user_id=u.id)
		FROM users u WHERE u.deleted_at IS NULL`
	var rows interface {
		Next() bool
		Scan(...any) error
		Close()
		Err() error
	}
	var err error
	if search != "" {
		esc := strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(search)
		like := "%" + esc + "%"
		rows, err = q.Query(ctx, base+`
		  AND (u.id LIKE $1 ESCAPE '\' OR lower(u.display_name) LIKE $1 ESCAPE '\'
		       OR EXISTS (SELECT 1 FROM auth_identities ai WHERE ai.user_id=u.id AND ai.email_normalized LIKE $1 ESCAPE '\'))
		  ORDER BY u.created_at DESC, u.id ASC LIMIT $2`, like, limit)
	} else {
		rows, err = q.Query(ctx, base+` ORDER BY u.created_at DESC, u.id ASC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminUserRow{}
	for rows.Next() {
		var r AdminUserRow
		var t time.Time
		if err := rows.Scan(&r.UserID, &r.ID, &r.DisplayName, &r.IsGuest, &t, &r.Email, &r.Providers, &r.Units, &r.Jobs); err != nil {
			return nil, err
		}
		r.CreatedAt = ISO(t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindUserByID 按完整 id 找未软删用户（手动充值用）。
func FindUserByID(ctx context.Context, q Queryer, id string) (string, bool, error) {
	var uid string
	var isGuest bool
	err := q.QueryRow(ctx, `SELECT id, is_guest FROM users WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&uid, &isGuest)
	return uid, isGuest, err
}

// FindUsersByEmail 按归一化邮箱找未软删用户（可能多个 -> 409 AMBIGUOUS）。
func FindUsersByEmail(ctx context.Context, q Queryer, email string) ([]string, []bool, error) {
	rows, err := q.Query(ctx,
		`SELECT DISTINCT u.id, u.is_guest FROM users u JOIN auth_identities ai ON ai.user_id = u.id
		 WHERE ai.email_normalized = $1 AND u.deleted_at IS NULL ORDER BY u.id`, email)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []string
	var guests []bool
	for rows.Next() {
		var id string
		var g bool
		if err := rows.Scan(&id, &g); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		guests = append(guests, g)
	}
	return ids, guests, rows.Err()
}

// GetManualGrantByKey 按幂等键取手动充值记录。
func GetManualGrantByKey(ctx context.Context, q Queryer, key string) (id, userID string, units int, err error) {
	err = q.QueryRow(ctx, `SELECT id, user_id, units FROM manual_grants WHERE idempotency_key = $1`, key).
		Scan(&id, &userID, &units)
	return
}

// InsertManualGrant 写一条手动充值审计行。
func InsertManualGrant(ctx context.Context, q Queryer, id, userID string, units int, note *string, expiresAt *time.Time, idemKey *string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO manual_grants (id, user_id, units, note, expires_at, idempotency_key, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, userID, units, note, expiresAt, idemKey, t)
	return err
}

// AdminStyleRow 是 GET /v1/admin/styles-admin 的一行（字段顺序与出参一致）。
type AdminStyleRow struct {
	ID          string
	InternalKey string
	Name        string
	Theme       string
	Status      string
	Premium     bool
	Jobs        int
}

// ListAdminStyles 风格管理列表（含未发布），ORDER BY public_name。
func ListAdminStyles(ctx context.Context, q Queryer) ([]AdminStyleRow, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, s.internal_key, s.public_name, s.theme, s.status, s.premium,
		  (SELECT count(*) FROM generation_jobs j JOIN style_versions v ON v.id = j.style_version_id
		   WHERE v.style_id = s.id)
		FROM styles s ORDER BY s.public_name ASC, s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminStyleRow{}
	for rows.Next() {
		var r AdminStyleRow
		if err := rows.Scan(&r.ID, &r.InternalKey, &r.Name, &r.Theme, &r.Status, &r.Premium, &r.Jobs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TodayCounts 是「最近 24 小时」的运营计数，给后台「应用配置 / 运行状态」页。
//
// 🔴 窗口是**滚动 24 小时**，不是自然日：与 AdminOverview.UsersToday 同一个判据
// （那一项也是 created_at >= now-24h）。两处用不同窗口的话，同一个面板上会出现
// 两个都叫「今日注册」但数字不一样的格子，而没人说得清哪个对。
type TodayCounts struct {
	Registrations int   `json:"registrations"`
	Jobs          int   `json:"jobs"`
	JobsSucceeded int   `json:"jobsSucceeded"`
	JobsFailed    int   `json:"jobsFailed"`
	Purchases     int   `json:"purchases"`
	RevenueMinor  int64 `json:"revenueMinor"`
}

// GetTodayCounts 统计滚动 24 小时窗口内的注册 / 生成 / 失败 / 购买。
//
// 🔴 一条 SQL 五个标量子查询，而不是五次往返：这个接口在后台页面加载时被调用，
// 而连接池只有 4 个槽（见 PoolStats 的说明）。
func GetTodayCounts(ctx context.Context, q Queryer, now time.Time) (TodayCounts, error) {
	since := now.Add(-24 * time.Hour)
	var t TodayCounts
	err := q.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM users             WHERE created_at >= $1),
		  (SELECT count(*) FROM generation_jobs   WHERE created_at >= $1),
		  (SELECT count(*) FROM generation_jobs   WHERE created_at >= $1 AND status = 'succeeded'),
		  (SELECT count(*) FROM generation_jobs   WHERE created_at >= $1 AND status = 'failed'),
		  (SELECT count(*) FROM purchases         WHERE created_at >= $1 AND status = 'verified'),
		  (SELECT COALESCE(SUM(amount_minor),0) FROM purchases WHERE created_at >= $1 AND status = 'verified')`,
		since).Scan(&t.Registrations, &t.Jobs, &t.JobsSucceeded, &t.JobsFailed, &t.Purchases, &t.RevenueMinor)
	if err != nil {
		return TodayCounts{}, err
	}
	return t, nil
}
