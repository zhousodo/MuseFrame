package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/imaging"
	"museframe-api/internal/store"
)

// MaxUpload 与 Node 版 api.js:MAX_UPLOAD 一致：25 MB（传输层再给 26 MB 余量）。
const MaxUpload = 25 * 1024 * 1024

// UploadIntent 是 POST /v1/assets/upload-intents 的出参。
type UploadIntent struct {
	AssetID   string `json:"assetId"`
	UploadURL string `json:"uploadUrl"`
	ExpiresAt string `json:"expiresAt"`
}

func (a *App) assetPath(key string) string { return filepath.Join(a.cfg.AssetDir, key) }

// hUploadIntent 申请一次上传。
func (a *App) hUploadIntent(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	projectID, err := optionalString(c.Body, "projectId", 100)
	if err != nil {
		return nil, err
	}
	contentType, _ := c.Body["contentType"].(string)
	if contentType != "image/jpeg" {
		return nil, apierr.New(422, apierr.CodeAssetUnsupported, "Upload a JPEG (the app converts for you).")
	}
	byteSize, present, err := optionalInt(c.Body, "byteSize")
	if err != nil {
		return nil, err
	}
	if present && byteSize != nil && *byteSize <= 0 {
		return nil, apierr.New(422, apierr.CodeValidation, "byteSize must be a positive integer.")
	}
	if byteSize != nil && *byteSize > MaxUpload {
		return nil, apierr.New(422, apierr.CodeAssetUnsupported, "Images up to 20 MB are supported.")
	}
	if projectID != nil && *projectID != "" {
		if _, err := store.GetProjectOfUser(ctx, a.st.Q(), *projectID, u.ID); err != nil {
			if store.IsNoRows(err) {
				return nil, notFound("Project not found.")
			}
			return nil, err
		}
	} else {
		projectID = nil
	}
	assetID := a.newID()
	now := a.now()
	asset := &store.Asset{
		ID: assetID, UserID: u.ID, ProjectID: projectID, Kind: "source", Status: "pending",
		StorageKey: assetID + ".jpg", ContentType: contentType, ByteSize: byteSize,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.InsertAsset(ctx, a.st.Q(), asset); err != nil {
		return nil, err
	}
	return UploadIntent{
		AssetID: assetID, UploadURL: "/v1/assets/" + assetID + "/upload",
		ExpiresAt: store.ISO(now.Add(15 * time.Minute)),
	}, nil
}

// hUpload 是 PUT /v1/assets/{id}/upload，请求体是裸 JPEG 二进制。
func (a *App) hUpload(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	// 只取这次上传意向对应的 pending 源图。没有 kind/status 谓词时，
	// 这条路由还会命中已经 ready 的资产与 worker 生成的候选图。
	asset, err := store.GetPendingSourceAsset(ctx, a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, apierr.New(404, apierr.CodeAssetNotReady, "Unknown asset.")
		}
		return nil, err
	}
	if asset.Status != "pending" {
		return nil, apierr.New(409, apierr.CodeAssetNotReady, "This upload has already been completed.")
	}
	if len(c.Raw) < 100 {
		return nil, apierr.New(422, apierr.CodeAssetUnsupported, "Empty upload.")
	}
	if len(c.Raw) > MaxUpload {
		return nil, apierr.New(422, apierr.CodeAssetUnsupported, "Images up to 20 MB are supported.")
	}
	// MIME 魔数：JPEG 必须以 FF D8 开头（spec §14.2 —— 绝不信扩展名）。
	if !imaging.HasJPEGMagic(c.Raw) {
		return nil, apierr.New(422, apierr.CodeAssetUnsupported, "Not a JPEG image.")
	}
	// 按账号的字节天花板。会话的铸造成本是零，没有这条，一个匿名调用者
	// 可以每次请求往 data/ 写 25 MB，直到把磁盘写满。
	used, err := store.UserStorageUsed(ctx, a.st.Q(), u.ID, asset.ID)
	if err != nil {
		return nil, err
	}
	if used+int64(len(c.Raw)) > a.cfg.MaxUserStorageBytes {
		return nil, apierr.New(413, apierr.CodeStorageQuotaExceeded,
			"Storage limit reached — delete some projects and try again.")
	}
	if err := os.WriteFile(a.assetPath(asset.StorageKey), c.Raw, 0o640); err != nil {
		return nil, err
	}
	if err := store.SetAssetBytes(ctx, a.st.Q(), asset.ID, int64(len(c.Raw)), a.now()); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// CompleteResult 是 POST /v1/assets/{id}/complete 的出参。
type CompleteResult struct {
	AssetID string `json:"assetId"`
	Status  string `json:"status"`
	Width   *int   `json:"width,omitempty"`
	Height  *int   `json:"height,omitempty"`
}

// hComplete 落库尺寸并触发分析；对已 ready 的资产是幂等的。
func (a *App) hComplete(c *Ctx) (any, error) {
	ctx := c.R.Context()
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	asset, err := store.GetAssetOfUser(ctx, a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, apierr.New(404, apierr.CodeAssetNotReady, "Unknown asset.")
		}
		return nil, err
	}
	raw, err := os.ReadFile(a.assetPath(asset.StorageKey))
	if err != nil {
		return nil, apierr.New(409, apierr.CodeAssetNotReady, "Upload has not arrived yet.")
	}
	if asset.Status == "ready" {
		return CompleteResult{AssetID: asset.ID, Status: "ready"}, nil // 幂等
	}
	// 截断 / 畸形文件（PUT 只查了 FF D8 魔数）在 Node 里会作为 500 带着解码器
	// 原文冒出来；不可用图片的契约状态是 422。
	img, err := imaging.DecodeJPEG(raw)
	if err != nil {
		a.lg.Warn("complete: 解码失败", map[string]any{"assetId": asset.ID})
		return nil, apierr.New(422, apierr.CodeAssetUnsupported, "This image could not be read.")
	}
	if img.Width*img.Height > imaging.MaxSourcePixels {
		return nil, apierr.New(422, apierr.CodeAssetUnsupported,
			"This image is too large — please use one under 40 megapixels.")
	}
	t := a.now()
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.CompleteAsset(ctx, q, asset.ID, img.Width, img.Height, t); err != nil {
			return err
		}
		return store.EnsureAnalysisRow(ctx, q, a.newID(), asset.ID, t)
	})
	if err != nil {
		return nil, err
	}
	// 把已经解好的图交出去：Node 版曾经在分析里再读一次文件并再解一次码，
	// 让事件循环上的 CPU 尖峰翻倍。
	go a.runAnalysis(asset.ID, img)
	w, h := img.Width, img.Height
	return CompleteResult{AssetID: asset.ID, Status: "ready", Width: &w, Height: &h}, nil
}

