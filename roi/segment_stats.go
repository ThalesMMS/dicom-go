package roi

import (
	"math"

	"github.com/ThalesMMS/dicom-go/render"
)

// VolumeStats are volumetric ROI statistics computed over a Segmentation3D using
// patient-space spacing from the segmentation's VolumeGeometry. It includes the
// central moments needed to derive skewness and kurtosis, so callers do not have
// to re-walk the voxels.
type VolumeStats struct {
	VoxelCount int
	AreaMM2    float64 // total in-plane area of the labeled voxels
	VolumeMM3  float64 // total volume using per-slice patient-space thickness

	Min  float64
	Max  float64
	Mean float64

	Variance float64
	StdDev   float64
	Skewness float64 // Fisher skewness (0 for symmetric distributions)
	Kurtosis float64 // excess kurtosis (0 for a normal distribution)

	Histogram         []int
	HistogramMin      float64
	HistogramBinWidth float64
}

// Statistics computes volumetric statistics over the segmentation. valueAt reads
// a voxel's rescaled value (e.g. MPRVolume.ValueAt); bins > 0 builds a value
// histogram and may read each voxel twice so the exact bin range can be known
// without buffering every value. Spacing comes from the segmentation's geometry.
func (s *Segmentation3D) Statistics(valueAt func(x, y, slice int) (float64, bool), bins int) VolumeStats {
	var stats VolumeStats
	if s == nil || valueAt == nil {
		return stats
	}

	slices := s.Slices()
	min, max := math.Inf(1), math.Inf(-1)
	var moments centralMoments
	for _, z := range slices {
		mask := s.masks[z]
		colSp, rowSp := sliceInPlaneSpacing(s.Geometry, z)
		thickness := sliceThickness(s.Geometry, z)
		areaPerVoxel := colSp * rowSp
		mask.ForEachPixel(func(x, y int) {
			v, ok := valueAt(x, y, z)
			if !ok {
				return
			}
			moments.Add(v)
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
			stats.AreaMM2 += areaPerVoxel
			stats.VolumeMM3 += areaPerVoxel * thickness
		})
	}
	if moments.Count == 0 {
		return VolumeStats{}
	}

	stats.VoxelCount = moments.Count
	n := float64(moments.Count)
	stats.Min, stats.Max = min, max
	stats.Mean = moments.Mean
	stats.Variance = moments.M2 / n
	if stats.Variance < 0 {
		stats.Variance = 0
	}
	stats.StdDev = math.Sqrt(stats.Variance)
	if stats.StdDev > 0 {
		stats.Skewness = (moments.M3 / n) / math.Pow(stats.StdDev, 3)
		stats.Kurtosis = (moments.M4/n)/(stats.Variance*stats.Variance) - 3
	}

	if bins > 0 && max > min {
		stats.Histogram = make([]int, bins)
		stats.HistogramMin = min
		stats.HistogramBinWidth = (max - min) / float64(bins)
		for _, z := range slices {
			s.masks[z].ForEachPixel(func(x, y int) {
				v, ok := valueAt(x, y, z)
				if !ok {
					return
				}
				b := int((v - min) / stats.HistogramBinWidth)
				if b >= bins {
					b = bins - 1
				}
				if b < 0 {
					b = 0
				}
				stats.Histogram[b]++
			})
		}
	}
	return stats
}

// centralMoments maintains the first four central moments using the one-pass
// recurrence described by Pébay. It is the higher-moment form of Welford's
// stable online variance algorithm.
type centralMoments struct {
	Count int
	Mean  float64
	M2    float64
	M3    float64
	M4    float64
}

func (m *centralMoments) Add(value float64) {
	previousCount := float64(m.Count)
	m.Count++
	count := float64(m.Count)
	delta := value - m.Mean
	deltaOverCount := delta / count
	deltaOverCountSquared := deltaOverCount * deltaOverCount
	term := delta * deltaOverCount * previousCount
	m.M4 += term*deltaOverCountSquared*(count*count-3*count+3) +
		6*deltaOverCountSquared*m.M2 - 4*deltaOverCount*m.M3
	m.M3 += term*deltaOverCount*(count-2) - 3*deltaOverCount*m.M2
	m.M2 += term
	m.Mean += deltaOverCount
}

// sliceInPlaneSpacing returns the (column, row) mm spacing for a slice index,
// defaulting to 1 mm when geometry is unavailable.
func sliceInPlaneSpacing(g render.VolumeGeometry, slice int) (colSp, rowSp float64) {
	if slice >= 0 && slice < len(g.Slices) {
		colSp, rowSp = g.Slices[slice].ColSpacing, g.Slices[slice].RowSpacing
	}
	if colSp <= 0 {
		colSp = 1
	}
	if rowSp <= 0 {
		rowSp = 1
	}
	return colSp, rowSp
}

// sliceThickness returns the through-plane thickness (mm) attributable to a
// slice, using the real positions: the half-distance to each neighbor for
// interior slices and the single adjacent gap at the ends.
func sliceThickness(g render.VolumeGeometry, slice int) float64 {
	n := len(g.Positions)
	if n < 2 {
		if g.MeanSpacing > 0 {
			return g.MeanSpacing
		}
		return 1
	}
	if slice < 0 {
		slice = 0
	}
	if slice >= n {
		slice = n - 1
	}
	switch {
	case slice == 0:
		return g.Positions[1] - g.Positions[0]
	case slice == n-1:
		return g.Positions[n-1] - g.Positions[n-2]
	default:
		return (g.Positions[slice+1] - g.Positions[slice-1]) / 2
	}
}
