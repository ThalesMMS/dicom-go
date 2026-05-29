package roi

import (
	"image"
	"math"
)

// MeasureSpacing is the effective in-plane mm-per-pixel of a (possibly resliced)
// pane: X = horizontal (column) spacing, Y = vertical (row) spacing. It lets the
// mm helpers work on reslice panes whose effective spacing is not the series'
// native PixelSpacing (mpr-toolbar-plan.md §5.6).
//
// Note on oblique planes: when U⊥V (the panes this engine produces), distances
// computed from per-axis spacing are exact Euclidean mm. For a hypothetical
// sheared plane (U not ⊥ V) this is an approximation; the engine never builds
// such planes.
type MeasureSpacing struct {
	X        float64
	Y        float64
	ColumnMM float64
	RowMM    float64
}

func (m MeasureSpacing) valid() bool {
	x, y := m.spacingXY()
	return x > 0 && y > 0 && !math.IsNaN(x) && !math.IsNaN(y) && !math.IsInf(x, 0) && !math.IsInf(y, 0)
}

func (m MeasureSpacing) spacingXY() (float64, float64) {
	x, y := m.X, m.Y
	if x <= 0 {
		x = m.ColumnMM
	}
	if y <= 0 {
		y = m.RowMM
	}
	return x, y
}

// LengthMMWith returns the distance between two pane pixels in mm using the
// pane's effective spacing.
func LengthMMWith(a, b image.Point, sp MeasureSpacing) (float64, bool) {
	if !sp.valid() {
		return 0, false
	}
	x, y := sp.spacingXY()
	dx := float64(a.X-b.X) * x
	dy := float64(a.Y-b.Y) * y
	return math.Hypot(dx, dy), true
}

// RectangleAreaMM2With returns the area of the rectangle spanned by a,b in mm².
func RectangleAreaMM2With(a, b image.Point, sp MeasureSpacing) (float64, bool) {
	if !sp.valid() {
		return 0, false
	}
	x, y := sp.spacingXY()
	w := math.Abs(float64(a.X-b.X)) * x
	h := math.Abs(float64(a.Y-b.Y)) * y
	return w * h, true
}

// EllipseAreaMM2With returns the area of the ellipse inscribed in bbox in mm².
func EllipseAreaMM2With(bbox image.Rectangle, sp MeasureSpacing) (float64, bool) {
	if !sp.valid() {
		return 0, false
	}
	x, y := sp.spacingXY()
	rw := math.Abs(float64(bbox.Dx())) / 2 * x
	rh := math.Abs(float64(bbox.Dy())) / 2 * y
	return math.Pi * rw * rh, true
}

// PolygonLengthMMWith returns the polyline length through pts in mm.
func PolygonLengthMMWith(pts []image.Point, sp MeasureSpacing) (float64, bool) {
	if !sp.valid() || len(pts) < 2 {
		return 0, false
	}
	x, y := sp.spacingXY()
	total := 0.0
	for i := 1; i < len(pts); i++ {
		dx := float64(pts[i].X-pts[i-1].X) * x
		dy := float64(pts[i].Y-pts[i-1].Y) * y
		total += math.Hypot(dx, dy)
	}
	return total, true
}

// PolygonAreaMM2With returns the area of the closed polygon (shoelace) in mm².
func PolygonAreaMM2With(pts []image.Point, sp MeasureSpacing) (float64, bool) {
	if !sp.valid() || len(pts) < 3 {
		return 0, false
	}
	x, y := sp.spacingXY()
	areaPx := 0.0
	n := len(pts)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		areaPx += float64(pts[i].X*pts[j].Y - pts[j].X*pts[i].Y)
	}
	return math.Abs(areaPx) / 2 * x * y, true
}
