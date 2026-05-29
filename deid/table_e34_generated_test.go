package deid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestGeneratedStructuredContentTableProvenance(t *testing.T) {
	if GeneratedStructuredContentStandardVersion != "PS3.15 2026b" {
		t.Fatalf("standard version = %q", GeneratedStructuredContentStandardVersion)
	}
	if GeneratedStructuredContentSourceURL != "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part15/sect_E.3.4.html" {
		t.Fatalf("source URL = %q", GeneratedStructuredContentSourceURL)
	}
	if GeneratedStructuredContentRowCount != 211 {
		t.Fatalf("declared row count = %d", GeneratedStructuredContentRowCount)
	}
	if got := len(generatedStructuredContentRules); got != GeneratedStructuredContentRowCount {
		t.Fatalf("generated row count = %d, want %d", got, GeneratedStructuredContentRowCount)
	}
	const expectedChecksum = "81d2e89aa42dc2d3331aad70cc03b71b21e9a79c54edfe759cf68e6504430c69"
	if GeneratedStructuredContentProjectionSHA256 != expectedChecksum {
		t.Fatalf("declared projection checksum = %q", GeneratedStructuredContentProjectionSHA256)
	}
	if got := computeGeneratedStructuredContentProjectionSHA256(); got != expectedChecksum {
		t.Fatalf("computed projection checksum = %s, want %s", got, expectedChecksum)
	}
}

func TestGeneratedStructuredContentKeysAreUnique(t *testing.T) {
	type key struct {
		codeValue              string
		codingSchemeDesignator string
		valueType              string
	}
	seen := make(map[key]int, len(generatedStructuredContentRules))
	for index, rule := range generatedStructuredContentRules {
		if rule.CodeValue == "" || rule.CodingSchemeDesignator == "" || rule.ValueType == "" {
			t.Fatalf("row %d has an empty key field: %#v", index+1, rule)
		}
		key := key{rule.CodeValue, rule.CodingSchemeDesignator, rule.ValueType}
		if previous, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate structured-content key at rows %d and %d: %#v", previous+1, index+1, key)
		}
		seen[key] = index
	}
}

func TestGeneratedStructuredContentActionCodes(t *testing.T) {
	allowed := map[ActionCode]bool{
		"":    true,
		"C":   true,
		"D":   true,
		"K":   true,
		"X":   true,
		"X/D": true,
	}
	for rowIndex, rule := range generatedStructuredContentRules {
		for actionIndex, action := range rule.Actions {
			if !allowed[action] {
				t.Fatalf("row %d action column %d has unsupported code %q", rowIndex+1, actionIndex+1, action)
			}
		}
	}
}

func TestGeneratedStructuredContentValueTypeDisambiguatesActions(t *testing.T) {
	image, ok := findGeneratedStructuredContentRule("121112", "DCM", "IMAGE")
	if !ok || image.Actions[0] != ActionCodeDummy {
		t.Fatalf("DCM 121112 IMAGE = %#v, %v; want Basic D", image, ok)
	}
	waveform, ok := findGeneratedStructuredContentRule("121112", "DCM", "WAVEFORM")
	if !ok || waveform.Actions[0] != ActionCodeRemove {
		t.Fatalf("DCM 121112 WAVEFORM = %#v, %v; want Basic X", waveform, ok)
	}
	composite, ok := findGeneratedStructuredContentRule("371524004", "SCT", "COMPOSITE")
	if !ok || composite.Actions[1] != ActionCodeKeep {
		t.Fatalf("SCT 371524004 COMPOSITE = %#v, %v; want Retain UIDs K", composite, ok)
	}
	text, ok := findGeneratedStructuredContentRule("371524004", "SCT", "TEXT")
	if !ok || text.Actions[7] != ActionCodeClean {
		t.Fatalf("SCT 371524004 TEXT = %#v, %v; want Clean Descriptors C", text, ok)
	}
}

func findGeneratedStructuredContentRule(codeValue, scheme, valueType string) (generatedStructuredContentRule, bool) {
	for _, rule := range generatedStructuredContentRules {
		if rule.CodeValue == codeValue && rule.CodingSchemeDesignator == scheme && rule.ValueType == valueType {
			return rule, true
		}
	}
	return generatedStructuredContentRule{}, false
}

func computeGeneratedStructuredContentProjectionSHA256() string {
	var projection bytes.Buffer
	for _, rule := range generatedStructuredContentRules {
		projection.WriteString(rule.CodeValue)
		projection.WriteByte('\t')
		projection.WriteString(rule.CodingSchemeDesignator)
		projection.WriteByte('\t')
		projection.WriteString(rule.ValueType)
		for _, action := range rule.Actions {
			projection.WriteByte('\t')
			projection.WriteString(string(action))
		}
		projection.WriteByte('\n')
	}
	digest := sha256.Sum256(projection.Bytes())
	return hex.EncodeToString(digest[:])
}
