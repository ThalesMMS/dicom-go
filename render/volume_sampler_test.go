package render

import (
	"image"
	"math"
	"testing"
)

func Test_volumeSamplerMatchesTrilinearAt(t *testing.T) {
	vol, err := BuildVolume(gradientXZStack(6, 7, 4))
	if err != nil {
		t.Fatal(err)
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}

	points := []Vec3{
		{X: 0, Y: 0, Z: 0},
		{X: 1.25, Y: 2.5, Z: 1.1},
		{X: 5.9, Y: 4.2, Z: 2.75},
		{X: -0.25, Y: 1.5, Z: 1},
		{X: 99, Y: 0, Z: 0},
	}
	for _, p := range points {
		got, gotOK := sampler.trilinearAt(p)
		want, wantOK := vol.TrilinearAt(p)
		if gotOK != wantOK || absFloat(got-want) > 1e-9 {
			t.Fatalf("trilinearAt(%+v) = %v/%v, want %v/%v", p, got, gotOK, want, wantOK)
		}
	}
}

func TestResliceObliqueUsesNormalizedMonochrome1Photometric(t *testing.T) {
	stack := gradientColumnStack(2, 2, 2)
	for _, frame := range stack.Frames {
		frame.Metadata.PhotometricInterpretation = " monochrome1\x00"
	}
	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}

	plane := vol.OrthogonalPlane(MPRPlaneAxial, vol.Center())
	img := ResliceOblique(vol, plane, 2, 2, WindowLevel{Center: 0.5, Width: 1})
	got := grayAt(img, 0, 0)
	want := uint8(255 - windowedGrayMapped(0, prepareWindow(WindowLevel{Center: 0.5, Width: 1})))
	if got != want {
		t.Fatalf("MONOCHROME1 reslice pixel = %d, want inverted %d", got, want)
	}
}

func TestRenderVRPreviewDisablesShadingAndGradientOpacity(t *testing.T) {
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	preset := DefaultVRPreset()
	preset.GradientOpacityScale = 1
	window := WindowLevel{Center: 128, Width: 256}
	q := PreviewVRQuality(32, 32)

	finalBefore := RenderVR(vol, cam, preset, window, true, nil, q)
	got := RenderVRPreview(vol, cam, preset, window, nil, q)
	expectedPreset := preset
	expectedPreset.GradientOpacityScale = 0
	want := RenderVR(vol, cam, expectedPreset, window, false, nil, q)
	if !sameImage(got, want) {
		t.Fatal("RenderVRPreview() should match RenderVR with shading and gradient opacity disabled")
	}
	finalAfter := RenderVR(vol, cam, preset, window, true, nil, q)
	if !sameImage(finalBefore, finalAfter) {
		t.Fatal("RenderVRPreview() should not mutate the settled RenderVR inputs")
	}
}

func sameImage(a, b image.Image) bool {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return false
	}
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func Test_newVolumeSampler_nilVolumeReturnsFalse(t *testing.T) {
	s, ok := newVolumeSampler(nil)
	if ok || s != nil {
		t.Fatalf("newVolumeSampler(nil) = %v/%v, want nil/false", s, ok)
	}
}

func Test_newVolumeSampler_zeroDimensionsReturnsFalse(t *testing.T) {
	cases := []struct {
		name  string
		stack *Stack
	}{
		{"zero cols", func() *Stack {
			s := gradientColumnStack(2, 0, 2)
			return s
		}()},
		{"zero rows", func() *Stack {
			s := gradientColumnStack(0, 2, 2)
			return s
		}()},
	}
	for _, tc := range cases {
		vol, err := BuildVolume(tc.stack)
		if err != nil {
			// BuildVolume may itself fail; that is acceptable for a degenerate input.
			continue
		}
		if vol != nil && (vol.Cols <= 0 || vol.Rows <= 0 || vol.Depth <= 0) {
			s, ok := newVolumeSampler(vol)
			if ok || s != nil {
				t.Fatalf("%s: newVolumeSampler() = %v/%v, want nil/false", tc.name, s, ok)
			}
		}
	}
}

