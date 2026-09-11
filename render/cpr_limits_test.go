package render

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestNewCPRPathCheckedRejectsInvalidDegenerateAndExcessiveInput(t *testing.T) {
	tests := []struct {
		name   string
		points []Vec3
		want   error
	}{
		{name: "nan", points: []Vec3{{}, {X: math.NaN()}}, want: ErrCPRInput},
		{name: "infinity", points: []Vec3{{}, {X: math.Inf(1)}}, want: ErrCPRInput},
		{name: "degenerate", points: []Vec3{{X: 1}, {X: 1}}, want: ErrCPRInput},
		{name: "overflowing segment", points: []Vec3{{X: -math.MaxFloat64}, {X: math.MaxFloat64}}, want: ErrCPRInput},
		{name: "excessive length", points: []Vec3{{}, {X: MaxCPRPathLengthMM + 1}}, want: ErrCPRLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := NewCPRPathChecked(test.points)
			if path != nil || !errors.Is(err, test.want) {
				t.Fatalf("path/error = %#v/%v, want nil/%v", path, err, test.want)
			}
			if legacy := NewCPRPath(test.points); legacy != nil {
				t.Fatalf("compatibility constructor returned %#v for rejected input", legacy)
			}
		})
	}

	tooMany := make([]Vec3, MaxCPRControlPoints+1)
	for index := range tooMany {
		tooMany[index].X = float64(index)
	}
	_, err := NewCPRPathChecked(tooMany)
	var limit *CPRLimitError
	if !errors.Is(err, ErrCPRLimit) || !errors.As(err, &limit) || limit.Resource != "control_points" {
		t.Fatalf("control-point error = %#v/%v", limit, err)
	}
}

func TestCPRPathResampleCheckedBoundsSampleCountBeforeAllocation(t *testing.T) {
	path, err := NewCPRPathChecked([]Vec3{{}, {X: 10}})
	if err != nil {
		t.Fatalf("NewCPRPathChecked: %v", err)
	}
	if _, err := path.ResampleChecked(math.NaN()); !errors.Is(err, ErrCPRInput) {
		t.Fatalf("NaN spacing error = %v, want ErrCPRInput", err)
	}
	_, err = path.ResampleChecked(1e-12)
	var limit *CPRLimitError
	if !errors.Is(err, ErrCPRLimit) || !errors.As(err, &limit) || limit.Resource != "path_samples" {
		t.Fatalf("sample limit error = %#v/%v", limit, err)
	}
	if samples := path.Resample(1e-12); samples != nil {
		t.Fatalf("compatibility resample allocated %d samples for excessive request", len(samples))
	}
	for spacing, want := range map[float64]int{2: 6, 3: 5} {
		count, err := path.SampleCountChecked(spacing)
		if err != nil || count != want {
			t.Fatalf("SampleCountChecked(%v) = %d, %v; want %d, nil", spacing, count, err, want)
		}
		if samples, err := path.ResampleChecked(spacing); err != nil || len(samples) != want {
			t.Fatalf("ResampleChecked(%v) count/error = %d/%v, want %d/nil", spacing, len(samples), err, want)
		}
	}
}

