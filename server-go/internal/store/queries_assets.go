package store

import (
	"context"
	"time"
)

const assetCols = `id, user_id, project_id, kind, status, storage_key, content_type, byte_size, width, height, sha256, aigc_label, created_at, updated_at, deleted_at`

func scanAsset(row interface{ Scan(...any) error }) (*Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.UserID, &a.ProjectID, &a.Kind, &a.Status, &a.StorageKey, &a.ContentType,
		&a.ByteSize, &a.Width, &a.Height, &a.SHA256, &a.AIGCLabel, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// InsertAsset 写一条资产（上传意向阶段是 pending）。
func InsertAsset(ctx context.Context, q Queryer, a *Asset) error {
	_, err := q.Exec(ctx,
		`INSERT INTO assets (id, user_id, project_id, kind, status, storage_key, content_type, byte_size, width, height, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		a.ID, a.UserID, a.ProjectID, a.Kind, a.Status, a.StorageKey, a.ContentType, a.ByteSize, a.Width, a.Height, a.CreatedAt, a.UpdatedAt)
	return err
}

// GetPendingSourceAsset 只取这次上传意向对应的 pending 源图。
// 没有 kind/status 谓词时，这条路由还会命中已经 ready 的资产（在已记录的
// width/height 与分析之下重写字节）和 worker 生成的候选图（覆盖已交付的成品）。
func GetPendingSourceAsset(ctx context.Context, q Queryer, id, userID string) (*Asset, error) {
	return scanAsset(q.QueryRow(ctx,
		`SELECT `+assetCols+` FROM assets WHERE id = $1 AND user_id = $2 AND kind = 'source' AND deleted_at IS NULL`, id, userID))
}

// GetAssetOfUser 取某用户名下的资产（归属下推到 SQL，查不到即 404，不泄漏存在性）。
func GetAssetOfUser(ctx context.Context, q Queryer, id, userID string) (*Asset, error) {
	return scanAsset(q.QueryRow(ctx, `SELECT `+assetCols+` FROM assets WHERE id = $1 AND user_id = $2`, id, userID))
}

// GetLiveAssetOfUser 取某用户名下未软删的资产。
func GetLiveAssetOfUser(ctx context.Context, q Queryer, id, userID string) (*Asset, error) {
	return scanAsset(q.QueryRow(ctx,
		`SELECT `+assetCols+` FROM assets WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID))
}

// GetAssetByID 取任意资产（管理后台 / worker 用）。
func GetAssetByID(ctx context.Context, q Queryer, id string) (*Asset, error) {
	return scanAsset(q.QueryRow(ctx, `SELECT `+assetCols+` FROM assets WHERE id = $1`, id))
}

// GetAssetForImgToken 取「属于某个非游客账号、未软删」的资产。
// 过滤 is_guest = 0 同时撤销了历史上为游客资产签发、尚未过期的图片令牌。
func GetAssetForImgToken(ctx context.Context, q Queryer, id string) (*Asset, error) {
	return scanAsset(q.QueryRow(ctx,
		`SELECT a.id, a.user_id, a.project_id, a.kind, a.status, a.storage_key, a.content_type, a.byte_size,
		        a.width, a.height, a.sha256, a.aigc_label, a.created_at, a.updated_at, a.deleted_at
		 FROM assets a JOIN users u ON u.id = a.user_id
		 WHERE a.id = $1 AND a.deleted_at IS NULL AND u.deleted_at IS NULL AND u.is_guest = false`, id))
}

// SourceReadyExists 判断某用户名下是否存在 ready 的源图。
func SourceReadyExists(ctx context.Context, q Queryer, assetID, userID string) (bool, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT 1 FROM assets WHERE id = $1 AND user_id = $2 AND status = 'ready' AND deleted_at IS NULL LIMIT 1`,
		assetID, userID).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// UserStorageUsed 统计某用户已占用的字节（排除指定资产自身）。
func UserStorageUsed(ctx context.Context, q Queryer, userID, exceptAssetID string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(byte_size), 0) FROM assets WHERE user_id = $1 AND deleted_at IS NULL AND id <> $2`,
		userID, exceptAssetID).Scan(&n)
	return n, err
}

