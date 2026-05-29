package roi

import (
	"math"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/render"
)

func TestOnlineStatisticsMatchesBufferedReference(t *testing.T) {
	seg := statisticsBenchmarkSegmentation(31, 19, 5)
	valueAt := func(x, y, slice int) (float64, bool) {
		if (x+2*y+3*slice)%11 == 0 {
			return 0, false
		}
		return 1_000_000 + float64((x*31+y*17+slice*13)%101)/7, true
	}

	for _, bins := range []int{0, 1, 7, 256} {
		got := seg.Statistics(valueAt, bins)
		want := bufferedStatisticsReference(seg, valueAt, bins)
		assertVolumeStatsClose(t, got, want)
	}
}

func TestOnlineStatisticsUsesTwoPassHistogram(t *testing.T) {
	seg := statisticsBenchmarkSegmentation(7, 5, 3)
	calls := 0
	stats := seg.Statistics(func(x, y, slice int) (float64, bool) {
		calls++
		return float64(x + y + slice), true
	}, 8)

	if calls != stats.VoxelCount*2 {
		t.Fatalf("valueAt calls = %d, want %d for two-pass histogram", calls, stats.VoxelCount*2)
	}
	histogramCount := 0
	for _, count := range stats.Histogram {
		histogramCount += count
	}
	if histogramCount != stats.VoxelCount {
		t.Fatalf("histogram count = %d, want %d", histogramCount, stats.VoxelCount)
	}
}

func TestOnlineStatisticsIsStableForLargeOffsets(t *testing.T) {
	seg := statisticsBenchmarkSegmentation(257, 1, 1)
	const offset = 1_000_000_000_000.0
	valueAt := func(x, _, _ int) (float64, bool) {
		return offset + float64(x-128)/16, true
	}

	got := seg.Statistics(valueAt, 0)
	wantVariance := 0.0
	for x := 0; x < 257; x++ {
		delta := float64(x-128) / 16
		wantVariance += delta * delta
	}
	wantVariance /= 257

	if math.Abs(got.Mean-offset) > 1e-9 {
		t.Fatalf("mean = %.12g, want %.12g", got.Mean, offset)
	}
	if math.Abs(got.Variance-wantVariance) > 1e-10 {
		t.Fatalf("variance = %.12g, want %.12g", got.Variance, wantVariance)
	}
	if math.Abs(got.Skewness) > 1e-12 {
		t.Fatalf("skewness = %g, want 0", got.Skewness)
	}
}

func BenchmarkStatisticsOnlineNoHistogram512x512x4(b *testing.B) {
	benchmarkStatistics(b, false, false)
}

func BenchmarkStatisticsBufferedReferenceNoHistogram512x512x4(b *testing.B) {
	benchmarkStatistics(b, true, false)
}

func BenchmarkStatisticsOnlineHistogram512x512x4(b *testing.B) {
	benchmarkStatistics(b, false, true)
}

func BenchmarkStatisticsBufferedReferenceHistogram512x512x4(b *testing.B) {
	benchmarkStatistics(b, true, true)
}

var benchmarkVolumeStats VolumeStats

func benchmarkStatistics(b *testing.B, buffered, histogram bool) {
	b.Helper()
	seg := statisticsBenchmarkSegmentation(512, 512, 4)
	valueAt := func(x, y, slice int) (float64, bool) {
		return float64((x*31 + y*17 + slice*13) % 4096), true
	}
	bins := 0
	if histogram {
		bins = 256
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if buffered {
			benchmarkVolumeStats = bufferedStatisticsReference(seg, valueAt, bins)
		} else {
			benchmarkVolumeStats = seg.Statistics(valueAt, bins)
		}
	}
}

