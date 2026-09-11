// 图片解码与启发式分析。逐条复刻 Node 版 engine/styleEngine.js 的
// decodeJpeg / analyzeImage / recommendStyles（analyzer_version = heuristic-0.1）。
//
// 只用标准库 image/jpeg —— 原实现的 jpeg-js 同样只解基线 JPEG。
package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"math"
)

// MaxSourcePixels 与 Node 版 api.js:MAX_SOURCE_PIXELS 一致：4000 万像素。
// 上限定在管线真正的天花板（供应商输出网格 1536x1024）而不是解码器的 6000 万。
const MaxSourcePixels = 40 * 1000 * 1000

// RGBA 是解码后的图（与 jpeg-js 的 {data,width,height} 对齐，data 为 RGBA 四通道）。
type RGBA struct {
	Data          []byte
	Width, Height int
}

// ErrNotJPEG 表示字节不是可解的 JPEG。
var ErrNotJPEG = errors.New("这张图片无法读取")

// HasJPEGMagic 检查 JPEG 魔数 FF D8（spec §14.2：绝不信扩展名）。
func HasJPEGMagic(b []byte) bool { return len(b) >= 2 && b[0] == 0xFF && b[1] == 0xD8 }

// DecodeJPEG 解码 JPEG 为 RGBA。
func DecodeJPEG(b []byte) (*RGBA, error) {
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotJPEG
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	out := &RGBA{Data: make([]byte, w*h*4), Width: w, Height: h}
	i := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			out.Data[i] = byte(r >> 8)
			out.Data[i+1] = byte(g >> 8)
			out.Data[i+2] = byte(bb >> 8)
			out.Data[i+3] = byte(a >> 8)
			i += 4
		}
	}
	return out, nil
}

// DecodeJPEGSize 只读头部拿尺寸，不展开像素 —— 上传完成那条路径够用且省内存。
func DecodeJPEGSize(b []byte) (w, h int, err error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, ErrNotJPEG
	}
	return cfg.Width, cfg.Height, nil
}

// EncodeJPEG 按给定质量编码。
func EncodeJPEG(img *RGBA, quality int) ([]byte, error) {
	m := image.NewRGBA(image.Rect(0, 0, img.Width, img.Height))
	copy(m.Pix, img.Data)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Analysis 是一次启发式分析的结果。
type Analysis struct {
	SubjectType string   `json:"subjectType"`
	PersonCount int      `json:"personCount"`
	Exposure    float64  `json:"exposure"`
	Contrast    float64  `json:"contrast"`
	Sharpness   float64  `json:"sharpness"`
	Saturation  float64  `json:"saturation"`
	Warnings    []string `json:"warnings"`
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// Analyze 复刻 analyzeImage()：约 4 万采样点，逐项阈值与 Node 版逐字一致。
func Analyze(img *RGBA) Analysis {
	w, h, data := img.Width, img.Height, img.Data
	step := int(math.Floor(math.Sqrt(float64(w*h) / 40000)))
	if step < 1 {
		step = 1
	}
	var n, skin, sky, green, lapN int
	var sumL, sumL2, satSum, lapSum float64
	for y := 0; y < h; y += step {
		for x := 0; x < w; x += step {
			i := (y*w + x) * 4
			r, g, b := float64(data[i]), float64(data[i+1]), float64(data[i+2])
			l := 0.299*r + 0.587*g + 0.114*b
			sumL += l
			sumL2 += l * l
			n++
			mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
			if mx != 0 {
				satSum += (mx - mn) / mx
			}
			if r > 95 && g > 40 && b > 20 && r > g && r > b && (r-math.Min(g, b)) > 15 {
				skin++
			}
			if b > 140 && b > r+20 && b >= g {
				sky++
			}
			if g > 90 && g > r+12 && g > b+12 {
				green++
			}
			if x+step < w && y+step < h {
				ix := (y*w + min(w-1, x+step)) * 4
				iy := (min(h-1, y+step)*w + x) * 4
				lx := 0.299*float64(data[ix]) + 0.587*float64(data[ix+1]) + 0.114*float64(data[ix+2])
				ly := 0.299*float64(data[iy]) + 0.587*float64(data[iy+1]) + 0.114*float64(data[iy+2])
				lapSum += math.Abs(2*l - lx - ly)
				lapN++
			}
		}
	}
	if n == 0 {
		n = 1
	}
	meanL := sumL / float64(n) / 255
	stdL := math.Sqrt(math.Max(0, sumL2/float64(n)-math.Pow(sumL/float64(n), 2))) / 255
	sharpness := 0.0
	if lapN > 0 {
		sharpness = math.Min(1, (lapSum/float64(lapN))/24)
	}
	skinF := float64(skin) / float64(n)
	skyF := float64(sky) / float64(n)
	greenF := float64(green) / float64(n)
	isPortraitFrame := float64(h) >= float64(w)*0.95

	subjectType := "object"
	switch {
	case skinF > 0.06 && isPortraitFrame:
		subjectType = "person"
	case skinF > 0.14:
		subjectType = "person"
	case skyF+greenF > 0.22 && !isPortraitFrame:
		subjectType = "landscape"
	case skyF+greenF > 0.35:
		subjectType = "landscape"
	}

	warnings := []string{}
	if meanL < 0.18 {
		warnings = append(warnings, "LOW_LIGHT")
	}
	if meanL > 0.86 {
		warnings = append(warnings, "OVEREXPOSED")
	}
	if sharpness < 0.12 {
		warnings = append(warnings, "LOW_SHARPNESS")
	}
	if min(img.Width, img.Height) < 768 {
		warnings = append(warnings, "LOW_RESOLUTION")
	}

	personCount := 0
	if subjectType == "person" {
		personCount = 1
	}
	return Analysis{
		SubjectType: subjectType, PersonCount: personCount,
		Exposure: round4(meanL), Contrast: round4(stdL),
		Sharpness: round4(sharpness), Saturation: round4(satSum / float64(n)),
		Warnings: warnings,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DecodeAny 解 PNG 或 JPEG（上游两种都可能返回）。
func DecodeAny(b []byte) (*RGBA, error) {
	if len(b) >= 2 && b[0] == 0x89 && b[1] == 0x50 {
		return decodePNG(b)
	}
	if HasJPEGMagic(b) {
		return DecodeJPEG(b)
	}
	return nil, errors.New("未知图片格式")
}