// AnalysisResult 是 GET /v1/assets/{id}/analysis 的出参。
type AnalysisResult struct {
	Status          string          `json:"status"`
	SubjectType     *string         `json:"subjectType"`
	PersonCount     *int            `json:"personCount"`
	Quality         AnalysisQuality `json:"quality"`
	Recommendations json.RawMessage `json:"recommendations"`
	Warnings        json.RawMessage `json:"warnings"`
}

// AnalysisQuality 是 quality 块。
type AnalysisQuality struct {
	Sharpness         *float64 `json:"sharpness"`
	Exposure          *float64 `json:"exposure"`
	MinimumQualityMet bool     `json:"minimumQualityMet"`
}

// hAnalysis 读分析结果。无分析 -> 404 ASSET_NOT_READY（不是 NOT_FOUND）。
func (a *App) hAnalysis(c *Ctx) (any, error) {
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	an, err := store.GetAnalysisOfUser(c.R.Context(), a.st.Q(), c.Params[0], u.ID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, apierr.New(404, apierr.CodeAssetNotReady, "No analysis for this asset.")
		}
		return nil, err
	}
	var warnings []string
	_ = json.Unmarshal(an.Warnings, &warnings)
	met := an.Status == "ready" && !contains(warnings, "LOW_RESOLUTION")
	return AnalysisResult{
		Status: an.Status, SubjectType: an.SubjectType, PersonCount: an.PersonCount,
		Quality:         AnalysisQuality{Sharpness: an.Sharpness, Exposure: an.Exposure, MinimumQualityMet: met},
		Recommendations: rawOr(an.Recommendations, "[]"), Warnings: rawOr(an.Warnings, "[]"),
	}, nil
}

func rawOr(b []byte, def string) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(def)
	}
	return json.RawMessage(b)
}

var _ = http.StatusOK
