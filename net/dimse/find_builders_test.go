package dimse

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestNewQueryRetrieveLevelElement(t *testing.T) {
	e, err := NewQueryRetrieveLevelElement("study")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Header.Tag != core.NewTag(0x0008, 0x0052) {
		t.Fatalf("unexpected tag: %v", e.Header.Tag)
	}
	if e.Header.VR != core.VRCS {
		t.Fatalf("unexpected VR: %v", e.Header.VR)
	}
	if got := e.StringValue(); got != "STUDY" {
		t.Fatalf("unexpected value: %q", got)
	}

	if e, err := NewQueryRetrieveLevelElement("patient"); err != nil {
		t.Fatalf("unexpected patient level error: %v", err)
	} else if got := e.StringValue(); got != "PATIENT" {
		t.Fatalf("patient level value = %q, want PATIENT", got)
	}

	if e, err := NewQueryRetrieveLevelElement("image"); err != nil {
		t.Fatalf("unexpected image level error: %v", err)
	} else if got := e.StringValue(); got != "IMAGE" {
		t.Fatalf("image level value = %q, want IMAGE", got)
	}

	if _, err := NewQueryRetrieveLevelElement("visit"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestBuildStudyRootStudyFindKeys(t *testing.T) {
	elems, err := BuildStudyRootStudyFindKeys(map[string]string{
		"PatientID":        "P1",
		"StudyInstanceUID": "1.2.3",
		"StudyDate":        "20260101",
	}, "Modality")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(elems) < 2 {
		t.Fatalf("expected elements")
	}
	if elems[0].Header.Tag != core.NewTag(0x0008, 0x0052) {
		t.Fatalf("first element should be QueryRetrieveLevel")
	}
}

func TestBuildStudyRootSeriesFindKeys(t *testing.T) {
	elems, err := BuildStudyRootSeriesFindKeys(map[string]string{
		"StudyInstanceUID":  "1.2.3",
		"SeriesInstanceUID": "4.5.6",
		"Modality":          "CT",
	}, "PatientID")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(elems) < 2 {
		t.Fatalf("expected elements")
	}
	if elems[0].Header.Tag != core.NewTag(0x0008, 0x0052) {
		t.Fatalf("first element should be QueryRetrieveLevel")
	}
}

func TestBuildStudyRootImageFindKeys(t *testing.T) {
	elems, err := BuildStudyRootImageFindKeys(map[string]string{
		"StudyInstanceUID":  "1.2.3",
		"SeriesInstanceUID": "4.5.6",
		"SOPInstanceUID":    "7.8.9",
		"SOPClassUID":       "1.2.840.10008.5.1.4.1.1.2",
	}, "InstanceNumber")
	if err != nil {
		t.Fatalf("BuildStudyRootImageFindKeys() error = %v", err)
	}
	if got := elems[0].StringValue(); got != QueryRetrieveLevelImage {
		t.Fatalf("QueryRetrieveLevel = %q, want %q", got, QueryRetrieveLevelImage)
	}
}

func TestBuildStudyRootFindKeysUnknownKeyword(t *testing.T) {
	if _, err := BuildStudyRootStudyFindKeys(map[string]string{"NotAKeyword": "x"}); err == nil {
		t.Fatalf("expected error")
	}
}

func TestStudyRootModelMetadata(t *testing.T) {
	levels, err := QueryRetrieveLevels(QueryRetrieveModelStudyRoot)
	if err != nil {
		t.Fatalf("QueryRetrieveLevels(StudyRoot) error = %v", err)
	}
	wantLevels := []string{
		QueryRetrieveLevelStudy,
		QueryRetrieveLevelSeries,
		QueryRetrieveLevelImage,
	}
	if len(levels) != len(wantLevels) {
		t.Fatalf("Study Root levels = %#v, want %#v", levels, wantLevels)
	}
	for i, want := range wantLevels {
		if levels[i] != want {
			t.Fatalf("Study Root levels = %#v, want %#v", levels, wantLevels)
		}
	}

	required, err := QueryRetrieveRequiredKeys(QueryRetrieveModelStudyRoot, QueryRetrieveLevelImage)
	if err != nil {
		t.Fatalf("QueryRetrieveRequiredKeys(StudyRoot, IMAGE) error = %v", err)
	}
	wantRequired := []string{"StudyInstanceUID", "SeriesInstanceUID", "SOPInstanceUID"}
	if len(required) != len(wantRequired) {
		t.Fatalf("required keys = %#v, want %#v", required, wantRequired)
	}
	for i, want := range wantRequired {
		if required[i] != want {
			t.Fatalf("required keys = %#v, want %#v", required, wantRequired)
		}
	}
}

func TestBuildPatientRootFindKeys(t *testing.T) {
	elems, err := BuildPatientRootPatientFindKeys(map[string]string{
		"PatientID":   "P1",
		"PatientName": "DOE^JANE",
	}, "StudyInstanceUID")
	if err != nil {
		t.Fatalf("BuildPatientRootPatientFindKeys() error = %v", err)
	}
	if got := elems[0].StringValue(); got != QueryRetrieveLevelPatient {
		t.Fatalf("QueryRetrieveLevel = %q, want %q", got, QueryRetrieveLevelPatient)
	}

	elems, err = BuildPatientRootStudyFindKeys(map[string]string{
		"PatientID":        "P1",
		"StudyInstanceUID": "1.2.3",
	}, "StudyDate")
	if err != nil {
		t.Fatalf("BuildPatientRootStudyFindKeys() error = %v", err)
	}
	if got := elems[0].StringValue(); got != QueryRetrieveLevelStudy {
		t.Fatalf("QueryRetrieveLevel = %q, want %q", got, QueryRetrieveLevelStudy)
	}

	elems, err = BuildPatientRootSeriesFindKeys(map[string]string{
		"PatientID":         "P1",
		"StudyInstanceUID":  "1.2.3",
		"SeriesInstanceUID": "4.5.6",
	}, "Modality")
	if err != nil {
		t.Fatalf("BuildPatientRootSeriesFindKeys() error = %v", err)
	}
	if got := elems[0].StringValue(); got != QueryRetrieveLevelSeries {
		t.Fatalf("QueryRetrieveLevel = %q, want %q", got, QueryRetrieveLevelSeries)
	}

	elems, err = BuildPatientRootImageFindKeys(map[string]string{
		"PatientID":         "P1",
		"StudyInstanceUID":  "1.2.3",
		"SeriesInstanceUID": "4.5.6",
		"SOPInstanceUID":    "7.8.9",
	})
	if err != nil {
		t.Fatalf("BuildPatientRootImageFindKeys() error = %v", err)
	}
	if got := elems[0].StringValue(); got != QueryRetrieveLevelImage {
		t.Fatalf("QueryRetrieveLevel = %q, want %q", got, QueryRetrieveLevelImage)
	}
}

func TestBuildFindKeysRejectsUnknownReturnKey(t *testing.T) {
	if _, err := BuildStudyRootStudyFindKeys(nil, "NotAKeyword"); err == nil {
		t.Fatal("BuildStudyRootStudyFindKeys() error = nil, want unknown return key error")
	}
}

func TestPatientRootModelMetadata(t *testing.T) {
	levels, err := QueryRetrieveLevels(QueryRetrieveModelPatientRoot)
	if err != nil {
		t.Fatalf("QueryRetrieveLevels(PatientRoot) error = %v", err)
	}
	wantLevels := []string{
		QueryRetrieveLevelPatient,
		QueryRetrieveLevelStudy,
		QueryRetrieveLevelSeries,
		QueryRetrieveLevelImage,
	}
	if len(levels) != len(wantLevels) {
		t.Fatalf("Patient Root levels = %#v, want %#v", levels, wantLevels)
	}
	for i, want := range wantLevels {
		if levels[i] != want {
			t.Fatalf("Patient Root levels = %#v, want %#v", levels, wantLevels)
		}
	}

	required, err := QueryRetrieveRequiredKeys(QueryRetrieveModelPatientRoot, QueryRetrieveLevelImage)
	if err != nil {
		t.Fatalf("QueryRetrieveRequiredKeys(PatientRoot, IMAGE) error = %v", err)
	}
	wantRequired := []string{"PatientID", "StudyInstanceUID", "SeriesInstanceUID", "SOPInstanceUID"}
	if len(required) != len(wantRequired) {
		t.Fatalf("required keys = %#v, want %#v", required, wantRequired)
	}
	for i, want := range wantRequired {
		if required[i] != want {
			t.Fatalf("required keys = %#v, want %#v", required, wantRequired)
		}
	}

	optional, err := QueryRetrieveOptionalKeys(QueryRetrieveModelPatientRoot, QueryRetrieveLevelStudy)
	if err != nil {
		t.Fatalf("QueryRetrieveOptionalKeys(PatientRoot, STUDY) error = %v", err)
	}
	if len(optional) == 0 {
		t.Fatalf("optional keys should not be empty")
	}
}
