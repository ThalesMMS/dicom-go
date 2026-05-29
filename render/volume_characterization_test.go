package render

import (
	"errors"
	"math"
	"testing"
)

func Test_BuildVolume_preserves_geometry_and_samples_oblique_planes(t *testing.T) {
	// Given
	stack := gradientColumnStack(16, 16, 8)

	// When
	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	plane := obliqueInteriorPlane(vol)
	img := ResliceOblique(vol, plane, 7, 7, WindowLevel{Center: 128, Width: 256})

	// Then
	if vol.Depth != 8 || vol.Cols != 16 || vol.Rows != 16 {
		t.Fatalf("volume dimensions = %dx%dx%d, want 16x16x8", vol.Cols, vol.Rows, vol.Depth)
	}
	for y := 0; y < 7; y++ {
		for x := 0; x < 7; x++ {
			s := float64(x) / 6
			tfrac := float64(y) / 6
			p := plane.At(s, tfrac)
			expected := vol.PatientToVoxel(p).X
			want := displayGrayMapped(expected, prepareWindow(WindowLevel{Center: 128, Width: 256}), "MONOCHROME2")
			got := grayAt(img, x, y)
			if diff := int(got) - int(want); diff < -1 || diff > 1 {
				t.Fatalf("oblique sample (%d,%d) = %d, want ~%d", x, y, got, want)
			}
		}
	}
}

func Test_VolumeValueAtPreparesCanonicalSnapshotLazily(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(4, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vol.Close() })
	if stats := vol.VolumeStoreStats(); stats.LiveBytes != 0 || stats.LiveGenerations != 0 {
		t.Fatalf("snapshot is resident before sampling: %+v", stats)
	}

	got, ph, ok := vol.ValueAt(2, 1, 1)
	if !ok {
		t.Fatal("ValueAt() ok = false")
	}
	if got != 2 {
		t.Fatalf("ValueAt() = %v, want decoded column value 2", got)
	}
	if ph != "MONOCHROME2" {
		t.Fatalf("ValueAt() photometric = %q, want MONOCHROME2", ph)
	}
	stats := vol.VolumeStoreStats()
	if stats.LiveBytes != uint64(vol.Cols*vol.Rows*vol.Depth*4) || stats.LiveGenerations != 1 {
		t.Fatalf("ValueAt() snapshot stats = %+v", stats)
	}
}

func Test_Stack_VolumeGeometry_classifies_missing_slices_and_maps_irregular_positions(t *testing.T) {
	// Given
	stack := gradientColumnStack(4, 4, 3)
	stack.Frames[0].ImagePosition = []float64{0, 0, 0}
	stack.Frames[1].ImagePosition = []float64{0, 0, 1}
	stack.Frames[2].ImagePosition = []float64{0, 0, 3}

	// When
	geometry, ok := stack.VolumeGeometry()
	vol, err := BuildVolume(stack)

	// Then
	if !ok {
		t.Fatal("VolumeGeometry() ok = false, want true")
	}
	if err != nil {
		t.Fatal(err)
	}
	if geometry.Classification != VolumeMissingSlices {
		t.Fatalf("Classification = %s, want %s", geometry.Classification, VolumeMissingSlices)
	}
	if geometry.Regular || !geometry.MissingSlices {
		t.Fatalf("regular=%v missing=%v, want regular=false missing=true", geometry.Regular, geometry.MissingSlices)
	}
	if len(geometry.Positions) != 3 || geometry.Positions[0] != 0 || geometry.Positions[1] != 1 || geometry.Positions[2] != 3 {
		t.Fatalf("Positions = %#v, want [0 1 3]", geometry.Positions)
	}
	if math.Abs(geometry.MeanSpacing-1.5) > 1e-9 {
		t.Fatalf("MeanSpacing = %v, want 1.5", geometry.MeanSpacing)
	}
	if got := vol.VoxelToPatient(Vec3{X: 1, Y: 1, Z: 1.5}); math.Abs(got.Z-2) > 1e-9 {
		t.Fatalf("VoxelToPatient irregular Z = %v, want 2", got.Z)
	}
	if got := vol.PatientToVoxel(Vec3{X: 1, Y: 1, Z: 2}); math.Abs(got.Z-1.5) > 1e-9 {
		t.Fatalf("PatientToVoxel irregular Z = %v, want 1.5", got.Z)
	}
}

func TestStackVolumeGeometryUsesPerFramePixelSpacing(t *testing.T) {
	stack := gradientColumnStack(4, 4, 2)
	stack.PixelSpacing = nil
	stack.Frames[0].PixelSpacing = []float64{0.5, 0.75}
	stack.Frames[1].PixelSpacing = []float64{0.6, 0.8}

	geometry, ok := stack.VolumeGeometry()
	if !ok {
		t.Fatal("VolumeGeometry() ok = false, want true")
	}
	if len(geometry.Slices) != 2 {
		t.Fatalf("len(Slices) = %d, want 2", len(geometry.Slices))
	}
	if geometry.Slices[0].RowSpacing != 0.5 || geometry.Slices[0].ColSpacing != 0.75 {
		t.Fatalf("frame 0 spacing = %v/%v, want 0.5/0.75", geometry.Slices[0].RowSpacing, geometry.Slices[0].ColSpacing)
	}
	if geometry.Slices[1].RowSpacing != 0.6 || geometry.Slices[1].ColSpacing != 0.8 {
		t.Fatalf("frame 1 spacing = %v/%v, want 0.6/0.8", geometry.Slices[1].RowSpacing, geometry.Slices[1].ColSpacing)
	}
	assessment := stack.GeometryAssessment()
	if assessment.FallbackReason != GeometryIssueInconsistentPixelSpacing {
		t.Fatalf("fallback reason = %q, want %q", assessment.FallbackReason, GeometryIssueInconsistentPixelSpacing)
	}
	if _, err := BuildVolume(stack); !errors.Is(err, ErrNonCorrectableGeometry) {
		t.Fatalf("BuildVolume() error = %v, want fail-closed geometry error", err)
	}
}

func TestSeriesSpacingSkipsInvalidValues(t *testing.T) {
	stack := gradientColumnStack(4, 4, 2)
	stack.PixelSpacing = []float64{math.NaN(), 0}
	stack.Frames[0].PixelSpacing = []float64{math.Inf(1), -1}
	stack.Frames[1].PixelSpacing = []float64{0.6, 0.8}

	if got := seriesRowSpacing(stack); got != 0.6 {
		t.Fatalf("seriesRowSpacing() = %v, want 0.6", got)
	}
	if got := seriesColumnSpacing(stack); got != 0.8 {
		t.Fatalf("seriesColumnSpacing() = %v, want 0.8", got)
	}
}

func TestBuildVolumeDefaultsNonFinitePixelSpacing(t *testing.T) {
	stack := gradientColumnStack(4, 4, 2)
	stack.PixelSpacing = []float64{math.Inf(1), math.Inf(1)}
	for _, frame := range stack.Frames {
		frame.PixelSpacing = []float64{math.Inf(1), math.Inf(-1)}
	}

	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	if volume.RowSpacing != 1 || volume.ColSpacing != 1 {
		t.Fatalf("volume spacing = %v/%v, want finite defaults 1/1", volume.RowSpacing, volume.ColSpacing)
	}
}
