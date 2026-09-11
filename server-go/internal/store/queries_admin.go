package store

import (
	"context"
	"time"
)

// AdminOverview 是 GET /v1/admin/overview 的数值部分。
type AdminOverview struct {
	Users               int
	UsersToday          int
	JobsByStatus        map[string]int
	SucceededAvgSeconds *int
	UnitsGranted        int
	UnitsConsumed       int
	RevenueMinor        int64
	Purchases           []ProductCount
	FeedbackPositive    int
	FeedbackNegative    int
	TopStyles           []StyleCount
}

// ProductCount 是「某商品卖了几单」。
type ProductCount struct {
	Product string `json:"product"`
	N       int    `json:"n"`
}

// StyleCount 是「某风格跑了几个任务」。
type StyleCount struct {
	Name string `json:"name"`
	Jobs int    `json:"jobs"`
}

// GetAdminOverview 汇总总览指标。
func GetAdminOverview(ctx context.Context, q Queryer, now time.Time) (*AdminOverview, error) {
	o := &AdminOverview{JobsByStatus: map[string]int{}, Purchases: []ProductCount{}, TopStyles: []StyleCount{}}
	rows, err := q.Query(ctx, `SELECT status, count(*) FROM generation_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		o.JobsByStatus[s] = n
	}
	rows.Close()

	var avg *float64
	if err := q.QueryRow(ctx,
		`SELECT AVG(EXTRACT(EPOCH FROM (finished_at - created_at)))
		 FROM generation_jobs WHERE status='succeeded' AND finished_at IS NOT NULL`).Scan(&avg); err != nil {
		return nil, err
	}
	if avg != nil {
		v := int(*avg + 0.5)
		o.SucceededAvgSeconds = &v
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&o.Users); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM users WHERE created_at >= $1`, now.Add(-24*time.Hour)).Scan(&o.UsersToday); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(units),0) FROM credit_ledger WHERE entry_type='grant'`).Scan(&o.UnitsGranted); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM credit_ledger WHERE entry_type='commit'`).Scan(&o.UnitsConsumed); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM purchases WHERE status='verified'`).Scan(&o.RevenueMinor); err != nil {
		return nil, err
	}
	prows, err := q.Query(ctx,
		`SELECT p.display_name, count(*) FROM purchases pu JOIN products p ON p.id=pu.product_id
		 WHERE pu.status='verified' GROUP BY p.id, p.display_name ORDER BY p.display_name`)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var pc ProductCount
		if err := prows.Scan(&pc.Product, &pc.N); err != nil {
			prows.Close()
			return nil, err
		}
		o.Purchases = append(o.Purchases, pc)
	}
	prows.Close()

	if err := q.QueryRow(ctx, `SELECT count(*) FROM user_feedback WHERE rating='positive'`).Scan(&o.FeedbackPositive); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM user_feedback WHERE rating='negative'`).Scan(&o.FeedbackNegative); err != nil {
		return nil, err
	}
	srows, err := q.Query(ctx,
		`SELECT s.public_name, count(*) AS jobs FROM generation_jobs j
		 JOIN style_versions v ON v.id=j.style_version_id JOIN styles s ON s.id=v.style_id
		 GROUP BY s.id, s.public_name ORDER BY jobs DESC, s.public_name ASC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var sc StyleCount
		if err := srows.Scan(&sc.Name, &sc.Jobs); err != nil {
			srows.Close()
			return nil, err
		}
		o.TopStyles = append(o.TopStyles, sc)
	}
	srows.Close()
	return o, nil
}

// AdminJobRow 是 GET /v1/admin/jobs 的一行（列名与 Node 版逐字一致）。
type AdminJobRow struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	Stage            string  `json:"stage"`
	ErrorCode        *string `json:"error_code"`
	AttemptCount     int     `json:"attempt_count"`
	CostMinor        int64   `json:"cost_minor"`
	CreatedAt        string  `json:"created_at"`
	Seconds          *int    `json:"seconds"`
	User             string  `json:"user"`
	Email            *string `json:"email"`
	Style            string  `json:"style"`
	SourceAssetID    string  `json:"sourceAssetId"`
	CandidateAssetID *string `json:"candidateAssetId"`
}

// ListAdminJobs 任务列表，ORDER BY created_at DESC。
func ListAdminJobs(ctx context.Context, q Queryer, limit int) ([]AdminJobRow, error) {
	rows, err := q.Query(ctx, `
		SELECT j.id, j.status, j.stage, j.error_code, j.attempt_count, j.cost_minor, j.created_at,
		       CAST(EXTRACT(EPOCH FROM (COALESCE(j.finished_at, j.updated_at) - j.created_at)) AS integer),
		       substr(j.user_id,1,8),
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=j.user_id AND ai.email_normalized IS NOT NULL LIMIT 1),
		       s.public_name, j.source_asset_id,
		       (SELECT c.asset_id FROM generation_candidates c WHERE c.job_id=j.id LIMIT 1)
		FROM generation_jobs j
		JOIN style_versions v ON v.id=j.style_version_id JOIN styles s ON s.id=v.style_id
		ORDER BY j.created_at DESC, j.id ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminJobRow{}
	for rows.Next() {
		var r AdminJobRow
		var created time.Time
		if err := rows.Scan(&r.ID, &r.Status, &r.Stage, &r.ErrorCode, &r.AttemptCount, &r.CostMinor,
			&created, &r.Seconds, &r.User, &r.Email, &r.Style, &r.SourceAssetID, &r.CandidateAssetID); err != nil {
			return nil, err
		}
		r.CreatedAt = ISO(created)
		out = append(out, r)
	}
	return out, rows.Err()
}
