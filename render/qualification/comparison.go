package qualification

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const ComparisonV1Schema = "comparison-v1"

var ErrInvalidComparison = errors.New("qualification: invalid comparison")

type ComparisonOutcome string

const (
	ComparisonPass ComparisonOutcome = "pass"
	ComparisonFail ComparisonOutcome = "fail"
)

type ComparisonToleranceOrigin string

const (
	// ComparisonToleranceFrozenV1 is the only tolerance origin accepted by V1.
	// A runner must select a profile before it observes either backend result.
	ComparisonToleranceFrozenV1 ComparisonToleranceOrigin = "frozen-contract-v1"
	// ComparisonToleranceSelfCalibrated is named so imported evidence can be
	// rejected explicitly instead of treating a runtime-derived threshold as a
	// new frozen profile.
	ComparisonToleranceSelfCalibrated ComparisonToleranceOrigin = "self-calibrated"
)

type ComparisonToleranceProfile string

const (
	ComparisonToleranceMPRF32V1       ComparisonToleranceProfile = "mpr-f32-v1"
	ComparisonToleranceVRRGBA8V1      ComparisonToleranceProfile = "vr-rgba8-v1"
	ComparisonToleranceMPRF32ExactV1  ComparisonToleranceProfile = "mpr-f32-exact-v1"
	ComparisonToleranceVRRGBA8ExactV1 ComparisonToleranceProfile = "vr-rgba8-exact-v1"
)

type GeometryBasisV1 string

const (
	// GeometryBasisCanonicalRequestLandmarksV1 compares independently derived
	// patient-LPS landmarks from the exact canonical request admitted to each
	// backend. It proves request parity; the frame comparison remains the
	// behavioral oracle for whether a renderer honored that request.
	GeometryBasisCanonicalRequestLandmarksV1 GeometryBasisV1 = "canonical-request-patient-lps-landmarks-v1"
)

// ComparisonTolerancesV1 is serialized with every result so a reader does not
// need an implicit default. Validate requires field-for-field equality with one
// of the frozen profiles returned by this package; a runner cannot derive these
// values from the frames under comparison.
type ComparisonTolerancesV1 struct {
	Origin                                  ComparisonToleranceOrigin  `json:"origin"`
	Profile                                 ComparisonToleranceProfile `json:"profile"`
	MaxLandmarkErrorPixels                  float64                    `json:"max_landmark_error_pixels"`
	MaxLandmarkErrorMinSpacingFraction      float64                    `json:"max_landmark_error_min_spacing_fraction"`
	FrameAbsComponentTolerance              float64                    `json:"frame_abs_component_tolerance"`
	MaxFrameComponentsOverToleranceFraction float64                    `json:"max_frame_components_over_tolerance_fraction"`
	RequireExactGeometrySHA256              bool                       `json:"require_exact_geometry_sha256"`
	RequireExactFrameSHA256                 bool                       `json:"require_exact_frame_sha256"`
}

// FrozenComparisonTolerancesV1 returns the immutable V1 profile for an
// operation. The geometry limits come from the VolumeSnapshot V1 contract.
// MPR retains the accepted F32 absolute tolerance; VR retains the accepted
// task-016 RGBA8 per-component/fraction tolerance.
func FrozenComparisonTolerancesV1(operation Operation) (ComparisonTolerancesV1, error) {
	common := ComparisonTolerancesV1{
		Origin:                             ComparisonToleranceFrozenV1,
		MaxLandmarkErrorPixels:             0.5,
		MaxLandmarkErrorMinSpacingFraction: 0.5,
	}
	switch operation {
	case OperationMPR:
		common.Profile = ComparisonToleranceMPRF32V1
		common.FrameAbsComponentTolerance = 1e-5
		common.MaxFrameComponentsOverToleranceFraction = 0
	case OperationVR:
		common.Profile = ComparisonToleranceVRRGBA8V1
		common.FrameAbsComponentTolerance = 32
		common.MaxFrameComponentsOverToleranceFraction = 0.002
	default:
		return ComparisonTolerancesV1{}, fmt.Errorf(
			"%w: operation %q has no frozen comparison profile",
			ErrInvalidComparison, operation,
		)
	}
	return common, nil
}

