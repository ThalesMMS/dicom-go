package dicomdir_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomdir"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestOpenReferencedFileRejectsSymlinkRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "IMAGE001"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	linkRoot := filepath.Join(linkParent, "MEDIA")
	if err := os.Symlink(outside, linkRoot); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	file, _, err := dicomdir.OpenReferencedFile(linkRoot, dicomdir.Reference{FileID: []string{"IMAGE001"}})
	if file != nil {
		_ = file.Close()
		t.Fatal("OpenReferencedFile returned a file through a symlink root")
	}
	if err == nil {
		t.Fatal("OpenReferencedFile accepted a symlink root")
	}
}

func TestFileSetRejectsCaseMismatchedPhysicalName(t *testing.T) {
	root := t.TempDir()
	writeTestDICOM(t, filepath.Join(root, "image001"), "1.2.826.0.1.3680043.10.543.625.700")
	fileSet := newTestFileSet(t, root, dicomdir.Options{})
	if err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001")); !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("Add error = %v, want ErrInvalidRecord for case-mismatched physical name", err)
	}
}

func TestFileSetRechecksCasingAfterRename(t *testing.T) {
	root := t.TempDir()
	writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.7001")
	writeTestDICOM(t, filepath.Join(root, "IMAGE002"), "1.2.826.0.1.3680043.10.543.625.7002")
	fileSet := newTestFileSet(t, root, dicomdir.Options{})
	if err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add(IMAGE001): %v", err)
	}
	if err := os.Rename(filepath.Join(root, "IMAGE002"), filepath.Join(root, "image002")); err != nil {
		t.Fatal(err)
	}
	if err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE002")); !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("Add(renamed IMAGE002) error = %v, want ErrInvalidRecord", err)
	}
}

func TestFileSetCopiesCallerSelectorOptions(t *testing.T) {
	root := t.TempDir()
	writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.7003")
	handlerCalls := 0
	options := dicomdir.Options{IndexOptions: index.Options{Selectors: []index.Selector{{
		ID: "caller.patient_id", Tag: core.NewTag(0x0010, 0x0020),
		Handle: func(context.Context, index.SelectedElement) error {
			handlerCalls++
			return nil
		},
	}}}}
	fileSet := newTestFileSet(t, root, options)
	options.IndexOptions.Selectors[0].ID = "invalid selector id"
	if err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add after caller mutation: %v", err)
	}
	if handlerCalls != 1 {
		t.Fatalf("selector handler calls = %d, want 1 from frozen options", handlerCalls)
	}
}

func TestCommitDICOMDIRHonorsCancellationDuringReadBack(t *testing.T) {
	root := t.TempDir()
	writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.7004")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handlerCalls := 0
	fileSet := newTestFileSet(t, root, dicomdir.Options{IndexOptions: index.Options{Selectors: []index.Selector{{
		ID: "cancel.readback", Tag: core.NewTag(0x0010, 0x0020),
		Handle: func(context.Context, index.SelectedElement) error {
			handlerCalls++
			// Initial Add plus the two source revalidations in WriteDICOMDIR
			// precede the strict ParseFileSet read-back performed by Commit.
			if handlerCalls == 4 {
				cancel()
			}
			return nil
		},
	}}}})
	if err := fileSet.Add(ctx, mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := dicomdir.CommitDICOMDIR(ctx, fileSet, dicomdir.WriteOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitDICOMDIR error = %v after %d handler calls, want context.Canceled", err, handlerCalls)
	}
	if _, err := os.Lstat(filepath.Join(root, "DICOMDIR")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled commit published DICOMDIR: %v", err)
	}
}

func TestFileSetRejectsReservedDescriptorAndFileSetIDSpace(t *testing.T) {
	root := t.TempDir()
	if _, err := dicomdir.NewFileSet(root, dicomdir.Options{
		FileSetUID: filesetFixedUID, FileSetID: "TEST_SET", DescriptorFileID: dicomdir.FileID{"DICOMDIR"},
	}); !errors.Is(err, dicomdir.ErrInvalidFileID) {
		t.Fatalf("reserved descriptor error = %v, want ErrInvalidFileID", err)
	}
	if _, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID, FileSetID: "BAD ID"}); !errors.Is(err, dicomdir.ErrInvalidOptions) {
		t.Fatalf("File-set ID with space error = %v, want ErrInvalidOptions", err)
	}
}

func TestFileSetAddPreservesIndexResourceLimit(t *testing.T) {
	root := t.TempDir()
	writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.701")
	fileSet := newTestFileSet(t, root, dicomdir.Options{
		IndexOptions: index.Options{Limits: index.Limits{MaxTotalBytes: 132}},
	})
	err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001"))
	if !errors.Is(err, dicomdir.ErrResourceLimit) {
		t.Fatalf("Add error = %v, want ErrResourceLimit", err)
	}
}

func TestFileSetRejectsFileSetUIDReusedBySource(t *testing.T) {
	root := t.TempDir()
	writeTestDICOM(t, filepath.Join(root, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.703")
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{
		FileSetUID: "1.2.826.0.1.3680043.10.543.625.10", FileSetID: "TEST_SET",
	})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	if err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001")); !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("Add error = %v, want ErrInvalidRecord for reused File-set UID", err)
	}
}

func TestFileSetValidateDetectsChangedSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "IMAGE001")
	writeTestDICOM(t, path, "1.2.826.0.1.3680043.10.543.625.702")
	fileSet := newTestFileSet(t, root, dicomdir.Options{})
	if err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	report, err := fileSet.Validate(context.Background())
	if !errors.Is(err, dicomdir.ErrSourceChanged) {
		t.Fatalf("Validate error = %v, want ErrSourceChanged", err)
	}
	if report.Valid || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != dicomdir.DiagnosticSourceChanged {
		t.Fatalf("Validate report = %+v, want one source_changed diagnostic", report)
	}
}

func TestFileSetValidateReportsChangedSourceIndex(t *testing.T) {
	root := t.TempDir()
	fileSet := newTestFileSet(t, root, dicomdir.Options{})
	for index, name := range []string{"IMAGE001", "IMAGE002"} {
		writeTestDICOM(t, filepath.Join(root, name), "1.2.826.0.1.3680043.10.543.625.710"+string(rune('1'+index)))
		if err := fileSet.Add(context.Background(), mustFileID(t, name)); err != nil {
			t.Fatalf("Add(%s): %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(root, "IMAGE002")); err != nil {
		t.Fatal(err)
	}
	report, err := fileSet.Validate(context.Background())
	if !errors.Is(err, dicomdir.ErrSourceChanged) || len(report.Diagnostics) != 1 || report.Diagnostics[0].SourceIndex != 1 {
		t.Fatalf("Validate = %+v, %v; want source_changed at SourceIndex 1", report, err)
	}
}

func TestFileSetValidateReportsChangedNestedDirectoryIndex(t *testing.T) {
	root := t.TempDir()
	fileSet := newTestFileSet(t, root, dicomdir.Options{})
	for index, directory := range []string{"DIR00001", "DIR00002"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestDICOM(t, filepath.Join(root, directory, "IMAGE001"), "1.2.826.0.1.3680043.10.543.625.712"+string(rune('1'+index)))
		if err := fileSet.Add(context.Background(), mustFileID(t, directory, "IMAGE001")); err != nil {
			t.Fatalf("Add(%s): %v", directory, err)
		}
	}
	if err := os.Rename(filepath.Join(root, "DIR00002"), filepath.Join(root, "dir00002")); err != nil {
		t.Fatal(err)
	}
	report, err := fileSet.Validate(context.Background())
	if !errors.Is(err, dicomdir.ErrSourceChanged) || len(report.Diagnostics) != 1 || report.Diagnostics[0].SourceIndex != 1 {
		t.Fatalf("Validate = %+v, %v; want nested source_changed at SourceIndex 1", report, err)
	}
}

func TestFileSetValidateDetectsConflictingSelectionKeys(t *testing.T) {
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID, FileSetID: "TEST_SET"})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	base := filesetInstanceSpec{
		fileID:    dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
		patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
		studyUID: "1.2.826.0.1.3680043.10.543.625.73", studyID: "A1", studyDate: "20260101", studyTime: "101010",
		seriesUID: "1.2.826.0.1.3680043.10.543.625.73.1", seriesNo: "1", modality: "CT",
		sopUID: "1.2.826.0.1.3680043.10.543.625.73.1.1", instanceNo: "1",
	}
	conflicting := base
	conflicting.fileID = dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0002"}
	conflicting.sopUID = "1.2.826.0.1.3680043.10.543.625.73.1.2"
	conflicting.instanceNo = "2"
	conflicting.studyDate = "20260102"
	for _, spec := range []filesetInstanceSpec{base, conflicting} {
		filesetWriteInstance(t, root, spec)
		if err := fileSet.Add(context.Background(), spec.fileID); err != nil {
			t.Fatalf("Add(%v): %v", spec.fileID, err)
		}
	}

	report, err := fileSet.Validate(context.Background())
	if !errors.Is(err, dicomdir.ErrInvalidRecord) || report.Valid {
		t.Fatalf("Validate = %+v, %v; want invalid conflicting selection keys", report, err)
	}
}

func TestFileSetRejectsInvalidDirectorySelectionValue(t *testing.T) {
	tests := []struct {
		name  string
		tag   core.Tag
		vr    core.VR
		value string
	}{
		{name: "invalid date", tag: core.NewTag(0x0008, 0x0020), vr: core.VRDA, value: "20260230"},
		{name: "invalid instance number", tag: core.NewTag(0x0020, 0x0013), vr: core.VRIS, value: "ABC"},
		{name: "UID reused across roles", tag: core.NewTag(0x0020, 0x000E), vr: core.VRUI, value: "1.2.826.0.1.3680043.10.543.625.10"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "IMAGE001")
			file := testDICOMFile("1.2.826.0.1.3680043.10.543.625.72" + string(rune('1'+index)))
			file.Dataset.Put(testStringElement(test.tag, test.vr, test.value))
			var encoded bytes.Buffer
			if err := object.WriteFile(&encoded, file); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			fileSet := newTestFileSet(t, root, dicomdir.Options{})
			err := fileSet.Add(context.Background(), mustFileID(t, "IMAGE001"))
			if !errors.Is(err, dicomdir.ErrInvalidRecord) {
				t.Fatalf("Add error = %v, want ErrInvalidRecord", err)
			}
		})
	}
}

