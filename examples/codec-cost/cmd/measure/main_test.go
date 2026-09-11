package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeasureCommandDocumentsMissingRuntime(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.json")
	code := run([]string{
		"-out", out,
		"-djxl", "/nonexistent/dicom-go-djxl",
		"-iterations", "1",
		"-corpus", dir,
	})
	if code != 0 {
		t.Fatalf("run exit = %d, want 0 with explicit skips", code)
	}
	payload, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "jpegxl-cli") || !strings.Contains(string(payload), "skips") {
		t.Fatalf("report missing skip evidence: %s", payload)
	}
}

func TestMeasureCommandRequireFailsClosed(t *testing.T) {
	code := run([]string{
		"-djxl", "/nonexistent/dicom-go-djxl",
		"-require", "jpegxl-cli",
		"-iterations", "1",
		"-corpus", t.TempDir(),
	})
	if code != 1 {
		t.Fatalf("run exit = %d, want 1", code)
	}
}

func TestMeasureCommandAbsFailureDoesNotSucceed(t *testing.T) {
	orig := absPath
	absPath = func(string) (string, error) {
		return "", errors.New("abs failed")
	}
	t.Cleanup(func() { absPath = orig })

	dir := t.TempDir()
	out := filepath.Join(dir, "report.json")
	code := run([]string{
		"-out", out,
		"-djxl", "/nonexistent/dicom-go-djxl",
		"-iterations", "1",
		"-corpus", dir,
	})
	if code != 1 {
		t.Fatalf("run exit = %d, want 1 when filepath.Abs fails", code)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("wrote report after Abs failure")
	}
}
