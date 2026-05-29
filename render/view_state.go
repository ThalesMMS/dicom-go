package render

import (
	"errors"
	"fmt"
	"math"
)

const (
	ViewStateContractVersion uint32 = 1
	ViewStateHeaderSizeV1    uint32 = 48
	MaxViewOutputDimension   uint32 = 8192
	MaxViewOutputBytes       uint64 = 256 << 20
	MaxTransferLUTSamples           = 4096
	MaxClippingPlanes               = 8
)

// Contract aliases keep the frozen MIN/MAX vocabulary explicit while reusing
// the established dicom-go slab enum and its persisted numeric values.
const (
	SlabMax = SlabMIP
	SlabMin = SlabMinIP
)

var ErrInvalidViewState = errors.New("render: invalid view state")

type ViewKind uint32

const (
	ViewKindUnknown ViewKind = iota
	ViewKindMPR
	ViewKindVR
)

type Interpolation uint32

const (
	InterpolationUnknown Interpolation = iota
	InterpolationNearest
	InterpolationLinear
	InterpolationCubic
)

type Projection uint32

const (
	ProjectionUnknown Projection = iota
	ProjectionPerspective
	ProjectionParallel
)

type MPRViewState struct {
	PixelOriginLPS      [3]float64
	PixelStepULPS       [3]float64
	PixelStepVLPS       [3]float64
	Interpolation       Interpolation
	SlabMode            SlabMode
	SlabAxisLPS         [3]float64
	SlabThicknessMM     float64
	SlabSampleSpacingMM float64
	OutsideValue        float32
}

type ClippingPlaneLPS [4]float64

type TransferSample struct {
	// RGB is linear-light, matching VRTransferFunction.BakeLUT. A backend that
	// returns display NRGBA performs the final linear-to-sRGB conversion.
	R float32
	G float32
	B float32
	A float32
}

type TransferLUT struct {
	DomainMin float64
	DomainMax float64
	Samples   []TransferSample
}

type VRViewState struct {
	PositionLPS         [3]float64
	FocalPointLPS       [3]float64
	ViewUpLPS           [3]float64
	Projection          Projection
	VerticalFOVRadians  float64
	ParallelScaleMM     float64
	NearMM              float64
	FarMM               float64
	Mode                VRMode
	SampleDistanceMM    float64
	ImageSampleDistance float64
	ShadingEnabled      uint32
	Ambient             float64
	Diffuse             float64
	Specular            float64
	SpecularPower       float64
	// GradientOpacityScale modulates DVR sample opacity by the central-
	// difference HU gradient magnitude. Zero disables the modulation.
	GradientOpacityScale float64
	ClippingPlanesLPS    []ClippingPlaneLPS
	TransferLUT          TransferLUT
}

// ViewState is the value-only, backend-neutral rendering request frozen for
// native, CPU and WebGPU engines. PresentationGeneration is carried through MPR
// even though MPR output remains modality-domain scalar data.
type ViewState struct {
	ContractVersion        uint32
	StructSize             uint32
	Kind                   ViewKind
	VolumeGeneration       uint64
	ViewGeneration         uint64
	PresentationGeneration uint64
	OutputWidth            uint32
	OutputHeight           uint32
	MPR                    MPRViewState
	VR                     VRViewState
}

func ValidateViewState(state ViewState) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidViewState, fmt.Sprintf(format, args...))
	}
	if state.ContractVersion != ViewStateContractVersion {
		return fail("contract version %d", state.ContractVersion)
	}
	if state.StructSize < ViewStateHeaderSizeV1 {
		return fail("struct size %d below V1 minimum %d", state.StructSize, ViewStateHeaderSizeV1)
	}
	if state.VolumeGeneration == 0 || state.ViewGeneration == 0 {
		return fail("zero volume or view generation")
	}
	if state.OutputWidth == 0 || state.OutputHeight == 0 ||
		state.OutputWidth > MaxViewOutputDimension || state.OutputHeight > MaxViewOutputDimension {
		return fail("output dimensions %dx%d outside 1..%d", state.OutputWidth, state.OutputHeight, MaxViewOutputDimension)
	}
	if uint64(state.OutputWidth) > math.MaxUint64/uint64(state.OutputHeight) {
		return fail("output dimension overflow")
	}
	if pixels := uint64(state.OutputWidth) * uint64(state.OutputHeight); pixels > MaxViewOutputBytes/4 {
		return fail("output byte size exceeds %d", MaxViewOutputBytes)
	}
	switch state.Kind {
	case ViewKindMPR:
		return validateMPRViewState(state.MPR, state.OutputWidth, state.OutputHeight, fail)
	case ViewKindVR:
		return validateVRViewState(state.VR, fail)
	default:
		return fail("unsupported view kind %d", state.Kind)
	}
}

