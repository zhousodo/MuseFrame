package store

import "context"

// StyleStatRow 是 GET /v1/admin/stats/styles 的一行。
// 注意：这条没有日期分桶，是按风格的全生命周期聚合（D-17 结论）。
type StyleStatRow struct {
	InternalKey      string   `json:"internalKey"`
	Name             string   `json:"name"`
	Theme            string   `json:"theme"`
	Status           string   `json:"status"`
	Premium          bool     `json:"premium"`
	Jobs             int      `json:"jobs"`
	Succeeded        int      `json:"succeeded"`
	Failed           int      `json:"failed"`
	SuccessRate      *float64 `json:"successRate"`
	AvgSeconds       *int     `json:"avgSeconds"`
	Saves            int      `json:"saves"`
	FeedbackPositive int      `json:"feedbackPositive"`
	FeedbackNegative int      `json:"feedbackNegative"`
}

// GetStyleStats 按风格聚合。
// 唯一的时间运算是成功任务的平均耗时：SQLite 用 julianday 相减，
// PG 侧对应 EXTRACT(EPOCH FROM (finished_at - created_at))，两端同为 timestamptz，无时区风险。
func GetStyleStats(ctx context.Context, q Queryer) ([]StyleStatRow, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, s.internal_key, s.public_name, s.theme, s.status, s.premium,
		  count(j.id) AS jobs,
		  COALESCE(SUM(CASE WHEN j.status='succeeded' THEN 1 ELSE 0 END),0),
		  COALESCE(SUM(CASE WHEN j.status='failed' THEN 1 ELSE 0 END),0),
		  AVG(CASE WHEN j.status='succeeded' AND j.finished_at IS NOT NULL
		           THEN EXTRACT(EPOCH FROM (j.finished_at - j.created_at)) END)
		FROM styles s
		LEFT JOIN style_versions v ON v.style_id = s.id
		LEFT JOIN generation_jobs j ON j.style_version_id = v.id
		GROUP BY s.id, s.internal_key, s.public_name, s.theme, s.status, s.premium
		ORDER BY jobs DESC, s.public_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type tmp struct {
		id  string
		row StyleStatRow
	}
	var list []tmp
	for rows.Next() {
		var t tmp
		var avg *float64
		if err := rows.Scan(&t.id, &t.row.InternalKey, &t.row.Name, &t.row.Theme, &t.row.Status,
			&t.row.Premium, &t.row.Jobs, &t.row.Succeeded, &t.row.Failed, &avg); err != nil {
			return nil, err
		}
		if finished := t.row.Succeeded + t.row.Failed; finished > 0 {
			r := float64(t.row.Succeeded) / float64(finished)
			t.row.SuccessRate = &r
		}
		if avg != nil {
			v := int(*avg + 0.5)
			t.row.AvgSeconds = &v
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	saves, err := countBy(ctx, q, `
		SELECT s.id, count(*) FROM events e
		JOIN generation_candidates c ON c.id = (e.props ->> 'candidateId')
		JOIN generation_jobs j ON j.id = c.job_id
		JOIN style_versions v ON v.id = j.style_version_id
		JOIN styles s ON s.id = v.style_id
		WHERE e.name = 'result_saved' GROUP BY s.id`)
	if err != nil {
		return nil, err
	}
	pos, neg, err := countByRating(ctx, q)
	if err != nil {
		return nil, err
	}

	out := make([]StyleStatRow, 0, len(list))
	for _, t := range list {
		t.row.Saves = saves[t.id]
		t.row.FeedbackPositive = pos[t.id]
		t.row.FeedbackNegative = neg[t.id]
		out = append(out, t.row)
	}
	return out, nil
}

func countBy(ctx context.Context, q Queryer, sql string) (map[string]int, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func countByRating(ctx context.Context, q Queryer) (map[string]int, map[string]int, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, f.rating, count(*) FROM user_feedback f
		JOIN generation_candidates c ON c.id = f.candidate_id
		JOIN generation_jobs j ON j.id = c.job_id
		JOIN style_versions v ON v.id = j.style_version_id
		JOIN styles s ON s.id = v.style_id
		GROUP BY s.id, f.rating`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	pos, neg := map[string]int{}, map[string]int{}
	for rows.Next() {
		var id, rating string
		var n int
		if err := rows.Scan(&id, &rating, &n); err != nil {
			return nil, nil, err
		}
		switch rating {
		case "positive":
			pos[id] = n
		case "negative":
			neg[id] = n
		}
	}
	return pos, neg, rows.Err()
}
