package display

import "math"

// ModalityLUT maps stored pixel values to modality values (e.g. Hounsfield
// units). Per DICOM PS3.3 C.11.1 the linear Rescale Slope/Intercept and a
// Modality LUT Sequence are mutually exclusive; when LUT is non-nil it takes
// precedence over Slope/Intercept.
type ModalityLUT struct {
	Slope     float64
	Intercept float64
	LUT       *LUT
}

// Apply maps a stored pixel value to a modality value.
func (m ModalityLUT) Apply(stored int) float64 {
	if m.LUT != nil {
		return float64(m.LUT.Lookup(stored))
	}
	slope := m.Slope
	if slope == 0 {
		slope = 1
	}
	return float64(stored)*slope + m.Intercept
}

// VOIFunction selects the value-of-interest windowing function (PS3.3
// C.11.2.1.2 / C.11.2.1.3).
type VOIFunction int

const (
	// VOILinear is the default DICOM "LINEAR" window function.
	VOILinear VOIFunction = iota
	// VOILinearExact is the "LINEAR_EXACT" window function.
	VOILinearExact
	// VOISigmoid is the "SIGMOID" window function.
	VOISigmoid
)

// VOILUT maps modality values to the 8-bit display range. When LUT is non-nil
// it takes precedence over the Center/Width window (PS3.3 C.11.2): a VOI LUT
// Sequence and Window Center/Width are mutually exclusive.
type VOILUT struct {
	Center   float64
	Width    float64
	Function VOIFunction
	LUT      *LUT
}

// VOIByteMapper prepares a VOI transform for repeated modality-to-display
// mapping. Preparing once avoids rescanning a potentially large LUT for every
// pixel.
type VOIByteMapper struct {
	voi     VOILUT
	lutMin  float64
	lutSpan float64
}

// NewVOIByteMapper prepares voi for efficient per-pixel display mapping.
func NewVOIByteMapper(voi VOILUT) VOIByteMapper {
	mapper := VOIByteMapper{voi: voi}
	if voi.LUT != nil {
		lo, hi := voi.LUT.outputRange()
		mapper.lutMin = float64(lo)
		mapper.lutSpan = float64(hi) - float64(lo)
	}
	return mapper
}

// Byte maps one modality value through the configured VOI LUT or window.
func (m VOIByteMapper) Byte(value float64) uint8 {
	if m.voi.LUT != nil {
		raw := m.voi.LUT.Lookup(int(math.Round(value)))
		return scaleToByte(float64(raw), m.lutMin, m.lutSpan)
	}
	return m.voi.windowToByte(value)
}

// hasWindow reports whether a usable Center/Width window is present.
func (v VOILUT) hasWindow() bool {
	return v.LUT == nil && v.Width > 0
}

// WindowByte maps a single modality value to an 8-bit grayscale display value
// using this VOI window and function. It is intended for callers that have
// already sampled a modality value (for example MPR/MIP reslicing) rather than
// a full stored frame. It applies only the Center/Width window function; a VOI
// LUT, if present, is ignored.
func (v VOILUT) WindowByte(value float64) uint8 {
	return v.windowToByte(value)
}

// WindowUnit maps one modality value to the normalized display range [0,1]
// using the configured Center/Width function. It avoids intermediate 8-bit
// quantization for callers such as volume renderers that use VOI output as a
// continuous opacity factor.
func (v VOILUT) WindowUnit(value float64) float64 {
	switch v.Function {
	case VOISigmoid:
		return sigmoidWindowUnit(value, v.Center, v.Width)
	case VOILinearExact:
		return linearExactWindowUnit(value, v.Center, v.Width)
	default:
		return linearWindowUnit(value, v.Center, v.Width)
	}
}

// windowToByte maps a modality value to an 8-bit grayscale value using the
// configured window function. It is only meaningful when LUT is nil.
func (v VOILUT) windowToByte(value float64) uint8 {
	switch v.Function {
	case VOISigmoid:
		return sigmoidWindow(value, v.Center, v.Width)
	case VOILinearExact:
		return linearExactWindow(value, v.Center, v.Width)
	default:
		return linearWindow(value, v.Center, v.Width)
	}
}

// linearWindow implements the DICOM "LINEAR" VOI function (PS3.3 C.11.2.1.2).
// This intentionally matches the long-standing pixeldata/frame windowing so the
// frame renderer can delegate without changing output.
func linearWindow(value, center, width float64) uint8 {
	return uint8(math.Round(linearWindowUnit(value, center, width) * 255))
}

func linearWindowUnit(value, center, width float64) float64 {
	if width <= 1 {
		if value > center {
			return 1
		}
		return 0
	}
	low := center - 0.5 - (width-1)/2
	high := center - 0.5 + (width-1)/2
	switch {
	case value <= low:
		return 0
	case value > high:
		return 1
	default:
		return clampFloat((value-(center-0.5))/(width-1)+0.5, 0, 1)
	}
}

// linearExactWindow implements the DICOM "LINEAR_EXACT" VOI function
// (PS3.3 C.11.2.1.3.2).
func linearExactWindow(value, center, width float64) uint8 {
	return uint8(math.Round(linearExactWindowUnit(value, center, width) * 255))
}

func linearExactWindowUnit(value, center, width float64) float64 {
	if width <= 0 {
		if value > center {
			return 1
		}
		return 0
	}
	low := center - width/2
	high := center + width/2
	switch {
	case value <= low:
		return 0
	case value >= high:
		return 1
	default:
		return clampFloat((value-low)/width, 0, 1)
	}
}

// sigmoidWindow implements the DICOM "SIGMOID" VOI function
// (PS3.3 C.11.2.1.3.1).
func sigmoidWindow(value, center, width float64) uint8 {
	return uint8(math.Round(sigmoidWindowUnit(value, center, width) * 255))
}

func sigmoidWindowUnit(value, center, width float64) float64 {
	if width == 0 {
		if value > center {
			return 1
		}
		return 0
	}
	y := 1.0 / (1 + math.Exp(-4*(value-center)/width))
	return clampFloat(y, 0, 1)
}

func clampFloat(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}