// SetAssetBytes 记录上传后的实际字节数。
func SetAssetBytes(ctx context.Context, q Queryer, id string, size int64, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE assets SET byte_size = $1, updated_at = $2 WHERE id = $3`, size, t, id)
	return err
}

// CompleteAsset 把资产标 ready 并落库尺寸。
func CompleteAsset(ctx context.Context, q Queryer, id string, w, h int, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE assets SET status = 'ready', width = $1, height = $2, updated_at = $3 WHERE id = $4`, w, h, t, id)
	return err
}

// SetAssetProject 把资产挂到项目上。
func SetAssetProject(ctx context.Context, q Queryer, assetID, projectID, userID string) error {
	_, err := q.Exec(ctx, `UPDATE assets SET project_id = $1 WHERE id = $2 AND user_id = $3`, projectID, assetID, userID)
	return err
}

// InsertCandidateAsset 写 worker 产出的候选图资产。
//
// aigcLabel 是《人工智能生成合成内容标识办法》的标识状态（aigc.MarkVisibleMeta /
// aigc.MarkMeta）。🔴 它是**必填参数**而不是事后 UPDATE：成品行和它的标识状态
// 必须在同一条 INSERT 里落库，否则中间任何一次失败都会留下一张「已交付、
// 状态未知」的图 —— 而那张图的字节里到底有没有标识，事后无从判断。
func InsertCandidateAsset(ctx context.Context, q Queryer, id, userID, projectID, storageKey string, size int64, w, h int, aigcLabel string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO assets (id, user_id, project_id, kind, status, storage_key, content_type, byte_size, width, height, aigc_label, created_at, updated_at)
		 VALUES ($1,$2,$3,'candidate','ready',$4,'image/jpeg',$5,$6,$7,$8,$9,$10)`,
		id, userID, projectID, storageKey, size, w, h, aigcLabel, t, t)
	return err
}

// ---- 照片分析 --------------------------------------------------------------

// EnsureAnalysisRow 建一条 pending 分析行（已存在则忽略）。
func EnsureAnalysisRow(ctx context.Context, q Queryer, id, assetID string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO photo_analyses (id, asset_id, analyzer_version, status, created_at, updated_at)
		 VALUES ($1,$2,'heuristic-0.1','pending',$3,$4) ON CONFLICT (asset_id) DO NOTHING`, id, assetID, t, t)
	return err
}

// SetAnalysisReady 写入分析结果。
func SetAnalysisReady(ctx context.Context, q Queryer, assetID, subjectType string, personCount int, sharpness, exposure float64, warnings, recommendations []byte, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE photo_analyses SET status='ready', subject_type=$1, person_count=$2, sharpness=$3, exposure=$4,
		        warnings=$5, recommendations=$6, updated_at=$7 WHERE asset_id=$8`,
		subjectType, personCount, sharpness, exposure, warnings, recommendations, t, assetID)
	return err
}

// SetAnalysisFailed 把分析标为失败。
func SetAnalysisFailed(ctx context.Context, q Queryer, assetID string, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE photo_analyses SET status='failed', updated_at=$1 WHERE asset_id=$2`, t, assetID)
	return err
}

// GetAnalysisOfUser 取某用户名下资产的分析（归属下推到 SQL）。
func GetAnalysisOfUser(ctx context.Context, q Queryer, assetID, userID string) (*PhotoAnalysis, error) {
	var a PhotoAnalysis
	err := q.QueryRow(ctx,
		`SELECT pa.id, pa.asset_id, pa.analyzer_version, pa.status, pa.subject_type, pa.person_count,
		        pa.sharpness, pa.exposure, pa.warnings, pa.recommendations, pa.created_at, pa.updated_at
		 FROM photo_analyses pa JOIN assets s ON s.id = pa.asset_id
		 WHERE pa.asset_id = $1 AND s.user_id = $2`, assetID, userID).
		Scan(&a.ID, &a.AssetID, &a.AnalyzerVersion, &a.Status, &a.SubjectType, &a.PersonCount,
			&a.Sharpness, &a.Exposure, &a.Warnings, &a.Recommendations, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetAnalysisBrief 取 worker 需要的三项。
func GetAnalysisBrief(ctx context.Context, q Queryer, assetID string) (subjectType *string, personCount *int, exposure *float64, err error) {
	err = q.QueryRow(ctx,
		`SELECT subject_type, person_count, exposure FROM photo_analyses WHERE asset_id = $1`, assetID).
		Scan(&subjectType, &personCount, &exposure)
	return
}
