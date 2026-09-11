package render

import (
	"errors"
	"math"
)

var (
	ErrMPRInvalidInput  = errors.New("dicom/render: invalid MPR input")
	ErrMPRResourceLimit = errors.New("dicom/render: MPR resource limit exceeded")
)

// MPRLimits bounds one reslice before any output allocation. Zero fields use
// safe defaults. Values above the package hard ceilings are capped so callers
// cannot accidentally disable the guardrails.
type MPRLimits struct {
	MaxOutputDimension int
	MaxOutputPixels    int64
	MaxWorkingBytes    int64
	MaxSlabSamples     int
}

// MPRRequestError identifies the rejected field without echoing coordinates or
// other caller-controlled values.
type MPRRequestError struct {
	Field string
	Err   error
}

func (e *MPRRequestError) Error() string {
	if e == nil || e.Err == nil {
		return ErrMPRInvalidInput.Error()
	}
	if e.Field == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Field
}

func (e *MPRRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// DefaultMPRLimits returns the finite limits used by zero-valued MPRLimits.
func DefaultMPRLimits() MPRLimits {
	return MPRLimits{
		MaxOutputDimension: MaxObliqueOutputDimension,
		MaxOutputPixels:    int64(MaxObliqueOutputDimension) * int64(MaxObliqueOutputDimension),
		MaxWorkingBytes:    DefaultObliqueWorkingSetBytes,
		MaxSlabSamples:     MaxObliqueSlabSamples,
	}
}

func normalizeMPRLimits(limits MPRLimits) (MPRLimits, error) {
	if limits.MaxOutputDimension < 0 {
		return MPRLimits{}, invalidMPRField("MaxOutputDimension")
	}
	if limits.MaxOutputPixels < 0 {
		return MPRLimits{}, invalidMPRField("MaxOutputPixels")
	}
	if limits.MaxWorkingBytes < 0 {
		return MPRLimits{}, invalidMPRField("MaxWorkingBytes")
	}
	if limits.MaxSlabSamples < 0 {
		return MPRLimits{}, invalidMPRField("MaxSlabSamples")
	}
	defaults := DefaultMPRLimits()
	if limits.MaxOutputDimension == 0 || limits.MaxOutputDimension > defaults.MaxOutputDimension {
		limits.MaxOutputDimension = defaults.MaxOutputDimension
	}
	if limits.MaxOutputPixels == 0 || limits.MaxOutputPixels > defaults.MaxOutputPixels {
		limits.MaxOutputPixels = defaults.MaxOutputPixels
	}
	if limits.MaxWorkingBytes == 0 || limits.MaxWorkingBytes > defaults.MaxWorkingBytes {
		limits.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	if limits.MaxSlabSamples == 0 || limits.MaxSlabSamples > defaults.MaxSlabSamples {
		limits.MaxSlabSamples = defaults.MaxSlabSamples
	}
	return limits, nil
}

func validateMPRReslice(plane Plane, width, height, slabSamples int, retainedBytesPerPixel int64, limits MPRLimits) (MPRLimits, error) {
	if !validMPRPlane(plane) {
		return MPRLimits{}, invalidMPRField("Plane")
	}
	return validateMPROutput(width, height, slabSamples, retainedBytesPerPixel, limits)
}

func validateMPROutput(width, height, slabSamples int, retainedBytesPerPixel int64, limits MPRLimits) (MPRLimits, error) {
	limits, err := normalizeMPRLimits(limits)
	if err != nil {
		return MPRLimits{}, err
	}
	if width <= 0 {
		return MPRLimits{}, invalidMPRField("Width")
	}
	if height <= 0 {
		return MPRLimits{}, invalidMPRField("Height")
	}
	if slabSamples <= 0 {
		return MPRLimits{}, invalidMPRField("SlabSamples")
	}
	if width > limits.MaxOutputDimension {
		return MPRLimits{}, limitedMPRField("Width")
	}
	if height > limits.MaxOutputDimension {
		return MPRLimits{}, limitedMPRField("Height")
	}
	if slabSamples > limits.MaxSlabSamples {
		return MPRLimits{}, limitedMPRField("SlabSamples")
	}
	if retainedBytesPerPixel <= 0 {
		return MPRLimits{}, invalidMPRField("BytesPerPixel")
	}

	pixels := int64(width) * int64(height)
	if pixels <= 0 || pixels > limits.MaxOutputPixels {
		return MPRLimits{}, limitedMPRField("OutputPixels")
	}
	extraSamples := int64(slabSamples - 1)
	if extraSamples > (math.MaxInt64-retainedBytesPerPixel)/8 {
		return MPRLimits{}, limitedMPRField("WorkingBytes")
	}
	workingBytesPerPixel := retainedBytesPerPixel + extraSamples*8
	if pixels > math.MaxInt64/workingBytesPerPixel || pixels*workingBytesPerPixel > limits.MaxWorkingBytes {
		return MPRLimits{}, limitedMPRField("WorkingBytes")
	}
	return limits, nil
}

func validMPRPlane(plane Plane) bool {
	if !finiteMPRVec3(plane.Origin) || !finiteMPRVec3(plane.U) || !finiteMPRVec3(plane.V) {
		return false
	}
	if !finiteMPRVec3(plane.Origin.Add(plane.U)) ||
		!finiteMPRVec3(plane.Origin.Add(plane.V)) ||
		!finiteMPRVec3(plane.Origin.Add(plane.U).Add(plane.V)) {
		return false
	}
	uLength := plane.U.Length()
	vLength := plane.V.Length()
	normalLength := plane.U.Cross(plane.V).Length()
	return uLength > 0 && vLength > 0 && normalLength > 0 &&
		!math.IsNaN(uLength) && !math.IsInf(uLength, 0) &&
		!math.IsNaN(vLength) && !math.IsInf(vLength, 0) &&
		!math.IsNaN(normalLength) && !math.IsInf(normalLength, 0)
}

func finiteMPRVec3(value Vec3) bool {
	return !math.IsNaN(value.X) && !math.IsInf(value.X, 0) &&
		!math.IsNaN(value.Y) && !math.IsInf(value.Y, 0) &&
		!math.IsNaN(value.Z) && !math.IsInf(value.Z, 0)
}

func invalidMPRField(field string) error {
	return &MPRRequestError{Field: field, Err: ErrMPRInvalidInput}
}

func limitedMPRField(field string) error {
	return &MPRRequestError{Field: field, Err: ErrMPRResourceLimit}
}