// FrozenExactComparisonTolerancesV1 returns the frozen V1 variant used by
// campaigns that require both request geometry and frame bytes to be identical.
func FrozenExactComparisonTolerancesV1(operation Operation) (ComparisonTolerancesV1, error) {
	tolerances, err := FrozenComparisonTolerancesV1(operation)
	if err != nil {
		return ComparisonTolerancesV1{}, err
	}
	switch operation {
	case OperationMPR:
		tolerances.Profile = ComparisonToleranceMPRF32ExactV1
	case OperationVR:
		tolerances.Profile = ComparisonToleranceVRRGBA8ExactV1
	}
	tolerances.RequireExactGeometrySHA256 = true
	tolerances.RequireExactFrameSHA256 = true
	return tolerances, nil
}

type GeometryComparisonV1 struct {
	Basis                  GeometryBasisV1 `json:"basis"`
	ReferenceSHA256        string          `json:"reference_sha256"`
	CandidateSHA256        string          `json:"candidate_sha256"`
	HashesMatch            bool            `json:"hashes_match"`
	LandmarkCount          uint64          `json:"landmark_count"`
	MinimumSourceSpacingMM float64         `json:"minimum_source_spacing_mm"`
	MaxLandmarkErrorPixels float64         `json:"max_landmark_error_pixels"`
	MaxLandmarkErrorMM     float64         `json:"max_landmark_error_mm"`
}

type FrameComparisonV1 struct {
	ReferenceSHA256           string  `json:"reference_sha256"`
	CandidateSHA256           string  `json:"candidate_sha256"`
	HashesMatch               bool    `json:"hashes_match"`
	ComparedComponents        uint64  `json:"compared_components"`
	ComponentsOverTolerance   uint64  `json:"components_over_tolerance"`
	MaxAbsComponentDifference float64 `json:"max_abs_component_difference"`
}

// FractionOverTolerance returns the observed component fraction without
// storing a second, potentially inconsistent floating-point value.
func (frame FrameComparisonV1) FractionOverTolerance() float64 {
	if frame.ComparedComponents == 0 {
		return math.NaN()
	}
	return float64(frame.ComponentsOverTolerance) / float64(frame.ComparedComponents)
}

// ComparisonV1 is deliberately independent of EvidenceBundleV1. It records
// one cross-backend geometry/frame decision at a frozen state-sequence step;
// evidence streams may reference the same run and generations without being
// embedded in, or required to validate, this contract.
type ComparisonV1 struct {
	Schema           string                 `json:"schema"`
	RunID            string                 `json:"run_id"`
	Sequence         string                 `json:"sequence"`
	StepID           string                 `json:"step_id"`
	SequenceNo       uint64                 `json:"sequence_no"`
	Operation        Operation              `json:"operation"`
	ReferenceBackend string                 `json:"reference_backend"`
	CandidateBackend string                 `json:"candidate_backend"`
	Generation       Generation             `json:"generation"`
	Geometry         GeometryComparisonV1   `json:"geometry"`
	Frame            FrameComparisonV1      `json:"frame"`
	Tolerances       ComparisonTolerancesV1 `json:"tolerances"`
	Outcome          ComparisonOutcome      `json:"outcome"`
}

// ExpectedOutcome validates the comparison inputs and derives the only
// permissible outcome. Out-of-tolerance observations are valid failure
// evidence; RequirePass turns that evidence into a qualification gate.
func (comparison ComparisonV1) ExpectedOutcome() (ComparisonOutcome, error) {
	if err := comparison.validateInputs(); err != nil {
		return "", err
	}
	tolerances := comparison.Tolerances
	geometryPass :=
		(!tolerances.RequireExactGeometrySHA256 || comparison.Geometry.HashesMatch) &&
			comparison.Geometry.MaxLandmarkErrorPixels <= tolerances.MaxLandmarkErrorPixels &&
			comparison.Geometry.MaxLandmarkErrorMM <=
				tolerances.MaxLandmarkErrorMinSpacingFraction*
					comparison.Geometry.MinimumSourceSpacingMM
	framePass :=
		(!tolerances.RequireExactFrameSHA256 || comparison.Frame.HashesMatch) &&
			comparison.Frame.FractionOverTolerance() <=
				tolerances.MaxFrameComponentsOverToleranceFraction
	if geometryPass && framePass {
		return ComparisonPass, nil
	}
	return ComparisonFail, nil
}

