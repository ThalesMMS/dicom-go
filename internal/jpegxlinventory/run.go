package jpegxlinventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	SchemaVersion      = 1
	rawFixturePolicy   = "Raw fixture files remain local unless separately approved for publication"
	defaultManifestRel = ""
)

var errUsage = errors.New("usage error")

type options struct {
	dir    string
	out    string
	verify bool
}

// Manifest is a PHI-safe inventory of local JPEG XL DICOM fixtures.
type Manifest struct {
	SchemaVersion int                    `json:"schemaVersion"`
	GeneratedAt   string                 `json:"generatedAt,omitempty"`
	SourceDir     string                 `json:"sourceDir"`
	Provenance    Provenance             `json:"provenance"`
	Summary       Summary                `json:"summary"`
	Groups        map[string]SyntaxGroup `json:"groups"`
}

type Provenance struct {
	RawFixturePolicy string   `json:"rawFixturePolicy"`
	Includes         []string `json:"includes"`
	Excludes         []string `json:"excludes"`
}

type Summary struct {
	TotalFiles int            `json:"totalFiles"`
	BySyntax   map[string]int `json:"bySyntax"`
}

type SyntaxGroup struct {
	TransferSyntaxUID  string  `json:"transferSyntaxUID"`
	TransferSyntaxName string  `json:"transferSyntaxName"`
	Files              []Entry `json:"files"`
}

type Entry struct {
	Path                      string        `json:"path"`
	FileSHA256                string        `json:"fileSha256"`
	TransferSyntaxUID         string        `json:"transferSyntaxUID"`
	TransferSyntaxName        string        `json:"transferSyntaxName"`
	Rows                      uint16        `json:"rows"`
	Columns                   uint16        `json:"columns"`
	SamplesPerPixel           uint16        `json:"samplesPerPixel"`
	PhotometricInterpretation string        `json:"photometricInterpretation"`
	BitsAllocated             uint16        `json:"bitsAllocated"`
	BitsStored                uint16        `json:"bitsStored"`
	PixelRepresentation       uint16        `json:"pixelRepresentation"`
	FrameCount                int           `json:"frameCount"`
	Encapsulation             Encapsulation `json:"encapsulation"`
}

type Encapsulation struct {
	Encapsulated      bool     `json:"encapsulated"`
	FragmentCount     int      `json:"fragmentCount"`
	FragmentSizes     []int    `json:"fragmentSizes"`
	FragmentSHA256    []string `json:"fragmentSha256"`
	OffsetTableBytes  int      `json:"offsetTableBytes"`
	OffsetTableSHA256 string   `json:"offsetTableSha256,omitempty"`
}

// Run executes the jpegxl-inventory command.
func Run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		clidiag.Fprintln(stderr, "jpegxl-inventory", err)
		return 2
	}

	if opts.verify {
		if err := verifyManifest(stdout, opts.dir, opts.out); err != nil {
			clidiag.Fprintln(stderr, "jpegxl-inventory", err)
			return 1
		}
		return 0
	}

	manifest, err := Generate(opts.dir, time.Now().UTC())
	if err != nil {
		clidiag.Fprintln(stderr, "jpegxl-inventory", err)
		return 1
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		clidiag.Fprintln(stderr, "jpegxl-inventory", err)
		return 1
	}
	data = append(data, '\n')
	if opts.out == "" {
		if _, err := stdout.Write(data); err != nil {
			clidiag.Fprintln(stderr, "jpegxl-inventory", err)
			return 1
		}
		return 0
	}
	if dir := filepath.Dir(opts.out); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			clidiag.Fprintln(stderr, "jpegxl-inventory", err)
			return 1
		}
	}
	if err := os.WriteFile(opts.out, data, 0o600); err != nil {
		clidiag.Fprintln(stderr, "jpegxl-inventory", err)
		return 1
	}
	return 0
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("jpegxl-inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.dir, "dir", "", "directory containing local JPEG XL DICOM fixtures")
	fs.StringVar(&opts.out, "out", defaultManifestRel, "manifest output path, or manifest path in -verify mode; defaults to stdout for generation")
	fs.BoolVar(&opts.verify, "verify", false, "verify an existing manifest instead of generating one")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s -dir JPEGXL-Fixture [-out manifest.json] [-verify]\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errUsage
	}
	switch {
	case fs.NArg() != 0:
		fs.Usage()
		return opts, errUsage
	case opts.dir == "":
		_, _ = fmt.Fprintln(stderr, "-dir is required")
		fs.Usage()
		return opts, errUsage
	case opts.verify && opts.out == "":
		_, _ = fmt.Fprintln(stderr, "-out is required with -verify")
		fs.Usage()
		return opts, errUsage
	}
	opts.dir = filepath.Clean(opts.dir)
	return opts, nil
}

func Generate(dir string, now time.Time) (Manifest, error) {
	manifest := newManifest(dir, now)
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		entry, ok, err := extractEntry(dir, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			return nil
		}
		groupName := jpegXLGroupName(entry.TransferSyntaxUID)
		group := manifest.Groups[groupName]
		group.Files = append(group.Files, entry)
		manifest.Groups[groupName] = group
		manifest.Summary.TotalFiles++
		manifest.Summary.BySyntax[groupName]++
		return nil
	}); err != nil {
		return Manifest{}, err
	}
	for name, group := range manifest.Groups {
		sort.Slice(group.Files, func(i, j int) bool { return group.Files[i].Path < group.Files[j].Path })
		manifest.Groups[name] = group
	}
	return manifest, nil
}

