package capture

import (
	"image"
	"image/draw"
)

// composeHorizontal lays images left-to-right into one RGBA canvas (top-aligned),
// returning the canvas and each source image's rectangle within it. A single
// image just yields a full-canvas rect.
func composeHorizontal(imgs []image.Image) (*image.RGBA, []Rect) {
	width, height := 0, 0
	for _, im := range imgs {
		b := im.Bounds()
		width += b.Dx()
		if b.Dy() > height {
			height = b.Dy()
		}
	}
	if width == 0 || height == 0 {
		return nil, nil
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	rects := make([]Rect, 0, len(imgs))
	x := 0
	for _, im := range imgs {
		b := im.Bounds()
		draw.Draw(canvas, image.Rect(x, 0, x+b.Dx(), b.Dy()), im, b.Min, draw.Src)
		rects = append(rects, Rect{X: x, Y: 0, W: b.Dx(), H: b.Dy()})
		x += b.Dx()
	}
	return canvas, rects
}

// downscaleArea shrinks src so its longest side is <= max using area (box)
// averaging — far kinder to small code/UI text than nearest-neighbor, which
// aliases away thin glyph strokes and starves OCR. Returns src and scale=1 when
// it already fits. The returned scale maps source pixels → output pixels.
func downscaleArea(src *image.RGBA, max int) (*image.RGBA, float64) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	longest := w
	if h > w {
		longest = h
	}
	if longest <= max || longest == 0 {
		return src, 1.0
	}
	scale := float64(max) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xRatio := float64(w) / float64(nw)
	yRatio := float64(h) / float64(nh)
	for dy := 0; dy < nh; dy++ {
		sy0 := int(float64(dy) * yRatio)
		sy1 := int(float64(dy+1) * yRatio)
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		if sy1 > h {
			sy1 = h
		}
		for dx := 0; dx < nw; dx++ {
			sx0 := int(float64(dx) * xRatio)
			sx1 := int(float64(dx+1) * xRatio)
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sx1 > w {
				sx1 = w
			}
			var r, g, b, count uint32
			for sy := sy0; sy < sy1; sy++ {
				rowOff := src.PixOffset(sx0, sy)
				for sx := sx0; sx < sx1; sx++ {
					r += uint32(src.Pix[rowOff])
					g += uint32(src.Pix[rowOff+1])
					b += uint32(src.Pix[rowOff+2])
					count++
					rowOff += 4
				}
			}
			if count == 0 {
				count = 1
			}
			di := dst.PixOffset(dx, dy)
			dst.Pix[di] = uint8(r / count)
			dst.Pix[di+1] = uint8(g / count)
			dst.Pix[di+2] = uint8(b / count)
			dst.Pix[di+3] = 0xff
		}
	}
	return dst, scale
}

// scaleRects multiplies each rect by `scale` (applied after the canvas is
// downscaled, so the rects still line up with the stored image).
func scaleRects(rects []Rect, scale float64) []Rect {
	if scale == 1.0 || len(rects) == 0 {
		return rects
	}
	out := make([]Rect, len(rects))
	for i, r := range rects {
		out[i] = Rect{
			X: int(float64(r.X) * scale),
			Y: int(float64(r.Y) * scale),
			W: int(float64(r.W) * scale),
			H: int(float64(r.H) * scale),
		}
	}
	return out
}
