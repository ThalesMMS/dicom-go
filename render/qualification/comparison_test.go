package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func TestQualificationComparisonUsesFrozenExplicitTolerances(t *testing.T) {
	mpr, err := FrozenComparisonTolerancesV1(OperationMPR)
	if err != nil {
		t.Fatal(err)
	}
	if mpr != (ComparisonTolerancesV1{
		Origin:                                  ComparisonToleranceFrozenV1,
		Profile:                                 ComparisonToleranceMPRF32V1,
		MaxLandmarkErrorPixels:                  0.5,
		MaxLandmarkErrorMinSpacingFraction:      0.5,
		FrameAbsComponentTolerance:              1e-5,
		MaxFrameComponentsOverToleranceFraction: 0,
	}) {
		t.Fatalf("MPR frozen tolerances drifted: %+v", mpr)
	}
	vr, err := FrozenComparisonTolerancesV1(OperationVR)
	if err != nil {
		t.Fatal(err)
	}
	if vr != (ComparisonTolerancesV1{
		Origin:                                  ComparisonToleranceFrozenV1,
		Profile:                                 ComparisonToleranceVRRGBA8V1,
		MaxLandmarkErrorPixels:                  0.5,
		MaxLandmarkErrorMinSpacingFraction:      0.5,
		FrameAbsComponentTolerance:              32,
		MaxFrameComponentsOverToleranceFraction: 0.002,
	}) {
		t.Fatalf("VR frozen tolerances drifted: %+v", vr)
	}
	if _, err := FrozenComparisonTolerancesV1(Operation2D); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("2D tolerance error = %v, want ErrInvalidComparison", err)
	}
}

func TestQualificationComparisonDerivesPassAndFailure(t *testing.T) {
	mpr := positiveComparisonV1(t, OperationMPR)
	if err := mpr.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := mpr.RequirePass(); err != nil {
		t.Fatal(err)
	}

	vr := positiveComparisonV1(t, OperationVR)
	vr.Frame.MaxAbsComponentDifference = 80
	vr.Frame.ComponentsOverTolerance = 2
	vr.Frame.ComparedComponents = 1000
	if err := vr.Validate(); err != nil {
		t.Fatalf("VR boundary comparison failed: %v", err)
	}
	if got := vr.Frame.FractionOverTolerance(); got != 0.002 {
		t.Fatalf("VR fraction = %g, want 0.002", got)
	}

	vr.Frame.ComponentsOverTolerance = 3
	vr.Outcome = ComparisonFail
	if err := vr.Validate(); err != nil {
		t.Fatalf("valid failure evidence was rejected: %v", err)
	}
	if err := vr.RequirePass(); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("RequirePass error = %v, want ErrInvalidComparison", err)
	}
	vr.Outcome = ComparisonPass
	if err := vr.Validate(); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("false pass error = %v, want ErrInvalidComparison", err)
	}
}

func TestQualificationComparisonExactSHAProfileIsReachable(t *testing.T) {
	comparison := positiveComparisonV1(t, OperationMPR)
	exact, err := FrozenExactComparisonTolerancesV1(OperationMPR)
	if err != nil {
		t.Fatal(err)
	}
	comparison.Tolerances = exact
	comparison.Outcome = ComparisonFail
	if err := comparison.Validate(); err != nil {
		t.Fatalf("exact profile rejected mismatched hashes: %v", err)
	}
	comparison.Geometry.CandidateSHA256 = comparison.Geometry.ReferenceSHA256
	comparison.Geometry.HashesMatch = true
	comparison.Frame.CandidateSHA256 = comparison.Frame.ReferenceSHA256
	comparison.Frame.HashesMatch = true
	comparison.Outcome = ComparisonPass
	if err := comparison.Validate(); err != nil {
		t.Fatalf("exact profile rejected matching hashes: %v", err)
	}
}

func TestQualificationComparisonRejectsSelfCalibratedTolerance(t *testing.T) {
	comparison := positiveComparisonV1(t, OperationVR)
	comparison.Tolerances.Origin = ComparisonToleranceSelfCalibrated
	comparison.Tolerances.FrameAbsComponentTolerance =
		comparison.Frame.MaxAbsComponentDifference
	if err := comparison.Validate(); !errors.Is(err, ErrInvalidComparison) ||
		!strings.Contains(err.Error(), "self-calibrated") {
		t.Fatalf("Validate error = %v, want explicit self-calibrated rejection", err)
	}
}

