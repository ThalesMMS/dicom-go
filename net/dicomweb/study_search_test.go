package dicomweb

import "testing"

func TestStudySearchParamsBuildsQIDOStudySearch(t *testing.T) {
	params, err := StudySearchParams(StudySearchCriteria{
		PatientName:        "DOE^JANE",
		AccessionNumber:    "ACC-1",
		StudyDateFrom:      "20260101",
		StudyDateTo:        "20260131",
		CustomFieldKeyword: "00080050",
		CustomFieldValue:   "CUSTOM",
		BodyPartExamined:   "CHEST",
		WorkListStatus:     "SCHEDULED",
		Limit:              25,
	})
	if err != nil {
		t.Fatalf("StudySearchParams() error = %v", err)
	}
	if got := params.Get("PatientName"); got != "DOE^JANE" {
		t.Fatalf("PatientName = %q", got)
	}
	if got := params.Get("StudyDate"); got != "20260101-20260131" {
		t.Fatalf("StudyDate = %q", got)
	}
	if got := params.Get("00080050"); got != "CUSTOM" {
		t.Fatalf("custom field = %q", got)
	}
	if got := params.Get("BodyPartExamined"); got != "CHEST" {
		t.Fatalf("BodyPartExamined = %q", got)
	}
	if got := params.Get("ScheduledProcedureStepStatus"); got != "SCHEDULED" {
		t.Fatalf("ScheduledProcedureStepStatus = %q", got)
	}
	if got := params.Get("limit"); got != "25" {
		t.Fatalf("limit = %q", got)
	}
	if got := params["includefield"]; len(got) == 0 {
		t.Fatal("StudySearchParams() did not include return fields")
	}
}

func TestStudySearchParamsRejectsCustomValueWithoutKeyword(t *testing.T) {
	_, err := StudySearchParams(StudySearchCriteria{CustomFieldValue: "CUSTOM"})
	if err == nil {
		t.Fatal("StudySearchParams() error = nil, want missing custom keyword error")
	}
}

func TestStudyMatchesFromDatasetsExtractsQIDOStudyFields(t *testing.T) {
	matches := StudyMatchesFromDatasets([]Dataset{{
		"00080020": {VR: "DA", Value: []any{"20260617"}},
		"00080030": {VR: "TM", Value: []any{"101530"}},
		"00080050": {VR: "SH", Value: []any{"ACC-1"}},
		"00080061": {VR: "CS", Value: []any{"CT", "MR"}},
		"00081030": {VR: "LO", Value: []any{"Chest CT"}},
		"00100010": {VR: "PN", Value: []any{map[string]any{"Alphabetic": "DOE^JANE"}}},
		"00100020": {VR: "LO", Value: []any{"P123"}},
		"0020000D": {VR: "UI", Value: []any{"1.2.3.4"}},
		"00201208": {VR: "IS", Value: []any{float64(7)}},
		"00180015": {VR: "CS", Value: []any{"CHEST"}},
		"00400020": {VR: "CS", Value: []any{"SCHEDULED"}},
	}})

	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	match := matches[0]
	if match.PatientName != "DOE^JANE" || match.PatientID != "P123" || match.StudyInstanceUID != "1.2.3.4" {
		t.Fatalf("match identifiers = %+v", match)
	}
	if match.Modalities != "CT\\MR" || match.ImageCount != "7" {
		t.Fatalf("modalities/image count = %q/%q", match.Modalities, match.ImageCount)
	}
	if match.BodyPartExamined != "CHEST" || match.WorkListStatus != "SCHEDULED" {
		t.Fatalf("body part/worklist status = %q/%q", match.BodyPartExamined, match.WorkListStatus)
	}
}
