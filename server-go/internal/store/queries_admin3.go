// 后台「运营」页的读写查询：风格完整编辑、商品补齐字段、用户禁用/启用、
// 操作审计、反馈原因码观测。
//
// 这一批都是 2026-09-12 第三轮盘点的落点 —— 此前后台对它们只有「看一眼」
// 甚至「看不见」的能力，而它们每一项都是运营每天要动的东西。
package store

import (
	"context"
	"encoding/json"
	"time"
)

// ---- 风格完整编辑 ----------------------------------------------------------

// AdminStyleFull 是 GET /v1/admin/styles-admin 的一行（含未发布风格）。
//
// 🔴 schema 里**没有**的东西，这里一个都不编：styles 表没有封面列
// （封面是 web/covers/<internal_key>.jpg 这个文件，由 styleCard 按文件是否存在决定）、
// 没有多语言名列（只有 public_name 一列，想给中文用户看中文就把它改成中文）、
// 也没有每风格单价列（每个任务恒定扣 1 张，见 public_jobs.go 的 units = 1）。
// 后台对这三样只能展示事实，不能给一个写了等于没写的输入框。
type AdminStyleFull struct {
	ID           string   `json:"id"`
	InternalKey  string   `json:"internalKey"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	ShortCaption string   `json:"shortCaption"`
	Theme        string   `json:"theme"`
	Status       string   `json:"status"`
	Premium      bool     `json:"premium"`
	Tags         []string `json:"tags"`
	Jobs         int      `json:"jobs"`
	// 展览归属与位次。排序键是 (exhibitions.editorial_rank, exhibition_styles.position)，
	// 所以改位次改的是 exhibition_styles 那一行，不是 styles。
	ExhibitionID    *string `json:"exhibitionId"`
	ExhibitionTitle *string `json:"exhibitionTitle"`
	EditorialRank   *int    `json:"editorialRank"`
	Position        *int    `json:"position"`
	// PublishedVersions 是已发布版本数。为 0 时这个风格即使 status=published
	// 也**不会出现在 /v1/styles 与 /v1/discover 里**（目录查询 JOIN 的是
	// style_versions.status='published'）——后台必须把这件事说出来，
	// 否则「我上线了为什么 App 里没有」只能靠读 SQL 回答。
	PublishedVersions int `json:"publishedVersions"`
}

// ListAdminStylesFull 风格管理列表（含未发布），ORDER BY 与目录一致：
// editorial_rank, position, id —— 这样后台看到的顺序就是用户看到的顺序。
// 没挂到任何展览的风格排在最后（NULLS LAST）。
func ListAdminStylesFull(ctx context.Context, q Queryer) ([]AdminStyleFull, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, s.internal_key, s.slug, s.public_name, s.short_caption, s.theme, s.status, s.premium,
		       s.suitability_tags,
		       (SELECT count(*) FROM generation_jobs j JOIN style_versions v ON v.id = j.style_version_id
		        WHERE v.style_id = s.id),
		       es.exhibition_id, e.title, e.editorial_rank, es.position,
		       (SELECT count(*) FROM style_versions v2 WHERE v2.style_id = s.id AND v2.status = 'published')
		FROM styles s
		LEFT JOIN exhibition_styles es ON es.style_id = s.id
		LEFT JOIN exhibitions e ON e.id = es.exhibition_id
		ORDER BY e.editorial_rank ASC NULLS LAST, es.position ASC NULLS LAST, s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminStyleFull{}
	for rows.Next() {
		var r AdminStyleFull
		var tags []byte
		if err := rows.Scan(&r.ID, &r.InternalKey, &r.Slug, &r.Name, &r.ShortCaption, &r.Theme,
			&r.Status, &r.Premium, &tags, &r.Jobs, &r.ExhibitionID, &r.ExhibitionTitle,
			&r.EditorialRank, &r.Position, &r.PublishedVersions); err != nil {
			return nil, err
		}
		r.Tags = []string{}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &r.Tags)
			if r.Tags == nil {
				r.Tags = []string{}
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAdminStyle 取一行（改完之后回读，让后台拿到的是库里的真相而不是入参回显）。
func GetAdminStyle(ctx context.Context, q Queryer, id string) (*AdminStyleFull, error) {
	all, err := ListAdminStylesFull(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}

// StyleUpdate 是风格可编辑字段的补丁。nil = 这次不改这一项。
type StyleUpdate struct {
	PublicName   *string
	ShortCaption *string
	Theme        *string
	Premium      *bool
	Status       *string
	// Tags 为非 nil 时整体替换 suitability_tags（已是 JSON 数组字节）。
	Tags []byte
	// Position 改 exhibition_styles 那一行的位次（风格挂在哪个展览由策展决定，
	// 后台不给改：换展览等于换产品结构，不是运营动作）。
	Position *int
}

// UpdateStyleFields 按补丁改一个风格。返回 false 表示风格不存在。
//
// 🔴 必须在**一个事务**里：名称改成功、状态改失败会留下一个「名字是新的、
// 还在下架」的中间态，而后台那一次保存返回的是错误，运营会再点一次，
// 于是名称被改两遍（幂等，无害）但状态到底有没有改过没人说得清。
func UpdateStyleFields(ctx context.Context, st *Store, id string, u StyleUpdate) (bool, error) {
	found := false
	err := st.InTx(ctx, func(q Queryer) error {
		var exists string
		err := q.QueryRow(ctx, `SELECT id FROM styles WHERE id = $1`, id).Scan(&exists)
		if IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if u.PublicName != nil {
			if _, err := q.Exec(ctx, `UPDATE styles SET public_name = $1 WHERE id = $2`, *u.PublicName, id); err != nil {
				return err
			}
		}
		if u.ShortCaption != nil {
			if _, err := q.Exec(ctx, `UPDATE styles SET short_caption = $1 WHERE id = $2`, *u.ShortCaption, id); err != nil {
				return err
			}
		}
		if u.Theme != nil {
			if _, err := q.Exec(ctx, `UPDATE styles SET theme = $1 WHERE id = $2`, *u.Theme, id); err != nil {
				return err
			}
		}
		if u.Premium != nil {
			if _, err := q.Exec(ctx, `UPDATE styles SET premium = $1 WHERE id = $2`, *u.Premium, id); err != nil {
				return err
			}
		}
		if u.Status != nil {
			if _, err := q.Exec(ctx, `UPDATE styles SET status = $1 WHERE id = $2`, *u.Status, id); err != nil {
				return err
			}
		}
		if u.Tags != nil {
			if _, err := q.Exec(ctx, `UPDATE styles SET suitability_tags = $1 WHERE id = $2`, u.Tags, id); err != nil {
				return err
			}
		}
		if u.Position != nil {
			// 一个风格理论上可以挂多个展览（主键是 (exhibition_id, style_id)）。
			// 实际目录里是一对一，但不能假设：这里改它**所有**展览行的位次，
			// 而不是随便挑一行改 —— 挑一行的话另一行的旧位次还在排序里起作用。
			if _, err := q.Exec(ctx,
				`UPDATE exhibition_styles SET position = $1 WHERE style_id = $2`, *u.Position, id); err != nil {
				return err
			}
		}
		return nil
	})
	return found, err
}

// ---- 用户禁用 / 启用 -------------------------------------------------------

// SetUserStatus 改 users.status。返回 false 表示用户不存在或已软删。
//
// 🔴 只动 status，**不删会话**。删会话看起来更干净，但那样「启用」之后用户
// 得重新登录一次（我们没法把删掉的 token 变回来），而封禁有很大概率是误判
// 或临时措施。改判据让封禁可逆：authenticate() 见到非 active 一律当没登录，
// 改回 active 之后原来的 token 立刻又能用。
func SetUserStatus(ctx context.Context, q Queryer, userID, status string, t time.Time) (bool, error) {
	tag, err := q.Exec(ctx,
		`UPDATE users SET status = $1, updated_at = $2 WHERE id = $3 AND deleted_at IS NULL`,
		status, t, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// CountUsersByStatus 统计各 status 的未软删用户数。
// 上线前用它确认「生产库里到底有哪些 status 值」—— 把 active 之外的一律当封禁
// 之前，必须先证明没有第三种值在正常使用中。
func CountUsersByStatus(ctx context.Context, q Queryer) (map[string]int, error) {
	rows, err := q.Query(ctx,
		`SELECT status, count(*) FROM users WHERE deleted_at IS NULL GROUP BY status ORDER BY status`)
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

// ---- 操作审计 --------------------------------------------------------------

// AuditRow 是 GET /v1/admin/audit 的一行。
type AuditRow struct {
	At     string         `json:"at"`
	Action string         `json:"action"`
	Props  map[string]any `json:"props"`
}

// AuditPrefix 是审计事件名的前缀。审计复用 events 表而不是新建一张：
// events 已经有 (name, occurred_at) 索引、已经进每日备份、已经在后台的
// 数据库浏览器里可查。
//
// 🔴 代价必须说清楚：events 受 event_retention_days 的每日清理管，
// 所以审计记录也会到期被删。注册表里那一项的说明和后台那一节的说明都写了这句。
const AuditPrefix = "admin."

// InsertAudit 记一条后台操作审计。
//
// 🔴 user_id 恒为 NULL：events.user_id 有外键指向 users，而「操作者」是管理员、
// 不是 users 表里的行。把被操作的用户 id 塞进 user_id 会让审计看起来像是
// **那个用户自己**干的。被操作对象一律进 props。
func InsertAudit(ctx context.Context, q Queryer, id, action string, props map[string]any, t time.Time) error {
	if props == nil {
		props = map[string]any{}
	}
	raw, err := json.Marshal(props)
	if err != nil {
		return err
	}
	return InsertEvent(ctx, q, id, nil, AuditPrefix+action, raw, t)
}

// ListAudit 最近的后台操作审计，倒序。
func ListAudit(ctx context.Context, q Queryer, limit int) ([]AuditRow, error) {
	rows, err := q.Query(ctx, `
		SELECT occurred_at, name, props FROM events
		WHERE name LIKE $1 ESCAPE '\' ORDER BY occurred_at DESC, id DESC LIMIT $2`,
		AuditPrefix+`%`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var t time.Time
		var name string
		var raw []byte
		if err := rows.Scan(&t, &name, &raw); err != nil {
			return nil, err
		}
		r := AuditRow{At: ISO(t), Action: name[len(AuditPrefix):], Props: map[string]any{}}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &r.Props)
			if r.Props == nil {
				r.Props = map[string]any{}
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 反馈原因码观测 --------------------------------------------------------

// ReasonCodeRow 是一个被**实际提交过**的原因码及其计数。
type ReasonCodeRow struct {
	Code     string `json:"code"`
	Negative int    `json:"negative"`
	Positive int    `json:"positive"`
}

// ListReasonCodes 按原因码聚合 user_feedback。
//
// 🔴 刻意是「观测」而不是「字典配置」：原因码在当前产品里是 App 里的四个硬编码
// 芯片（web/app.js 的 FACE_CHANGED / WRONG_STYLE / BAD_DETAILS / TOO_STRONG），
// 服务端对它们没有任何枚举校验，App 也不会去读服务端下发的字典。
// 做一个「后台可配的原因码字典」等于做一个没有任何客户端会读的开关 ——
// 面板上看起来能配，改了却什么都不会发生。所以这里只回答一个真问题：
// 用户到底点了哪些码、各多少次。
func ListReasonCodes(ctx context.Context, q Queryer) ([]ReasonCodeRow, error) {
	rows, err := q.Query(ctx, `
		SELECT code,
		       count(*) FILTER (WHERE rating = 'negative'),
		       count(*) FILTER (WHERE rating = 'positive')
		FROM user_feedback f, jsonb_array_elements_text(f.reason_codes) AS code
		GROUP BY code ORDER BY 2 DESC, 3 DESC, code ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReasonCodeRow{}
	for rows.Next() {
		var r ReasonCodeRow
		if err := rows.Scan(&r.Code, &r.Negative, &r.Positive); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
