package deid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

func TestGeneratedProfileTableProvenance(t *testing.T) {
	if GeneratedProfileStandardVersion != "PS3.15 2026b" {
		t.Fatalf("standard version = %q", GeneratedProfileStandardVersion)
	}
	if GeneratedProfileSourceURL != "https://dicom.nema.org/medical/dicom/2026b/output/chtml/part15/chapter_E.html" {
		t.Fatalf("source URL = %q", GeneratedProfileSourceURL)
	}
	if GeneratedProfileRowCount != 655 {
		t.Fatalf("declared row count = %d", GeneratedProfileRowCount)
	}
	if got := len(generatedProfileRules); got != GeneratedProfileRowCount {
		t.Fatalf("generated row count = %d, want %d", got, GeneratedProfileRowCount)
	}
	if GeneratedProfileProjectionSHA256 != "5feabf5ab985707bdf299b3d6ea253859be230ee54faed25ad3dbd1a9df98fc3" {
		t.Fatalf("declared projection checksum = %q", GeneratedProfileProjectionSHA256)
	}
	if got := computeGeneratedProfileProjectionSHA256(); got != GeneratedProfileProjectionSHA256 {
		t.Fatalf("computed projection checksum = %s, want %s", got, GeneratedProfileProjectionSHA256)
	}
}

func TestGeneratedProfilePatternsAreUniqueAndCanonical(t *testing.T) {
	standardTag := regexp.MustCompile(`^\([0-9A-F]{4},[0-9A-F]{4}\)$`)
	repeatingTag := regexp.MustCompile(`^\([0-9A-F]{2}xx,(?:[0-9A-F]{4}|xxxx)\)$`)
	seen := make(map[string]int, len(generatedProfileRules))
	for index, rule := range generatedProfileRules {
		if previous, duplicate := seen[rule.Pattern]; duplicate {
			t.Fatalf("duplicate pattern %q at rows %d and %d", rule.Pattern, previous+1, index+1)
		}
		seen[rule.Pattern] = index
		if !standardTag.MatchString(rule.Pattern) &&
			!repeatingTag.MatchString(rule.Pattern) &&
			rule.Pattern != "(gggg,eeee) where gggg is odd" {
			t.Fatalf("row %d has non-canonical pattern %q", index+1, rule.Pattern)
		}
	}

	for _, pattern := range []string{
		"(0008,0050)",
		"(50xx,xxxx)",
		"(60xx,3000)",
		"(60xx,4000)",
		"(gggg,eeee) where gggg is odd",
	} {
		if _, ok := seen[pattern]; !ok {
			t.Errorf("required exact pattern %q is absent", pattern)
		}
	}
}

func TestGeneratedProfileActionCodes(t *testing.T) {
	allowed := map[ActionCode]bool{
		"":       true,
		"C":      true,
		"D":      true,
		"K":      true,
		"U":      true,
		"X":      true,
		"Z":      true,
		"X/D":    true,
		"X/Z":    true,
		"Z/D":    true,
		"X/Z/D":  true,
		"X/Z/U*": true,
	}
	for rowIndex, rule := range generatedProfileRules {
		for actionIndex, action := range rule.Actions {
			if !allowed[action] {
				t.Fatalf("row %d action column %d has unsupported code %q", rowIndex+1, actionIndex+1, action)
			}
		}
	}
}

func computeGeneratedProfileProjectionSHA256() string {
	var projection bytes.Buffer
	for _, rule := range generatedProfileRules {
		projection.WriteString(rule.Pattern)
		for _, action := range rule.Actions {
			projection.WriteByte('\t')
			projection.WriteString(string(action))
		}
		projection.WriteByte('\n')
	}
	digest := sha256.Sum256(projection.Bytes())
	return hex.EncodeToString(digest[:])
}
