package render

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestBuildVolumeAppliesStableGantryTiltInPatientTransforms(t *testing.T) {
	tests := []struct {
		name       string
		origins    []Vec3
		rowSpacing float64
		colSpacing float64
		voxel      Vec3
		want       Vec3
	}{
		{
			name:       "positive row-axis tilt with anisotropic pixels",
			origins:    []Vec3{{0, 0, 0}, {0, 1, 2}, {0, 2, 4}},
			rowSpacing: 0.5,
			colSpacing: 0.8,
			voxel:      Vec3{X: 3, Y: 4, Z: 1.5},
			want:       Vec3{X: 2.4, Y: 3.5, Z: 3},
		},
		{
			name:       "negative column-axis tilt with irregular spacing",
			origins:    []Vec3{{0, 0, 0}, {-1, 0, 1}, {-3, 0, 3}},
			rowSpacing: 1.25,
			colSpacing: 0.5,
			voxel:      Vec3{X: 4, Y: 2, Z: 1.5},
			want:       Vec3{X: 0, Y: 2.5, Z: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := tiltedGradientRowStack(6, 8, tt.origins, tt.rowSpacing, tt.colSpacing)
			vol, err := BuildVolume(stack)
			if err != nil {
				t.Fatal(err)
			}

			correction := vol.GantryTiltCorrection()
			if !correction.Applied {
				t.Fatal("GantryTiltCorrection().Applied = false, want true")
			}
			got := vol.VoxelToPatient(tt.voxel)
			assertVec3Near(t, got, tt.want, 1e-9)
			assertVec3Near(t, vol.PatientToVoxel(got), tt.voxel, 1e-9)
			sourceDistance := tt.origins[len(tt.origins)-1].Sub(tt.origins[0]).Length()
			mappedStart := vol.VoxelToPatient(Vec3{})
			mappedEnd := vol.VoxelToPatient(Vec3{Z: float64(len(tt.origins) - 1)})
			mappedDistance := WorldDistance(
				WorldPoint{X: mappedStart.X, Y: mappedStart.Y, Z: mappedStart.Z},
				WorldPoint{X: mappedEnd.X, Y: mappedEnd.Y, Z: mappedEnd.Z},
			)
			if math.Abs(mappedDistance-sourceDistance) > 1e-9 {
				t.Fatalf("mapped source distance = %v, want %v mm", mappedDistance, sourceDistance)
			}
			plane, width, height, ok := vol.correctedOrthogonalPlane(MPRPlaneCoronal, 2)
			if !ok {
				t.Fatal("correctedOrthogonalPlane() ok = false")
			}
			spacing := plane.PixelSpacingMM(width, height)
			if math.Abs(spacing.X-tt.colSpacing) > 1e-9 || math.Abs(spacing.Y-tt.colSpacing) > 1e-9 {
				t.Fatalf("corrected coronal pixel spacing = %#v, want square %v mm pixels", spacing, tt.colSpacing)
			}
			if vol.VolumeStoreStats().LiveBytes != 0 {
				t.Fatal("patient transform eagerly decoded pixel data")
			}
			if len(vol.sliceOrigins) != len(tt.origins) {
				t.Fatalf("len(sliceOrigins) = %d, want bounded O(depth) geometry %d", len(vol.sliceOrigins), len(tt.origins))
			}
		})
	}
}

