package imaging

import (
	"bytes"
	"image/png"
)

func decodePNG(b []byte) (*RGBA, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
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
