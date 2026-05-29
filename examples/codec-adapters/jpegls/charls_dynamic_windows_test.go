//go:build (jpegls_charls || codecfull) && windows

package jpegls

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestWindowsCharLSLibraryNames(t *testing.T) {
	t.Setenv("DICOM_GO_CHARLS_LIBRARY", "")

	names := charlsLibraryNames()
	var want string
	switch runtime.GOARCH {
	case "amd64":
		want = "charls-2-x64.dll"
	case "arm64":
		want = "charls-2-arm64.dll"
	default:
		t.Skipf("no architecture-specific runtime for %s", runtime.GOARCH)
	}
	if !slices.Contains(names, want) {
		t.Fatalf("charlsLibraryNames() = %v, want %q", names, want)
	}
}

func TestWindowsCharLSLibraryOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "approved-charls.dll")
	t.Setenv("DICOM_GO_CHARLS_LIBRARY", override)

	names := charlsLibraryNames()
	if len(names) != 1 || names[0] != override {
		t.Fatalf("charlsLibraryNames() = %v, want [%q]", names, override)
	}
}

func TestWindowsCharLSDecoderUnavailableForCorruptLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt-charls.dll")
	if err := os.WriteFile(path, []byte("not a portable executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DICOM_GO_CHARLS_LIBRARY", path)

	_, err := openCharLSAPI()
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("openCharLSAPI() error = %v, want ErrDecoderUnavailable", err)
	}
}
