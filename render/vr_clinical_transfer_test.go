package render

import (
	"crypto/sha256"
	"encoding/hex"
	"image"
	"math"
	"testing"
)

func TestVRWindowCoordinateUsesContinuousClinicalBounds(t *testing.T) {
	window := WindowLevel{Center: 40, Width: 400}
	tests := []struct {
		value float64
		want  float64
	}{
		{value: -200, want: 0},
		{value: -160, want: 0},
		{value: -60, want: 0.25},
		{value: 40, want: 0.5},
		{value: 140, want: 0.75},
		{value: 240, want: 1},
		{value: 300, want: 1},
	}
	for _, test := range tests {
		if got := VRWindowCoordinate(test.value, window); got != test.want {
			t.Errorf("VRWindowCoordinate(%v) = %.12f, want %.12f", test.value, got, test.want)
		}
	}
	if lower, upper := VRWindowBounds(window); lower != -160 || upper != 240 {
		t.Fatalf("VRWindowBounds() = %.12f..%.12f, want -160..240", lower, upper)
	}
}

func TestVRMusclesBonesRGBResourceChecksumAndSentinels(t *testing.T) {
	table := VRMusclesBonesRGB8()
	bytes := make([]byte, 0, len(table)*3)
	for _, color := range table {
		bytes = append(bytes, color[:]...)
	}
	sum := sha256.Sum256(bytes)
	if got := hex.EncodeToString(sum[:]); got != VRMusclesBonesRGBSHA256 {
		t.Fatalf("VR Muscles-Bones checksum = %s, want %s", got, VRMusclesBonesRGBSHA256)
	}
	want := map[int][3]uint8{
		0:   {0, 0, 0},
		63:  {169, 14, 18},
		95:  {255, 21, 27},
		127: {255, 117, 16},
		191: {255, 229, 33},
		223: {255, 247, 125},
		255: {255, 254, 251},
	}
	for index, expected := range want {
		if table[index] != expected {
			t.Errorf("VR Muscles-Bones[%d] = %v, want %v", index, table[index], expected)
		}
	}
}

func TestClinicalVROpacityCurves(t *testing.T) {
	linear := []float64{0, 0.25, 0.5, 0.75, 1}
	logarithmic := []float64{0, 0.11069829749368966, 0.2596373105057561, 0.4881166390211257, 1}
	for index, value := range linear {
		if got := VRLinearOpacity(value); math.Abs(got-value) > 1e-12 {
			t.Errorf("linear(%v) = %.12f", value, got)
		}
		if got := VRLogarithmicInverseOpacity(value); math.Abs(got-logarithmic[index]) > 1e-12 {
			t.Errorf("log-inverse(%v) = %.12f, want %.12f", value, got, logarithmic[index])
		}
	}
	if VRLinearOpacity(-1) != 0 || VRLinearOpacity(2) != 1 ||
		VRLogarithmicInverseOpacity(-1) != 0 || VRLogarithmicInverseOpacity(2) != 1 {
		t.Fatal("clinical opacity curves did not clamp to 0..1")
	}
}

func TestLungAdvancedRGBAUsesCanonicalNormalizedBreakpoints(t *testing.T) {
	tf := NewLungAdvancedVRTransferFunction()
	if tf.Domain() != VRTransferDomainNormalized ||
		tf.ColorMapping() != VRColorMappingLungAdvancedRGBA ||
		tf.OpacityMapping() != VROpacityMappingEmbeddedRGBA {
		t.Fatalf("Lung metadata = domain %d color %d alpha %d", tf.Domain(), tf.ColorMapping(), tf.OpacityMapping())
	}
	points := []struct {
		t     float64
		alpha float64
	}{
		{t: 0, alpha: 0},
		{t: 0.14468519748445072, alpha: 0.049316491931676865},
		{t: 0.6390236282900787, alpha: 0.24969108402729034},
		{t: 1, alpha: 0},
	}
	for _, point := range points {
		r, g, b := tf.ColorAt(point.t)
		if r != 0 || math.Abs(g-0.60537117719650269) > 1e-15 || math.Abs(b-0.70577555894851685) > 1e-15 {
			t.Errorf("Lung color(%v) = %.16f %.16f %.16f", point.t, r, g, b)
		}
		if got := tf.AlphaAt(point.t); math.Abs(got-point.alpha) > 1e-15 {
			t.Errorf("Lung alpha(%v) = %.16f, want %.16f", point.t, got, point.alpha)
		}
	}
	middle := (points[1].t + points[2].t) / 2
	wantMiddle := (points[1].alpha + points[2].alpha) / 2
	if got := tf.AlphaAt(middle); math.Abs(got-wantMiddle) > 1e-15 {
		t.Fatalf("Lung midpoint alpha = %.16f, want %.16f", got, wantMiddle)
	}
	lut := tf.BakeLUT(0, 1, ClinicalVRLUTSize)
	maxError := 0.0
	for index := 0; index <= 10000; index++ {
		position := float64(index) / 10000
		delta := math.Abs(lut.Lookup(position).A - tf.AlphaAt(position))
		maxError = math.Max(maxError, delta)
	}
	// A 4096-entry table preserves the piecewise curve, including its two
	// non-grid-aligned knees, to substantially better than 5e-5 alpha.
	if maxError > 5e-5 {
		t.Fatalf("Lung 4096-sample LUT max alpha error = %.9g, want <= 5e-5", maxError)
	}
}

