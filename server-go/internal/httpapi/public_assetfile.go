package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"museframe-api/internal/apierr"
	"museframe-api/internal/imaging"
	"museframe-api/internal/store"
)

// hAssetImgToken 是 GET /v1/assets/img-token：给 <img src> 用的短时、账号绑定令牌，
// 这样长期会话令牌不必再出现在 Caddy / Cloudflare 逐字记录的 query string 里。
func (a *App) hAssetImgToken(c *Ctx) (any, error) {
	u, err := requireAccount(c)
	if err != nil {
		return nil, err
	}
	bucket := a.now().UnixMilli() / 3600000
	return map[string]any{"token": a.assetImgToken(u.ID, bucket), "ttlSeconds": 3600}, nil
}

// hAssetFile 是 GET /v1/assets/{id}/file，返回二进制 JPEG。
//
// 🔴 全站唯一允许 query 传令牌的公开路由（`?img_token=` 是 HMAC 图片令牌，
// `?token=` 是已发布客户端遗留的会话令牌，只在这条路由上被 authenticate 接受）。
// 别顺手统一成「一律只读 Header」，那会让所有 <img> 标签失效。
func (a *App) hAssetFile(c *Ctx) (any, error) {
	ctx := c.R.Context()
	var asset *store.Asset
	if it := c.URL.Query().Get("img_token"); it != "" {
		row, err := store.GetAssetForImgToken(ctx, a.st.Q(), c.Params[0])
		if err != nil && !store.IsNoRows(err) {
			return nil, err
		}
		// 令牌绑定到拥有该资产的账号，所以它只解锁那个已注册账号自己的图片，
		// 且只在当前/上一小时有效。这里过滤 owner 同时撤销了历史上为游客资产
		// 签发、尚未过期的图片令牌。
		if row == nil || !a.verifyAssetImgToken(it, row.UserID) {
			return nil, apierr.New(404, apierr.CodeAssetNotReady, "Unknown asset.")
		}
		asset = row
	} else {
		u, err := requireAccount(c)
		if err != nil {
			return nil, err
		}
		row, err := store.GetLiveAssetOfUser(ctx, a.st.Q(), c.Params[0], u.ID)
		if err != nil {
			if store.IsNoRows(err) {
				return nil, apierr.New(404, apierr.CodeAssetNotReady, "Unknown asset.")
			}
			return nil, err
		}
		asset = row
	}
	raw, err := os.ReadFile(a.assetPath(asset.StorageKey))
	if err != nil {
		return nil, apierr.New(404, apierr.CodeAssetNotReady, "File missing.")
	}
	c.W.Header().Set("Content-Type", asset.ContentType)
	c.W.Header().Set("Cache-Control", "private, max-age=3600")
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(raw)
	c.Handled = true
	return nil, nil
}

// runAnalysis 在后台跑启发式分析并落库。
func (a *App) runAnalysis(assetID string, img *imaging.RGBA) {
	ctx := context.Background()
	t := a.now()
	res := imaging.Analyze(img)
	rows, err := store.PublishedStyleRows(ctx, a.st.Q())
	if err != nil {
		a.lg.Warn("analysis: 读风格目录失败", map[string]any{"assetId": assetID})
		_ = store.SetAnalysisFailed(ctx, a.st.Q(), assetID, t)
		return
	}
	cands := make([]imaging.StyleCandidate, 0, len(rows))
	for _, r := range rows {
		spec, err := parseSpec(r.Spec)
		if err != nil {
			continue
		}
		cands = append(cands, imaging.StyleCandidate{
			StyleID: r.StyleID, VersionID: r.VersionID, EditorialRank: r.EditorialRank,
			Subjects: spec.Compatibility.Subjects, Tags: spec.Identity.Tags,
		})
	}
	recs := imaging.Recommend(res, cands)
	if len(recs) > 4 {
		recs = recs[:4]
	}
	warnings, _ := json.Marshal(res.Warnings)
	recsJSON, _ := json.Marshal(recs)
	if err := store.SetAnalysisReady(ctx, a.st.Q(), assetID, res.SubjectType, res.PersonCount,
		res.Sharpness, res.Exposure, warnings, recsJSON, t); err != nil {
		a.lg.Warn("analysis: 落库失败", map[string]any{"assetId": assetID})
		_ = store.SetAnalysisFailed(ctx, a.st.Q(), assetID, t)
	}
}
