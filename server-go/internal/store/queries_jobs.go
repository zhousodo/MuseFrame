package store

import (
	"context"
	"time"
)

const jobCols = `id, user_id, project_id, source_asset_id, style_version_id, parent_job_id, status, stage,
	controls, output, attempt_count, reserved_units, error_code, cost_minor, created_at, updated_at, finished_at`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.UserID, &j.ProjectID, &j.SourceAssetID, &j.StyleVersionID, &j.ParentJobID,
		&j.Status, &j.Stage, &j.Controls, &j.Output, &j.AttemptCount, &j.ReservedUnits, &j.ErrorCode,
		&j.CostMinor, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// InsertJob 建任务行（状态直接写 queued，与 Node 版 api.js:876 一致；
// created 只可能出现在历史行，开机恢复仍要扫它，所以枚举不能删）。
func InsertJob(ctx context.Context, q Queryer, j *Job) error {
	_, err := q.Exec(ctx,
		`INSERT INTO generation_jobs (id, user_id, project_id, source_asset_id, style_version_id, parent_job_id,
		   status, stage, controls, output, reserved_units, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		j.ID, j.UserID, j.ProjectID, j.SourceAssetID, j.StyleVersionID, j.ParentJobID,
		j.Status, j.Stage, j.Controls, j.Output, j.ReservedUnits, j.CreatedAt, j.UpdatedAt)
	return err
}

// GetJobOfUser 取某用户名下的任务。
func GetJobOfUser(ctx context.Context, q Queryer, id, userID string) (*Job, error) {
	return scanJob(q.QueryRow(ctx, `SELECT `+jobCols+` FROM generation_jobs WHERE id = $1 AND user_id = $2`, id, userID))
}

// GetJob 取任意任务（worker 用）。
func GetJob(ctx context.Context, q Queryer, id string) (*Job, error) {
	return scanJob(q.QueryRow(ctx, `SELECT `+jobCols+` FROM generation_jobs WHERE id = $1`, id))
}

// ListJobsOfProject 取项目下全部任务，created_at DESC。
func ListJobsOfProject(ctx context.Context, q Queryer, projectID string) ([]Job, error) {
	return queryJobs(ctx, q, `SELECT `+jobCols+` FROM generation_jobs WHERE project_id = $1 ORDER BY created_at DESC, id ASC`, projectID)
}

// LatestJobOfProject 取项目最近一条任务。
func LatestJobOfProject(ctx context.Context, q Queryer, projectID string) (*Job, error) {
	return scanJob(q.QueryRow(ctx,
		`SELECT `+jobCols+` FROM generation_jobs WHERE project_id = $1 ORDER BY created_at DESC, id ASC LIMIT 1`, projectID))
}

// ListJobsByStatuses 取指定状态集合的任务（开机恢复用）。
func ListJobsByStatuses(ctx context.Context, q Queryer, statuses []string) ([]Job, error) {
	return queryJobs(ctx, q, `SELECT `+jobCols+` FROM generation_jobs WHERE status = ANY($1) ORDER BY created_at ASC`, statuses)
}

func queryJobs(ctx context.Context, q Queryer, sql string, args ...any) ([]Job, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// SetJobStage 改任务状态与阶段。
func SetJobStage(ctx context.Context, q Queryer, id, status, stage string, t time.Time) error {
	_, err := q.Exec(ctx, `UPDATE generation_jobs SET status=$1, stage=$2, updated_at=$3 WHERE id=$4`, status, stage, t, id)
	return err
}

// SetJobAttempts 记录尝试次数。
func SetJobAttempts(ctx context.Context, q Queryer, id string, attempts int) error {
	_, err := q.Exec(ctx, `UPDATE generation_jobs SET attempt_count=$1 WHERE id=$2`, attempts, id)
	return err
}

// FinishJobSucceeded 收口一个成功任务。
func FinishJobSucceeded(ctx context.Context, q Queryer, id string, costMinor int64, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE generation_jobs SET status='succeeded', stage='complete', cost_minor=$1, error_code=NULL,
		        finished_at=$2, updated_at=$3 WHERE id=$4`, costMinor, t, t, id)
	return err
}

