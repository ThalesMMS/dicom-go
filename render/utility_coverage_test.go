package render

import (
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestCPRPathLengthAndControlPointCount(t *testing.T) {
	if got := (*CPRPath)(nil).Length(); got != 0 {
		t.Fatalf("nil CPRPath.Length() = %v, want 0", got)
	}
	if got := (*CPRPath)(nil).ControlPointCount(); got != 0 {
		t.Fatalf("nil CPRPath.ControlPointCount() = %v, want 0", got)
	}
	if got := NewCPRPath([]Vec3{{X: 1}, {X: 1}}); got != nil {
		t.Fatalf("NewCPRPath(duplicate only) = %#v, want nil", got)
	}

	path := NewCPRPath([]Vec3{
		{},
		{X: 3},
		{X: 3},
		{X: 3, Y: 4},
	})
	if path == nil {
		t.Fatal("NewCPRPath() = nil, want path")
	}
	if got := path.ControlPointCount(); got != 3 {
		t.Fatalf("ControlPointCount() = %d, want 3 distinct points", got)
	}
	if got := path.Length(); got != 7 {
		t.Fatalf("Length() = %v, want 7", got)
	}
	if got := path.PointAt(math.NaN()); got != (Vec3{}) {
		t.Fatalf("PointAt(NaN) = %+v, want first point", got)
	}
	if got := path.PointAt(99); got != (Vec3{X: 3, Y: 4}) {
		t.Fatalf("PointAt(clamped) = %+v, want final point", got)
	}
	if got := path.TangentAt(4); got != (Vec3{Y: 1}) {
		t.Fatalf("TangentAt(4) = %+v, want +Y", got)
	}
}

func TestVolumeClassificationString(t *testing.T) {
	tests := map[VolumeClassification]string{
		VolumeRegular:             "regular",
		VolumeSingleSlice:         "single-slice",
		VolumeIrregularSpacing:    "irregular-spacing",
		VolumeMissingSlices:       "missing-slices",
		VolumeGantryTilted:        "gantry-tilted",
		VolumeUnstableOrientation: "unstable-orientation",
		VolumeClassification(99):  "unknown",
	}

	for classification, want := range tests {
		if got := classification.String(); got != want {
			t.Fatalf("%v.String() = %q, want %q", int(classification), got, want)
		}
	}
}

func TestVRPresetsLookupAndQuality(t *testing.T) {
	presets := VRPresets()
	if len(presets) != 9 {
		t.Fatalf("VRPresets() length = %d, want 9", len(presets))
	}

	seen := map[string]bool{}
	for _, preset := range presets {
		if preset.Name == "" {
			t.Fatal("VRPresets() returned a preset with empty name")
		}
		if seen[preset.Name] {
			t.Fatalf("duplicate preset name %q", preset.Name)
		}
		seen[preset.Name] = true
	}

	got, ok := VRPresetByName(PresetMIPBWInverse)
	if !ok {
		t.Fatalf("VRPresetByName(%q) ok = false", PresetMIPBWInverse)
	}
	if got.Mode != VRModeMIP || !got.Inverse {
		t.Fatalf("VRPresetByName(%q) = %+v, want inverse MIP preset", PresetMIPBWInverse, got)
	}
	if _, ok := VRPresetByName("missing"); ok {
		t.Fatal("VRPresetByName(missing) ok = true, want false")
	}
	if got := DefaultVRPreset(); got.Name != PresetBonesSkin1 || got.Mode != VRModeDVR || !got.ShadingDefault {
		t.Fatalf("DefaultVRPreset() = %+v", got)
	}

	full := DefaultVRQuality(320, 240)
	preview := PreviewVRQuality(320, 240)
	if full.Width != 320 || full.Height != 240 || full.MaxSteps <= preview.MaxSteps {
		t.Fatalf("quality presets = full %+v preview %+v", full, preview)
	}
	if preview.MaxSteps <= 0 {
		t.Fatalf("PreviewVRQuality() = %+v, want positive MaxSteps", preview)
	}
}

func TestVRTransferFunctionCopiesAndLUTLen(t *testing.T) {
	tf := NewVRTransferFunction(
		[]TFColorStop{
			{HU: 100, R: 1},
			{HU: 0, B: 1},
		},
		[]TFAlphaStop{
			{HU: 100, A: 1},
			{HU: 0, A: 0},
		},
	)

	colors := tf.ColorStops()
	alphas := tf.AlphaStops()
	if len(colors) != 2 || colors[0].HU != 0 || colors[1].HU != 100 {
		t.Fatalf("ColorStops() = %+v, want HU-sorted copy", colors)
	}
	if len(alphas) != 2 || alphas[0].HU != 0 || alphas[1].HU != 100 {
		t.Fatalf("AlphaStops() = %+v, want HU-sorted copy", alphas)
	}

	colors[0].R = 99
	alphas[0].A = 99
	if got := tf.ColorStops()[0].R; got == 99 {
		t.Fatal("ColorStops() returned an aliased slice")
	}
	if got := tf.AlphaStops()[0].A; got == 99 {
		t.Fatal("AlphaStops() returned an aliased slice")
	}

	lut := tf.BakeLUT(0, 100, 1)
	if got := lut.Len(); got != 2 {
		t.Fatalf("BakeLUT(..., n=1).Len() = %d, want minimum 2", got)
	}
	if got := lut.Lookup(0); got.B != 1 || got.A != 0 {
		t.Fatalf("Lookup(0) = %+v, want first control point", got)
	}
	if got := lut.Lookup(1); got.R != 1 || got.A != 1 {
		t.Fatalf("Lookup(1) = %+v, want final control point", got)
	}
	if got := (VRLUT{}).Len(); got != 0 {
		t.Fatalf("zero LUT Len() = %d, want 0", got)
	}
}

func TestVRTransferFunctionBakeLUTLinearizesSRGBStops(t *testing.T) {
	tf := NewVRTransferFunction(
		[]TFColorStop{{HU: 0, R: 0.5, G: 0.5, B: 0.5}},
		[]TFAlphaStop{{HU: 0, A: 1}},
	)

	got := tf.BakeLUT(0, 1, 2).Lookup(0)
	want := srgbToLinear(0.5)
	if math.Abs(got.R-want) > 1e-12 || math.Abs(got.G-want) > 1e-12 || math.Abs(got.B-want) > 1e-12 {
		t.Fatalf("linearized LUT color = %+v, want RGB %.12f", got, want)
	}
	if roundTrip := linearToSRGB(got.R); math.Abs(roundTrip-0.5) > 1e-12 {
		t.Fatalf("linearToSRGB(srgbToLinear(0.5)) = %.12f, want 0.5", roundTrip)
	}
}

func TestVRClipMutatorsAndCutMask(t *testing.T) {
	clip := NewVRClip()
	if !clip.Empty() || !clip.keep(Vec3{X: 1.2, Y: 1.2, Z: 1.2}) {
		t.Fatalf("new clip Empty/keep = %v/%v, want true/true", clip.Empty(), clip.keep(Vec3{X: 1.2, Y: 1.2, Z: 1.2}))
	}

	clip.SetCropBox(Vec3{X: 0.25, Y: 0.25, Z: 0.25}, Vec3{X: 0.75, Y: 0.75, Z: 0.75})
	if clip.Empty() {
		t.Fatal("clip with crop reports Empty() = true")
	}
	if !clip.keep(Vec3{X: 0.5, Y: 0.5, Z: 0.5}) {
		t.Fatal("crop box rejected an interior sample")
	}
	if clip.keep(Vec3{X: 0.9, Y: 0.5, Z: 0.5}) {
		t.Fatal("crop box kept an exterior sample")
	}
	clip.ClearCropBox()
	if !clip.Empty() {
		t.Fatal("ClearCropBox() left non-empty clip")
	}

	clip.AddClipPlane(Vec3{X: 1}, 0.5)
	clip.AddClipPlane(Vec3{Y: 1}, 0.5)
	clip.AddClipPlane(Vec3{Z: 1}, 0.5)
	clip.AddClipPlane(Vec3{X: -1}, 0.5)
	if len(clip.planes) != 3 {
		t.Fatalf("clip plane count = %d, want capped at 3", len(clip.planes))
	}
	if !clip.keep(Vec3{X: 0.25, Y: 0.25, Z: 0.25}) {
		t.Fatal("clip planes rejected an interior sample")
	}
	if clip.keep(Vec3{X: 0.75, Y: 0.25, Z: 0.25}) {
		t.Fatal("clip plane kept a clipped sample")
	}
	clip.ClearPlanes()
	if !clip.Empty() {
		t.Fatal("ClearPlanes() left non-empty clip")
	}

	mask := NewVRCutMask(&Volume{Cols: 2, Rows: 2, Depth: 2})
	mask.Set(1, 1, 1, true)
	clip.SetCut(mask)
	if clip.CutMask() != mask {
		t.Fatal("CutMask() did not return installed mask")
	}
	if clip.Empty() {
		t.Fatal("clip with cut mask reports Empty() = true")
	}
	if clip.keep(Vec3{X: 1, Y: 1, Z: 1}) {
		t.Fatal("cut mask kept a cut sample")
	}
	if !clip.keep(Vec3{}) {
		t.Fatal("cut mask rejected an uncut sample")
	}

	var nilClip *VRClip
	nilClip.SetCropBox(Vec3{}, Vec3{})
	nilClip.ClearCropBox()
	nilClip.AddClipPlane(Vec3{}, 0)
	nilClip.ClearPlanes()
	nilClip.SetCut(mask)
	if !nilClip.Empty() || nilClip.CutMask() != nil || !nilClip.keep(Vec3{X: 2}) {
		t.Fatal("nil VRClip should behave as an empty keep-all clip")
	}
}

func TestVRCameraFrameAndTransforms(t *testing.T) {
	camera := NewVRCamera(2)
	if camera.Radius != 2 || camera.Distance <= camera.Radius {
		t.Fatalf("NewVRCamera() = %+v, want fitted radius and distance", camera)
	}

	right := camera.Right()
	up := camera.Up()
	forward := camera.Forward()
	if !almost(right.Length(), 1) || !almost(up.Length(), 1) || !almost(forward.Length(), 1) {
		t.Fatalf("camera basis lengths = right %.3f up %.3f forward %.3f", right.Length(), up.Length(), forward.Length())
	}
	if math.Abs(right.Dot(up)) > 1e-9 || math.Abs(right.Dot(forward)) > 1e-9 || math.Abs(up.Dot(forward)) > 1e-9 {
		t.Fatalf("camera basis is not orthogonal: right=%+v up=%+v forward=%+v", right, up, forward)
	}

	beforeDistance := camera.Distance
	camera.Zoom(2)
	if camera.Distance >= beforeDistance {
		t.Fatalf("Zoom(2) distance = %v, want less than %v", camera.Distance, beforeDistance)
	}
	camera.Zoom(0)
	if camera.Distance <= 0 {
		t.Fatal("Zoom(0) should leave a positive distance")
	}

	beforeTarget := camera.Target
	camera.Pan(10, -5)
	if camera.Target == beforeTarget {
		t.Fatal("Pan() did not move camera target")
	}

	camera.RollBy(0.25)
	if !almost(camera.Roll, 0.25) {
		t.Fatalf("RollBy() roll = %v, want 0.25", camera.Roll)
	}

	camera.FitExtent(4, 2)
	if camera.Radius != 4 || camera.Roll != 0 || camera.Distance <= 0 {
		t.Fatalf("FitExtent() = %+v", camera)
	}
	camera.SetOrientation(Vec3{Y: -1})
	if !almost(camera.Yaw, 0) || !almost(camera.Pitch, 0) || camera.Roll != 0 {
		t.Fatalf("SetOrientation(-Y) = yaw %v pitch %v roll %v", camera.Yaw, camera.Pitch, camera.Roll)
	}
	camera.SetOrientation(Vec3{})
	if !almost(camera.Yaw, 0) || !almost(camera.Pitch, 0) {
		t.Fatal("SetOrientation(zero) should leave orientation unchanged")
	}

	px, py, visible := camera.Project(Vec3{}, 100, 100)
	if !visible || math.Abs(px-50) > 1 || math.Abs(py-50) > 1 {
		t.Fatalf("Project(target) = (%v, %v, %v), want centered visible point", px, py, visible)
	}
	behind := camera.Position().Sub(camera.Forward())
	if _, _, visible := camera.Project(behind, 100, 100); visible {
		t.Fatal("Project(point behind camera) visible = true, want false")
	}
}

func TestVolumeCoordinateHelpers(t *testing.T) {
	vol := &Volume{
		Cols: 4, Rows: 2, Depth: 8,
		Origin: Vec3{X: 10, Y: 20, Z: 30},
		AxisX:  Vec3{X: 1}, AxisY: Vec3{Y: 1}, Normal: Vec3{Z: 1},
		ColSpacing: 2, RowSpacing: 3, SliceSpacing: 4,
	}

	norm := vol.VoxelToNormalized(Vec3{X: 2, Y: 1, Z: 4})
	if norm != (Vec3{X: 0.5, Y: 0.5, Z: 0.5}) {
		t.Fatalf("VoxelToNormalized() = %+v, want 0.5 in each axis", norm)
	}
	if got := vol.NormalizedToVoxel(norm); got != (Vec3{X: 2, Y: 1, Z: 4}) {
		t.Fatalf("NormalizedToVoxel() = %+v, want original voxel", got)
	}
	if got := vol.CenterVoxel(); got != (Vec3{X: 1.5, Y: 0.5, Z: 3.5}) {
		t.Fatalf("CenterVoxel() = %+v", got)
	}
	if got := (&Volume{}).VoxelToNormalized(Vec3{X: 1, Y: 1, Z: 1}); got != (Vec3{}) {
		t.Fatalf("zero-size VoxelToNormalized() = %+v, want zero", got)
	}
}

func TestStackDisplayableSkipsInvalidFrames(t *testing.T) {
	var nilStack *Stack
	if nilStack.Displayable() || nilStack.FirstDisplayFrame() != nil {
		t.Fatal("nil Stack should not be displayable")
	}

	want := &Frame{
		Metadata: pixeldata.Metadata{Rows: 1, Columns: 1},
	}
	stack := &Stack{Frames: []*Frame{
		nil,
		{DecodeErr: errors.New("decode")},
		{Encapsulated: true},
		want,
	}}
	if got := stack.FirstDisplayFrame(); got != want {
		t.Fatalf("FirstDisplayFrame() = %p, want %p", got, want)
	}
	if !stack.Displayable() {
		t.Fatal("Displayable() = false, want true")
	}
}

func TestRenderWindowThumbnailAndBlankHelpers(t *testing.T) {
	if err := ValidateWindow(WindowLevel{Center: 40, Width: 400}); err != nil {
		t.Fatalf("ValidateWindow(valid) error = %v", err)
	}
	for _, window := range []WindowLevel{
		{Center: math.NaN(), Width: 400},
		{Center: math.Inf(1), Width: 400},
		{Center: 40, Width: 0},
		{Center: 40, Width: math.NaN()},
		{Center: 40, Width: math.Inf(1)},
	} {
		if err := ValidateWindow(window); err == nil {
			t.Fatalf("ValidateWindow(%+v) error = nil, want validation error", window)
		}
	}

	centerMin, centerMax, widthMax := WindowSliderRanges(WindowLevel{Center: 50, Width: 100})
	if centerMin >= 50 || centerMax <= 50 || widthMax < 4096 {
		t.Fatalf("WindowSliderRanges() = (%v, %v, %v)", centerMin, centerMax, widthMax)
	}

	blank := blankImage(0, -1)
	if blank.Bounds().Dx() != 512 || blank.Bounds().Dy() != 512 || blank.Pix[0] != 24 {
		t.Fatalf("blankImage(0,-1) bounds/pixel = %v/%d", blank.Bounds(), blank.Pix[0])
	}

	nilThumb := RenderThumbnail(nil)
	if nilThumb.Bounds().Dx() != 128 || nilThumb.Bounds().Dy() != 128 {
		t.Fatalf("RenderThumbnail(nil) size = %v, want 128x128", nilThumb.Bounds())
	}

	stack := &Stack{DefaultWindow: WindowLevel{Center: 128, Width: 256}, Frames: []*Frame{{
		Metadata: pixeldata.Metadata{
			Rows:                      1,
			Columns:                   2,
			SamplesPerPixel:           1,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PhotometricInterpretation: "MONOCHROME2",
		},
		PixelBytes: []byte{0, 255},
		Rescale:    Rescale{Slope: 1},
	}}}
	thumb := RenderThumbnail(stack)
	if thumb.Bounds().Dx() != 2 || thumb.Bounds().Dy() != 1 {
		t.Fatalf("RenderThumbnail(valid) size = %v, want 2x1", thumb.Bounds())
	}

	pngBytes, err := RenderFramePNG(stack.Frames[0], stack.DefaultWindow)
	if err != nil {
		t.Fatalf("RenderFramePNG() error = %v", err)
	}
	if len(pngBytes) < 8 || string(pngBytes[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("RenderFramePNG() did not return a PNG header: %v", pngBytes[:min(len(pngBytes), 8)])
	}
}

func almost(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
