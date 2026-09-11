package store

import (
	"context"
	"time"
)

const userCols = `id, status, is_guest, display_name, locale, timezone, created_at, updated_at, deleted_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Status, &u.IsGuest, &u.DisplayName, &u.Locale, &u.Timezone,
		&u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser 建一个用户行。
func CreateUser(ctx context.Context, q Queryer, id string, displayName *string, isGuest bool, locale string, t time.Time) error {
	if locale == "" {
		locale = "en"
	}
	_, err := q.Exec(ctx,
		`INSERT INTO users (id, is_guest, display_name, locale, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, isGuest, displayName, locale, t, t)
	return err
}

// GetUser 取一个未软删的用户。
func GetUser(ctx context.Context, q Queryer, id string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1 AND deleted_at IS NULL`, id))
}

// GetUserAny 取一个用户（含软删），仅管理后台与合并逻辑使用。
func GetUserAny(ctx context.Context, q Queryer, id string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

// CreateSession 写一条会话。令牌由调用方生成（randomBytes(24) 的 base64url，32 字符）。
func CreateSession(ctx context.Context, q Queryer, token, userID string, deviceID *string, t time.Time, ttlDays int) error {
	exp := t.Add(time.Duration(ttlDays) * 24 * time.Hour)
	_, err := q.Exec(ctx,
		`INSERT INTO sessions (token, user_id, device_id, created_at, last_seen_at, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		token, userID, deviceID, t, t, exp)
	return err
}

// GetSession 按令牌取会话。
func GetSession(ctx context.Context, q Queryer, token string) (*Session, error) {
	var s Session
	err := q.QueryRow(ctx,
		`SELECT token, user_id, device_id, created_at, last_seen_at, expires_at FROM sessions WHERE token = $1`, token).
		Scan(&s.Token, &s.UserID, &s.DeviceID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// TouchSession 刷新 last_seen_at 与 expires_at。
// 调用方只在距上次超过 1 小时才调 —— 否则轮询会把每次读变成一次写。
func TouchSession(ctx context.Context, q Queryer, token string, t time.Time, ttlDays int) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET last_seen_at = $1, expires_at = $2 WHERE token = $3`,
		t, t.Add(time.Duration(ttlDays)*24*time.Hour), token)
	return err
}

// DeleteSession 删一条会话（登出）。
func DeleteSession(ctx context.Context, q Queryer, token string) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

// GetIdentityUserID 按 (provider, subject) 找 user_id。
func GetIdentityUserID(ctx context.Context, q Queryer, provider, subject string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT user_id FROM auth_identities WHERE provider = $1 AND provider_subject = $2`,
		provider, subject).Scan(&id)
	return id, err
}

// UpdateIdentityEmail 刷新已存在身份的归一化邮箱。
func UpdateIdentityEmail(ctx context.Context, q Queryer, provider, subject, email string) error {
	_, err := q.Exec(ctx,
		`UPDATE auth_identities SET email_normalized = $1 WHERE provider = $2 AND provider_subject = $3`,
		email, provider, subject)
	return err
}

// InsertIdentity 写一条身份。(provider, provider_subject) 上的唯一约束是
// 「同一邮箱 / Google 账号必然解析到同一 user_id」的地基 —— 迁移无感靠的就是它。
func InsertIdentity(ctx context.Context, q Queryer, id, userID, provider, subject string, email *string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO auth_identities (id, user_id, provider, provider_subject, email_normalized, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`, id, userID, provider, subject, email, t)
	return err
}

// PromoteGuest 把游客行升格为正式账号（身份第一次出现时走这条）。
func PromoteGuest(ctx context.Context, q Queryer, userID string, name *string, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE users SET is_guest = false, display_name = $1, updated_at = $2 WHERE id = $3`,
		name, t, userID)
	return err
}

// MergeGuestInto 把在途游客账号的内容与**已购买**额度桶搬到目标账号。
//
// 免费额度刻意不跟着走（spec §10.2），这也是 credit_ledger 的
// UNIQUE (user_id, reference_key) 在这里不会撞车的原因：只有 uuid 键的
// purchase/job 引用会被改指。游客行保留它的 free_grants 记账（这样这台设备
// 不能再领一次），但因为 deleted_at 非空，它的会话不再能认证。
func MergeGuestInto(ctx context.Context, q Queryer, targetUserID, guestID string, t time.Time) (int, error) {
	rows, err := q.Query(ctx, `SELECT id FROM credit_buckets WHERE user_id = $1 AND source_type <> 'free_grant'`, guestID)
	if err != nil {
		return 0, err
	}
	var bucketIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		bucketIDs = append(bucketIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, b := range bucketIDs {
		if _, err := q.Exec(ctx, `UPDATE credit_buckets SET user_id = $1 WHERE id = $2`, targetUserID, b); err != nil {
			return 0, err
		}
		if _, err := q.Exec(ctx,
			`UPDATE credit_ledger SET user_id = $1 WHERE balance_bucket_id = $2 AND user_id = $3`,
			targetUserID, b, guestID); err != nil {
			return 0, err
		}
	}
	for _, table := range []string{"projects", "assets", "generation_jobs", "purchases", "user_feedback", "events"} {
		if _, err := q.Exec(ctx, `UPDATE `+table+` SET user_id = $1 WHERE user_id = $2`, targetUserID, guestID); err != nil {
			return 0, err
		}
	}
	if _, err := q.Exec(ctx,
		`UPDATE users SET deleted_at = $1, status = 'merged', updated_at = $2 WHERE id = $3`, t, t, guestID); err != nil {
		return 0, err
	}
	return len(bucketIDs), nil
}
