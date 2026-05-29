package main

import (
	"strings"
	"testing"
)

func TestParseSNOMEDMappingTable(t *testing.T) {
	const input = `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body>
<a id="other"></a><table><tbody><tr><td>wrong</td></tr></tbody></table>
<a id="table_O-1"></a><table><thead><tr><th>current</th></tr></thead><tbody>
<tr><td> 371524004 </td><td> D3-13000 </td><td> Clinical <strong>report</strong> </td></tr>
</tbody></table></body></html>`

	rows, err := parseSNOMEDMappingTable(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseSNOMEDMappingTable() error = %v", err)
	}
	want := []snomedMappingRow{{CurrentCodeValue: "371524004", RetiredCodeValue: "D3-13000"}}
	if len(rows) != 1 || rows[0] != want[0] {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

func TestProjectRetiredAliasesFiltersCurrentStructuredContent(t *testing.T) {
	mappings := []snomedMappingRow{
		{CurrentCodeValue: "371524004", RetiredCodeValue: "D3-13000"},
		{CurrentCodeValue: "999999999", RetiredCodeValue: "D9-99999"},
	}
	structured := []structuredContentRow{
		{CodeValue: "371524004", CodingSchemeDesignator: "SCT", ValueType: "TEXT"},
		{CodeValue: "371524004", CodingSchemeDesignator: "SCT", ValueType: "COMPOSITE"},
		{CodeValue: "121022", CodingSchemeDesignator: "DCM", ValueType: "TEXT"},
	}

	aliases, err := projectRetiredAliases(mappings, structured)
	if err != nil {
		t.Fatalf("projectRetiredAliases() error = %v", err)
	}
	want := []retiredAliasRow{
		{OldCodeValue: "D3-13000", OldCodingSchemeDesignator: "99SDM", CurrentCodeValue: "371524004", CurrentCodingSchemeDesignator: "SCT"},
		{OldCodeValue: "D3-13000", OldCodingSchemeDesignator: "SNM3", CurrentCodeValue: "371524004", CurrentCodingSchemeDesignator: "SCT"},
		{OldCodeValue: "D3-13000", OldCodingSchemeDesignator: "SRT", CurrentCodeValue: "371524004", CurrentCodingSchemeDesignator: "SCT"},
	}
	if len(aliases) != len(want) {
		t.Fatalf("len(aliases) = %d, want %d: %#v", len(aliases), len(want), aliases)
	}
	for index := range want {
		if aliases[index] != want[index] {
			t.Fatalf("aliases[%d] = %#v, want %#v", index, aliases[index], want[index])
		}
	}
	if got := string(canonicalRetiredAliasProjection(aliases)); got != "D3-13000\t99SDM\t371524004\tSCT\nD3-13000\tSNM3\t371524004\tSCT\nD3-13000\tSRT\t371524004\tSCT\n" {
		t.Fatalf("canonicalRetiredAliasProjection() = %q", got)
	}
}

func TestProjectRetiredAliasesRejectsAmbiguousOldCode(t *testing.T) {
	mappings := []snomedMappingRow{
		{CurrentCodeValue: "111", RetiredCodeValue: "D3-13000"},
		{CurrentCodeValue: "222", RetiredCodeValue: "D3-13000"},
	}
	structured := []structuredContentRow{
		{CodeValue: "111", CodingSchemeDesignator: "SCT", ValueType: "TEXT"},
		{CodeValue: "222", CodingSchemeDesignator: "SCT", ValueType: "TEXT"},
	}
	_, err := projectRetiredAliases(mappings, structured)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("projectRetiredAliases() error = %v, want ambiguity", err)
	}
}

func TestParseSNOMEDMappingTableRejectsChangedShape(t *testing.T) {
	const input = `<html><body><a id="table_O-1"></a><table><tbody><tr><td>371524004</td><td>D3-13000</td></tr></tbody></table></body></html>`
	_, err := parseSNOMEDMappingTable(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "3 columns") {
		t.Fatalf("parseSNOMEDMappingTable() error = %v, want shape error", err)
	}
}