func statisticsBenchmarkSegmentation(columns, rows, slices int) *Segmentation3D {
	geometry := render.VolumeGeometry{
		Slices:    make([]render.SliceGeometry, slices),
		Positions: make([]float64, slices),
	}
	for slice := 0; slice < slices; slice++ {
		geometry.Slices[slice] = render.SliceGeometry{RowSpacing: 0.7, ColSpacing: 0.8}
		geometry.Positions[slice] = float64(slice) * 2.5
	}
	seg := NewSegmentation3D(geometry, columns, rows)
	for slice := 0; slice < slices; slice++ {
		mask := NewRasterMask(columns, rows)
		for y := 0; y < rows; y++ {
			mask.SetRun(y, 0, columns)
		}
		seg.SetMask(slice, mask)
	}
	return seg
}

func bufferedStatisticsReference(s *Segmentation3D, valueAt func(x, y, slice int) (float64, bool), bins int) VolumeStats {
	var stats VolumeStats
	if s == nil || valueAt == nil {
		return stats
	}
	values := make([]float64, 0)
	minValue, maxValue := math.Inf(1), math.Inf(-1)
	var sum float64
	for _, z := range s.Slices() {
		mask := s.masks[z]
		colSpacing, rowSpacing := sliceInPlaneSpacing(s.Geometry, z)
		thickness := sliceThickness(s.Geometry, z)
		areaPerVoxel := colSpacing * rowSpacing
		mask.ForEachPixel(func(x, y int) {
			value, ok := valueAt(x, y, z)
			if !ok {
				return
			}
			values = append(values, value)
			sum += value
			if value < minValue {
				minValue = value
			}
			if value > maxValue {
				maxValue = value
			}
			stats.AreaMM2 += areaPerVoxel
			stats.VolumeMM3 += areaPerVoxel * thickness
			stats.VoxelCount++
		})
	}
	if stats.VoxelCount == 0 {
		return VolumeStats{}
	}
	n := float64(stats.VoxelCount)
	stats.Min, stats.Max = minValue, maxValue
	stats.Mean = sum / n
	var m2, m3, m4 float64
	for _, value := range values {
		delta := value - stats.Mean
		deltaSquared := delta * delta
		m2 += deltaSquared
		m3 += deltaSquared * delta
		m4 += deltaSquared * deltaSquared
	}
	stats.Variance = m2 / n
	stats.StdDev = math.Sqrt(stats.Variance)
	if stats.StdDev > 0 {
		stats.Skewness = (m3 / n) / math.Pow(stats.StdDev, 3)
		stats.Kurtosis = (m4/n)/(stats.Variance*stats.Variance) - 3
	}
	if bins > 0 && maxValue > minValue {
		stats.Histogram = make([]int, bins)
		stats.HistogramMin = minValue
		stats.HistogramBinWidth = (maxValue - minValue) / float64(bins)
		for _, value := range values {
			bin := int((value - minValue) / stats.HistogramBinWidth)
			if bin >= bins {
				bin = bins - 1
			}
			if bin < 0 {
				bin = 0
			}
			stats.Histogram[bin]++
		}
	}
	return stats
}

func assertVolumeStatsClose(t *testing.T, got, want VolumeStats) {
	t.Helper()
	if got.VoxelCount != want.VoxelCount || !reflect.DeepEqual(got.Histogram, want.Histogram) {
		t.Fatalf("counts differ: got voxels=%d histogram=%v, want voxels=%d histogram=%v", got.VoxelCount, got.Histogram, want.VoxelCount, want.Histogram)
	}
	gotValues := []float64{got.AreaMM2, got.VolumeMM3, got.Min, got.Max, got.Mean, got.Variance, got.StdDev, got.Skewness, got.Kurtosis, got.HistogramMin, got.HistogramBinWidth}
	wantValues := []float64{want.AreaMM2, want.VolumeMM3, want.Min, want.Max, want.Mean, want.Variance, want.StdDev, want.Skewness, want.Kurtosis, want.HistogramMin, want.HistogramBinWidth}
	for index := range gotValues {
		scale := math.Max(1, math.Abs(wantValues[index]))
		if math.Abs(gotValues[index]-wantValues[index]) > 1e-9*scale {
			t.Fatalf("field %d = %.15g, want %.15g", index, gotValues[index], wantValues[index])
		}
	}
}
