package roi

import (
	"image"
	"math"
	"testing"
)

func TestMeanAxisLinesJoinOppositeEdgeMidpoints(t *testing.T) {
	primary, secondary, ok := MeanAxisLines([4]image.Point{
		image.Pt(0, 0), image.Pt(4, 0), image.Pt(6, 8), image.Pt(-2, 8),
	})
	if !ok {
		t.Fatal("MeanAxisLines() rejected a valid quadrilateral")
	}
	if primary != (Line2D{Start: Point2D{X: 2, Y: 0}, End: Point2D{X: 2, Y: 8}}) {
		t.Fatalf("primary = %#v", primary)
	}
	if secondary != (Line2D{Start: Point2D{X: -1, Y: 4}, End: Point2D{X: 5, Y: 4}}) {
		t.Fatalf("secondary = %#v", secondary)
	}
}

func TestTrianglePerpendicularBisectorsAreConcurrentAxes(t *testing.T) {
	points := [3]image.Point{image.Pt(0, 0), image.Pt(6, 0), image.Pt(0, 8)}
	lines, ok := TrianglePerpendicularBisectors(points)
	if !ok {
		t.Fatal("TrianglePerpendicularBisectors() rejected a valid triangle")
	}

	wantCenter := Point2D{X: 3, Y: 4}
	for i, line := range lines {
		next := (i + 1) % len(points)
		wantMidpoint := Point2D{
			X: float64(points[i].X+points[next].X) / 2,
			Y: float64(points[i].Y+points[next].Y) / 2,
		}
		gotMidpoint := Point2D{X: (line.Start.X + line.End.X) / 2, Y: (line.Start.Y + line.End.Y) / 2}
		assertPoint2DClose(t, gotMidpoint, wantMidpoint)

		sideX := float64(points[next].X - points[i].X)
		sideY := float64(points[next].Y - points[i].Y)
		axisX := line.End.X - line.Start.X
		axisY := line.End.Y - line.Start.Y
		if dot := sideX*axisX + sideY*axisY; math.Abs(dot) > 1e-9 {
			t.Errorf("axis %d dot side = %v, want perpendicular", i, dot)
		}
		if cross := axisX*(wantCenter.Y-line.Start.Y) - axisY*(wantCenter.X-line.Start.X); math.Abs(cross) > 1e-9 {
			t.Errorf("axis %d does not pass through circumcenter: cross = %v", i, cross)
		}
		if !pointOnSegment(wantCenter, line.Start, line.End) {
			t.Errorf("axis %d is not extended through circumcenter: %#v", i, line)
		}
	}

	intersection, ok := testLineIntersection(lines[0], lines[1])
	if !ok {
		t.Fatal("first two perpendicular bisectors are parallel")
	}
	assertPoint2DClose(t, intersection, wantCenter)
}

func TestTrianglePerpendicularBisectorsRejectDegenerateTriangle(t *testing.T) {
	for _, points := range [][3]image.Point{
		{image.Pt(0, 0), image.Pt(0, 0), image.Pt(2, 3)},
		{image.Pt(0, 0), image.Pt(2, 2), image.Pt(4, 4)},
	} {
		if _, ok := TrianglePerpendicularBisectors(points); ok {
			t.Fatalf("TrianglePerpendicularBisectors(%v) accepted a degenerate triangle", points)
		}
	}
}

func TestPerpendicularLinePointsProjectsDerivedFeet(t *testing.T) {
	points, ok := PerpendicularLinePoints(image.Pt(2, 5), image.Pt(8, -3), image.Pt(0, 0), image.Pt(10, 0))
	if !ok {
		t.Fatal("PerpendicularLinePoints() rejected a valid main line")
	}
	want := [6]image.Point{
		image.Pt(2, 0), image.Pt(2, 5), image.Pt(8, 0), image.Pt(8, -3), image.Pt(0, 0), image.Pt(10, 0),
	}
	if points != want {
		t.Fatalf("points = %v, want %v", points, want)
	}
	if _, ok := PerpendicularLinePoints(image.Point{}, image.Point{}, image.Pt(1, 1), image.Pt(1, 1)); ok {
		t.Fatal("degenerate main line unexpectedly accepted")
	}
}

func TestDynamicAngleDegAppliesAnisotropicSpacing(t *testing.T) {
	acute, opposite, ok := DynamicAngleDeg(
		image.Pt(0, 0), image.Pt(2, 1),
		image.Pt(0, 0), image.Pt(2, -1),
		MeasureSpacing{X: 1, Y: 2},
	)
	if !ok || math.Abs(acute-90) > 1e-9 || math.Abs(opposite-270) > 1e-9 {
		t.Fatalf("angles = %v, %v, ok %v; want 90, 270", acute, opposite, ok)
	}
}

func TestSphereSliceRadiusMM(t *testing.T) {
	tests := []struct {
		offset float64
		want   float64
		ok     bool
	}{
		{offset: 0, want: 5, ok: true},
		{offset: 3, want: 4, ok: true},
		{offset: -3, want: 4, ok: true},
		{offset: 5, want: 0, ok: true},
		{offset: 6, ok: false},
	}
	for _, test := range tests {
		got, ok := SphereSliceRadiusMM(5, test.offset)
		if ok != test.ok || math.Abs(got-test.want) > 1e-9 {
			t.Errorf("offset %v: got %v, %v; want %v, %v", test.offset, got, ok, test.want, test.ok)
		}
	}
}

