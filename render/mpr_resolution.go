package render

import (
	"fmt"
	"math"
)

const (
	// MaxObliqueOutputDimension is the hard per-axis safety ceiling for an MPR
	// reslice. Callers may request a smaller ceiling, but never a larger one.
	MaxObliqueOutputDimension = 2048

	// DefaultObliqueWorkingSetBytes bounds the output plus the repeated slab
	// samples represented by one reslice request. The renderer streams samples,
	// but accounting for them here also bounds CPU work for thick slabs.
	DefaultObliqueWorkingSetBytes int64 = 64 << 20

	// MaxObliqueSlabSamples is the planner's hard work-accounting ceiling.
	// Product UIs should normally choose a lower interaction-specific limit.
	MaxObliqueSlabSamples = 4096
)

// ObliqueResolutionRequest describes the desired sampling density for an
// arbitrary patient-space plane. TargetLongEdge <= 0 requests native density
// from the finest source-volume spacing. Preview halves the settled sampling
// density while preserving the exact patient-space field of view. Preview and
// settled renders use the same normalized plane coordinates; the conformance
// test permits at most one 8-bit gray level at their shared sample locations.
type ObliqueResolutionRequest struct {
	TargetLongEdge  int
	Preview         bool
	SlabSamples     int
	MaxDimension    int
	MaxWorkingBytes int64
}

// ObliqueResolution is a bounded render plan. PixelSpacing is derived from the
// unchanged Plane, so preview and settled plans cover identical patient-space
// geometry even though their sampling density differs.
type ObliqueResolution struct {
	Width                 int
	Height                int
	PixelSpacing          MeasureSpacing
	EstimatedWorkingBytes int64
	Bounded               bool
}

// PlanObliqueResolution derives a square-pixel output grid from the plane FOV
// and source spacing, then applies dimension and slab-work ceilings. This keeps
// native/custom reslices physically proportionate without permitting a large
// resolution × slab combination to exhaust memory or CPU. It returns an error
// when a non-zero caller ceiling cannot accommodate the minimum 2x2 output.
func (v *Volume) PlanObliqueResolution(plane Plane, request ObliqueResolutionRequest) (ObliqueResolution, error) {
	maxDimension := request.MaxDimension
	bounded := false
	if maxDimension == 0 {
		maxDimension = MaxObliqueOutputDimension
	} else if maxDimension < 2 {
		return ObliqueResolution{}, fmt.Errorf("dicom/render: MaxDimension %d cannot fit the minimum 2x2 output", maxDimension)
	}
	if maxDimension > MaxObliqueOutputDimension {
		maxDimension = MaxObliqueOutputDimension
		bounded = true
	}

	maxWorkingBytes := request.MaxWorkingBytes
	if maxWorkingBytes == 0 || maxWorkingBytes > DefaultObliqueWorkingSetBytes {
		if maxWorkingBytes > DefaultObliqueWorkingSetBytes {
			bounded = true
		}
		maxWorkingBytes = DefaultObliqueWorkingSetBytes
	} else if maxWorkingBytes < 0 {
		return ObliqueResolution{}, fmt.Errorf("dicom/render: MaxWorkingBytes %d must not be negative", maxWorkingBytes)
	}

	uLength := finitePositiveOr(plane.U.Length(), 1)
	vLength := finitePositiveOr(plane.V.Length(), 1)
	longEdge := request.TargetLongEdge
	if longEdge <= 0 {
		spacing := finestVolumeSpacing(v)
		longEdge = int(math.Round(math.Max(uLength, vLength)/spacing)) + 1
	}
	if longEdge < 2 {
		longEdge = 2
		bounded = true
	}
	if longEdge > maxDimension {
		longEdge = maxDimension
		bounded = true
	}
	if request.Preview && longEdge > 2 {
		longEdge = max(2, (longEdge+1)/2)
	}

	width, height := physicalAspectDimensions(uLength, vLength, longEdge)
	slabSamples := max(1, request.SlabSamples)
	if slabSamples > MaxObliqueSlabSamples {
		slabSamples = MaxObliqueSlabSamples
		bounded = true
	}
	bytesPerPixel := int64(1 + 8*slabSamples)
	minWorkingBytes := int64(4) * bytesPerPixel
	if maxWorkingBytes < minWorkingBytes {
		return ObliqueResolution{}, fmt.Errorf(
			"dicom/render: MaxWorkingBytes %d cannot fit the minimum 2x2 output requiring %d bytes",
			maxWorkingBytes,
			minWorkingBytes,
		)
	}
	maxPixels := maxWorkingBytes / bytesPerPixel
	pixels := int64(width) * int64(height)
	if pixels > maxPixels {
		scale := math.Sqrt(float64(maxPixels) / float64(pixels))
		width = max(2, int(math.Floor(float64(width)*scale)))
		height = max(2, int(math.Floor(float64(height)*scale)))
		if int64(width)*int64(height) > maxPixels {
			if width >= height {
				width = max(2, min(width, int(maxPixels/int64(height))))
			} else {
				height = max(2, min(height, int(maxPixels/int64(width))))
			}
		}
		bounded = true
	}

	estimated := int64(width) * int64(height) * bytesPerPixel
	return ObliqueResolution{
		Width:                 width,
		Height:                height,
		PixelSpacing:          plane.PixelSpacingMM(width, height),
		EstimatedWorkingBytes: estimated,
		Bounded:               bounded,
	}, nil
}

func physicalAspectDimensions(uLength, vLength float64, longEdge int) (int, int) {
	if uLength >= vLength {
		return longEdge, max(2, int(math.Round(float64(longEdge-1)*vLength/uLength))+1)
	}
	return max(2, int(math.Round(float64(longEdge-1)*uLength/vLength))+1), longEdge
}

func finestVolumeSpacing(v *Volume) float64 {
	if v == nil {
		return 1
	}
	spacing := math.Inf(1)
	for _, candidate := range []float64{v.ColSpacing, v.RowSpacing, v.SliceSpacing} {
		if candidate > 0 && !math.IsNaN(candidate) && !math.IsInf(candidate, 0) && candidate < spacing {
			spacing = candidate
		}
	}
	if math.IsInf(spacing, 1) {
		return 1
	}
	return spacing
}

func finitePositiveOr(value, fallback float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
}
