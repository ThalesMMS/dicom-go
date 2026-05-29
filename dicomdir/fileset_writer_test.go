package dicomdir_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomdir"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	filesetTagFileSetID             = core.NewTag(0x0004, 0x1130)
	filesetTagFirstRootOffset       = core.NewTag(0x0004, 0x1200)
	filesetTagLastRootOffset        = core.NewTag(0x0004, 0x1202)
	filesetTagConsistencyFlag       = core.NewTag(0x0004, 0x1212)
	filesetTagDirectorySequence     = core.NewTag(0x0004, 0x1220)
	filesetTagNextOffset            = core.NewTag(0x0004, 0x1400)
	filesetTagRecordInUse           = core.NewTag(0x0004, 0x1410)
	filesetTagLowerOffset           = core.NewTag(0x0004, 0x1420)
	filesetTagRecordType            = core.NewTag(0x0004, 0x1430)
	filesetTagReferencedFileID      = core.NewTag(0x0004, 0x1500)
	filesetTagReferencedSOPClass    = core.NewTag(0x0004, 0x1510)
	filesetTagReferencedSOPInstance = core.NewTag(0x0004, 0x1511)
	filesetTagReferencedSyntax      = core.NewTag(0x0004, 0x1512)
	filesetTagReferencedRelatedSOP  = core.NewTag(0x0004, 0x151a)
	filesetTagRelatedGeneralSOP     = core.NewTag(0x0008, 0x001a)
)

const (
	filesetDirectorySOPClass = "1.2.840.10008.1.3.10"
	filesetFixedUID          = "1.2.826.0.1.3680043.10.543.625.1"
	filesetImageSOPClass     = "1.2.840.10008.5.1.4.1.1.2"
)

type filesetInstanceSpec struct {
	fileID                dicomdir.FileID
	specificCharacterSet  string
	patientID             string
	patientName           string
	studyUID              string
	studyID               string
	studyDate             string
	studyTime             string
	seriesUID             string
	seriesNo              string
	modality              string
	sopUID                string
	instanceNo            string
	relatedGeneralSOPUIDs []string
}

func TestWriteDICOMDIRBuildsDeterministicPortableHierarchy(t *testing.T) {
	root, fileSet, specs := filesetSyntheticFileSet(t)

	var first bytes.Buffer
	report, err := dicomdir.WriteDICOMDIR(context.Background(), &first, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20})
	if err != nil {
		t.Fatalf("WriteDICOMDIR first pass: %v (report: %+v)", err, report)
	}
	var second bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &second, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("WriteDICOMDIR second pass: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("fixed File-set UID and unchanged sources produced different DICOMDIR bytes")
	}

	written := filesetReadPart10(t, first.Bytes())
	filesetAssertPart10(t, written)
	filesetAssertDirectoryGraph(t, written.Dataset, specs)

	path := filepath.Join(root, "DICOMDIR")
	if err := os.WriteFile(path, first.Bytes(), 0o600); err != nil {
		t.Fatalf("write generated DICOMDIR for ParseFileSet: %v", err)
	}
	parsed, err := dicomdir.ParseFileSet(path)
	if err != nil {
		t.Fatalf("ParseFileSet: %v", err)
	}
	filesetAssertQueries(t, parsed, specs)

	var imported bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &imported, parsed, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("rewrite parsed file-set: %v", err)
	}
	importedFile := filesetReadPart10(t, imported.Bytes())
	if got, ok := importedFile.Meta.GetUID(tags.MediaStorageSOPInstanceUID); !ok || got != filesetFixedUID {
		t.Fatalf("parsed File-set UID = %q, %v; want %q, true", got, ok, filesetFixedUID)
	}
}

func TestWriteDICOMDIROffsetsRemainExactWithVaryingValueLengths(t *testing.T) {
	_, fileSet, specs := filesetSyntheticFileSet(t)
	var encoded bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &encoded, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("WriteDICOMDIR: %v", err)
	}
	filesetAssertDirectoryGraph(t, filesetReadPart10(t, encoded.Bytes()).Dataset, specs)
}

func TestWriteDICOMDIRRejectsSourceChangedSinceAdd(t *testing.T) {
	root, fileSet, specs := filesetSyntheticFileSet(t)
	changed := filepath.Join(append([]string{root}, []string(specs[0].fileID)...)...)
	replacement := append([]byte(nil), filesetReadBytes(t, changed)...)
	replacement = append(replacement, 0x00, 0x00)
	if err := os.WriteFile(changed, replacement, 0o600); err != nil {
		t.Fatalf("mutate indexed source: %v", err)
	}

	var output bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &output, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err == nil {
		t.Fatal("WriteDICOMDIR accepted a source changed after Add")
	}
	if output.Len() != 0 {
		t.Fatalf("failed preflight exposed %d output bytes, want 0", output.Len())
	}
}