func (comparison ComparisonV1) Validate() error {
	expected, err := comparison.ExpectedOutcome()
	if err != nil {
		return err
	}
	if comparison.Outcome != expected {
		return fmt.Errorf(
			"%w: outcome %q does not match derived outcome %q",
			ErrInvalidComparison, comparison.Outcome, expected,
		)
	}
	return nil
}

func (comparison ComparisonV1) RequirePass() error {
	if err := comparison.Validate(); err != nil {
		return err
	}
	if comparison.Outcome != ComparisonPass {
		return fmt.Errorf(
			"%w: %s/%s comparison failed frozen tolerances",
			ErrInvalidComparison, comparison.Sequence, comparison.StepID,
		)
	}
	return nil
}

func (comparison ComparisonV1) validateInputs() error {
	if comparison.Schema != ComparisonV1Schema {
		return fmt.Errorf(
			"%w: schema %q",
			ErrInvalidComparison, comparison.Schema,
		)
	}
	for field, value := range map[string]string{
		"run_id":            comparison.RunID,
		"sequence":          comparison.Sequence,
		"step_id":           comparison.StepID,
		"reference_backend": comparison.ReferenceBackend,
		"candidate_backend": comparison.CandidateBackend,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: empty %s", ErrInvalidComparison, field)
		}
	}
	if comparison.ReferenceBackend == comparison.CandidateBackend {
		return fmt.Errorf(
			"%w: reference and candidate backend are identical",
			ErrInvalidComparison,
		)
	}
	if comparison.SequenceNo == 0 {
		return fmt.Errorf("%w: zero sequence number", ErrInvalidComparison)
	}
	if comparison.Generation.Volume == 0 ||
		comparison.Generation.View == 0 ||
		comparison.Generation.Presentation == 0 {
		return fmt.Errorf("%w: incomplete generation tuple", ErrInvalidComparison)
	}
	frozen, err := FrozenComparisonTolerancesV1(comparison.Operation)
	if err != nil {
		return err
	}
	if comparison.Tolerances.Origin == ComparisonToleranceSelfCalibrated {
		return fmt.Errorf(
			"%w: self-calibrated tolerance is forbidden",
			ErrInvalidComparison,
		)
	}
	exact, err := FrozenExactComparisonTolerancesV1(comparison.Operation)
	if err != nil {
		return err
	}
	if comparison.Tolerances != frozen && comparison.Tolerances != exact {
		return fmt.Errorf(
			"%w: tolerance profile %q is not the frozen %q profile",
			ErrInvalidComparison, comparison.Tolerances.Profile, frozen.Profile,
		)
	}
	if err := validateGeometryComparisonV1(comparison.Geometry); err != nil {
		return err
	}
	if err := validateFrameComparisonV1(
		comparison.Frame,
		comparison.Tolerances.FrameAbsComponentTolerance,
	); err != nil {
		return err
	}
	return nil
}

func validateGeometryComparisonV1(geometry GeometryComparisonV1) error {
	if geometry.Basis != GeometryBasisCanonicalRequestLandmarksV1 {
		return fmt.Errorf(
			"%w: unsupported geometry basis %q",
			ErrInvalidComparison,
			geometry.Basis,
		)
	}
	if err := validateComparisonSHA256("reference geometry", geometry.ReferenceSHA256); err != nil {
		return err
	}
	if err := validateComparisonSHA256("candidate geometry", geometry.CandidateSHA256); err != nil {
		return err
	}
	if geometry.HashesMatch != (geometry.ReferenceSHA256 == geometry.CandidateSHA256) {
		return fmt.Errorf(
			"%w: geometry hashes_match disagrees with hashes",
			ErrInvalidComparison,
		)
	}
	if geometry.LandmarkCount == 0 {
		return fmt.Errorf("%w: zero geometry landmark count", ErrInvalidComparison)
	}
	if !positiveFiniteComparison(geometry.MinimumSourceSpacingMM) ||
		!nonNegativeFiniteComparison(geometry.MaxLandmarkErrorPixels) ||
		!nonNegativeFiniteComparison(geometry.MaxLandmarkErrorMM) {
		return fmt.Errorf(
			"%w: invalid geometry spacing or landmark error",
			ErrInvalidComparison,
		)
	}
	return nil
}

