package main

import (
	"bufio"
	"strings"
	"testing"

	uiddict "github.com/ThalesMMS/dicom-go/dictionary/uid"
)

func TestParseUIDTableSkipsCommentsAndParsesEntries(t *testing.T) {
	input := strings.Join([]string{
		"# comment",
		"",
		"1.2.840.10008.1.1\tVerification SOP Class\tVerification\tSOP Class\tfalse",
		"1.2.840.10008.1.2.2 \x00\tExplicit VR Big Endian (Retired)\tExplicitVRBigEndian\tTransfer Syntax\ttrue",
	}, "\n")

	entries, err := parseUIDTable(bufio.NewScanner(strings.NewReader(input)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}

	if entries[0].UID != "1.2.840.10008.1.1" {
		t.Fatalf("UID = %q, want %q", entries[0].UID, "1.2.840.10008.1.1")
	}
	if entries[0].Type != uiddict.SOPClass || entries[0].TypeExpr != "SOPClass" {
		t.Fatalf("first type = %#v / %q, want SOPClass", entries[0].Type, entries[0].TypeExpr)
	}
	if !entries[1].Retired {
		t.Fatal("second entry retired = false, want true")
	}
	if entries[1].UID != "1.2.840.10008.1.2.2" {
		t.Fatalf("normalized UID = %q, want %q", entries[1].UID, "1.2.840.10008.1.2.2")
	}
}

func TestParseLineRejectsInvalidRows(t *testing.T) {
	tests := []string{
		"1.2\tName\tKeyword\tTransfer Syntax",
		"\tName\tKeyword\tTransfer Syntax\tfalse",
		"1.2\t\tKeyword\tTransfer Syntax\tfalse",
		"1.2\tName\t\tTransfer Syntax\tfalse",
		"1.2\tName\tKeyword\tUnknown Type\tfalse",
		"1.2\tName\tKeyword\tTransfer Syntax\tmaybe",
	}

	for _, input := range tests {
		if _, err := parseLine(input); err == nil {
			t.Fatalf("parseLine(%q) succeeded, want error", input)
		}
	}
}
