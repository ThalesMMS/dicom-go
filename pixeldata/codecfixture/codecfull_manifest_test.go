package codecfixture

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestSyntheticCorpusManifest(t *testing.T) {
	manifest, err := readCodecFullCorpusManifest(filepath.Join("testdata", "codecfull"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]codecFullCorpusFixture{}
	for _, f := range manifest.Fixtures {
		if f.CaseName != "" {
			if _, exists := byName[f.CaseName]; exists {
				t.Fatal("duplicate case")
			}
			byName[f.CaseName] = f
		}
	}
	for _, c := range CorpusCases() {
		t.Run(c.Name, func(t *testing.T) {
			f, exists := byName[c.Name]
			if !exists {
				t.Fatal("case missing from corpus manifest")
			}
			delete(byName, c.Name)
			encoded, err := c.Part10Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(encoded)) != f.SHA256 || fmt.Sprintf("%x", sha256.Sum256(bytes.Join(c.ExpectedFrames, nil))) != f.ReferenceSHA256 {
				t.Fatal("synthetic fixture or full expectation hash drift; explicit regeneration and review required")
			}
			meta, err := pixeldata.ExtractMetadata(c.Object())
			if err != nil {
				t.Fatal(err)
			}
			if corpusInput(c.Syntax.UID, meta) != f.Input {
				t.Fatal("synthetic input metadata drift")
			}
			expectation := string(c.ExpectedError)
			if expectation == "" {
				expectation = "decode-success"
			}
			if f.Expectation != expectation {
				t.Fatal("expected outcome drift")
			}
			if !reflect.DeepEqual(f.SamplePolicy, c.SamplePolicy) {
				t.Fatal("comparison policy drift")
			}
			registry, err := c.Registry()
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCase(registry, c); err != nil {
				t.Fatal(err)
			}
		})
	}
	if len(byName) != 0 {
		t.Fatal("manifest advertises absent synthetic case")
	}
}

func TestCorpusRejectsMetadataAndOracleDrift(t *testing.T) {
	root := filepath.Join("testdata", "codecfull")
	for _, name := range []string{"geometry", "signedness", "precision", "oracle provenance", "oracle frame", "oracle hash"} {
		t.Run(name, func(t *testing.T) {
			m, err := readCodecFullCorpusManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "geometry":
				m.Fixtures[0].Input.Columns++
			case "signedness":
				m.Fixtures[0].Input.Signed = !m.Fixtures[0].Input.Signed
			case "precision":
				m.Fixtures[0].Input.BitsStored--
			default:
				for i := range m.Fixtures {
					if m.Fixtures[i].Reconstruction == nil {
						continue
					}
					switch name {
					case "oracle provenance":
						m.Fixtures[i].Reconstruction.Backend = ""
					case "oracle frame":
						m.Fixtures[i].Reconstruction.Frames++
					case "oracle hash":
						m.Fixtures[i].ReferenceSHA256 = strings.Repeat("0", 64)
					}
					break
				}
			}
			if err := validateCodecFullCorpusManifest(root, m); err == nil {
				t.Fatal("manifest corruption accepted")
			}
		})
	}
}

func TestCodecFullCorpusManifest(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	if err := ValidateCodecFullReleaseEvidence(moduleRoot); err != nil {
		t.Fatal(err)
	}
}

func TestCorpusRejectsSourceBoundEvidenceDrift(t *testing.T) {
	root := filepath.Join("testdata", "codecfull")
	for _, name := range []string{"path", "hash", "negative bound", "excessive bound", "signed domain", "lossy flag", "layout", "missing reconstruction"} {
		t.Run(name, func(t *testing.T) {
			m, err := readCodecFullCorpusManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			var f *codecFullCorpusFixture
			for i := range m.Fixtures {
				if m.Fixtures[i].SourceSamples != nil {
					f = &m.Fixtures[i]
					break
				}
			}
			if f == nil {
				t.Fatal("source evidence missing")
			}
			switch name {
			case "path":
				f.SourceSamples.Path = "../outside.raw"
			case "hash":
				f.SourceSamples.SHA256 = strings.Repeat("0", 64)
			case "negative bound":
				f.SourceSamples.Near = -1
			case "excessive bound":
				f.SourceSamples.Near = 256
			case "signed domain":
				f.Input.Signed = true
			case "lossy flag":
				f.Lossy = !f.Lossy
			case "layout":
				f.Input.Frames++
			case "missing reconstruction":
				f.Reconstruction = nil
			}
			if err := validateSourceSamples(root, *f); err == nil {
				t.Fatal("invalid source-bound evidence accepted")
			}
		})
	}
}

func TestCodecFullCorpusManifestRejectsEscapingPaths(t *testing.T) {
	root := filepath.Join("testdata", "codecfull")
	tests := []struct {
		name   string
		mutate func(*codecFullCorpusManifest)
	}{
		{
			name: "fixture",
			mutate: func(manifest *codecFullCorpusManifest) {
				manifest.Fixtures[0].Path = "../outside.dcm"
			},
		},
		{
			name: "absolute fixture",
			mutate: func(manifest *codecFullCorpusManifest) {
				manifest.Fixtures[0].Path = filepath.Join(t.TempDir(), "outside.dcm")
			},
		},
		{
			name: "reference",
			mutate: func(manifest *codecFullCorpusManifest) {
				manifest.Fixtures[0].ReferencePath = "../outside.raw"
			},
		},
		{
			name: "license",
			mutate: func(manifest *codecFullCorpusManifest) {
				manifest.Sources[0].LicensePath = "../LICENSE"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := readCodecFullCorpusManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&manifest)
			err = validateCodecFullCorpusManifest(root, manifest)
			if err == nil || !strings.Contains(err.Error(), "path escapes codecfull corpus") {
				t.Fatalf("validation error = %v, want confined-path rejection", err)
			}
		})
	}
}
