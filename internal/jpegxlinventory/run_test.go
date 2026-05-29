package jpegxlinventory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	testTagPatientName               = core.NewTag(0x0010, 0x0010)
	testTagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	testTagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	testTagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	testTagRows                      = core.NewTag(0x0028, 0x0010)
	testTagColumns                   = core.NewTag(0x0028, 0x0011)
	testTagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	testTagBitsStored                = core.NewTag(0x0028, 0x0101)
	testTagHighBit                   = core.NewTag(0x0028, 0x0102)
	testTagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
)

func TestRunGeneratesPHISafeGroupedManifest(t *testing.T) {
	dir := t.TempDir()
	genericFragment := []byte("RAW-JXL-FRAGMENT")
	writeJPEGXLFixture(t, dir, "lossless.dcm", transfer.JPEGXLLossless, []byte{1, 2, 3}, []byte{4, 5})
	writeJPEGXLFixture(t, dir, "recompression.dcm", transfer.JPEGXLJPEGRecompression, []byte{6, 7, 8})
	writeJPEGXLFixture(t, dir, "nested/generic.dcm", transfer.JPEGXL, genericFragment)
	writePart10(t, filepath.Join(dir, "native.dcm"), transfer.ExplicitVRLittleEndian, technicalElements(true, []byte{0}))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, stderr = %s", code, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("SECRET^PATIENT")) {
		t.Fatal("manifest contains patient name")
	}
	if bytes.Contains(stdout.Bytes(), genericFragment) {
		t.Fatal("manifest contains raw fragment bytes")
	}

	var manifest Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest JSON did not decode: %v\n%s", err, stdout.String())
	}
	if manifest.Summary.TotalFiles != 3 {
		t.Fatalf("Summary.TotalFiles = %d, want 3", manifest.Summary.TotalFiles)
	}
	if !strings.Contains(manifest.Provenance.RawFixturePolicy, "Raw fixture files remain local") {
		t.Fatalf("RawFixturePolicy = %q", manifest.Provenance.RawFixturePolicy)
	}

	wantGroups := map[string]transfer.Syntax{
		"JPEGXLLossless":          transfer.JPEGXLLossless,
		"JPEGXLJPEGRecompression": transfer.JPEGXLJPEGRecompression,
		"JPEGXL":                  transfer.JPEGXL,
	}
	for name, syntax := range wantGroups {
		group, ok := manifest.Groups[name]
		if !ok {
			t.Fatalf("missing group %s", name)
		}
		if group.TransferSyntaxUID != syntax.UID || group.TransferSyntaxName != syntax.Name {
			t.Fatalf("%s syntax = %q/%q, want %q/%q", name, group.TransferSyntaxUID, group.TransferSyntaxName, syntax.UID, syntax.Name)
		}
		if len(group.Files) != 1 {
			t.Fatalf("%s file count = %d, want 1", name, len(group.Files))
		}
		entry := group.Files[0]
		if filepath.IsAbs(entry.Path) {
			t.Fatalf("%s path is absolute: %q", name, entry.Path)
		}
		if len(entry.FileSHA256) != 64 {
			t.Fatalf("%s file hash length = %d, want 64", name, len(entry.FileSHA256))
		}
		if entry.Rows != 2 || entry.Columns != 3 || entry.SamplesPerPixel != 1 || entry.PhotometricInterpretation != "MONOCHROME2" {
			t.Fatalf("%s metadata = %+v", name, entry)
		}
		if entry.BitsAllocated != 8 || entry.BitsStored != 8 || entry.PixelRepresentation != 0 || entry.FrameCount != 1 {
			t.Fatalf("%s pixel metadata = %+v", name, entry)
		}
		if !entry.Encapsulation.Encapsulated || entry.Encapsulation.FragmentCount == 0 {
			t.Fatalf("%s encapsulation = %+v", name, entry.Encapsulation)
		}
		if len(entry.Encapsulation.FragmentSHA256) != entry.Encapsulation.FragmentCount {
			t.Fatalf("%s fragment hashes = %d, want %d", name, len(entry.Encapsulation.FragmentSHA256), entry.Encapsulation.FragmentCount)
		}
	}

	generic := manifest.Groups["JPEGXL"].Files[0]
	wantFragmentHash := sha256.Sum256(genericFragment)
	if got := generic.Encapsulation.FragmentSHA256[0]; got != hex.EncodeToString(wantFragmentHash[:]) {
		t.Fatalf("generic fragment hash = %q, want %q", got, hex.EncodeToString(wantFragmentHash[:]))
	}
}

func TestRunVerifyDetectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	writeJPEGXLFixture(t, dir, "generic.dcm", transfer.JPEGXL, []byte{1, 2, 3})
	manifestPath := filepath.Join(dir, "manifest.json")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-dir", dir, "-out", manifestPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code = %d, stderr = %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"-dir", dir, "-out", manifestPath, "-verify"}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify code = %d, stdout = %s stderr = %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "verified 1 file") {
		t.Fatalf("verify output = %q", stdout.String())
	}

	if err := os.Remove(filepath.Join(dir, "generic.dcm")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"-dir", dir, "-out", manifestPath, "-verify"}, &stdout, &stderr); code == 0 {
		t.Fatalf("verify after remove code = 0, stdout = %s stderr = %s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing file") {
		t.Fatalf("verify stderr = %q, want missing file", stderr.String())
	}
}

func TestRunVerifyRejectsManifestPathOutsideDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJPEGXLFixture(t, dir, "generic.dcm", transfer.JPEGXL, []byte{1, 2, 3})
	data, err := os.ReadFile(filepath.Join(dir, "generic.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "escape.dcm"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-dir", dir, "-out", manifestPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code = %d, stderr = %s", code, stderr.String())
	}
	var manifest Manifest
	if err := json.Unmarshal(mustReadFile(t, manifestPath), &manifest); err != nil {
		t.Fatal(err)
	}
	group := manifest.Groups["JPEGXL"]
	group.Files[0].Path = "../escape.dcm"
	manifest.Groups["JPEGXL"] = group
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"-dir", dir, "-out", manifestPath, "-verify"}, &stdout, &stderr); code == 0 {
		t.Fatalf("verify code = 0, stdout = %s stderr = %s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid manifest path") {
		t.Fatalf("verify stderr = %q, want invalid manifest path", stderr.String())
	}
}

func TestRunVerifyRejectsManifestPathArgumentOutsideDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJPEGXLFixture(t, dir, "generic.dcm", transfer.JPEGXL, []byte{1, 2, 3})
	manifestPath := filepath.Join(parent, "manifest.json")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-dir", dir, "-out", manifestPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate code = %d, stderr = %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	traversalPath := filepath.Join(dir, "..", "manifest.json")
	if code := Run([]string{"-dir", dir, "-out", traversalPath, "-verify"}, &stdout, &stderr); code == 0 {
		t.Fatalf("verify code = 0, stdout = %s stderr = %s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid manifest path") {
		t.Fatalf("verify stderr = %q, want invalid manifest path", stderr.String())
	}
}

func TestRunFailsWhenRequiredTechnicalTagsAreMissing(t *testing.T) {
	dir := t.TempDir()
	writePart10(t, filepath.Join(dir, "broken.dcm"), transfer.JPEGXL, technicalElements(false, []byte{1, 2, 3}))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-dir", dir}, &stdout, &stderr); code == 0 {
		t.Fatalf("Run() code = 0, stdout = %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Rows") {
		t.Fatalf("stderr = %q, want missing Rows", stderr.String())
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeJPEGXLFixture(t *testing.T, dir, rel string, syntax transfer.Syntax, fragments ...[]byte) {
	t.Helper()
	writePart10(t, filepath.Join(dir, rel), syntax, technicalElements(true, fragments...))
}

func writePart10(t *testing.T, path string, syntax transfer.Syntax, elems []core.Element) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := dicomtest.Part10File(syntax, elems...)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func technicalElements(includeMetadata bool, fragments ...[]byte) []core.Element {
	elems := []core.Element{
		dicomtest.NewPNElement(testTagPatientName, "SECRET^PATIENT"),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...),
	}
	if !includeMetadata {
		return elems
	}
	return append([]core.Element{
		dicomtest.NewUShortElement(testTagSamplesPerPixel, 1),
		dicomtest.NewStringElement(testTagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(testTagNumberOfFrames, core.VRIS, "1"),
		dicomtest.NewUShortElement(testTagRows, 2),
		dicomtest.NewUShortElement(testTagColumns, 3),
		dicomtest.NewUShortElement(testTagBitsAllocated, 8),
		dicomtest.NewUShortElement(testTagBitsStored, 8),
		dicomtest.NewUShortElement(testTagHighBit, 7),
		dicomtest.NewUShortElement(testTagPixelRepresentation, 0),
	}, elems...)
}
