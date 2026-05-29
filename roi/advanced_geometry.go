package roi

import (
	"image"
	"math"
)

// Point2D is a sub-pixel point used by derived ROI geometry. Persisted ROI
// control points remain image.Point values; derived axes and projections retain
// their fractional position until the UI maps them to the viewport.
type Point2D struct {
	X float64
	Y float64
}

// Line2D is a derived line segment between two sub-pixel points.
type Line2D struct {
	Start Point2D
	End   Point2D
}

// Rectangle2D is an axis-aligned sub-pixel rectangle. Callers may provide its
// corners in either order; selector intersection helpers canonicalize them.
type Rectangle2D struct {
	Min Point2D
	Max Point2D
}

// MeanAxisLines preserves the legacy four-point mean-axis construction. The
// first line joins the midpoints of edges 0-1 and 2-3; the second joins the
// midpoints of edges 0-3 and 1-2. The current three-point Axis ROI uses
// TrianglePerpendicularBisectors instead.
func MeanAxisLines(points [4]image.Point) (Line2D, Line2D, bool) {
	primary := Line2D{
		Start: midpoint(points[0], points[1]),
		End:   midpoint(points[2], points[3]),
	}
	secondary := Line2D{
		Start: midpoint(points[0], points[3]),
		End:   midpoint(points[1], points[2]),
	}
	valid := primary.Start != primary.End && secondary.Start != secondary.End
	return primary, secondary, valid
}

// TrianglePerpendicularBisectors derives the three axes displayed by the
// three-point Axis ROI. Each line is the perpendicular bisector of one side,
// in side order 0-1, 1-2, and 2-0. The returned segments are extended far
// enough to include the triangle's circumcenter and a visible margin beyond
// it. A triangle with coincident or collinear vertices has no unique result.
func TrianglePerpendicularBisectors(points [3]image.Point) ([3]Line2D, bool) {
	var result [3]Line2D
	circumcenter, ok := triangleCircumcenter(points)
	if !ok {
		return result, false
	}

	longestSide := 0.0
	for i := range points {
		j := (i + 1) % len(points)
		length := math.Hypot(float64(points[j].X-points[i].X), float64(points[j].Y-points[i].Y))
		if length == 0 {
			return result, false
		}
		longestSide = math.Max(longestSide, length)
	}

	for i := range points {
		j := (i + 1) % len(points)
		start := point2D(points[i])
		end := point2D(points[j])
		dx := end.X - start.X
		dy := end.Y - start.Y
		length := math.Hypot(dx, dy)
		middle := Point2D{X: (start.X + end.X) / 2, Y: (start.Y + end.Y) / 2}
		normal := Point2D{X: -dy / length, Y: dx / length}
		centerDistance := math.Abs((circumcenter.X-middle.X)*normal.X + (circumcenter.Y-middle.Y)*normal.Y)
		halfLength := math.Max(longestSide, centerDistance+longestSide/2)
		result[i] = Line2D{
			Start: Point2D{X: middle.X - normal.X*halfLength, Y: middle.Y - normal.Y*halfLength},
			End:   Point2D{X: middle.X + normal.X*halfLength, Y: middle.Y + normal.Y*halfLength},
		}
	}
	return result, true
}

// ProjectPointToLine returns the orthogonal projection of point onto the
// infinite line through start and end. It intentionally does not clamp the
// projection to the segment because Perpendicular Lines keeps B/C orthogonal
// even when their feet move beyond the displayed A endpoints.
func ProjectPointToLine(point, start, end image.Point) (Point2D, bool) {
	dx := float64(end.X - start.X)
	dy := float64(end.Y - start.Y)
	denominator := dx*dx + dy*dy
	if denominator == 0 {
		return Point2D{}, false
	}
	u := (float64(point.X-start.X)*dx + float64(point.Y-start.Y)*dy) / denominator
	return Point2D{
		X: float64(start.X) + u*dx,
		Y: float64(start.Y) + u*dy,
	}, true
}

// PerpendicularLinePoints builds the six persisted points of the OsiriX-style
// Perpendicular Lines ROI. Points 1 and 3 are the editable B/C endpoints,
// points 4 and 5 define main line A, and points 0 and 2 are derived feet.
func PerpendicularLinePoints(bEnd, cEnd, mainStart, mainEnd image.Point) ([6]image.Point, bool) {
	var points [6]image.Point
	bFoot, ok := ProjectPointToLine(bEnd, mainStart, mainEnd)
	if !ok {
		return points, false
	}
	cFoot, ok := ProjectPointToLine(cEnd, mainStart, mainEnd)
	if !ok {
		return points, false
	}
	points[0] = image.Pt(int(math.Round(bFoot.X)), int(math.Round(bFoot.Y)))
	points[1] = bEnd
	points[2] = image.Pt(int(math.Round(cFoot.X)), int(math.Round(cFoot.Y)))
	points[3] = cEnd
	points[4] = mainStart
	points[5] = mainEnd
	return points, true
}

