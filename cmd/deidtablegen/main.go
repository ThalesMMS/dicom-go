// Command deidtablegen projects normative DICOM de-identification tables and
// code mappings into the compact data sets used by package deid.
//
// The input is a local copy of the official XHTML. The command deliberately
// has no network client: updating the generated table is a separate, auditable
// step that requires an explicitly supplied source file.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	standardVersion           = "PS3.15 2026b"
	sourceURL                 = "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part15/chapter_E.html"
	expectedProjectionSHA256  = "5feabf5ab985707bdf299b3d6ea253859be230ee54faed25ad3dbd1a9df98fc3"
	expectedRowCount          = 655
	expectedSourceColumnCount = 15
	actionColumnCount         = 11
)

type projectedRow struct {
	Pattern string
	Actions [actionColumnCount]string
}

func main() {
	tableName := flag.String("table", "e1", "table projection to generate: e1, e34, or retired")
	inputPath := flag.String("in", "", "path to the primary official DICOM 2026b XHTML source page")
	structuredInputPath := flag.String("structured-in", "", "path to the official PS3.15 2026b Table E.3.4-1 XHTML source page")
	outputPath := flag.String("out", "", "path to generated Go source")
	flag.Parse()

	if *inputPath == "" {
		fatalf("-in is required")
	}
	input, err := os.Open(*inputPath)
	if err != nil {
		fatalf("open input: %v", err)
	}
	defer input.Close()

	var source []byte
	var outputDefault string
	var rows []projectedRow
	switch *tableName {
	case "e1":
		rows, err = parseTable(input)
		if err != nil {
			fatalf("parse Table E.1-1: %v", err)
		}
		if len(rows) != expectedRowCount {
			fatalf("Table E.1-1 has %d projected rows, want %d", len(rows), expectedRowCount)
		}
		digest := projectionSHA256(rows)
		if digest != expectedProjectionSHA256 {
			fatalf("canonical projection SHA-256 is %s, want %s", digest, expectedProjectionSHA256)
		}
		source, err = render(rows)
		outputDefault = "deid/table_e1_generated.go"
	case "e34":
		rows, parseErr := parseStructuredContentTable(input)
		if parseErr != nil {
			fatalf("parse Table E.3.4-1: %v", parseErr)
		}
		if len(rows) != expectedStructuredContentRowCount {
			fatalf("Table E.3.4-1 has %d projected rows, want %d", len(rows), expectedStructuredContentRowCount)
		}
		digest := structuredContentProjectionSHA256(rows)
		if digest != expectedStructuredContentSHA256 {
			fatalf("canonical structured-content projection SHA-256 is %s, want %s", digest, expectedStructuredContentSHA256)
		}
		source, err = renderStructuredContent(rows)
		outputDefault = "deid/table_e34_generated.go"
	case "retired":
		if *structuredInputPath == "" {
			fatalf("-structured-in is required for -table retired")
		}
		structuredInput, openErr := os.Open(*structuredInputPath)
		if openErr != nil {
			fatalf("open structured-content input: %v", openErr)
		}
		defer structuredInput.Close()
		mappings, parseErr := parseSNOMEDMappingTable(input)
		if parseErr != nil {
			fatalf("parse Part 16 Table O-1: %v", parseErr)
		}
		structuredRows, parseErr := parseStructuredContentTable(structuredInput)
		if parseErr != nil {
			fatalf("parse Part 15 Table E.3.4-1: %v", parseErr)
		}
		aliases, projectErr := projectRetiredAliases(mappings, structuredRows)
		if projectErr != nil {
			fatalf("project retired code aliases: %v", projectErr)
		}
		if len(aliases) != expectedRetiredAliasRowCount {
			fatalf("retired alias projection has %d rows, want %d", len(aliases), expectedRetiredAliasRowCount)
		}
		digest := retiredAliasProjectionSHA256(aliases)
		if digest != expectedRetiredAliasSHA256 {
			fatalf("canonical retired alias projection SHA-256 is %s, want %s", digest, expectedRetiredAliasSHA256)
		}
		source, err = renderRetiredAliases(aliases)
		outputDefault = "deid/table_retired_generated.go"
	default:
		fatalf("unsupported -table %q (want e1, e34, or retired)", *tableName)
	}
	if err != nil {
		fatalf("render generated source: %v", err)
	}
	if *outputPath == "" {
		*outputPath = outputDefault
	}
	if err := os.WriteFile(*outputPath, source, 0o644); err != nil {
		fatalf("write output: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "deidtablegen: "+format+"\n", args...)
	os.Exit(1)
}

func parseTable(r io.Reader) ([]projectedRow, error) {
	decoder := xml.NewDecoder(r)
	anchorSeen := false
	inTable := false
	tableDepth := 0
	inBody := false
	bodyDepth := 0
	inRow := false
	inCell := false
	var cellText strings.Builder
	var cells []string
	var rows []projectedRow

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode XHTML: %w", err)
		}

		switch token := token.(type) {
		case xml.StartElement:
			if !anchorSeen && token.Name.Local == "a" && attribute(token.Attr, "id") == "table_E.1-1" {
				anchorSeen = true
				continue
			}
			if anchorSeen && !inTable && token.Name.Local == "table" {
				inTable = true
				tableDepth = 1
				continue
			}
			if !inTable {
				continue
			}
			if token.Name.Local == "table" {
				tableDepth++
			}
			if token.Name.Local == "tbody" && tableDepth == 1 {
				inBody = true
				bodyDepth = 1
				continue
			}
			if inBody && token.Name.Local == "tbody" {
				bodyDepth++
			}
			if inBody && bodyDepth == 1 && token.Name.Local == "tr" {
				inRow = true
				cells = cells[:0]
				continue
			}
			if inRow && token.Name.Local == "td" {
				if inCell {
					return nil, fmt.Errorf("nested table cell in row %d", len(rows)+1)
				}
				inCell = true
				cellText.Reset()
			}
		case xml.CharData:
			if inCell {
				cellText.Write(token)
			}
		case xml.EndElement:
			if !inTable {
				continue
			}
			if inRow && inCell && token.Name.Local == "td" {
				cells = append(cells, normalizeText(cellText.String()))
				inCell = false
				continue
			}
			if inBody && bodyDepth == 1 && inRow && token.Name.Local == "tr" {
				row, err := projectRow(cells, len(rows)+1)
				if err != nil {
					return nil, err
				}
				rows = append(rows, row)
				inRow = false
				continue
			}
			if inBody && token.Name.Local == "tbody" {
				bodyDepth--
				if bodyDepth == 0 {
					inBody = false
				}
				continue
			}
			if token.Name.Local == "table" {
				tableDepth--
				if tableDepth == 0 {
					inTable = false
				}
			}
		}
	}

	if !anchorSeen {
		return nil, fmt.Errorf("anchor table_E.1-1 not found")
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("Table E.1-1 body has no rows")
	}
	return rows, nil
}

