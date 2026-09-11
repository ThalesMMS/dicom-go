package render

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

func TestResliceFusedMPRScalarSyntheticGoldens(t *testing.T) {
	base, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	overlayStack := gradientXZStack(3, 4, 3)
	for _, frame := range overlayStack.Frames {
		frame.ImagePosition[0] = 10
	}
	overlay, err := BuildVolume(overlayStack)
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()

	tests := []struct {
		name    string
		overlay *Volume
		plane   Plane
		mapper  PatientPointMapper
		golden  []float64
	}{
		{
			name:    "orthogonal aligned",
			overlay: base,
			plane:   base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}),
			golden:  []float64{50, 51, 52, 53, 50, 51, 52, 53, 50, 51, 52, 53},
		},
		{
			name:    "orthogonal displaced",
			overlay: overlay,
			plane:   base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}),
			mapper:  func(point Vec3) (Vec3, error) { return point.Add(Vec3{X: 10}), nil },
			golden:  []float64{50, 51, 52, 53, 50, 51, 52, 53, 50, 51, 52, 53},
		},
		{
			name:    "oblique displaced",
			overlay: overlay,
			plane: Plane{
				Origin: Vec3{},
				U:      Vec3{X: 3, Z: 2},
				V:      Vec3{Y: 2},
			},
			mapper: func(point Vec3) (Vec3, error) { return point.Add(Vec3{X: 10}), nil },
			golden: []float64{
				0, 1 + 100.0/3, 2 + 200.0/3, 103,
				0, 1 + 100.0/3, 2 + 200.0/3, 103,
				0, 1 + 100.0/3, 2 + 200.0/3, 103,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planes, err := ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
				Base: base, Overlay: test.overlay, TargetPlane: test.plane,
				Width: 4, Height: 3, TargetToSource: test.mapper,
			})
			if err != nil {
				t.Fatal(err)
			}
			for index, want := range test.golden {
				x, y := index%4, index/4
				baseValue, baseOK := planes.Base.ValueAt(x, y)
				overlayValue, overlayOK := planes.Overlay.ValueAt(x, y)
				if !baseOK || !overlayOK || math.Abs(baseValue-want) > 1e-5 || math.Abs(overlayValue-want) > 1e-5 {
					t.Fatalf("pixel (%d,%d) base=%v/%v overlay=%v/%v, want %v", x, y, baseValue, baseOK, overlayValue, overlayOK, want)
				}
			}
			if planes.Report.TargetSamples != 12 || planes.Report.BaseInBounds != 12 || planes.Report.OverlayInBounds != 12 {
				t.Fatalf("sampling report = %+v, want all 12 samples in bounds", planes.Report)
			}
		})
	}
}

func TestResliceFusedMPRScalarHandlesDifferentSpacingAndDimensions(t *testing.T) {
	base := buildPhysicalGradientVolume(t, 3, 3, 3, 1, 1, 1, Vec3{})
	defer base.Close()
	overlay := buildPhysicalGradientVolume(t, 5, 5, 5, 0.5, 0.5, 0.5, Vec3{X: 10})
	defer overlay.Close()
	plane := base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1})

	planes, err := ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
		Base: base, Overlay: overlay, TargetPlane: plane,
		Width: 3, Height: 3,
		TargetToSource: func(point Vec3) (Vec3, error) {
			return point.Add(Vec3{X: 10}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			baseValue, baseOK := planes.Base.ValueAt(x, y)
			overlayValue, overlayOK := planes.Overlay.ValueAt(x, y)
			want := float64(20*x + 30)
			if !baseOK || !overlayOK || baseValue != want || overlayValue != want {
				t.Fatalf("pixel (%d,%d) base=%v/%v overlay=%v/%v, want %v", x, y, baseValue, baseOK, overlayValue, overlayOK, want)
			}
		}
	}
}

func TestResliceFusedMPRScalarUsesSameSlabExtentForBothLayers(t *testing.T) {
	base, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	overlay, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	plane := base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1})

	tests := []struct {
		mode SlabMode
		want float64
	}{
		{mode: SlabNone, want: 52},
		{mode: SlabMIP, want: 102},
		{mode: SlabMinIP, want: 2},
		{mode: SlabAverage, want: 52},
	}
	for _, test := range tests {
		t.Run(slabModeName(test.mode), func(t *testing.T) {
			planes, err := ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
				Base: base, Overlay: overlay, TargetPlane: plane, Width: 4, Height: 3,
				Slab: FusedMPRSlab{Mode: test.mode, ThicknessMM: 2, SampleSpacingMM: 1},
			})
			if err != nil {
				t.Fatal(err)
			}
			baseValue, baseOK := planes.Base.ValueAt(2, 1)
			overlayValue, overlayOK := planes.Overlay.ValueAt(2, 1)
			if !baseOK || !overlayOK || baseValue != test.want || overlayValue != test.want {
				t.Fatalf("mode %v base=%v/%v overlay=%v/%v, want %v", test.mode, baseValue, baseOK, overlayValue, overlayOK, test.want)
			}
			wantSamples := int64(12)
			if test.mode != SlabNone {
				wantSamples *= 3
			}
			if planes.Report.TargetSamples != wantSamples || planes.Report.BaseInBounds != wantSamples || planes.Report.OverlayInBounds != wantSamples {
				t.Fatalf("mode %v report = %+v, want %d samples per layer", test.mode, planes.Report, wantSamples)
			}
		})
	}
}

