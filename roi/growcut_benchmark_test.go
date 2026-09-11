package roi

import (
	"context"
	"reflect"
	"testing"
)

var benchmarkGrowCutResult map[int]*RasterMask

func TestGrowCutMetricsPreserveRepresentativeResultsAndTies(t *testing.T) {
	const columns, rows, slices = 32, 24, 3
	foreground, background := growCutBenchmarkSeeds(columns, rows, slices)
	valueAt := func(x, y, slice int) (float64, bool) {
		// Large equal-valued regions exercise stable queue ordering and ties.
		if x < columns/2 {
			return 100, true
		}
		return 200 + float64((y+slice)%2), true
	}
	limits := DefaultGrowCutLimits()

	withoutMetrics, err := growCutLabelsContext(context.Background(), columns, rows, slices,
		foreground, background, true, valueAt, limits, nil)
	if err != nil {
		t.Fatalf("GrowCut without metrics: %v", err)
	}
	metrics := &growCutRunMetrics{}
	withMetrics, err := growCutLabelsContext(context.Background(), columns, rows, slices,
		foreground, background, true, valueAt, limits, metrics)
	if err != nil {
		t.Fatalf("GrowCut with metrics: %v", err)
	}
	legacy := GrowCut3D(columns, rows, slices, foreground, background, valueAt)
	if !reflect.DeepEqual(withMetrics, withoutMetrics) || !reflect.DeepEqual(withMetrics, legacy) {
		t.Fatal("queue instrumentation changed GrowCut labels or tie results")
	}
	wantRun := []MaskRun{{Start: 0, End: columns / 2}}
	for slice := 0; slice < slices; slice++ {
		for y := 0; y < rows; y++ {
			if !reflect.DeepEqual(withMetrics[slice].Runs(y), wantRun) {
				t.Fatalf("result[%d].Runs(%d) = %v, want %v", slice, y, withMetrics[slice].Runs(y), wantRun)
			}
		}
	}
	if metrics.queuePushes == 0 || metrics.queuePops != metrics.queuePushes {
		t.Fatalf("queue metrics = %+v, want one pop per completed push", *metrics)
	}
	if metrics.peakQueueItems <= 0 || metrics.peakQueueItems > metrics.queuePushes {
		t.Fatalf("peak queue metric = %+v, want 0 < peak <= pushes", *metrics)
	}
}

func TestGrowCutStableTieWinner(t *testing.T) {
	foreground := NewRasterMask(5, 1)
	foreground.Set(0, 0, true)
	background := NewRasterMask(5, 1)
	background.Set(4, 0, true)

	result := GrowCut2D(5, 1, foreground, background, func(int, int) (float64, bool) {
		return 0, true
	})
	if got, want := result.Runs(0), []MaskRun{{Start: 0, End: 3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("flat-field tie result = %v, want %v", got, want)
	}
}

func BenchmarkGrowCut2D512x512(b *testing.B) {
	benchmarkGrowCut(b, 1)
}

func BenchmarkGrowCut3D512x512x4(b *testing.B) {
	benchmarkGrowCut(b, 4)
}

func benchmarkGrowCut(b *testing.B, slices int) {
	b.Helper()
	const columns, rows = 512, 512
	foreground, background := growCutBenchmarkSeeds(columns, rows, slices)
	valueAt := func(x, y, slice int) (float64, bool) {
		// Two tissue-like regions with deterministic low-amplitude texture.
		base := 1000
		if x >= columns/2 {
			base = 1200
		}
		return float64(base + (x*13+y*7+slice*29)%31), true
	}
	limits := DefaultGrowCutLimits()
	var metrics growCutRunMetrics
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		metrics = growCutRunMetrics{}
		result, err := growCutLabelsContext(context.Background(), columns, rows, slices,
			foreground, background, slices > 1, valueAt, limits, &metrics)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkGrowCutResult = result
	}
	b.ReportMetric(float64(metrics.queuePushes), "pushes/op")
	b.ReportMetric(float64(metrics.stalePops), "stale/op")
	b.ReportMetric(float64(metrics.peakQueueItems), "peak_queue")
}

func growCutBenchmarkSeeds(columns, rows, slices int) (map[int]*RasterMask, map[int]*RasterMask) {
	slice := slices / 2
	foreground := NewRasterMask(columns, rows)
	foreground.Set(columns/4, rows/2, true)
	background := NewRasterMask(columns, rows)
	background.Set(columns*3/4, rows/2, true)
	return map[int]*RasterMask{slice: foreground}, map[int]*RasterMask{slice: background}
}
