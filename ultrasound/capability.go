package ultrasound

// CapabilityIssueCode is a bounded, value-free reason why an ultrasound
// calibration region cannot be used by a supported quantitative workflow.
type CapabilityIssueCode string

const (
	CapabilityIssueNoCalibration            CapabilityIssueCode = "no-calibration"
	CapabilityIssueUnsupportedSpatialFormat CapabilityIssueCode = "unsupported-spatial-format"
	CapabilityIssueUnsupportedUnits         CapabilityIssueCode = "unsupported-units"
	CapabilityIssueFrequencyDoppler         CapabilityIssueCode = "frequency-doppler"
	CapabilityIssueAmbiguousCalibration     CapabilityIssueCode = "ambiguous-calibration"
	CapabilityIssueInvalidCalibration       CapabilityIssueCode = "invalid-calibration"
)

type CapabilityIssue struct {
	Code  CapabilityIssueCode
	Count int
}

// FrameCapabilities describes the calibrated measurement workflows available
// in one frame without exposing source values or patient metadata.
type FrameCapabilities struct {
	Regions            int
	BiometryRegions    int
	DopplerRegions     int
	UnsupportedRegions int
	Issues             []CapabilityIssue
}

// InspectFrameCapabilities classifies every calibration region in a frame.
// Biometry is deliberately limited to calibrated 2D centimetre axes. Doppler
// is deliberately limited to spectral/waveform time and velocity axes;
// frequency Doppler is reported as unsupported rather than relabelled.
func InspectFrameCapabilities(frame FrameCalibration) FrameCapabilities {
	out := FrameCapabilities{Regions: len(frame.Regions)}
	if len(frame.Regions) == 0 {
		out.Issues = []CapabilityIssue{{Code: CapabilityIssueNoCalibration, Count: 1}}
		return out
	}

	counts := make(map[CapabilityIssueCode]int)
	biometry := make([]Region, 0, len(frame.Regions))
	doppler := make([]Region, 0, len(frame.Regions))
	for _, region := range frame.Regions {
		if !validRegionCalibration(region) {
			out.UnsupportedRegions++
			counts[CapabilityIssueInvalidCalibration]++
			continue
		}
		switch region.SpatialFormat {
		case Spatial2D:
			if region.UnitsX == UnitCentimeter && region.UnitsY == UnitCentimeter {
				out.BiometryRegions++
				biometry = append(biometry, region)
			} else {
				out.UnsupportedRegions++
				counts[CapabilityIssueUnsupportedUnits]++
			}
		case SpatialSpectral, SpatialWaveform:
			switch {
			case region.Flags&(1<<2) != 0:
				out.UnsupportedRegions++
				counts[CapabilityIssueFrequencyDoppler]++
			case region.UnitsX == UnitSecond && region.UnitsY == UnitCentimeterPerSec:
				out.DopplerRegions++
				doppler = append(doppler, region)
			default:
				out.UnsupportedRegions++
				counts[CapabilityIssueUnsupportedUnits]++
			}
		default:
			out.UnsupportedRegions++
			counts[CapabilityIssueUnsupportedSpatialFormat]++
		}
	}

	counts[CapabilityIssueAmbiguousCalibration] += ambiguousRegionPairs(biometry, false)
	counts[CapabilityIssueAmbiguousCalibration] += ambiguousRegionPairs(doppler, true)
	order := []CapabilityIssueCode{
		CapabilityIssueInvalidCalibration,
		CapabilityIssueFrequencyDoppler,
		CapabilityIssueUnsupportedUnits,
		CapabilityIssueUnsupportedSpatialFormat,
		CapabilityIssueAmbiguousCalibration,
	}
	for _, code := range order {
		if count := counts[code]; count > 0 {
			out.Issues = append(out.Issues, CapabilityIssue{Code: code, Count: count})
		}
	}
	return out
}

func validRegionCalibration(region Region) bool {
	return region.Index >= 0 && region.Bounds.Min.X >= 0 && region.Bounds.Min.Y >= 0 &&
		region.Bounds.Max.X >= region.Bounds.Min.X && region.Bounds.Max.Y >= region.Bounds.Min.Y &&
		finite(region.DeltaX) && finite(region.DeltaY) && region.DeltaX != 0 && region.DeltaY != 0 &&
		finite(region.ReferenceValueX) && finite(region.ReferenceValueY)
}

func ambiguousRegionPairs(regions []Region, absoluteAxes bool) int {
	count := 0
	for i := range regions {
		for j := i + 1; j < len(regions); j++ {
			if inclusiveBoundsOverlap(regions[i], regions[j]) && !sameScaling(regions[i], regions[j], absoluteAxes) {
				count++
			}
		}
	}
	return count
}

func inclusiveBoundsOverlap(a, b Region) bool {
	return a.Bounds.Min.X <= b.Bounds.Max.X && b.Bounds.Min.X <= a.Bounds.Max.X &&
		a.Bounds.Min.Y <= b.Bounds.Max.Y && b.Bounds.Min.Y <= a.Bounds.Max.Y
}
