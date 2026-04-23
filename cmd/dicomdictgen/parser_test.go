package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestParseDictionarySkipsCommentsAndUnsupportedRows(t *testing.T) {
	input := strings.Join([]string{
		"# comment",
		"(0010,0010)\tPN\tPatientName\t1\tDICOM",
		"(0009-o-FFFF,0010-u-00FF)\tLO\tPrivateCreator\t1\tPRIVATE",
		"(0001-o-0007,0000)\tUL\tIllegalGroupLength\t1\tILLEGAL",
		"(0000-u-FFFF,0000)\tUL\tGenericGroupLength\t1\tGENERIC",
		"(FFFE,E000)\tna\tItem\t1\tDICOM",
	}, "\n")

	entries, err := parseDictionary(bufio.NewScanner(strings.NewReader(input)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}

	entry := entries[0]
	if entry.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("tag = %v, want %v", entry.Tag, core.NewTag(0x0010, 0x0010))
	}
	if entry.VRExpr != "core.VRPN" {
		t.Fatalf("VRExpr = %q, want %q", entry.VRExpr, "core.VRPN")
	}
	if entry.Keyword != "PatientName" {
		t.Fatalf("keyword = %q, want %q", entry.Keyword, "PatientName")
	}
	if entry.Name != "Patient Name" {
		t.Fatalf("name = %q, want %q", entry.Name, "Patient Name")
	}
	if entry.Retired {
		t.Fatal("retired = true, want false")
	}
}

func TestParseDictionaryMarksRetiredEntriesAndStripsPrefix(t *testing.T) {
	input := "(0008,0010)\tSH\tRETIRED_RecognitionCode\t1\tDICOM/retired\n"

	entries, err := parseDictionary(bufio.NewScanner(strings.NewReader(input)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}

	entry := entries[0]
	if entry.Keyword != "RecognitionCode" {
		t.Fatalf("keyword = %q, want %q", entry.Keyword, "RecognitionCode")
	}
	if entry.Name != "Recognition Code" {
		t.Fatalf("name = %q, want %q", entry.Name, "Recognition Code")
	}
	if !entry.Retired {
		t.Fatal("retired = false, want true")
	}
}

func TestParseDictionaryExpandsRangesAndSortsDeterministically(t *testing.T) {
	input := strings.Join([]string{
		"(6000-6004,3000)\tox\tOverlayData\t1\tDICOM",
		"(0020,3100-3104)\tCS\tRETIRED_SourceImageIDs\t1-n\tDICOM/retired",
	}, "\n")

	entries, err := parseDictionary(bufio.NewScanner(strings.NewReader(input)))
	if err != nil {
		t.Fatal(err)
	}

	var got []core.Tag
	for _, entry := range entries {
		got = append(got, entry.Tag)
	}

	want := []core.Tag{
		core.NewTag(0x0020, 0x3100),
		core.NewTag(0x0020, 0x3102),
		core.NewTag(0x0020, 0x3104),
		core.NewTag(0x6000, 0x3000),
		core.NewTag(0x6002, 0x3000),
		core.NewTag(0x6004, 0x3000),
	}
	if len(got) != len(want) {
		t.Fatalf("tag count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tag[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	if entries[0].Keyword != "SourceImageIDs" || !entries[0].Retired {
		t.Fatalf("first entry = %#v, want retired SourceImageIDs", entries[0])
	}
	if entries[len(entries)-1].VRExpr != "core.VROW" {
		t.Fatalf("last entry VRExpr = %q, want %q", entries[len(entries)-1].VRExpr, "core.VROW")
	}
}

func TestParseDictionaryPrefersExactNonRetiredEntryOverOverlappingRange(t *testing.T) {
	input := strings.Join([]string{
		"(7FE0,0010)\tpx\tPixelData\t1\tDICOM",
		"(7F00-7FFF,0010)\tox\tRETIRED_VariablePixelData\t1\tDICOM/retired",
	}, "\n")

	entries, err := parseDictionary(bufio.NewScanner(strings.NewReader(input)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 128 {
		t.Fatalf("entry count = %d, want %d", len(entries), 128)
	}

	var pixelData parsedEntry
	found := false
	for _, entry := range entries {
		if entry.Tag == core.NewTag(0x7FE0, 0x0010) {
			pixelData = entry
			found = true
			break
		}
	}
	if !found {
		t.Fatal("did not find concrete PixelData entry")
	}
	if pixelData.Keyword != "PixelData" {
		t.Fatalf("keyword = %q, want %q", pixelData.Keyword, "PixelData")
	}
	if pixelData.Retired {
		t.Fatal("retired = true, want false")
	}
}

func TestKeywordToName(t *testing.T) {
	tests := map[string]string{
		"PatientName":                 "Patient Name",
		"RTVCommunicationSOPClassUID": "RTV Communication SOP Class UID",
		"MRDRDirectoryRecordOffset":   "MRDR Directory Record Offset",
		"LUTData":                     "LUT Data",
	}

	for keyword, want := range tests {
		if got := keywordToName(keyword); got != want {
			t.Fatalf("keywordToName(%q) = %q, want %q", keyword, got, want)
		}
	}
}
