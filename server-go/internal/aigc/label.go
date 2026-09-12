package aigc

import (
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// 内嵌字体。运行镜像是 distroless static，里面一个字体文件都没有 ——
// 水印文字默认含中文，靠系统字体等于线上永远画不出显式标识。
// 子集范围、许可与可复现的子集化命令见 fontdata/NOTICE.md。
//
//go:embed fontdata/NotoSansSC-AIGCSubset.otf
var fontBytes []byte

var (
	fontOnce sync.Once
	fontFace *sfnt.Font
	fontErr  error

	// 字号 -> face。字号只取决于图像短边，实际上就那么几个值，
	// 而每次 NewFace 都要重建一套 rasterizer 状态。
	faceMu    sync.Mutex
	faceCache = map[int]font.Face{}

	// sfnt.Buffer 不是并发安全的，而 worker 是多任务并发的。
	glyphMu  sync.Mutex
	glyphBuf sfnt.Buffer
)

func loadFont() (*sfnt.Font, error) {
	fontOnce.Do(func() { fontFace, fontErr = opentype.Parse(fontBytes) })
	return fontFace, fontErr
}

func faceFor(px int) (font.Face, error) {
	f, err := loadFont()
	if err != nil {
		return nil, err
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	if fc, ok := faceCache[px]; ok {
		return fc, nil
	}
	// DPI 固定 72，于是 Size 的单位就是像素，字号计算不必再绕一圈。
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	faceCache[px] = fc
	return fc, nil
}

// UnsupportedRunes 返回 text 里内嵌字体**没有字形**的字符（去重、保序）。
//
// 🔴 它存在的唯一理由：`aigc_label_text` 是后台可改的运营项，而字体是子集。
// 没有这道校验，一句含生僻字的文案会被原样保存、页面显示「已保存」，
// 而线上每一张图的水印里那个字是**空白**—— 一个只有下载成品逐字看才能
// 发现的故障，且期间产出的每一张图都缺了法定标识的一部分。
func UnsupportedRunes(text string) []rune {
	f, err := loadFont()
	if err != nil {
		return nil
	}
	seen := map[rune]bool{}
	out := []rune{}
	glyphMu.Lock()
	defer glyphMu.Unlock()
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' || seen[r] {
			continue
		}
		seen[r] = true
		idx, err := f.GlyphIndex(&glyphBuf, r)
		if err != nil || idx == 0 {
			out = append(out, r)
		}
	}
	return out
}

// ErrFontMissingGlyphs 表示文案里有字体画不出来的字符。
var ErrFontMissingGlyphs = errors.New("aigc: 水印文案里有内嵌字体不支持的字符")

// clamp01 把不透明度夹进 (0,1]。0 = 完全透明 = 等于没加水印，
// 所以下界取 0.05 而不是 0：想关水印请用 aigc_label_enabled，
// 而不是把不透明度调成 0 —— 后者会产出一张「自称已标识」却看不见标识的图。
func clamp01(v float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return 0.85
	}
	if v < 0.05 {
		return 0.05
	}
	if v > 1 {
		return 1
	}
	return v
}

// fontPx 由短边和百分比算出字号，并夹进 [12, 短边/4]。
// 下界 12：再小的中文字在照片上就是一团噪点，等于没有标识。
func fontPx(w, h int, pct float64) int {
	short := w
	if h < short {
		short = h
	}
	if pct <= 0 || math.IsNaN(pct) {
		pct = 3.2
	}
	px := int(float64(short)*pct/100 + 0.5)
	if px < 12 {
		px = 12
	}
	if max := short / 4; max >= 12 && px > max {
		px = max
	}
	return px
}

// DrawLabel 把显式标识画进 img 的像素（就地修改）。
func DrawLabel(img *Image, o Options) error {
	text := strings.TrimSpace(o.LabelText)
	if text == "" {
		return errors.New("aigc: 水印文案为空")
	}
	if bad := UnsupportedRunes(text); len(bad) > 0 {
		return fmt.Errorf("%w: %q", ErrFontMissingGlyphs, string(bad))
	}
	px := fontPx(img.Width, img.Height, o.LabelSizePct)
	face, err := faceFor(px)
	if err != nil {
		return err
	}
	// 直接共享底层字节：img.Data 就是 RGBA 像素，不必拷一份再拷回去。
	dst := &image.RGBA{Pix: img.Data, Stride: img.Width * 4, Rect: image.Rect(0, 0, img.Width, img.Height)}

	d := &font.Drawer{Dst: dst, Face: face}
	advance := d.MeasureString(text)
	textW := advance.Ceil()
	m := face.Metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()

	short := img.Width
	if img.Height < short {
		short = img.Height
	}
	margin := short / 40
	if margin < 6 {
		margin = 6
	}
	shadow := px / 14
	if shadow < 1 {
		shadow = 1
	}

	var x, y int // y 是基线
	switch o.LabelPosition {
	case PosBottomLeft:
		x, y = margin, img.Height-margin-descent
	case PosTopLeft:
		x, y = margin, margin+ascent
	case PosTopRight:
		x, y = img.Width-margin-textW, margin+ascent
	case PosBottomCenter:
		x, y = (img.Width-textW)/2, img.Height-margin-descent
	default: // PosBottomRight
		x, y = img.Width-margin-textW, img.Height-margin-descent
	}
	if x < margin {
		x = margin
	}
	if y < ascent {
		y = ascent
	}

	alpha := clamp01(o.LabelOpacity)
	// 先画一层深色影子再画白字：成品的角落可能是任何颜色，
	// 纯白字画在雪地/高光上就是看不见的 —— 而看不见的标识不算标识。
	d.Src = image.NewUniform(premul(color.RGBA{0, 0, 0, 255}, alpha*0.55))
	d.Dot = fixed.P(x+shadow, y+shadow)
	d.DrawString(text)
	d.Src = image.NewUniform(premul(color.RGBA{255, 255, 255, 255}, alpha))
	d.Dot = fixed.P(x, y)
	d.DrawString(text)
	return nil
}

// premul 把一个不透明色乘上 alpha，返回 image/draw 要的**预乘**颜色。
// 忘了预乘的后果是水印比设定的更亮、边缘发灰。
func premul(c color.RGBA, a float64) color.RGBA {
	if a < 0 {
		a = 0
	}
	if a > 1 {
		a = 1
	}
	A := uint8(a*255 + 0.5)
	f := func(v uint8) uint8 { return uint8(float64(v)*float64(A)/255 + 0.5) }
	return color.RGBA{f(c.R), f(c.G), f(c.B), A}
}

var _ = draw.Over
