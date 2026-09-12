// 后台「全链路可见性」补齐：App 上报的每一类数据都要有一个后台读法。
//
// 这一批补的是 2026-09-12 审计出来的四个**黑洞** —— 数据在写、后台看不见：
//
//	events      App 有 16 个埋点在打 POST /v1/events，后台只有一个数据库浏览器
//	assets      App 上传的每张源图 + 每张成品都在 assets 表，后台只能按 id 取文件
//	feedback    user_feedback.comment 从建库起就在写，GET /v1/admin/feedback 没 SELECT 它
//	email       验证码发信只有进程内的 LastSend（重启即失忆）
//
// 外加一个纵向视图：按用户把额度账本 / 资产 / 任务 / 购买 / 会话 / 反馈拉到一起。
package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ---- 埋点 events -----------------------------------------------------------

// notUserEventClause 是「只留真正的用户埋点」那条 WHERE 片段。
//
// 🔴 events 这张表是三种东西的合流：用户埋点（App 的 track()）、后台操作审计
// （admin.* —— 复用同表换来的索引/备份/可查）、以及发信记录（email.send，同理）。
// 埋点视图的全部用处是判断 App 功能有没有人用，所以后两者必须排掉 ——
// 不排的话运营自己改一次配置、系统发一封验证码，都会在「用户行为」里多一行。
//
// 它占两个占位符：$n = 审计前缀的 LIKE 模式，$n+1 = 发信记录的事件名。
// 用 itoa 拼的只是**占位符序号**，值一律走参数。
func notUserEventClause(firstArg int) string {
	return ` AND name NOT LIKE $` + itoa(firstArg) + ` ESCAPE '\' AND name <> $` + itoa(firstArg+1)
}

// notUserEventArgs 是上面那条片段要的两个参数，次序固定。
func notUserEventArgs() []any { return []any{AuditPrefix + `%`, EmailSendKind} }

// EventNameRow 是埋点按事件名的聚合。
type EventNameRow struct {
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Users  int    `json:"users"`
	LastAt string `json:"lastAt"`
	// FirstAt 和 LastAt 一起回答「这个埋点还在不在打」—— 一个只有历史、
	// 最近 7 天为 0 的事件名通常意味着对应的 App 版本已经没人用了。
	FirstAt string `json:"firstAt"`
}

