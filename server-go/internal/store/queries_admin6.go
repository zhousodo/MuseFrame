// 后台「最后三块只读视图」（2026-09-12 第七轮盘点）。
//
// 这三张表此前在后台**只有数据库浏览器一条读法**，而数据库浏览器是按表分页的
// 原始视图：它答不了「这一个任务产出了几张候选、哪几张没过质检」
// 「这张源图的分析结果是什么」「这个风格实际在用的 spec 长什么样」这三个问题 ——
// 它们都需要跨表 JOIN 或者按 id 定位一行。
//
//	photo_analyses   App 上传源图后服务端做的画面分析（主体/人数/清晰度/曝光/建议）
//	generation_candidates  一个任务的**全部**候选（任务列表只显示第一张）
//	style_versions.spec    风格的不可变生成规格（风格页此前只显示「有几个已发布版本」）
//
// 三者都是**只读**：没有任何写入口，也不该有 —— spec 改一个字就是换一个版本，
// 分析结果由 worker 写，候选由生成流水线写。
package store

import (
	"context"
	"encoding/json"
	"time"
)

// ---- 任务候选 --------------------------------------------------------------

// JobCandidateRow 是一个任务的一张候选。
type JobCandidateRow struct {
	ID            string `json:"id"`
	Index         int    `json:"index"`
	AssetID       string `json:"assetId"`
	QualityPassed bool   `json:"qualityPassed"`
	CreatedAt     string `json:"createdAt"`
	// 资产侧的事实，省掉前端再发一轮请求。
	AssetStatus string  `json:"assetStatus"`
	AIGCLabel   *string `json:"aigcLabel"`
	ByteSize    *int64  `json:"byteSize"`
	Width       *int    `json:"width"`
	Height      *int    `json:"height"`
	DeletedAt   *string `json:"deletedAt"`
}

