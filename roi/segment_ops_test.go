package roi

import (
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/render"
)

func Test_Brush_sets_and_erases_filled_disc(t *testing.T) {
	// Given
	mask := NewRasterMask(11, 11)

	// When
	Brush(mask, 5, 5, 2, true)

	// Then
	if !mask.Get(5, 5) {
		t.Fatal("brush center should be set")
	}
	if mask.Count() != 13 {
		t.Fatalf("brush count = %d, want 13", mask.Count())
	}

	// When
	Brush(mask, 5, 5, 2, false)

	// Then
	if mask.Count() != 0 {
		t.Fatalf("brush erase count = %d, want 0", mask.Count())
	}
}

func Test_ThresholdMask_groups_inclusive_range_into_runs(t *testing.T) {
	// Given
	valueAt := func(x, y int) (float64, bool) {
		return float64(x), true
	}

	// When
	mask := ThresholdMask(6, 1, 2, 4, valueAt)

	// Then
	if mask.Count() != 3 {
		t.Fatalf("threshold count = %d, want 3", mask.Count())
	}
	if !mask.Get(2, 0) || !mask.Get(4, 0) || mask.Get(1, 0) || mask.Get(5, 0) {
		t.Fatal("threshold membership incorrect")
	}
}

func Test_FloodFill_returns_only_seed_connected_region(t *testing.T) {
	// Given
	inside := func(x, y int) bool {
		if x >= 1 && x <= 3 && y >= 1 && y <= 3 {
			return true
		}
		return x == 5 && y == 5
	}

	// When
	mask := FloodFill(8, 8, 2, 2, inside)

	// Then
	if mask.Count() != 9 {
		t.Fatalf("flood fill count = %d, want 9", mask.Count())
	}
	if mask.Get(5, 5) {
		t.Fatal("disconnected island should not be filled")
	}
}

func Test_ConnectedComponents_returns_row_major_components(t *testing.T) {
	// Given
	mask := NewRasterMask(10, 10)
	mask.SetRun(0, 0, 2)
	mask.SetRun(1, 0, 2)
	mask.SetRun(5, 5, 7)
	mask.SetRun(6, 5, 7)

	// When
	comps := ConnectedComponents(mask, false)

	// Then
	if len(comps) != 2 {
		t.Fatalf("component count = %d, want 2", len(comps))
	}
	for i, c := range comps {
		if c.Count() != 4 {
			t.Fatalf("component %d size = %d, want 4", i, c.Count())
		}
	}
}

func Test_ConnectedComponents_respects_four_and_eight_connected_diagonals(t *testing.T) {
	// Given
	mask := NewRasterMask(4, 4)
	mask.Set(1, 1, true)
	mask.Set(2, 2, true)

	// When
	fourConnected := ConnectedComponents(mask, false)
	eightConnected := ConnectedComponents(mask, true)

	// Then
	if len(fourConnected) != 2 {
		t.Fatalf("4-connected component count = %d, want 2", len(fourConnected))
	}
	if len(eightConnected) != 1 {
		t.Fatalf("8-connected component count = %d, want 1", len(eightConnected))
	}
}