func validateMPRViewState(
	state MPRViewState,
	outputWidth, outputHeight uint32,
	fail func(string, ...any) error,
) error {
	if !finite3(state.PixelOriginLPS) || !finite3(state.PixelStepULPS) || !finite3(state.PixelStepVLPS) {
		return fail("non-finite MPR basis")
	}
	u, uOK := normalize3(state.PixelStepULPS)
	v, vOK := normalize3(state.PixelStepVLPS)
	cross, crossOK := cross3Finite(u, v)
	if !uOK || !vOK || !crossOK || norm3(cross) <= 1e-12 {
		return fail("degenerate MPR basis")
	}
	switch state.Interpolation {
	case InterpolationNearest, InterpolationLinear, InterpolationCubic:
	default:
		return fail("unsupported interpolation %d", state.Interpolation)
	}
	if !finite(float64(state.OutsideValue)) || !finite(state.SlabThicknessMM) || state.SlabThicknessMM < 0 {
		return fail("invalid MPR outside value or slab thickness")
	}
	switch state.SlabMode {
	case SlabNone:
	case SlabMIP, SlabMinIP, SlabAverage:
		slabNorm := norm3(state.SlabAxisLPS)
		if !finite3(state.SlabAxisLPS) || !finite(slabNorm) || math.Abs(slabNorm-1) > 1e-6 {
			return fail("slab axis is not finite unit length")
		}
		if !finite(state.SlabSampleSpacingMM) || state.SlabSampleSpacingMM <= 0 {
			return fail("invalid slab sample spacing")
		}
	default:
		return fail("unsupported slab mode %d", state.SlabMode)
	}
	return validateMPRDerivedGeometry(state, outputWidth, outputHeight, fail)
}

func validateMPRDerivedGeometry(
	state MPRViewState,
	outputWidth, outputHeight uint32,
	fail func(string, ...any) error,
) error {
	lastX := float64(outputWidth - 1)
	lastY := float64(outputHeight - 1)
	xValues := []float64{0, math.Max(0, lastX-1), lastX}
	yValues := []float64{0, math.Max(0, lastY-1), lastY}
	offsets := []float64{0}
	if state.SlabMode != SlabNone {
		offsets = []float64{-state.SlabThicknessMM / 2, state.SlabThicknessMM / 2}
	}
	for _, x := range xValues {
		for _, y := range yValues {
			point, ok := scaledAdd3(state.PixelOriginLPS, state.PixelStepULPS, x)
			if !ok {
				return fail("MPR U extent is non-finite")
			}
			point, ok = scaledAdd3(point, state.PixelStepVLPS, y)
			if !ok {
				return fail("MPR V extent is non-finite")
			}
			for _, offset := range offsets {
				sample := point
				if state.SlabMode != SlabNone {
					var sampleOK bool
					sample, sampleOK = scaledAdd3(point, state.SlabAxisLPS, offset)
					if !sampleOK {
						return fail("MPR slab extent is non-finite")
					}
				}
				if x < lastX {
					next, nextOK := scaledAdd3(sample, state.PixelStepULPS, 1)
					if !nextOK || next == sample {
						return fail("MPR U step collapses within the derived extent")
					}
				}
				if y < lastY {
					next, nextOK := scaledAdd3(sample, state.PixelStepVLPS, 1)
					if !nextOK || next == sample {
						return fail("MPR V step collapses within the derived extent")
					}
				}
			}
		}
	}
	return nil
}

