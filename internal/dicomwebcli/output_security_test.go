package dicomwebcli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicFileCreatesOutputAndCleansSpool(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(directory, "result.dcm")
	want := []byte("synthetic DICOM payload")

	err := writeAtomicFile(path, func(w io.Writer) error {
		_, err := w.Write(want)
		return err
	})
	if err != nil {
		t.Fatalf("writeAtomicFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content=%q, want %q", got, want)
	}
	dicomwebCLITestAssertNoSpoolFiles(t, directory)
}

func TestWriteAtomicFileDoesNotOverwriteCollision(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "result.dcm")
	want := []byte("existing")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := writeAtomicFile(path, func(w io.Writer) error {
		_, err := w.Write([]byte("replacement"))
		return err
	})
	var input *inputError
	if !errors.As(err, &input) {
		t.Fatalf("error=%v, want inputError", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("collision replaced destination: got %q want %q", got, want)
	}
	dicomwebCLITestAssertNoSpoolFiles(t, directory)
}

func TestWriteAtomicFileRemovesSpoolOnWriterFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "result.dcm")
	wantErr := errors.New("synthetic writer failure")

	err := writeAtomicFile(path, func(w io.Writer) error {
		if _, writeErr := w.Write([]byte("partial")); writeErr != nil {
			return writeErr
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error=%v, want not exist", statErr)
	}
	dicomwebCLITestAssertNoSpoolFiles(t, directory)
}

func TestEnsureOutputDirectoryCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "output")
	if err := ensureOutputDirectory(path); err != nil {
		t.Fatalf("ensureOutputDirectory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", path)
	}
}

func TestWriteJSONUsesAtomicNoOverwriteOutput(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "response.json")
	var stdout bytes.Buffer
	if err := writeJSON(&stdout, path, map[string]string{"status": "ok"}); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty", stdout.String())
	}
	if err := writeJSON(&stdout, path, map[string]string{"status": "replaced"}); err == nil {
		t.Fatal("second writeJSON succeeded, want collision error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "{\"status\":\"ok\"}\n" {
		t.Fatalf("content=%q", got)
	}
	dicomwebCLITestAssertNoSpoolFiles(t, directory)
}

func dicomwebCLITestAssertNoSpoolFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".dicomweb-output-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("spool files remain: %v", matches)
	}
}
