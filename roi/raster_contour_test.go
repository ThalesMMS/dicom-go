package roi

import (
	"image"
	"reflect"
	"testing"
)

func TestRasterMaskLargestContourRoundTripsConcaveRegion(t *testing.T) {
	mask := NewRasterMask(12, 10)
	mask.SetRun(2, 2, 9)
	mask.SetRun(3, 2, 9)
	mask.SetRun(4, 2, 6)
	mask.SetRun(5, 2, 6)

	contour := mask.LargestContour()
	if len(contour) != 6 {
		t.Fatalf("LargestContour() = %v, want six concave corners", contour)
	}
	roundTrip := (VectorROI{Shape: ROIPolygon, Points: contour}).Rasterize(mask.Columns, mask.Rows)
	for y := 0; y < mask.Rows; y++ {
		if !reflect.DeepEqual(roundTrip.Runs(y), mask.Runs(y)) {
			t.Fatalf("round-trip row %d = %v, want %v; contour %v", y, roundTrip.Runs(y), mask.Runs(y), contour)
		}
	}
}

func TestRasterMaskContoursSeparateOuterHoleAndIsland(t *testing.T) {
	mask := NewRasterMask(12, 12)
	for y := 1; y < 9; y++ {
		mask.SetRun(y, 1, 9)
	}
	for y := 3; y < 7; y++ {
		mask.ClearRun(y, 3, 7)
	}
	mask.Set(10, 10, true)

	contours := mask.Contours()
	if len(contours) != 3 {
		t.Fatalf("Contours() = %v, want outer, hole, and island", contours)
	}
	largest := mask.LargestContour()
	want := []image.Point{{1, 1}, {9, 1}, {9, 9}, {1, 9}}
	if !reflect.DeepEqual(largest, want) {
		t.Fatalf("LargestContour() = %v, want %v", largest, want)
	}
}

func TestSimplifyClosedContourReducesStairStepsAndPreservesCorners(t *testing.T) {
	stair := []image.Point{
		{1, 1}, {3, 1}, {3, 2}, {5, 2}, {5, 3}, {7, 3},
		{7, 8}, {1, 8},
	}
	simplified := SimplifyClosedContour(stair, 1.1)
	if len(simplified) >= len(stair) || len(simplified) < 4 {
		t.Fatalf("SimplifyClosedContour() = %v, want 4..%d points", simplified, len(stair)-1)
	}
	if closedContourSignedArea(simplified) <= 0 {
		t.Fatalf("simplified contour changed orientation: %v", simplified)
	}
}

func TestRasterMaskContourHandlesSinglePixel(t *testing.T) {
	mask := NewRasterMask(4, 4)
	mask.Set(2, 1, true)
	want := []image.Point{{2, 1}, {3, 1}, {3, 2}, {2, 2}}
	if got := mask.LargestContour(); !reflect.DeepEqual(got, want) {
		t.Fatalf("LargestContour() = %v, want %v", got, want)
	}
}
