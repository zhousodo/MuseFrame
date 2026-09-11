package httpapi

import (
	"encoding/json"

	"museframe-api/internal/apierr"
	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

// JobDetail 是 GET /v1/generation-jobs/{id} 的出参。
type JobDetail struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Stage          string          `json:"stage"`
	AttemptCount   int             `json:"attemptCount"`
	ProjectID      string          `json:"projectId"`
	StyleVersionID string          `json:"styleVersionId"`
	Controls       json.RawMessage `json:"controls"`
	Output         json.RawMessage `json:"output"`
	Candidate      *JobCandidate   `json:"candidate"`
	Billing        JobBilling      `json:"billing"`
	Error          *JobError       `json:"error"`
}

// JobCandidate 是产出候选。
// 🔴 width / height 取自 assets 行，为空时**该键直接消失**（不是 null），
// 与 Node 版 `candAsset?.width` 的 undefined 语义一致。
type JobCandidate struct {
	ID          string `json:"id"`
	AssetID     string `json:"assetId"`
	PreviewURL  string `json:"previewUrl"`
	DownloadURL string `json:"downloadUrl"`
	Width       *int   `json:"width,omitempty"`
	Height      *int   `json:"height,omitempty"`
}

// JobBilling 是计费块。
type JobBilling struct {
	UnitsCommitted int `json:"unitsCommitted"`
}

// JobError 是错误块。
type JobError struct {
	Code string `json:"code"`
}

// hGetJob 轮询任务。纯读，不写库。
func (a *App) hGetJob(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	j, err := store.GetJobOfUser(ctx, a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Job not found.")
		}
		return nil, err
	}
	out := JobDetail{
		ID: j.ID, Status: j.Status, Stage: j.Stage, AttemptCount: j.AttemptCount,
		ProjectID: j.ProjectID, StyleVersionID: j.StyleVersionID,
		Controls: rawOr(j.Controls, "{}"), Output: rawOr(j.Output, "{}"),
		Billing: JobBilling{},
	}
	if j.Status == "succeeded" {
		out.Billing.UnitsCommitted = j.ReservedUnits
	}
	if j.ErrorCode != nil && *j.ErrorCode != "" {
		out.Error = &JobError{Code: *j.ErrorCode}
	}
	if cand, err := store.FirstCandidateOfJob(ctx, a.st.Q(), j.ID); err == nil {
		jc := &JobCandidate{
			ID: cand.ID, AssetID: cand.AssetID,
			PreviewURL:  "/v1/assets/" + cand.AssetID + "/file",
			DownloadURL: "/v1/assets/" + cand.AssetID + "/file",
		}
		if asset, err := store.GetAssetByID(ctx, a.st.Q(), cand.AssetID); err == nil {
			jc.Width, jc.Height = asset.Width, asset.Height
		}
		out.Candidate = jc
	} else if !store.IsNoRows(err) {
		return nil, err
	}
	return out, nil
}

// hCancelJob 尽力取消：running 的任务会跑完。
// 🔴 releaseUnits 必须幂等且先检查是否已 commit —— 否则「成功后再取消」会退两次钱。
func (a *App) hCancelJob(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	j, err := store.GetJobOfUser(ctx, a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Job not found.")
		}
		return nil, err
	}
	if j.Status != "queued" && j.Status != "created" {
		return map[string]any{"id": j.ID, "status": j.Status}, nil
	}
	t := a.now()
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := ledger.Release(ctx, q, a.newID, u.ID, j.ID, t); err != nil {
			return err
		}
		if err := store.CancelJob(ctx, q, j.ID, t); err != nil {
			return err
		}
		// 建任务时项目被翻成 generating，而没有任何东西把它翻回来，
		// 于是一个被取消的任务会让项目永远卡在「进行中」。
		// 已经选好候选的项目仍是 ready；从未产出过的回到 draft。
		sel, err := store.ProjectSelectedCandidate(ctx, q, j.ProjectID)
		if err != nil {
			return err
		}
		status := "draft"
		if sel != nil && *sel != "" {
			status = "ready"
		}
		return store.SetProjectStatus(ctx, q, j.ProjectID, status, t)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": j.ID, "status": "cancelled"}, nil
}

// MaxFeedbackComment 与 Node 版一致。
const MaxFeedbackComment = 1000

