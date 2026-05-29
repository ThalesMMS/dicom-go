package roi

import (
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/render"
)

func Test_RasterMask_merges_touching_runs_when_setting_run(t *testing.T) {
	// Given
	mask := NewRasterMask(20, 5)

	// When
	mask.SetRun(1, 0, 5)
	mask.SetRun(1, 5, 10)

	// Then
	runs := mask.Runs(1)
	if len(runs) != 1 || runs[0] != (MaskRun{Start: 0, End: 10}) {
		t.Fatalf("Runs() = %#v, want one [0,10)", runs)
	}
	if mask.Count() != 10 {
		t.Fatalf("Count() = %d, want 10", mask.Count())
	}
}

func TestRasterMaskXORPreservesOnlyPixelsSetInOneMask(t *testing.T) {
	mask := NewRasterMask(8, 2)
	mask.SetRun(0, 1, 7)
	other := NewRasterMask(8, 2)
	other.SetRun(0, 3, 5)
	other.SetRun(1, 2, 4)

	mask.XOR(other)

	wantRow0 := []MaskRun{{Start: 1, End: 3}, {Start: 5, End: 7}}
	if got := mask.Runs(0); !reflect.DeepEqual(got, wantRow0) {
		t.Fatalf("row 0 runs = %#v, want %#v", got, wantRow0)
	}
	wantRow1 := []MaskRun{{Start: 2, End: 4}}
	if got := mask.Runs(1); !reflect.DeepEqual(got, wantRow1) {
		t.Fatalf("row 1 runs = %#v, want %#v", got, wantRow1)
	}
}

func TestRasterMaskIntersectAndSubtractPreserveHolesAndBorders(t *testing.T) {
	base := NewRasterMask(8, 4)
	base.SetRun(0, 0, 8)
	base.SetRun(1, 0, 8)
	base.SetRun(2, 0, 8)
	selection := NewRasterMask(8, 4)
	selection.SetRun(0, 0, 2)
	selection.SetRun(1, 2, 6)
	selection.SetRun(2, 6, 8)

	intersection := base.Clone()
	intersection.Intersect(selection)
	if intersection.Count() != 8 || !intersection.Get(0, 0) || !intersection.Get(5, 1) || !intersection.Get(7, 2) {
		t.Fatalf("intersection count/membership = %d, want border runs and center", intersection.Count())
	}

	subtracted := base.Clone()
	subtracted.Subtract(selection)
	if subtracted.Count() != 16 || subtracted.Get(0, 0) || subtracted.Get(3, 1) || subtracted.Get(7, 2) {
		t.Fatalf("subtraction count/membership = %d, want exact holes", subtracted.Count())
	}
	if !subtracted.Get(7, 0) || !subtracted.Get(0, 1) || !subtracted.Get(0, 2) {
		t.Fatal("subtraction removed pixels outside the selection")
	}
}

func TestRasterMaskIntersectMismatchedGridFailsClosed(t *testing.T) {
	mask := NewRasterMask(4, 4)
	mask.SetRun(0, 0, 4)
	mask.Intersect(NewRasterMask(3, 4))
	if !mask.Empty() {
		t.Fatal("grid-mismatched intersection retained pixels")
	}
}

func Test_VectorROI_rasterizes_rectangle_ellipse_and_polygon(t *testing.T) {
	tests := []struct {
		name       string
		region     VectorROI
		inside     image.Point
		outside    image.Point
		wantCount  int
		checkCount bool
	}{
		{
			name:       "rectangle",
			region:     VectorROI{Shape: ROIRectangle, Points: []image.Point{{X: 1, Y: 1}, {X: 3, Y: 3}}},
			inside:     image.Pt(2, 2),
			outside:    image.Pt(0, 0),
			wantCount:  9,
			checkCount: true,
		},
		{
			name:    "ellipse",
			region:  VectorROI{Shape: ROIEllipse, Points: []image.Point{{X: 0, Y: 0}, {X: 4, Y: 4}}},
			inside:  image.Pt(2, 2),
			outside: image.Pt(0, 0),
		},
		{
			name:    "polygon",
			region:  VectorROI{Shape: ROIPolygon, Points: []image.Point{{X: 0, Y: 0}, {X: 4, Y: 0}, {X: 0, Y: 4}}},
			inside:  image.Pt(1, 1),
			outside: image.Pt(3, 3),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			mask := tt.region.Rasterize(5, 5)

			// Then
			if !mask.Get(tt.inside.X, tt.inside.Y) {
				t.Fatalf("Rasterize() did not include %v", tt.inside)
			}
			if mask.Get(tt.outside.X, tt.outside.Y) {
				t.Fatalf("Rasterize() included outside point %v", tt.outside)
			}
			if tt.checkCount && mask.Count() != tt.wantCount {
				t.Fatalf("Rasterize() count = %d, want %d", mask.Count(), tt.wantCount)
			}
		})
	}
}

