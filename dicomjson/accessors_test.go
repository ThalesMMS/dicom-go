package dicomjson

import "testing"

func TestElementStringsExtractsTextNumbersAndPersonNames(t *testing.T) {
	dataset := Dataset{
		"00100010": {VR: "PN", Value: []any{map[string]any{"Alphabetic": "DOE^JANE"}}},
		"00201208": {VR: "IS", Value: []any{float64(7)}},
		"00080061": {VR: "CS", Value: []any{"CT", "MR"}},
	}

	if got := ElementString(dataset, "00100010"); got != "DOE^JANE" {
		t.Fatalf("ElementString(PN) = %q", got)
	}
	if got := ElementString(dataset, "00201208"); got != "7" {
		t.Fatalf("ElementString(number) = %q", got)
	}
	got := ElementStrings(dataset, "00080061")
	if len(got) != 2 || got[0] != "CT" || got[1] != "MR" {
		t.Fatalf("ElementStrings(multivalue) = %#v", got)
	}
}

func TestElementStringsNormalizesTagLookupAndSkipsBlankValues(t *testing.T) {
	dataset := Dataset{
		"00080050": {VR: "SH", Value: []any{"", " ACC-1 "}},
	}

	got := ElementStrings(dataset, "00080050")
	if len(got) != 1 || got[0] != "ACC-1" {
		t.Fatalf("ElementStrings() = %#v, want [ACC-1]", got)
	}
}
