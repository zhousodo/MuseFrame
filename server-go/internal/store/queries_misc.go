package store

import (
	"context"
	"time"
)

// UpsertFeedback 一个 (user, candidate) 只保留一条评价：再评是替换不是叠加。
func UpsertFeedback(ctx context.Context, q Queryer, newID func() string, userID, candidateID, rating string, reasonCodes []byte, comment *string, t time.Time) error {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM user_feedback WHERE user_id = $1 AND candidate_id = $2`, userID, candidateID).Scan(&id)
	if err == nil {
		_, err = q.Exec(ctx,
			`UPDATE user_feedback SET rating=$1, reason_codes=$2, comment=$3, created_at=$4 WHERE id=$5`,
			rating, reasonCodes, comment, t, id)
		return err
	}
	if !IsNoRows(err) {
		return err
	}
	_, err = q.Exec(ctx,
		`INSERT INTO user_feedback (id, user_id, candidate_id, rating, reason_codes, comment, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`, newID(), userID, candidateID, rating, reasonCodes, comment, t)
	return err
}

// InsertEvent 写一条埋点。
func InsertEvent(ctx context.Context, q Queryer, id string, userID *string, name string, props []byte, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO events (id, user_id, name, props, occurred_at) VALUES ($1,$2,$3,$4,$5)`, id, userID, name, props, t)
	return err
}

// GetEmailCode 取一条验证码记录。
func GetEmailCode(ctx context.Context, q Queryer, email string) (*EmailCode, error) {
	var c EmailCode
	err := q.QueryRow(ctx,
		`SELECT email, code_hash, expires_at, attempts, created_at, issue_count, window_start
		 FROM email_codes WHERE email = $1`, email).
		Scan(&c.Email, &c.CodeHash, &c.ExpiresAt, &c.Attempts, &c.CreatedAt, &c.IssueCount, &c.WindowStart)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertEmailCode 写/覆盖一条验证码记录，并把本窗口的签发计数推进一格。
func UpsertEmailCode(ctx context.Context, q Queryer, email, codeHash string, expiresAt, t time.Time, issueCount int, windowStart time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO email_codes (email, code_hash, expires_at, attempts, created_at, issue_count, window_start)
		 VALUES ($1,$2,$3,0,$4,$5,$6)
		 ON CONFLICT (email) DO UPDATE SET code_hash=excluded.code_hash, expires_at=excluded.expires_at,
		   attempts=0, created_at=excluded.created_at, issue_count=$5, window_start=$6`,
		email, codeHash, expiresAt, t, issueCount, windowStart)
	return err
}

// BumpEmailCodeAttempts 猜错一次。
func BumpEmailCodeAttempts(ctx context.Context, q Queryer, email string) error {
	_, err := q.Exec(ctx, `UPDATE email_codes SET attempts = attempts + 1 WHERE email = $1`, email)
	return err
}

// DeleteEmailCode 验证成功后立刻删除（一次性）。
func DeleteEmailCode(ctx context.Context, q Queryer, email string) error {
	_, err := q.Exec(ctx, `DELETE FROM email_codes WHERE email = $1`, email)
	return err
}

// ServerSecret 读取（不存在则生成）一个服务端持久密钥。
// 这些值必须存活于重启之间、且绝不能是运维输入：图片令牌的 HMAC 密钥曾经
// 派生自 ADMIN_TOKEN，而 ADMIN_TOKEN 默认为空串 —— 等于一个谁都能算的 HMAC 密钥。
func ServerSecret(ctx context.Context, q Queryer, key string, gen func() string, t time.Time) (string, error) {
	var v string
	err := q.QueryRow(ctx, `SELECT value FROM server_secrets WHERE key = $1`, key).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !IsNoRows(err) {
		return "", err
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO server_secrets (key, value, created_at) VALUES ($1,$2,$3) ON CONFLICT (key) DO NOTHING`,
		key, gen(), t); err != nil {
		return "", err
	}
	err = q.QueryRow(ctx, `SELECT value FROM server_secrets WHERE key = $1`, key).Scan(&v)
	return v, err
}

// PruneResult 是一次保留期清理的四个删除计数。
type PruneResult struct {
	Events      int64 `json:"events"`
	Sessions    int64 `json:"sessions"`
	Idempotency int64 `json:"idempotency"`
	EmailCodes  int64 `json:"emailCodes"`
}

// PruneOldRows 保留期清理：events 90 天 / 过期会话 / 幂等记录 30 天 / 过期验证码 1 天。
// 与 Node 版 db.js:342 同语义；PG 侧不需要 PRAGMA optimize（autovacuum 负责）。
func PruneOldRows(ctx context.Context, q Queryer, now time.Time, eventDays, idempotencyDays int) (PruneResult, error) {
	var out PruneResult
	tag, err := q.Exec(ctx, `DELETE FROM events WHERE occurred_at < $1`, now.Add(-time.Duration(eventDays)*24*time.Hour))
	if err != nil {
		return out, err
	}
	out.Events = tag.RowsAffected()
	if tag, err = q.Exec(ctx, `DELETE FROM sessions WHERE expires_at IS NOT NULL AND expires_at < $1`, now); err != nil {
		return out, err
	}
	out.Sessions = tag.RowsAffected()
	if tag, err = q.Exec(ctx, `DELETE FROM idempotency_records WHERE created_at < $1`,
		now.Add(-time.Duration(idempotencyDays)*24*time.Hour)); err != nil {
		return out, err
	}
	out.Idempotency = tag.RowsAffected()
	if tag, err = q.Exec(ctx, `DELETE FROM email_codes WHERE expires_at < $1`, now.Add(-24*time.Hour)); err != nil {
		return out, err
	}
	out.EmailCodes = tag.RowsAffected()
	return out, nil
}