func TestVolumeGeometryExposesDirectionalGantryTiltMetrics(t *testing.T) {
	stack := tiltedGradientRowStack(4, 4, []Vec3{{0, 0, 0}, {0, -1, 2}, {0, -2, 4}}, 1, 1)

	geometry, ok := stack.VolumeGeometry()
	if !ok {
		t.Fatal("VolumeGeometry() ok = false")
	}
	if geometry.Classification != VolumeGantryTilted || !geometry.GantryTilted {
		t.Fatalf("classification=%s tilted=%v, want gantry-tilted", geometry.Classification, geometry.GantryTilted)
	}
	assertVec3Near(t, geometry.GantryTiltOffset, Vec3{Y: -2}, 1e-9)
	assertVec3Near(t, geometry.GantryTiltShear, Vec3{Y: -0.5}, 1e-9)
	wantAngle := math.Atan(0.5) * 180 / math.Pi
	if math.Abs(geometry.GantryTiltAngleDegrees-wantAngle) > 1e-9 {
		t.Fatalf("GantryTiltAngleDegrees = %v, want %v", geometry.GantryTiltAngleDegrees, wantAngle)
	}
}

func TestGantryTiltMetricsRejectsZeroThroughPlaneSpan(t *testing.T) {
	offset, shear, angle := gantryTiltMetrics(
		[]SliceGeometry{{Origin: Vec3{}}, {Origin: Vec3{X: 1}}},
		Vec3{Z: 1},
	)
	if offset != (Vec3{}) || shear != (Vec3{}) || angle != 0 {
		t.Fatalf("gantryTiltMetrics() = (%#v, %#v, %v), want all zeros", offset, shear, angle)
	}
}

func TestTiltedVolumeTransformsUseFiniteFallbackSliceSpacing(t *testing.T) {
	vol := &Volume{
		Origin:       Vec3{},
		AxisX:        Vec3{X: 1},
		AxisY:        Vec3{Y: 1},
		Normal:       Vec3{Z: 1},
		ColSpacing:   1,
		RowSpacing:   1,
		SliceSpacing: math.NaN(),
		regular:      true,
		tilt:         GantryTiltCorrection{Applied: true},
	}
	voxel := Vec3{X: 2, Y: 3, Z: 4}
	patient := vol.VoxelToPatient(voxel)
	assertVec3Near(t, patient, Vec3{X: 2, Y: 3, Z: 4}, 1e-9)
	assertVec3Near(t, vol.PatientToVoxel(patient), voxel, 1e-9)
}

func TestPatientToVoxelUsesRecordedPositionsForRegularStack(t *testing.T) {
	stack := tiltedGradientRowStack(
		4,
		4,
		[]Vec3{{0, 0, 0}, {0, 1, 1}, {0, 2.04, 2.04}},
		1,
		1,
	)
	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	if !vol.regular {
		t.Fatal("test stack must remain within the regular-spacing tolerance")
	}

	got := vol.PatientToVoxel(Vec3{Y: 1, Z: 1})
	assertVec3Near(t, got, Vec3{Z: 1}, 1e-9)
}

func TestSliceGeometryFailureErrorFailsClosedForUnknownFailure(t *testing.T) {
	err := sliceGeometryFailureError(sliceGeometryFailure(255), 0)
	if !errors.Is(err, ErrNonCorrectableGeometry) {
		t.Fatalf("sliceGeometryFailureError() = %v, want ErrNonCorrectableGeometry", err)
	}
}

func TestSliceOriginAtRejectsNonFiniteIndexes(t *testing.T) {
	tests := []struct {
		name    string
		origins []Vec3
		origin  Vec3
		want    Vec3
	}{
		{
			name:   "empty origins fall back to volume origin",
			origin: Vec3{X: 7, Y: 8, Z: 9},
			want:   Vec3{X: 7, Y: 8, Z: 9},
		},
		{
			name:    "existing origins fall back to first origin",
			origins: []Vec3{{X: 1, Y: 2, Z: 3}, {X: 4, Y: 5, Z: 6}},
			origin:  Vec3{X: 7, Y: 8, Z: 9},
			want:    Vec3{X: 1, Y: 2, Z: 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vol := &Volume{Origin: test.origin, sliceOrigins: test.origins}
			for _, z := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
				if got := vol.sliceOriginAt(z); got != test.want {
					t.Fatalf("sliceOriginAt(%v) = %#v, want %#v", z, got, test.want)
				}
			}
		})
	}
}