// ListEventNames 按事件名聚合 events，窗口内倒序。
//
// 🔴 必须排除审计行。审计（admin.*）复用的是同一张 events 表，
// 不排掉的话「埋点」页会把运营自己的每一次点击算成用户行为 ——
// 而这张表正是用来判断 App 功能有没有人用的。
func ListEventNames(ctx context.Context, q Queryer, since time.Time) ([]EventNameRow, error) {
	rows, err := q.Query(ctx, `
		SELECT name, count(*), count(DISTINCT user_id), min(occurred_at), max(occurred_at)
		FROM events
		WHERE occurred_at >= $1`+notUserEventClause(2)+`
		GROUP BY name ORDER BY count(*) DESC, name ASC`,
		append([]any{since}, notUserEventArgs()...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventNameRow{}
	for rows.Next() {
		var r EventNameRow
		var first, last time.Time
		if err := rows.Scan(&r.Name, &r.Total, &r.Users, &first, &last); err != nil {
			return nil, err
		}
		r.FirstAt, r.LastAt = ISO(first), ISO(last)
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventDayRow 是埋点按天的聚合（某一天、某个事件名）。
type EventDayRow struct {
	Day   string `json:"day"`
	Name  string `json:"name"`
	Total int    `json:"total"`
}

// ListEventsByDay 按 (UTC 日, 事件名) 聚合。
//
// 🔴 日界按 UTC 切。后台展示用北京时间，但聚合键必须和 /v1/admin/stats/daily
// 同一套口径（那个也是 UTC），否则同一天的「埋点数」和「任务数」对不上，
// 而运营唯一会做的事就是把两张表横着对。这条差异写在视图说明里。
func ListEventsByDay(ctx context.Context, q Queryer, since time.Time) ([]EventDayRow, error) {
	rows, err := q.Query(ctx, `
		SELECT to_char(date_trunc('day', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), name, count(*)
		FROM events
		WHERE occurred_at >= $1`+notUserEventClause(2)+`
		GROUP BY 1, 2 ORDER BY 1 DESC, 3 DESC`,
		append([]any{since}, notUserEventArgs()...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventDayRow{}
	for rows.Next() {
		var r EventDayRow
		if err := rows.Scan(&r.Day, &r.Name, &r.Total); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventVersionRow 是埋点按 App 版本的聚合。
type EventVersionRow struct {
	Version string `json:"version"`
	Total   int    `json:"total"`
	Users   int    `json:"users"`
}

// ListEventVersions 按 props 里的版本字段聚合。
//
// 🔴 当前 App（web/app.js 的 track()）**不上报版本号** —— props 里只有
// styleId / jobId 这类业务字段。所以这个聚合的正常结果是一行 "(未上报)"。
// 这是故意保留的：面板上那一行就是「App 还没带版本号」这件事的唯一可见处，
// 假装有版本分布（或者干脆不做这一节）会让运维以为已经能按版本看问题了。
// 取值兼容三种常见写法，App 哪天开始报哪一个都能直接生效。
func ListEventVersions(ctx context.Context, q Queryer, since time.Time) ([]EventVersionRow, error) {
	rows, err := q.Query(ctx, `
		SELECT COALESCE(NULLIF(props->>'appVersion',''), NULLIF(props->>'app_version',''),
		                NULLIF(props->>'version',''), '(未上报)'),
		       count(*), count(DISTINCT user_id)
		FROM events
		WHERE occurred_at >= $1`+notUserEventClause(2)+`
		GROUP BY 1 ORDER BY 2 DESC, 1 ASC`,
		append([]any{since}, notUserEventArgs()...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventVersionRow{}
	for rows.Next() {
		var r EventVersionRow
		if err := rows.Scan(&r.Version, &r.Total, &r.Users); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventSampleRow 是一条原始埋点样本。User 只回前 8 位（脱敏契约，与 users 列表一致）。
type EventSampleRow struct {
	At    string          `json:"at"`
	Name  string          `json:"name"`
	User  *string         `json:"user"`
	Props json.RawMessage `json:"props"`
}

// ListEventSamples 原始埋点样本，倒序。name 为空表示不筛。
func ListEventSamples(ctx context.Context, q Queryer, since time.Time, name string, limit int) ([]EventSampleRow, error) {
	sql := `
		SELECT occurred_at, name, substr(user_id,1,8), props
		FROM events
		WHERE occurred_at >= $1` + notUserEventClause(2)
	args := append([]any{since}, notUserEventArgs()...)
	if name != "" {
		args = append(args, name)
		sql += ` AND name = $` + itoa(len(args))
	}
	sql += ` ORDER BY occurred_at DESC, id DESC LIMIT ` + itoa(limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventSampleRow{}
	for rows.Next() {
		var r EventSampleRow
		var t time.Time
		var raw []byte
		if err := rows.Scan(&t, &r.Name, &r.User, &raw); err != nil {
			return nil, err
		}
		r.At = ISO(t)
		r.Props = json.RawMessage(raw)
		if len(raw) == 0 {
			r.Props = json.RawMessage(`{}`)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 资产 assets -----------------------------------------------------------

// AdminAssetRow 是 GET /v1/admin/assets 的一行。
type AdminAssetRow struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	ContentType string  `json:"contentType"`
	ByteSize    *int64  `json:"byteSize"`
	Width       *int    `json:"width"`
	Height      *int    `json:"height"`
	SHA256      *string `json:"sha256"`
	User        string  `json:"user"`
	Email       *string `json:"email"`
	ProjectID   *string `json:"projectId"`
	CreatedAt   string  `json:"createdAt"`
	DeletedAt   *string `json:"deletedAt"`
}

// AssetFilter 是资产列表的筛选条件。零值表示不筛。
type AssetFilter struct {
	Kind   string
	Status string
	UserID string
	Limit  int
}

// ListAdminAssets 资产列表，倒序。
//
// 🔴 storage_key 刻意**不回**：它是磁盘上的相对路径，回给后台等于把资产目录
// 的布局发到浏览器里；而后台要看图走的是 /v1/admin/assets/{id}/file（短时令牌），
// 不需要知道文件在哪。
func ListAdminAssets(ctx context.Context, q Queryer, f AssetFilter) ([]AdminAssetRow, error) {
	sql := `
		SELECT a.id, a.kind, a.status, a.content_type, a.byte_size, a.width, a.height, a.sha256,
		       substr(a.user_id,1,8),
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=a.user_id AND ai.email_normalized IS NOT NULL LIMIT 1),
		       a.project_id, a.created_at, a.deleted_at
		FROM assets a WHERE true`
	args := []any{}
	if f.Kind != "" {
		args = append(args, f.Kind)
		sql += ` AND a.kind = $` + itoa(len(args))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		sql += ` AND a.status = $` + itoa(len(args))
	}
	if f.UserID != "" {
		// 前缀匹配：后台列表里只显示 id 的前 8 位，运营能复制到的就是那 8 位。
		// 要求填完整 id 等于这个筛选永远没人用得上。
		args = append(args, f.UserID+"%")
		sql += ` AND a.user_id LIKE $` + itoa(len(args)) + ` ESCAPE '\'`
	}
	sql += ` ORDER BY a.created_at DESC, a.id ASC LIMIT ` + itoa(f.Limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminAssetRow{}
	for rows.Next() {
		var r AdminAssetRow
		var created time.Time
		var deleted *time.Time
		if err := rows.Scan(&r.ID, &r.Kind, &r.Status, &r.ContentType, &r.ByteSize, &r.Width, &r.Height,
			&r.SHA256, &r.User, &r.Email, &r.ProjectID, &created, &deleted); err != nil {
			return nil, err
		}
		r.CreatedAt = ISO(created)
		if deleted != nil {
			s := ISO(*deleted)
			r.DeletedAt = &s
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AssetTotals 是资产列表页头的三个数字（按 kind 汇总，覆盖全表而不是当前页）。
type AssetTotals struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// SumAssetsByKind 按 kind 汇总条数与字节数。
func SumAssetsByKind(ctx context.Context, q Queryer) ([]AssetTotals, error) {
	rows, err := q.Query(ctx, `
		SELECT kind, count(*), COALESCE(sum(byte_size),0) FROM assets
		GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetTotals{}
	for rows.Next() {
		var r AssetTotals
		if err := rows.Scan(&r.Kind, &r.Count, &r.Bytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 用户纵向详情 ----------------------------------------------------------

// LedgerRow 是额度账本的一行。
type LedgerRow struct {
	At        string  `json:"at"`
	EntryType string  `json:"entryType"`
	Units     int     `json:"units"`
	JobID     *string `json:"jobId"`
	// Source 是这笔额度来自哪个 bucket 的来源类型（free_grant / purchase / promo / manual）。
	// 没有它，账本上一串 grant 行看不出哪几次是充值、哪几次是白送。
	Source string `json:"source"`
}

// ListUserLedger 某用户的额度账本，倒序。
func ListUserLedger(ctx context.Context, q Queryer, userID string, limit int) ([]LedgerRow, error) {
	rows, err := q.Query(ctx, `
		SELECT l.created_at, l.entry_type, l.units, l.job_id, b.source_type
		FROM credit_ledger l JOIN credit_buckets b ON b.id = l.balance_bucket_id
		WHERE l.user_id = $1 ORDER BY l.created_at DESC, l.id ASC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LedgerRow{}
	for rows.Next() {
		var r LedgerRow
		var t time.Time
		if err := rows.Scan(&t, &r.EntryType, &r.Units, &r.JobID, &r.Source); err != nil {
			return nil, err
		}
		r.At = ISO(t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionRow 是一个会话。token 绝不出现在出参里。
type SessionRow struct {
	// TokenTail 是令牌的**后 6 位**，用来让运营在「用户说他被踢了」时
	// 对上具体哪一个设备 —— 但拿到这 6 位无法重建令牌。
	TokenTail  string  `json:"tokenTail"`
	DeviceID   *string `json:"deviceId"`
	CreatedAt  string  `json:"createdAt"`
	LastSeenAt string  `json:"lastSeenAt"`
	ExpiresAt  *string `json:"expiresAt"`
	Expired    bool    `json:"expired"`
}

// ListUserSessions 某用户的会话，按最近活跃倒序。
func ListUserSessions(ctx context.Context, q Queryer, userID string, now time.Time, limit int) ([]SessionRow, error) {
	rows, err := q.Query(ctx, `
		SELECT right(token, 6), device_id, created_at, last_seen_at, expires_at
		FROM sessions WHERE user_id = $1 ORDER BY last_seen_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionRow{}
	for rows.Next() {
		var r SessionRow
		var created, seen time.Time
		var exp *time.Time
		if err := rows.Scan(&r.TokenTail, &r.DeviceID, &created, &seen, &exp); err != nil {
			return nil, err
		}
		r.CreatedAt, r.LastSeenAt = ISO(created), ISO(seen)
		if exp != nil {
			s := ISO(*exp)
			r.ExpiresAt = &s
			r.Expired = exp.Before(now)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserProjectRow 是某用户的一个项目。
type UserProjectRow struct {
	ID        string  `json:"id"`
	Title     *string `json:"title"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
	Deleted   bool    `json:"deleted"`
	Jobs      int     `json:"jobs"`
}

// ListUserProjects 某用户的项目，倒序。软删的也回（带 deleted 标记）——
// 「用户说他的作品不见了」这个问题只有看得见软删行才答得上。
func ListUserProjects(ctx context.Context, q Queryer, userID string, limit int) ([]UserProjectRow, error) {
	rows, err := q.Query(ctx, `
		SELECT p.id, p.title, p.status, p.created_at, p.updated_at, p.deleted_at IS NOT NULL,
		       (SELECT count(*) FROM generation_jobs j WHERE j.project_id = p.id)
		FROM projects p WHERE p.user_id = $1
		ORDER BY p.updated_at DESC, p.id ASC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserProjectRow{}
	for rows.Next() {
		var r UserProjectRow
		var created, updated time.Time
		if err := rows.Scan(&r.ID, &r.Title, &r.Status, &created, &updated, &r.Deleted, &r.Jobs); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = ISO(created), ISO(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserPurchaseRow 是某用户的一笔购买。与 AdminPurchaseRow 的区别是带平台与交易号。
type UserPurchaseRow struct {
	ID          string  `json:"id"`
	Product     string  `json:"product"`
	Platform    string  `json:"platform"`
	TxID        string  `json:"txId"`
	Status      string  `json:"status"`
	AmountMinor *int64  `json:"amountMinor"`
	Currency    *string `json:"currency"`
	PurchasedAt string  `json:"purchasedAt"`
}

// ListUserPurchases 某用户的购买，倒序。
func ListUserPurchases(ctx context.Context, q Queryer, userID string, limit int) ([]UserPurchaseRow, error) {
	rows, err := q.Query(ctx, `
		SELECT pu.id, p.display_name, pu.platform, pu.external_transaction_id, pu.status,
		       pu.amount_minor, pu.currency, pu.purchased_at
		FROM purchases pu JOIN products p ON p.id = pu.product_id
		WHERE pu.user_id = $1 ORDER BY pu.purchased_at DESC, pu.id ASC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPurchases(rows)
}

func scanUserPurchases(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]UserPurchaseRow, error) {
	out := []UserPurchaseRow{}
	for rows.Next() {
		var r UserPurchaseRow
		var t time.Time
		if err := rows.Scan(&r.ID, &r.Product, &r.Platform, &r.TxID, &r.Status,
			&r.AmountMinor, &r.Currency, &t); err != nil {
			return nil, err
		}
		r.PurchasedAt = ISO(t)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetUserByPrefix 按 id 前缀找唯一一个用户，返回完整 id。
//
// 🔴 必须回「找到几个」而不是只回第一个：后台列表显示的是 8 位前缀，
// 理论上两个 uuid 能撞前缀。悄悄取第一个意味着运营给 A 发的额度落到了 B 头上。
func GetUserByPrefix(ctx context.Context, q Queryer, prefix string) (id string, matches int, err error) {
	rows, err := q.Query(ctx, `SELECT id FROM users WHERE id LIKE $1 ESCAPE '\' LIMIT 3`, prefix+"%")
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			return "", 0, err
		}
		matches++
		if matches == 1 {
			id = got
		}
	}
	return id, matches, rows.Err()
}

// ---- 邮件发送记录 ----------------------------------------------------------

// EmailSendKind 是邮件发送记录在 events 表里的事件名。
//
// 🔴 刻意复用 events 而不是建一张 email_sends 表 —— 和审计（admin.*）同一个取舍：
// events 已经有 (name, occurred_at) 索引、已经进每日备份、已经能被数据库浏览器查。
// 代价同样要说清楚：events 受 event_retention_days 的每日清理管，
// 所以发信记录也会到期被删。后台那一节的说明里写了这句。
const EmailSendKind = "email.send"

// EmailSendRow 是一条发信记录。收件地址**已打码**（a***@example.com）。
type EmailSendRow struct {
	At    string  `json:"at"`
	Kind  string  `json:"kind"`
	To    string  `json:"to"`
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}

// InsertEmailSend 记一条发信记录。to 必须是**已打码**的地址。
func InsertEmailSend(ctx context.Context, q Queryer, id, kind, maskedTo string, ok bool, errText string, t time.Time) error {
	props := map[string]any{"kind": kind, "to": maskedTo, "ok": ok}
	if errText != "" {
		props["error"] = errText
	}
	raw, err := json.Marshal(props)
	if err != nil {
		return err
	}
	return InsertEvent(ctx, q, id, nil, EmailSendKind, raw, t)
}

// ListEmailSends 发信记录，倒序。
func ListEmailSends(ctx context.Context, q Queryer, limit int) ([]EmailSendRow, error) {
	rows, err := q.Query(ctx, `
		SELECT occurred_at, props->>'kind', props->>'to', COALESCE((props->>'ok')::boolean, false), props->>'error'
		FROM events WHERE name = $1 ORDER BY occurred_at DESC, id DESC LIMIT $2`, EmailSendKind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EmailSendRow{}
	for rows.Next() {
		var r EmailSendRow
		var t time.Time
		var kind, to *string
		if err := rows.Scan(&t, &kind, &to, &r.OK, &r.Error); err != nil {
			return nil, err
		}
		r.At = ISO(t)
		r.Kind, r.To = deref(kind), deref(to)
		out = append(out, r)
	}
	return out, rows.Err()
}

// EmailCodeRow 是一行待用/历史验证码的**元数据**（绝不含 code_hash）。
type EmailCodeRow struct {
	Email      string `json:"email"`
	CreatedAt  string `json:"createdAt"`
	ExpiresAt  string `json:"expiresAt"`
	Attempts   int    `json:"attempts"`
	IssueCount int    `json:"issueCount"`
	Expired    bool   `json:"expired"`
}

// ListEmailCodes 验证码签发台账。email 打码。
//
// 🔴 code_hash 一个字节都不回。它是 HMAC，但后台页面不需要它，
// 而「后台能看到验证码哈希」这件事本身会变成下一个人复制粘贴的起点。
func ListEmailCodes(ctx context.Context, q Queryer, now time.Time, limit int) ([]EmailCodeRow, error) {
	rows, err := q.Query(ctx, `
		SELECT email, created_at, expires_at, attempts, issue_count
		FROM email_codes ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EmailCodeRow{}
	for rows.Next() {
		var r EmailCodeRow
		var created, expires time.Time
		if err := rows.Scan(&r.Email, &created, &expires, &r.Attempts, &r.IssueCount); err != nil {
			return nil, err
		}
		r.Email = MaskEmail(r.Email)
		r.CreatedAt, r.ExpiresAt = ISO(created), ISO(expires)
		r.Expired = expires.Before(now)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaskEmail 把地址打码成 a***@example.com。与 mailer.MaskEmail 同语义。
//
// 🔴 刻意不 import internal/mailer：store 被 mailer 之外的一堆东西用，
// 为一个 20 行的纯函数加一条 store -> mailer 依赖会把依赖图拧成环（mailer 要用 store 落记录）。
func MaskEmail(to string) string {
	to = strings.TrimSpace(to)
	i := strings.LastIndex(to, "@")
	if i <= 0 {
		if to == "" {
			return ""
		}
		return "***"
	}
	return to[:1] + "***" + to[i:]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// itoa 是 strconv.Itoa 的本地别名。
//
// 🔴 只用来拼**已经夹过范围的整数**（LIMIT、占位符序号），绝不用于用户输入：
// 用户输入一律走 $N 占位符。
func itoa(n int) string { return strconv.Itoa(n) }
