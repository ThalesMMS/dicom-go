package dicomtest

import (
	"bytes"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestEncodeElementProducesExpectedBytes(t *testing.T) {
	elem := NewPNElement(core.NewTag(0x0010, 0x0010), "TEST^PATIENT")

	got := EncodeElement(elem, transfer.ExplicitVRLittleEndian)
	want := []byte{
		0x10, 0x00, 0x10, 0x00,
		'P', 'N',
		0x0C, 0x00,
		'T', 'E', 'S', 'T', '^', 'P', 'A', 'T', 'I', 'E', 'N', 'T',
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("unexpected encoding:\n got %v\nwant %v", got, want)
	}
}

func TestFileMetaBuilderEncodesGroupLengthFirst(t *testing.T) {
	reader := parser.NewReader(
		bytes.NewReader(NewFileMetaBuilder().WithTransferSyntax(transfer.ImplicitVRLittleEndian.UID).Encode()),
		transfer.ExplicitVRLittleEndian,
		parser.ReaderOptions{Dictionary: std.Dictionary},
	)
	first, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.Element.Tag() != tagFileMetaInformationGroupLength {
		t.Fatalf("unexpected first tag: %s", first.Element.Tag())
	}
	if first.Element.VR() != core.VRUL {
		t.Fatalf("unexpected first VR: %s", first.Element.VR())
	}
}

func TestMinimalFileIsParseableByObjectReadFile(t *testing.T) {
	data, err := ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	assertDatasetString(t, file.Dataset, core.NewTag(0x0010, 0x0010), "TEST^PATIENT", "patient name")
	assertDatasetString(t, file.Dataset, core.NewTag(0x0010, 0x0020), "TESTID001", "patient id")
	assertDatasetString(t, file.Dataset, core.NewTag(0x0008, 0x0018), TestSOPInstanceUID, "SOP instance uid")
	assertDatasetString(t, file.Dataset, core.NewTag(0x0020, 0x000D), TestStudyInstanceUID, "study instance uid")
}

func TestImplicitVRFileIsParseableByObjectReadFile(t *testing.T) {
	data, err := ImplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if file.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q", file.TransferSyntax.UID)
	}
	assertDatasetString(t, file.Dataset, core.NewTag(0x0010, 0x0010), "TEST^PATIENT", "patient name")
	assertDatasetString(t, file.Dataset, core.NewTag(0x0010, 0x0020), "TESTID001", "patient id")
	assertDatasetString(t, file.Dataset, core.NewTag(0x0008, 0x0018), TestSOPInstanceUID, "SOP instance uid")
	assertDatasetString(t, file.Dataset, core.NewTag(0x0020, 0x000D), TestStudyInstanceUID, "study instance uid")
}

func TestImplicitVRSequenceFileIsParseableByObjectReadFile(t *testing.T) {
	data, err := ImplicitVRSequenceFile()
	if err != nil {
		t.Fatal(err)
	}

	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if file.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q", file.TransferSyntax.UID)
	}

	seqElem, ok := file.Dataset.Get(ImplicitSequenceTag)
	if !ok {
		t.Fatalf("missing implicit-VR sequence element %s", ImplicitSequenceTag)
	}
	if seqElem.VR() != core.VRSQ {
		t.Fatalf("sequence VR = %s, want %s", seqElem.VR(), core.VRSQ)
	}

	items, ok := file.GetSequence(ImplicitSequenceTag)
	if !ok {
		t.Fatalf("GetSequence(%s) = ok false, want true", ImplicitSequenceTag)
	}
	if len(items) != 1 {
		t.Fatalf("sequence item count = %d, want 1", len(items))
	}

	gotUID, ok := items[0].GetString(tagSeriesInstanceUID)
	if !ok {
		t.Fatalf("missing nested SeriesInstanceUID %s", tagSeriesInstanceUID)
	}
	if gotUID != TestSeriesInstanceUID {
		t.Fatalf("nested SeriesInstanceUID = %q, want %q", gotUID, TestSeriesInstanceUID)
	}
}

func TestMinimalFileIsByteStable(t *testing.T) {
	first := MinimalFile()
	second := MinimalFile()
	if !bytes.Equal(first, second) {
		t.Fatal("fixture bytes changed between invocations")
	}
	if len(first) < 132 {
		t.Fatalf("fixture too small: %d", len(first))
	}
	if string(first[128:132]) != "DICM" {
		t.Fatalf("missing DICM marker: %q", first[128:132])
	}
}

func TestTransferSyntaxVariantsHaveDifferentBytesSameLogicalContent(t *testing.T) {
	explicit, err := ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	implicit, err := ImplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	bigEndian, err := BigEndianFile()
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(explicit, implicit) {
		t.Fatal("explicit and implicit fixtures should not have identical bytes")
	}
	if bytes.Equal(explicit, bigEndian) {
		t.Fatal("explicit and big endian fixtures should not have identical bytes")
	}
	if bytes.Equal(implicit, bigEndian) {
		t.Fatal("implicit and big endian fixtures should not have identical bytes")
	}

	assertLogicalContent := func(t *testing.T, data []byte, wantUID string) {
		t.Helper()
		file, err := object.ReadFile(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if file.TransferSyntax.UID != wantUID {
			t.Fatalf("unexpected transfer syntax uid: %q", file.TransferSyntax.UID)
		}
		assertDatasetString(t, file.Dataset, core.NewTag(0x0010, 0x0010), "TEST^PATIENT", "patient name")
		assertDatasetString(t, file.Dataset, core.NewTag(0x0010, 0x0020), "TESTID001", "patient id")
		assertDatasetString(t, file.Dataset, core.NewTag(0x0008, 0x0018), TestSOPInstanceUID, "SOP instance uid")
		assertDatasetString(t, file.Dataset, core.NewTag(0x0020, 0x000D), TestStudyInstanceUID, "study instance uid")
	}

	assertLogicalContent(t, explicit, transfer.ExplicitVRLittleEndian.UID)
	assertLogicalContent(t, implicit, transfer.ImplicitVRLittleEndian.UID)
	assertLogicalContent(t, bigEndian, transfer.ExplicitVRBigEndian.UID)
}

func TestDatasetWithPixelDataIncludesExpectedPayload(t *testing.T) {
	elements := DatasetWithPixelData()
	pixelData := findElement(elements, tagPixelData)
	if pixelData.Tag() != tagPixelData {
		t.Fatalf("missing pixel data element: %v", elements)
	}
	raw, ok := pixelData.RawBytes()
	if !ok {
		t.Fatal("pixel data fixture was not encoded as raw bytes")
	}
	want := syntheticPixelData8x8()
	if !bytes.Equal(raw, want) {
		t.Fatalf("unexpected pixel data payload:\n got %v\nwant %v", raw, want)
	}
}

func findElement(elements []core.Element, tag core.Tag) core.Element {
	for _, elem := range elements {
		if elem.Tag() == tag {
			return elem
		}
	}
	return core.Element{}
}

func assertDatasetString(t *testing.T, ds *object.Object, tag core.Tag, expected string, label string) {
	t.Helper()
	got, ok := ds.GetString(tag)
	if !ok || got != expected {
		t.Fatalf("unexpected %s: %q ok=%v", label, got, ok)
	}
}
