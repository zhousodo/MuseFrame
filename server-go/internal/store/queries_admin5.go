package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ---- 反馈（全文 + 已处理标记） ---------------------------------------------

// AdminFeedbackFullRow 是 GET /v1/admin/feedback 的一行（2026-09-12 起）。
//
// 🔴 与旧的 AdminFeedbackRow 的差别就是这次审计的结论：
// 多了 ID（不给 id 就没法标已处理）、Comment（**用户写的正文**，
// 从建库起就在落库、后台从来没显示过）、User/Email（没有归属就无法回访）、
// CandidateID（运营要看那张图）、HandledAt/HandledNote。
// 旧结构体仍然保留给 CSV 之外的老调用方 —— 见 ListAdminFeedback。
type AdminFeedbackFullRow struct {
	ID          string          `json:"id"`
	Rating      string          `json:"rating"`
	ReasonCodes json.RawMessage `json:"reasonCodes"`
	Comment     *string         `json:"comment"`
	CreatedAt   string          `json:"createdAt"`
	Style       *string         `json:"style"`
	User        string          `json:"user"`
	Email       *string         `json:"email"`
	CandidateID *string         `json:"candidateId"`
	AssetID     *string         `json:"assetId"`
	HandledAt   *string         `json:"handledAt"`
	HandledNote *string         `json:"handledNote"`
}

// FeedbackFilter 是反馈列表的筛选条件。
type FeedbackFilter struct {
	Rating string
	// Handled: "" 不筛、"yes" 只看已处理、"no" 只看未处理。
	Handled string
	// Range 按提交时间筛（半开区间）。页面与 CSV 用同一个字段，口径才会一致。
	Range TimeRange
	Limit int
}

