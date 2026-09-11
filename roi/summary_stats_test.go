package roi

import (
	"math"
	"testing"
)

var benchmarkSummaryStats SummaryStats

func TestStats2DSummaryMatchesFullStats(t *testing.T) {
	mask := NewRasterMask(4, 2)
	mask.SetRun(0, 0, 4)
	mask.SetRun(1, 0, 4)
	values := []float64{1e12 + 0.25, -7.5, 3, 99, 4.5, 4.5, 18, 1e12 + 0.75}
	valueAt := func(x, y int) (float64, bool) { return values[y*4+x], true }

	full := Stats2D(mask, valueAt)
	summary := Stats2DSummary(mask, valueAt)
	if summary.Count != full.Count {
		t.Fatalf("Count = %d, want %d", summary.Count, full.Count)
	}
	for name, pair := range map[string][2]float64{
		"Sum": {summary.Sum, full.Sum}, "Min": {summary.Min, full.Min}, "Max": {summary.Max, full.Max},
		"Mean": {summary.Mean, full.Mean}, "Variance": {summary.Variance, full.Variance}, "StdDev": {summary.StdDev, full.StdDev},
	} {
		tolerance := 1e-12 * math.Max(1, math.Abs(pair[1]))
		if math.IsNaN(pair[0]) || math.IsInf(pair[0], 0) || math.Abs(pair[0]-pair[1]) > tolerance {
			t.Fatalf("%s = %.17g, want %.17g within %.3g", name, pair[0], pair[1], tolerance)
		}
	}
}

func TestStats2DSummarySkipsUnavailableAndNonFiniteValues(t *testing.T) {
	mask := NewRasterMask(6, 1)
	mask.SetRun(0, 0, 6)
	values := []float64{-2, math.NaN(), math.Inf(1), math.Inf(-1), 6, 100}
	calls := 0
	summary := Stats2DSummary(mask, func(x, _ int) (float64, bool) {
		calls++
		return values[x], x != 5
	})
	if calls != 6 {
		t.Fatalf("value callback calls = %d, want one per marked pixel", calls)
	}
	if summary.Count != 2 || summary.Sum != 4 || summary.Min != -2 || summary.Max != 6 || summary.Mean != 2 || summary.Variance != 16 || summary.StdDev != 4 {
		t.Fatalf("Stats2DSummary() = %#v, want finite available values [-2, 6]", summary)
	}
}

func TestStats2DSummaryHandlesNilEmptyAndConstantInputs(t *testing.T) {
	if got := Stats2DSummary(nil, func(int, int) (float64, bool) { return 1, true }); got != (SummaryStats{}) {
		t.Fatalf("nil mask = %#v, want zero", got)
	}
	mask := NewRasterMask(3, 1)
	if got := Stats2DSummary(mask, nil); got != (SummaryStats{}) {
		t.Fatalf("nil callback = %#v, want zero", got)
	}
	mask.SetRun(0, 0, 3)
	constant := Stats2DSummary(mask, func(int, int) (float64, bool) { return 7, true })
	if constant.Count != 3 || constant.Sum != 21 || constant.Min != 7 || constant.Max != 7 || constant.Mean != 7 || constant.Variance != 0 || constant.StdDev != 0 {
		t.Fatalf("constant summary = %#v", constant)
	}
	invalid := Stats2DSummary(mask, func(int, int) (float64, bool) { return math.NaN(), true })
	if invalid != (SummaryStats{}) {
		t.Fatalf("all-invalid summary = %#v, want zero", invalid)
	}
}

func TestStats2DFullPathRetainsExactMedian(t *testing.T) {
	mask := NewRasterMask(4, 1)
	mask.SetRun(0, 0, 4)
	values := []float64{100, 1, 9, 3}
	stats := Stats2D(mask, func(x, _ int) (float64, bool) { return values[x], true })
	if stats.Median != 6 {
		t.Fatalf("Median = %v, want exact even-sample median 6", stats.Median)
	}
}

func BenchmarkStats2DSummary512x512(b *testing.B) {
	mask := fullBenchmarkMask(512, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkSummaryStats = Stats2DSummary(mask, benchmarkValueAt)
	}
}

func BenchmarkStats2DFull512x512(b *testing.B) {
	mask := fullBenchmarkMask(512, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkSummaryStats = summaryFromFullStats(Stats2D(mask, benchmarkValueAt))
	}
}

func fullBenchmarkMask(columns, rows int) *RasterMask {
	mask := NewRasterMask(columns, rows)
	for y := 0; y < rows; y++ {
		mask.SetRun(y, 0, columns)
	}
	return mask
}

func benchmarkValueAt(x, y int) (float64, bool) {
	return float64((x*31+y*17)%4096) - 1024, true
}

func summaryFromFullStats(stats Stats) SummaryStats {
	return SummaryStats{Count: stats.Count, Sum: stats.Sum, Min: stats.Min, Max: stats.Max, Mean: stats.Mean, Variance: stats.Variance, StdDev: stats.StdDev}
}
