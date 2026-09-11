//go:build !windows

package dicomwebcli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicFileCreatesPrivateOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.dcm")
	if err := writeAtomicFile(path, func(w io.Writer) error {
		_, err := w.Write([]byte("synthetic DICOM payload"))
		return err
	}); err != nil {
		t.Fatalf("writeAtomicFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("mode=%#o, want 0600", gotMode)
	}
}

func TestEnsureOutputDirectoryCreatesPrivateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "output")
	if err := ensureOutputDirectory(path); err != nil {
		t.Fatalf("ensureOutputDirectory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o700 {
		t.Fatalf("mode=%#o, want 0700", gotMode)
	}
}
