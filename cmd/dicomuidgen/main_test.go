package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGeneratesOutputFile(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "uids.tsv")
	outputPath := filepath.Join(t.TempDir(), "std_gen.go")

	input := strings.Join([]string{
		"# UID\tName\tKeyword\tType\tRetired",
		"1.2.840.10008.1.1\tVerification SOP Class\tVerification\tSOP Class\tfalse",
		"1.2.840.10008.1.2.1\tExplicit VR Little Endian\tExplicitVRLittleEndian\tTransfer Syntax\tfalse",
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
	if !strings.Contains(text, "Verification") || !strings.Contains(text, "ExplicitVRLittleEndian") {
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
	if !strings.Contains(stderr.String(), "Usage: dicomuidgen [flags]") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunProducesDeterministicOutputForSameInput(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "uids.tsv")
	outputPathA := filepath.Join(t.TempDir(), "std_a.go")
	outputPathB := filepath.Join(t.TempDir(), "std_b.go")

	input := strings.Join([]string{
		"# UID\tName\tKeyword\tType\tRetired",
		"1.2.840.10008.1.2.1\tExplicit VR Little Endian\tExplicitVRLittleEndian\tTransfer Syntax\tfalse",
		"1.2.840.10008.1.1\tVerification SOP Class\tVerification\tSOP Class\tfalse",
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