func TestCorrectedSlabAxisMatchesOrthogonalIndexConvention(t *testing.T) {
	vol := &Volume{
		AxisX:      Vec3{X: 1},
		AxisY:      Vec3{Y: 1},
		ColSpacing: 0.5,
		RowSpacing: 0.75,
	}
	tests := []struct {
		plane MPRPlane
		axis  Vec3
		step  float64
		count int
	}{
		{plane: MPRPlaneCoronal, axis: vol.AxisY, step: vol.RowSpacing, count: 11},
		{plane: MPRPlaneSagittal, axis: vol.AxisX, step: vol.ColSpacing, count: 13},
	}
	for _, test := range tests {
		axis, step, count := vol.correctedSlabAxis(test.plane, 11, 13)
		if axis != test.axis || step != test.step || count != test.count {
			t.Fatalf("correctedSlabAxis(%s) = (%#v, %v, %d), want (%#v, %v, %d)", test.plane, axis, step, count, test.axis, test.step, test.count)
		}
	}
}

func TestCollectSliceGeometriesPreservesRenderableFrameFiltering(t *testing.T) {
	stack := tiltedGradientRowStack(4, 4, []Vec3{{0, 0, 0}, {0, 1, 1}, {0, 2, 2}}, 1, 1)
	decodeFailure := volumeTestFrame(4, 4, make([]byte, 16), 0)
	decodeFailure.DecodeErr = errors.New("synthetic decode failure")
	emptyGrid := volumeTestFrame(4, 4, make([]byte, 16), 0)
	emptyGrid.Metadata.Rows = 0
	stack.Frames = append([]*Frame{nil, decodeFailure, emptyGrid}, stack.Frames...)

	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	if vol.Depth != 3 {
		t.Fatalf("BuildVolume depth = %d, want 3 renderable frames", vol.Depth)
	}
	geometry, ok := stack.VolumeGeometry()
	if !ok {
		t.Fatal("VolumeGeometry() ok = false")
	}
	if len(geometry.Slices) != 3 {
		t.Fatalf("VolumeGeometry slices = %d, want 3 renderable frames", len(geometry.Slices))
	}
}

func TestRenderMPRPlaneCorrectsGantryTiltInPatientSpace(t *testing.T) {
	stack := tiltedGradientRowStack(4, 5, []Vec3{{0, 0, 0}, {0, 1, 1}, {0, 2, 2}}, 1, 1)
	window := WindowLevel{Center: 127.5, Width: 255}
	originalPixels := make([][]byte, len(stack.Frames))
	for i, frame := range stack.Frames {
		originalPixels[i] = bytes.Clone(frame.PixelBytes)
	}

	img, err := RenderMPRPlane(stack, MPRPlaneCoronal, 2, window)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.Bounds().Dx(), 5; got != want {
		t.Fatalf("corrected width = %d, want %d", got, want)
	}
	if got, want := img.Bounds().Dy(), 3; got != want {
		t.Fatalf("corrected height = %d, want %d", got, want)
	}

	mapper := prepareWindow(window)
	for outputY, sourceRow := range []int{0, 1, 2} {
		want := displayGrayMapped(float64(10+sourceRow*80), mapper, "MONOCHROME2")
		if got := grayAt(img, 2, outputY); got != want {
			t.Fatalf("corrected sample y=%d = %d, want source row %d (%d)", outputY, got, sourceRow, want)
		}
	}
	sourceOptions := DefaultMPRRenderOptions()
	sourceOptions.GantryTiltMode = GantryTiltSourceGeometry
	sourceImage, err := RenderMPRPlaneWithOptions(stack, MPRPlaneCoronal, 2, window, sourceOptions)
	if err != nil {
		t.Fatal(err)
	}
	sourceWant := displayGrayMapped(float64(10+2*80), mapper, "MONOCHROME2")
	for outputY := 0; outputY < sourceImage.Bounds().Dy(); outputY++ {
		if got := grayAt(sourceImage, 2, outputY); got != sourceWant {
			t.Fatalf("source-geometry sample y=%d = %d, want acquired row 2 (%d)", outputY, got, sourceWant)
		}
	}

	fullMIP, err := RenderSlab(stack, MPRPlaneCoronal, 2, 0, SlabMIP, window)
	if err != nil {
		t.Fatal(err)
	}
	for outputY, sourceRow := range []int{1, 2, 3} {
		want := displayGrayMapped(float64(10+sourceRow*80), mapper, "MONOCHROME2")
		if got := grayAt(fullMIP, 2, outputY); got != want {
			t.Fatalf("corrected full MIP y=%d = %d, want highest in-bounds source row %d (%d)", outputY, got, sourceRow, want)
		}
	}
	sourceMIP, err := RenderSlabWithOptions(stack, MPRPlaneCoronal, 2, 0, SlabMIP, window, sourceOptions)
	if err != nil {
		t.Fatal(err)
	}
	sourceMIPWant := displayGrayMapped(float64(10+3*80), mapper, "MONOCHROME2")
	for outputY := 0; outputY < sourceMIP.Bounds().Dy(); outputY++ {
		if got := grayAt(sourceMIP, 2, outputY); got != sourceMIPWant {
			t.Fatalf("source-geometry MIP y=%d = %d, want acquired-grid maximum %d", outputY, got, sourceMIPWant)
		}
	}
	for i, frame := range stack.Frames {
		if !bytes.Equal(frame.PixelBytes, originalPixels[i]) {
			t.Fatalf("source frame %d was mutated by gantry-tilt correction", i)
		}
	}
}

