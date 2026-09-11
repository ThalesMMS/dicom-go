// Command privatecataloggen validates a reviewed private catalog source and
// generates deterministic Go records. It never fetches data from the network.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/curated"
)

var errSource = errors.New("privatecataloggen: invalid reviewed source")

const maxSourceBytes = 2 << 20

type source struct {
	Schema     int            `json:"schema_version"`
	Records    []sourceRecord `json:"records"`
	Exclusions []string       `json:"exclusions"`
}
type sourceRecord struct {
	Creator string       `json:"creator"`
	Group   string       `json:"group"`
	Offset  string       `json:"offset"`
	VR      string       `json:"vr"`
	VM      string       `json:"vm"`
	Name    string       `json:"name"`
	Keyword string       `json:"keyword"`
	Origin  sourceOrigin `json:"origin"`
}
type sourceOrigin struct {
	Project      string `json:"project"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	SourceURL    string `json:"source_url"`
	SourceSHA256 string `json:"source_sha256"`
	SourceLine   int    `json:"source_line"`
	SourceRow    string `json:"source_row"`
	License      string `json:"license"`
	LicenseURL   string `json:"license_url"`
	Restrictions string `json:"restrictions"`
	Verification string `json:"verification"`
}

var (
	hexGroup    = regexp.MustCompile(`^[0-9A-F]{4}$`)
	hexOffset   = regexp.MustCompile(`^[0-9A-F]{2}$`)
	commitHash  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	contentHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyword     = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)
	upstreamKey = regexp.MustCompile(`^\(([0-9a-fA-F]{4}),"([^"]+)",([0-9a-fA-F]{2})\)$`)
)

func canonicalVM(vm string) bool {
	parts := strings.Split(vm, "-")
	positive := func(v string) int {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000000 || strconv.Itoa(n) != v {
			return 0
		}
		return n
	}
	if len(parts) > 2 || positive(parts[0]) == 0 {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	if parts[1] == "n" {
		return true
	}
	if strings.HasSuffix(parts[1], "n") {
		n := positive(strings.TrimSuffix(parts[1], "n"))
		return n > 0 && n == positive(parts[0])
	}
	return positive(parts[1]) >= positive(parts[0])
}

func decodeSource(data []byte) (source, error) {
	if len(data) > maxSourceBytes {
		return source{}, errSource
	}
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return source{}, errSource
	}
	var s source
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return source{}, errSource
	}
	if d.Decode(new(any)) != io.EOF {
		return source{}, errSource
	}
	if s.Schema != 1 || len(s.Records) == 0 || len(s.Records) > 4096 || len(s.Exclusions) == 0 {
		return source{}, errSource
	}
	return s, nil
}

// encoding/json accepts duplicate names using last-wins semantics, including
// case variants of struct field names. Reviewed definitions must not be silently
// replaced this way. The format has shallow bounded objects and arrays.
func uniqueJSONKeys(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errSource
	}
	token, err := d.Token()
	if err != nil {
		return errSource
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return errSource
			}
			key, ok := token.(string)
			if !ok {
				return errSource
			}
			key = strings.ToLower(key)
			if seen[key] {
				return errSource
			}
			seen[key] = true
			if err := uniqueJSONKeys(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return errSource
		}
	case '[':
		for d.More() {
			if err := uniqueJSONKeys(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return errSource
		}
	default:
		return errSource
	}
	return nil
}

func validatedRecords(s source) ([]curated.Record, error) {
	var records []curated.Record
	seen := map[string]bool{}
	for index, r := range s.Records {
		invalid := func(field string) ([]curated.Record, error) {
			return nil, fmt.Errorf("%w: record %d %s", errSource, index, field)
		}
		creator, err := dictionary.PrivateCreatorID(r.Creator)
		if err != nil || creator != r.Creator || !hexGroup.MatchString(r.Group) || !hexOffset.MatchString(r.Offset) {
			return invalid("canonical key")
		}
		group, _ := strconv.ParseUint(r.Group, 16, 16)
		offset, _ := strconv.ParseUint(r.Offset, 16, 8)
		if !dictionary.IsPrivateGroup(uint16(group)) {
			return invalid("private group")
		}
		vr, err := core.ParseVR(r.VR)
		if err != nil || string(vr) != r.VR || vr == core.VRUN || !canonicalVM(r.VM) {
			return invalid("VR/VM")
		}
		if !keyword.MatchString(r.Keyword) || r.Name != r.Keyword || strings.EqualFold(r.Name, "Unknown") || len(r.Name) > 128 {
			return invalid("reviewed name")
		}
		id := r.Creator + "/" + r.Group + "/" + r.Offset
		if seen[id] {
			return invalid("duplicate or conflict")
		}
		seen[id] = true
		o := r.Origin
		if o.Project != "DCMTK" || !strings.HasPrefix(o.Version, "DCMTK-") || !commitHash.MatchString(o.Commit) || !contentHash.MatchString(o.SourceSHA256) || o.SourceLine < 1 || o.SourceLine > 1000000 {
			return invalid("pinned provenance")
		}
		base := "https://github.com/DCMTK/dcmtk/blob/" + o.Commit
		if o.SourceURL != base+"/dcmdata/data/private.dic" || o.LicenseURL != base+"/COPYRIGHT" || o.License != "BSD-3-Clause" || strings.TrimSpace(o.Restrictions) == "" || len(o.Restrictions) > 2048 || o.Verification != "reviewed-upstream" {
			return invalid("source/license/review")
		}
		fields := strings.Split(o.SourceRow, "\t")
		if len(fields) != 5 {
			return invalid("upstream row")
		}
		key := upstreamKey.FindStringSubmatch(fields[0])
		if len(key) != 4 || strings.ToUpper(key[1]) != r.Group || key[2] != r.Creator || strings.ToUpper(key[3]) != r.Offset || fields[1] != r.VR || fields[2] != r.Name || fields[3] != r.VM || fields[4] != "PrivateTag" {
			return invalid("definition contradicts source row")
		}
		hash := sha256.Sum256([]byte(o.SourceRow))
		records = append(records, curated.Record{Definition: dictionary.PrivateEntry{Creator: r.Creator, Group: uint16(group), Offset: uint8(offset), VR: vr, VM: r.VM, Name: r.Name, Keyword: r.Keyword}, Origin: curated.Origin{Project: o.Project, Version: o.Version, Commit: o.Commit, SourceURL: o.SourceURL, SourceSHA256: o.SourceSHA256, SourceLine: o.SourceLine, DefinitionSHA256: hex.EncodeToString(hash[:]), License: o.License, LicenseURL: o.LicenseURL, Restrictions: o.Restrictions, Verification: o.Verification}})
	}
	slices.SortFunc(records, func(a, b curated.Record) int {
		if c := strings.Compare(a.Definition.Creator, b.Definition.Creator); c != 0 {
			return c
		}
		if a.Definition.Group != b.Definition.Group {
			return int(a.Definition.Group) - int(b.Definition.Group)
		}
		return int(a.Definition.Offset) - int(b.Definition.Offset)
	})
	return records, nil
}

