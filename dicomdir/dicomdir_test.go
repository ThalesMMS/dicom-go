package dicomdir

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	testTagSOPClassUID    = core.NewTag(0x0008, 0x0016)
	testTagSOPInstanceUID = core.NewTag(0x0008, 0x0018)
)

func TestReferencesExtractsReferencedFileIdsFromDirectoryRecords(t *testing.T) {
	// Given
	obj := object.FromElements([]core.Element{directoryRecordSequence(
		directoryRecord(`DICOM\PT0\ST0\SE0\IM0`),
		directoryRecord("DICOM", "PT0", "ST0", "SE0", "IM1"),
		object.New(std.Dictionary),
		directoryRecord("", `\`, "  "),
	)}, std.Dictionary)

	// When
	refs := References(obj)

	// Then
	if len(refs) != 2 {
		t.Fatalf("References() returned %d refs, want 2", len(refs))
	}
	if got := refs[0].FileID; len(got) != 5 || got[4] != "IM0" {
		t.Fatalf("first FileID = %#v, want five components ending with IM0", got)
	}
	if got := refs[1].FileID; len(got) != 5 || got[4] != "IM1" {
		t.Fatalf("second FileID = %#v, want five components ending with IM1", got)
	}
}

func TestValidateFileRecordDoesNotMutateRelatedSOPClasses(t *testing.T) {
	record := FileRecord{
		FileID: FileID{"DICOM", "IMAGE001"}, RecordType: RecordTypeImage, PatientID: "PATIENT", StudyDate: "20260808", StudyTime: "120000",
		StudyInstanceUID: "1.2.3.1", StudyID: "STUDY", Modality: "CT", SeriesInstanceUID: "1.2.3.2",
		SeriesNumber: "1", InstanceNumber: "1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2",
		SOPInstanceUID: "1.2.3.3", TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
		RelatedGeneralSOPClassUIDs: []string{"1.2.840.10008.5.1.4.1.1.2 "},
	}
	if err := validateFileRecord(record); err != nil {
		t.Fatalf("validateFileRecord() error = %v", err)
	}
	if got := record.RelatedGeneralSOPClassUIDs[0]; got != "1.2.840.10008.5.1.4.1.1.2 " {
		t.Fatalf("validateFileRecord() mutated caller slice to %q", got)
	}
}

func TestReferencesFlatFallbackSkipsInactiveRecordsAndReturnsRecordType(t *testing.T) {
	// Given: manually constructed objects have no encoded sequence-item offsets.
	active := directoryRecord("ACTIVE")
	active.Put(stringElement(tagDirectoryRecordType, core.VRCS, "IMAGE"))
	active.Put(uint16Element(tagRecordInUseFlag, 0xFFFF))
	inactive := directoryRecord("DELETED")
	inactive.Put(stringElement(tagDirectoryRecordType, core.VRCS, "IMAGE"))
	inactive.Put(uint16Element(tagRecordInUseFlag, 0x0000))
	obj := object.FromElements([]core.Element{directoryRecordSequence(active, inactive)}, std.Dictionary)

	// When
	refs := References(obj)

	// Then
	if len(refs) != 1 || len(refs[0].FileID) != 1 || refs[0].FileID[0] != "ACTIVE" {
		t.Fatalf("References() = %#v, want only ACTIVE", refs)
	}
	if refs[0].Type != "IMAGE" {
		t.Fatalf("References()[0].Type = %q, want IMAGE", refs[0].Type)
	}
}

func TestReferencesTraversesOffsetHierarchyAndSkipsDeletedAndUnreachableRecords(t *testing.T) {
	// Given: physical sequence order deliberately differs from the offset graph.
	obj := object.FromElements([]core.Element{
		uint32Element(tagOffsetFirstRootDirectoryRecord, 100),
		directoryRecordDataSetSequence(
			hierarchicalDirectoryRecord(500, "IMAGE", 0xFFFF, 0, 0, "ORPHAN"),
			hierarchicalDirectoryRecord(400, "IMAGE", 0x0000, 300, 600, "DELETED"),
			hierarchicalDirectoryRecord(100, "PATIENT", 0xFFFF, 200, 300),
			hierarchicalDirectoryRecord(300, "IMAGE", 0xFFFF, 400, 0, "ACTIVE"),
			hierarchicalDirectoryRecord(200, "IMAGE", 0xFFFF, 0, 0, "ROOT2"),
			hierarchicalDirectoryRecord(600, "IMAGE", 0xFFFF, 0, 0, "ACTIVE_CHILD_OF_DELETED"),
		),
	}, std.Dictionary)

	// When
	refs := References(obj)

	// Then: lower-level records precede the next root record. The inactive
	// record's next pointer creates a cycle, which must terminate safely.
	want := []string{"ACTIVE", "ACTIVE_CHILD_OF_DELETED", "ROOT2"}
	if len(refs) != len(want) {
		t.Fatalf("References() = %#v, want %v", refs, want)
	}
	for i, fileID := range want {
		if len(refs[i].FileID) != 1 || refs[i].FileID[0] != fileID {
			t.Fatalf("References()[%d] = %#v, want %q", i, refs[i], fileID)
		}
		if refs[i].Type != "IMAGE" {
			t.Fatalf("References()[%d].Type = %q, want IMAGE", i, refs[i].Type)
		}
	}
}

func TestReferencesTreatsExplicitZeroRootOffsetAsEmptyDirectory(t *testing.T) {
	obj := object.FromElements([]core.Element{
		uint32Element(tagOffsetFirstRootDirectoryRecord, 0),
		directoryRecordSequence(directoryRecord("UNREACHABLE")),
	}, std.Dictionary)

	if refs := References(obj); len(refs) != 0 {
		t.Fatalf("References() = %#v, want no references", refs)
	}
}

func TestReferencesFailsClosedForMalformedRootOffset(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagOffsetFirstRootDirectoryRecord, core.VRUL, []byte{1, 2}),
		directoryRecordSequence(directoryRecord("UNREACHABLE")),
	}, std.Dictionary)

	if refs := References(obj); len(refs) != 0 {
		t.Fatalf("References() = %#v, want no references", refs)
	}
}

func TestReferencesUsesAbsoluteItemOffsetsFromEncodedDicomdir(t *testing.T) {
	// Given: write a complete media-directory file, then populate the offset
	// fields with the actual absolute positions produced by the encoder.
	root := t.TempDir()
	path := filepath.Join(root, "DICOMDIR")
	dataset := object.FromElements([]core.Element{
		uiElement(testTagSOPClassUID, "1.2.840.10008.1.3.10"),
		uiElement(testTagSOPInstanceUID, "1.2.3.4.5.6"),
		uint32Element(tagOffsetFirstRootDirectoryRecord, 0xA1B2C3D4),
		directoryRecordDataSetSequence(
			hierarchicalDirectoryRecord(0, "IMAGE", 0xFFFF, 0xB1C2D3E4, 0, "ACTIVE"),
			hierarchicalDirectoryRecord(0, "IMAGE", 0x0000, 0, 0, "DELETED"),
		),
	}, std.Dictionary)
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, &object.File{Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}); err != nil {
		t.Fatal(err)
	}
	raw := encoded.Bytes()
	sequenceOffset := bytes.Index(raw, littleEndianTagBytes(tagDirectoryRecordSequence))
	if sequenceOffset < 0 {
		t.Fatal("encoded DICOMDIR has no Directory Record Sequence")
	}
	itemTag := littleEndianTagBytes(core.TagItem)
	firstItemOffset := bytes.Index(raw[sequenceOffset+12:], itemTag)
	if firstItemOffset < 0 {
		t.Fatal("encoded DICOMDIR has no first directory-record item")
	}
	firstItemOffset += sequenceOffset + 12
	secondItemOffset := bytes.Index(raw[firstItemOffset+len(itemTag):], itemTag)
	if secondItemOffset < 0 {
		t.Fatal("encoded DICOMDIR has no second directory-record item")
	}
	secondItemOffset += firstItemOffset + len(itemTag)
	patchUint32ElementValue(t, raw, tagOffsetFirstRootDirectoryRecord, 0, uint32(firstItemOffset))
	patchUint32ElementValue(t, raw, tagOffsetNextDirectoryRecord, firstItemOffset, uint32(secondItemOffset))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	file, err := object.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	refs := References(file.Dataset)

	// Then
	records, ok := file.Dataset.GetSequence(tagDirectoryRecordSequence)
	if !ok || len(records) != 2 {
		t.Fatalf("parsed directory records = %d/%v, want 2/true", len(records), ok)
	}
	if got, ok := records[0].ItemOffset(); !ok || got != int64(firstItemOffset) {
		t.Fatalf("first parsed item offset = %d/%v, want %d/true", got, ok, firstItemOffset)
	}
	if len(refs) != 1 || len(refs[0].FileID) != 1 || refs[0].FileID[0] != "ACTIVE" {
		t.Fatalf("References() = %#v, want only ACTIVE", refs)
	}
}

func TestResolveSplitsBackslashComponentsAndSkipsBlankParts(t *testing.T) {
	// Given
	root := filepath.Join("media", "disc")

	tests := []struct {
		name string
		ref  Reference
		want string
	}{
		{
			name: "single component",
			ref:  Reference{FileID: []string{"IMG001"}},
			want: filepath.Join(root, "IMG001"),
		},
		{
			name: "multi value components",
			ref:  Reference{FileID: []string{"DICOM", "PT0", "ST0", "SE0", "IM0"}},
			want: filepath.Join(root, "DICOM", "PT0", "ST0", "SE0", "IM0"),
		},
		{
			name: "backslash inside one value",
			ref:  Reference{FileID: []string{`DICOM\PT0\ST0\IM0`}},
			want: filepath.Join(root, "DICOM", "PT0", "ST0", "IM0"),
		},
		{
			name: "blank components skipped",
			ref:  Reference{FileID: []string{"", "  ", "IM0"}},
			want: filepath.Join(root, "IM0"),
		},
		{
			name: "all blank",
			ref:  Reference{FileID: []string{"", `\`, "  "}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := Resolve(root, tt.ref)

			// Then
			if got != tt.want {
				t.Fatalf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		ref  Reference
	}{
		{name: "parent component", ref: Reference{FileID: []string{"..", "outside.dcm"}}},
		{name: "native absolute path", ref: Reference{FileID: []string{filepath.Join(root, "..", "outside.dcm")}}},
		{name: "drive absolute path", ref: Reference{FileID: []string{`C:\Users\patient\outside.dcm`}}},
		{name: "drive relative path", ref: Reference{FileID: []string{`C:outside.dcm`}}},
		{name: "drive as separate component", ref: Reference{FileID: []string{"C:", "outside.dcm"}}},
		{name: "UNC path", ref: Reference{FileID: []string{`\\server\share\outside.dcm`}}},
		{name: "device namespace", ref: Reference{FileID: []string{`\\?\C:\outside.dcm`}}},
		{name: "device object", ref: Reference{FileID: []string{`\\.\PhysicalDrive0`}}},
		{name: "rooted backslash path", ref: Reference{FileID: []string{`\outside.dcm`}}},
		{name: "rooted slash path", ref: Reference{FileID: []string{`/outside.dcm`}}},
		{name: "forward slash traversal", ref: Reference{FileID: []string{`DICOM/../outside.dcm`}}},
		{name: "embedded backslash traversal", ref: Reference{FileID: []string{`DICOM\..\outside.dcm`}}},
		{name: "NUL device", ref: Reference{FileID: []string{`NUL`}}},
		{name: "CON device with extension", ref: Reference{FileID: []string{`CON.txt`}}},
		{name: "COM1 device", ref: Reference{FileID: []string{`COM1`}}},
		{name: "LPT1 device", ref: Reference{FileID: []string{`LPT1`}}},
		{name: "case insensitive PRN device", ref: Reference{FileID: []string{`prn.dcm`}}},
		{name: "AUX device with trailing dots", ref: Reference{FileID: []string{`AUX...`}}},
		{name: "highest COM device", ref: Reference{FileID: []string{`COM9.image`}}},
		{name: "highest LPT device", ref: Reference{FileID: []string{`lpt9.`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(root, tt.ref); got != "" {
				t.Fatalf("Resolve(%#v) = %q, want empty path", tt.ref, got)
			}
		})
	}
}

func TestOpenReferencedFileOpensRegularReference(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "IMAGES"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "IMAGES", "IM1")
	if err := os.WriteFile(wantPath, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}

	file, path, err := OpenReferencedFile(root, Reference{FileID: []string{"IMAGES", "IM1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if path != wantPath {
		t.Fatalf("OpenReferencedFile() path = %q, want %q", path, wantPath)
	}
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "inside" {
		t.Fatalf("opened contents = %q, want inside", got)
	}
}

func TestReferencedPathsOpensDicomdirFileAndDeduplicatesResolvedPaths(t *testing.T) {
	// Given
	root := t.TempDir()
	path := filepath.Join(root, "DICOMDIR")
	dataset := object.FromElements([]core.Element{
		uiElement(testTagSOPClassUID, "1.2.840.10008.1.3.10"),
		uiElement(testTagSOPInstanceUID, "1.2.3.4.5"),
		directoryRecordSequence(
			directoryRecord("DICOM", "PT0", "ST0", "SE0", "IM0"),
			directoryRecord(`DICOM\PT0\ST0\SE0\IM0`),
			directoryRecord("DICOM", "PT0", "ST0", "SE0", "IM1"),
		),
	}, std.Dictionary)
	file := &object.File{
		Dataset:        dataset,
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	paths, err := ReferencedPaths(path)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "DICOM", "PT0", "ST0", "SE0", "IM0"),
		filepath.Join(root, "DICOM", "PT0", "ST0", "SE0", "IM1"),
	}
	if len(paths) != len(want) {
		t.Fatalf("ReferencedPaths() returned %d paths, want %d: %#v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("ReferencedPaths()[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestReferencedPathsReturnsAbsolutePathsForRelativeDicomdir(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "disc")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "DICOMDIR")
	dataset := object.FromElements([]core.Element{
		uiElement(testTagSOPClassUID, "1.2.840.10008.1.3.10"),
		uiElement(testTagSOPInstanceUID, "1.2.3.4.5"),
		directoryRecordSequence(directoryRecord("DICOM", "IM0")),
	}, std.Dictionary)
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, &object.File{Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}

	paths, err := ReferencedPaths(filepath.Join("disc", "DICOMDIR"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join("disc", "DICOM", "IM0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != want || !filepath.IsAbs(paths[0]) {
		t.Fatalf("ReferencedPaths() = %#v, want absolute %q", paths, want)
	}
}

func directoryRecord(fileID ...string) *object.Object {
	obj := object.New(std.Dictionary)
	raw := []byte{}
	for i, value := range fileID {
		if i > 0 {
			raw = append(raw, '\\')
		}
		raw = append(raw, value...)
	}
	obj.Put(core.NewRawElement(tagReferencedFileID, core.VRCS, raw))
	return obj
}

func directoryRecordSequence(items ...*object.Object) core.Element {
	dataSets := make([]core.DataSet, 0, len(items))
	for _, item := range items {
		dataSets = append(dataSets, item.ToDataSet())
	}
	return core.Element{
		Header: core.ElementHeader{Tag: tagDirectoryRecordSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: dataSets},
	}
}

func directoryRecordDataSetSequence(items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tagDirectoryRecordSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}
}

func hierarchicalDirectoryRecord(offset uint32, recordType string, inUse uint16, next, lower uint32, fileID ...string) core.DataSet {
	record := directoryRecord(fileID...)
	record.Put(uint32Element(tagOffsetNextDirectoryRecord, next))
	record.Put(uint16Element(tagRecordInUseFlag, inUse))
	record.Put(uint32Element(tagOffsetLowerLevelRecord, lower))
	record.Put(stringElement(tagDirectoryRecordType, core.VRCS, recordType))
	dataset := record.ToDataSet()
	dataset.ItemOffset = int64(offset)
	dataset.ItemOffsetSet = true
	return dataset
}

func uint16Element(tag core.Tag, value uint16) core.Element {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return core.NewRawElement(tag, core.VRUS, raw)
}

func uint32Element(tag core.Tag, value uint32) core.Element {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, value)
	return core.NewRawElement(tag, core.VRUL, raw)
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	raw := []byte(value)
	if len(raw)%2 != 0 {
		raw = append(raw, ' ')
	}
	return core.NewRawElement(tag, vr, raw)
}

func littleEndianTagBytes(tag core.Tag) []byte {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint16(raw[0:2], tag.Group)
	binary.LittleEndian.PutUint16(raw[2:4], tag.Element)
	return raw
}

func patchUint32ElementValue(t *testing.T, raw []byte, tag core.Tag, searchFrom int, value uint32) {
	t.Helper()
	offset := bytes.Index(raw[searchFrom:], littleEndianTagBytes(tag))
	if offset < 0 {
		t.Fatalf("encoded DICOMDIR has no element %s after offset %d", tag, searchFrom)
	}
	valueOffset := searchFrom + offset + 8 // UL uses an 8-byte Explicit VR header.
	if valueOffset+4 > len(raw) {
		t.Fatalf("encoded value for %s extends past file", tag)
	}
	binary.LittleEndian.PutUint32(raw[valueOffset:valueOffset+4], value)
}

func uiElement(tag core.Tag, uid string) core.Element {
	value := []byte(uid)
	if len(value)%2 != 0 {
		value = append(value, 0)
	}
	return core.NewRawElement(tag, core.VRUI, value)
}
