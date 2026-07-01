package capture

import (
	"image"
	"testing"
)

func solid(w, h int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = 0x40, 0x80, 0xc0, 0xff
	}
	return im
}

func TestComposeHorizontal(t *testing.T) {
	canvas, rects := composeHorizontal([]image.Image{solid(100, 80), solid(60, 50)})
	if canvas.Rect.Dx() != 160 || canvas.Rect.Dy() != 80 {
		t.Fatalf("canvas = %dx%d, want 160x80", canvas.Rect.Dx(), canvas.Rect.Dy())
	}
	want := []Rect{{0, 0, 100, 80}, {100, 0, 60, 50}}
	if len(rects) != 2 || rects[0] != want[0] || rects[1] != want[1] {
		t.Fatalf("rects = %v, want %v", rects, want)
	}
}

func TestDownscaleAreaShrinksAndScales(t *testing.T) {
	out, scale := downscaleArea(solid(400, 200), 200)
	if out.Rect.Dx() != 200 || out.Rect.Dy() != 100 {
		t.Fatalf("out = %dx%d, want 200x100", out.Rect.Dx(), out.Rect.Dy())
	}
	if scale != 0.5 {
		t.Fatalf("scale = %v, want 0.5", scale)
	}
	// Area-averaging a solid color must preserve that color (and opaque alpha).
	r, g, b, a := out.At(10, 10).RGBA()
	if r>>8 != 0x40 || g>>8 != 0x80 || b>>8 != 0xc0 || a>>8 != 0xff {
		t.Fatalf("color = %x %x %x %x, want 40 80 c0 ff", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestDownscaleAreaNoopWhenSmall(t *testing.T) {
	src := solid(100, 50)
	out, scale := downscaleArea(src, 200)
	if out != src || scale != 1.0 {
		t.Fatalf("expected unchanged src and scale 1.0, got scale %v", scale)
	}
}

func TestScaleRects(t *testing.T) {
	got := scaleRects([]Rect{{0, 0, 100, 80}, {100, 0, 60, 50}}, 0.5)
	want := []Rect{{0, 0, 50, 40}, {50, 0, 30, 25}}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