func verifyUpstream(s source, data []byte) error {
	if len(data) > maxSourceBytes {
		return errSource
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, r := range s.Records {
		if r.Origin.SourceSHA256 != digest || r.Origin.SourceLine < 1 || r.Origin.SourceLine > len(lines) || lines[r.Origin.SourceLine-1] != r.Origin.SourceRow {
			return fmt.Errorf("%w: upstream record %d differs", errSource, i)
		}
		// The upstream format permits later overrides. Reject any competing
		// definition of a selected block-relative key rather than taking one.
		for _, line := range lines {
			fields := strings.Split(line, "\t")
			if len(fields) != 5 {
				continue
			}
			key := upstreamKey.FindStringSubmatch(fields[0])
			if len(key) == 4 && strings.ToUpper(key[1]) == r.Group && key[2] == r.Creator && strings.ToUpper(key[3]) == r.Offset && line != r.Origin.SourceRow {
				return fmt.Errorf("%w: upstream conflict %d", errSource, i)
			}
		}
	}
	return nil
}

func generate(records []curated.Record) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by cmd/privatecataloggen; DO NOT EDIT.\n// Selected DCMTK private.dic definitions Copyright (C) 1994-2020 OFFIS e.V.\n// Redistribution terms and disclaimer: LICENSE.DCMTK.\npackage curated\nimport \"github.com/ThalesMMS/dicom-go/dictionary\"\nvar records = []Record{\n")
	for _, r := range records {
		d, o := r.Definition, r.Origin
		fmt.Fprintf(&b, "{Definition:dictionary.PrivateEntry{Creator:%q,Group:0x%04X,Offset:0x%02X,VR:%q,VM:%q,Name:%q,Keyword:%q},Origin:Origin{Project:%q,Version:%q,Commit:%q,SourceURL:%q,SourceSHA256:%q,SourceLine:%d,DefinitionSHA256:%q,License:%q,LicenseURL:%q,Restrictions:%q,Verification:%q}},\n", d.Creator, d.Group, d.Offset, d.VR, d.VM, d.Name, d.Keyword, o.Project, o.Version, o.Commit, o.SourceURL, o.SourceSHA256, o.SourceLine, o.DefinitionSHA256, o.License, o.LicenseURL, o.Restrictions, o.Verification)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errSource
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
	if err != nil || len(data) > maxSourceBytes {
		return nil, errSource
	}
	return data, nil
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("privatecataloggen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sourcePath := fs.String("source", "dictionary/curated/catalog.json", "reviewed local JSON source")
	out := fs.String("out", "dictionary/curated/catalog_gen.go", "generated Go path")
	check := fs.Bool("check", false, "verify existing output without mutation")
	upstream := fs.String("verify-upstream", "", "optional local pinned private.dic; no downloads")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	data, err := readBounded(*sourcePath)
	if err != nil {
		return fail(err)
	}
	s, err := decodeSource(data)
	if err != nil {
		return fail(err)
	}
	records, err := validatedRecords(s)
	if err != nil {
		return fail(err)
	}
	if *upstream != "" {
		data, err := readBounded(*upstream)
		if err != nil {
			return fail(err)
		}
		if err := verifyUpstream(s, data); err != nil {
			return fail(err)
		}
	}
	generated, err := generate(records)
	if err != nil {
		return fail(errSource)
	}
	if *check {
		existing, err := readBounded(*out)
		if err != nil || !bytes.Equal(existing, generated) {
			return fail(errors.New("privatecataloggen: generated output is stale"))
		}
		return 0
	}
	f, err := os.CreateTemp(filepath.Dir(*out), ".privatecataloggen-*.go")
	if err != nil {
		return fail(errSource)
	}
	name := f.Name()
	defer os.Remove(name)
	_, writeErr := f.Write(generated)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return fail(errSource)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fail(errSource)
	}
	if err := os.Rename(name, *out); err != nil {
		return fail(errSource)
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
