package render

import (
	"image"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

func TestRenderVRHonorsVOILUTFunction(t *testing.T) {
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	quality := DefaultVRQuality(24, 24)
	linear := RenderVR(vol, cam, opaqueAbovePreset(), WindowLevel{Center: 128, Width: 256}, false, nil, quality)
	sigmoid := RenderVR(vol, cam, opaqueAbovePreset(), WindowLevel{Center: 128, Width: 256, Function: display.VOISigmoid}, false, nil, quality)
	if sameImage(linear, sigmoid) {
		t.Fatal("RenderVR returned identical LINEAR and SIGMOID images")
	}
}

func Test_RenderVR_renders_opaque_sphere_brighter_than_background(t *testing.T) {
	// Given
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())

	// When
	img := RenderVR(vol, cam, opaqueAbovePreset(), WindowLevel{Center: 128, Width: 256}, false, nil, DefaultVRQuality(48, 48)).(*image.NRGBA)

	// Then
	center := vrBrightness(img, 24, 24)
	corner := vrBrightness(img, 1, 1)
	if center <= corner {
		t.Fatalf("center brightness = %d, corner = %d; want center brighter", center, corner)
	}
	if center == 0 {
		t.Fatal("center pixel is black, want visible sphere")
	}
	if corner > 30 {
		t.Fatalf("corner brightness = %d, want near black", corner)
	}
}

func Test_vrRayGenerator_matches_camera_rays(t *testing.T) {
	cam := NewVRCamera(42)
	cam.Rotate(17, -9)
	cam.RollBy(0.2)
	gen := newVRRayGenerator(cam, 65, 47)

	for _, point := range [][2]int{{0, 0}, {17, 9}, {32, 23}, {64, 46}} {
		px, py := point[0], point[1]
		wantOrigin, wantDir := cam.Ray(px, py, 65, 47)
		gotOrigin, gotDir := gen.ray(px, gen.rowBase(py))
		if !vecApproxEqual(gotOrigin, wantOrigin, 1e-12) || !vecApproxEqual(gotDir, wantDir, 1e-12) {
			t.Fatalf("ray(%d,%d) origin/dir = %+v/%+v, want %+v/%+v", px, py, gotOrigin, gotDir, wantOrigin, wantDir)
		}
	}
}

func Test_vrRayGenerator_supports_parallel_and_endoscopy_camera_modes(t *testing.T) {
	cam := NewVRCamera(42)
	cam.Rotate(17, -9)
	cam.RollBy(0.2)

	cam.Projection = VRProjectionParallel
	centerOrigin, centerDir := cam.Ray(32, 23, 65, 47)
	cornerOrigin, cornerDir := cam.Ray(0, 0, 65, 47)
	if !vecApproxEqual(centerDir, cam.Forward(), 1e-12) || !vecApproxEqual(cornerDir, centerDir, 1e-12) {
		t.Fatalf("parallel ray dirs = %+v/%+v, want shared forward %+v", centerDir, cornerDir, cam.Forward())
	}
	if vecApproxEqual(cornerOrigin, centerOrigin, 1e-9) {
		t.Fatalf("parallel ray origins should vary across pixels, got %+v and %+v", centerOrigin, cornerOrigin)
	}
	gen := newVRRayGenerator(cam, 65, 47)
	gotOrigin, gotDir := gen.ray(0, gen.rowBase(0))
	if !vecApproxEqual(gotOrigin, cornerOrigin, 1e-12) || !vecApproxEqual(gotDir, cornerDir, 1e-12) {
		t.Fatalf("parallel generator ray = %+v/%+v, want camera ray %+v/%+v", gotOrigin, gotDir, cornerOrigin, cornerDir)
	}

	cam.Projection = VRProjectionEndoscopy
	origin, dir := cam.Ray(32, 23, 65, 47)
	if !vecApproxEqual(origin, cam.Target, 1e-12) {
		t.Fatalf("endoscopy origin = %+v, want target %+v", origin, cam.Target)
	}
	if !vecApproxEqual(dir, cam.Forward(), 1e-12) {
		t.Fatalf("endoscopy center dir = %+v, want forward %+v", dir, cam.Forward())
	}

	before := cam.Target
	forward := cam.Forward()
	cam.Zoom(math.E)
	moved := cam.Target.Sub(before)
	if moved.Dot(forward) <= 0 {
		t.Fatalf("endoscopy zoom-in moved target by %+v, want forward along %+v", moved, forward)
	}
}