func TestWriteDICOMDIRHonorsCancellationAndShortWrites(t *testing.T) {
	_, fileSet, _ := filesetSyntheticFileSet(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var canceled bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(ctx, &canceled, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WriteDICOMDIR error = %v, want context.Canceled", err)
	}
	if canceled.Len() != 0 {
		t.Fatalf("canceled write exposed %d bytes, want 0", canceled.Len())
	}

	short := &filesetShortWriter{remaining: 127}
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), short, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v, want io.ErrShortWrite", err)
	}
}

func TestCommitDICOMDIRIsAtomicWhenSourceBecomesStale(t *testing.T) {
	root, fileSet, specs := filesetSyntheticFileSet(t)
	if _, err := dicomdir.CommitDICOMDIR(context.Background(), fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("initial CommitDICOMDIR: %v", err)
	}
	destination := filepath.Join(root, "DICOMDIR")
	before := filesetReadBytes(t, destination)
	filesetAssertPart10(t, filesetReadPart10(t, before))

	changed := filepath.Join(append([]string{root}, []string(specs[len(specs)-1].fileID)...)...)
	if err := os.Truncate(changed, 132); err != nil {
		t.Fatalf("truncate indexed source: %v", err)
	}
	if _, err := dicomdir.CommitDICOMDIR(context.Background(), fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err == nil {
		t.Fatal("CommitDICOMDIR accepted a stale source")
	}
	after := filesetReadBytes(t, destination)
	if !bytes.Equal(after, before) {
		t.Fatal("failed commit replaced the previously valid DICOMDIR")
	}
	filesetAssertPart10(t, filesetReadPart10(t, after))
}

func TestWriteDICOMDIRPreservesISOIR100PatientName(t *testing.T) {
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID, FileSetID: "TEST_SET"})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	spec := filesetInstanceSpec{
		fileID:               dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
		specificCharacterSet: "ISO_IR 100", patientID: "SUBJECT_A", patientName: "José^ALPHA",
		studyUID: "1.2.826.0.1.3680043.10.543.625.41", studyID: "A1", studyDate: "20260101", studyTime: "101010",
		seriesUID: "1.2.826.0.1.3680043.10.543.625.41.1", seriesNo: "1", modality: "CT",
		sopUID: "1.2.826.0.1.3680043.10.543.625.41.1.1", instanceNo: "1",
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
			if charset, _ := item.GetString(core.NewTag(0x0008, 0x0005)); charset != "ISO_IR 100" {
				t.Fatalf("patient record charset = %q, want ISO_IR 100", charset)
			}
			if got, err := item.LookupString(tags.PatientName); err != nil || got != "José^ALPHA" {
				t.Fatalf("patient name after DICOMDIR round-trip = %q, %v", got, err)
			}
			return
		}
	}
	t.Fatal("generated DICOMDIR has no PATIENT record")
}

func TestParseFileSetRejectsMalformedOffsetGraphs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
	}{
		{
			name: "zero root with records",
			mutate: func(t *testing.T, data []byte) []byte {
				filesetPatchUint32(t, data, 132, filesetTagFirstRootOffset, 0)
				return data
			},
		},
		{
			name: "dangling root",
			mutate: func(t *testing.T, data []byte) []byte {
				filesetPatchUint32(t, data, 132, filesetTagFirstRootOffset, 0x7fffff00)
				return data
			},
		},
		{
			name: "root cycle",
			mutate: func(t *testing.T, data []byte) []byte {
				file := filesetReadPart10(t, data)
				items, ok := file.Dataset.GetSequence(filesetTagDirectorySequence)
				if !ok || len(items) == 0 {
					t.Fatal("fixture has no directory records")
				}
				offset, set := items[0].ItemOffset()
				if !set {
					t.Fatal("first root record has no ItemOffset")
				}
				filesetPatchUint32(t, data, int(offset), filesetTagNextOffset, uint32(offset))
				return data
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, fileSet, _ := filesetSyntheticFileSet(t)
			data := filesetWriteBytes(t, fileSet)
			path := filesetWriteDICOMDIRFixture(t, root, test.mutate(t, data))
			if _, err := dicomdir.ParseFileSet(path); err == nil {
				t.Fatal("ParseFileSet accepted a malformed offset graph")
			}
		})
	}
}

