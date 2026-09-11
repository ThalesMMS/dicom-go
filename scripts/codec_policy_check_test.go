package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodecPolicyRecursiveManifests(t *testing.T) {
	bash := os.Getenv("DICOM_GO_POLICY_BASH")
	if bash == "" {
		var err error
		bash, err = exec.LookPath("bash")
		if err != nil {
			t.Skip("codec policy fixtures require Bash")
		}
	}
	for _, tc := range []struct {
		name, path, content string
		forbidden           bool
	}{
		{name: "clean"},
		{"adapter module", "examples/codec-adapters/jpegxl/go.mod", "\nrequire example.org/gpl/codec v1.0.0\n", true},
		{"nested module", "examples/codec-adapters/new codec/deep/more/go.mod", "module example.org/codec\nrequire example.org/lgpl/codec v1.0.0\n", true},
		{"nested sums", "examples/codecfull/deep/more/go.sum", "example.org/agpl/codec v1.0.0 h1:synthetic\n", true},
		{"large manifest early match", "examples/codecfull/deep/go.sum", "example.org/agpl/codec v1.0.0 h1:synthetic\n" + strings.Repeat("example.org/permissive/codec v1.0.0 h1:synthetic\n", 10000), true},
		{"hidden directory", "examples/codec-adapters/.hidden/deep/go.mod", "module example.org/grok/codec\n", true},
		{"allowed nested module", "examples/codecfull/nested codec/deep/go.mod", "module example.org/permissive\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{
				"scripts/codec_policy_check.sh", "docs/CODEC_DEPENDENCY_POLICY.md", "docs/JPEGXL_DECODER_STRATEGY.md",
				"go.mod", "go.sum", "examples/codec-adapters/jpeg2000/go.mod",
				"examples/codec-adapters/jpegls/go.mod", "examples/codec-adapters/jpegxl/go.mod", "examples/codecfull/go.mod",
			} {
				data, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(name)))
				if err != nil {
					t.Fatal(err)
				}
				writePolicyFixture(t, filepath.Join(root, filepath.FromSlash(name)), data)
			}
			if tc.path != "" {
				path := filepath.Join(root, filepath.FromSlash(tc.path))
				data, err := os.ReadFile(path)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				writePolicyFixture(t, path, append(data, tc.content...))
			}
			// A policy scan must never invoke either Unix find or Windows FIND.EXE.
			writePolicyFixture(t, filepath.Join(root, "tools", "find"), []byte("#!/bin/sh\necho unexpected-find >&2\nexit 99\n"))
			cmd := exec.Command(bash, "-c", `export PATH="$PWD/tools:$PATH"; exec "$BASH" scripts/codec_policy_check.sh`)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if strings.Contains(string(output), "unexpected-find") {
				t.Fatalf("invoked find: %s", output)
			}
			if tc.forbidden {
				if err == nil || !strings.Contains(string(output), "forbidden/copyleft pattern") || !strings.Contains(string(output), tc.path) {
					t.Fatalf("want rejection of %s: err=%v\n%s", tc.path, err, output)
				}
			} else if err != nil || !strings.Contains(string(output), "codec-policy-check: OK") {
				t.Fatalf("want success: err=%v\n%s", err, output)
			}
		})
	}
}

func writePolicyFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