// DynamicAngleDeg returns the acute angle between two directed lines and the
// opposite angle displayed by the Dynamic Angle ROI. Pixel spacing is applied
// before measuring, so anisotropic source pixels do not skew the result.
func DynamicAngleDeg(line1Start, line1End, line2Start, line2End image.Point, spacing MeasureSpacing) (float64, float64, bool) {
	if !spacing.valid() {
		return 0, 0, false
	}
	x, y := spacing.spacingXY()
	v1x := float64(line1End.X-line1Start.X) * x
	v1y := float64(line1End.Y-line1Start.Y) * y
	v2x := float64(line2End.X-line2Start.X) * x
	v2y := float64(line2End.Y-line2Start.Y) * y
	if math.Hypot(v1x, v1y) == 0 || math.Hypot(v2x, v2y) == 0 {
		return 0, 0, false
	}
	delta := math.Mod(math.Abs(math.Atan2(v1y, v1x)-math.Atan2(v2y, v2x))*180/math.Pi, 180)
	if delta > 90 {
		delta = 180 - delta
	}
	return delta, 360 - delta, true
}

// SphereSliceRadiusMM returns the radius of a sphere's circular intersection
// with a plane at the given signed distance from its center.
func SphereSliceRadiusMM(radiusMM, planeOffsetMM float64) (float64, bool) {
	if radiusMM <= 0 || math.IsNaN(radiusMM) || math.IsInf(radiusMM, 0) || math.IsNaN(planeOffsetMM) || math.IsInf(planeOffsetMM, 0) {
		return 0, false
	}
	offset := math.Abs(planeOffsetMM)
	if offset > radiusMM {
		return 0, false
	}
	value := radiusMM*radiusMM - offset*offset
	if value < 0 {
		value = 0
	}
	return math.Sqrt(value), true
}

// RepulsePolyline applies a radial, linearly-falling displacement to control
// points inside radius. strength is the maximum displacement at the center;
// points on or outside the radius are unchanged. The input is never aliased or
// modified. A point exactly at the center uses the positive X direction, which
// keeps the otherwise undefined radial case deterministic.
func RepulsePolyline(points []Point2D, center Point2D, radius, strength float64) ([]Point2D, bool) {
	if len(points) == 0 || !finitePoint(center) || !finiteNumber(radius) || radius <= 0 || !finiteNumber(strength) || strength < 0 {
		return nil, false
	}
	result := make([]Point2D, len(points))
	for i, point := range points {
		if !finitePoint(point) {
			return nil, false
		}
		result[i] = point
		dx := point.X - center.X
		dy := point.Y - center.Y
		distance := math.Hypot(dx, dy)
		if distance >= radius || strength == 0 {
			continue
		}
		falloff := 1 - distance/radius
		if distance == 0 {
			result[i].X += strength
			continue
		}
		displacement := strength * falloff / distance
		result[i].X += dx * displacement
		result[i].Y += dy * displacement
	}
	return result, true
}

// PolylineIntersectsRectangle reports whether any point or segment of an open
// polyline touches or crosses rectangle. Rectangle boundaries are inclusive.
func PolylineIntersectsRectangle(points []Point2D, rectangle Rectangle2D) bool {
	rectangle, ok := canonicalRectangle(rectangle)
	if !ok || len(points) == 0 {
		return false
	}
	for _, point := range points {
		if !finitePoint(point) {
			return false
		}
		if pointInRectangle(point, rectangle) {
			return true
		}
	}
	for i := 1; i < len(points); i++ {
		if segmentIntersectsRectangle(points[i-1], points[i], rectangle) {
			return true
		}
	}
	return false
}

// PolygonIntersectsRectangle reports whether a closed polygon and rectangle
// overlap, cross, contain one another, or touch at their boundaries.
func PolygonIntersectsRectangle(points []Point2D, rectangle Rectangle2D) bool {
	rectangle, ok := canonicalRectangle(rectangle)
	if !ok || len(points) < 3 {
		return false
	}
	for _, point := range points {
		if !finitePoint(point) {
			return false
		}
		if pointInRectangle(point, rectangle) {
			return true
		}
	}
	for i := range points {
		if segmentIntersectsRectangle(points[i], points[(i+1)%len(points)], rectangle) {
			return true
		}
	}
	return pointInPolygon2D(rectangle.Min, points)
}

