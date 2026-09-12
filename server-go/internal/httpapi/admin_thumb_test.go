package httpapi

import (
	"testing"

	"museframe-api/internal/imaging"
)

func mkJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := &imaging.RGBA{Data: make([]byte, w*h*4), Width: w, Height: h}
	for i := 0; i < len(img.Data); i += 4 {
		img.Data[i], img.Data[i+1], img.Data[i+2], img.Data[i+3] = byte(i%251), byte(i%97), byte(i%13), 255
	}
	b, err := imaging.EncodeJPEG(img, 90)
	if err != nil {
		t.Fatalf("EncodeJPEG: %v", err)
	}
	return b
}

// 🔴 w= 越界/拼错一律当成「要原图」，不是报错：一个手抖的 w=abc 不该让
// 整张表变成一片坏图图标。
func TestParseThumbWidth(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"", 0}, {"96", 96}, {"16", 16}, {"1024", 1024},
		{"15", 0}, {"1025", 0}, {"0", 0}, {"-96", 0}, {"abc", 0}, {"96.5", 0},
	} {
		if got := parseThumbWidth(c.in); got != c.want {
			t.Errorf("parseThumbWidth(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// 后台缩略图必须真的变小：这是「缩略图加载慢」那条验收意见的全部修法。
// 线上原图 400KB–1MB，一屏 22 张 ≈ 12MB，而它们只被画成 36×45。
func TestThumbnailDownscales(t *testing.T) {
	a := &App{}
	raw := mkJPEG(t, 1200, 1500)

	full, ct := a.thumbnail("asset-1", 0, raw)
	if len(full) != len(raw) || ct != "" {
		t.Fatal("w=0（大图查看器）必须原样回原图，不重编")
	}

	small, ct := a.thumbnail("asset-1", 96, raw)
	if ct != "image/jpeg" {
		t.Fatalf("缩略图的 Content-Type 应为 image/jpeg，实际 %q", ct)
	}
	if len(small) >= len(raw) {
		t.Fatalf("缩略图没有变小：%d >= %d", len(small), len(raw))
	}
	img, err := imaging.DecodeJPEG(small)
	if err != nil {
		t.Fatalf("缩略图解不开：%v", err)
	}
	if img.Height != 96 || img.Width > 96 {
		t.Fatalf("最长边应被缩到 96，实际 %dx%d", img.Width, img.Height)
	}

	// 第二次走进程内缓存，结果必须逐字节一致（缓存键是 id+宽度）。
	again, _ := a.thumbnail("asset-1", 96, raw)
	if string(again) != string(small) {
		t.Fatal("同一张图同一宽度两次结果不一致（缓存坏了）")
	}

	// 本来就比目标小的图不放大：放大只会把体积变大、把画质变糊。
	tiny := mkJPEG(t, 40, 50)
	out, ct := a.thumbnail("asset-2", 96, tiny)
	if len(out) != len(tiny) || ct != "" {
		t.Fatal("原图比目标还小时不应重编")
	}

	// 解不开的字节回落到原图，而不是 500。
	bad := []byte("not an image at all")
	out, ct = a.thumbnail("asset-3", 96, bad)
	if string(out) != string(bad) || ct != "" {
		t.Fatal("解不开的图必须原样回落，不能报错")
	}
}

// 缓存有上限，别让一个长期进程把内存吃光。
func TestThumbCacheBounded(t *testing.T) {
	c := &thumbCache{m: map[string][]byte{}}
	for i := 0; i < thumbCacheMax+5; i++ {
		c.put(string(rune('a'+i%26))+string(rune('a'+i/26)), []byte{byte(i)})
	}
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n > thumbCacheMax {
		t.Fatalf("缓存条目数 %d 超过上限 %d", n, thumbCacheMax)
	}
}
