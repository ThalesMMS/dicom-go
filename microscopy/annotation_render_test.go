package microscopy

import (
	"image"
	"image/color"
	"testing"
)

func TestClipAnnotationLineBoundsRasterization(t *testing.T) {
	bounds := image.Rect(0, 0, 10, 10)
	from, to, ok := clipAnnotationLine(bounds, image.Pt(-1_000_000, 5), image.Pt(1_000_000, 5))
	if !ok || from != image.Pt(0, 5) || to != image.Pt(9, 5) {
		t.Fatalf("clipped line = %v -> %v, %v; want (0,5) -> (9,5), true", from, to, ok)
	}
	if _, _, ok := clipAnnotationLine(bounds, image.Pt(-1_000_000, -1), image.Pt(1_000_000, -1)); ok {
		t.Fatal("fully outside line was not rejected")
	}
}

func TestDrawAnnotationPrimitiveRendersEllipseOutline(t *testing.T) {
	target := image.NewRGBA(image.Rect(0, 0, 21, 21))
	ink := color.NRGBA{R: 255, A: 255}
	drawAnnotationPrimitive(target, []image.Point{
		image.Pt(2, 10),
		image.Pt(18, 10),
		image.Pt(10, 6),
		image.Pt(10, 14),
	}, "ELLIPSE", ink)

	for _, point := range []image.Point{image.Pt(2, 10), image.Pt(18, 10), image.Pt(10, 6), image.Pt(10, 14)} {
		if got := color.NRGBAModel.Convert(target.At(point.X, point.Y)).(color.NRGBA); got.A == 0 {
			t.Fatalf("ellipse axis endpoint %v was not rendered", point)
		}
	}
	if got := color.NRGBAModel.Convert(target.At(10, 10)).(color.NRGBA); got.A != 0 {
		t.Fatalf("ellipse center was rendered as a polygon diameter: %#v", got)
	}
}
