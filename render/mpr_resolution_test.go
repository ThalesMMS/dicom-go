package render

import (
	"math"
	"testing"
)

func TestPlanObliqueResolutionNativePreservesPhysicalAspect(t *testing.T) {
	vol := &Volume{ColSpacing: 0.5, RowSpacing: 0.8, SliceSpacing: 1.5}
	plane := Plane{
		U: Vec3{X: 99.5},
		V: Vec3{Y: 49.5},
	}

	plan, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Width != 200 || plan.Height != 100 {
		t.Fatalf("native plan = %dx%d, want 200x100", plan.Width, plan.Height)
	}
	if math.Abs(plan.PixelSpacing.X-plan.PixelSpacing.Y) > 0.001 {
		t.Fatalf("native pixel spacing = %+v, want physically square pixels", plan.PixelSpacing)
	}
	if plan.Bounded {
		t.Fatal("small native plan was unexpectedly bounded")
	}
}

func TestPlanObliqueResolutionPreviewKeepsExactFOVAndAspect(t *testing.T) {
	vol := &Volume{ColSpacing: 0.25, RowSpacing: 0.5, SliceSpacing: 1}
	plane := Plane{
		Origin: Vec3{X: 3, Y: 4, Z: 5},
		U:      Vec3{X: 200},
		V:      Vec3{Y: 100},
	}
	settled, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{TargetLongEdge: 1000})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{TargetLongEdge: 1000, Preview: true})
	if err != nil {
		t.Fatal(err)
	}

	if settled.Width != 1000 || settled.Height != 501 {
		t.Fatalf("settled plan = %dx%d, want 1000x501", settled.Width, settled.Height)
	}
	if preview.Width != 500 || preview.Height != 251 {
		t.Fatalf("preview plan = %dx%d, want 500x251", preview.Width, preview.Height)
	}
	if plane.At(0, 0) != plane.Origin || plane.At(1, 1) != plane.Origin.Add(plane.U).Add(plane.V) {
		t.Fatal("resolution planning changed the patient-space field of view")
	}
	settledAspect := float64(settled.Width-1) / float64(settled.Height-1)
	previewAspect := float64(preview.Width-1) / float64(preview.Height-1)
	if math.Abs(settledAspect-previewAspect) > 0.01 {
		t.Fatalf("preview/settled physical aspect = %.4f/%.4f", previewAspect, settledAspect)
	}
}

func TestObliquePreviewAndSettledSamplesAreEquivalentOnSharedGrid(t *testing.T) {
	vol, err := BuildVolume(gradientColumnStack(32, 64, 4))
	if err != nil {
		t.Fatal(err)
	}
	plane := Plane{
		Origin: Vec3{X: 10, Y: 10, Z: 1},
		U:      Vec3{X: 20},
		V:      Vec3{Y: 10},
	}
	settledPlan, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{TargetLongEdge: 81})
	if err != nil {
		t.Fatal(err)
	}
	previewPlan, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{TargetLongEdge: 81, Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	settled := ResliceOblique(vol, plane, settledPlan.Width, settledPlan.Height, WindowLevel{Center: 31.5, Width: 63})
	preview := ResliceOblique(vol, plane, previewPlan.Width, previewPlan.Height, WindowLevel{Center: 31.5, Width: 63})

	for y := 0; y < previewPlan.Height; y++ {
		for x := 0; x < previewPlan.Width; x++ {
			got := grayAt(preview, x, y)
			want := grayAt(settled, x*2, y*2)
			if delta := math.Abs(float64(got) - float64(want)); delta > 1 {
				t.Fatalf("preview (%d,%d) = %d, settled shared-grid sample = %d", x, y, got, want)
			}
		}
	}
}

func TestPlanObliqueResolutionBoundsCustomSlabWorkingSet(t *testing.T) {
	vol := &Volume{ColSpacing: 0.1, RowSpacing: 0.1, SliceSpacing: 0.1}
	plane := Plane{U: Vec3{X: 500}, V: Vec3{Y: 500}}
	const budget = int64(8 << 20)

	plan, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{
		TargetLongEdge:  100000,
		SlabSamples:     512,
		MaxDimension:    100000,
		MaxWorkingBytes: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Bounded {
		t.Fatal("oversized custom slab plan was not marked bounded")
	}
	if plan.Width > MaxObliqueOutputDimension || plan.Height > MaxObliqueOutputDimension {
		t.Fatalf("bounded plan = %dx%d, exceeds hard dimension", plan.Width, plan.Height)
	}
	if plan.EstimatedWorkingBytes > budget {
		t.Fatalf("estimated working set = %d, want <= %d", plan.EstimatedWorkingBytes, budget)
	}
}

func TestPlanObliqueResolutionRejectsUnusableCallerCaps(t *testing.T) {
	vol := &Volume{ColSpacing: 1, RowSpacing: 1, SliceSpacing: 1}
	plane := Plane{U: Vec3{X: 10}, V: Vec3{Y: 10}}
	tests := []struct {
		name    string
		request ObliqueResolutionRequest
	}{
		{name: "dimension one", request: ObliqueResolutionRequest{MaxDimension: 1}},
		{name: "negative dimension", request: ObliqueResolutionRequest{MaxDimension: -1}},
		{name: "negative working bytes", request: ObliqueResolutionRequest{MaxWorkingBytes: -1}},
		{name: "working set below four pixels", request: ObliqueResolutionRequest{MaxWorkingBytes: 35}},
		{name: "slab working set below four pixels", request: ObliqueResolutionRequest{SlabSamples: 2, MaxWorkingBytes: 67}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := vol.PlanObliqueResolution(plane, tt.request); err == nil {
				t.Fatalf("PlanObliqueResolution(%+v) error = nil", tt.request)
			}
		})
	}
}

func TestPlanObliqueResolutionHonorsWorkingCapForThinPlane(t *testing.T) {
	vol := &Volume{ColSpacing: 0.1, RowSpacing: 0.1, SliceSpacing: 0.1}
	plane := Plane{U: Vec3{X: 500}, V: Vec3{Y: 1}}
	const budget = int64(90)
	plan, err := vol.PlanObliqueResolution(plane, ObliqueResolutionRequest{
		TargetLongEdge:  1000,
		MaxWorkingBytes: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.EstimatedWorkingBytes > budget {
		t.Fatalf("thin-plane working set = %d, want <= %d", plan.EstimatedWorkingBytes, budget)
	}
}