func TestParseFileSetRejectsMissingFileSetIDAttribute(t *testing.T) {
	root, fileSet, _ := filesetSyntheticFileSet(t)
	data := filesetWriteBytes(t, fileSet)
	header, _ := filesetElementValueRange(t, data, 132, filesetTagFileSetID)
	// Preserve every encoded length and directory offset while changing
	// (0004,1130) into an unknown neighboring tag.
	data[header+2]++
	path := filesetWriteDICOMDIRFixture(t, root, data)
	if _, err := dicomdir.ParseFileSet(path); err == nil {
		t.Fatal("ParseFileSet accepted a dataset without the Type 2 File-set ID attribute")
	}
}

func TestParseFileSetRejectsActiveImageWithoutFileID(t *testing.T) {
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
			header, _ := filesetElementValueRange(t, data, int(offset), filesetTagReferencedFileID)
			// Keep the encoded size and all directory offsets stable while making
			// (0004,1500) unavailable to the parser.
			data[header+2] = 0x01
			path := filesetWriteDICOMDIRFixture(t, root, data)
			if _, err := dicomdir.ParseFileSet(path); err == nil {
				t.Fatal("ParseFileSet accepted an active IMAGE without Referenced File ID")
			}
			return
		}
	}
	t.Fatal("fixture has no IMAGE record")
}

func TestParseFileSetRejectsAncestorPatientIDMismatch(t *testing.T) {
	root, fileSet, _ := filesetSyntheticFileSet(t)
	data := filesetWriteBytes(t, fileSet)
	file := filesetReadPart10(t, data)
	items, ok := file.Dataset.GetSequence(filesetTagDirectorySequence)
	if !ok {
		t.Fatal("fixture has no directory records")
	}
	for _, item := range items {
		if recordType, _ := item.GetString(filesetTagRecordType); recordType == "PATIENT" {
			offset, set := item.ItemOffset()
			if !set {
				t.Fatal("PATIENT record has no ItemOffset")
			}
			_, value := filesetElementValueRange(t, data, int(offset), tags.PatientID)
			if len(value) != len("SUBJECT_A ") {
				t.Fatalf("fixture Patient ID encoded length = %d", len(value))
			}
			copy(value, []byte("SUBJECT_X "))
			path := filesetWriteDICOMDIRFixture(t, root, data)
			if _, err := dicomdir.ParseFileSet(path); err == nil {
				t.Fatal("ParseFileSet accepted a source under a mismatched PATIENT key")
			}
			return
		}
	}
	t.Fatal("fixture has no PATIENT record")
}

func TestParseFileSetPreservesDescriptorOnRewrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README01"), []byte("synthetic file-set descriptor\n"), 0o600); err != nil {
		t.Fatalf("write descriptor fixture: %v", err)
	}
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{
		FileSetUID: filesetFixedUID, FileSetID: "TEST_SET",
		DescriptorFileID: dicomdir.FileID{"README01"}, DescriptorCharacterSet: "ISO_IR 192",
	})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	spec := filesetInstanceSpec{
		fileID:    dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
		patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
		studyUID: "1.2.826.0.1.3680043.10.543.625.51", studyID: "A1", studyDate: "20260101", studyTime: "101010",
		seriesUID: "1.2.826.0.1.3680043.10.543.625.51.1", seriesNo: "1", modality: "CT",
		sopUID: "1.2.826.0.1.3680043.10.543.625.51.1.1", instanceNo: "1",
	}
	filesetWriteInstance(t, root, spec)
	if err := fileSet.Add(context.Background(), spec.fileID); err != nil {
		t.Fatalf("Add: %v", err)
	}
	path := filesetWriteDICOMDIRFixture(t, root, filesetWriteBytes(t, fileSet))
	parsed, err := dicomdir.ParseFileSet(path)
	if err != nil {
		t.Fatalf("ParseFileSet: %v", err)
	}
	rewritten := filesetReadPart10(t, filesetWriteBytes(t, parsed))
	if got, ok := rewritten.Dataset.GetStrings(core.NewTag(0x0004, 0x1141)); !ok || !filesetEqualStrings(got, []string{"README01"}) {
		t.Fatalf("descriptor File ID after rewrite = %v, %v", got, ok)
	}
	if got, ok := rewritten.Dataset.GetString(core.NewTag(0x0004, 0x1142)); !ok || got != "ISO_IR 192" {
		t.Fatalf("descriptor charset after rewrite = %q, %v", got, ok)
	}
}

