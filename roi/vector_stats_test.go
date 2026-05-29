package roi

import (
	"image"
	"math"
	"reflect"
	"testing"
)

func TestVectorROIStatisticsIncludesDistributionAndHistogram(t *testing.T) {
	region := VectorROI{Shape: ROIRectangle, Points: []image.Point{{1, 0}, {2, 1}}}
	values := [][]float64{
		{0, 10, 20, 30},
		{40, 50, 60, 70},
		{80, 90, 100, 110},
	}
	stats := region.Statistics(4, 3, func(x, y int) (float64, bool) {
		return values[y][x], true
	}, 2)

	if stats.Count != 4 || stats.Sum != 140 || stats.Min != 10 || stats.Max != 60 || stats.Mean != 35 || stats.Median != 35 {
		t.Fatalf("Statistics() basics = %#v, want count=4 sum=140 range=10..60 mean/median=35", stats)
	}
	if math.Abs(stats.Variance-425) > 1e-9 || math.Abs(stats.StdDev-math.Sqrt(425)) > 1e-9 {
		t.Fatalf("Statistics() variance/stddev = %v/%v, want 425/sqrt(425)", stats.Variance, stats.StdDev)
	}
	if math.Abs(stats.Skewness) > 1e-9 || math.Abs(stats.Kurtosis+1.7785467128027683) > 1e-9 {
		t.Fatalf("Statistics() skewness/kurtosis = %v/%v", stats.Skewness, stats.Kurtosis)
	}
	if !reflect.DeepEqual(stats.Histogram, []int{2, 2}) || stats.HistogramMin != 10 || stats.HistogramBinWidth != 25 {
		t.Fatalf("Statistics() histogram = %v min=%v width=%v, want [2 2], 10, 25", stats.Histogram, stats.HistogramMin, stats.HistogramBinWidth)
	}
}

func TestStats2DWithHistogramHandlesConstantAndInvalidValues(t *testing.T) {
	mask := NewRasterMask(3, 1)
	mask.SetRun(0, 0, 3)
	stats := Stats2DWithHistogram(mask, func(x, _ int) (float64, bool) {
		if x == 2 {
			return math.NaN(), true
		}
		return 7, true
	}, 4)

	if stats.Count != 2 || stats.Median != 7 || stats.HistogramBinWidth != 1 {
		t.Fatalf("constant Statistics = %#v, want two valid values at 7", stats)
	}
	if !reflect.DeepEqual(stats.Histogram, []int{2, 0, 0, 0}) {
		t.Fatalf("constant histogram = %v, want first bin to contain both values", stats.Histogram)
	}
}

func TestVectorROIStatisticsWithHistogramBoundsUsesFullDisplayRangeAndClamps(t *testing.T) {
	region := VectorROI{Shape: ROIRectangle, Points: []image.Point{{0, 0}, {3, 0}}}
	values := []float64{-20, 0, 50, 120}
	stats := region.StatisticsWithHistogramBounds(4, 1, func(x, _ int) (float64, bool) {
		return values[x], true
	}, 4, 0, 100)

	if stats.Min != -20 || stats.Max != 120 || stats.HistogramMin != 0 || stats.HistogramBinWidth != 25 {
		t.Fatalf("bounded Statistics = %#v, want actual range -20..120 and histogram range 0..100", stats)
	}
	if !reflect.DeepEqual(stats.Histogram, []int{2, 0, 1, 1}) {
		t.Fatalf("bounded histogram = %v, want low/high outliers retained in edge bins", stats.Histogram)
	}
}