func TestRenderSlabCorrectsTiltedSagittalProjectionDirection(t *testing.T) {
	const (
		rows = 3
		cols = 3
	)
	stack := &Stack{
		PixelSpacing:   []float64{1, 1},
		SliceThickness: 1,
		DefaultWindow:  WindowLevel{Center: 100, Width: 200},
	}
	for z, origin := range []Vec3{{0, 0, 0}, {0, 1, 1}, {0, 2, 2}} {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				data[y*cols+x] = byte(x * 50)
			}
		}
		frame := volumeTestFrame(rows, cols, data, z)
		frame.PixelSpacing = []float64{1, 1}
		frame.ImagePosition = []float64{origin.X, origin.Y, origin.Z}
		stack.Frames = append(stack.Frames, frame)
	}

	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	plane, _, _, ok := vol.correctedOrthogonalPlane(MPRPlaneSagittal, 0)
	if !ok {
		t.Fatal("correctedOrthogonalPlane() ok = false")
	}
	assertVec3Near(t, plane.U.Normalize(), vol.AxisY, 1e-9)

	img, err := RenderSlab(stack, MPRPlaneSagittal, 0, 2, SlabMIP, stack.DefaultWindow)
	if err != nil {
		t.Fatal(err)
	}
	want := displayGrayMapped(50, prepareWindow(stack.DefaultWindow), "MONOCHROME2")
	if got := grayAt(img, rows-1, 0); got != want {
		t.Fatalf("corrected sagittal slab = %d, want +AxisX neighbour %d", got, want)
	}
}