// ListAdminFeedbackFull 反馈列表（带正文与处理状态），倒序。
func ListAdminFeedbackFull(ctx context.Context, q Queryer, f FeedbackFilter) ([]AdminFeedbackFullRow, error) {
	sql := `
		SELECT f.id, f.rating, f.reason_codes, f.comment, f.created_at, s.public_name,
		       f.user_id,
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=f.user_id AND ai.email_normalized IS NOT NULL LIMIT 1),
		       f.candidate_id, c.asset_id, f.handled_at, f.handled_note
		FROM user_feedback f
		LEFT JOIN generation_candidates c ON c.id=f.candidate_id
		LEFT JOIN generation_jobs j ON j.id=c.job_id
		LEFT JOIN style_versions v ON v.id=j.style_version_id LEFT JOIN styles s ON s.id=v.style_id
		WHERE true`
	args := []any{}
	if f.Rating != "" {
		args = append(args, f.Rating)
		sql += ` AND f.rating = $` + itoa(len(args))
	}
	switch f.Handled {
	case "yes":
		sql += ` AND f.handled_at IS NOT NULL`
	case "no":
		sql += ` AND f.handled_at IS NULL`
	}
	sql = f.Range.apply("f.created_at", sql, &args)
	sql += ` ORDER BY f.created_at DESC, f.id ASC LIMIT ` + itoa(f.Limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminFeedbackFullRow{}
	for rows.Next() {
		var r AdminFeedbackFullRow
		var created time.Time
		var handled *time.Time
		var codes []byte
		if err := rows.Scan(&r.ID, &r.Rating, &codes, &r.Comment, &created, &r.Style,
			&r.User, &r.Email, &r.CandidateID, &r.AssetID, &handled, &r.HandledNote); err != nil {
			return nil, err
		}
		r.ReasonCodes = json.RawMessage(codes)
		if len(codes) == 0 {
			r.ReasonCodes = json.RawMessage(`[]`)
		}
		r.CreatedAt = ISO(created)
		if handled != nil {
			s := ISO(*handled)
			r.HandledAt = &s
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountFeedbackUnhandled 未处理反馈条数（页头的那个数字）。
func CountFeedbackUnhandled(ctx context.Context, q Queryer) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM user_feedback WHERE handled_at IS NULL`).Scan(&n)
	return n, err
}

// SetFeedbackHandled 标记/取消标记一条反馈。handled 为假时把 handled_at 与
// handled_note 一起清空。返回受影响行数 —— 0 表示 id 不存在。
//
// 🔴 取消标记必须把 note 一起清掉：留着上一次的备注会让下一个人看到
// 「未处理」却带着一条「已退款」的备注，而这两件事里只有一件是真的。
func SetFeedbackHandled(ctx context.Context, q Queryer, id string, handled bool, note *string, t time.Time) (int64, error) {
	if !handled {
		tag, err := q.Exec(ctx, `UPDATE user_feedback SET handled_at=NULL, handled_note=NULL WHERE id=$1`, id)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}
	tag, err := q.Exec(ctx,
		`UPDATE user_feedback SET handled_at=$1, handled_note=$2 WHERE id=$3`, t, note, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- 任务（筛选 + 上游摘要） -----------------------------------------------

// JobFilter 是任务列表的筛选条件。
type JobFilter struct {
	Status string
	// SinceHours > 0 表示只看最近这么多小时（相对窗，和 Range 可以叠加）。
	SinceHours int
	UserID     string
	// JobID 精确匹配一条任务（任务详情用）。
	//
	// 🔴 刻意复用列表查询而不是再写一条 SELECT：详情页显示的每一个字段
	// 都必须和列表里那一行**同源**，否则两处会慢慢漂成两套口径
	// （「列表说失败、详情说成功」这种 bug 没人能在评审里看出来）。
	JobID string
	// Range 按创建时间筛（半开区间的绝对时刻）。
	Range TimeRange
	Limit int
}

// AdminJobFullRow 是带上游摘要的任务行。
type AdminJobFullRow struct {
	AdminJobRow
	// ProjectID / StyleID / Controls / Output 是「这次到底按什么参数生成的」。
	// 失败任务的复现此前只能靠去数据库浏览器翻 generation_jobs 的 jsonb 列。
	ProjectID string          `json:"projectId"`
	StyleID   string          `json:"styleId"`
	Controls  json.RawMessage `json:"controls"`
	Output    json.RawMessage `json:"output"`
	// UpstreamSummary 是上游（图片服务商）返回的摘要。它来自 events 表里
	// 本任务对应的 provider.* 事件；没有就是 nil。
	UpstreamSummary json.RawMessage `json:"upstreamSummary"`
	ReservedUnits   int             `json:"reservedUnits"`
	ParentJobID     *string         `json:"parentJobId"`
	RetryCount      int             `json:"retryCount"`
}

// ListAdminJobsFiltered 任务列表，支持状态 / 时间窗 / 用户筛选。
func ListAdminJobsFiltered(ctx context.Context, q Queryer, f JobFilter, now time.Time) ([]AdminJobFullRow, error) {
	sql := `
		SELECT j.id, j.status, j.stage, j.error_code, j.attempt_count, j.cost_minor, j.created_at,
		       ` + adminJobSecondsExpr + `,
		       j.user_id,
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=j.user_id AND ai.email_normalized IS NOT NULL LIMIT 1),
		       s.public_name, j.source_asset_id,
		       (SELECT c.asset_id FROM generation_candidates c WHERE c.job_id=j.id ORDER BY c.candidate_index ASC, c.created_at ASC LIMIT 1),
		       j.project_id, s.id, j.controls, j.output,
		       (SELECT e.props FROM events e
		          WHERE e.name LIKE 'provider.%' AND e.props->>'jobId' = j.id
		          ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1),
		       j.reserved_units, j.parent_job_id,
		       (SELECT count(*) FROM generation_jobs r WHERE r.parent_job_id = j.id)
		FROM generation_jobs j
		JOIN style_versions v ON v.id=j.style_version_id JOIN styles s ON s.id=v.style_id
		WHERE true`
	args := []any{}
	if f.Status != "" {
		args = append(args, f.Status)
		sql += ` AND j.status = $` + itoa(len(args))
	}
	if f.SinceHours > 0 {
		args = append(args, now.Add(-time.Duration(f.SinceHours)*time.Hour))
		sql += ` AND j.created_at >= $` + itoa(len(args))
	}
	if f.UserID != "" {
		args = append(args, f.UserID+"%")
		sql += ` AND j.user_id LIKE $` + itoa(len(args)) + ` ESCAPE '\'`
	}
	if f.JobID != "" {
		args = append(args, f.JobID)
		sql += ` AND j.id = $` + itoa(len(args))
	}
	sql = f.Range.apply("j.created_at", sql, &args)
	sql += ` ORDER BY j.created_at DESC, j.id ASC LIMIT ` + itoa(f.Limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminJobFullRow{}
	for rows.Next() {
		var r AdminJobFullRow
		var created time.Time
		var controls, output, upstream []byte
		if err := rows.Scan(&r.ID, &r.Status, &r.Stage, &r.ErrorCode, &r.AttemptCount, &r.CostMinor,
			&created, &r.Seconds, &r.User, &r.Email, &r.Style, &r.SourceAssetID, &r.CandidateAssetID,
			&r.ProjectID, &r.StyleID, &controls, &output, &upstream,
			&r.ReservedUnits, &r.ParentJobID, &r.RetryCount); err != nil {
			return nil, err
		}
		r.CreatedAt = ISO(created)
		r.Controls, r.Output = json.RawMessage(controls), json.RawMessage(output)
		if len(upstream) > 0 {
			r.UpstreamSummary = json.RawMessage(upstream)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountJobsByStatus 任务状态分布（筛选条之上的计数，页头用）。
func CountJobsByStatus(ctx context.Context, q Queryer) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT status, count(*) FROM generation_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// ---- 购买（完整列 + 重验） -------------------------------------------------

// AdminPurchaseFullRow 是带平台与交易号的购买行。
type AdminPurchaseFullRow struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	// TxID 是 external_transaction_id。它是 Google/Apple 的订单号，
	// **不打码** —— 运营拿它去商店后台对账是这一列存在的全部理由。
	TxID        string  `json:"txId"`
	Status      string  `json:"status"`
	Product     string  `json:"product"`
	ProductKey  string  `json:"productKey"`
	AmountMinor *int64  `json:"amountMinor"`
	Currency    *string `json:"currency"`
	PurchasedAt string  `json:"purchasedAt"`
	ExpiresAt   *string `json:"expiresAt"`
	User        string  `json:"user"`
	UserID      string  `json:"userId"`
	Email       *string `json:"email"`
	// UnitsGranted 是这笔购买实际入账的额度。0 且 status=active 就是一笔
	// 「钱收了、额度没发」的事故 —— 这个数字是它唯一的可见处。
	UnitsGranted int `json:"unitsGranted"`
}

// PurchaseFilter 是购买列表的筛选条件。
type PurchaseFilter struct {
	Status   string
	Platform string
	// Range 按购买时间筛（半开区间）。
	Range TimeRange
	Limit int
}

// ListAdminPurchasesFull 购买列表（完整列），倒序。
func ListAdminPurchasesFull(ctx context.Context, q Queryer, f PurchaseFilter) ([]AdminPurchaseFullRow, error) {
	sql := `
		SELECT pu.id, pu.platform, pu.external_transaction_id, pu.status,
		       p.display_name, p.internal_key, pu.amount_minor, pu.currency,
		       -- user 与 userId 两列都回完整 id（2026-09-12 起 user 不再是 8 位前缀）。
		       pu.purchased_at, pu.expires_at, pu.user_id, pu.user_id,
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=pu.user_id AND ai.email_normalized IS NOT NULL LIMIT 1),
		       (SELECT COALESCE(sum(l.units),0) FROM credit_ledger l WHERE l.purchase_id = pu.id AND l.entry_type = 'grant')
		FROM purchases pu JOIN products p ON p.id=pu.product_id
		WHERE true`
	args := []any{}
	if f.Status != "" {
		args = append(args, f.Status)
		sql += ` AND pu.status = $` + itoa(len(args))
	}
	if f.Platform != "" {
		args = append(args, f.Platform)
		sql += ` AND pu.platform = $` + itoa(len(args))
	}
	sql = f.Range.apply("pu.purchased_at", sql, &args)
	sql += ` ORDER BY pu.purchased_at DESC, pu.id ASC LIMIT ` + itoa(f.Limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminPurchaseFullRow{}
	for rows.Next() {
		var r AdminPurchaseFullRow
		var purchased time.Time
		var expires *time.Time
		if err := rows.Scan(&r.ID, &r.Platform, &r.TxID, &r.Status, &r.Product, &r.ProductKey,
			&r.AmountMinor, &r.Currency, &purchased, &expires, &r.User, &r.UserID, &r.Email,
			&r.UnitsGranted); err != nil {
			return nil, err
		}
		r.PurchasedAt = ISO(purchased)
		if expires != nil {
			s := ISO(*expires)
			r.ExpiresAt = &s
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetPurchaseForReverify 取一笔购买的重验所需字段。
func GetPurchaseForReverify(ctx context.Context, q Queryer, id string) (userID, productID, platform, txID, status string, err error) {
	err = q.QueryRow(ctx, `
		SELECT user_id, product_id, platform, external_transaction_id, status
		FROM purchases WHERE id = $1`, id).Scan(&userID, &productID, &platform, &txID, &status)
	return
}

// ---- CSV 文本 --------------------------------------------------------------

// 🔴 2026-09-12 第七轮：这里曾有一个 CSVMaskEmail，把导出里的邮箱打成
// a***@example.com。它被删掉了，不是忘了接 —— 导出的唯一用途是把后台里看到的
// 那张表拿去做对账、群发、挨个联系用户，而打了码的邮箱做不了这三件事里的任何一件，
// 于是实际发生的事是有人绕过导出、直接去数据库抄。
// 邮箱现在走 CSVText（只负责压平换行），和昵称、反馈正文同一个口径。

// CSVText 把一个可能含换行/逗号/引号的自由文本压成 CSV 安全的单行。
//
// 🔴 encoding/csv 会正确地给含换行的字段加引号，Excel 也认。
// 但运营打开 CSV 的第二个工具永远是某个只按行切的脚本 ——
// 用户反馈正文里的一个回车就会把一行变成两行、把后面所有列错位。
// 所以换行在这里就换成空格，不依赖下游正确处理多行字段。
func CSVText(s *string) string {
	if s == nil {
		return ""
	}
	r := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")
	return strings.TrimSpace(r.Replace(*s))
}
