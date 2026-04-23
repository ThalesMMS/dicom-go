package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunGeneratesOutputFile(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "dicom.dic")
	outputPath := filepath.Join(t.TempDir(), "std_gen.go")

	input := strings.Join([]string{
		"(0010,0010)\tPN\tPatientName\t1\tDICOM",
		"(0010,0020)\tLO\tPatientID\t1\tDICOM",
	}, "\n")
	if err := os.WriteFile(inputPath, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"-input", inputPath, "-output", outputPath}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0, stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "PatientName") || !strings.Contains(text, "PatientID") {
		t.Fatalf("generated output missing expected entries:\n%s", text)
	}
	if !strings.Contains(stdout.String(), "generated "+outputPath+" from "+inputPath+" (2 entries)") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunReturnsUsageErrorForUnexpectedArgs(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"extra-arg"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("run() exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage: dicomdictgen [flags]") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunProducesDeterministicOutputForSameInput(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "dicom.dic")
	outputPathA := filepath.Join(t.TempDir(), "std_a.go")
	outputPathB := filepath.Join(t.TempDir(), "std_b.go")

	input := strings.Join([]string{
		"# File created on 2026-03-28 10:26:30 by Test.",
		"(0010,0010)\tPN\tPatientName\t1\tDICOM",
		"(0008,0060)\tCS\tModality\t1\tDICOM",
	}, "\n")
	if err := os.WriteFile(inputPath, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run([]string{"-input", inputPath, "-output", outputPathA}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("first run exit code = %d, stderr=%q", exitCode, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := run([]string{"-input", inputPath, "-output", outputPathB}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("second run exit code = %d, stderr=%q", exitCode, stderr.String())
	}

	dataA, err := os.ReadFile(outputPathA)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(outputPathB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dataA, dataB) {
		t.Fatal("repeated generation produced different output bytes")
	}
}

func TestDictionaryTimestampParsesDCMTKHeader(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "dicom.dic")
	input := "# File created on 2026-03-28 10:26:30 by Test.\n(0010,0010)\tPN\tPatientName\t1\tDICOM\n"
	if err := os.WriteFile(inputPath, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := dictionaryTimestamp(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 28, 10, 26, 30, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("dictionaryTimestamp() = %s, want %s", got, want)
	}
}