func TestBuildVolumeRejectsNonCorrectableGeometry(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Stack)
	}{
		{
			name: "missing position",
			mutate: func(stack *Stack) {
				stack.Frames[1].ImagePosition = nil
			},
		},
		{
			name: "missing orientation",
			mutate: func(stack *Stack) {
				stack.Frames[1].ImageOrientation = nil
			},
		},
		{
			name: "mixed ImagePosition and SliceLocation fallback",
			mutate: func(stack *Stack) {
				stack.Frames[1].ImagePosition = nil
				stack.Frames[1].SliceLocation = 1
				stack.Frames[1].SliceLocationOK = true
			},
		},
		{
			name: "through-plane orientation drift",
			mutate: func(stack *Stack) {
				angle := 5 * math.Pi / 180
				stack.Frames[1].ImageOrientation = []float64{1, 0, 0, 0, math.Cos(angle), math.Sin(angle)}
			},
		},
		{
			name: "in-plane orientation drift",
			mutate: func(stack *Stack) {
				angle := 5 * math.Pi / 180
				stack.Frames[1].ImageOrientation = []float64{math.Cos(angle), math.Sin(angle), 0, -math.Sin(angle), math.Cos(angle), 0}
			},
		},
		{
			name: "duplicate normal position",
			mutate: func(stack *Stack) {
				stack.Frames[1].ImagePosition = append([]float64(nil), stack.Frames[0].ImagePosition...)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := tiltedGradientRowStack(4, 4, []Vec3{{0, 0, 0}, {0, 1, 1}, {0, 2, 2}}, 1, 1)
			tt.mutate(stack)
			_, err := BuildVolume(stack)
			if !errors.Is(err, ErrNonCorrectableGeometry) {
				t.Fatalf("BuildVolume() error = %v, want ErrNonCorrectableGeometry", err)
			}
			if _, renderErr := RenderMPRPlane(stack, MPRPlaneCoronal, 1, WindowLevel{Center: 128, Width: 256}); !errors.Is(renderErr, ErrNonCorrectableGeometry) {
				t.Fatalf("RenderMPRPlane() error = %v, want fail-closed ErrNonCorrectableGeometry", renderErr)
			}
			if _, renderErr := RenderSlab(stack, MPRPlaneCoronal, 1, 2, SlabMIP, WindowLevel{Center: 128, Width: 256}); !errors.Is(renderErr, ErrNonCorrectableGeometry) {
				t.Fatalf("RenderSlab() error = %v, want fail-closed ErrNonCorrectableGeometry", renderErr)
			}
			if _, renderErr := RenderVolumeSlab(stack, MPRPlaneCoronal, 1, 2, WindowLevel{Center: 128, Width: 256}, DefaultVolumeSlabOptions()); !errors.Is(renderErr, ErrNonCorrectableGeometry) {
				t.Fatalf("RenderVolumeSlab() error = %v, want fail-closed ErrNonCorrectableGeometry", renderErr)
			}
		})
	}
}

func tiltedGradientRowStack(rows, cols int, origins []Vec3, rowSpacing, colSpacing float64) *Stack {
	stack := &Stack{PixelSpacing: []float64{rowSpacing, colSpacing}, SliceThickness: 1}
	for z, origin := range origins {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				data[y*cols+x] = byte(10 + y*80)
			}
		}
		frame := volumeTestFrame(rows, cols, data, z)
		frame.PixelSpacing = []float64{rowSpacing, colSpacing}
		frame.ImagePosition = []float64{origin.X, origin.Y, origin.Z}
		stack.Frames = append(stack.Frames, frame)
	}
	return stack
}

func assertVec3Near(t *testing.T, got, want Vec3, tolerance float64) {
	t.Helper()
	if math.Abs(got.X-want.X) > tolerance ||
		math.Abs(got.Y-want.Y) > tolerance ||
		math.Abs(got.Z-want.Z) > tolerance {
		t.Fatalf("Vec3 = %#v, want %#v (tolerance %g)", got, want, tolerance)
	}
}

func BenchmarkGantryTiltCorrectedCoronalMPR(b *testing.B) {
	origins := make([]Vec3, 64)
	for i := range origins {
		origins[i] = Vec3{Y: float64(i) * 0.25, Z: float64(i)}
	}
	stack := tiltedGradientRowStack(128, 128, origins, 0.75, 0.75)
	window := WindowLevel{Center: 127.5, Width: 255}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := RenderMPRPlane(stack, MPRPlaneCoronal, 64, window); err != nil {
			b.Fatal(err)
		}
	}
}
