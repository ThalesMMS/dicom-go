package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func sourceFixture(t testing.TB) source {
	t.Helper()
	data, err := os.ReadFile("../../dictionary/curated/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := decodeSource(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPrivateCatalogGenerationReproducible(t *testing.T) {
	s := sourceFixture(t)
	records, err := validatedRecords(s)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../dictionary/curated/catalog_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := generate(records)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("generated catalog differs")
	}
	slices.Reverse(s.Records)
	records, err = validatedRecords(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err = generate(records)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("source order changes output")
	}
	var stderr bytes.Buffer
	if code := run([]string{"-source", "../../dictionary/curated/catalog.json", "-out", "../../dictionary/curated/catalog_gen.go", "-check"}, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
}

func TestPrivateCatalogGeneratorRejectsUnreviewedDefinitions(t *testing.T) {
	mutations := map[string]func(*source){
		"duplicate":                 func(s *source) { s.Records = append(s.Records, s.Records[0]) },
		"conflict":                  func(s *source) { other := s.Records[0]; other.VR = "FD"; s.Records = append(s.Records, other) },
		"padded creator":            func(s *source) { s.Records[0].Creator += " " },
		"forbidden group":           func(s *source) { s.Records[0].Group = "0007" },
		"even group":                func(s *source) { s.Records[0].Group = "0018" },
		"lowercase key":             func(s *source) { s.Records[0].Offset = "a0" },
		"fixed block":               func(s *source) { s.Records[0].Offset = "1002" },
		"unknown VR":                func(s *source) { s.Records[0].VR = "UN" },
		"invalid VR":                func(s *source) { s.Records[0].VR = "XX" },
		"noncanonical VM":           func(s *source) { s.Records[0].VM = "01" },
		"no license":                func(s *source) { s.Records[0].Origin.License = "" },
		"no source":                 func(s *source) { s.Records[0].Origin.SourceURL = "" },
		"unpinned":                  func(s *source) { s.Records[0].Origin.Commit = "main" },
		"unreviewed":                func(s *source) { s.Records[0].Origin.Verification = "inferred" },
		"missing restrictions":      func(s *source) { s.Records[0].Origin.Restrictions = "" },
		"unknown manufacturer name": func(s *source) { s.Records[0].Name = "Unknown"; s.Records[0].Keyword = "Unknown" },
		"contradictory source": func(s *source) {
			s.Records[0].Origin.SourceRow = strings.Replace(s.Records[0].Origin.SourceRow, "\tSL\t", "\tSS\t", 1)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := sourceFixture(t)
			mutate(&s)
			if _, err := validatedRecords(s); !errors.Is(err, errSource) {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
	for _, vm := range []string{"1", "2", "1-n", "2-2n", "1-3"} {
		if !canonicalVM(vm) {
			t.Fatal("valid VM rejected", vm)
		}
	}
	for _, vm := range []string{"", "0", "1-0", "3-2", "1-2-3", "1-0n", "1-3n", "01", "+1", "1-99999999999999999999"} {
		if canonicalVM(vm) {
			t.Fatal("invalid VM accepted", vm)
		}
	}
}

func TestPrivateCatalogSourceVerificationAndFailedGeneration(t *testing.T) {
	s := sourceFixture(t)
	// A synthetic upstream excerpt tests hash/line verification without a
	// network dependency. It is never distributed as manufacturer evidence.
	s.Records = s.Records[:1]
	s.Records[0].Origin.SourceLine = 1
	data := []byte(s.Records[0].Origin.SourceRow + "\n")
	hash := sha256.Sum256(data)
	s.Records[0].Origin.SourceSHA256 = hex.EncodeToString(hash[:])
	if err := verifyUpstream(s, data); err != nil {
		t.Fatal(err)
	}
	if err := verifyUpstream(s, append(bytes.Clone(data), 'x')); err == nil {
		t.Fatal("source drift accepted")
	}
	conflict := strings.Replace(string(data), "\tSL\t", "\tSS\t", 1)
	data = append(data, conflict...)
	hash = sha256.Sum256(data)
	s.Records[0].Origin.SourceSHA256 = hex.EncodeToString(hash[:])
	if err := verifyUpstream(s, data); err == nil {
		t.Fatal("upstream conflicting override accepted")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.json")
	output := filepath.Join(dir, "catalog.go")
	if err := os.WriteFile(output, []byte("preserve existing output"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Records[0].Origin.License = ""
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-source", input, "-out", output}, &bytes.Buffer{}); code != 1 {
		t.Fatal("invalid source generated output")
	}
	after, err := os.ReadFile(output)
	if err != nil || string(after) != "preserve existing output" {
		t.Fatal("failure changed output")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("generation retained temporary")
	}
}

func TestPrivateCatalogJSONRejectsTrailingAndUnknownFields(t *testing.T) {
	s := sourceFixture(t)
	data, _ := json.Marshal(s)
	for _, bad := range [][]byte{append(bytes.Clone(data), []byte(" {}")...), []byte(strings.Replace(string(data), `"schema_version":1`, `"schema_version":1,"unreviewed":true`, 1)), bytes.Repeat([]byte(" "), maxSourceBytes+1)} {
		if _, err := decodeSource(bad); err == nil {
			t.Fatal("invalid source syntax accepted")
		}
	}
	for _, field := range []string{"schema_version", "SCHEMA_VERSION"} {
		bad := strings.Replace(string(data), `"schema_version":1`, `"schema_version":2,"`+field+`":1`, 1)
		if _, err := decodeSource([]byte(bad)); err == nil {
			t.Fatal("duplicate JSON field silently overrode the review source")
		}
	}
}
