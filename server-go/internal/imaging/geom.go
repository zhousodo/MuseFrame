package imaging

import "math"

// Resize 双线性缩放，逐行复刻 Node 版 engine/styleEngine.js:21 的 resize()，
// 包括 `Math.min(height - 1.001, …)` 这个边界处理 —— 换成别的写法会在最后一行
// 产生 1 像素的差异。
func Resize(img *RGBA, tw, th int) *RGBA {
	out := &RGBA{Data: make([]byte, tw*th*4), Width: tw, Height: th}
	w, h, data := img.Width, img.Height, img.Data
	xr := float64(w) / float64(tw)
	yr := float64(h) / float64(th)
	for y := 0; y < th; y++ {
		sy := math.Min(float64(h)-1.001, float64(y)*yr)
		y0 := int(sy)
		fy := sy - float64(y0)
		y1 := min(h-1, y0+1)
		for x := 0; x < tw; x++ {
			sx := math.Min(float64(w)-1.001, float64(x)*xr)
			x0 := int(sx)
			fx := sx - float64(x0)
			x1 := min(w-1, x0+1)
			i00 := (y0*w + x0) * 4
			i10 := (y0*w + x1) * 4
			i01 := (y1*w + x0) * 4
			i11 := (y1*w + x1) * 4
			o := (y*tw + x) * 4
			for k := 0; k < 4; k++ {
				top := float64(data[i00+k]) + (float64(data[i10+k])-float64(data[i00+k]))*fx
				bot := float64(data[i01+k]) + (float64(data[i11+k])-float64(data[i01+k]))*fx
				out.Data[o+k] = clampByte(top + (bot-top)*fy)
			}
		}
	}
	return out
}

// clampByte 复刻 Uint8ClampedArray 的赋值语义：先按「就近偶数」舍入再夹到 [0,255]。
func clampByte(v float64) byte {
	if math.IsNaN(v) {
		return 0
	}
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(math.RoundToEven(v))
}

// CropTo 居中裁剪到给定比例，逐行复刻 Node 版 cropTo()。
//
// 🔴 供应商的尺寸网格与请求比例不一定一致，所以产出必须居中裁到目标比例；
// 少了这一步，`candidate.width/height` 就与 Node 版不同。
func CropTo(img *RGBA, ratioW, ratioH int) *RGBA {
	w, h := img.Width, img.Height
	cw := w
	ch := int(math.Round(float64(w) * float64(ratioH) / float64(ratioW)))
	if ch > h {
		ch = h
		cw = int(math.Round(float64(h) * float64(ratioW) / float64(ratioH)))
	}
	x0 := (w - cw) / 2
	y0 := (h - ch) / 2
	out := &RGBA{Data: make([]byte, cw*ch*4), Width: cw, Height: ch}
	for y := 0; y < ch; y++ {
		src := ((y0+y)*w + x0) * 4
		copy(out.Data[y*cw*4:(y+1)*cw*4], img.Data[src:src+cw*4])
	}
	return out
}

// AspectRatioOf 把请求的 aspectRatio 映射成整数比；original 返回 (0,0)
// 表示「跟随源图比例」。
func AspectRatioOf(aspect string) (int, int) {
	switch aspect {
	case "1:1":
		return 1, 1
	case "4:5":
		return 4, 5
	case "16:9":
		return 16, 9
	}
	return 0, 0
}
