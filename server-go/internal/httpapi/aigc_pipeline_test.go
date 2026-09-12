package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"museframe-api/internal/aigc"
	"museframe-api/internal/imaging"
	"museframe-api/internal/ledger"
	"museframe-api/internal/provider"
	"museframe-api/internal/store"
)

// 🔴 这是整条生成管线的合规回归：**假上游 → worker → 磁盘上的那份字节**。
//
// 单测能证明 aigc 包自己是对的，但证明不了「线上真正落盘的那张图带标识」——
// 中间隔着 provider 解码、裁切、质量闸、编码、落盘五步，任何一步换个顺序
// 都可能把标识挤掉，而所有接口照样 200。这里断言的是最终产物本身。
func TestPipelineWritesLabeledArtifactToDisk(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	// 假上游：返回一张纯色 PNG（真上游也可能回 PNG，DecodeAny 两种都吃）。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/images/edits") {
			http.NotFound(w, r)
			return
		}
		m := image.NewRGBA(image.Rect(0, 0, 512, 640))
		for y := 0; y < 640; y++ {
			for x := 0; x < 512; x++ {
				m.Set(x, y, color.RGBA{90, 110, 130, 255})
			}
		}
		var buf strings.Builder
		enc := base64.NewEncoder(base64.StdEncoding, &buf)
		_ = jpeg.Encode(enc, m, &jpeg.Options{Quality: 92})
		_ = enc.Close()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]string{{"b64_json": buf.String()}},
			"usage": map[string]any{"total_tokens": 10},
		})
	}))
	defer upstream.Close()
	if err := e.rt.Set(ctx, "image_provider_base_url", upstream.URL); err != nil {
		t.Fatal(err)
	}
	e.prov.SetProbeEnabled(false)

	uid, _, pid, aid := e.prepareJobInputs("pipeline@example.com", 3)
	// 源图必须真的躺在资产目录里：worker 第一步就是读它。
	src := image.NewRGBA(image.Rect(0, 0, 800, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 800; x++ {
			src.Set(x, y, color.RGBA{200, 190, 180, 255})
		}
	}
	var srcBuf strings.Builder
	if err := jpeg.Encode(&writerAdapter{&srcBuf}, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	srcAsset, err := store.GetAssetByID(ctx, e.st.Q(), aid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.assets, srcAsset.StorageKey), []byte(srcBuf.String()), 0o640); err != nil {
		t.Fatal(err)
	}

	jobID := "job-pipeline-aigc"
	if err := store.InsertJob(ctx, e.st.Q(), &store.Job{
		ID: jobID, UserID: uid, ProjectID: pid, SourceAssetID: aid,
		StyleVersionID: "ver-style-free", Status: "queued", Stage: "preparing",
		Controls:      []byte(`{"strength":"balanced","fidelity":"high","composition":"keep"}`),
		Output:        []byte(`{"aspectRatio":"4:5","qualityTier":"standard"}`),
		ReservedUnits: 1, CreatedAt: e.now, UpdatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	// worker 拒绝跑没有 reserve 台账的任务（「没付钱就不生成」），所以先记一笔。
	if err := e.st.InTx(ctx, func(q store.Queryer) error {
		return ledger.Reserve(ctx, q, NewUUID, uid, jobID, 1, e.now)
	}); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.wk.Run(runCtx)
	e.wk.Enqueue(jobID)

	var candAsset *store.Asset
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		j, err := store.GetJob(ctx, e.st.Q(), jobID)
		if err == nil && (j.Status == "succeeded" || j.Status == "failed") {
			if j.Status == "failed" {
				code := ""
				if j.ErrorCode != nil {
					code = *j.ErrorCode
				}
				t.Fatalf("任务失败了：%s", code)
			}
			cand, err := store.FirstCandidateOfJob(ctx, e.st.Q(), jobID)
			if err != nil {
				t.Fatal(err)
			}
			candAsset, err = store.GetAssetByID(ctx, e.st.Q(), cand.AssetID)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(120 * time.Millisecond)
	}
	if candAsset == nil {
		t.Fatal("等了 25 秒，任务既没成功也没失败")
	}

	// (1) DB 行必须记下标识状态 —— 后台与 App 都读它。
	if candAsset.AIGCLabel == nil || *candAsset.AIGCLabel != aigc.MarkVisibleMeta {
		t.Fatalf("assets.aigc_label = %v，想要 %q", candAsset.AIGCLabel, aigc.MarkVisibleMeta)
	}

	// (2) 磁盘上那份字节必须带隐式标识。这是最终交付物，不是中间产物。
	raw, err := os.ReadFile(filepath.Join(e.assets, candAsset.StorageKey))
	if err != nil {
		t.Fatal(err)
	}
	got, err := aigc.ExtractJPEG(raw)
	if err != nil {
		t.Fatalf("落盘的成品读不出标识：%v", err)
	}
	if got.Label.Label != "1" || got.Label.ProduceID != jobID {
		t.Errorf("隐式标识不对：%+v（ProduceID 应当是任务 id %q）", got.Label, jobID)
	}
	if got.Label.ContentProducer == "" {
		t.Error("隐式标识里没有内容制作服务提供者")
	}
	if !strings.Contains(got.XMP, "trainedAlgorithmicMedia") {
		t.Error("XMP 里没有 IPTC 的 DigitalSourceType")
	}

	// (3) 显式水印必须画进像素。上游回的是一张纯色图，所以任何偏离
	//     那个颜色的像素都只可能来自水印。
	dec, err := imaging.DecodeJPEG(raw)
	if err != nil {
		t.Fatalf("成品解不开：%v", err)
	}
	ink := 0
	for i := 0; i+3 < len(dec.Data); i += 4 {
		r, g, b := int(dec.Data[i]), int(dec.Data[i+1]), int(dec.Data[i+2])
		if abs(r-90) > 40 || abs(g-110) > 40 || abs(b-130) > 40 {
			ink++
		}
	}
	if ink < 150 {
		t.Fatalf("成品上只有 %d 个像素偏离底色，显式水印看起来没画上", ink)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// writerAdapter 把 strings.Builder 变成 io.Writer（jpeg.Encode 要的是后者）。
type writerAdapter struct{ b *strings.Builder }

func (w *writerAdapter) Write(p []byte) (int, error) { return w.b.Write(p) }

var _ = provider.CodeProviderError