func newManifest(dir string, now time.Time) Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now.Format(time.RFC3339),
		SourceDir:     dir,
		Provenance: Provenance{
			RawFixturePolicy: rawFixturePolicy,
			Includes: []string{
				"technical DICOM pixel metadata",
				"transfer syntax identifiers",
				"file and encapsulated Pixel Data fragment SHA-256 hashes",
				"encapsulation fragment counts and sizes",
			},
			Excludes: []string{
				"patient names",
				"patient IDs",
				"accession numbers",
				"raw DICOM bytes",
				"raw Pixel Data fragments",
			},
		},
		Summary: Summary{BySyntax: map[string]int{
			"JPEGXLLossless":          0,
			"JPEGXLJPEGRecompression": 0,
			"JPEGXL":                  0,
		}},
		Groups: map[string]SyntaxGroup{
			"JPEGXLLossless":          emptyGroup(transfer.JPEGXLLossless),
			"JPEGXLJPEGRecompression": emptyGroup(transfer.JPEGXLJPEGRecompression),
			"JPEGXL":                  emptyGroup(transfer.JPEGXL),
		},
	}
}

func emptyGroup(syntax transfer.Syntax) SyntaxGroup {
	return SyntaxGroup{TransferSyntaxUID: syntax.UID, TransferSyntaxName: syntax.Name, Files: []Entry{}}
}

func extractEntry(root, path string) (Entry, bool, error) {
	file, err := object.OpenFile(path)
	if err != nil {
		return Entry{}, false, err
	}
	defer func() { _ = file.Close() }()

	uid := transfer.NormalizeUID(file.TransferSyntax.UID)
	if !transfer.IsJPEGXLTransferSyntax(uid) {
		return Entry{}, false, nil
	}

	meta, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		return Entry{}, false, err
	}
	pixels, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		return Entry{}, false, err
	}
	if !pixels.Encapsulated {
		return Entry{}, false, fmt.Errorf("JPEG XL fixture Pixel Data is not encapsulated")
	}
	fileHash, err := sha256File(path)
	if err != nil {
		return Entry{}, false, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return Entry{}, false, err
	}
	entry := Entry{
		Path:                      filepath.ToSlash(rel),
		FileSHA256:                fileHash,
		TransferSyntaxUID:         uid,
		TransferSyntaxName:        file.TransferSyntax.Name,
		Rows:                      meta.Rows,
		Columns:                   meta.Columns,
		SamplesPerPixel:           meta.SamplesPerPixel,
		PhotometricInterpretation: meta.PhotometricInterpretation,
		BitsAllocated:             meta.BitsAllocated,
		BitsStored:                meta.BitsStored,
		PixelRepresentation:       meta.PixelRepresentation,
		FrameCount:                meta.NumberOfFrames,
		Encapsulation:             encapsulationInfo(pixels.Sequence),
	}
	return entry, true, nil
}

func encapsulationInfo(seq core.FragmentSequence) Encapsulation {
	info := Encapsulation{
		Encapsulated:     true,
		FragmentCount:    len(seq.Fragments),
		FragmentSizes:    make([]int, 0, len(seq.Fragments)),
		FragmentSHA256:   make([]string, 0, len(seq.Fragments)),
		OffsetTableBytes: len(seq.OffsetTable),
	}
	if len(seq.OffsetTable) > 0 {
		sum := sha256.Sum256(seq.OffsetTable)
		info.OffsetTableSHA256 = hex.EncodeToString(sum[:])
	}
	for _, fragment := range seq.Fragments {
		sum := sha256.Sum256(fragment)
		info.FragmentSizes = append(info.FragmentSizes, len(fragment))
		info.FragmentSHA256 = append(info.FragmentSHA256, hex.EncodeToString(sum[:]))
	}
	return info
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func jpegXLGroupName(uid string) string {
	switch transfer.NormalizeUID(uid) {
	case transfer.JPEGXLLossless.UID:
		return "JPEGXLLossless"
	case transfer.JPEGXLJPEGRecompression.UID:
		return "JPEGXLJPEGRecompression"
	default:
		return "JPEGXL"
	}
}

func verifyManifest(stdout io.Writer, dir, manifestPath string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	path, err := filepath.Abs(manifestPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid manifest path %q", manifestPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	checked := 0
	for _, group := range manifest.Groups {
		for _, entry := range group.Files {
			checked++
			path, err := manifestEntryPath(dir, entry.Path)
			if err != nil {
				return fmt.Errorf("%s: %w", entry.Path, err)
			}
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("missing file %s", entry.Path)
				}
				return err
			}
			got, ok, err := extractEntry(dir, path)
			if err != nil {
				return fmt.Errorf("%s: %w", entry.Path, err)
			}
			if !ok {
				return fmt.Errorf("%s: not a JPEG XL DICOM fixture", entry.Path)
			}
			if !reflect.DeepEqual(got, entry) {
				return fmt.Errorf("%s: manifest entry does not match current file", entry.Path)
			}
		}
	}
	_, err = fmt.Fprintf(stdout, "verified %d file(s)\n", checked)
	return err
}

func manifestEntryPath(dir, entryPath string) (string, error) {
	native := filepath.Clean(filepath.FromSlash(entryPath))
	if native == "." || filepath.IsAbs(native) {
		return "", fmt.Errorf("invalid manifest path %q", entryPath)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, native))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid manifest path %q", entryPath)
	}
	return path, nil
}
