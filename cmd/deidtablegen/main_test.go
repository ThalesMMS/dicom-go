package main

import (
	"strings"
	"testing"
)

func TestParseTableProjectsOnlyNormativeActionColumns(t *testing.T) {
	const input = `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body>
<a id="table_E.1-1a"></a><table><tbody><tr><td>wrong table</td></tr></tbody></table>
<a id="table_E.1-1"></a><p>title</p><table><thead><tr><th>header</th></tr></thead><tbody>
<tr><td> Attribute <strong>Name</strong> </td><td> (60xx,3000/4000) </td><td>N</td><td>Y</td>
<td>X/Z</td><td></td><td>U</td><td>D</td><td>K</td><td>C</td><td>X</td><td>Z</td><td>X/D</td><td>X/Z/D</td><td>X/Z/U*</td></tr>
</tbody></table></body></html>`

	rows, err := parseTable(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseTable() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	want := projectedRow{
		Pattern: "(60xx,3000/4000)",
		Actions: [11]string{"X/Z", "", "U", "D", "K", "C", "X", "Z", "X/D", "X/Z/D", "X/Z/U*"},
	}
	if rows[0] != want {
		t.Fatalf("row = %#v, want %#v", rows[0], want)
	}
	if got := string(canonicalProjection(rows)); got != "(60xx,3000/4000)\tX/Z\t\tU\tD\tK\tC\tX\tZ\tX/D\tX/Z/D\tX/Z/U*\n" {
		t.Fatalf("canonicalProjection() = %q", got)
	}
}

func TestParseTableRejectsChangedShape(t *testing.T) {
	const input = `<html><body><a id="table_E.1-1"></a><table><tbody><tr><td>name</td><td>(0008,0050)</td></tr></tbody></table></body></html>`
	_, err := parseTable(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "15") {
		t.Fatalf("parseTable() error = %v, want column count error", err)
	}
}

func TestParseTableRequiresExactAnchor(t *testing.T) {
	const input = `<html><body><a id="table_E.1-1a"></a><table><tbody><tr><td>not target</td></tr></tbody></table></body></html>`
	_, err := parseTable(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("parseTable() error = %v, want missing anchor error", err)
	}
}

func TestParseStructuredContentTableProjectsCodesAndActions(t *testing.T) {
	const input = `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body>
<a id="table_E.1-1"></a><table><tbody><tr><td>wrong table</td></tr></tbody></table>
<a id="table_E.3.4-1"></a><p>title</p><table><thead><tr><th>header</th></tr></thead><tbody>
<tr><td> Observer <strong>Name</strong> </td><td> 121008 </td><td> DCM </td><td>PNAME</td><td>N</td><td>Y</td>
<td>X</td><td>U</td><td>K</td><td>C</td><td>Z</td><td>X/Z</td><td>X/D</td><td>X/Z/D</td></tr>
</tbody></table></body></html>`

	rows, err := parseStructuredContentTable(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseStructuredContentTable() error = %v", err)
	}
	want := structuredContentRow{
		CodeValue:              "121008",
		CodingSchemeDesignator: "DCM",
		ValueType:              "PNAME",
		Actions:                [8]string{"X", "U", "K", "C", "Z", "X/Z", "X/D", "X/Z/D"},
	}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("rows = %#v, want %#v", rows, []structuredContentRow{want})
	}
	if got := string(canonicalStructuredContentProjection(rows)); got != "121008\tDCM\tPNAME\tX\tU\tK\tC\tZ\tX/Z\tX/D\tX/Z/D\n" {
		t.Fatalf("canonicalStructuredContentProjection() = %q", got)
	}
}

func TestParseStructuredContentTableRejectsChangedShape(t *testing.T) {
	const input = `<html><body><a id="table_E.3.4-1"></a><table><tbody><tr><td>name</td><td>121008</td><td>DCM</td></tr></tbody></table></body></html>`
	_, err := parseStructuredContentTable(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "14") {
		t.Fatalf("parseStructuredContentTable() error = %v, want column count error", err)
	}
}
