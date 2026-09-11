package roi

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestGrowCut2DContextMatchesLegacyOnValidInput(t *testing.T) {
	foreground := NewRasterMask(5, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(5, 1)
	background.Set(4, 0, true)
	valueAt := func(x, _ int) (float64, bool) {
		return []float64{0, 0, 1, 10, 10}[x], true
	}
	legacy := GrowCut2D(5, 1, foreground, background, valueAt)
	checked, err := GrowCut2DContext(context.Background(), 5, 1, foreground, background, valueAt, GrowCutLimits{
		MaxVoxels: 100, MaxBytes: 1 << 20, MaxQueueItems: 100,
	})
	if err != nil {
		t.Fatalf("GrowCut2DContext returned error: %v", err)
	}
	for x := 0; x < 5; x++ {
		if checked.Get(x, 0) != legacy.Get(x, 0) {
			t.Fatalf("checked[%d] = %v, legacy = %v", x, checked.Get(x, 0), legacy.Get(x, 0))
		}
	}
}

func TestGrowCutContextRejectsDimensionsAndResourcesBeforeAllocation(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	foreground := NewRasterMask(1, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(1, 1)
	background.Set(0, 0, true)
	valueAt := func(int, int) (float64, bool) { return 1, true }
	tests := []struct {
		name   string
		cols   int
		rows   int
		limits GrowCutLimits
		want   error
	}{
		{name: "zero dimensions", cols: 0, rows: 1, want: ErrGrowCutInvalidDimensions},
		{name: "multiplication overflow", cols: maxInt, rows: 2, want: ErrGrowCutDimensionOverflow},
		{name: "voxel limit", cols: 3, rows: 2, limits: GrowCutLimits{MaxVoxels: 4, MaxBytes: 1 << 20, MaxQueueItems: 10}, want: ErrGrowCutResourceLimit},
		{name: "byte limit", cols: 1, rows: 1, limits: GrowCutLimits{MaxVoxels: 10, MaxBytes: 1, MaxQueueItems: 10}, want: ErrGrowCutResourceLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GrowCut2DContext(context.Background(), tt.cols, tt.rows, foreground, background, valueAt, tt.limits)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tt.want)
			}
		})
	}
}

func TestGrowCutContextRejectsInvalidSeedsAndNonFiniteValues(t *testing.T) {
	foreground := NewRasterMask(2, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(2, 1)
	background.Set(1, 0, true)
	wrongDimensions := NewRasterMask(1, 1)
	wrongDimensions.Set(0, 0, true)
	valueAt := func(x, _ int) (float64, bool) { return float64(x), true }

	if _, err := GrowCut2DContext(context.Background(), 2, 1, foreground, wrongDimensions, valueAt, GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 10}); !errors.Is(err, ErrGrowCutInvalidSeeds) {
		t.Fatalf("wrong seed dimensions error = %v", err)
	}
	if _, err := GrowCut2DContext(context.Background(), 2, 1, foreground, nil, valueAt, GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 10}); !errors.Is(err, ErrGrowCutMissingSeeds) {
		t.Fatalf("missing background error = %v", err)
	}
	if _, err := GrowCut2DContext(context.Background(), 2, 1, foreground, background, func(int, int) (float64, bool) { return math.NaN(), true }, GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 10}); !errors.Is(err, ErrGrowCutNonFiniteValue) {
		t.Fatalf("non-finite value error = %v", err)
	}
	if _, err := GrowCut2DContext(context.Background(), 2, 1, foreground, background, nil, GrowCutLimits{}); !errors.Is(err, ErrGrowCutInvalidValueSource) {
		t.Fatalf("nil value source error = %v", err)
	}
}

func TestGrowCutContextHonorsCancellationAndQueueLimit(t *testing.T) {
	foreground := NewRasterMask(5, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(5, 1)
	background.Set(4, 0, true)
	limits := GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 100}
	ctx, cancel := context.WithCancel(context.Background())
	called := false
	_, err := GrowCut2DContext(ctx, 5, 1, foreground, background, func(x, _ int) (float64, bool) {
		if !called {
			called = true
			cancel()
		}
		return float64(x), true
	}, limits)
	if !errors.Is(err, ErrGrowCutCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	_, err = GrowCut2DContext(context.Background(), 5, 1, foreground, background, func(x, _ int) (float64, bool) {
		return float64(x), true
	}, GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 1})
	if !errors.Is(err, ErrGrowCutResourceLimit) {
		t.Fatalf("queue limit error = %v", err)
	}
}

func TestGrowCut3DContextValidatesAndPropagates(t *testing.T) {
	foregroundSeed := NewRasterMask(3, 1)
	foregroundSeed.Set(0, 0, true)
	backgroundSeed := NewRasterMask(3, 1)
	backgroundSeed.Set(2, 0, true)
	result, err := GrowCut3DContext(context.Background(), 3, 1, 2,
		map[int]*RasterMask{0: foregroundSeed}, map[int]*RasterMask{1: backgroundSeed},
		func(x, _ int, slice int) (float64, bool) {
			values := [][]float64{{0, 0, 1}, {0, 9, 10}}
			return values[slice][x], true
		}, GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 100})
	if err != nil {
		t.Fatalf("GrowCut3DContext returned error: %v", err)
	}
	if result[0] == nil || !result[0].Get(0, 0) || result[1] == nil || !result[1].Get(0, 0) {
		t.Fatalf("foreground did not propagate: %#v", result)
	}
	if result[1].Get(2, 0) {
		t.Fatal("background seed was labeled foreground")
	}
}

func TestGrowCut3DContextMatchesLegacyOnValidInput(t *testing.T) {
	foregroundSeed := NewRasterMask(3, 1)
	foregroundSeed.Set(0, 0, true)
	backgroundSeed := NewRasterMask(3, 1)
	backgroundSeed.Set(2, 0, true)
	foreground := map[int]*RasterMask{0: foregroundSeed}
	background := map[int]*RasterMask{1: backgroundSeed}
	valueAt := func(x, _ int, slice int) (float64, bool) {
		values := [][]float64{{0, 0, 1}, {0, 9, 10}}
		return values[slice][x], true
	}
	legacy := GrowCut3D(3, 1, 2, foreground, background, valueAt)
	checked, err := GrowCut3DContext(context.Background(), 3, 1, 2, foreground, background, valueAt, GrowCutLimits{MaxVoxels: 10, MaxBytes: 1 << 20, MaxQueueItems: 100})
	if err != nil {
		t.Fatalf("GrowCut3DContext returned error: %v", err)
	}
	for slice := 0; slice < 2; slice++ {
		for x := 0; x < 3; x++ {
			legacyOn := legacy[slice] != nil && legacy[slice].Get(x, 0)
			checkedOn := checked[slice] != nil && checked[slice].Get(x, 0)
			if checkedOn != legacyOn {
				t.Fatalf("checked[%d,%d] = %v, legacy = %v", slice, x, checkedOn, legacyOn)
			}
		}
	}
}

func TestLegacyGrowCutRejectsAdversarialDimensionsWithoutPanicking(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if result := GrowCut2D(maxInt, 2, nil, nil, func(int, int) (float64, bool) { return 0, true }); result == nil || !result.Empty() {
		t.Fatalf("legacy 2D result = %#v, want a safe empty mask", result)
	}
	if result := GrowCut3D(maxInt, 2, 2, nil, nil, func(int, int, int) (float64, bool) { return 0, true }); len(result) != 0 {
		t.Fatalf("legacy 3D result = %#v, want an empty result", result)
	}
}