func TestQualificationComparisonRejectsProfileAndObservationDrift(t *testing.T) {
	tests := map[string]func(*ComparisonV1){
		"tolerance changed": func(comparison *ComparisonV1) {
			comparison.Tolerances.FrameAbsComponentTolerance++
		},
		"profile changed": func(comparison *ComparisonV1) {
			comparison.Tolerances.Profile = ComparisonToleranceMPRF32V1
		},
		"geometry hash case": func(comparison *ComparisonV1) {
			comparison.Geometry.ReferenceSHA256 = strings.Repeat("A", 64)
		},
		"geometry hash match lie": func(comparison *ComparisonV1) {
			comparison.Geometry.HashesMatch = true
		},
		"frame hash match lie": func(comparison *ComparisonV1) {
			comparison.Frame.HashesMatch = true
		},
		"missing landmarks": func(comparison *ComparisonV1) {
			comparison.Geometry.LandmarkCount = 0
		},
		"nan geometry": func(comparison *ComparisonV1) {
			comparison.Geometry.MaxLandmarkErrorMM = math.NaN()
		},
		"missing components": func(comparison *ComparisonV1) {
			comparison.Frame.ComparedComponents = 0
		},
		"inconsistent over count": func(comparison *ComparisonV1) {
			comparison.Frame.ComponentsOverTolerance = 1
		},
		"identical backends": func(comparison *ComparisonV1) {
			comparison.CandidateBackend = comparison.ReferenceBackend
		},
		"incomplete generation": func(comparison *ComparisonV1) {
			comparison.Generation.Presentation = 0
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			comparison := positiveComparisonV1(t, OperationVR)
			mutate(&comparison)
			if err := comparison.Validate(); !errors.Is(err, ErrInvalidComparison) {
				t.Fatalf("Validate error = %v, want ErrInvalidComparison", err)
			}
		})
	}
}

func TestQualificationComparisonJSONLIsDeterministicAndFailClosed(t *testing.T) {
	first := positiveComparisonV1(t, OperationMPR)
	second := positiveComparisonV1(t, OperationVR)
	second.SequenceNo = 2
	second.StepID = "mixed-03-vr-fit"
	comparisons := []ComparisonV1{first, second}

	var encoded1, encoded2 bytes.Buffer
	if err := WriteComparisonJSONL(&encoded1, comparisons); err != nil {
		t.Fatal(err)
	}
	if err := WriteComparisonJSONL(&encoded2, comparisons); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded1.Bytes(), encoded2.Bytes()) ||
		!bytes.HasSuffix(encoded1.Bytes(), []byte("\n")) {
		t.Fatalf("comparison JSONL is not deterministic:\n%s\n%s", encoded1.Bytes(), encoded2.Bytes())
	}
	const goldenSHA256 = "89ae2dce7f3dd159b76b31cc935d2889a165c7b6cd84538534c3bd82163e0771"
	gotSHA256 := sha256.Sum256(encoded1.Bytes())
	if got := hex.EncodeToString(gotSHA256[:]); got != goldenSHA256 {
		t.Fatalf("comparison JSONL SHA-256 = %s, want %s\n%s", got, goldenSHA256, encoded1.Bytes())
	}

	bad := append([]ComparisonV1(nil), comparisons...)
	bad[1].SequenceNo = 1
	var untouched bytes.Buffer
	if err := WriteComparisonJSONL(&untouched, bad); !errors.Is(err, ErrInvalidComparison) {
		t.Fatalf("out-of-order error = %v, want ErrInvalidComparison", err)
	}
	if untouched.Len() != 0 {
		t.Fatal("invalid stream was partially written")
	}
	if err := WriteComparisonJSONL(deterministicShortWriter{}, comparisons); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v, want io.ErrShortWrite", err)
	}
}

func positiveComparisonV1(t *testing.T, operation Operation) ComparisonV1 {
	t.Helper()
	tolerances, err := FrozenComparisonTolerancesV1(operation)
	if err != nil {
		t.Fatal(err)
	}
	comparison := ComparisonV1{
		Schema:           ComparisonV1Schema,
		RunID:            "run-001",
		Sequence:         "mixed",
		StepID:           "mixed-02-mpr-axial",
		SequenceNo:       1,
		Operation:        operation,
		ReferenceBackend: "dicom-go-cpu-v1",
		CandidateBackend: "vtk-v1",
		Generation:       Generation{Volume: 1, View: 201, Presentation: 1},
		Geometry: GeometryComparisonV1{
			Basis:                  GeometryBasisCanonicalRequestLandmarksV1,
			ReferenceSHA256:        strings.Repeat("1", 64),
			CandidateSHA256:        strings.Repeat("2", 64),
			LandmarkCount:          8,
			MinimumSourceSpacingMM: 0.7,
			MaxLandmarkErrorPixels: 0.25,
			MaxLandmarkErrorMM:     0.3,
		},
		Frame: FrameComparisonV1{
			ReferenceSHA256:           strings.Repeat("3", 64),
			CandidateSHA256:           strings.Repeat("4", 64),
			ComparedComponents:        1024,
			ComponentsOverTolerance:   0,
			MaxAbsComponentDifference: tolerances.FrameAbsComponentTolerance,
		},
		Tolerances: tolerances,
		Outcome:    ComparisonPass,
	}
	return comparison
}