func TestRepulsePolylineUsesRadialFalloffWithoutAliasing(t *testing.T) {
	input := []Point2D{{X: 0, Y: 0}, {X: 3, Y: 4}, {X: 10, Y: 0}, {X: 12, Y: 0}}
	got, ok := RepulsePolyline(input, Point2D{}, 10, 4)
	if !ok {
		t.Fatal("RepulsePolyline() rejected valid input")
	}
	want := []Point2D{{X: 4, Y: 0}, {X: 4.2, Y: 5.6}, {X: 10, Y: 0}, {X: 12, Y: 0}}
	for i := range want {
		assertPoint2DClose(t, got[i], want[i])
	}
	if input[0] != (Point2D{}) {
		t.Fatalf("RepulsePolyline() mutated input: %#v", input)
	}
	got[0].X = 99
	if input[0].X == got[0].X {
		t.Fatal("RepulsePolyline() result aliases input")
	}
}

func TestRepulsePolylineRejectsInvalidField(t *testing.T) {
	points := []Point2D{{X: 1, Y: 1}}
	for _, test := range []struct {
		name     string
		points   []Point2D
		center   Point2D
		radius   float64
		strength float64
	}{
		{name: "empty", radius: 2, strength: 1},
		{name: "zero radius", points: points, radius: 0, strength: 1},
		{name: "negative strength", points: points, radius: 2, strength: -1},
		{name: "non finite center", points: points, center: Point2D{X: math.NaN()}, radius: 2, strength: 1},
		{name: "non finite point", points: []Point2D{{X: math.Inf(1)}}, radius: 2, strength: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := RepulsePolyline(test.points, test.center, test.radius, test.strength); ok {
				t.Fatal("RepulsePolyline() accepted invalid input")
			}
		})
	}
}

func TestPolylineIntersectsRectangle(t *testing.T) {
	rectangle := Rectangle2D{Min: Point2D{X: 2, Y: 2}, Max: Point2D{X: 8, Y: 8}}
	tests := []struct {
		name   string
		points []Point2D
		want   bool
	}{
		{name: "point inside", points: []Point2D{{X: 3, Y: 3}}, want: true},
		{name: "crossing endpoints outside", points: []Point2D{{X: 0, Y: 5}, {X: 10, Y: 5}}, want: true},
		{name: "touching corner", points: []Point2D{{X: 0, Y: 0}, {X: 2, Y: 2}}, want: true},
		{name: "outside", points: []Point2D{{X: 0, Y: 0}, {X: 1, Y: 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PolylineIntersectsRectangle(test.points, rectangle); got != test.want {
				t.Fatalf("PolylineIntersectsRectangle() = %v, want %v", got, test.want)
			}
		})
	}
	if !PolylineIntersectsRectangle([]Point2D{{X: 3, Y: 3}}, Rectangle2D{Min: rectangle.Max, Max: rectangle.Min}) {
		t.Fatal("PolylineIntersectsRectangle() did not canonicalize reversed rectangle")
	}
}

func TestPolygonIntersectsRectangleIncludesContainment(t *testing.T) {
	rectangle := Rectangle2D{Min: Point2D{X: 2, Y: 2}, Max: Point2D{X: 4, Y: 4}}
	tests := []struct {
		name    string
		polygon []Point2D
		want    bool
	}{
		{
			name:    "polygon contains rectangle",
			polygon: []Point2D{{X: 0, Y: 0}, {X: 8, Y: 0}, {X: 8, Y: 8}, {X: 0, Y: 8}},
			want:    true,
		},
		{
			name:    "rectangle contains polygon",
			polygon: []Point2D{{X: 2.5, Y: 2.5}, {X: 3.5, Y: 2.5}, {X: 3, Y: 3.5}},
			want:    true,
		},
		{
			name:    "edge crossing",
			polygon: []Point2D{{X: 0, Y: 3}, {X: 3, Y: 0}, {X: 6, Y: 3}, {X: 3, Y: 6}},
			want:    true,
		},
		{
			name:    "disjoint",
			polygon: []Point2D{{X: 10, Y: 10}, {X: 12, Y: 10}, {X: 11, Y: 12}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PolygonIntersectsRectangle(test.polygon, rectangle); got != test.want {
				t.Fatalf("PolygonIntersectsRectangle() = %v, want %v", got, test.want)
			}
		})
	}
}

func assertPoint2DClose(t *testing.T, got, want Point2D) {
	t.Helper()
	if math.Abs(got.X-want.X) > 1e-9 || math.Abs(got.Y-want.Y) > 1e-9 {
		t.Fatalf("point = %#v, want %#v", got, want)
	}
}

func testLineIntersection(first, second Line2D) (Point2D, bool) {
	firstX := first.End.X - first.Start.X
	firstY := first.End.Y - first.Start.Y
	secondX := second.End.X - second.Start.X
	secondY := second.End.Y - second.Start.Y
	denominator := firstX*secondY - firstY*secondX
	if math.Abs(denominator) <= 1e-9 {
		return Point2D{}, false
	}
	deltaX := second.Start.X - first.Start.X
	deltaY := second.Start.Y - first.Start.Y
	t := (deltaX*secondY - deltaY*secondX) / denominator
	return Point2D{X: first.Start.X + t*firstX, Y: first.Start.Y + t*firstY}, true
}
