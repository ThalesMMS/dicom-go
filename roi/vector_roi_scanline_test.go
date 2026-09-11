package roi

import (
	"image"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

var benchmarkRasterMask *RasterMask

func TestPolygonRasterizeMatchesPointInPolygonReference(t *testing.T) {
	cases := []struct {
		name   string
		points []image.Point
	}{
		{"triangle with horizontal edge", []image.Point{{0, 0}, {9, 0}, {0, 9}}},
		{"axis aligned rectangle", []image.Point{{2, 2}, {8, 2}, {8, 7}, {2, 7}}},
		{"concave arrow", []image.Point{{1, 1}, {10, 5}, {1, 10}, {4, 5}}},
		{"self intersecting bow tie", []image.Point{{1, 1}, {10, 10}, {1, 10}, {10, 1}}},
		{"partially outside grid", []image.Point{{-20, -5}, {7, 2}, {20, 15}, {-4, 13}}},
		{"repeated vertices", []image.Point{{2, 2}, {8, 2}, {8, 2}, {8, 8}, {2, 8}, {2, 2}}},
		{"single pixel width crossings", []image.Point{{3, 0}, {4, 5}, {3, 10}, {2, 5}}},
		{"extreme coordinates around grid", []image.Point{{-1 << 60, -1 << 60}, {1 << 60, -1 << 60}, {1 << 60, 1 << 60}, {-1 << 60, 1 << 60}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			region := VectorROI{Shape: ROIPolygon, Points: tc.points}
			assertRasterMasksEqual(t, region.Rasterize(12, 12), rasterizeContainsReference(region, 12, 12))
		})
	}
}

func TestPolygonRasterizeMatchesReferenceForDeterministicRandomPolygons(t *testing.T) {
	random := rand.New(rand.NewSource(315))
	for iteration := 0; iteration < 500; iteration++ {
		pointCount := 3 + random.Intn(30)
		points := make([]image.Point, pointCount)
		for i := range points {
			points[i] = image.Pt(random.Intn(97)-24, random.Intn(97)-24)
		}
		region := VectorROI{Shape: ROIPolygon, Points: points}
		got := region.Rasterize(48, 48)
		want := rasterizeContainsReference(region, 48, 48)
		for y := 0; y < 48; y++ {
			if !reflect.DeepEqual(got.Runs(y), want.Runs(y)) {
				t.Fatalf("iteration %d row %d differs\npoints=%v\ngot=%v\nwant=%v", iteration, y, points, got.Runs(y), want.Runs(y))
			}
		}
	}
}

func TestPolygonRasterizeComplexFreehandMatchesReference(t *testing.T) {
	region := VectorROI{Shape: ROIPolygon, Points: complexFreehandPoints(128, 1024, 7)}
	assertRasterMasksEqual(t, region.Rasterize(128, 128), rasterizeContainsReference(region, 128, 128))
}

func rasterizeContainsReference(region VectorROI, columns, rows int) *RasterMask {
	mask := NewRasterMask(columns, rows)
	for y := 0; y < rows; y++ {
		x := 0
		for x < columns {
			if !region.Contains(x, y) {
				x++
				continue
			}
			start := x
			for x < columns && region.Contains(x, y) {
				x++
			}
			mask.SetRun(y, start, x)
		}
	}
	return mask
}

func assertRasterMasksEqual(t *testing.T, got, want *RasterMask) {
	t.Helper()
	if got.Columns != want.Columns || got.Rows != want.Rows {
		t.Fatalf("mask dimensions = %dx%d, want %dx%d", got.Columns, got.Rows, want.Columns, want.Rows)
	}
	for y := 0; y < got.Rows; y++ {
		if !reflect.DeepEqual(got.Runs(y), want.Runs(y)) {
			t.Fatalf("row %d runs = %v, want %v", y, got.Runs(y), want.Runs(y))
		}
	}
}

func BenchmarkPolygonRasterizeSimple512(b *testing.B) {
	benchmarkPolygonRasterize(b, 512, []VectorROI{{
		Shape:  ROIPolygon,
		Points: []image.Point{{32, 32}, {480, 32}, {480, 480}, {32, 480}},
	}})
}

func BenchmarkPolygonRasterizeMultiple16x512(b *testing.B) {
	regions := make([]VectorROI, 16)
	for index := range regions {
		regions[index] = VectorROI{Shape: ROIPolygon, Points: complexFreehandPoints(512, 128, index)}
	}
	benchmarkPolygonRasterize(b, 512, regions)
}

func BenchmarkPolygonRasterizeComplex1024Vertices512(b *testing.B) {
	benchmarkPolygonRasterize(b, 512, []VectorROI{{
		Shape:  ROIPolygon,
		Points: complexFreehandPoints(512, 1024, 0),
	}})
}

func BenchmarkPolygonRasterizeComplex4096Vertices1024(b *testing.B) {
	benchmarkPolygonRasterize(b, 1024, []VectorROI{{
		Shape:  ROIPolygon,
		Points: complexFreehandPoints(1024, 4096, 0),
	}})
}

func benchmarkPolygonRasterize(b *testing.B, size int, regions []VectorROI) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for index := range regions {
			benchmarkRasterMask = regions[index].Rasterize(size, size)
		}
	}
}

func complexFreehandPoints(size, vertices, phase int) []image.Point {
	points := make([]image.Point, vertices)
	center := float64(size-1) / 2
	baseRadius := float64(size) * 0.38
	for index := range points {
		angle := 2 * math.Pi * float64(index) / float64(vertices)
		wave := 0.72 + 0.18*math.Sin(17*angle+float64(phase)) + 0.08*math.Sin(53*angle)
		points[index] = image.Pt(
			int(math.Round(center+baseRadius*wave*math.Cos(angle))),
			int(math.Round(center+baseRadius*wave*math.Sin(angle))),
		)
	}
	return points
}
