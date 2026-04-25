package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"testing"
)

func TestExplicitVRLittleEndianElement(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", tok.Element.Tag())
	}
	if got := tok.Element.StringValue(); got != "TEST" {
		t.Fatalf("unexpected value: %q", got)
	}
}
func TestReadAllFromMinimalFileElementStream(t *testing.T) {
	data := dicomtest.MinimalFile()
	if len(data) < 132 {
		t.Fatalf("minimal file too short for Part 10 preamble: got %d bytes", len(data))
	}
	reader := NewReader(
		bytes.NewReader(data[132:]),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	elements, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(elements) == 0 {
		t.Fatal("expected at least one element")
	}

	foundPatientName := false
	for _, elem := range elements {
		if elem.Tag() == core.NewTag(0x0010, 0x0010) {
			foundPatientName = true
			if got := elem.StringValue(); got != "TEST^PATIENT" {
				t.Fatalf("unexpected patient name: %q", got)
			}
		}
	}
	if !foundPatientName {
		t.Fatal("patient name not found in minimal file element stream")
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after ReadAll consumed stream, got %v", err)
	}
}
func TestImplicitVRLittleEndianElement(t *testing.T) {
	buf := dicomtest.ImplicitElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", tok.Element.Tag())
	}
	if tok.Element.VR() != core.VRPN {
		t.Fatalf("unexpected VR: %s", tok.Element.VR())
	}
	if got := tok.Element.StringValue(); got != "TEST" {
		t.Fatalf("unexpected value: %q", got)
	}
}
func TestImplicitVRLittleEndianPixelFixtureUsesDictionarySeed(t *testing.T) {
	buf := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	reader := NewReader(bytes.NewReader(buf), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	elements, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	wantVRs := map[core.Tag]core.VR{
		core.NewTag(0x0028, 0x0101): core.VRUS,
		core.NewTag(0x0028, 0x0102): core.VRUS,
		core.NewTag(0x0028, 0x0103): core.VRUS,
	}
	pixelDataTag := core.NewTag(0x7FE0, 0x0010)
	foundPixelData := false

	for _, elem := range elements {
		if elem.Tag() == pixelDataTag {
			foundPixelData = true
			if elem.VR() == core.VRUN {
				t.Fatalf("element %s VR = %s, want a resolved non-UN VR", elem.Tag(), elem.VR())
			}
			continue
		}

		wantVR, ok := wantVRs[elem.Tag()]
		if !ok {
			continue
		}
		if elem.VR() != wantVR {
			t.Fatalf("element %s VR = %s, want %s", elem.Tag(), elem.VR(), wantVR)
		}
		delete(wantVRs, elem.Tag())
	}

	if len(wantVRs) != 0 {
		t.Fatalf("missing expected elements after parse: %v", wantVRs)
	}
	if !foundPixelData {
		t.Fatalf("missing expected element after parse: %s", pixelDataTag)
	}
}
func TestReadDataSetImplicitVRSequenceUsesDictionaryLookup(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.ImplicitVRSequenceDataSet().Elements...)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}

	seqTag := dicomtest.ImplicitSequenceTag
	var seqElem core.Element
	var ok bool
	for _, elem := range got.Elements {
		if elem.Tag() == seqTag {
			seqElem = elem
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("missing implicit-VR sequence element %s", seqTag)
	}
	if seqElem.VR() != core.VRSQ {
		t.Fatalf("sequence VR = %s, want %s", seqElem.VR(), core.VRSQ)
	}

	seqValue, ok := seqElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("sequence value type = %T, want core.SequenceValue", seqElem.Value)
	}
	if len(seqValue.Items) != 1 {
		t.Fatalf("sequence item count = %d, want 1", len(seqValue.Items))
	}
	if len(seqValue.Items[0].Elements) != 1 {
		t.Fatalf("item element count = %d, want 1", len(seqValue.Items[0].Elements))
	}

	nested := seqValue.Items[0].Elements[0]
	if nested.Tag() != core.NewTag(0x0020, 0x000E) {
		t.Fatalf("nested tag = %s, want %s", nested.Tag(), core.NewTag(0x0020, 0x000E))
	}
	if nested.VR() != core.VRUI {
		t.Fatalf("nested VR = %s, want %s", nested.VR(), core.VRUI)
	}
	if gotUID := nested.StringValue(); gotUID != dicomtest.TestSeriesInstanceUID {
		t.Fatalf("nested SeriesInstanceUID = %q, want %q", gotUID, dicomtest.TestSeriesInstanceUID)
	}
}
func TestImplicitVRPrivateTagsFallbackToUNAndPreserveReaderState(t *testing.T) {
	privateCreatorTag := core.NewTag(0x0011, 0x0010)
	privateDataTag := core.NewTag(0x0011, 0x1001)
	patientNameTag := core.NewTag(0x0010, 0x0010)

	stream := bytes.Join([][]byte{
		definedElementBytes(transfer.ImplicitVRLittleEndian, privateCreatorTag, core.VRLO, 8, []byte("CREATOR ")),
		definedElementBytes(transfer.ImplicitVRLittleEndian, privateDataTag, core.VRUN, 4, []byte{0xDE, 0xAD, 0xBE, 0xEF}),
		definedElementBytes(transfer.ImplicitVRLittleEndian, patientNameTag, core.VRPN, 10, []byte("AFTER^TEST")),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != privateCreatorTag {
		t.Fatalf("first tag = %s, want %s", tok.Element.Tag(), privateCreatorTag)
	}
	if tok.Element.VR() != core.VRUN {
		t.Fatalf("private creator VR = %s, want %s", tok.Element.VR(), core.VRUN)
	}
	if raw, ok := tok.Element.RawBytes(); !ok || !bytes.Equal(raw, []byte("CREATOR ")) {
		t.Fatalf("private creator raw = %v ok=%v, want %v true", raw, ok, []byte("CREATOR "))
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != privateDataTag {
		t.Fatalf("second tag = %s, want %s", tok.Element.Tag(), privateDataTag)
	}
	if tok.Element.VR() != core.VRUN {
		t.Fatalf("private element VR = %s, want %s", tok.Element.VR(), core.VRUN)
	}
	if raw, ok := tok.Element.RawBytes(); !ok || !bytes.Equal(raw, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Fatalf("private element raw = %v ok=%v, want %v true", raw, ok, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != patientNameTag {
		t.Fatalf("third tag = %s, want %s", tok.Element.Tag(), patientNameTag)
	}
	if tok.Element.VR() != core.VRPN {
		t.Fatalf("patient name VR = %s, want %s", tok.Element.VR(), core.VRPN)
	}
	if got := tok.Element.StringValue(); got != "AFTER^TEST" {
		t.Fatalf("patient name = %q, want %q", got, "AFTER^TEST")
	}
}
func TestImplicitVRPrivateTagRejectsUndefinedLengthWithoutCorruptingReaderState(t *testing.T) {
	privateDataTag := core.NewTag(0x0011, 0x1001)
	patientNameTag := core.NewTag(0x0010, 0x0010)

	stream := bytes.Join([][]byte{
		implicitHeaderBytes(binary.LittleEndian, privateDataTag, 0xFFFFFFFF),
		definedElementBytes(transfer.ImplicitVRLittleEndian, patientNameTag, core.VRPN, 10, []byte("AFTER^TEST")),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnsupportedUndefinedLength) {
		t.Fatalf("expected ErrUnsupportedUndefinedLength, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("op = %s, want %s", parseErr.Op, OpReadValue)
	}
	if parseErr.Tag != privateDataTag {
		t.Fatalf("tag = %s, want %s", parseErr.Tag, privateDataTag)
	}
	if parseErr.VR != core.VRUN {
		t.Fatalf("VR = %s, want %s", parseErr.VR, core.VRUN)
	}
	if parseErr.Length != core.UndefinedLength {
		t.Fatalf("length = %s, want %s", parseErr.Length, core.UndefinedLength)
	}

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != patientNameTag {
		t.Fatalf("subsequent tag = %s, want %s", tok.Element.Tag(), patientNameTag)
	}
	if tok.Element.VR() != core.VRPN {
		t.Fatalf("subsequent VR = %s, want %s", tok.Element.VR(), core.VRPN)
	}
	if got := tok.Element.StringValue(); got != "AFTER^TEST" {
		t.Fatalf("subsequent patient name = %q, want %q", got, "AFTER^TEST")
	}
}
func TestReadHeaderImplicitVRUsesDictionaryForRetiredTag(t *testing.T) {
	tag := core.NewTag(0x0004, 0x1504)
	reader := NewReader(
		bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, tag, 4)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.Tag != tag {
		t.Fatalf("header tag = %s, want %s", header.Tag, tag)
	}
	if header.VR != core.VRUL {
		t.Fatalf("header VR = %s, want %s", header.VR, core.VRUL)
	}
}
func TestReadDataSetImplicitVRUsesCustomDictionaryForSequenceTag(t *testing.T) {
	customSeqTag := core.NewTag(0x7777, 0x0010)
	patientNameTag := core.NewTag(0x0010, 0x0010)
	itemValue := definedElementBytes(transfer.ImplicitVRLittleEndian, patientNameTag, core.VRPN, 10, []byte("CUSTOM^SQ "))
	stream := bytes.Join([][]byte{
		implicitHeaderBytes(binary.LittleEndian, customSeqTag, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(itemValue))),
		itemValue,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	dict := &multiCountingDictionary{
		entries: map[core.Tag]core.VR{
			customSeqTag:   core.VRSQ,
			patientNameTag: core.VRPN,
		},
	}

	reader := NewReader(bytes.NewReader(stream), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: dict})
	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if dict.byTagCalls == 0 {
		t.Fatal("custom dictionary was not consulted")
	}
	if len(got.Elements) != 1 {
		t.Fatalf("element count = %d, want 1", len(got.Elements))
	}

	seqElem := got.Elements[0]
	if seqElem.Tag() != customSeqTag {
		t.Fatalf("sequence tag = %s, want %s", seqElem.Tag(), customSeqTag)
	}
	if seqElem.VR() != core.VRSQ {
		t.Fatalf("sequence VR = %s, want %s", seqElem.VR(), core.VRSQ)
	}

	seqValue, ok := seqElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("sequence value type = %T, want core.SequenceValue", seqElem.Value)
	}
	if len(seqValue.Items) != 1 {
		t.Fatalf("sequence item count = %d, want 1", len(seqValue.Items))
	}
	if len(seqValue.Items[0].Elements) != 1 {
		t.Fatalf("item element count = %d, want 1", len(seqValue.Items[0].Elements))
	}

	nested := seqValue.Items[0].Elements[0]
	if nested.Tag() != patientNameTag {
		t.Fatalf("nested tag = %s, want %s", nested.Tag(), patientNameTag)
	}
	if nested.VR() != core.VRPN {
		t.Fatalf("nested VR = %s, want %s", nested.VR(), core.VRPN)
	}
	if got := nested.StringValue(); got != "CUSTOM^SQ" {
		t.Fatalf("nested patient name = %q, want %q", got, "CUSTOM^SQ")
	}
}
func TestExplicitVRBigEndianElement(t *testing.T) {
	buf := dicomtest.BigEndianElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRBigEndian, ReaderOptions{Dictionary: std.Dictionary})
	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", tok.Element.Tag())
	}
	if got := tok.Element.StringValue(); got != "TEST" {
		t.Fatalf("unexpected value: %q", got)
	}
}
func TestReaderPositionStartsAtZero(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	if pos := reader.Position(); pos != 0 {
		t.Fatalf("Position() before any read = %d, want 0", pos)
	}
}
func TestReaderPositionAdvancesAfterRead(t *testing.T) {
	elem := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST")
	buf := dicomtest.EncodeElement(elem, transfer.ExplicitVRLittleEndian)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if pos := reader.Position(); pos != int64(len(buf)) {
		t.Fatalf("Position() after full element read = %d, want %d", pos, len(buf))
	}
}
func TestReaderBaseOffsetShiftsReportedPosition(t *testing.T) {
	const baseOffset = int64(132)
	elem := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST")
	buf := dicomtest.EncodeElement(elem, transfer.ExplicitVRLittleEndian)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary: std.Dictionary,
		BaseOffset: baseOffset,
	})

	if pos := reader.Position(); pos != baseOffset {
		t.Fatalf("Position() before read with BaseOffset = %d, want %d", pos, baseOffset)
	}

	_, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	want := baseOffset + int64(len(buf))
	if pos := reader.Position(); pos != want {
		t.Fatalf("Position() after read with BaseOffset = %d, want %d", pos, want)
	}
}
func TestReaderBaseOffsetAppearsInParseError(t *testing.T) {
	const baseOffset = int64(512)
	// A single 4-byte tag header only: truncated so value read fails.
	buf := []byte{0x10, 0x00, 0x10, 0x00, 'P', 'N', 0x04, 0x00} // header only, no value bytes
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary: std.Dictionary,
		BaseOffset: baseOffset,
	})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error from truncated element, got nil")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected *ParseError, got %T: %v", err, err)
	}
	expectedOffset := baseOffset + int64(len(buf))
	if parseErr.Offset != expectedOffset {
		t.Fatalf("ParseError.Offset = %d, want %d", parseErr.Offset, expectedOffset)
	}
}
func TestNilReaderPositionReturnsZero(t *testing.T) {
	var r *Reader
	if pos := r.Position(); pos != 0 {
		t.Fatalf("nil Reader.Position() = %d, want 0", pos)
	}
}