func attribute(attributes []xml.Attr, name string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func projectRow(cells []string, rowNumber int) (projectedRow, error) {
	if len(cells) != expectedSourceColumnCount {
		return projectedRow{}, fmt.Errorf("row %d has %d columns, want %d", rowNumber, len(cells), expectedSourceColumnCount)
	}
	row := projectedRow{Pattern: cells[1]}
	if row.Pattern == "" {
		return projectedRow{}, fmt.Errorf("row %d has an empty Tag column", rowNumber)
	}
	copy(row.Actions[:], cells[4:])
	return row, nil
}

// canonicalProjection serializes each row as its Tag followed by the Basic
// Profile and ten option action cells, separated by tabs and terminated by LF.
// Attribute names and informational columns are deliberately excluded.
func canonicalProjection(rows []projectedRow) []byte {
	var projection bytes.Buffer
	for _, row := range rows {
		projection.WriteString(row.Pattern)
		for _, action := range row.Actions {
			projection.WriteByte('\t')
			projection.WriteString(action)
		}
		projection.WriteByte('\n')
	}
	return projection.Bytes()
}

func projectionSHA256(rows []projectedRow) string {
	digest := sha256.Sum256(canonicalProjection(rows))
	return hex.EncodeToString(digest[:])
}

func render(rows []projectedRow) ([]byte, error) {
	var source bytes.Buffer
	source.WriteString("// Code generated by cmd/deidtablegen from DICOM PS3.15 2026b Table E.1-1; DO NOT EDIT.\n")
	source.WriteString("\npackage deid\n\n")
	source.WriteString("const (\n")
	fmt.Fprintf(&source, "\tGeneratedProfileStandardVersion = %s\n", strconv.Quote(standardVersion))
	fmt.Fprintf(&source, "\tGeneratedProfileSourceURL = %s\n", strconv.Quote(sourceURL))
	fmt.Fprintf(&source, "\tGeneratedProfileProjectionSHA256 = %s\n", strconv.Quote(expectedProjectionSHA256))
	fmt.Fprintf(&source, "\tGeneratedProfileRowCount = %d\n", expectedRowCount)
	source.WriteString(")\n\n")
	source.WriteString("const (\n")
	source.WriteString("\tgeneratedProfileStandardVersion = GeneratedProfileStandardVersion\n")
	source.WriteString("\tgeneratedProfileSourceURL = GeneratedProfileSourceURL\n")
	source.WriteString("\tgeneratedProfileProjectionSHA256 = GeneratedProfileProjectionSHA256\n")
	source.WriteString("\tgeneratedProfileRowCount = GeneratedProfileRowCount\n")
	source.WriteString(")\n\n")
	source.WriteString("var generatedProfileRules = [...]generatedProfileRule{\n")
	for _, row := range rows {
		fmt.Fprintf(&source, "\t{Pattern: %s, Actions: [11]ActionCode{", strconv.Quote(row.Pattern))
		for index, action := range row.Actions {
			if index != 0 {
				source.WriteString(", ")
			}
			source.WriteString(strconv.Quote(action))
		}
		source.WriteString("}},\n")
	}
	source.WriteString("}\n")

	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt: %w", err)
	}
	return formatted, nil
}
