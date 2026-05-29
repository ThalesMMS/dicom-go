package dicomdir_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/dicomdir"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
)

// TestDICOMDIRPydicomInterop validates an authored file-set with an independent
// reader. It is opt-in so ordinary offline test runs do not depend on Python.
func TestDICOMDIRPydicomInterop(t *testing.T) {
	if os.Getenv("DICOM_GO_PYDICOM_DICOMDIR") != "1" {
		t.Skip("set DICOM_GO_PYDICOM_DICOMDIR=1 to run the pydicom file-set check")
	}
	python := strings.TrimSpace(os.Getenv("DICOM_GO_PYTHON"))
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Fatal("python3 is required when DICOM_GO_PYDICOM_DICOMDIR=1")
		}
	}
	if output, err := exec.Command(python, "-c", "import pydicom; from pydicom.fileset import FileSet").CombinedOutput(); err != nil {
		t.Fatalf("pydicom is required when DICOM_GO_PYDICOM_DICOMDIR=1: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	root, fileSet, _ := filesetSyntheticFileSet(t)
	if _, err := dicomdir.CommitDICOMDIR(context.Background(), fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("CommitDICOMDIR: %v", err)
	}
	path := filepath.Join(root, "DICOMDIR")
	const script = `
import sys
import pydicom
from pydicom.fileset import FileSet

path = sys.argv[1]
ds = pydicom.dcmread(path)
assert str(ds.file_meta.MediaStorageSOPClassUID) == "1.2.840.10008.1.3.10"
assert str(ds.file_meta.TransferSyntaxUID) == "1.2.840.10008.1.2.1"
assert len(ds.DirectoryRecordSequence) == 10
assert int(ds.OffsetOfTheFirstDirectoryRecordOfTheRootDirectoryEntity) > 132
assert int(ds.OffsetOfTheLastDirectoryRecordOfTheRootDirectoryEntity) > 132
fs = FileSet(path)
assert len(fs) == 4
assert len({str(instance.SOPInstanceUID) for instance in fs}) == 4
print("pydicom DICOMDIR OK")
`
	if output, err := exec.CommandContext(context.Background(), python, "-c", script, path).CombinedOutput(); err != nil {
		t.Fatalf("pydicom rejected authored DICOMDIR: %v (%s)", err, strings.TrimSpace(string(output)))
	}
}

func TestFileSetStatisticsIncludeInstancesAndSourceBytes(t *testing.T) {
	root, fileSet, specs := filesetSyntheticFileSet(t)
	var wantBytes int64
	for _, spec := range specs {
		path := filepath.Join(append([]string{root}, []string(spec.fileID)...)...)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat source fixture: %v", err)
		}
		wantBytes += info.Size()
	}
	statistics := fileSet.Statistics()
	if statistics.Files != len(specs) || statistics.Instances != len(specs) || statistics.Bytes != wantBytes {
		t.Fatalf("Statistics() files/instances/bytes = %d/%d/%d, want %d/%d/%d",
			statistics.Files, statistics.Instances, statistics.Bytes, len(specs), len(specs), wantBytes)
	}
}

func TestWriteAndParseEmptyFileSet(t *testing.T) {
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	var encoded bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &encoded, fileSet, dicomdir.WriteOptions{}); err != nil {
		t.Fatalf("WriteDICOMDIR: %v", err)
	}
	file, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	defer file.Close()
	first, ok := file.Dataset.Get(filesetTagFirstRootOffset)
	raw, rawOK := first.RawBytes()
	if !ok || !rawOK || len(raw) != 4 || binary.LittleEndian.Uint32(raw) != 0 {
		t.Fatal("empty DICOMDIR omitted or misencoded First Root Directory Record offset")
	}
	path := filepath.Join(root, "DICOMDIR")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write empty DICOMDIR: %v", err)
	}
	parsed, err := dicomdir.ParseFileSet(path)
	if err != nil {
		t.Fatalf("ParseFileSet(empty): %v", err)
	}
	if statistics := parsed.Statistics(); statistics.Files != 0 || statistics.Instances != 0 || statistics.Bytes != 0 {
		t.Fatalf("empty Statistics() = %+v", statistics)
	}
}

func TestScanReportsMissingRequiredSelectionKey(t *testing.T) {
	root, _, specs := filesetSyntheticFileSet(t)
	path := filepath.Join(append([]string{root}, []string(specs[0].fileID)...)...)
	file, err := object.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if !file.Dataset.Remove(tags.StudyInstanceUID) {
		t.Fatal("fixture has no Study Instance UID")
	}
	var rewritten bytes.Buffer
	if err := object.WriteFile(&rewritten, file); err != nil {
		t.Fatalf("rewrite source without Study Instance UID: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	if err := os.WriteFile(path, rewritten.Bytes(), 0o600); err != nil {
		t.Fatalf("replace source: %v", err)
	}

	scanned, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	report, err := scanned.Scan(context.Background(), dicomdir.ScanOptions{Policy: dicomdir.EntrySkip})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Skipped != 1 || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != dicomdir.DiagnosticMissingKey {
		t.Fatalf("missing-key ScanReport = %+v", report)
	}
	if err := scanned.Add(context.Background(), specs[0].fileID); !errors.Is(err, dicomdir.ErrMissingRequiredKey) || !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("Add missing-key error = %v", err)
	}
}

func TestNewFileSetRejectsInvalidOIDStructure(t *testing.T) {
	for _, uid := range []string{"1.02.3", "3.1.2", "1", "1.40.1"} {
		t.Run(uid, func(t *testing.T) {
			if _, err := dicomdir.NewFileSet(t.TempDir(), dicomdir.Options{FileSetUID: uid}); !errors.Is(err, dicomdir.ErrInvalidOptions) {
				t.Fatalf("NewFileSet(FileSetUID=%q) error = %v, want ErrInvalidOptions", uid, err)
			}
		})
	}
}

func TestScanSkipsDICOMDIRAndConfiguredDescriptor(t *testing.T) {
	root, authored, specs := filesetSyntheticFileSet(t)
	if _, err := dicomdir.CommitDICOMDIR(context.Background(), authored, dicomdir.WriteOptions{}); err != nil {
		t.Fatalf("CommitDICOMDIR: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README01"), []byte("descriptor\n"), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	scanned, err := dicomdir.NewFileSet(root, dicomdir.Options{
		FileSetUID: filesetFixedUID, DescriptorFileID: dicomdir.FileID{"README01"},
	})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	report, err := scanned.Scan(context.Background(), dicomdir.ScanOptions{Policy: dicomdir.EntryReject})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Added != len(specs) || report.Skipped != 0 {
		t.Fatalf("Scan report = %+v, want %d DICOM instances and no skipped entries", report, len(specs))
	}
}
