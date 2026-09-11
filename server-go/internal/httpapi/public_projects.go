package httpapi

import (
	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// ProjectCreated 是 POST /v1/projects 的出参。status 是字面量 draft，不查库。
type ProjectCreated struct {
	ID        string  `json:"id"`
	Title     *string `json:"title"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"createdAt"`
}

// hCreateProject 建项目。源图非 ready -> 409 ASSET_NOT_READY（不是 VALIDATION / NOT_FOUND）。
func (a *App) hCreateProject(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	title, err := optionalString(c.Body, "title", 200)
	if err != nil {
		return nil, err
	}
	sourceAssetID, err := optionalString(c.Body, "sourceAssetId", 100)
	if err != nil {
		return nil, err
	}
	if sourceAssetID != nil && *sourceAssetID != "" {
		ok, err := store.SourceReadyExists(ctx, a.st.Q(), *sourceAssetID, u.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(409, apierr.CodeAssetNotReady, "Source photo is not ready.")
		}
	} else {
		sourceAssetID = nil
	}
	id := a.newID()
	t := a.now()
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.InsertProject(ctx, q, id, u.ID, title, sourceAssetID, t); err != nil {
			return err
		}
		if sourceAssetID != nil {
			return store.SetAssetProject(ctx, q, *sourceAssetID, id, u.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ProjectCreated{ID: id, Title: title, Status: "draft", CreatedAt: store.ISO(t)}, nil
}

// ProjectListItem 是 GET /v1/projects 的一行。
type ProjectListItem struct {
	ID               string  `json:"id"`
	Title            *string `json:"title"`
	Status           string  `json:"status"`
	UpdatedAt        string  `json:"updatedAt"`
	StyleName        *string `json:"styleName"`
	JobStatus        *string `json:"jobStatus"`
	JobID            *string `json:"jobId"`
	SourceAssetID    *string `json:"sourceAssetId"`
	CandidateAssetID *string `json:"candidateAssetId"`
}

// hListProjects 是「我的作品」。排序 updated_at DESC 是契约；软删不得出现；空结果 []。
func (a *App) hListProjects(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListProjects(ctx, a.st.Q(), u.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectListItem, 0, len(rows))
	for i := range rows {
		p := rows[i]
		item := ProjectListItem{
			ID: p.ID, Title: p.Title, Status: p.Status, UpdatedAt: store.ISO(p.UpdatedAt),
			SourceAssetID: p.SourceAssetID,
		}
		if job, err := store.LatestJobOfProject(ctx, a.st.Q(), p.ID); err == nil {
			jid, jst := job.ID, job.Status
			item.JobID, item.JobStatus = &jid, &jst
			if name, err := store.StyleNameOfVersion(ctx, a.st.Q(), job.StyleVersionID); err == nil {
				item.StyleName = &name
			}
		} else if !store.IsNoRows(err) {
			return nil, err
		}
		if p.SelectedCandidateID != nil && *p.SelectedCandidateID != "" {
			if assetID, err := store.CandidateAssetID(ctx, a.st.Q(), *p.SelectedCandidateID); err == nil {
				item.CandidateAssetID = &assetID
			} else if !store.IsNoRows(err) {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return map[string]any{"projects": out}, nil
}

// ProjectDetail 是 GET /v1/projects/{id} 的出参。
type ProjectDetail struct {
	ID                  string          `json:"id"`
	Title               *string         `json:"title"`
	Status              string          `json:"status"`
	SourceAssetID       *string         `json:"sourceAssetId"`
	SelectedCandidateID *string         `json:"selectedCandidateId"`
	Jobs                []ProjectJobRef `json:"jobs"`
}

// ProjectJobRef 是项目详情里的任务摘要。
type ProjectJobRef struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Stage          string  `json:"stage"`
	StyleVersionID string  `json:"styleVersionId"`
	ErrorCode      *string `json:"errorCode"`
	CreatedAt      string  `json:"createdAt"`
}

// hGetProject 取项目详情。别人的项目必须 404 而不是 403（归属下推到 SQL）。
func (a *App) hGetProject(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	p, err := store.GetProjectOfUser(ctx, a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Project not found.")
		}
		return nil, err
	}
	jobs, err := store.ListJobsOfProject(ctx, a.st.Q(), p.ID)
	if err != nil {
		return nil, err
	}
	refs := make([]ProjectJobRef, 0, len(jobs))
	for _, j := range jobs {
		refs = append(refs, ProjectJobRef{
			ID: j.ID, Status: j.Status, Stage: j.Stage, StyleVersionID: j.StyleVersionID,
			ErrorCode: j.ErrorCode, CreatedAt: store.ISO(j.CreatedAt),
		})
	}
	return ProjectDetail{
		ID: p.ID, Title: p.Title, Status: p.Status, SourceAssetID: p.SourceAssetID,
		SelectedCandidateID: p.SelectedCandidateID, Jobs: refs,
	}, nil
}

// hPatchProject 改标题。
// 注意：Node 版**只处理 title**，selectedCandidateId 虽在契约里写着但实际未实现，
// 这里按源码为准（差异已在 README 登记）。
func (a *App) hPatchProject(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	p, err := store.GetProjectOfUser(ctx, a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Project not found.")
		}
		return nil, err
	}
	if raw, present := c.Body["title"]; present {
		var title *string
		if raw != nil {
			s, err := optionalString(c.Body, "title", 200)
			if err != nil {
				return nil, err
			}
			title = s
		}
		if err := store.UpdateProjectTitle(ctx, a.st.Q(), p.ID, title, a.now()); err != nil {
			return nil, err
		}
	}
	return map[string]any{"ok": true}, nil
}

// hDeleteProject 软删项目（置 deleted_at）。改成硬删会破坏审计与额度台账关联。
func (a *App) hDeleteProject(c *Ctx) (any, error) {
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	t := a.now()
	if err := store.SoftDeleteProject(c.R.Context(), a.st.Q(), c.Params[0], u.ID, t); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}