func TestVRLUTLinearInterpolationAndSingleSRGBConversion(t *testing.T) {
	tf := NewNormalizedVRTransferFunction(
		[]TFColorStop{{HU: 0, R: 0.5, G: 0.5, B: 0.5}, {HU: 1, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: 0, A: 0}, {HU: 1, A: 1}},
		1,
	)
	lut := tf.BakeLUT(0, 1, 2)
	first := lut.Lookup(0)
	if want := srgbToLinear(0.5); math.Abs(first.R-want) > 1e-15 {
		t.Fatalf("first linear RGB = %.16f, want one sRGB conversion %.16f", first.R, want)
	}
	quarter := lut.Lookup(0.25)
	wantRed := lerp(srgbToLinear(0.5), 1, 0.25)
	if math.Abs(quarter.R-wantRed) > 1e-15 || math.Abs(quarter.A-0.25) > 1e-15 {
		t.Fatalf("interpolated LUT(0.25) = %+v, want R %.16f A .25", quarter, wantRed)
	}
}

func TestMusclesBonesLUTInterpolatesBetweenLinearized8BitEntries(t *testing.T) {
	tf := NewMusclesBonesVRTransferFunction(VROpacityMappingLinear)
	lut := tf.BakeLUT(0, 1, 256)
	const lower = 94
	position := (float64(lower) + 0.5) / 255
	got := lut.Lookup(position)
	table := VRMusclesBonesRGB8()
	want := func(channel int) float64 {
		return 0.5 * (srgbToLinear(float64(table[lower][channel])/255) +
			srgbToLinear(float64(table[lower+1][channel])/255))
	}
	if math.Abs(got.R-want(0)) > 1e-15 || math.Abs(got.G-want(1)) > 1e-15 || math.Abs(got.B-want(2)) > 1e-15 {
		t.Fatalf("linearized CLUT midpoint = %+v, want RGB %.16f %.16f %.16f", got, want(0), want(1), want(2))
	}
}

func TestNormalizedVRLUTCacheIgnoresWindowOnlyChanges(t *testing.T) {
	tf := NewMusclesBonesVRTransferFunction(VROpacityMappingLinear)
	first := tf.BakeWindowLUT(WindowLevel{Center: 0, Width: 100}, 256)
	second := tf.BakeWindowLUT(WindowLevel{Center: 500, Width: 2000}, 256)
	if len(first.entries) == 0 || &first.entries[0] != &second.entries[0] {
		t.Fatal("normalized window change rebuilt the immutable LUT")
	}
	scaled := tf.WithOpacityScale(0.5).BakeWindowLUT(WindowLevel{Center: 0, Width: 100}, 256)
	if &first.entries[0] == &scaled.entries[0] {
		t.Fatal("opacity-scale change reused an incompatible LUT identity")
	}
	cloned := tf.Clone().BakeWindowLUT(WindowLevel{Center: -500, Width: 50}, 256)
	if &first.entries[0] != &cloned.entries[0] {
		t.Fatal("immutable presentation clone discarded the reusable LUT cache")
	}
}

