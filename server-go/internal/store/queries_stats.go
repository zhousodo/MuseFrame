package store

import (
	"context"
	"time"
)

// DailyRow 是 GET /v1/admin/stats/daily 的一行，键序固定。
type DailyRow struct {
	Date             string `json:"date"`
	NewUsers         int    `json:"newUsers"`
	JobsCreated      int    `json:"jobsCreated"`
	JobsSucceeded    int    `json:"jobsSucceeded"`
	JobsFailed       int    `json:"jobsFailed"`
	UnitsCommitted   int    `json:"unitsCommitted"`
	RevenueMinor     int64  `json:"revenueMinor"`
	FeedbackPositive int    `json:"feedbackPositive"`
	FeedbackNegative int    `json:"feedbackNegative"`
}

func bucket(ctx context.Context, q Queryer, sql string, since time.Time) (map[string]int64, error) {
	rows, err := q.Query(ctx, sql, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var d time.Time
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d.Format("2006-01-02")] = n
	}
	return out, rows.Err()
}

// GetDailyStats 按 UTC 日聚合。缺数据的日子补 0 而不是跳过，date 升序。
//
// 🔴 日期分桶一律 UTC（D-17）。SQLite 侧是 date(created_at)，而 created_at 带 Z，
// SQLite 按 UTC 归一。PG 侧必须写成 (created_at AT TIME ZONE 'UTC')::date，
// 绝不能写 created_at::date —— 那会按会话 TimeZone 解释，中国区 session
// 整体偏 8 小时，把凌晨 0~8 点的数据算进前一天。
func GetDailyStats(ctx context.Context, q Queryer, now time.Time, days int) ([]DailyRow, error) {
	today := now.UTC()
	dates := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := time.Date(today.Year(), today.Month(), today.Day()-i, 0, 0, 0, 0, time.UTC)
		dates = append(dates, d.Format("2006-01-02"))
	}
	since, err := time.Parse("2006-01-02", dates[0])
	if err != nil {
		return nil, err
	}

	type spec struct {
		dst *map[string]int64
		sql string
	}
	var newUsers, jobsCreated, jobsSucceeded, jobsFailed, unitsCommitted, revenue, fbPos, fbNeg map[string]int64
	for _, s := range []spec{
		{&newUsers, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM users
		             WHERE (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		{&jobsCreated, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM generation_jobs
		                WHERE (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		{&jobsSucceeded, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM generation_jobs
		                  WHERE status='succeeded' AND (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		{&jobsFailed, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM generation_jobs
		               WHERE status='failed' AND (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		{&unitsCommitted, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM credit_ledger
		                   WHERE entry_type='commit' AND (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		// 🔴 收入按 purchased_at 分桶，不是 created_at。
		{&revenue, `SELECT (purchased_at AT TIME ZONE 'UTC')::date AS d, COALESCE(SUM(amount_minor),0) FROM purchases
		            WHERE status='verified' AND (purchased_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		{&fbPos, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM user_feedback
		          WHERE rating='positive' AND (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
		{&fbNeg, `SELECT (created_at AT TIME ZONE 'UTC')::date AS d, count(*) FROM user_feedback
		          WHERE rating='negative' AND (created_at AT TIME ZONE 'UTC')::date >= $1 GROUP BY d`},
	} {
		m, err := bucket(ctx, q, s.sql, since)
		if err != nil {
			return nil, err
		}
		*s.dst = m
	}

	out := make([]DailyRow, 0, len(dates))
	for _, d := range dates {
		out = append(out, DailyRow{
			Date: d, NewUsers: int(newUsers[d]), JobsCreated: int(jobsCreated[d]),
			JobsSucceeded: int(jobsSucceeded[d]), JobsFailed: int(jobsFailed[d]),
			UnitsCommitted: int(unitsCommitted[d]), RevenueMinor: revenue[d],
			FeedbackPositive: int(fbPos[d]), FeedbackNegative: int(fbNeg[d]),
		})
	}
	return out, nil
}