// hFeedback 是 POST /v1/candidates/{id}/feedback。
func (a *App) hFeedback(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	rating, _ := c.Body["rating"].(string)
	if rating != "positive" && rating != "negative" {
		return nil, apierr.New(422, apierr.CodeValidation, "rating must be positive|negative")
	}
	// 归属校验。没有它，任何免费令牌都能把**别人的**候选图刷成差评 ——
	// 而那些行会喂给运维用来决定紧急下架的按风格数据。
	candID, ownerID, _, _, _, err := store.CandidateOwner(ctx, a.st.Q(), c.Params[0])
	if err != nil || ownerID != u.ID {
		if err != nil && !store.IsNoRows(err) {
			return nil, err
		}
		return nil, notFound("Candidate not found.")
	}
	codes := []string{}
	if arr, ok := c.Body["reasonCodes"].([]any); ok {
		for i, v := range arr {
			if i >= 8 {
				break
			}
			s, _ := v.(string)
			s = truncateRunes(s, 40)
			codes = append(codes, s)
		}
	}
	var comment *string
	if raw, ok := c.Body["comment"]; ok && raw != nil {
		s, _ := raw.(string)
		s = truncateRunes(s, MaxFeedbackComment)
		comment = &s
	}
	codesJSON, _ := json.Marshal(codes)
	if err := store.UpsertFeedback(ctx, a.st.Q(), a.newID, u.ID, candID, rating, codesJSON, comment, a.now()); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// ExportResult 是 POST /v1/candidates/{id}/export 的出参。
// format 与 qualityTier 是**硬编码字面量**，不反映实际文件。
// downloadUrl 是普通相对路径，不是签名 URL、没有有效期（D-13）。
type ExportResult struct {
	DownloadURL string `json:"downloadUrl"`
	Format      string `json:"format"`
	QualityTier string `json:"qualityTier"`
}

// hExport 记一次导出。两个写副作用：写 result_saved 埋点 + 把项目标 saved
// （🔴 后者会改变 GET /v1/projects 的行序，因为那个列表按 updated_at DESC）。
func (a *App) hExport(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	candID, ownerID, _, projectID, assetID, err := store.CandidateOwner(ctx, a.st.Q(), c.Params[0])
	if err != nil || ownerID != u.ID {
		if err != nil && !store.IsNoRows(err) {
			return nil, err
		}
		return nil, notFound("Candidate not found.")
	}
	t := a.now()
	props, _ := json.Marshal(map[string]string{"candidateId": candID})
	// Node 版这两条写**不在同一个事务**里；Go 版放进同一个事务
	// （差异已在 README 登记：更强的原子性，出参不变）。
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.InsertEvent(ctx, q, a.newID(), &u.ID, "result_saved", props, t); err != nil {
			return err
		}
		return store.SetProjectStatus(ctx, q, projectID, "saved", t)
	})
	if err != nil {
		return nil, err
	}
	return ExportResult{DownloadURL: "/v1/assets/" + assetID + "/file", Format: "jpeg", QualityTier: "standard"}, nil
}

// MaxEventProps 与 Node 版一致：单条 props 序列化后 2048 字节。
const MaxEventProps = 2048

// hEvents 是埋点上报。需要一个会话（免费但可归因，并落在 auth 限流之后），
// 且对序列化后的 props 封顶 —— name 一直被截断，props 曾经不截，
// 于是一个匿名的 26 MB body 会逐字落库，还能按线速重复。
func (a *App) hEvents(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireUser(c)
	if err != nil {
		return nil, err
	}
	arr, _ := c.Body["events"].([]any)
	if len(arr) > 50 {
		arr = arr[:50]
	}
	accepted := 0
	t := a.now()
	for _, raw := range arr {
		e, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := e["name"].(string)
		if name == "" {
			continue
		}
		name = truncateRunes(name, 64)
		propsRaw := e["props"]
		if propsRaw == nil {
			propsRaw = map[string]any{}
		}
		props, err := json.Marshal(propsRaw)
		if err != nil {
			props = []byte("{}")
		}
		if len(props) > MaxEventProps {
			props, _ = json.Marshal(map[string]any{"truncated": true, "bytes": len(props)})
		}
		if err := store.InsertEvent(ctx, a.st.Q(), a.newID(), &u.ID, name, props, t); err != nil {
			return nil, err
		}
		accepted++
	}
	return map[string]any{"accepted": accepted}, nil
}
