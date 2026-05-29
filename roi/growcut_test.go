package roi

import "testing"

func TestGrowCut2DSeparatesCompetingIntensityRegions(t *testing.T) {
	values := []float64{0, 0, 1, 10, 10}
	foreground := NewRasterMask(5, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(5, 1)
	background.Set(4, 0, true)

	result := GrowCut2D(5, 1, foreground, background, func(x, _ int) (float64, bool) {
		return values[x], true
	})
	for x := 0; x < 5; x++ {
		want := x <= 2
		if result.Get(x, 0) != want {
			t.Fatalf("result[%d] = %v, want %v", x, result.Get(x, 0), want)
		}
	}
	if foreground.Count() != 1 || background.Count() != 1 {
		t.Fatal("GrowCut mutated a seed mask")
	}
}

func TestGrowCut2DInvalidPixelsBlockPropagation(t *testing.T) {
	foreground := NewRasterMask(5, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(5, 1)
	background.Set(4, 0, true)
	result := GrowCut2D(5, 1, foreground, background, func(x, _ int) (float64, bool) {
		return float64(x), x != 2
	})
	if result.Get(2, 0) || result.Get(3, 0) || result.Get(4, 0) {
		t.Fatalf("foreground crossed an invalid barrier: %#v", result)
	}
}

func TestGrowCut3DUsesSixConnectedSliceCompetition(t *testing.T) {
	foregroundSeed := NewRasterMask(3, 1)
	foregroundSeed.Set(0, 0, true)
	backgroundSeed := NewRasterMask(3, 1)
	backgroundSeed.Set(2, 0, true)
	foreground := map[int]*RasterMask{0: foregroundSeed}
	background := map[int]*RasterMask{1: backgroundSeed}
	values := [][]float64{{0, 0, 1}, {0, 9, 10}}

	result := GrowCut3D(3, 1, 2, foreground, background, func(x, _ int, slice int) (float64, bool) {
		return values[slice][x], true
	})
	if result[0] == nil || !result[0].Get(0, 0) || !result[1].Get(0, 0) {
		t.Fatalf("foreground did not propagate across the similar slice neighbour: %#v", result)
	}
	if result[1].Get(2, 0) {
		t.Fatal("background seed was labeled foreground")
	}
}

func TestGrowCutWithoutSeedsReturnsEmptyMasks(t *testing.T) {
	result := GrowCut2D(2, 2, nil, nil, func(x, y int) (float64, bool) { return float64(x + y), true })
	if !result.Empty() {
		t.Fatalf("GrowCut without seeds = %#v, want empty", result)
	}
}
