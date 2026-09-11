package imaging

import (
	"testing"
)

func solid(w, h int, r, g, b byte) *RGBA {
	img := &RGBA{Data: make([]byte, w*h*4), Width: w, Height: h}
	for i := 0; i < len(img.Data); i += 4 {
		img.Data[i], img.Data[i+1], img.Data[i+2], img.Data[i+3] = r, g, b, 255
	}
	return img
}

// JPEG 魔数：绝不信扩展名（spec §14.2）。
func TestJPEGMagic(t *testing.T) {
	if !HasJPEGMagic([]byte{0xFF, 0xD8, 0x00}) {
		t.Fatal("FF D8 应被识别为 JPEG")
	}
	for _, b := range [][]byte{{0x89, 0x50}, {0x47, 0x49}, {}, {0xFF}} {
		if HasJPEGMagic(b) {
			t.Fatalf("%v 不应被识别为 JPEG", b)
		}
	}
}

// 编解码往返 + 尺寸只读头。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	img := solid(120, 80, 200, 160, 120)
	jpg, err := EncodeJPEG(img, 90)
	if err != nil {
		t.Fatal(err)
	}
	if !HasJPEGMagic(jpg) {
		t.Fatal("编码结果应是 JPEG")
	}
	back, err := DecodeJPEG(jpg)
	if err != nil {
		t.Fatal(err)
	}
	if back.Width != 120 || back.Height != 80 {
		t.Fatalf("尺寸应保持 120x80，实际 %dx%d", back.Width, back.Height)
	}
	w, h, err := DecodeJPEGSize(jpg)
	if err != nil || w != 120 || h != 80 {
		t.Fatalf("只读头拿尺寸失败：%d %d %v", w, h, err)
	}
	if _, err := DecodeJPEG([]byte("not a jpeg at all")); err == nil {
		t.Fatal("坏字节必须解码失败（返回 422 而不是 500）")
	}
}

// 分析器的告警阈值（heuristic-0.1）。
func TestAnalyzeWarnings(t *testing.T) {
	dark := Analyze(solid(800, 800, 10, 10, 10))
	if !has(dark.Warnings, "LOW_LIGHT") {
		t.Errorf("全黑图应告 LOW_LIGHT，实际 %v", dark.Warnings)
	}
	bright := Analyze(solid(800, 800, 250, 250, 250))
	if !has(bright.Warnings, "OVEREXPOSED") {
		t.Errorf("全白图应告 OVEREXPOSED，实际 %v", bright.Warnings)
	}
	small := Analyze(solid(320, 240, 128, 128, 128))
	if !has(small.Warnings, "LOW_RESOLUTION") {
		t.Errorf("短边 < 768 应告 LOW_RESOLUTION，实际 %v", small.Warnings)
	}
	big := Analyze(solid(1000, 1000, 128, 128, 128))
	if has(big.Warnings, "LOW_RESOLUTION") {
		t.Errorf("1000x1000 不应告 LOW_RESOLUTION，实际 %v —— 否则上一条是假绿", big.Warnings)
	}
	// 空结果必须是 []，不是 nil（JSON 里要序列化成 [] 而不是 null）。
	if big.Warnings == nil {
		t.Fatal("warnings 必须是空切片而不是 nil")
	}
}

