package dicomweb

import "testing"

func TestSeriesSearchParamsBuildsQIDOSeriesSearch(t *testing.T) {
	params, err := SeriesSearchParams(SeriesSearchCriteria{
		SeriesInstanceUID:  "1.2.3.4",
		Modality:           "CT",
		SeriesNumber:       "7",
		SeriesDescription:  "Chest",
		SeriesDateFrom:     "20260101",
		SeriesDateTo:       "20260131",
		SeriesTimeFrom:     "090000",
		SeriesTimeTo:       "170000",
		CustomFieldKeyword: "0008103E",
		CustomFieldValue:   "CUSTOM",
		Limit:              25,
	})
	if err != nil {
		t.Fatalf("SeriesSearchParams() error = %v", err)
	}
	if got := params.Get("SeriesInstanceUID"); got != "1.2.3.4" {
		t.Fatalf("SeriesInstanceUID = %q", got)
	}
	if got := params.Get("Modality"); got != "CT" {
		t.Fatalf("Modality = %q", got)
	}
	if got := params.Get("SeriesNumber"); got != "7" {
		t.Fatalf("SeriesNumber = %q", got)
	}
	if got := params.Get("SeriesDescription"); got != "Chest" {
		t.Fatalf("SeriesDescription = %q", got)
	}
	if got := params.Get("SeriesDate"); got != "20260101-20260131" {
		t.Fatalf("SeriesDate = %q", got)
	}
	if got := params.Get("SeriesTime"); got != "090000-170000" {
		t.Fatalf("SeriesTime = %q", got)
	}
	if got := params.Get("0008103E"); got != "CUSTOM" {
		t.Fatalf("custom field = %q", got)
	}
	if got := params.Get("limit"); got != "25" {
		t.Fatalf("limit = %q", got)
	}
	if got, want := params["includefield"], SeriesSearchReturnFields(); len(got) != len(want) {
		t.Fatalf("includefield count = %d, want %d (%v)", len(got), len(want), got)
	}
}

func TestSeriesSearchParamsRejectsCustomValueWithoutKeyword(t *testing.T) {
	_, err := SeriesSearchParams(SeriesSearchCriteria{CustomFieldValue: "CUSTOM"})
	if err == nil {
		t.Fatal("SeriesSearchParams() error = nil, want missing custom keyword error")
	}
}

func TestSeriesMatchesFromDatasetsExtractsQIDOSeriesFields(t *testing.T) {
	matches := SeriesMatchesFromDatasets([]Dataset{{
		"00080021": {VR: "DA", Value: []any{"20260617"}},
		"00080031": {VR: "TM", Value: []any{"101530"}},
		"00080060": {VR: "CS", Value: []any{"CT"}},
		"0008103E": {VR: "LO", Value: []any{"Chest"}},
		"0020000D": {VR: "UI", Value: []any{"1.2.3"}},
		"0020000E": {VR: "UI", Value: []any{"1.2.3.4"}},
		"00200011": {VR: "IS", Value: []any{float64(7)}},
		"00201209": {VR: "IS", Value: []any{float64(42)}},
	}})

	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	match := matches[0]
	if match.StudyInstanceUID != "1.2.3" || match.SeriesInstanceUID != "1.2.3.4" || match.Modality != "CT" {
		t.Fatalf("match identifiers = %+v", match)
	}
	if match.SeriesNumber != "7" || match.SeriesDescription != "Chest" {
		t.Fatalf("series text = %+v", match)
	}
	if match.SeriesDate != "20260617" || match.SeriesTime != "101530" || match.InstanceCount != "42" {
		t.Fatalf("date/time/count = %+v", match)
	}
}
