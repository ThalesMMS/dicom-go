package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata/codecprofile"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		check    func(t *testing.T, stdout, stderr *bytes.Buffer)
	}{
		{
			name:     "emits valid manifest",
			wantCode: 0,
			check: func(t *testing.T, stdout, stderr *bytes.Buffer) {
				var manifest codecprofile.ProfileManifest
				if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
					t.Fatal(err)
				}
				if err := manifest.Validate(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "require ready emits qualified manifest",
			args:     []string{"-require-ready"},
			wantCode: 0,
			check: func(t *testing.T, stdout, stderr *bytes.Buffer) {
				var manifest codecprofile.ProfileManifest
				if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
					t.Fatal(err)
				}
				if !manifest.ClinicalReleaseReady || stderr.Len() != 0 {
					t.Fatalf("manifest ready=%t stderr=%q", manifest.ClinicalReleaseReady, stderr.String())
				}
			},
		},
		{
			name:     "validate only is silent",
			args:     []string{"-validate-only"},
			wantCode: 0,
			check: func(t *testing.T, stdout, stderr *bytes.Buffer) {
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("validate-only output stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			},
		},
		{
			name:     "rejects unexpected arguments",
			args:     []string{"typo"},
			wantCode: 2,
			check: func(t *testing.T, stdout, stderr *bytes.Buffer) {
				if stdout.Len() != 0 || !strings.Contains(stderr.String(), "unexpected arguments") {
					t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			},
		},
		{
			name:     "require ready rejects invalid evidence root",
			args:     []string{"-require-ready", "-evidence-root", t.TempDir()},
			wantCode: 1,
			check: func(t *testing.T, stdout, stderr *bytes.Buffer) {
				if stdout.Len() != 0 || !strings.Contains(stderr.String(), "go.mod") {
					t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != test.wantCode {
				t.Fatalf("run() code = %d, want %d, stderr=%s", code, test.wantCode, stderr.String())
			}
			test.check(t, &stdout, &stderr)
		})
	}
}

func TestValidateModuleRootRequiresExactModuleDirective(t *testing.T) {
	tests := []struct {
		name    string
		module  string
		wantErr bool
	}{
		{name: "exact", module: "github.com/ThalesMMS/dicom-go"},
		{name: "prefix collision", module: "github.com/ThalesMMS/dicom-go-evil", wantErr: true},
		{name: "nested module", module: "github.com/ThalesMMS/dicom-go/examples/codecfull", wantErr: true},
		{name: "module appears in comment", module: "example.invalid/module // github.com/ThalesMMS/dicom-go", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			data := []byte("module " + test.module + "\n\ngo 1.22\n")
			if err := os.WriteFile(filepath.Join(root, "go.mod"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			err := validateModuleRoot(root)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateModuleRoot() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