func Test_Measurements_use_pixel_spacing_in_millimeters(t *testing.T) {
	// Given
	spacing := MeasureSpacing{ColumnMM: 2, RowMM: 1}

	// When
	length, ok := LengthMMWith(image.Pt(0, 0), image.Pt(4, 3), spacing)

	// Then
	if !ok {
		t.Fatal("LengthMMWith() ok = false, want true")
	}
	if math.Abs(length-math.Sqrt(73)) > 1e-9 {
		t.Fatalf("LengthMMWith() = %v, want sqrt(73)", length)
	}

	// When
	area, ok := RectangleAreaMM2With(image.Pt(0, 0), image.Pt(4, 3), spacing)

	// Then
	if !ok {
		t.Fatal("RectangleAreaMM2With() ok = false, want true")
	}
	if area != 24 {
		t.Fatalf("RectangleAreaMM2With() = %v, want 24", area)
	}
}

func Test_Segmentation3D_carries_render_geometry_and_counts_voxels(t *testing.T) {
	// Given
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{
		axialSlice(0), axialSlice(2), axialSlice(4),
	}, render.DefaultGeometryTolerances())
	seg := NewSegmentation3D(geometry, 8, 8)
	region := VectorROI{Shape: ROIRectangle, Points: []image.Point{{X: 2, Y: 2}, {X: 4, Y: 4}}}

	// When
	seg.SetMask(1, region.Rasterize(8, 8))

	// Then
	if seg.Geometry.MeanSpacing != 2 {
		t.Fatalf("MeanSpacing = %v, want 2", seg.Geometry.MeanSpacing)
	}
	if seg.VoxelCount() != 9 {
		t.Fatalf("VoxelCount() = %d, want 9", seg.VoxelCount())
	}
	if !seg.Voxel(3, 3, 1) {
		t.Fatal("Voxel() center = false, want true")
	}
}

func Test_Stats2D_and_Stats3D_compute_masked_voxel_statistics(t *testing.T) {
	// Given
	mask := NewRasterMask(4, 4)
	mask.Set(1, 1, true)
	mask.Set(2, 1, true)
	value2D := func(x, y int) (float64, bool) {
		return float64(x + y*10), true
	}

	// When
	stats := Stats2D(mask, value2D)

	// Then
	if stats.Count != 2 || stats.Min != 11 || stats.Max != 12 || stats.Mean != 11.5 {
		t.Fatalf("Stats2D() = %+v, want count 2 min 11 max 12 mean 11.5", stats)
	}

	// Given
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{
		axialSlice(0), axialSlice(2),
	}, render.DefaultGeometryTolerances())
	seg := NewSegmentation3D(geometry, 4, 4)
	seg.SetMask(0, mask)
	value3D := func(x, y, slice int) (float64, bool) {
		return float64(x + y*10 + slice*100), true
	}

	// When
	volStats := Stats3D(seg, value3D, 4)

	// Then
	if volStats.VoxelCount != 2 || volStats.Min != 11 || volStats.Max != 12 || volStats.Mean != 11.5 {
		t.Fatalf("Stats3D() = %+v, want count 2 min 11 max 12 mean 11.5", volStats)
	}
}

func TestXorRunsNormalizesEmptyResultToNil(t *testing.T) {
	runs := []MaskRun{{Start: 2, End: 5}}
	if got := xorRuns(runs, runs); got != nil {
		t.Fatalf("xorRuns(equal, equal) = %#v, want nil", got)
	}
}

func axialSlice(position float64) render.SliceGeometry {
	return render.SliceGeometry{
		Origin:     render.Vec3{Z: position},
		RowDir:     render.Vec3{X: 1},
		ColDir:     render.Vec3{Y: 1},
		Normal:     render.Vec3{Z: 1},
		RowSpacing: 1,
		ColSpacing: 1,
		Rows:       4,
		Columns:    4,
	}
}