func vecApproxEqual(got, want Vec3, tol float64) bool {
	return math.Abs(got.X-want.X) <= tol &&
		math.Abs(got.Y-want.Y) <= tol &&
		math.Abs(got.Z-want.Z) <= tol
}

func Test_PickVR_returns_surface_point_and_respects_crop_clip(t *testing.T) {
	// Given
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	window := WindowLevel{Center: 128, Width: 256}
	preset := opaqueAbovePreset()

	// When
	point, ok := PickVR(vol, cam, preset, window, nil, 24, 24, 48, 48)

	// Then
	if !ok {
		t.Fatal("PickVR() ok = false, want true for visible sphere center")
	}
	if point.X < 0 || point.Y < 0 || point.Z < 0 || point.X > 23 || point.Y > 23 || point.Z > 23 {
		t.Fatalf("PickVR() point = %+v, want patient-space point inside volume bounds", point)
	}

	// When
	clip := NewVRClip()
	clip.SetCropBox(Vec3{}, Vec3{X: 0.05, Y: 1, Z: 1})
	_, clippedOK := PickVR(vol, cam, preset, window, clip, 24, 24, 48, 48)

	// Then
	if clippedOK {
		t.Fatal("PickVR() with crop excluding the sphere ok = true, want false")
	}
}

func Test_CarveVR_cut_mask_removes_picked_surface(t *testing.T) {
	// Given
	vol, err := BuildVolume(sphereStack(24, 8))
	if err != nil {
		t.Fatal(err)
	}
	cam := NewVRCamera(vol.BoundingRadiusMM())
	window := WindowLevel{Center: 128, Width: 256}
	preset := opaqueAbovePreset()
	lasso := [][2]float64{{0, 0}, {48, 0}, {48, 48}, {0, 48}}

	// When
	mask := CarveVR(vol, cam, nil, lasso, 48, 48, true)

	// Then
	if mask.CutCount() == 0 {
		t.Fatal("CarveVR() cut count = 0, want visible voxels cut")
	}
	clip := NewVRClip()
	clip.SetCut(mask)
	if _, ok := PickVR(vol, cam, preset, window, clip, 24, 24, 48, 48); ok {
		t.Fatal("PickVR() after full-viewport cut ok = true, want cut surface hidden")
	}
}

func TestCarveVRReinitializesIncompatiblePreviousMask(t *testing.T) {
	previousVolume := &Volume{Cols: 1, Rows: 1, Depth: 1, ColSpacing: 1, RowSpacing: 1, SliceSpacing: 1}
	previous := NewVRCutMask(previousVolume)
	previous.Set(0, 0, 0, true)
	volume := &Volume{Cols: 65, Rows: 1, Depth: 1, ColSpacing: 1, RowSpacing: 1, SliceSpacing: 1}
	lasso := [][2]float64{{-100, -100}, {-90, -100}, {-90, -90}, {-100, -90}}

	mask := CarveVR(volume, VRCamera{}, previous, lasso, 32, 32, false)

	if mask == nil {
		t.Fatal("CarveVR incompatible mask = nil, want replacement")
	}
	if mask.CutCount() != 65 || !mask.IsCut(64, 0, 0) {
		t.Fatalf("CarveVR incompatible mask = %#v count %d lastCut %v, want 65-voxel replacement", mask, mask.CutCount(), mask.IsCut(64, 0, 0))
	}
	if previous.CutCount() != 1 {
		t.Fatalf("CarveVR mutated previous mask: count = %d, want 1", previous.CutCount())
	}
}
