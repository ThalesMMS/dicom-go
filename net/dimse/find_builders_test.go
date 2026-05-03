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

	if _, err := NewQueryRetrieveLevelElement("patient"); err == nil {
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

func TestBuildStudyRootFindKeysUnknownKeyword(t *testing.T) {
	if _, err := BuildStudyRootStudyFindKeys(map[string]string{"NotAKeyword": "x"}); err == nil {
		t.Fatalf("expected error")
	}
}
