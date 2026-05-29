//go:build codecfull

package codecfull

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata/codecprofile"
)

func TestCodecFullRuntimeAndRegistry(t *testing.T) {
	if err := ValidateRuntime(); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, syntax := range codecprofile.RequiredCodecFullTransferSyntaxes() {
		if !syntax.Encapsulated || strings.Contains(syntax.Name, "Encapsulated Uncompressed") {
			continue
		}
		if _, ok := registry.GetCodec(syntax.UID); !ok {
			t.Errorf("codecfull registry is missing %s (%s)", syntax.Name, syntax.UID)
		}
	}
}

func TestCodecFullRuntimeFailsClosed(t *testing.T) {
	badRuntime := filepath.Join(t.TempDir(), "not-a-codec-runtime")
	if err := os.WriteFile(badRuntime, []byte("not a qualified codec runtime"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		variable string
		want     string
	}{
		{name: "CharLS", variable: "DICOM_GO_CHARLS_LIBRARY", want: "codecfull requires CharLS"},
		{name: "OpenJPEG", variable: "DICOM_GO_OPENJPEG_DECOMPRESS", want: "codecfull requires OpenJPEG"},
		{name: "OpenJPH", variable: "DICOM_GO_OPENJPH_EXPAND", want: "codecfull requires OpenJPH"},
		{name: "djxl", variable: "DICOM_GO_DJXL", want: "codecfull requires djxl"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := testCommand(t, "TestCodecFullRuntimeFailsClosedSubprocess")
			command.Env = append(
				command.Env,
				"CODECFULL_FAIL_CLOSED_SUBPROCESS=1",
				"CODECFULL_BAD_RUNTIME_VARIABLE="+test.variable,
				"CODECFULL_BAD_RUNTIME_PATH="+badRuntime,
				"CODECFULL_BAD_RUNTIME_WANT="+test.want,
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("fail-closed subprocess failed: %v\n%s", err, output)
			}
		})
	}
}

func TestCodecFullRuntimeFailsClosedSubprocess(t *testing.T) {
	if os.Getenv("CODECFULL_FAIL_CLOSED_SUBPROCESS") != "1" {
		t.Skip("subprocess helper")
	}
	variable := os.Getenv("CODECFULL_BAD_RUNTIME_VARIABLE")
	if variable == "" {
		t.Fatal("missing bad runtime variable")
	}
	if err := os.Setenv(variable, os.Getenv("CODECFULL_BAD_RUNTIME_PATH")); err != nil {
		t.Fatal(err)
	}
	err := ValidateRuntime()
	if err == nil {
		t.Fatal("codecfull runtime preflight unexpectedly succeeded")
	}
	if want := os.Getenv("CODECFULL_BAD_RUNTIME_WANT"); !strings.Contains(err.Error(), want) {
		t.Fatalf("ValidateRuntime() error = %v, want %q", err, want)
	}
}
