package deid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

func TestGeneratedRetiredCodeTableProvenance(t *testing.T) {
	if GeneratedRetiredCodeStandardVersion != "PS3.16 2026b" {
		t.Fatalf("standard version = %q", GeneratedRetiredCodeStandardVersion)
	}
	if GeneratedRetiredCodeSourceURL != "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part16/chapter_O.html" {
		t.Fatalf("source URL = %q", GeneratedRetiredCodeSourceURL)
	}
	if GeneratedRetiredCodePolicySourceURL != "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part16/sect_8.3.html" {
		t.Fatalf("policy source URL = %q", GeneratedRetiredCodePolicySourceURL)
	}
	if GeneratedRetiredCodeStructuredContentSourceURL != GeneratedStructuredContentSourceURL {
		t.Fatalf("structured-content source URL = %q", GeneratedRetiredCodeStructuredContentSourceURL)
	}
	if GeneratedRetiredCodeRowCount != 30 || len(generatedRetiredCodeAliases) != GeneratedRetiredCodeRowCount {
		t.Fatalf("row counts = %d/%d, want 30", GeneratedRetiredCodeRowCount, len(generatedRetiredCodeAliases))
	}
	const expectedChecksum = "6f7688d225e3e558e5279d3fba5e26176d207e332465ab0a9769cdf65bc5e6c1"
	if GeneratedRetiredCodeProjectionSHA256 != expectedChecksum {
		t.Fatalf("declared checksum = %q", GeneratedRetiredCodeProjectionSHA256)
	}
	if got := computeGeneratedRetiredCodeProjectionSHA256(); got != expectedChecksum {
		t.Fatalf("computed checksum = %s, want %s", got, expectedChecksum)
	}
}

func TestGeneratedRetiredCodeAliasesAreUniqueAndCovered(t *testing.T) {
	type oldKey struct {
		value  string
		scheme string
	}
	seen := make(map[oldKey]struct{}, len(generatedRetiredCodeAliases))
	schemeCounts := map[string]int{"SRT": 0, "SNM3": 0, "99SDM": 0}
	for index, alias := range generatedRetiredCodeAliases {
		if alias.OldCodeValue == "" || alias.OldCodingSchemeDesignator == "" || alias.CurrentCodeValue == "" {
			t.Fatalf("row %d has an empty alias field: %#v", index+1, alias)
		}
		if alias.CurrentCodingSchemeDesignator != "SCT" {
			t.Fatalf("row %d current scheme = %q, want SCT", index+1, alias.CurrentCodingSchemeDesignator)
		}
		if _, ok := schemeCounts[alias.OldCodingSchemeDesignator]; !ok {
			t.Fatalf("row %d old scheme = %q", index+1, alias.OldCodingSchemeDesignator)
		}
		schemeCounts[alias.OldCodingSchemeDesignator]++
		key := oldKey{alias.OldCodeValue, alias.OldCodingSchemeDesignator}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate old code key at row %d: %#v", index+1, key)
		}
		seen[key] = struct{}{}
		if !generatedStructuredContentHasSCTCode(alias.CurrentCodeValue) {
			t.Fatalf("row %d target SCT code %q is absent from E.3.4", index+1, alias.CurrentCodeValue)
		}
	}
	for scheme, count := range schemeCounts {
		if count != 10 {
			t.Errorf("%s alias count = %d, want 10", scheme, count)
		}
	}
}

func TestGeneratedRetiredCodeAliasExample(t *testing.T) {
	for _, scheme := range []string{"SRT", "SNM3", "99SDM"} {
		alias, ok := findGeneratedRetiredCodeAlias("R-42B89", scheme)
		if !ok || alias.CurrentCodeValue != "371524004" || alias.CurrentCodingSchemeDesignator != "SCT" {
			t.Errorf("R-42B89/%s = %#v, %v", scheme, alias, ok)
		}
	}
}

func TestGeneratedRetiredCodeAliasCarriesNoMeaning(t *testing.T) {
	typeOfAlias := reflect.TypeOf(generatedRetiredCodeAlias{})
	wantFields := []string{"OldCodeValue", "OldCodingSchemeDesignator", "CurrentCodeValue", "CurrentCodingSchemeDesignator"}
	if typeOfAlias.NumField() != len(wantFields) {
		t.Fatalf("generatedRetiredCodeAlias field count = %d, want %d", typeOfAlias.NumField(), len(wantFields))
	}
	for index, want := range wantFields {
		if got := typeOfAlias.Field(index).Name; got != want {
			t.Fatalf("field %d = %q, want %q", index, got, want)
		}
	}
}

func generatedStructuredContentHasSCTCode(codeValue string) bool {
	for _, rule := range generatedStructuredContentRules {
		if rule.CodeValue == codeValue && rule.CodingSchemeDesignator == "SCT" {
			return true
		}
	}
	return false
}

func findGeneratedRetiredCodeAlias(oldValue, oldScheme string) (generatedRetiredCodeAlias, bool) {
	for _, alias := range generatedRetiredCodeAliases {
		if alias.OldCodeValue == oldValue && alias.OldCodingSchemeDesignator == oldScheme {
			return alias, true
		}
	}
	return generatedRetiredCodeAlias{}, false
}

func computeGeneratedRetiredCodeProjectionSHA256() string {
	var projection bytes.Buffer
	for _, alias := range generatedRetiredCodeAliases {
		projection.WriteString(alias.OldCodeValue)
		projection.WriteByte('\t')
		projection.WriteString(alias.OldCodingSchemeDesignator)
		projection.WriteByte('\t')
		projection.WriteString(alias.CurrentCodeValue)
		projection.WriteByte('\t')
		projection.WriteString(alias.CurrentCodingSchemeDesignator)
		projection.WriteByte('\n')
	}
	digest := sha256.Sum256(projection.Bytes())
	return hex.EncodeToString(digest[:])
}