func TestVRTransferWithControlPointsPreservesClinicalIdentity(t *testing.T) {
	base := NewMusclesBonesVRTransferFunction(VROpacityMappingLogarithmicInverse).
		WithOpacityUnitDistanceMM(2.5).
		WithOpacityScale(0.4)
	custom := base.WithControlPoints(
		[]TFColorStop{{HU: 0}, {HU: 1, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: 0, A: 0.1}, {HU: 1, A: 0.9}},
	)
	if custom.Domain() != VRTransferDomainNormalized ||
		custom.ColorMapping() != VRColorMappingControlPoints ||
		custom.OpacityMapping() != VROpacityMappingControlPoints ||
		custom.ColorSpace() != VRColorSpaceSRGB ||
		custom.OpacityUnitDistanceMM() != 2.5 || custom.OpacityScale() != 0.4 {
		t.Fatalf("customized clinical transfer metadata = domain %d color %d alpha %d space %d unit %g scale %g",
			custom.Domain(), custom.ColorMapping(), custom.OpacityMapping(), custom.ColorSpace(), custom.OpacityUnitDistanceMM(), custom.OpacityScale())
	}
}

func TestClinicalDVRSampleUsesWindowAsOnlyRGBAAddress(t *testing.T) {
	tf := NewNormalizedVRTransferFunction(
		[]TFColorStop{{HU: 0}, {HU: 1, R: 1}},
		[]TFAlphaStop{{HU: 0, A: 0.2}, {HU: 1, A: 0.8}},
		1,
	)
	lut := tf.BakeWindowLUT(WindowLevel{Center: 0, Width: 100}, ClinicalVRLUTSize)
	firstWindow := WindowLevel{Center: 0, Width: 100}
	secondWindow := WindowLevel{Center: 500, Width: 200}
	first := dvrTransferSample(tf, lut, 0, firstWindow, -1000, 3000)
	second := dvrTransferSample(tf, lut, 500, secondWindow, -1000, 3000)
	if math.Abs(first.R-second.R) > 1e-15 || math.Abs(first.A-second.A) > 1e-15 {
		t.Fatalf("same t under shifted window = %+v and %+v", first, second)
	}
	if math.Abs(first.A-0.5) > 1e-9 {
		t.Fatalf("clinical alpha at WL = %.12f, want LUT alpha .5 without a second window multiplication", first.A)
	}
}

func TestClinicalDVRUsesWindowForSyntheticVolumeRGBA(t *testing.T) {
	const (
		rows  = 3
		cols  = 5
		depth = 3
	)
	values := []byte{0, 64, 128, 192, 255}
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < depth; z++ {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			copy(data[y*cols:(y+1)*cols], values)
		}
		stack.Frames = append(stack.Frames, volumeTestFrame(rows, cols, data, z))
	}
	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	sampler, ok := newVolumeSampler(volume)
	if !ok {
		t.Fatal("newVolumeSampler() rejected synthetic volume")
	}
	defer sampler.Close()

	tf := NewNormalizedVRTransferFunction(
		[]TFColorStop{{HU: 0}, {HU: 1, R: 1}},
		[]TFAlphaStop{{HU: 0, A: 0.2}, {HU: 1, A: 0.8}},
		1,
	)
	window := WindowLevel{Center: 128, Width: 128}
	lut := tf.BakeWindowLUT(window, ClinicalVRLUTSize)
	wantT := []float64{0, 0, 0.5, 1, 1}
	for x, expectedT := range wantT {
		hu, sampled := sampler.textureAt(Vec3{X: float64(x) / float64(cols-1), Y: 0.5, Z: 0.5})
		if !sampled {
			t.Fatalf("synthetic voxel %d was not sampled", x)
		}
		rgba := dvrTransferSample(tf, lut, hu, window, 0, 255)
		if math.Abs(rgba.R-expectedT) > 2e-4 || math.Abs(rgba.A-(0.2+0.6*expectedT)) > 2e-4 {
			t.Errorf("voxel %d HU %.0f => %+v, want t %.2f and alpha %.2f", x, hu, rgba, expectedT, 0.2+0.6*expectedT)
		}
	}

	shifted := WindowLevel{Center: 192, Width: 128}
	shiftedSample := dvrTransferSample(tf, lut, 192, shifted, 0, 255)
	expanded := WindowLevel{Center: 128, Width: 256}
	expandedSample := dvrTransferSample(tf, lut, 64, expanded, 0, 255)
	if math.Abs(shiftedSample.R-0.5) > 2e-4 || math.Abs(shiftedSample.A-0.5) > 2e-4 {
		t.Fatalf("shifted WL sample = %+v, want t=.5 RGBA", shiftedSample)
	}
	if math.Abs(expandedSample.R-0.25) > 2e-4 || math.Abs(expandedSample.A-0.35) > 2e-4 {
		t.Fatalf("expanded WW sample = %+v, want t=.25 RGBA", expandedSample)
	}
}

