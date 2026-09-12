package httpapi

import (
	"strings"
	"testing"

	"museframe-api/internal/aigc"
	"museframe-api/internal/store"
)

// 🔴 变异测试：把 hGetJob 里那行 `jc.AIGCLabeled = ...` 删掉（或写成恒 false），
// 这个用例立刻红。
//
// App 的「AI 生成」角标是读这个字段决定显示与否的。字段丢了不会有任何
// 报错：角标只是**静悄悄地不再出现**，而那正是《人工智能生成合成内容
// 标识办法》要求 App 侧给出的提示。
func TestJobCandidateReportsAIGCLabel(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, tok, pid, aid := e.prepareJobInputs("aigc@example.com", 3)

	type jobDetail struct {
		Candidate *struct {
			ID          string `json:"id"`
			AssetID     string `json:"assetId"`
			AIGCLabeled bool   `json:"aigcLabeled"`
		} `json:"candidate"`
	}

	mk := func(jobID, candID, mark string) jobDetail {
		candAsset := e.nextID()
		if err := store.InsertCandidateAsset(ctx, e.st.Q(), candAsset, uid, pid,
			candAsset+".jpg", 1000, 800, 1000, mark, e.now); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
			ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid,
			StyleVersionID: "ver-style-free", Status: "succeeded", Stage: "complete",
			Controls: []byte(`{}`), Output: []byte(`{}`), ReservedUnits: 1,
			CreatedAt: e.now, UpdatedAt: e.now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertCandidate(ctx, e.st.Q(), candID, jobID, 0, candAsset, e.now); err != nil {
			t.Fatal(err)
		}
		var out jobDetail
		r := e.do("GET", "/v1/generation-jobs/"+jobID, nil, bearer(tok))
		if r.Code != 200 {
			t.Fatalf("任务详情应当 200，实际 %d: %s", r.Code, r.Body)
		}
		r.JSON(t, &out)
		if out.Candidate == nil {
			t.Fatalf("没有候选：%s", r.Body)
		}
		return out
	}

	if got := mk("job-aigc-1", "cand-aigc-1", aigc.MarkVisibleMeta); !got.Candidate.AIGCLabeled {
		t.Error("带显式水印 + 元数据的成品，aigcLabeled 应当是 true")
	}
	// 运营关掉显式水印后仍然有隐式标识，所以 App 侧照样该显示角标。
	if got := mk("job-aigc-2", "cand-aigc-2", aigc.MarkMeta); !got.Candidate.AIGCLabeled {
		t.Error("只有隐式元数据的成品，aigcLabeled 仍应当是 true")
	}
	// 历史成品（本版之前产出的）刻意不回溯，列是 NULL。
	if got := mk("job-aigc-3", "cand-aigc-3", ""); got.Candidate.AIGCLabeled {
		t.Error("历史成品（未标识）的 aigcLabeled 应当是 false，不能凭空宣称已标识")
	}
}

// 后台资产视图必须逐行显示标识状态。看不见它，运营就回答不了
// 「我们线上还有多少张成品是没标识的」这个唯一有意义的合规问题。
func TestAdminAssetsExposeAIGCLabel(t *testing.T) {
	e := newTestEnv(t)
	ctx := nil2ctx()
	uid, _, pid, _ := e.prepareJobInputs("aigcadmin@example.com", 2)
	labeled := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), labeled, uid, pid,
		labeled+".jpg", 1000, 800, 1000, aigc.MarkVisibleMeta, e.now); err != nil {
		t.Fatal(err)
	}
	legacy := e.nextID()
	if err := store.InsertCandidateAsset(ctx, e.st.Q(), legacy, uid, pid,
		legacy+".jpg", 1000, 800, 1000, "", e.now); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Note   string `json:"note"`
		Assets []struct {
			ID        string  `json:"id"`
			Kind      string  `json:"kind"`
			AIGCLabel *string `json:"aigcLabel"`
		} `json:"assets"`
	}
	r := e.do("GET", "/v1/admin/assets", nil, e.admin())
	if r.Code != 200 {
		t.Fatalf("资产视图应当 200，实际 %d: %s", r.Code, r.Body)
	}
	r.JSON(t, &out)
	seen := map[string]*string{}
	for _, a := range out.Assets {
		a := a
		seen[a.ID] = a.AIGCLabel
	}
	if v, ok := seen[labeled]; !ok || v == nil || *v != aigc.MarkVisibleMeta {
		t.Errorf("已标识的成品行 aigcLabel = %v，想要 %q", deref(v), aigc.MarkVisibleMeta)
	}
	if v, ok := seen[legacy]; !ok || v != nil && *v != "" {
		t.Errorf("历史成品行 aigcLabel 应当是空的，实际 %v", deref(v))
	}
	// 视图说明必须解释这一列，否则「未标识」三个字会被当成故障来报。
	if !strings.Contains(out.Note, "AI 标识") {
		t.Errorf("资产视图说明里没有解释 AI 标识列：%q", out.Note)
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
