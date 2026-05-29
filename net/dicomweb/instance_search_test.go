package dicomweb

import "testing"

func TestInstanceSearchParamsBuildsQIDOInstanceSearch(t *testing.T) {
	params, err := InstanceSearchParams(InstanceSearchCriteria{
		SOPInstanceUID:     "1.2.3.4.5",
		SOPClassUID:        "1.2.840.10008.5.1.4.1.1.2",
		InstanceNumber:     "7",
		Modality:           "CT",
		CustomFieldKeyword: "00080018",
		CustomFieldValue:   "CUSTOM",
		Limit:              25,
	})
	if err != nil {
		t.Fatalf("InstanceSearchParams() error = %v", err)
	}
	if got := params.Get("SOPInstanceUID"); got != "1.2.3.4.5" {
		t.Fatalf("SOPInstanceUID = %q", got)
	}
	if got := params.Get("SOPClassUID"); got != "1.2.840.10008.5.1.4.1.1.2" {
		t.Fatalf("SOPClassUID = %q", got)
	}
	if got := params.Get("InstanceNumber"); got != "7" {
		t.Fatalf("InstanceNumber = %q", got)
	}
	if got := params.Get("Modality"); got != "CT" {
		t.Fatalf("Modality = %q", got)
	}
	if got := params.Get("00080018"); got != "CUSTOM" {
		t.Fatalf("custom field = %q", got)
	}
	if got := params.Get("limit"); got != "25" {
		t.Fatalf("limit = %q", got)
	}
	if got, want := params["includefield"], InstanceSearchReturnFields(); len(got) != len(want) {
		t.Fatalf("includefield count = %d, want %d (%v)", len(got), len(want), got)
	}
}

func TestInstanceSearchParamsRejectsCustomValueWithoutKeyword(t *testing.T) {
	_, err := InstanceSearchParams(InstanceSearchCriteria{CustomFieldValue: "CUSTOM"})
	if err == nil {
		t.Fatal("InstanceSearchParams() error = nil, want missing custom keyword error")
	}
}

func TestInstanceMatchesFromDatasetsExtractsQIDOInstanceFields(t *testing.T) {
	matches := InstanceMatchesFromDatasets([]Dataset{{
		"00080016": {VR: "UI", Value: []any{"1.2.840.10008.5.1.4.1.1.2"}},
		"00080018": {VR: "UI", Value: []any{"1.2.3.4.5"}},
		"00080060": {VR: "CS", Value: []any{"CT"}},
		"00100010": {VR: "PN", Value: []any{map[string]any{"Alphabetic": "DOE^JANE"}}},
		"00100020": {VR: "LO", Value: []any{"P123"}},
		"0020000D": {VR: "UI", Value: []any{"1.2.3"}},
		"0020000E": {VR: "UI", Value: []any{"1.2.3.4"}},
		"00200013": {VR: "IS", Value: []any{float64(7)}},
	}})

	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	match := matches[0]
	if match.SOPInstanceUID != "1.2.3.4.5" || match.SOPClassUID != "1.2.840.10008.5.1.4.1.1.2" {
		t.Fatalf("SOP identifiers = %+v", match)
	}
	if match.InstanceNumber != "7" || match.Modality != "CT" {
		t.Fatalf("instance fields = %+v", match)
	}
	if match.PatientName != "DOE^JANE" || match.PatientID != "P123" {
		t.Fatalf("patient context = %+v", match)
	}
	if match.StudyInstanceUID != "1.2.3" || match.SeriesInstanceUID != "1.2.3.4" {
		t.Fatalf("study/series context = %+v", match)
	}
}
