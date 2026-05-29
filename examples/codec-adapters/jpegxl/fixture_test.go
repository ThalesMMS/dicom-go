package jpegxladapter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const jpegxlFullFixtureEnv = "DICOMGO_JPEGXL_FULL"

type jpegxlFixtureManifest struct {
	SourceDir string                        `json:"sourceDir"`
	Summary   jpegxlFixtureSummary          `json:"summary"`
	Groups    map[string]jpegxlFixtureGroup `json:"groups"`
}

type jpegxlFixtureSummary struct {
	TotalFiles int            `json:"totalFiles"`
	BySyntax   map[string]int `json:"bySyntax"`
}

type jpegxlFixtureGroup struct {
	TransferSyntaxUID  string              `json:"transferSyntaxUID"`
	TransferSyntaxName string              `json:"transferSyntaxName"`
	Files              []jpegxlFixtureFile `json:"files"`
}

type jpegxlFixtureFile struct {
	Path                      string                     `json:"path"`
	TransferSyntaxUID         string                     `json:"transferSyntaxUID"`
	TransferSyntaxName        string                     `json:"transferSyntaxName"`
	Rows                      uint16                     `json:"rows"`
	Columns                   uint16                     `json:"columns"`
	SamplesPerPixel           uint16                     `json:"samplesPerPixel"`
	PhotometricInterpretation string                     `json:"photometricInterpretation"`
	BitsAllocated             uint16                     `json:"bitsAllocated"`
	BitsStored                uint16                     `json:"bitsStored"`
	PixelRepresentation       uint16                     `json:"pixelRepresentation"`
	FrameCount                int                        `json:"frameCount"`
	Encapsulation             jpegxlFixtureEncapsulation `json:"encapsulation"`
}

type jpegxlFixtureEncapsulation struct {
	Encapsulated     bool  `json:"encapsulated"`
	FragmentCount    int   `json:"fragmentCount"`
	FragmentSizes    []int `json:"fragmentSizes"`
	OffsetTableBytes int   `json:"offsetTableBytes"`
}

func TestJPEGXLFixtureManifestLoads(t *testing.T) {
	manifest := loadJPEGXLFixtureManifest(t)
	if manifest.Summary.TotalFiles <= 0 {
		t.Fatalf("manifest totalFiles=%d, want > 0", manifest.Summary.TotalFiles)
	}
	for _, key := range []string{"JPEGXLLossless", "JPEGXLJPEGRecompression", "JPEGXL"} {
		if _, ok := manifest.Groups[key]; !ok {
			t.Fatalf("manifest group %s missing", key)
		}
	}
	if got := manifest.Summary.BySyntax["JPEGXL"]; got <= 0 {
		t.Fatalf("manifest JPEGXL count=%d, want > 0", got)
	}
}

func TestJPEGXLFixtureManifestCoversEveryPresentSyntax(t *testing.T) {
	manifest := loadJPEGXLFixtureManifest(t)
	for syntax, count := range manifest.Summary.BySyntax {
		group, ok := manifest.Groups[syntax]
		if !ok {
			t.Fatalf("summary syntax %s missing group", syntax)
		}
		if count == 0 {
			continue
		}
		if len(group.Files) == 0 {
			t.Fatalf("summary syntax %s count=%d but group has no files", syntax, count)
		}
	}
}

func TestDecodeJPEGXLDefaultRegistryStillReportsMissingAdapter(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{}, []byte("encoded"))

	_, err := pixeldata.NewMemoryRegistry().DecodeFrames(transfer.JPEGXL.UID, pixel, obj)
	if !errors.Is(err, pixeldata.ErrCodecNotFound) {
		t.Fatalf("DecodeFrames() error = %v, want ErrCodecNotFound", err)
	}
	var availability *pixeldata.CodecAvailabilityError
	if !errors.As(err, &availability) {
		t.Fatalf("DecodeFrames() error = %T %[1]v, want CodecAvailabilityError", err)
	}
	if availability.TransferSyntaxUID != transfer.JPEGXL.UID || !strings.Contains(availability.Hint, "no default decoder adapter") {
		t.Fatalf("availability = %#v, want JPEG XL missing-adapter diagnostic", availability)
	}
}

func TestDecodeJPEGXLUnsupportedPixelRepresentationNamesCondition(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{pixelRepresentation: 2}, []byte("encoded"))

	_, err := registry.DecodeFrames(transfer.JPEGXL.UID, pixel, obj)
	if !errors.Is(err, ErrUnsupportedMetadata) {
		t.Fatalf("DecodeFrames() error = %v, want ErrUnsupportedMetadata", err)
	}
	if !strings.Contains(err.Error(), transfer.JPEGXL.UID) || !strings.Contains(err.Error(), "PixelRepresentation=2") {
		t.Fatalf("DecodeFrames() error = %q, want syntax and unsupported condition", err)
	}
}

func loadJPEGXLFixtureManifest(t testing.TB) jpegxlFixtureManifest {
	t.Helper()
	data, err := os.ReadFile(jpegxlManifestPath())
	if err != nil {
		t.Fatalf("read JPEG XL manifest: %v", err)
	}
	var manifest jpegxlFixtureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse JPEG XL manifest: %v", err)
	}
	return manifest
}

func jpegxlRepoRoot() string {
	return filepath.Clean(filepath.Join("..", "..", ".."))
}

func jpegxlManifestPath() string {
	return filepath.Join(jpegxlRepoRoot(), "pixeldata", "codecfixture", "testdata", "codecs", "jpegxl_manifest.json")
}

func jpegxlFixtureRoot(t testing.TB, manifest jpegxlFixtureManifest) string {
	t.Helper()
	if manifest.SourceDir == "" {
		t.Fatal("JPEG XL manifest sourceDir is empty")
	}
	return filepath.Clean(filepath.Join(jpegxlRepoRoot(), manifest.SourceDir))
}

func skipIfJPEGXLFixturesUnavailable(t testing.TB, manifest jpegxlFixtureManifest) string {
	t.Helper()
	root := jpegxlFixtureRoot(t, manifest)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Skipf("JPEG XL fixtures unavailable at %s", root)
	}
	return root
}

func loadFixtureFile(t testing.TB, root string, fixture jpegxlFixtureFile) *object.File {
	t.Helper()
	path := filepath.Join(root, fixture.Path)
	file, err := object.OpenFile(path)
	if err != nil {
		t.Fatalf("open fixture %s: %v", fixture.Path, err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Fatalf("close fixture %s: %v", fixture.Path, err)
		}
	})
	return file
}

func sampleJPEGXLFixtureFiles(group jpegxlFixtureGroup) []jpegxlFixtureFile {
	if os.Getenv(jpegxlFullFixtureEnv) == "1" {
		return append([]jpegxlFixtureFile(nil), group.Files...)
	}
	switch len(group.Files) {
	case 0:
		return nil
	case 1:
		return append([]jpegxlFixtureFile(nil), group.Files[0])
	case 2:
		return append([]jpegxlFixtureFile(nil), group.Files...)
	default:
		return []jpegxlFixtureFile{
			group.Files[0],
			group.Files[len(group.Files)/2],
			group.Files[len(group.Files)-1],
		}
	}
}