func validateFrameComparisonV1(frame FrameComparisonV1, absTolerance float64) error {
	if err := validateComparisonSHA256("reference frame", frame.ReferenceSHA256); err != nil {
		return err
	}
	if err := validateComparisonSHA256("candidate frame", frame.CandidateSHA256); err != nil {
		return err
	}
	if frame.HashesMatch != (frame.ReferenceSHA256 == frame.CandidateSHA256) {
		return fmt.Errorf(
			"%w: frame hashes_match disagrees with hashes",
			ErrInvalidComparison,
		)
	}
	if frame.ComparedComponents == 0 ||
		frame.ComponentsOverTolerance > frame.ComparedComponents {
		return fmt.Errorf(
			"%w: invalid compared/over-tolerance component counts",
			ErrInvalidComparison,
		)
	}
	if !nonNegativeFiniteComparison(frame.MaxAbsComponentDifference) {
		return fmt.Errorf(
			"%w: invalid maximum frame component difference",
			ErrInvalidComparison,
		)
	}
	if frame.MaxAbsComponentDifference <= absTolerance &&
		frame.ComponentsOverTolerance != 0 {
		return fmt.Errorf(
			"%w: components exceed tolerance but maximum difference does not",
			ErrInvalidComparison,
		)
	}
	if frame.MaxAbsComponentDifference > absTolerance &&
		frame.ComponentsOverTolerance == 0 {
		return fmt.Errorf(
			"%w: maximum difference exceeds tolerance without an affected component",
			ErrInvalidComparison,
		)
	}
	return nil
}

func validateComparisonSHA256(field, value string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return fmt.Errorf("%w: %s is not lowercase SHA-256", ErrInvalidComparison, field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf(
			"%w: %s is not lowercase SHA-256: %v",
			ErrInvalidComparison, field, err,
		)
	}
	return nil
}

func positiveFiniteComparison(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonNegativeFiniteComparison(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// WriteComparisonJSONL validates the complete ordered stream before the first
// byte is published. Struct field order and the absence of maps in the wire
// contract make repeated serialization byte-identical.
func WriteComparisonJSONL(dst io.Writer, comparisons []ComparisonV1) error {
	if dst == nil {
		return fmt.Errorf("%w: nil comparison writer", ErrInvalidComparison)
	}
	if len(comparisons) == 0 {
		return fmt.Errorf("%w: empty comparison stream", ErrInvalidComparison)
	}
	var (
		previousSequenceNo uint64
		runID              string
		referenceBackend   string
		candidateBackend   string
		buffer             strings.Builder
	)
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for index, comparison := range comparisons {
		if err := comparison.Validate(); err != nil {
			return fmt.Errorf("comparison record %d: %w", index, err)
		}
		if comparison.SequenceNo <= previousSequenceNo {
			return fmt.Errorf(
				"%w: sequence_no %d is duplicate or out of order after %d",
				ErrInvalidComparison, comparison.SequenceNo, previousSequenceNo,
			)
		}
		if index == 0 {
			runID = comparison.RunID
			referenceBackend = comparison.ReferenceBackend
			candidateBackend = comparison.CandidateBackend
		} else if comparison.RunID != runID ||
			comparison.ReferenceBackend != referenceBackend ||
			comparison.CandidateBackend != candidateBackend {
			return fmt.Errorf(
				"%w: comparison stream identity changed at sequence_no %d",
				ErrInvalidComparison, comparison.SequenceNo,
			)
		}
		if err := encoder.Encode(comparison); err != nil {
			return fmt.Errorf("comparison record %d: %w", index, err)
		}
		previousSequenceNo = comparison.SequenceNo
	}
	text := buffer.String()
	written, err := io.WriteString(dst, text)
	if err != nil {
		return err
	}
	if written != len(text) {
		return io.ErrShortWrite
	}
	return nil
}