func TestRenderCPRRejectsInvalidAndBoundedRequestsBeforeOutputAllocation(t *testing.T) {
	volume, err := BuildVolume(bentTubeStack())
	if err != nil {
		t.Fatalf("BuildVolume: %v", err)
	}
	path, err := NewCPRPathChecked([]Vec3{
		volume.VoxelToPatient(Vec3{X: 3, Y: 10, Z: 4}),
		volume.VoxelToPatient(Vec3{X: 25, Y: 22, Z: 4}),
	})
	if err != nil {
		t.Fatalf("NewCPRPathChecked: %v", err)
	}
	base := CPRRequest{Mode: CPRStraightened, Volume: volume, Path: path, Width: 64, ArcSpacing: 1, CrossSpacing: 1}

	invalid := []CPRRequest{
		func() CPRRequest { request := base; request.ArcSpacing = math.NaN(); return request }(),
		func() CPRRequest { request := base; request.CrossSpacing = math.Inf(1); return request }(),
		func() CPRRequest { request := base; request.RotationDegrees = math.NaN(); return request }(),
		func() CPRRequest { request := base; request.Mode = CPRMode(99); return request }(),
	}
	for index, request := range invalid {
		if _, err := RenderCPR(context.Background(), request); !errors.Is(err, ErrCPRInput) {
			t.Fatalf("invalid request %d error = %v, want ErrCPRInput", index, err)
		}
	}

	tooWide := base
	tooWide.Width = MaxCPROutputDimension + 1
	if _, err := RenderCPR(context.Background(), tooWide); !errors.Is(err, ErrCPRLimit) {
		t.Fatalf("wide request error = %v, want ErrCPRLimit", err)
	}
	longPath, err := NewCPRPathChecked([]Vec3{{}, {X: MaxCPROutputDimension + 100}})
	if err != nil {
		t.Fatal(err)
	}
	longOutput := base
	longOutput.Path = longPath
	longOutput.Width = 1
	if _, err := RenderCPR(context.Background(), longOutput); !errors.Is(err, ErrCPRLimit) {
		t.Fatalf("long output error = %v, want preflight ErrCPRLimit", err)
	}
	hugeSlab := base
	hugeSlab.SlabMode = SlabMIP
	hugeSlab.Thickness = MaxCPRSlabSamples + 1
	if _, err := RenderCPR(context.Background(), hugeSlab); !errors.Is(err, ErrCPRLimit) {
		t.Fatalf("slab request error = %v, want ErrCPRLimit", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderCPR(ctx, tooWide); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled request error = %v, want context.Canceled", err)
	}
}

func TestFindCPRAssistedPathBoundsOptionsHeapExpansionAndOutput(t *testing.T) {
	volume, err := BuildVolume(bentTubeStack())
	if err != nil {
		t.Fatalf("BuildVolume: %v", err)
	}
	start := volume.VoxelToPatient(Vec3{X: 3, Y: 10, Z: 4})
	end := volume.VoxelToPatient(Vec3{X: 25, Y: 22, Z: 4})

	_, err = FindCPRAssistedPath(context.Background(), volume, []Vec3{start, end}, CPRPathAssistantOptions{GridSpacingMM: math.NaN()})
	if !errors.Is(err, ErrCPRPathAssistantInput) {
		t.Fatalf("invalid option error = %v, want ErrCPRPathAssistantInput", err)
	}
	_, err = FindCPRAssistedPath(context.Background(), volume, []Vec3{start, end}, CPRPathAssistantOptions{MaxGridVoxels: MaxCPRAssistantGridVoxels + 1})
	assertCPRAssistantLimit(t, err, "grid_voxels")

	_, err = FindCPRAssistedPath(context.Background(), volume, []Vec3{start, end}, CPRPathAssistantOptions{
		GridSpacingMM: 1, MarginMM: 3, MaxExpandedNodes: 1,
	})
	assertCPRAssistantLimit(t, err, "expanded_nodes")

	_, err = FindCPRAssistedPath(context.Background(), volume, []Vec3{start, end}, CPRPathAssistantOptions{
		GridSpacingMM: 1, MarginMM: 3, MaxQueueEntries: 1,
	})
	assertCPRAssistantLimit(t, err, "queue_entries")

	_, err = FindCPRAssistedPath(context.Background(), volume, []Vec3{start, end}, CPRPathAssistantOptions{
		GridSpacingMM: 1, MarginMM: 3, OutputSpacingMM: 0.01, MaxOutputPoints: 2,
	})
	assertCPRAssistantLimit(t, err, "output_points")
}

func assertCPRAssistantLimit(t *testing.T, err error, resource string) {
	t.Helper()
	if !errors.Is(err, ErrCPRPathAssistantMemory) {
		t.Fatalf("error = %v, want ErrCPRPathAssistantMemory", err)
	}
	var limit *CPRPathAssistantLimitError
	if !errors.As(err, &limit) || limit.Resource != resource {
		t.Fatalf("limit error = %#v/%v, want resource %q", limit, err, resource)
	}
}
