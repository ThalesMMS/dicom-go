package codecfixture

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCodecFullCorpusManifest(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	if err := ValidateCodecFullReleaseEvidence(moduleRoot); err != nil {
		t.Fatal(err)
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