func triangleCircumcenter(points [3]image.Point) (Point2D, bool) {
	ax, ay := float64(points[0].X), float64(points[0].Y)
	bx, by := float64(points[1].X), float64(points[1].Y)
	cx, cy := float64(points[2].X), float64(points[2].Y)
	d := 2 * (ax*(by-cy) + bx*(cy-ay) + cx*(ay-by))
	if d == 0 {
		return Point2D{}, false
	}
	aa := ax*ax + ay*ay
	bb := bx*bx + by*by
	cc := cx*cx + cy*cy
	center := Point2D{
		X: (aa*(by-cy) + bb*(cy-ay) + cc*(ay-by)) / d,
		Y: (aa*(cx-bx) + bb*(ax-cx) + cc*(bx-ax)) / d,
	}
	return center, finitePoint(center)
}

func canonicalRectangle(rectangle Rectangle2D) (Rectangle2D, bool) {
	if !finitePoint(rectangle.Min) || !finitePoint(rectangle.Max) {
		return Rectangle2D{}, false
	}
	return Rectangle2D{
		Min: Point2D{X: math.Min(rectangle.Min.X, rectangle.Max.X), Y: math.Min(rectangle.Min.Y, rectangle.Max.Y)},
		Max: Point2D{X: math.Max(rectangle.Min.X, rectangle.Max.X), Y: math.Max(rectangle.Min.Y, rectangle.Max.Y)},
	}, true
}

func pointInRectangle(point Point2D, rectangle Rectangle2D) bool {
	return point.X >= rectangle.Min.X && point.X <= rectangle.Max.X && point.Y >= rectangle.Min.Y && point.Y <= rectangle.Max.Y
}

func segmentIntersectsRectangle(start, end Point2D, rectangle Rectangle2D) bool {
	if pointInRectangle(start, rectangle) || pointInRectangle(end, rectangle) {
		return true
	}
	topLeft := rectangle.Min
	topRight := Point2D{X: rectangle.Max.X, Y: rectangle.Min.Y}
	bottomRight := rectangle.Max
	bottomLeft := Point2D{X: rectangle.Min.X, Y: rectangle.Max.Y}
	return segmentsIntersect(start, end, topLeft, topRight) ||
		segmentsIntersect(start, end, topRight, bottomRight) ||
		segmentsIntersect(start, end, bottomRight, bottomLeft) ||
		segmentsIntersect(start, end, bottomLeft, topLeft)
}

func segmentsIntersect(a, b, c, d Point2D) bool {
	o1 := orientation(a, b, c)
	o2 := orientation(a, b, d)
	o3 := orientation(c, d, a)
	o4 := orientation(c, d, b)
	if oppositeSigns(o1, o2) && oppositeSigns(o3, o4) {
		return true
	}
	return o1 == 0 && pointOnSegment(c, a, b) ||
		o2 == 0 && pointOnSegment(d, a, b) ||
		o3 == 0 && pointOnSegment(a, c, d) ||
		o4 == 0 && pointOnSegment(b, c, d)
}

func orientation(a, b, c Point2D) float64 {
	value := (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
	if math.Abs(value) <= 1e-9 {
		return 0
	}
	return value
}

func oppositeSigns(a, b float64) bool {
	return a < 0 && b > 0 || a > 0 && b < 0
}

func pointOnSegment(point, start, end Point2D) bool {
	return point.X >= math.Min(start.X, end.X)-1e-9 && point.X <= math.Max(start.X, end.X)+1e-9 &&
		point.Y >= math.Min(start.Y, end.Y)-1e-9 && point.Y <= math.Max(start.Y, end.Y)+1e-9
}

func pointInPolygon2D(point Point2D, polygon []Point2D) bool {
	inside := false
	for i, current := range polygon {
		previous := polygon[(i+len(polygon)-1)%len(polygon)]
		if orientation(previous, current, point) == 0 && pointOnSegment(point, previous, current) {
			return true
		}
		if (current.Y > point.Y) != (previous.Y > point.Y) {
			crossingX := (previous.X-current.X)*(point.Y-current.Y)/(previous.Y-current.Y) + current.X
			if point.X < crossingX {
				inside = !inside
			}
		}
	}
	return inside
}

func point2D(point image.Point) Point2D {
	return Point2D{X: float64(point.X), Y: float64(point.Y)}
}

func finitePoint(point Point2D) bool {
	return finiteNumber(point.X) && finiteNumber(point.Y)
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func midpoint(a, b image.Point) Point2D {
	return Point2D{X: float64(a.X+b.X) / 2, Y: float64(a.Y+b.Y) / 2}
}