// FinishJobFailed 收口一个失败任务。
func FinishJobFailed(ctx context.Context, q Queryer, id, code string, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE generation_jobs SET status='failed', stage='failed', error_code=$1, finished_at=$2, updated_at=$3 WHERE id=$4`,
		code, t, t, id)
	return err
}

// CancelJob 取消一个尚未开跑的任务。
// 注意 stage 写的是 failed 不是 cancelled（与 Node 版 api.js:929 一致）。
func CancelJob(ctx context.Context, q Queryer, id string, t time.Time) error {
	_, err := q.Exec(ctx,
		`UPDATE generation_jobs SET status='cancelled', stage='failed', finished_at=$1, updated_at=$2 WHERE id=$3`, t, t, id)
	return err
}

// InsertCandidate 写一条候选。
func InsertCandidate(ctx context.Context, q Queryer, id, jobID string, index int, assetID string, t time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO generation_candidates (id, job_id, candidate_index, asset_id, quality_passed, created_at)
		 VALUES ($1,$2,$3,$4,true,$5)`, id, jobID, index, assetID, t)
	return err
}

// FirstCandidateOfJob 取任务的首个候选。
func FirstCandidateOfJob(ctx context.Context, q Queryer, jobID string) (*Candidate, error) {
	var c Candidate
	err := q.QueryRow(ctx,
		`SELECT id, job_id, candidate_index, asset_id, quality_passed, created_at
		 FROM generation_candidates WHERE job_id = $1 ORDER BY candidate_index ASC LIMIT 1`, jobID).
		Scan(&c.ID, &c.JobID, &c.Index, &c.AssetID, &c.QualityPassed, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CandidateOwner 取候选及其所属任务的归属信息（反馈 / 导出的归属校验用）。
func CandidateOwner(ctx context.Context, q Queryer, candidateID string) (candID, userID, jobID, projectID, assetID string, err error) {
	err = q.QueryRow(ctx,
		`SELECT c.id, j.user_id, j.id, j.project_id, c.asset_id
		 FROM generation_candidates c JOIN generation_jobs j ON j.id = c.job_id WHERE c.id = $1`, candidateID).
		Scan(&candID, &userID, &jobID, &projectID, &assetID)
	return
}

// ProviderOutcome 是一条「上游到底成不成」的历史证据，用来给健康账本播种。
// 没有它，容器一重启健康判据就回到「零样本」，于是一个连着失败一周的部署
// 在重启后照样报绿 —— 本轮故障暴露的正是这个盲区。
type ProviderOutcome struct {
	At   time.Time
	OK   bool
	Code string
}

// providerOutcomeCodes 是**与上游供给相关**的失败码白名单。
// ASSET_NOT_READY / INTERNAL_ERROR / BAD_DIMENSIONS / GENERATION_REJECTED 都不是
// 「上游能不能产图」的证据（前三个是我们自己的问题，最后一个说明上游正常工作、
// 只是拒了这一条指令），放进来会把健康判据污染成噪声。
var providerOutcomeCodes = []string{
	"PROVIDER_UNAVAILABLE", "PROVIDER_TIMEOUT", "PROVIDER_ERROR",
}

// ListRecentProviderOutcomes 取最近 limit 条「成功 / 上游类失败」的收口行，**按时间升序**返回
// （健康账本的环形缓冲要求最新的在最后）。
func ListRecentProviderOutcomes(ctx context.Context, q Queryer, limit int, since time.Time) ([]ProviderOutcome, error) {
	rows, err := q.Query(ctx, `
		SELECT finished_at, status, COALESCE(error_code, '')
		  FROM (
			SELECT finished_at, status, error_code
			  FROM generation_jobs
			 WHERE finished_at IS NOT NULL
			   AND finished_at >= $2
			   AND (status = 'succeeded' OR error_code = ANY($3))
			 ORDER BY finished_at DESC
			 LIMIT $1
		  ) t
		 ORDER BY finished_at ASC`, limit, since, providerOutcomeCodes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderOutcome
	for rows.Next() {
		var o ProviderOutcome
		var status, code string
		if err := rows.Scan(&o.At, &status, &code); err != nil {
			return nil, err
		}
		o.OK = status == "succeeded"
		o.Code = code
		out = append(out, o)
	}
	return out, rows.Err()
}