func has(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// 推荐排序：分数降序，稳定，score 保留 4 位小数。
func TestRecommendOrdering(t *testing.T) {
	rows := []StyleCandidate{
		{StyleID: "a", VersionID: "va", EditorialRank: 0, Subjects: map[string]float64{"person": 0.95}, Tags: []string{"portrait"}},
		{StyleID: "b", VersionID: "vb", EditorialRank: 1, Subjects: map[string]float64{"person": 0.20}, Tags: []string{"landscape"}},
		{StyleID: "c", VersionID: "vc", EditorialRank: 2, Subjects: map[string]float64{}, Tags: []string{}},
	}
	out := Recommend(Analysis{SubjectType: "person"}, rows)
	if len(out) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].Score < out[i].Score {
			t.Fatalf("必须按分数降序，实际 %v", out)
		}
	}
	if out[0].StyleID != "a" {
		t.Fatalf("兼容度最高的应排第一，实际 %s", out[0].StyleID)
	}
	if out[0].ReasonCode != "PRESERVES_SOFT_FACE_LIGHT" {
		t.Fatalf("portrait 标签应映射到 PRESERVES_SOFT_FACE_LIGHT，实际 %s", out[0].ReasonCode)
	}
	// 缺失的兼容度默认 0.5。
	var cScore float64
	for _, r := range out {
		if r.StyleID == "c" {
			cScore = r.Score
		}
	}
	if cScore == 0 {
		t.Fatal("缺失兼容度应回落 0.5 而不是 0")
	}
	if len(Recommend(Analysis{SubjectType: "person"}, nil)) != 0 {
		t.Fatal("空目录应返回空切片")
	}
}

// CropTo 的目标尺寸必须与 Node 版逐像素一致（candidate.width/height 是出参）。
func TestCropToDimensions(t *testing.T) {
	cases := []struct {
		w, h, rw, rh int
		wantW, wantH int
	}{
		// 供应商网格 1024x1024 裁成 4:5
		{1024, 1024, 4, 5, 819, 1024},
		// 1536x1024 裁成 16:9
		{1536, 1024, 16, 9, 1536, 864},
		// 1024x1024 裁成 1:1 —— 原样
		{1024, 1024, 1, 1, 1024, 1024},
		// original：跟随源图 1536x2048 的比例
		{1024, 1536, 1536, 2048, 1024, 1365},
	}
	for _, c := range cases {
		got := CropTo(solid(c.w, c.h, 100, 100, 100), c.rw, c.rh)
		if got.Width != c.wantW || got.Height != c.wantH {
			t.Errorf("CropTo(%dx%d, %d:%d) = %dx%d，期望 %dx%d",
				c.w, c.h, c.rw, c.rh, got.Width, got.Height, c.wantW, c.wantH)
		}
		if len(got.Data) != got.Width*got.Height*4 {
			t.Errorf("裁剪后数据长度与尺寸不符")
		}
	}
}

// Resize 的降采样：尺寸正确、内容非空白（不会把图搞没）。
func TestResizeDownscale(t *testing.T) {
	src := solid(2048, 1536, 180, 140, 100)
	k := 1024.0 / 2048.0
	got := Resize(src, int(float64(2048)*k+0.5), int(float64(1536)*k+0.5))
	if got.Width != 1024 || got.Height != 768 {
		t.Fatalf("降采样尺寸应为 1024x768，实际 %dx%d", got.Width, got.Height)
	}
	// 纯色图缩放后仍应是同一颜色（双线性插值对常量场是恒等的）。
	if got.Data[0] != 180 || got.Data[1] != 140 || got.Data[2] != 100 {
		t.Fatalf("纯色图缩放后颜色应不变，实际 %d,%d,%d", got.Data[0], got.Data[1], got.Data[2])
	}
	// 负向：尺寸真的变了（否则上面的断言可能是因为压根没缩放）。
	if got.Width == src.Width {
		t.Fatal("没有真正缩放")
	}
}

// AspectRatioOf：original 返回 (0,0) 表示跟随源图。
func TestAspectRatioOf(t *testing.T) {
	for in, want := range map[string][2]int{
		"1:1": {1, 1}, "4:5": {4, 5}, "16:9": {16, 9}, "original": {0, 0}, "garbage": {0, 0},
	} {
		w, h := AspectRatioOf(in)
		if w != want[0] || h != want[1] {
			t.Errorf("AspectRatioOf(%q) = %d,%d，期望 %v", in, w, h, want)
		}
	}
}