func TestVolumeSamplerFloat32FastPathRequiresPackedStrides(t *testing.T) {
	store := NewVolumeStore()
	descriptor := testVolumeDescriptor(2, 2, 1)
	descriptor.RowStrideBytes = 3 * 4
	descriptor.SliceStrideBytes = 2 * descriptor.RowStrideBytes
	generation, err := store.ReplaceFloat32(descriptor, []float32{1, 2, 99, 3, 4, 99})
	if err != nil {
		t.Fatal(err)
	}
	vol := &Volume{
		Cols: 2, Rows: 2, Depth: 1,
		ColSpacing: 1, RowSpacing: 1, SliceSpacing: 1,
		store: store, generation: generation,
	}

	sampler, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	defer sampler.Close()
	if sampler.values != nil {
		t.Fatal("padded float32 descriptor incorrectly selected packed fast path")
	}
	if got, ok := sampler.valueAt(0, 1, 0); !ok || got != 3 {
		t.Fatalf("valueAt(0, 1, 0) = %v/%v, want 3/true", got, ok)
	}
}

func TestHURangeReusesExistingSamplerLease(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(2, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	defer sampler.Close()
	if got := vol.VolumeStoreStats().ActiveLeases; got != 1 {
		t.Fatalf("active leases before HURange = %d, want 1", got)
	}
	if _, _, ok := vol.huRangeFromSampler(sampler); !ok {
		t.Fatal("huRangeFromSampler() ok = false")
	}
	if got := vol.VolumeStoreStats().ActiveLeases; got != 1 {
		t.Fatalf("active leases after HURange = %d, want 1", got)
	}
}

func Test_volumeSampler_displayGray_noInversionForMonochrome2(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(2, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	window := WindowLevel{Center: 128, Width: 256}
	got := s.displayGrayMapped(128, prepareWindow(window))
	want := windowedGrayMapped(128, prepareWindow(window))
	if got != want {
		t.Fatalf("displayGray MONOCHROME2: got %d, want %d", got, want)
	}
}

func Test_volumeSampler_displayGray_inversionForMonochrome1(t *testing.T) {
	stack := gradientColumnStack(2, 2, 2)
	for _, frame := range stack.Frames {
		frame.Metadata.PhotometricInterpretation = "MONOCHROME1"
	}
	vol, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	if !s.invert {
		t.Fatal("MONOCHROME1 sampler should have invert=true")
	}
	window := WindowLevel{Center: 128, Width: 256}
	got := s.displayGrayMapped(128, prepareWindow(window))
	want := 255 - windowedGrayMapped(128, prepareWindow(window))
	if got != want {
		t.Fatalf("displayGray MONOCHROME1: got %d, want inverted %d", got, want)
	}
}

func Test_volumeSampler_trilinearAt_nilSamplerReturnsFalse(t *testing.T) {
	var s *volumeSampler
	val, ok := s.trilinearAt(Vec3{X: 0, Y: 0, Z: 0})
	if ok || val != 0 {
		t.Fatalf("nil sampler trilinearAt() = %v/%v, want 0/false", val, ok)
	}
}

func Test_volumeSampler_textureAt_scalesCoordinates(t *testing.T) {
	// A gradient-column volume: voxel (x, y, z) has value x.
	// textureAt samples at texture coords in [0,1], so tex=1 should map to the
	// last column, giving a high value.
	vol, err := BuildVolume(gradientColumnStack(4, 8, 4))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	// tex.X=0 → column 0 (value 0); tex.X=1 → column 7 (value 7).
	low, ok0 := s.textureAt(Vec3{X: 0, Y: 0.5, Z: 0.5})
	high, ok1 := s.textureAt(Vec3{X: 1, Y: 0.5, Z: 0.5})
	if !ok0 || !ok1 {
		t.Fatalf("textureAt ok = %v/%v, want both true", ok0, ok1)
	}
	if high <= low {
		t.Fatalf("textureAt(X=1)=%v should exceed textureAt(X=0)=%v", high, low)
	}
}

func Test_volumeSampler_gradientAt_nonZeroNearBoundary(t *testing.T) {
	// A gradient-column volume has a non-zero gradient in X everywhere.
	vol, err := BuildVolume(gradientColumnStack(4, 8, 4))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	// Gradient at the centre; since columns go 0..7 the X gradient should be positive.
	grad := s.gradientAt(Vec3{X: 0.5, Y: 0.5, Z: 0.5})
	if grad.X <= 0 {
		t.Fatalf("gradient X component = %v, want positive for gradient-column volume", grad.X)
	}
}

func TestVolumeTextureSampleMatchesValueAndLinearGradient(t *testing.T) {
	vol, err := BuildVolume(gradientXZStack(6, 7, 4))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	for _, tex := range []Vec3{
		{X: 0.5, Y: 0.5, Z: 0.5},
		{X: 0, Y: 0, Z: 0},
		{X: 1, Y: 1, Z: 1},
		{X: 0.02, Y: 0.97, Z: 0.33},
	} {
		sample, gotOK := s.textureSample(tex)
		wantValue, wantOK := s.textureAt(tex)
		if gotOK != wantOK || sample.value() != wantValue {
			t.Errorf("textureSample(%+v) = %v/%v, want %v/%v", tex, sample.value(), gotOK, wantValue, wantOK)
		}
	}
	center := Vec3{X: 0.5, Y: 0.5, Z: 0.5}
	sample, _ := s.textureSample(center)
	var gradientCell volumeGradientCell
	gotGradient := gradientCell.gradient(s, sample)
	wantGradient := s.gradientAt(center)
	if gotGradient.Sub(wantGradient).Length() > 1e-12 {
		t.Errorf("linear gradient(%+v) = %+v, want central difference %+v", center, gotGradient, wantGradient)
	}
}

func TestVolumeGradientCellTracksCentralDifference(t *testing.T) {
	vol, err := BuildVolume(sphereStack(48, 16))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := newVolumeSampler(vol)
	if !ok {
		t.Fatal("newVolumeSampler() ok = false")
	}
	var compared, zeroMismatch int
	var angleSum, angleMax, magnitudeRatioSum float64
	var gradientCell volumeGradientCell
	for z := 1; z < 31; z++ {
		for y := 1; y < 31; y++ {
			for x := 1; x < 31; x++ {
				tex := Vec3{X: float64(x) / 32, Y: float64(y) / 32, Z: float64(z) / 32}
				sample, ok := s.textureSample(tex)
				if !ok {
					continue
				}
				got := gradientCell.gradient(s, sample)
				want := s.gradientAt(tex)
				gotLength, wantLength := got.Length(), want.Length()
				if gotLength < 1e-9 || wantLength < 1e-9 {
					if (gotLength < 1e-9) != (wantLength < 1e-9) {
						zeroMismatch++
					}
					continue
				}
				cosine := got.Dot(want) / (gotLength * wantLength)
				cosine = math.Max(-1, math.Min(1, cosine))
				angle := math.Acos(cosine) * 180 / math.Pi
				angleSum += angle
				if angle > angleMax {
					angleMax = angle
				}
				magnitudeRatioSum += gotLength / wantLength
				compared++
			}
		}
	}
	if compared == 0 {
		t.Fatal("gradient comparison found no non-zero samples")
	}
	meanAngle := angleSum / float64(compared)
	meanMagnitudeRatio := magnitudeRatioSum / float64(compared)
	if zeroMismatch != 0 || meanAngle >= 1 || angleMax >= 3 || meanMagnitudeRatio < 0.95 || meanMagnitudeRatio > 1.05 {
		t.Fatalf("gradient fidelity: compared=%d zeroMismatch=%d meanAngle=%.3f maxAngle=%.3f meanMagnitudeRatio=%.3f", compared, zeroMismatch, meanAngle, angleMax, meanMagnitudeRatio)
	}
}

func TestRenderVRPreviewNilVolumeReturnsNonNilImage(t *testing.T) {
	cam := NewVRCamera(100)
	preset := DefaultVRPreset()
	window := WindowLevel{Center: 128, Width: 256}
	q := PreviewVRQuality(16, 16)

	img := RenderVRPreview(nil, cam, preset, window, nil, q)
	if img == nil {
		t.Fatal("RenderVRPreview(nil vol) should return non-nil image")
	}
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 16 {
		t.Fatalf("RenderVRPreview(nil vol) image size = %v, want 16x16", img.Bounds())
	}
}

func TestRenderVRPreviewWithMIPModeMatchesRenderVRWithGradientOpacityZero(t *testing.T) {
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	window := WindowLevel{Center: 128, Width: 256}
	q := PreviewVRQuality(24, 24)

	mipPreset := DefaultVRPreset()
	mipPreset.Mode = VRModeMIP
	mipPreset.GradientOpacityScale = 1

	got := RenderVRPreview(vol, cam, mipPreset, window, nil, q)
	expectedPreset := mipPreset
	expectedPreset.GradientOpacityScale = 0
	want := RenderVR(vol, cam, expectedPreset, window, false, nil, q)
	if !sameImage(got, want) {
		t.Fatal("RenderVRPreview with MIP mode should match RenderVR(shading=false, gradientOpacity=0)")
	}
}
