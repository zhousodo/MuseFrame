package store

import (
	"context"
	"time"
)

const projectCols = `id, user_id, title, source_asset_id, selected_candidate_id, status, created_at, updated_at, deleted_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.UserID, &p.Title, &p.SourceAssetID, &p.SelectedCandidateID, &p.Status,
		&p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// InsertProject 建项目。
func InsertProject(ctx context.Context, q Queryer, id, userID string, title, sourceAssetID *string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO projects (id, user_id, title, source_asset_id, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, userID, title, sourceAssetID, t, t)
	return err
}

// GetProjectOfUser 取某用户名下未软删的项目。
// 归属下推到 SQL（AND user_id = $2），查不到即 404 —— 先查后比会用 403
// 泄漏「这个 id 存在」。PATCH / DELETE / POST /v1/generation-jobs 同一写法。
func GetProjectOfUser(ctx context.Context, q Queryer, id, userID string) (*Project, error) {
	return scanProject(q.QueryRow(ctx,
		`SELECT `+projectCols+` FROM projects WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID))
}

// ListProjects 我的作品。排序 updated_at DESC 是契约；LIMIT 100。
func ListProjects(ctx context.Context, q Queryer, userID string) ([]Project, error) {
	rows, err := q.Query(ctx,
		`SELECT `+projectCols+` FROM projects WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY updated_at DESC, id ASC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpdateProjectTitle 改标题。
func UpdateProjectTitle(ctx context.Context, q Queryer, id string, title *string, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE projects SET title = $1, updated_at = $2 WHERE id = $3`, title, t, id)
	return err
}

// SoftDeleteProject 软删（置 deleted_at），不是物理删除。
func SoftDeleteProject(ctx context.Context, q Queryer, id, userID string, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE projects SET deleted_at = $1, updated_at = $2 WHERE id = $3 AND user_id = $4`,
		t, t, id, userID)
	return err
}

// SetProjectStatus 改项目状态。
func SetProjectStatus(ctx context.Context, q Queryer, id, status string, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3`, status, t, id)
	return err
}

// SetProjectCandidate 记录选中的候选并标 ready。
func SetProjectCandidate(ctx context.Context, q Queryer, id, candidateID string, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE projects SET selected_candidate_id = $1, status = 'ready', updated_at = $2 WHERE id = $3`,
		candidateID, t, id)
	return err
}

// ProjectSelectedCandidate 读 selected_candidate_id（失败/取消时决定回落 ready 还是 draft）。
func ProjectSelectedCandidate(ctx context.Context, q Queryer, id string) (*string, error) {
	var c *string
	err := q.QueryRow(ctx, `SELECT selected_candidate_id FROM projects WHERE id = $1`, id).Scan(&c)
	if IsNoRows(err) {
		return nil, nil
	}
	return c, err
}