func TestWriteDICOMDIRPreservesRelatedGeneralSOPClasses(t *testing.T) {
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID, FileSetID: "TEST_SET"})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	want := []string{
		"1.2.840.10008.5.1.4.1.1.2",
		"1.2.840.10008.5.1.4.1.1.4",
	}
	spec := filesetInstanceSpec{
		fileID:    dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
		patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
		studyUID: "1.2.826.0.1.3680043.10.543.625.61", studyID: "A1", studyDate: "20260101", studyTime: "101010",
		seriesUID: "1.2.826.0.1.3680043.10.543.625.61.1", seriesNo: "1", modality: "CT",
		sopUID: "1.2.826.0.1.3680043.10.543.625.61.1.1", instanceNo: "1",
		relatedGeneralSOPUIDs: want,
	}
	filesetWriteInstance(t, root, spec)
	if err := fileSet.Add(context.Background(), spec.fileID); err != nil {
		t.Fatalf("Add: %v", err)
	}
	encoded := filesetWriteBytes(t, fileSet)
	filesetAssertRelatedGeneralSOPClasses(t, filesetReadPart10(t, encoded).Dataset, want)

	path := filesetWriteDICOMDIRFixture(t, root, encoded)
	parsed, err := dicomdir.ParseFileSet(path)
	if err != nil {
		t.Fatalf("ParseFileSet: %v", err)
	}
	filesetAssertRelatedGeneralSOPClasses(t, filesetReadPart10(t, filesetWriteBytes(t, parsed)).Dataset, want)
}

func TestParseFileSetRejectsEmptyRelatedGeneralSOPClassComponent(t *testing.T) {
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{FileSetUID: filesetFixedUID, FileSetID: "TEST_SET"})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	spec := filesetInstanceSpec{
		fileID:    dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
		patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
		studyUID: "1.2.826.0.1.3680043.10.543.625.611", studyID: "A1", studyDate: "20260101", studyTime: "101010",
		seriesUID: "1.2.826.0.1.3680043.10.543.625.611.1", seriesNo: "1", modality: "CT",
		sopUID: "1.2.826.0.1.3680043.10.543.625.611.1.1", instanceNo: "1",
		relatedGeneralSOPUIDs: []string{"1.2.3"},
	}
	filesetWriteInstance(t, root, spec)
	if err := fileSet.Add(context.Background(), spec.fileID); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data := filesetWriteBytes(t, fileSet)
	items, ok := filesetReadPart10(t, data).Dataset.GetSequence(filesetTagDirectorySequence)
	if !ok {
		t.Fatal("fixture has no directory records")
	}
	for _, item := range items {
		if recordType, _ := item.GetString(filesetTagRecordType); recordType == "IMAGE" {
			offset, set := item.ItemOffset()
			if !set {
				t.Fatal("IMAGE record has no ItemOffset")
			}
			_, raw := filesetElementValueRange(t, data, int(offset), filesetTagReferencedRelatedSOP)
			if len(raw) != len("1.2.3\\") {
				t.Fatalf("related UID encoded length = %d, want %d", len(raw), len("1.2.3\\"))
			}
			copy(raw, "1.2.3\\")
			path := filesetWriteDICOMDIRFixture(t, root, data)
			if _, err := dicomdir.ParseFileSet(path); !errors.Is(err, dicomdir.ErrInvalidRecord) {
				t.Fatalf("ParseFileSet error = %v, want ErrInvalidRecord", err)
			}
			return
		}
	}
	t.Fatal("fixture has no IMAGE record")
}