func Test_ThresholdSegmentation_components_and_stats_use_irregular_geometry(t *testing.T) {
	// Given
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{
		axialSlice(0), axialSlice(1), axialSlice(3),
	}, render.DefaultGeometryTolerances())
	valueAt := func(x, y, slice int) (float64, bool) {
		switch {
		case x == 1 && y == 1 && slice == 0:
			return 10, true
		case x == 1 && y == 1 && slice == 1:
			return 20, true
		case x == 3 && y == 3 && slice == 2:
			return 30, true
		default:
			return 0, true
		}
	}

	// When
	seg := ThresholdSegmentation(geometry, 4, 4, 10, 30, valueAt)
	components := ConnectedComponents3D(seg)
	stats := Stats3D(seg, valueAt, 2)

	// Then
	if seg.VoxelCount() != 3 {
		t.Fatalf("VoxelCount() = %d, want 3", seg.VoxelCount())
	}
	if len(components) != 2 {
		t.Fatalf("ConnectedComponents3D() count = %d, want 2", len(components))
	}
	if components[0].VoxelCount() != 2 || components[1].VoxelCount() != 1 {
		t.Fatalf("component sizes = [%d %d], want [2 1]", components[0].VoxelCount(), components[1].VoxelCount())
	}
	if stats.VoxelCount != 3 || stats.Min != 10 || stats.Max != 30 || stats.Mean != 20 {
		t.Fatalf("Stats3D() = %+v, want count 3 min 10 max 30 mean 20", stats)
	}
	if stats.AreaMM2 != 3 {
		t.Fatalf("Stats3D() area = %v, want 3", stats.AreaMM2)
	}
	if math.Abs(stats.VolumeMM3-4.5) > 1e-9 {
		t.Fatalf("Stats3D() volume = %v, want 4.5", stats.VolumeMM3)
	}
	if len(stats.Histogram) != 2 || stats.Histogram[0] != 1 || stats.Histogram[1] != 2 {
		t.Fatalf("Stats3D() histogram = %#v, want [1 2]", stats.Histogram)
	}
	if stats.HistogramMin != 10 || stats.HistogramBinWidth != 10 {
		t.Fatalf("histogram min/bin = %v/%v, want 10/10", stats.HistogramMin, stats.HistogramBinWidth)
	}
}

func Test_InterpolateMask_blends_translated_masks(t *testing.T) {
	// Given
	first := NewRasterMask(9, 5)
	first.SetRun(1, 1, 4)
	first.SetRun(2, 1, 4)
	first.SetRun(3, 1, 4)
	last := NewRasterMask(9, 5)
	last.SetRun(1, 5, 8)
	last.SetRun(2, 5, 8)
	last.SetRun(3, 5, 8)

	// When
	mid := InterpolateMask(first, last, 0.5)

	// Then
	if mid == nil {
		t.Fatal("InterpolateMask() returned nil")
	}
	if !mid.Get(4, 2) {
		t.Fatal("interpolated mask should include the midpoint between translated masks")
	}
	if mid.Get(1, 2) || mid.Get(7, 2) {
		t.Fatal("interpolated mask should not simply copy either endpoint")
	}
	if mid.Count() == 0 {
		t.Fatal("interpolated mask should be non-empty")
	}
}

func Test_InterpolateBetweenSlices_fills_missing_slices(t *testing.T) {
	// Given
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{
		axialSlice(0), axialSlice(1), axialSlice(2),
	}, render.DefaultGeometryTolerances())
	seg := NewSegmentation3D(geometry, 9, 5)
	first := NewRasterMask(9, 5)
	first.SetRun(2, 1, 4)
	last := NewRasterMask(9, 5)
	last.SetRun(2, 5, 8)
	seg.SetMask(0, first)
	seg.SetMask(2, last)

	// When
	written := InterpolateBetweenSlices(seg, 0, 2)

	// Then
	if written != 1 {
		t.Fatalf("InterpolateBetweenSlices() = %d, want one synthesized slice", written)
	}
	if !seg.Voxel(4, 2, 1) {
		t.Fatal("interpolated middle slice should bridge the edited endpoint slices")
	}
	if seg.Voxel(1, 2, 1) || seg.Voxel(7, 2, 1) {
		t.Fatal("interpolated middle slice should not leave an endpoint-shaped gap fill")
	}
}

func Test_Dilate_and_Erode_use_four_connected_structuring_element(t *testing.T) {
	// Given
	mask := NewRasterMask(11, 11)
	mask.Set(5, 5, true)

	// When
	dilated := Dilate(mask)
	eroded := Erode(dilated)

	// Then
	if dilated.Count() != 5 {
		t.Fatalf("dilate count = %d, want 5", dilated.Count())
	}
	if eroded.Count() != 1 || !eroded.Get(5, 5) {
		t.Fatalf("erode of plus = %d set, want one center pixel", eroded.Count())
	}
}