func TestFusedScalarPlanesCompositeAppliesIndependentPresentation(t *testing.T) {
	base, err := BuildVolume(gradientXZStack(2, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	overlay, err := BuildVolume(gradientXZStack(2, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	planes, err := ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
		Base: base, Overlay: overlay,
		TargetPlane: base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}), Width: 2, Height: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	basePresentation := FusionLayerPresentation{Window: WindowLevel{Center: 25, Width: 100}, Alpha: 1}
	overlayPresentation := FusionLayerPresentation{Window: WindowLevel{Center: 50, Width: 100}, Palette: display.HotIronLUT(), Alpha: 0.5}
	image, err := planes.Composite(context.Background(), basePresentation, overlayPresentation)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := planes.Base.ValueAt(1, 0)
	baseGray := displayGrayMapped(value, prepareWindow(basePresentation.Window), planes.Base.photometric)
	overlayGray := displayGrayMapped(value, prepareWindow(overlayPresentation.Window), planes.Overlay.photometric)
	overlayR, overlayG, overlayB := overlayPresentation.Palette.At(int(overlayGray))
	wantR := uint8(math.Round((float64(baseGray) + float64(overlayR)) / 2))
	wantG := uint8(math.Round((float64(baseGray) + float64(overlayG)) / 2))
	wantB := uint8(math.Round((float64(baseGray) + float64(overlayB)) / 2))
	gotR, gotG, gotB, gotA := image.At(1, 0).RGBA()
	if uint8(gotR>>8) != wantR || uint8(gotG>>8) != wantG || uint8(gotB>>8) != wantB || uint8(gotA>>8) != 255 {
		t.Fatalf("composite RGBA = (%d,%d,%d,%d), want (%d,%d,%d,255)", gotR>>8, gotG>>8, gotB>>8, gotA>>8, wantR, wantG, wantB)
	}
}

func TestResliceFusedMPRScalarFailsClosedOnMapperError(t *testing.T) {
	base, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	overlay, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	sentinel := errors.New("mapper sentinel")

	planes, err := ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
		Base: base, Overlay: overlay,
		TargetPlane: base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}), Width: 4, Height: 3,
		TargetToSource: func(point Vec3) (Vec3, error) {
			if point.X >= 1 {
				return Vec3{}, sentinel
			}
			return point, nil
		},
	})
	if planes != nil || !errors.Is(err, ErrFusionMapping) || !errors.Is(err, sentinel) {
		t.Fatalf("result/error = %v/%v, want nil error matching mapping and sentinel", planes, err)
	}

	planes, err = ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
		Base: base, Overlay: overlay,
		TargetPlane: base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}), Width: 4, Height: 3,
		TargetToSource: func(Vec3) (Vec3, error) { return Vec3{X: math.NaN()}, nil },
	})
	if planes != nil || !errors.Is(err, ErrFusionMapping) || !errors.Is(err, ErrMPRInvalidInput) {
		t.Fatalf("non-finite mapper result/error = %v/%v, want typed mapping input failure", planes, err)
	}
}

func TestResliceFusedMPRScalarReportsNoOverlayDomainOverlap(t *testing.T) {
	base, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	overlay, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()

	planes, err := ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
		Base: base, Overlay: overlay,
		TargetPlane: base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}), Width: 4, Height: 3,
		TargetToSource: func(point Vec3) (Vec3, error) { return point.Add(Vec3{X: 100}), nil },
	})
	if !errors.Is(err, ErrFusionNoOverlap) || planes == nil || planes.Report.OverlayInBounds != 0 {
		t.Fatalf("result/error = %+v/%v, want explicit no-overlap result", planes, err)
	}
	if _, ok := planes.Overlay.ValueAt(0, 0); ok {
		t.Fatal("no-overlap overlay unexpectedly retained a valid sample")
	}
}

func TestResliceFusedMPRScalarRejectsCombinedWorkingSet(t *testing.T) {
	base, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	overlay, err := BuildVolume(gradientXZStack(3, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	_, err = ResliceFusedMPRScalarContext(context.Background(), FusedMPRRequest{
		Base: base, Overlay: overlay,
		TargetPlane: base.OrthogonalPlane(MPRPlaneAxial, Vec3{Z: 1}), Width: 4, Height: 3,
		Limits: MPRLimits{MaxWorkingBytes: 12*18 - 1},
	})
	if !errors.Is(err, ErrMPRResourceLimit) {
		t.Fatalf("combined working-set error = %v, want ErrMPRResourceLimit", err)
	}
}

func BenchmarkMPRFusionDisabled(b *testing.B) {
	volume, err := BuildVolume(gradientXZStack(128, 128, 32))
	if err != nil {
		b.Fatal(err)
	}
	defer volume.Close()
	plane := volume.OrthogonalPlane(MPRPlaneAxial, volume.Center())
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		// Fusion-off callers retain the original one-plane API and therefore do
		// not allocate FusedScalarPlanes or acquire a secondary reader.
		result, err := ResliceObliqueScalarContext(context.Background(), volume, plane, 128, 128)
		if err != nil || result == nil {
			b.Fatalf("ResliceObliqueScalarContext = %v/%v", result, err)
		}
	}
}

func buildPhysicalGradientVolume(t *testing.T, rows, cols, depth int, rowSpacing, colSpacing, sliceSpacing float64, origin Vec3) *Volume {
	t.Helper()
	stack := gradientColumnStack(rows, cols, depth)
	stack.PixelSpacing = []float64{rowSpacing, colSpacing}
	stack.SliceThickness = sliceSpacing
	for z, frame := range stack.Frames {
		frame.PixelSpacing = []float64{rowSpacing, colSpacing}
		frame.ImagePosition = []float64{origin.X, origin.Y, origin.Z + float64(z)*sliceSpacing}
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				frame.PixelBytes[y*cols+x] = byte(math.Round(20*float64(x)*colSpacing + 30*float64(z)*sliceSpacing))
			}
		}
	}
	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	return volume
}