func filesetSyntheticFileSet(t *testing.T) (string, *dicomdir.FileSet, []filesetInstanceSpec) {
	t.Helper()
	root := t.TempDir()
	fileSet, err := dicomdir.NewFileSet(root, dicomdir.Options{
		FileSetUID: filesetFixedUID,
		FileSetID:  "TEST_SET",
	})
	if err != nil {
		t.Fatalf("NewFileSet: %v", err)
	}
	specs := []filesetInstanceSpec{
		{
			fileID:    dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0001"},
			patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
			studyUID: "1.2.826.0.1.3680043.10.543.625.11", studyID: "A1", studyDate: "20260101", studyTime: "101010",
			seriesUID: "1.2.826.0.1.3680043.10.543.625.11.1", seriesNo: "1", modality: "CT",
			sopUID: "1.2.826.0.1.3680043.10.543.625.11.1.1", instanceNo: "1",
		},
		{
			fileID:    dicomdir.FileID{"PATA0001", "STUDYA1", "SERIESA1", "IMGA0002"},
			patientID: "SUBJECT_A", patientName: "SUBJECT^ALPHA",
			studyUID: "1.2.826.0.1.3680043.10.543.625.11", studyID: "A1", studyDate: "20260101", studyTime: "101010",
			seriesUID: "1.2.826.0.1.3680043.10.543.625.11.1", seriesNo: "1", modality: "CT",
			sopUID: "1.2.826.0.1.3680043.10.543.625.11.1.20002", instanceNo: "22",
		},
		{
			fileID:    dicomdir.FileID{"PATB0002", "STUDYB22", "SERIESB2", "IMGB0001"},
			patientID: "SUBJECT_B_WITH_LONGER_ID", patientName: "SUBJECT^BETA_WITH_LONGER_NAME",
			studyUID: "1.2.826.0.1.3680043.10.543.625.2200001", studyID: "B_STUDY", studyDate: "20261231", studyTime: "235959.123456",
			seriesUID: "1.2.826.0.1.3680043.10.543.625.2200001.333", seriesNo: "333", modality: "MR",
			sopUID: "1.2.826.0.1.3680043.10.543.625.2200001.333.1", instanceNo: "1",
		},
		{
			fileID:    dicomdir.FileID{"PATB0002", "STUDYB22", "SERIESB2", "IMGB0002"},
			patientID: "SUBJECT_B_WITH_LONGER_ID", patientName: "SUBJECT^BETA_WITH_LONGER_NAME",
			studyUID: "1.2.826.0.1.3680043.10.543.625.2200001", studyID: "B_STUDY", studyDate: "20261231", studyTime: "235959.123456",
			seriesUID: "1.2.826.0.1.3680043.10.543.625.2200001.333", seriesNo: "333", modality: "MR",
			sopUID: "1.2.826.0.1.3680043.10.543.625.2200001.333.2000004", instanceNo: "2000004",
		},
	}
	for _, spec := range specs {
		filesetWriteInstance(t, root, spec)
		if err := fileSet.Add(context.Background(), spec.fileID); err != nil {
			t.Fatalf("Add(%v): %v", spec.fileID, err)
		}
	}
	return root, fileSet, specs
}