func validateVRViewState(state VRViewState, fail func(string, ...any) error) error {
	if !finite3(state.PositionLPS) || !finite3(state.FocalPointLPS) || !finite3(state.ViewUpLPS) {
		return fail("non-finite VR camera")
	}
	direction := sub3(state.FocalPointLPS, state.PositionLPS)
	directionUnit, directionOK := normalize3(direction)
	upUnit, upOK := normalize3(state.ViewUpLPS)
	cameraCross, crossOK := cross3Finite(directionUnit, upUnit)
	if !finite3(direction) || !directionOK || !upOK || !crossOK ||
		norm3(cameraCross) <= 1e-12 {
		return fail("degenerate VR camera")
	}
	switch state.Projection {
	case ProjectionPerspective:
		if !finite(state.VerticalFOVRadians) || state.VerticalFOVRadians <= 0 || state.VerticalFOVRadians >= math.Pi {
			return fail("invalid perspective FOV")
		}
	case ProjectionParallel:
		if !finite(state.ParallelScaleMM) || state.ParallelScaleMM <= 0 {
			return fail("invalid parallel scale")
		}
	default:
		return fail("unsupported projection %d", state.Projection)
	}
	if !finite(state.NearMM) || !finite(state.FarMM) || state.NearMM <= 0 || state.NearMM >= state.FarMM {
		return fail("invalid near/far planes")
	}
	switch state.Mode {
	case VRModeDVR, VRModeMIP, VRModeMinIP, VRModeAverage:
	default:
		return fail("unsupported VR mode %d", state.Mode)
	}
	if !finite(state.SampleDistanceMM) || state.SampleDistanceMM <= 0 ||
		!finite(state.ImageSampleDistance) || state.ImageSampleDistance <= 0 {
		return fail("invalid VR sampling distance")
	}
	if state.ShadingEnabled > 1 {
		return fail("invalid shading flag %d", state.ShadingEnabled)
	}
	for _, value := range []float64{
		state.Ambient,
		state.Diffuse,
		state.Specular,
		state.SpecularPower,
		state.GradientOpacityScale,
	} {
		if !finite(value) || value < 0 {
			return fail("invalid shading or gradient-opacity coefficient")
		}
	}
	if len(state.ClippingPlanesLPS) > MaxClippingPlanes {
		return fail("too many clipping planes: %d", len(state.ClippingPlanesLPS))
	}
	for _, plane := range state.ClippingPlanesLPS {
		normal := [3]float64{plane[0], plane[1], plane[2]}
		if !finite4(plane) || maxAbs3(normal) == 0 {
			return fail("invalid clipping plane")
		}
	}
	if !finite(state.TransferLUT.DomainMin) || !finite(state.TransferLUT.DomainMax) ||
		state.TransferLUT.DomainMin >= state.TransferLUT.DomainMax {
		return fail("invalid transfer domain")
	}
	if len(state.TransferLUT.Samples) < 2 || len(state.TransferLUT.Samples) > MaxTransferLUTSamples {
		return fail("transfer sample count %d outside 2..%d", len(state.TransferLUT.Samples), MaxTransferLUTSamples)
	}
	for _, sample := range state.TransferLUT.Samples {
		for _, value := range []float32{sample.R, sample.G, sample.B, sample.A} {
			if !finite(float64(value)) || value < 0 || value > 1 {
				return fail("invalid transfer sample")
			}
		}
	}
	return nil
}

// FreezeViewState validates and deep-copies the variable-size members before a
// request crosses a scheduler or process boundary. Caller mutations after this
// function returns cannot alter the admitted request.
func FreezeViewState(state ViewState) (ViewState, error) {
	if err := ValidateViewState(state); err != nil {
		return ViewState{}, err
	}
	state.VR.ClippingPlanesLPS = append([]ClippingPlaneLPS(nil), state.VR.ClippingPlanesLPS...)
	state.VR.TransferLUT.Samples = append([]TransferSample(nil), state.VR.TransferLUT.Samples...)
	return state, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finite3(value [3]float64) bool {
	return finite(value[0]) && finite(value[1]) && finite(value[2])
}
func finite4(value [4]float64) bool {
	return finite(value[0]) && finite(value[1]) && finite(value[2]) && finite(value[3])
}
func norm3(value [3]float64) float64 {
	return math.Hypot(math.Hypot(value[0], value[1]), value[2])
}

func normalize3(value [3]float64) ([3]float64, bool) {
	scale := maxAbs3(value)
	if scale == 0 || !finite(scale) {
		return [3]float64{}, false
	}
	scaled := [3]float64{
		value[0] / scale,
		value[1] / scale,
		value[2] / scale,
	}
	length := norm3(scaled)
	result := [3]float64{
		scaled[0] / length,
		scaled[1] / length,
		scaled[2] / length,
	}
	return result, finite3(result)
}

func maxAbs3(value [3]float64) float64 {
	return math.Max(math.Abs(value[0]), math.Max(math.Abs(value[1]), math.Abs(value[2])))
}

func cross3Finite(a, b [3]float64) ([3]float64, bool) {
	result := [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
	return result, finite3(result)
}

func scaledAdd3(base, direction [3]float64, scale float64) ([3]float64, bool) {
	var result [3]float64
	for component := 0; component < 3; component++ {
		product := direction[component] * scale
		result[component] = base[component] + product
		if !finite(product) || !finite(result[component]) {
			return [3]float64{}, false
		}
	}
	return result, true
}

func sub3(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]}
}