func TestWriteDICOMDIRPreservesMultiValueSpecificCharacterSet(t *testing.T) {
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID, FileSetID: "TEST_SET"})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	spec := filesetInstanceSpec{
		fileID:               dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
		specificCharacterSet: `ISO 2022 IR 6\ISO 2022 IR 87`, patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
		studyUID: "1.2.826.0.1.3680043.10.543.625.71", studyID: "A1", studyDate: "20260101", studyTime: "101010",
		seriesUID: "1.2.826.0.1.3680043.10.543.625.71.1", seriesNo: "1", modality: "CT",
		sopUID: "1.2.826.0.1.3680043.10.543.625.71.1.1", instanceNo: "1",
	}
	filesetWriteInstance(t, root, spec)
	if err := fileSet.Add(context.Background(), spec.fileID); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var encoded bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &encoded, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("WriteDICOMDIR: %v", err)
	}
	items, ok := filesetReadPart10(t, encoded.Bytes()).Dataset.GetSequence(filesetTagDirectorySequence)
	if !ok {
		t.Fatal("generated DICOMDIR has no Directory Record Sequence")
	}
	for _, item := range items {
		if recordType, _ := item.GetString(filesetTagRecordType); recordType == "PATIENT" {
			values, ok := item.GetStrings(core.NewTag(0x0008, 0x0005))
			if !ok || len(values) != 2 || values[0] != "ISO 2022 IR 6" || values[1] != "ISO 2022 IR 87" {
				t.Fatalf("Specific Character Set = %v, %v; want two preserved values", values, ok)
			}
			return
		}
	}
	t.Fatal("generated DICOMDIR has no PATIENT record")
}

func TestParseFileSetAppliesCurrentRecordInUseSemantics(t *testing.T) {
	tests := []struct {
		name    string
		inUse   uint16
		wantErr bool
	}{
		{name: "zero is forbidden", inUse: 0, wantErr: true},
		{name: "reserved nonzero is active", inUse: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, fileSet, _ := filesetSyntheticFileSet(t)
			data := filesetWriteBytes(t, fileSet)
			file := filesetReadPart10(t, data)
			items, ok := file.Dataset.GetSequence(filesetTagDirectorySequence)
			if !ok {
				t.Fatal("fixture has no directory records")
			}
			for _, item := range items {
				if recordType, _ := item.GetString(filesetTagRecordType); recordType == "IMAGE" {
					offset, set := item.ItemOffset()
					if !set {
						t.Fatal("IMAGE record has no ItemOffset")
					}
					_, value := filesetElementValueRange(t, data, int(offset), filesetTagRecordInUse)
					binary.LittleEndian.PutUint16(value, test.inUse)
					break
				}
			}
			path := filesetWriteDICOMDIRFixture(t, root, data)
			_, err := dicomdir.ParseFileSet(path)
			if (err != nil) != test.wantErr {
				t.Fatalf("ParseFileSet error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestParseFileSetRejectsSymlinkAndNonReservedName(t *testing.T) {
	root, fileSet, _ := filesetSyntheticFileSet(t)
	data := filesetWriteBytes(t, fileSet)
	realPath := filepath.Join(root, "REALDIR1")
	if err := os.WriteFile(realPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dicomdir.ParseFileSet(realPath); !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("ParseFileSet(non-DICOMDIR) error = %v, want ErrInvalidRecord", err)
	}
	linkPath := filepath.Join(root, "DICOMDIR")
	if err := os.Symlink(realPath, linkPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := dicomdir.ParseFileSet(linkPath); !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("ParseFileSet(symlink) error = %v, want ErrInvalidRecord", err)
	}
}

func TestParseFileSetRejectsPhysicalDICOMDIRCaseMismatch(t *testing.T) {
	root, fileSet, _ := filesetSyntheticFileSet(t)
	lowerPath := filepath.Join(root, "dicomdir")
	if err := os.WriteFile(lowerPath, filesetWriteBytes(t, fileSet), 0o600); err != nil {
		t.Fatal(err)
	}
	upperPath := filepath.Join(root, "DICOMDIR")
	lowerInfo, lowerErr := os.Stat(lowerPath)
	upperInfo, upperErr := os.Stat(upperPath)
	if lowerErr != nil || upperErr != nil || !os.SameFile(lowerInfo, upperInfo) {
		t.Skip("filesystem is case-sensitive")
	}
	if _, err := dicomdir.ParseFileSet(upperPath); !errors.Is(err, dicomdir.ErrInvalidRecord) {
		t.Fatalf("ParseFileSet(case-mismatched physical DICOMDIR) error = %v, want ErrInvalidRecord", err)
	}
}