func filesetWriteInstance(t *testing.T, root string, spec filesetInstanceSpec) {
	t.Helper()
	path := filepath.Join(append([]string{root}, []string(spec.fileID)...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create synthetic fixture directory: %v", err)
	}
	elements := []core.Element{
		filesetStringElement(tags.SOPClassUID, core.VRUI, filesetImageSOPClass),
		filesetStringElement(tags.SOPInstanceUID, core.VRUI, spec.sopUID),
		filesetStringElement(tags.PatientName, core.VRPN, spec.patientName),
		filesetStringElement(tags.PatientID, core.VRLO, spec.patientID),
		filesetStringElement(tags.StudyDate, core.VRDA, spec.studyDate),
		filesetStringElement(tags.StudyTime, core.VRTM, spec.studyTime),
		filesetStringElement(tags.AccessionNumber, core.VRSH, ""),
		filesetStringElement(tags.StudyInstanceUID, core.VRUI, spec.studyUID),
		filesetStringElement(tags.StudyID, core.VRSH, spec.studyID),
		filesetStringElement(tags.Modality, core.VRCS, spec.modality),
		filesetStringElement(tags.SeriesInstanceUID, core.VRUI, spec.seriesUID),
		filesetStringElement(tags.SeriesNumber, core.VRIS, spec.seriesNo),
		filesetStringElement(tags.InstanceNumber, core.VRIS, spec.instanceNo),
		core.NewRawElement(tags.PixelData, core.VROB, []byte{0x01, 0x02}),
	}
	if spec.specificCharacterSet != "" {
		elements = append(elements,
			filesetStringElement(core.NewTag(0x0008, 0x0005), core.VRCS, strings.Split(spec.specificCharacterSet, `\`)...),
		)
	}
	if len(spec.relatedGeneralSOPUIDs) != 0 {
		elements = append(elements,
			filesetStringElement(filesetTagRelatedGeneralSOP, core.VRUI, spec.relatedGeneralSOPUIDs...),
		)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create synthetic DICOM: %v", err)
	}
	writeErr := object.WriteFile(file, &object.File{
		Dataset:        object.FromElements(elements, nil),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	})
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatalf("write synthetic DICOM: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close synthetic DICOM: %v", closeErr)
	}
}

func filesetStringElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
}

func filesetReadPart10(t *testing.T, data []byte) *object.File {
	t.Helper()
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read generated Part 10 file: %v", err)
	}
	return file
}

func filesetWriteBytes(t *testing.T, fileSet *dicomdir.FileSet) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if _, err := dicomdir.WriteDICOMDIR(context.Background(), &encoded, fileSet, dicomdir.WriteOptions{MaxBytes: 8 << 20}); err != nil {
		t.Fatalf("WriteDICOMDIR fixture: %v", err)
	}
	return append([]byte(nil), encoded.Bytes()...)
}

func filesetWriteDICOMDIRFixture(t *testing.T, root string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, "DICOMDIR")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write DICOMDIR fixture: %v", err)
	}
	return path
}

func filesetElementValueRange(t *testing.T, data []byte, start int, tag core.Tag) (int, []byte) {
	t.Helper()
	if start < 0 || start >= len(data) {
		t.Fatalf("invalid encoded search start %d for %d bytes", start, len(data))
	}
	needle := []byte{byte(tag.Group), byte(tag.Group >> 8), byte(tag.Element), byte(tag.Element >> 8)}
	relative := bytes.Index(data[start:], needle)
	if relative < 0 {
		t.Fatalf("encoded element %s not found after offset %d", tag, start)
	}
	header := start + relative
	if header+8 > len(data) {
		t.Fatalf("encoded element %s has a truncated header", tag)
	}
	length := int(binary.LittleEndian.Uint16(data[header+6 : header+8]))
	valueStart := header + 8
	if valueStart+length < valueStart || valueStart+length > len(data) {
		t.Fatalf("encoded element %s has invalid value length %d", tag, length)
	}
	return header, data[valueStart : valueStart+length]
}

func filesetPatchUint32(t *testing.T, data []byte, start int, tag core.Tag, value uint32) {
	t.Helper()
	header, raw := filesetElementValueRange(t, data, start, tag)
	if got := string(data[header+4 : header+6]); got != "UL" {
		t.Fatalf("encoded element %s VR = %q, want UL", tag, got)
	}
	if len(raw) != 4 {
		t.Fatalf("encoded element %s length = %d, want 4", tag, len(raw))
	}
	binary.LittleEndian.PutUint32(raw, value)
}

func filesetAssertPart10(t *testing.T, file *object.File) {
	t.Helper()
	if file == nil || file.Meta == nil || file.Dataset == nil {
		t.Fatal("generated file is missing File Meta or dataset")
	}
	if file.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax = %q, want Explicit VR Little Endian", file.TransferSyntax.UID)
	}
	if got, ok := file.Meta.GetUID(tags.MediaStorageSOPClassUID); !ok || got != filesetDirectorySOPClass {
		t.Fatalf("Media Storage SOP Class UID = %q, %v", got, ok)
	}
	if got, ok := file.Meta.GetUID(tags.MediaStorageSOPInstanceUID); !ok || got != filesetFixedUID {
		t.Fatalf("Media Storage SOP Instance UID = %q, %v", got, ok)
	}
	if file.Dataset.Has(tags.SOPClassUID) || file.Dataset.Has(tags.SOPInstanceUID) {
		t.Fatal("Basic Directory dataset unexpectedly contains SOP Common UID attributes")
	}
	if got, ok := file.Dataset.GetString(filesetTagFileSetID); !ok || got != "TEST_SET" {
		t.Fatalf("File-set ID = %q, %v", got, ok)
	}
	if got := filesetUint16(t, file.Dataset, filesetTagConsistencyFlag); got != 0 {
		t.Fatalf("File-set Consistency Flag = %#x, want 0", got)
	}
}

func filesetAssertDirectoryGraph(t *testing.T, dataset *object.Object, specs []filesetInstanceSpec) {
	t.Helper()
	items, ok := dataset.GetSequence(filesetTagDirectorySequence)
	if !ok {
		t.Fatal("generated dataset has no Directory Record Sequence")
	}
	if got, want := len(items), 10; got != want {
		t.Fatalf("directory record count = %d, want %d", got, want)
	}

	offsets := make(map[string]uint32, len(items))
	records := make(map[string]*object.Object, len(items))
	for _, item := range items {
		if item == nil {
			t.Fatal("Directory Record Sequence contains nil item")
		}
		offset, set := item.ItemOffset()
		if !set || offset <= 0 || offset > int64(^uint32(0)) {
			t.Fatalf("invalid directory item offset %d, set=%v", offset, set)
		}
		key := filesetRecordKey(t, item)
		if _, duplicate := offsets[key]; duplicate {
			t.Fatalf("duplicate directory record key %q", key)
		}
		offsets[key] = uint32(offset)
		records[key] = item
		if got := filesetUint16(t, item, filesetTagRecordInUse); got != 0xffff {
			t.Fatalf("record %q in-use flag = %#x, want 0xffff", key, got)
		}
	}

	patientA := "PATIENT:" + specs[0].patientID
	studyA := "STUDY:" + specs[0].studyUID
	seriesA := "SERIES:" + specs[0].seriesUID
	imageA1 := "IMAGE:" + specs[0].sopUID
	imageA2 := "IMAGE:" + specs[1].sopUID
	patientB := "PATIENT:" + specs[2].patientID
	studyB := "STUDY:" + specs[2].studyUID
	seriesB := "SERIES:" + specs[2].seriesUID
	imageB1 := "IMAGE:" + specs[2].sopUID
	imageB2 := "IMAGE:" + specs[3].sopUID

	if got := filesetUint32(t, dataset, filesetTagFirstRootOffset); got != offsets[patientA] {
		t.Fatalf("first root offset = %d, want %d", got, offsets[patientA])
	}
	if got := filesetUint32(t, dataset, filesetTagLastRootOffset); got != offsets[patientB] {
		t.Fatalf("last root offset = %d, want %d", got, offsets[patientB])
	}
	filesetAssertLinks(t, records[patientA], offsets[patientB], offsets[studyA])
	filesetAssertLinks(t, records[patientB], 0, offsets[studyB])
	filesetAssertLinks(t, records[studyA], 0, offsets[seriesA])
	filesetAssertLinks(t, records[studyB], 0, offsets[seriesB])
	filesetAssertLinks(t, records[seriesA], 0, offsets[imageA1])
	filesetAssertLinks(t, records[seriesB], 0, offsets[imageB1])
	filesetAssertLinks(t, records[imageA1], offsets[imageA2], 0)
	filesetAssertLinks(t, records[imageA2], 0, 0)
	filesetAssertLinks(t, records[imageB1], offsets[imageB2], 0)
	filesetAssertLinks(t, records[imageB2], 0, 0)

	refs := dicomdir.References(dataset)
	if got, want := len(refs), len(specs); got != want {
		t.Fatalf("read-back active references = %d, want %d", got, want)
	}
	for index, ref := range refs {
		if ref.Type != "IMAGE" {
			t.Fatalf("reference %d type = %q, want IMAGE", index, ref.Type)
		}
		if !filesetEqualStrings(ref.FileID, []string(specs[index].fileID)) {
			t.Fatalf("reference %d File ID = %v, want %v", index, ref.FileID, specs[index].fileID)
		}
	}
}

func filesetRecordKey(t *testing.T, record *object.Object) string {
	t.Helper()
	recordType, ok := record.GetString(filesetTagRecordType)
	if !ok {
		t.Fatal("directory record has no Directory Record Type")
	}
	var tag core.Tag
	switch recordType {
	case "PATIENT":
		tag = tags.PatientID
	case "STUDY":
		tag = tags.StudyInstanceUID
	case "SERIES":
		tag = tags.SeriesInstanceUID
	case "IMAGE":
		tag = filesetTagReferencedSOPInstance
	default:
		t.Fatalf("unexpected Directory Record Type %q", recordType)
	}
	value, ok := record.GetString(tag)
	if !ok || value == "" {
		t.Fatalf("directory record %q has no identity value at %s", recordType, tag)
	}
	if recordType == "IMAGE" {
		filesetAssertImageReferenceFields(t, record)
	}
	return recordType + ":" + value
}

func filesetAssertImageReferenceFields(t *testing.T, record *object.Object) {
	t.Helper()
	if fileID, ok := record.GetStrings(filesetTagReferencedFileID); !ok || len(fileID) == 0 {
		t.Fatal("IMAGE record has no Referenced File ID")
	}
	if got, ok := record.GetUID(filesetTagReferencedSOPClass); !ok || got != filesetImageSOPClass {
		t.Fatalf("referenced SOP Class UID = %q, %v", got, ok)
	}
	if got, ok := record.GetUID(filesetTagReferencedSyntax); !ok || got != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("referenced Transfer Syntax UID = %q, %v", got, ok)
	}
}

func filesetAssertRelatedGeneralSOPClasses(t *testing.T, dataset *object.Object, want []string) {
	t.Helper()
	items, ok := dataset.GetSequence(filesetTagDirectorySequence)
	if !ok {
		t.Fatal("generated DICOMDIR has no Directory Record Sequence")
	}
	for _, item := range items {
		if recordType, _ := item.GetString(filesetTagRecordType); recordType == "IMAGE" {
			got, ok := item.GetStrings(filesetTagReferencedRelatedSOP)
			if !ok || !filesetEqualStrings(got, want) {
				t.Fatalf("Referenced Related General SOP Class UID in File = %v, %v; want %v", got, ok, want)
			}
			return
		}
	}
	t.Fatal("generated DICOMDIR has no IMAGE record")
}

func filesetAssertLinks(t *testing.T, record *object.Object, wantNext, wantLower uint32) {
	t.Helper()
	if record == nil {
		t.Fatal("expected directory record is absent")
	}
	if got := filesetUint32(t, record, filesetTagNextOffset); got != wantNext {
		t.Fatalf("record %q next offset = %d, want %d", filesetRecordKey(t, record), got, wantNext)
	}
	if got := filesetUint32(t, record, filesetTagLowerOffset); got != wantLower {
		t.Fatalf("record %q lower offset = %d, want %d", filesetRecordKey(t, record), got, wantLower)
	}
}

func filesetUint32(t *testing.T, obj *object.Object, tag core.Tag) uint32 {
	t.Helper()
	element, ok := obj.Get(tag)
	if !ok || element.VR() != core.VRUL {
		t.Fatalf("missing or invalid UL element %s", tag)
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw) != 4 {
		t.Fatalf("UL element %s has invalid raw value", tag)
	}
	return obj.ValueByteOrder().Uint32(raw)
}

func filesetUint16(t *testing.T, obj *object.Object, tag core.Tag) uint16 {
	t.Helper()
	element, ok := obj.Get(tag)
	if !ok || element.VR() != core.VRUS {
		t.Fatalf("missing or invalid US element %s", tag)
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw) != 2 {
		t.Fatalf("US element %s has invalid raw value", tag)
	}
	return obj.ValueByteOrder().Uint16(raw)
}

func filesetAssertQueries(t *testing.T, fileSet *dicomdir.FileSet, specs []filesetInstanceSpec) {
	t.Helper()
	statistics := fileSet.Statistics()
	if statistics.Files != 4 || statistics.Patients != 2 || statistics.Studies != 2 || statistics.Series != 2 {
		t.Fatalf("Statistics() = %+v, want 4 files in a 2/2/2 patient/study/series hierarchy", statistics)
	}
	if statistics.ByType[dicomdir.RecordTypeImage] != 4 || statistics.ByModality["CT"] != 2 || statistics.ByModality["MR"] != 2 {
		t.Fatalf("Statistics() type/modality counts = %+v/%+v", statistics.ByType, statistics.ByModality)
	}
	tests := []struct {
		name  string
		query dicomdir.Query
		want  int
	}{
		{name: "patient", query: dicomdir.Query{PatientID: specs[0].patientID}, want: 2},
		{name: "study", query: dicomdir.Query{StudyInstanceUID: specs[2].studyUID}, want: 2},
		{name: "series", query: dicomdir.Query{SeriesInstanceUID: specs[0].seriesUID}, want: 2},
		{name: "SOP instance", query: dicomdir.Query{SOPInstanceUID: specs[3].sopUID}, want: 1},
		{name: "modality", query: dicomdir.Query{Modality: "MR"}, want: 2},
		{name: "record type", query: dicomdir.Query{RecordType: dicomdir.RecordTypeImage}, want: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := len(fileSet.Query(test.query)); got != test.want {
				t.Fatalf("Query(%+v) returned %d records, want %d", test.query, got, test.want)
			}
		})
	}
}

func filesetReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return data
}

func filesetEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type filesetShortWriter struct {
	remaining int
}

func (w *filesetShortWriter) Write(data []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, nil
	}
	if len(data) > w.remaining {
		written := w.remaining
		w.remaining = 0
		return written, nil
	}
	w.remaining -= len(data)
	return len(data), nil
}
