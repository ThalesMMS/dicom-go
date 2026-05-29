package nifti

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSidecarMarshalCanonicalSchema(t *testing.T) {
	sidecar := Sidecar{
		Dimensions:               [4]int{64, 32, 12, 3},
		Datatype:                 "int16",
		ScalingPolicy:            "preserve-uniform",
		Reordered:                true,
		Resampled:                false,
		Interpolation:            "none",
		Units:                    CodedUnits{Code: "HU", Scheme: "UCUM"},
		Quantity:                 CodedUnits{Code: "126400", Scheme: "DCM"},
		TemporalOffsetsSeconds:   []float64{0, 1.5, 3},
		TemporalDurationsSeconds: []float64{1.5, 1.5, 1.5},
		Warnings:                 []string{"frames_reordered"},
	}

	got, err := sidecar.Marshal(4096)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"schema":"dicom-go.nifti.sidecar/v1","dimensions":[64,32,12,3],"datatype":"int16","scaling_policy":"preserve-uniform","reordered":true,"resampled":false,"interpolation":"none","units":{"code":"HU","scheme":"UCUM"},"quantity":{"code":"126400","scheme":"DCM"},"temporal_offsets_seconds":[0,1.5,3],"temporal_durations_seconds":[1.5,1.5,1.5],"warnings":["frames_reordered"]}`
	if string(got) != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestSidecarMarshalEnforcesByteLimit(t *testing.T) {
	sidecar := Sidecar{Dimensions: [4]int{1, 1, 1, 1}}
	encoded, err := sidecar.Marshal(4096)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if _, err := sidecar.Marshal(len(encoded)); err != nil {
		t.Fatalf("Marshal() at exact limit error = %v", err)
	}

	tooLarge, err := sidecar.Marshal(len(encoded) - 1)
	if tooLarge != nil {
		t.Fatalf("Marshal() returned %d bytes past the configured limit", len(tooLarge))
	}
	var sizeErr *SidecarSizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("Marshal() error = %T %v, want *SidecarSizeError", err, err)
	}
	if sizeErr.ActualBytes != len(encoded) || sizeErr.MaxBytes != len(encoded)-1 {
		t.Fatalf("SidecarSizeError = %#v, want actual=%d max=%d", sizeErr, len(encoded), len(encoded)-1)
	}
	if !errors.Is(err, ErrSidecarTooLarge) {
		t.Fatalf("Marshal() error = %v, want ErrSidecarTooLarge", err)
	}

	const phiCanary = "DOE^JANE"
	tooLarge, err = (Sidecar{Warnings: []string{phiCanary}}).Marshal(1)
	if tooLarge != nil {
		t.Fatalf("Marshal() returned PHI-bearing output past the configured limit: %q", tooLarge)
	}
	if !errors.As(err, &sizeErr) {
		t.Fatalf("Marshal() PHI canary error = %T %v, want *SidecarSizeError", err, err)
	}
	if strings.Contains(err.Error(), phiCanary) {
		t.Fatalf("size error leaked sidecar content: %v", err)
	}

	for _, maxBytes := range []int{0, -1} {
		if _, err := sidecar.Marshal(maxBytes); !errors.Is(err, ErrInvalidSidecarLimit) {
			t.Errorf("Marshal(%d) error = %v, want ErrInvalidSidecarLimit", maxBytes, err)
		}
	}
}

func TestSidecarMarshalIsDeterministicAndClosedToPHIFields(t *testing.T) {
	sidecar := Sidecar{
		Dimensions:               [4]int{8, 9, 10, 2},
		Datatype:                 "float32",
		ScalingPolicy:            "apply-float32",
		Reordered:                true,
		Interpolation:            "none",
		Units:                    CodedUnits{Code: "HU", Scheme: "UCUM"},
		Quantity:                 CodedUnits{Code: "126400", Scheme: "DCM"},
		TemporalOffsetsSeconds:   []float64{0, 2.25},
		TemporalDurationsSeconds: []float64{2.25, 2.25},
		Warnings:                 []string{"frames_reordered", "temporal_spacing_explicit"},
	}

	first, err := sidecar.Marshal(4096)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for i := 0; i < 32; i++ {
		next, err := sidecar.Marshal(4096)
		if err != nil {
			t.Fatalf("Marshal() iteration %d error = %v", i, err)
		}
		if !bytes.Equal(next, first) {
			t.Fatalf("Marshal() iteration %d was not deterministic:\nfirst=%s\nnext=%s", i, first, next)
		}
	}

	wantFields := map[string]struct{}{
		"Dimensions": {}, "Datatype": {}, "ScalingPolicy": {},
		"Reordered": {}, "Resampled": {}, "Interpolation": {},
		"Units": {}, "Quantity": {}, "TemporalOffsetsSeconds": {},
		"TemporalDurationsSeconds": {}, "Warnings": {},
	}
	typ := reflect.TypeOf(Sidecar{})
	if typ.NumField() != len(wantFields) {
		t.Fatalf("Sidecar has %d fields, want closed allowlist of %d", typ.NumField(), len(wantFields))
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if _, ok := wantFields[field.Name]; !ok {
			t.Errorf("Sidecar field %q is outside the security allowlist", field.Name)
		}
		lowerName := strings.ToLower(field.Name)
		for _, forbidden := range []string{"patient", "person", "uid", "path", "date", "description", "meaning"} {
			if strings.Contains(lowerName, forbidden) {
				t.Errorf("Sidecar field %q can carry forbidden %s metadata", field.Name, forbidden)
			}
		}
	}
	unitsType := reflect.TypeOf(CodedUnits{})
	if unitsType.NumField() != 2 || unitsType.Field(0).Name != "Code" || unitsType.Field(1).Name != "Scheme" {
		t.Fatalf("CodedUnits must contain only Code and Scheme, got %v", unitsType)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	wantKeys := map[string]struct{}{
		"schema": {}, "dimensions": {}, "datatype": {}, "scaling_policy": {},
		"reordered": {}, "resampled": {}, "interpolation": {}, "units": {}, "quantity": {},
		"temporal_offsets_seconds": {}, "temporal_durations_seconds": {}, "warnings": {},
	}
	if len(document) != len(wantKeys) {
		t.Fatalf("sidecar has %d JSON keys, want %d: %s", len(document), len(wantKeys), first)
	}
	for key := range document {
		if _, ok := wantKeys[key]; !ok {
			t.Errorf("JSON key %q is outside the security allowlist", key)
		}
	}

	for _, canary := range []string{
		"DOE^JANE", "MRN-739184", "1.2.840.113619.2.55.3.604688123.1", "/patients/JaneDoe/scan.dcm", "19700101",
	} {
		if bytes.Contains(first, []byte(canary)) {
			t.Errorf("sidecar leaked PHI canary %q", canary)
		}
	}
}