func TestClinicalVRPreviewAndSettledUseSameScalarSelection(t *testing.T) {
	volume, err := BuildVolume(sphereStack(18, 6))
	if err != nil {
		t.Fatal(err)
	}
	camera := NewVRCamera(volume.BoundingRadiusMM())
	preset := VRPreset{
		Name: "clinical-preview",
		TF:   NewMusclesBonesVRTransferFunction(VROpacityMappingLogarithmicInverse),
		Mode: VRModeDVR,
	}
	window := WindowLevel{Center: 128, Width: 256}
	quality := VRQuality{Width: 20, Height: 20, MaxSteps: 192}
	preview := RenderVRPreview(volume, camera, preset, window, nil, quality)
	settled := RenderVR(volume, camera, preset, window, false, nil, quality)
	if !sameImage(preview, settled) {
		t.Fatal("preview changed the clinical scalar/LUT selection")
	}
}

func TestClinicalTransferDoesNotAffectNonDVRProjectionSemantics(t *testing.T) {
	volume, err := BuildVolume(sphereStack(14, 5))
	if err != nil {
		t.Fatal(err)
	}
	camera := NewVRCamera(volume.BoundingRadiusMM())
	window := WindowLevel{Center: 128, Width: 256}
	quality := VRQuality{Width: 12, Height: 12, MaxSteps: 96}
	clinical := NewMusclesBonesVRTransferFunction(VROpacityMappingLogarithmicInverse)
	legacy := NewVRTransferFunction(
		[]TFColorStop{{HU: 0}, {HU: 1, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: 0, A: 1}, {HU: 1, A: 0}},
	)
	for _, mode := range []VRMode{VRModeMIP, VRModeMinIP, VRModeAverage} {
		first := RenderVR(volume, camera, VRPreset{TF: clinical, Mode: mode}, window, false, nil, quality)
		second := RenderVR(volume, camera, VRPreset{TF: legacy, Mode: mode}, window, false, nil, quality)
		if !sameImage(first, second) {
			t.Errorf("non-DVR mode %d started depending on the transfer LUT", mode)
		}
	}
}

func TestRenderVRPhysicalAlphaStableAcrossMaxSteps(t *testing.T) {
	volume, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	camera := NewVRCamera(volume.BoundingRadiusMM())
	transfer := NewNormalizedVRTransferFunction(
		[]TFColorStop{{HU: 0, R: 1, G: 1, B: 1}, {HU: 1, R: 1, G: 1, B: 1}},
		[]TFAlphaStop{{HU: 0, A: 0}, {HU: 1, A: 0.02}},
		1,
	)
	preset := VRPreset{Name: "physical-alpha", TF: transfer, Mode: VRModeDVR}
	window := WindowLevel{Center: 128, Width: 256}
	low := RenderVR(volume, camera, preset, window, false, nil, VRQuality{Width: 32, Height: 32, MaxSteps: 96}).(*image.NRGBA)
	high := RenderVR(volume, camera, preset, window, false, nil, VRQuality{Width: 32, Height: 32, MaxSteps: 768}).(*image.NRGBA)
	lowCenter := low.NRGBAAt(16, 16)
	highCenter := high.NRGBAAt(16, 16)
	if delta := math.Abs(float64(lowCenter.R) - float64(highCenter.R)); delta > 6 {
		t.Fatalf("center brightness changed by %.0f across MaxSteps: low=%v high=%v", delta, lowCenter, highCenter)
	}
}

func TestCorrectVRAlphaUsesPhysicalDistanceAndFallback(t *testing.T) {
	want := 1 - math.Pow(1-0.4*0.5, 2.5/2)
	if got := CorrectVRAlpha(0.4, 0.5, 2.5, 2); math.Abs(got-want) > 1e-15 {
		t.Fatalf("CorrectVRAlpha() = %.16f, want %.16f", got, want)
	}
	fallback := CorrectVRAlpha(0.4, 1, 0.5, 0)
	wantFallback := 1 - math.Pow(0.6, 0.5)
	if math.Abs(fallback-wantFallback) > 1e-15 {
		t.Fatalf("CorrectVRAlpha(unit=0) = %.16f, want 1 mm fallback %.16f", fallback, wantFallback)
	}
}
