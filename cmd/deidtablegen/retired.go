package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"go/format"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	retiredCodeStandardVersion            = "PS3.16 2026b"
	retiredCodeSourceURL                  = "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part16/chapter_O.html"
	retiredCodePolicySourceURL            = "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part16/sect_8.3.html"
	retiredCodeStructuredContentSourceURL = structuredContentSourceURL
	expectedRetiredAliasSHA256            = "6f7688d225e3e558e5279d3fba5e26176d207e332465ab0a9769cdf65bc5e6c1"
	expectedRetiredAliasRowCount          = 30
	snomedMappingSourceColumnCount        = 3
	currentCodingSchemeDesignator         = "SCT"
)

var retiredCodingSchemeDesignators = [...]string{"SRT", "SNM3", "99SDM"}

type snomedMappingRow struct {
	CurrentCodeValue string
	RetiredCodeValue string
}

type retiredAliasRow struct {
	OldCodeValue                  string
	OldCodingSchemeDesignator     string
	CurrentCodeValue              string
	CurrentCodingSchemeDesignator string
}

func parseSNOMEDMappingTable(r io.Reader) ([]snomedMappingRow, error) {
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
	var rows []snomedMappingRow

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
			if !anchorSeen && token.Name.Local == "a" && attribute(token.Attr, "id") == "table_O-1" {
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
				row, projectErr := projectSNOMEDMappingRow(cells, len(rows)+1)
				if projectErr != nil {
					return nil, projectErr
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
		return nil, fmt.Errorf("anchor table_O-1 not found")
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("Table O-1 body has no rows")
	}
	return rows, nil
}

func projectSNOMEDMappingRow(cells []string, rowNumber int) (snomedMappingRow, error) {
	if len(cells) != snomedMappingSourceColumnCount {
		return snomedMappingRow{}, fmt.Errorf("row %d has %d columns, want 3 columns", rowNumber, len(cells))
	}
	row := snomedMappingRow{CurrentCodeValue: cells[0], RetiredCodeValue: cells[1]}
	if row.CurrentCodeValue == "" || row.RetiredCodeValue == "" {
		return snomedMappingRow{}, fmt.Errorf("row %d has an empty current or retired code value", rowNumber)
	}
	return row, nil
}

func projectRetiredAliases(mappings []snomedMappingRow, structured []structuredContentRow) ([]retiredAliasRow, error) {
	currentStructuredCodes := make(map[string]struct{})
	for _, row := range structured {
		if row.CodingSchemeDesignator == currentCodingSchemeDesignator {
			currentStructuredCodes[row.CodeValue] = struct{}{}
		}
	}

	type oldKey struct {
		value  string
		scheme string
	}
	seen := make(map[oldKey]string)
	aliases := make([]retiredAliasRow, 0)
	for _, mapping := range mappings {
		if _, relevant := currentStructuredCodes[mapping.CurrentCodeValue]; !relevant {
			continue
		}
		for _, scheme := range retiredCodingSchemeDesignators {
			key := oldKey{value: mapping.RetiredCodeValue, scheme: scheme}
			if current, exists := seen[key]; exists {
				if current != mapping.CurrentCodeValue {
					return nil, fmt.Errorf("retired code mapping is ambiguous for one old code and scheme")
				}
				continue
			}
			seen[key] = mapping.CurrentCodeValue
			aliases = append(aliases, retiredAliasRow{
				OldCodeValue: mapping.RetiredCodeValue, OldCodingSchemeDesignator: scheme,
				CurrentCodeValue: mapping.CurrentCodeValue, CurrentCodingSchemeDesignator: currentCodingSchemeDesignator,
			})
		}
	}
	if len(aliases) == 0 {
		return nil, fmt.Errorf("no Annex O mappings intersect current E.3.4 SCT codes")
	}
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].OldCodeValue != aliases[j].OldCodeValue {
			return aliases[i].OldCodeValue < aliases[j].OldCodeValue
		}
		if aliases[i].OldCodingSchemeDesignator != aliases[j].OldCodingSchemeDesignator {
			return aliases[i].OldCodingSchemeDesignator < aliases[j].OldCodingSchemeDesignator
		}
		return aliases[i].CurrentCodeValue < aliases[j].CurrentCodeValue
	})
	return aliases, nil
}

func canonicalRetiredAliasProjection(rows []retiredAliasRow) []byte {
	var projection bytes.Buffer
	for _, row := range rows {
		projection.WriteString(row.OldCodeValue)
		projection.WriteByte('\t')
		projection.WriteString(row.OldCodingSchemeDesignator)
		projection.WriteByte('\t')
		projection.WriteString(row.CurrentCodeValue)
		projection.WriteByte('\t')
		projection.WriteString(row.CurrentCodingSchemeDesignator)
		projection.WriteByte('\n')
	}
	return projection.Bytes()
}

func retiredAliasProjectionSHA256(rows []retiredAliasRow) string {
	digest := sha256.Sum256(canonicalRetiredAliasProjection(rows))
	return hex.EncodeToString(digest[:])
}

func renderRetiredAliases(rows []retiredAliasRow) ([]byte, error) {
	checksum := retiredAliasProjectionSHA256(rows)
	var source bytes.Buffer
	source.WriteString("// Code generated by cmd/deidtablegen from DICOM PS3.16 2026b Annex O and Section 8.3; DO NOT EDIT.\n")
	source.WriteString("\npackage deid\n\n")
	source.WriteString("const (\n")
	fmt.Fprintf(&source, "\tGeneratedRetiredCodeStandardVersion = %s\n", strconv.Quote(retiredCodeStandardVersion))
	fmt.Fprintf(&source, "\tGeneratedRetiredCodeSourceURL = %s\n", strconv.Quote(retiredCodeSourceURL))
	fmt.Fprintf(&source, "\tGeneratedRetiredCodePolicySourceURL = %s\n", strconv.Quote(retiredCodePolicySourceURL))
	fmt.Fprintf(&source, "\tGeneratedRetiredCodeStructuredContentSourceURL = %s\n", strconv.Quote(retiredCodeStructuredContentSourceURL))
	fmt.Fprintf(&source, "\tGeneratedRetiredCodeProjectionSHA256 = %s\n", strconv.Quote(checksum))
	fmt.Fprintf(&source, "\tGeneratedRetiredCodeRowCount = %d\n", len(rows))
	source.WriteString(")\n\n")
	source.WriteString("type generatedRetiredCodeAlias struct {\n")
	source.WriteString("\tOldCodeValue string\n")
	source.WriteString("\tOldCodingSchemeDesignator string\n")
	source.WriteString("\tCurrentCodeValue string\n")
	source.WriteString("\tCurrentCodingSchemeDesignator string\n")
	source.WriteString("}\n\n")
	source.WriteString("var generatedRetiredCodeAliases = []generatedRetiredCodeAlias{\n")
	for _, row := range rows {
		fmt.Fprintf(&source, "\t{OldCodeValue: %s, OldCodingSchemeDesignator: %s, CurrentCodeValue: %s, CurrentCodingSchemeDesignator: %s},\n",
			strconv.Quote(row.OldCodeValue), strconv.Quote(row.OldCodingSchemeDesignator), strconv.Quote(row.CurrentCodeValue), strconv.Quote(row.CurrentCodingSchemeDesignator))
	}
	source.WriteString("}\n")

	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt: %w", err)
	}
	return formatted, nil
}