// ListJobCandidates 一个任务的**全部**候选，按 created_at 升序。
//
// 🔴 排序键是 created_at（而不是 candidate_index）：这是「产出顺序」，
// 也是运营要的那个顺序 —— 任务列表里显示的是第一张，出问题时要看的是
// 「后面几张是不是也这样」。candidate_index 作为并列时的次级键：
// 同一秒写入的多张候选用它定序，否则行序未定义。
func ListJobCandidates(ctx context.Context, q Queryer, jobID string) ([]JobCandidateRow, error) {
	rows, err := q.Query(ctx, `
		SELECT c.id, c.candidate_index, c.asset_id, c.quality_passed, c.created_at,
		       a.status, a.aigc_label, a.byte_size, a.width, a.height, a.deleted_at
		FROM generation_candidates c JOIN assets a ON a.id = c.asset_id
		WHERE c.job_id = $1
		ORDER BY c.created_at ASC, c.candidate_index ASC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobCandidateRow{}
	for rows.Next() {
		var r JobCandidateRow
		var created time.Time
		var deleted *time.Time
		if err := rows.Scan(&r.ID, &r.Index, &r.AssetID, &r.QualityPassed, &created,
			&r.AssetStatus, &r.AIGCLabel, &r.ByteSize, &r.Width, &r.Height, &deleted); err != nil {
			return nil, err
		}
		r.CreatedAt = ISO(created)
		r.DeletedAt = ISOPtr(deleted)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 用户的登录身份 --------------------------------------------------------

// UserIdentityRow 是一条登录身份（auth_identities 的一行）。
//
// 🔴 email 与 provider_subject 都是**完整值**。用户详情页此前一个邮箱都不显示
// （只有列表页有），于是客服在详情页确认完「就是这个人」之后，还得回列表页去抄
// 邮箱才能回信。provider_subject 是 Google/Apple 那边的稳定用户标识
// （邮箱登录时形如 "email:<完整邮箱>"）—— 它是向平台提工单时唯一认的那个 id。
type UserIdentityRow struct {
	Provider  string  `json:"provider"`
	Email     *string `json:"email"`
	Subject   string  `json:"subject"`
	CreatedAt string  `json:"createdAt"`
}

// ListUserIdentities 一个用户的全部登录身份，按创建时间升序。
func ListUserIdentities(ctx context.Context, q Queryer, userID string) ([]UserIdentityRow, error) {
	rows, err := q.Query(ctx, `
		SELECT provider, email_normalized, provider_subject, created_at
		FROM auth_identities WHERE user_id = $1 ORDER BY created_at ASC, provider ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserIdentityRow{}
	for rows.Next() {
		var r UserIdentityRow
		var created time.Time
		if err := rows.Scan(&r.Provider, &r.Email, &r.Subject, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = ISO(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PrimaryEmail 从身份列表里挑一个主邮箱（最早绑定的那个有邮箱的身份）。
// 没有任何邮箱时回 nil —— 那是一个纯游客账号。
func PrimaryEmail(ids []UserIdentityRow) *string {
	for i := range ids {
		if ids[i].Email != nil && *ids[i].Email != "" {
			return ids[i].Email
		}
	}
	return nil
}

// ---- 画面分析 photo_analyses -----------------------------------------------

// PhotoAnalysisRow 是 photo_analyses 的一行（带资产与用户归属）。
type PhotoAnalysisRow struct {
	ID              string          `json:"id"`
	AssetID         string          `json:"assetId"`
	AnalyzerVersion string          `json:"analyzerVersion"`
	Status          string          `json:"status"`
	SubjectType     *string         `json:"subjectType"`
	PersonCount     *int            `json:"personCount"`
	Sharpness       *float64        `json:"sharpness"`
	Exposure        *float64        `json:"exposure"`
	Warnings        json.RawMessage `json:"warnings"`
	Recommendations json.RawMessage `json:"recommendations"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
	// User / Email 是这张源图的归属。完整值（管理员后台）。
	User  string  `json:"user"`
	Email *string `json:"email"`
}

// PhotoAnalysisFilter 是画面分析列表的筛选条件。零值表示不筛。
type PhotoAnalysisFilter struct {
	Status string
	// UserID 按**前缀**匹配（工单里抄来的常常只有几位）。
	UserID string
	// AssetID 精确匹配一张源图。
	AssetID string
	Range   TimeRange
	Limit   int
}

// ListAdminPhotoAnalyses 画面分析列表，倒序。
func ListAdminPhotoAnalyses(ctx context.Context, q Queryer, f PhotoAnalysisFilter) ([]PhotoAnalysisRow, error) {
	sql := `
		SELECT pa.id, pa.asset_id, pa.analyzer_version, pa.status, pa.subject_type, pa.person_count,
		       pa.sharpness, pa.exposure, pa.warnings, pa.recommendations, pa.created_at, pa.updated_at,
		       a.user_id,
		       (SELECT ai.email_normalized FROM auth_identities ai WHERE ai.user_id=a.user_id AND ai.email_normalized IS NOT NULL LIMIT 1)
		FROM photo_analyses pa JOIN assets a ON a.id = pa.asset_id
		WHERE true`
	args := []any{}
	if f.Status != "" {
		args = append(args, f.Status)
		sql += ` AND pa.status = $` + itoa(len(args))
	}
	if f.AssetID != "" {
		args = append(args, f.AssetID)
		sql += ` AND pa.asset_id = $` + itoa(len(args))
	}
	if f.UserID != "" {
		args = append(args, f.UserID+"%")
		sql += ` AND a.user_id LIKE $` + itoa(len(args)) + ` ESCAPE '\'`
	}
	sql = f.Range.apply("pa.created_at", sql, &args)
	sql += ` ORDER BY pa.created_at DESC, pa.id ASC LIMIT ` + itoa(f.Limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PhotoAnalysisRow{}
	for rows.Next() {
		var r PhotoAnalysisRow
		var created, updated time.Time
		var warn, rec []byte
		if err := rows.Scan(&r.ID, &r.AssetID, &r.AnalyzerVersion, &r.Status, &r.SubjectType,
			&r.PersonCount, &r.Sharpness, &r.Exposure, &warn, &rec, &created, &updated,
			&r.User, &r.Email); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = ISO(created), ISO(updated)
		r.Warnings, r.Recommendations = jsonOrEmptyArray(warn), jsonOrEmptyArray(rec)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountPhotoAnalysesByStatus 画面分析的状态分布（页头用）。
func CountPhotoAnalysesByStatus(ctx context.Context, q Queryer) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT status, count(*) FROM photo_analyses GROUP BY status`)
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

// ---- 风格版本与 spec -------------------------------------------------------

// StyleVersionRow 是一个风格版本，**含完整 spec**。
type StyleVersionRow struct {
	ID      string `json:"id"`
	StyleID string `json:"styleId"`
	Version int    `json:"version"`
	Status  string `json:"status"`
	// Spec 是不可变的 StyleSpec（jsonb 原文）。
	//
	// 🔴 它是「这个风格到底怎么生成的」的唯一答案：提示词模板、控件取值域、
	// 负面词、后处理参数全在里面。后台此前只显示「有几个已发布版本」，
	// 于是「为什么这个风格出图变了」只能靠 SSH 上去 psql。
	// 只读：spec 改一个字就该是一个新版本，所以这里没有、也不该有写入口。
	Spec        json.RawMessage `json:"spec"`
	PublishedAt *string         `json:"publishedAt"`
	CreatedAt   string          `json:"createdAt"`
	// Jobs 是用这个版本跑过的任务数 —— 判断「这个版本有没有真的在服役」。
	Jobs int `json:"jobs"`
}

// ListStyleVersions 一个风格的全部版本（含 spec），版本号倒序。
// styleID 为空时返回全部风格的版本（受 limit 夹住）。
func ListStyleVersions(ctx context.Context, q Queryer, styleID string, limit int) ([]StyleVersionRow, error) {
	sql := `
		SELECT v.id, v.style_id, v.version, v.status, v.spec, v.published_at, v.created_at,
		       (SELECT count(*) FROM generation_jobs j WHERE j.style_version_id = v.id)
		FROM style_versions v WHERE true`
	args := []any{}
	if styleID != "" {
		args = append(args, styleID)
		sql += ` AND v.style_id = $` + itoa(len(args))
	}
	sql += ` ORDER BY v.style_id ASC, v.version DESC LIMIT ` + itoa(limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StyleVersionRow{}
	for rows.Next() {
		var r StyleVersionRow
		var spec []byte
		var published *time.Time
		var created time.Time
		if err := rows.Scan(&r.ID, &r.StyleID, &r.Version, &r.Status, &spec, &published, &created, &r.Jobs); err != nil {
			return nil, err
		}
		r.Spec = json.RawMessage(spec)
		if len(spec) == 0 {
			r.Spec = json.RawMessage(`{}`)
		}
		r.PublishedAt = ISOPtr(published)
		r.CreatedAt = ISO(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// jsonOrEmptyArray 把可能为 NULL 的 jsonb 列归一成合法 JSON。
//
// 🔴 空字节切片直接塞进 json.RawMessage 会让整个响应变成**非法 JSON**
// （`"warnings":` 后面什么都没有），而前端看到的是一句解析失败 —— 排查方向
// 会完全跑偏到「接口挂了」。
func jsonOrEmptyArray(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}
