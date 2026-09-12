package httpapi

import (
	"strconv"
	"sync"

	"museframe-api/internal/imaging"
)

// 后台缩略图下采样。
//
// 🔴 为什么需要它：GET /v1/admin/assets/{id}/file 以前一律回**完整原图**
// （线上单张 400KB–1MB），而后台把它塞进 36×45 的格子里。一屏任务表有 22 张
// 缩略图 ≈ 12MB，资产页 19 张 ≈ 10MB —— 2026-09-12 验收时「缩略图加载慢」
// 就是这么来的：慢的不是磁盘，是把 12MB 原图推过公网只为画几十个像素。
//
// 现在前端按显示尺寸带 ?w=96 来取，这里按最长边缩到 w 再以 JPEG 重编。
// 实测同一张图 569KB → 3.4KB（约 1/160）。大图查看器不带 w=，仍取原图。
const (
	// thumbMinWidth / thumbMaxWidth 是 ?w= 的合法区间。越界一律当成「要原图」
	// 而不是报错：一个拼错的 w= 不该让整张表变成一片坏图图标。
	thumbMinWidth = 16
	thumbMaxWidth = 1024
	// thumbQuality 82 是「肉眼看不出差别」与体积之间的常用折中点。
	thumbQuality = 82
	// thumbCacheMax 是进程内缓存的条目数上限。缩略图是纯函数（资产文件一旦
	// ready 就不再变），所以只按 id+宽度 做键，不需要失效策略；超过上限整体清空
	// 而不是做 LRU —— 这里要的是「别让一个长期进程把内存吃光」，不是命中率。
	thumbCacheMax = 512
)

// parseThumbWidth 读 ?w=。返回 0 表示不缩放（原图）。
func parseThumbWidth(raw string) int {
	if raw == "" {
		return 0
	}
	w, err := strconv.Atoi(raw)
	if err != nil || w < thumbMinWidth || w > thumbMaxWidth {
		return 0
	}
	return w
}

type thumbCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

var adminThumbs = &thumbCache{m: map[string][]byte{}}

func (c *thumbCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[key]
	return b, ok
}

func (c *thumbCache) put(key string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= thumbCacheMax {
		c.m = map[string][]byte{}
	}
	c.m[key] = b
}

// thumbnail 把原始图片字节缩到最长边不超过 w 的 JPEG。
//
// 🔴 任何一步失败都回落到原图而不是报错：后台看图是运维在排查别的问题时顺手做的事，
// 为了一张解不开的图让整行变成坏图，只会多制造一个假故障。
func (a *App) thumbnail(assetID string, w int, raw []byte) ([]byte, string) {
	if w <= 0 {
		return raw, ""
	}
	key := assetID + "@" + strconv.Itoa(w)
	if b, ok := adminThumbs.get(key); ok {
		return b, "image/jpeg"
	}
	img, err := imaging.DecodeAny(raw)
	if err != nil || img == nil || img.Width <= 0 || img.Height <= 0 {
		return raw, ""
	}
	// 已经比目标还小就不要放大：放大只会把体积变大、把画质变糊。
	if img.Width <= w && img.Height <= w {
		return raw, ""
	}
	tw, th := img.Width, img.Height
	if tw >= th {
		th = int(float64(th) * float64(w) / float64(tw))
		tw = w
	} else {
		tw = int(float64(tw) * float64(w) / float64(th))
		th = w
	}
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	out, err := imaging.EncodeJPEG(imaging.Resize(img, tw, th), thumbQuality)
	if err != nil || len(out) == 0 || len(out) >= len(raw) {
		return raw, ""
	}
	adminThumbs.put(key, out)
	return out, "image/jpeg"
}
