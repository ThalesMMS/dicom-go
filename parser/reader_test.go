package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
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
	if len(elements) < 8 {
		t.Fatalf("expected multiple elements, got %d", len(elements))
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
}

func TestReadDataSetBuildsSequenceWithTwoItems(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1111)
	itemOne := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "ONE^ITEM"),
		dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "ITEM001"),
	)
	itemTwo := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TWO^ITEM"),
		dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "ITEM002"),
	)
	stream := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, seqTag, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(itemOne))),
		itemOne,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(itemTwo))),
		itemTwo,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Elements) != 1 {
		t.Fatalf("element count = %d, want 1", len(got.Elements))
	}
	seqElem := got.Elements[0]
	if seqElem.Tag() != seqTag {
		t.Fatalf("sequence tag = %s, want %s", seqElem.Tag(), seqTag)
	}
	seqValue, ok := seqElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("sequence value type = %T, want core.SequenceValue", seqElem.Value)
	}
	if len(seqValue.Items) != 2 {
		t.Fatalf("sequence item count = %d, want 2", len(seqValue.Items))
	}
	if len(seqValue.Items[0].Elements) != 2 {
		t.Fatalf("first item element count = %d, want 2", len(seqValue.Items[0].Elements))
	}
	if got := seqValue.Items[0].Elements[0].StringValue(); got != "ONE^ITEM" {
		t.Fatalf("first item patient name = %q, want %q", got, "ONE^ITEM")
	}
	if got := seqValue.Items[0].Elements[1].StringValue(); got != "ITEM001" {
		t.Fatalf("first item patient id = %q, want %q", got, "ITEM001")
	}
	if len(seqValue.Items[1].Elements) != 2 {
		t.Fatalf("second item element count = %d, want 2", len(seqValue.Items[1].Elements))
	}
	if got := seqValue.Items[1].Elements[0].StringValue(); got != "TWO^ITEM" {
		t.Fatalf("second item patient name = %q, want %q", got, "TWO^ITEM")
	}
	if got := seqValue.Items[1].Elements[1].StringValue(); got != "ITEM002" {
		t.Fatalf("second item patient id = %q, want %q", got, "ITEM002")
	}
}

func TestReadDataSetBuildsNestedSequences(t *testing.T) {
	outerSeqTag := core.NewTag(0x0008, 0x1111)
	innerSeqTag := core.NewTag(0x0008, 0x1115)
	innerItem := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "NEST^ONE"),
	)
	innerSeq := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, innerSeqTag, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(innerItem))),
		innerItem,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)
	stream := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, outerSeqTag, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(innerSeq))),
		innerSeq,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Elements) != 1 {
		t.Fatalf("element count = %d, want 1", len(got.Elements))
	}
	outer, ok := got.Elements[0].Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("outer value type = %T, want core.SequenceValue", got.Elements[0].Value)
	}
	if len(outer.Items) != 1 {
		t.Fatalf("outer item count = %d, want 1", len(outer.Items))
	}
	if len(outer.Items[0].Elements) != 1 {
		t.Fatalf("outer item element count = %d, want 1", len(outer.Items[0].Elements))
	}
	innerElem := outer.Items[0].Elements[0]
	if innerElem.Tag() != innerSeqTag {
		t.Fatalf("inner sequence tag = %s, want %s", innerElem.Tag(), innerSeqTag)
	}
	inner, ok := innerElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("inner value type = %T, want core.SequenceValue", innerElem.Value)
	}
	if len(inner.Items) != 1 {
		t.Fatalf("inner item count = %d, want 1", len(inner.Items))
	}
	if len(inner.Items[0].Elements) != 1 {
		t.Fatalf("innermost element count = %d, want 1", len(inner.Items[0].Elements))
	}
	leaf := inner.Items[0].Elements[0]
	if leaf.Tag() != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("innermost tag = %s, want %s", leaf.Tag(), core.NewTag(0x0010, 0x0010))
	}
	if got := leaf.StringValue(); got != "NEST^ONE" {
		t.Fatalf("innermost patient name = %q, want %q", got, "NEST^ONE")
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
		sequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(itemValue))),
		itemValue,
		sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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

func TestNextReturnsCleanEOFOnlyAtElementBoundary(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}

	_, err := reader.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected clean EOF, got %v", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected clean EOF, got unexpected EOF: %v", err)
	}
	var parseErr *ParseError
	if errors.As(err, &parseErr) {
		t.Fatalf("expected plain EOF at element boundary, got parse error: %v", parseErr)
	}
}

func TestNextWrapsPartialHeaderEOFWithOffset(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "one_tag_byte", data: []byte{0x10}},
		{name: "two_tag_bytes", data: []byte{0x10, 0x00}},
		{name: "three_tag_bytes", data: []byte{0x10, 0x00, 0x10}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(
				bytes.NewReader(tt.data),
				transfer.ExplicitVRLittleEndian,
				ReaderOptions{Dictionary: std.Dictionary, BaseOffset: 132},
			)

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, io.EOF) {
				t.Fatalf("partial header should not match io.EOF: %v", err)
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("expected unexpected EOF, got %v", err)
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != OpReadTag {
				t.Fatalf("unexpected op: %s", parseErr.Op)
			}
			if parseErr.Offset != 132 {
				t.Fatalf("unexpected offset: got %d want 132", parseErr.Offset)
			}
			if !strings.Contains(parseErr.Error(), "offset 132") {
				t.Fatalf("error string missing offset: %v", parseErr)
			}
		})
	}
}

func TestNextWrapsValueEOFWithContext(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	truncated := buf[:len(buf)-2]
	reader := NewReader(bytes.NewReader(truncated), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("partial value should not match io.EOF: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Offset != 8 {
		t.Fatalf("unexpected value offset: got %d want 8", parseErr.Offset)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if parseErr.VR != core.VRPN {
		t.Fatalf("unexpected VR: %s", parseErr.VR)
	}
	if parseErr.Length != 4 {
		t.Fatalf("unexpected length: %s", parseErr.Length)
	}
	msg := parseErr.Error()
	for _, want := range []string{"read value", "offset 8", "(0010,0010)", "VR PN", "length 4"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error string %q missing %q", msg, want)
		}
	}
}

func TestNextWrapsMaxElementBytesGuardAsParseError(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		vr     core.VR
		tag    core.Tag
	}{
		{name: "explicit little endian", syntax: transfer.ExplicitVRLittleEndian, vr: core.VRPN, tag: core.NewTag(0x0010, 0x0010)},
		{name: "implicit little endian", syntax: transfer.ImplicitVRLittleEndian, vr: core.VRPN, tag: core.NewTag(0x0010, 0x0010)},
		{name: "explicit big endian", syntax: transfer.ExplicitVRBigEndian, vr: core.VRPN, tag: core.NewTag(0x0010, 0x0010)},
		{name: "explicit little endian long header", syntax: transfer.ExplicitVRLittleEndian, vr: core.VROB, tag: core.NewTag(0x0002, 0x0001)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := []byte("TEST")
			if tt.vr == core.VROB {
				value = []byte{1, 2, 3, 4}
			}
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, uint32(len(value)), value)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{
				Dictionary:      std.Dictionary,
				MaxElementBytes: 2,
			})

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != OpReadValue {
				t.Fatalf("unexpected op: %s", parseErr.Op)
			}
			wantOffset := definedHeaderLength(tt.syntax, tt.vr)
			if parseErr.Offset != wantOffset {
				t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, wantOffset)
			}
			if parseErr.Tag != tt.tag {
				t.Fatalf("unexpected tag: %s", parseErr.Tag)
			}
			if parseErr.VR != tt.vr {
				t.Fatalf("unexpected VR: %s", parseErr.VR)
			}
			if parseErr.Length != 4 {
				t.Fatalf("unexpected length: %s", parseErr.Length)
			}
			if got := reader.Position(); got != wantOffset {
				t.Fatalf("reader position after size-limit error = %d, want %d", got, wantOffset)
			}
		})
	}
}

func TestNextRejectsGiantElementLengthBeforeAllocation(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7FE0, 0x0010), core.VROB, [2]byte{}, 0xFFFFFFFE)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:      std.Dictionary,
		MaxElementBytes: 16,
	})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxElementBytesExceeded) {
		t.Fatalf("expected ErrMaxElementBytesExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Length != core.Length(0xFFFFFFFE) {
		t.Fatalf("unexpected length: got %s want %s", parseErr.Length, core.Length(0xFFFFFFFE))
	}
	if got := reader.Position(); got != definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VROB) {
		t.Fatalf("reader position after size-limit error = %d", got)
	}
}

func TestNextRejectsMaxElements(t *testing.T) {
	stream := bytes.Join([][]byte{
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "ONE^TEST"), transfer.ExplicitVRLittleEndian),
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0020), "TWO^TEST"), transfer.ExplicitVRLittleEndian),
	}, nil)
	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:  std.Dictionary,
		MaxElements: 1,
	})

	if _, err := reader.Next(); err != nil {
		t.Fatalf("first element: %v", err)
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxElementsExceeded) {
		t.Fatalf("expected ErrMaxElementsExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckElementCount {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0020) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
}

func TestNextRejectsMaxTotalBytesAtBoundary(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:    std.Dictionary,
		MaxTotalBytes: int64(len(buf)),
	})

	if _, err := reader.Next(); err != nil {
		t.Fatalf("first element: %v", err)
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxTotalBytesExceeded) {
		t.Fatalf("expected ErrMaxTotalBytesExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckTotalBytes {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Offset != int64(len(buf)) {
		t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, len(buf))
	}
}

func TestNextRejectsInvalidExplicitVRBytes(t *testing.T) {
	// Unlike permissive readers, dicom-go rejects unknown VR bytes during
	// header parsing instead of silently treating them as UN or raw bytes.
	buf := []byte{
		0x10, 0x00, 0x10, 0x00,
		'Z', 'Z',
		0x04, 0x00,
		'T', 'E', 'S', 'T',
	}
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadVR {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if !strings.Contains(parseErr.Error(), `invalid VR "ZZ"`) {
		t.Fatalf("unexpected error message: %v", parseErr)
	}
}

func TestNextRejectsNonASCIIExplicitVRBytes(t *testing.T) {
	buf := []byte{
		0x10, 0x00, 0x10, 0x00,
		0xFF, 0xFE,
		0x04, 0x00,
		'T', 'E', 'S', 'T',
	}
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadVR {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if !strings.Contains(parseErr.Error(), "invalid VR") {
		t.Fatalf("unexpected error message: %v", parseErr)
	}
}

func TestNextRejectsGarbageInputWithOffsetContext(t *testing.T) {
	tests := []struct {
		name       string
		syntax     transfer.Syntax
		data       []byte
		wantOp     Op
		wantOffset int64
	}{
		{
			name:       "random_bytes_implicit_vr",
			syntax:     transfer.ImplicitVRLittleEndian,
			data:       []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x20, 0x00, 0x00, 0x00, 0x99},
			wantOp:     OpReadValue,
			wantOffset: 8,
		},
		{
			name:       "truncated_explicit_vr_header_after_tag",
			syntax:     transfer.ExplicitVRLittleEndian,
			data:       []byte{0x10, 0x00, 0x10, 0x00, 'P'},
			wantOp:     OpReadVR,
			wantOffset: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(bytes.NewReader(tt.data), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != tt.wantOp {
				t.Fatalf("unexpected op: got %s want %s", parseErr.Op, tt.wantOp)
			}
			if parseErr.Offset != tt.wantOffset {
				t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, tt.wantOffset)
			}
		})
	}
}

func TestReadAllPropagatesPartialReadError(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	truncated := buf[:len(buf)-2]
	reader := NewReader(bytes.NewReader(truncated), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.ReadAll()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("ReadAll should not treat partial read as clean EOF: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("unexpected op: %s", parseErr.Op)
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

func TestNextReadsZeroLengthDefinedValueAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		tag    core.Tag
		vr     core.VR
	}{
		{name: "explicit little endian pn", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit little endian lo", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO},
		{name: "explicit little endian us", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS},
		{name: "explicit little endian ob", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
		{name: "implicit little endian pn", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "implicit little endian lo", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO},
		{name: "implicit little endian us", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS},
		{name: "implicit little endian ob", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
		{name: "explicit big endian pn", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit big endian lo", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO},
		{name: "explicit big endian us", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS},
		{name: "explicit big endian ob", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, 0, nil)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tok.Kind != TokenElement {
				t.Fatalf("token kind = %v, want %v", tok.Kind, TokenElement)
			}
			if tok.Element.Tag() != tt.tag {
				t.Fatalf("unexpected tag: got %s want %s", tok.Element.Tag(), tt.tag)
			}
			if tok.Element.VR() != tt.vr {
				t.Fatalf("unexpected VR: got %s want %s", tok.Element.VR(), tt.vr)
			}
			if tok.Header.Length != 0 {
				t.Fatalf("header length = %s, want 0", tok.Header.Length)
			}
			if tok.Element.EncodedLength() != 0 {
				t.Fatalf("element encoded length = %s, want 0", tok.Element.EncodedLength())
			}
			raw, ok := tok.Element.RawBytes()
			if !ok {
				t.Fatalf("expected raw value")
			}
			if len(raw) != 0 {
				t.Fatalf("raw length = %d, want 0", len(raw))
			}
			wantPos := definedHeaderLength(tt.syntax, tt.vr)
			if got := reader.Position(); got != wantPos {
				t.Fatalf("reader position = %d, want %d", got, wantPos)
			}
		})
	}
}

func TestNextPreservesRawMultiValueStringsAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		tag    core.Tag
		vr     core.VR
		value  []byte
		want   []string
	}{
		{name: "explicit little endian pn", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("ALPHA^ONE\\BETA^TWO  "), want: []string{"ALPHA^ONE", "BETA^TWO"}},
		{name: "explicit little endian lo", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, value: []byte("LEFT\\RIGHT  "), want: []string{"LEFT", "RIGHT"}},
		{name: "implicit little endian pn", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("ALPHA^ONE\\BETA^TWO  "), want: []string{"ALPHA^ONE", "BETA^TWO"}},
		{name: "implicit little endian lo", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, value: []byte("LEFT\\RIGHT  "), want: []string{"LEFT", "RIGHT"}},
		{name: "explicit big endian pn", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("ALPHA^ONE\\BETA^TWO  "), want: []string{"ALPHA^ONE", "BETA^TWO"}},
		{name: "explicit big endian lo", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, value: []byte("LEFT\\RIGHT  "), want: []string{"LEFT", "RIGHT"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, uint32(len(tt.value)), tt.value)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			raw, ok := tok.Element.RawBytes()
			if !ok {
				t.Fatalf("expected raw value")
			}
			if !bytes.Equal(raw, tt.value) {
				t.Fatalf("raw bytes = %q, want %q", raw, tt.value)
			}
			gotStrings := tok.Element.StringValues()
			if len(gotStrings) != len(tt.want) {
				t.Fatalf("string value count = %d, want %d", len(gotStrings), len(tt.want))
			}
			for i := range tt.want {
				if gotStrings[i] != tt.want[i] {
					t.Fatalf("string value %d = %q, want %q", i, gotStrings[i], tt.want[i])
				}
			}
			wantPos := definedHeaderLength(tt.syntax, tt.vr) + int64(len(tt.value))
			if got := reader.Position(); got != wantPos {
				t.Fatalf("reader position = %d, want %d", got, wantPos)
			}
		})
	}
}

func TestNextRejectsOddLengthByDefaultAcrossTransferSyntaxes(t *testing.T) {
	testRejectOddLengthAcrossTransferSyntaxes(t, "default policy", ReaderOptions{Dictionary: std.Dictionary})
}

func TestNextRejectsOddLengthWhenExplicitlyConfiguredAcrossTransferSyntaxes(t *testing.T) {
	testRejectOddLengthAcrossTransferSyntaxes(t, "explicit reject policy", ReaderOptions{
		Dictionary:      std.Dictionary,
		OddLengthPolicy: RejectOddLength,
	})
}

func TestNextAcceptsOddLengthWhenConfiguredAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		tag    core.Tag
		vr     core.VR
	}{
		{name: "explicit little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "implicit little endian", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit big endian", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit little endian long header", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := []byte("ODD")
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, uint32(len(value)), value)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{
				Dictionary:      std.Dictionary,
				OddLengthPolicy: AcceptOddLength,
			})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			raw, ok := tok.Element.RawBytes()
			if !ok {
				t.Fatalf("expected raw value")
			}
			if !bytes.Equal(raw, value) {
				t.Fatalf("raw bytes = %q, want %q", raw, value)
			}
			if tok.Header.Length != 3 {
				t.Fatalf("header length = %s, want 3", tok.Header.Length)
			}
			wantPos := definedHeaderLength(tt.syntax, tt.vr) + int64(len(value))
			if got := reader.Position(); got != wantPos {
				t.Fatalf("reader position = %d, want %d", got, wantPos)
			}
		})
	}
}

func TestNextReadsDefinedValuesAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name    string
		syntax  transfer.Syntax
		elems   []integrationElement
		wantEnd int64
	}{
		{
			name:   "explicit little endian",
			syntax: transfer.ExplicitVRLittleEndian,
			elems: []integrationElement{
				{tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("DOE^JOHN"), wantString: "DOE^JOHN"},
				{tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS, value: []byte{0x40, 0x00}},
				{tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, value: []byte{0x00, 0x01}},
			},
		},
		{
			name:   "implicit little endian",
			syntax: transfer.ImplicitVRLittleEndian,
			elems: []integrationElement{
				{tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("DOE^JOHN"), wantString: "DOE^JOHN"},
				{tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS, value: []byte{0x40, 0x00}},
				{tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, value: []byte{0x00, 0x01}},
			},
		},
		{
			name:   "explicit big endian",
			syntax: transfer.ExplicitVRBigEndian,
			elems: []integrationElement{
				{tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("DOE^JOHN"), wantString: "DOE^JOHN"},
				{tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS, value: []byte{0x00, 0x40}},
				{tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, value: []byte{0x00, 0x01}},
			},
		},
	}

	for i := range tests {
		var total int64
		for _, elem := range tests[i].elems {
			total += definedHeaderLength(tests[i].syntax, elem.vr) + int64(len(elem.value))
		}
		tests[i].wantEnd = total
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stream []byte
			var positions []int64
			var offset int64
			for _, elem := range tt.elems {
				stream = append(stream, definedElementBytes(tt.syntax, elem.tag, elem.vr, uint32(len(elem.value)), elem.value)...)
				offset += definedHeaderLength(tt.syntax, elem.vr) + int64(len(elem.value))
				positions = append(positions, offset)
			}

			reader := NewReader(bytes.NewReader(stream), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})
			for i, want := range tt.elems {
				tok, err := reader.Next()
				if err != nil {
					t.Fatalf("reading element %d: %v", i, err)
				}
				if tok.Kind != TokenElement {
					t.Fatalf("element %d token kind = %v, want %v", i, tok.Kind, TokenElement)
				}
				if tok.Element.Tag() != want.tag {
					t.Fatalf("element %d tag = %s, want %s", i, tok.Element.Tag(), want.tag)
				}
				if tok.Element.VR() != want.vr {
					t.Fatalf("element %d VR = %s, want %s", i, tok.Element.VR(), want.vr)
				}
				raw, ok := tok.Element.RawBytes()
				if !ok {
					t.Fatalf("element %d expected raw value", i)
				}
				if !bytes.Equal(raw, want.value) {
					t.Fatalf("element %d raw bytes = %v, want %v", i, raw, want.value)
				}
				if want.wantString != "" && tok.Element.StringValue() != want.wantString {
					t.Fatalf("element %d string value = %q, want %q", i, tok.Element.StringValue(), want.wantString)
				}
				if got := reader.Position(); got != positions[i] {
					t.Fatalf("reader position after element %d = %d, want %d", i, got, positions[i])
				}
			}

			if got := reader.Position(); got != tt.wantEnd {
				t.Fatalf("final reader position = %d, want %d", got, tt.wantEnd)
			}
		})
	}
}

func TestReadHeaderExplicitVRLongFormsDoNotConsumeValue(t *testing.T) {
	tests := []struct {
		name         string
		syntax       transfer.Syntax
		tag          core.Tag
		vr           core.VR
		length       uint32
		value        []byte
		headerLength int64
	}{
		{name: "ob little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "od little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0018, 0x9808), vr: core.VROD, length: 8, value: []byte{1, 2, 3, 4, 5, 6, 7, 8}, headerLength: 12},
		{name: "of little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0018, 0x9820), vr: core.VROF, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "ol little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0066, 0x0123), vr: core.VROL, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "ow little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0028, 0x0100), vr: core.VROW, length: 2, value: []byte{1, 0}, headerLength: 12},
		{name: "sq little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0008, 0x1111), vr: core.VRSQ, length: 8, value: []byte("SEQUENCE"), headerLength: 12},
		{name: "un little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x7777, 0x0010), vr: core.VRUN, length: 4, value: []byte("DATA"), headerLength: 12},
		{name: "uc little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0008, 0x4000), vr: core.VRUC, length: 6, value: []byte("AUTHOR"), headerLength: 12},
		{name: "ur little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0008, 0x0120), vr: core.VRUR, length: 18, value: []byte("https://dicom.dev/"), headerLength: 12},
		{name: "of big endian", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0018, 0x9820), vr: core.VROF, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "ut big endian", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0040, 0xA160), vr: core.VRUT, length: 6, value: []byte("REPORT"), headerLength: 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := append(explicitLongHeaderBytes(tt.syntax.ByteOrder, tt.tag, tt.vr, [2]byte{}, tt.length), tt.value...)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			header, err := reader.readHeader()
			if err != nil {
				t.Fatal(err)
			}
			if header.Tag != tt.tag {
				t.Fatalf("header tag = %s, want %s", header.Tag, tt.tag)
			}
			if header.VR != tt.vr {
				t.Fatalf("header VR = %s, want %s", header.VR, tt.vr)
			}
			if header.Length != core.Length(tt.length) {
				t.Fatalf("header length = %s, want %d", header.Length, tt.length)
			}
			if got := reader.Position(); got != tt.headerLength {
				t.Fatalf("reader position after header = %d, want %d", got, tt.headerLength)
			}
		})
	}
}

func TestReadHeaderStrictReservedRejectsNonZeroBytes(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0040, 0xA160), core.VRUT, [2]byte{0x12, 0x34}, 6)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:          std.Dictionary,
		StrictReservedBytes: true,
	})

	_, err := reader.readHeader()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNonZeroReservedBytes) {
		t.Fatalf("expected reserved-byte error, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpValidateReserved {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
}

func TestReadHeaderLenientReservedAllowsNonZeroBytes(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0040, 0xA160), core.VRUT, [2]byte{0x12, 0x34}, 6)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.VR != core.VRUT {
		t.Fatalf("header VR = %s, want %s", header.VR, core.VRUT)
	}
	if header.Length != 6 {
		t.Fatalf("header length = %s, want 6", header.Length)
	}
}

func TestNextReturnsSequenceAndItemControlTokensWithoutReadingBodies(t *testing.T) {
	inner := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		inner,
		sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenStartSequence {
		t.Fatalf("first token kind = %v, want %v", tok.Kind, TokenStartSequence)
	}
	if tok.Header.Tag != core.NewTag(0x0008, 0x1111) || tok.Header.VR != core.VRSQ || tok.Header.Length != core.UndefinedLength {
		t.Fatalf("unexpected sequence header: %+v", tok.Header)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenStartItem {
		t.Fatalf("second token kind = %v, want %v", tok.Kind, TokenStartItem)
	}
	if tok.Header.Tag != core.TagItem || tok.Header.Length != core.UndefinedLength {
		t.Fatalf("unexpected item header: %+v", tok.Header)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenElement {
		t.Fatalf("third token kind = %v, want %v", tok.Kind, TokenElement)
	}
	if tok.Element.Tag() != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected inner tag: %s", tok.Element.Tag())
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenEndItem {
		t.Fatalf("fourth token kind = %v, want %v", tok.Kind, TokenEndItem)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenEndSequence {
		t.Fatalf("fifth token kind = %v, want %v", tok.Kind, TokenEndSequence)
	}
}

func TestReadDataSetBuildsFragmentSequenceWithEmptyBasicOffsetTable(t *testing.T) {
	fragmentOne := []byte{0x10, 0x11, 0x12, 0x13}
	fragmentTwo := []byte{0x20, 0x21}
	stream := bytes.Join([][]byte{
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "PIXEL^TEST"), transfer.JPEGBaseline),
		encapsulatedPixelDataBytes(nil, fragmentOne, fragmentTwo),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary})
	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Elements) != 2 {
		t.Fatalf("element count = %d, want 2", len(got.Elements))
	}

	pixelElem := got.Elements[1]
	if pixelElem.Tag() != core.TagPixelData {
		t.Fatalf("pixel data tag = %s, want %s", pixelElem.Tag(), core.TagPixelData)
	}
	seq, ok := pixelElem.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("pixel value type = %T, want core.FragmentSequence", pixelElem.Value)
	}
	if len(seq.OffsetTable) != 0 {
		t.Fatalf("offset table len = %d, want 0", len(seq.OffsetTable))
	}
	if len(seq.Fragments) != 2 {
		t.Fatalf("fragment count = %d, want 2", len(seq.Fragments))
	}
	if !bytes.Equal(seq.Fragments[0], fragmentOne) {
		t.Fatalf("first fragment = %v, want %v", seq.Fragments[0], fragmentOne)
	}
	if !bytes.Equal(seq.Fragments[1], fragmentTwo) {
		t.Fatalf("second fragment = %v, want %v", seq.Fragments[1], fragmentTwo)
	}
}

func TestReadDataSetBuildsFragmentSequenceWithPopulatedBasicOffsetTable(t *testing.T) {
	offsetTable := []byte{0x00, 0x00, 0x00, 0x00, 0x12, 0x00, 0x00, 0x00}
	fragmentOne := []byte{0xAA, 0xBB}
	fragmentTwo := []byte{0xCC, 0xDD, 0xEE, 0xFF}
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(offsetTable, fragmentOne, fragmentTwo)),
		transfer.JPEGBaseline,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Elements) != 1 {
		t.Fatalf("element count = %d, want 1", len(got.Elements))
	}

	seq, ok := got.Elements[0].Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("pixel value type = %T, want core.FragmentSequence", got.Elements[0].Value)
	}
	if !bytes.Equal(seq.OffsetTable, offsetTable) {
		t.Fatalf("offset table = %v, want %v", seq.OffsetTable, offsetTable)
	}
	if len(seq.Fragments) != 2 {
		t.Fatalf("fragment count = %d, want 2", len(seq.Fragments))
	}
	if !bytes.Equal(seq.Fragments[0], fragmentOne) {
		t.Fatalf("first fragment = %v, want %v", seq.Fragments[0], fragmentOne)
	}
	if !bytes.Equal(seq.Fragments[1], fragmentTwo) {
		t.Fatalf("second fragment = %v, want %v", seq.Fragments[1], fragmentTwo)
	}
}

func TestReadDataSetBuildsFragmentSequenceWithMultipleFragments(t *testing.T) {
	offsetTable := []byte{}
	fragmentOne := []byte{0x01, 0x02}
	fragmentTwo := []byte{0x10, 0x11, 0x12, 0x13}
	fragmentThreePadded := core.VROB.PadToEvenLength([]byte{0x20, 0x21, 0x22})
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(offsetTable, fragmentOne, fragmentTwo, fragmentThreePadded)),
		transfer.JPEGBaseline,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	seq, ok := got.Elements[0].Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("pixel value type = %T, want core.FragmentSequence", got.Elements[0].Value)
	}
	if len(seq.Fragments) != 3 {
		t.Fatalf("fragment count = %d, want 3", len(seq.Fragments))
	}
	if !bytes.Equal(seq.Fragments[0], fragmentOne) {
		t.Fatalf("first fragment = %v, want %v", seq.Fragments[0], fragmentOne)
	}
	if !bytes.Equal(seq.Fragments[1], fragmentTwo) {
		t.Fatalf("second fragment = %v, want %v", seq.Fragments[1], fragmentTwo)
	}
	if !bytes.Equal(seq.Fragments[2], fragmentThreePadded) {
		t.Fatalf("third fragment = %v, want %v", seq.Fragments[2], fragmentThreePadded)
	}
	if len(seq.Fragments[2])%2 != 0 {
		t.Fatalf("third fragment length = %d, want even padded length", len(seq.Fragments[2]))
	}
}

func TestReadDataSetRejectsMaxFragments(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(nil, []byte{0x01, 0x02}, []byte{0x03, 0x04})),
		transfer.JPEGBaseline,
		ReaderOptions{
			Dictionary:   std.Dictionary,
			MaxFragments: 1,
		},
	)

	_, err := reader.ReadDataSet()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxFragmentsExceeded) {
		t.Fatalf("expected ErrMaxFragmentsExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckFragmentCount {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
}

func TestReadAllTokensReturnsPixelFragmentSequenceFlow(t *testing.T) {
	offsetTable := []byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00}
	fragmentOne := []byte{0x01, 0x02}
	fragmentTwo := []byte{0x03, 0x04, 0x05, 0x00}
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(offsetTable, fragmentOne, fragmentTwo)),
		transfer.JPEGBaseline,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}

	assertTokenKinds(t, tokens, []TokenKind{
		TokenStartPixelSequence,
		TokenElement,
		TokenElement,
		TokenElement,
		TokenEndSequence,
	})

	if tokens[0].Header.Tag != core.TagPixelData {
		t.Fatalf("start token tag = %s, want %s", tokens[0].Header.Tag, core.TagPixelData)
	}
	if tokens[0].Header.VR != core.VROB {
		t.Fatalf("start token VR = %s, want %s", tokens[0].Header.VR, core.VROB)
	}
	if tokens[0].Header.Length != core.UndefinedLength {
		t.Fatalf("start token length = %s, want %s", tokens[0].Header.Length, core.UndefinedLength)
	}

	wantItems := []struct {
		length core.Length
		data   []byte
	}{
		{length: core.Length(len(offsetTable)), data: offsetTable},
		{length: core.Length(len(fragmentOne)), data: fragmentOne},
		{length: core.Length(len(fragmentTwo)), data: fragmentTwo},
	}
	for i, want := range wantItems {
		tok := tokens[i+1]
		if tok.Header.Tag != core.TagItem {
			t.Fatalf("token %d tag = %s, want %s", i+1, tok.Header.Tag, core.TagItem)
		}
		if tok.Header.VR != core.VRUN {
			t.Fatalf("token %d VR = %s, want %s", i+1, tok.Header.VR, core.VRUN)
		}
		if tok.Header.Length != want.length {
			t.Fatalf("token %d length = %s, want %s", i+1, tok.Header.Length, want.length)
		}
		raw, ok := tok.Element.RawBytes()
		if !ok {
			t.Fatalf("token %d missing raw bytes", i+1)
		}
		if !bytes.Equal(raw, want.data) {
			t.Fatalf("token %d data = %v, want %v", i+1, raw, want.data)
		}
	}

	if tokens[4].Header.Tag != core.TagSequenceDelimitationItem {
		t.Fatalf("end token tag = %s, want %s", tokens[4].Header.Tag, core.TagSequenceDelimitationItem)
	}
	if tokens[4].Header.VR != core.VRUN {
		t.Fatalf("end token VR = %s, want %s", tokens[4].Header.VR, core.VRUN)
	}
	if tokens[4].Header.Length != 0 {
		t.Fatalf("end token length = %s, want 0", tokens[4].Header.Length)
	}
}

func TestReadDataSetRejectsEncapsulatedPixelDataWithoutBasicOffsetTableItem(t *testing.T) {
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.TagPixelData, core.VROB, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.ReadDataSet()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMissingBasicOffsetTable) {
		t.Fatalf("expected ErrMissingBasicOffsetTable, got %v", err)
	}
}

func TestNextRejectsUnexpectedTagInsideEncapsulatedPixelData(t *testing.T) {
	unexpected := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "BROKEN^PIXEL"),
		transfer.JPEGBaseline,
	)
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.TagPixelData, core.VROB, [2]byte{}, 0xFFFFFFFF),
		unexpected,
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.JPEGBaseline, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenStartPixelSequence {
		t.Fatalf("first token kind = %v, want %v", tok.Kind, TokenStartPixelSequence)
	}

	_, err = reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("op = %s, want %s", parseErr.Op, OpReadValue)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("tag = %s, want %s", parseErr.Tag, core.NewTag(0x0010, 0x0010))
	}
	if parseErr.VR != core.VRPN {
		t.Fatalf("VR = %s, want %s", parseErr.VR, core.VRPN)
	}
	if parseErr.Length != 12 {
		t.Fatalf("length = %s, want 12", parseErr.Length)
	}
	if !strings.Contains(parseErr.Error(), "unexpected tag (0010,0010) inside encapsulated Pixel Data") {
		t.Fatalf("unexpected error message: %v", parseErr)
	}
}

func TestNextReturnsFFFEControlTokensForExplicitAndImplicitVR(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		stream []byte
		skip   int
		want   TokenKind
		tag    core.Tag
		length uint32
		dict   dictionary.DataDictionary
	}{
		{
			name:   "explicit start item",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
			}, nil),
			skip:   1,
			want:   TokenStartItem,
			tag:    core.TagItem,
			length: 0xFFFFFFFF,
			dict:   &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
		},
		{
			name:   "explicit end item",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:   2,
			want:   TokenEndItem,
			tag:    core.TagItemDelimitationItem,
			length: 0,
			dict:   &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
		},
		{
			name:   "explicit end sequence",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:   1,
			want:   TokenEndSequence,
			tag:    core.TagSequenceDelimitationItem,
			length: 0,
		},
		{
			name:   "implicit start item",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
			}, nil),
			skip:   1,
			want:   TokenStartItem,
			tag:    core.TagItem,
			length: 0xFFFFFFFF,
			dict:   &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
		},
		{
			name:   "implicit end item",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:   2,
			want:   TokenEndItem,
			tag:    core.TagItemDelimitationItem,
			length: 0,
			dict:   &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
		},
		{
			name:   "implicit end sequence",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:   1,
			want:   TokenEndSequence,
			tag:    core.TagSequenceDelimitationItem,
			length: 0,
			dict:   &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dict := tt.dict
			if dict == nil {
				dict = std.Dictionary
			}
			reader := NewReader(
				bytes.NewReader(tt.stream),
				tt.syntax,
				ReaderOptions{Dictionary: dict},
			)

			for i := 0; i < tt.skip; i++ {
				if _, err := reader.Next(); err != nil {
					t.Fatalf("preparing token stream: %v", err)
				}
			}

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tok.Kind != tt.want {
				t.Fatalf("token kind = %v, want %v", tok.Kind, tt.want)
			}
			if tok.Header.Tag != tt.tag {
				t.Fatalf("token tag = %s, want %s", tok.Header.Tag, tt.tag)
			}
			if tok.Header.Length != core.Length(tt.length) {
				t.Fatalf("token length = %s, want %d", tok.Header.Length, tt.length)
			}
			if tok.Header.VR != core.VRUN {
				t.Fatalf("token VR = %s, want %s", tok.Header.VR, core.VRUN)
			}
		})
	}
}

func TestNextAutoClosesDefinedLengthSequenceAndItem(t *testing.T) {
	inner := definedElementBytes(
		transfer.ExplicitVRLittleEndian,
		core.NewTag(0x0010, 0x0010),
		core.VRPN,
		4,
		[]byte("TEST"),
	)
	item := sequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner)))
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, uint32(len(item)+len(inner))),
		item,
		inner,
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	wantKinds := []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenEndSequence,
	}
	wantDepths := []int{1, 2, 2, 1, 0}

	for i, want := range wantKinds {
		tok, err := reader.Next()
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if tok.Kind != want {
			t.Fatalf("token %d kind = %v, want %v", i, tok.Kind, want)
		}
		if got := len(reader.seqDelimiters); got != wantDepths[i] {
			t.Fatalf("token %d stack depth = %d, want %d", i, got, wantDepths[i])
		}
	}

	if got := reader.Position(); got != int64(len(buf)) {
		t.Fatalf("reader position = %d, want %d", got, len(buf))
	}
	if reader.delimiterCheckPending != true {
		t.Fatalf("delimiterCheckPending = %v, want true before EOF boundary flush", reader.delimiterCheckPending)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after auto-closed sequence, got %v", err)
	}
	if got := len(reader.seqDelimiters); got != 0 {
		t.Fatalf("final stack depth = %d, want 0", got)
	}
}

func TestNextAutoClosesZeroLengthDefinedSequenceAndItem(t *testing.T) {
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 8),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	wantKinds := []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenEndItem,
		TokenEndSequence,
	}
	wantDepths := []int{1, 2, 1, 0}

	for i, want := range wantKinds {
		tok, err := reader.Next()
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if tok.Kind != want {
			t.Fatalf("token %d kind = %v, want %v", i, tok.Kind, want)
		}
		if got := len(reader.seqDelimiters); got != wantDepths[i] {
			t.Fatalf("token %d stack depth = %d, want %d", i, got, wantDepths[i])
		}
	}

	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after zero-length auto-close, got %v", err)
	}
}

func TestNextReadsEmptyUndefinedLengthSequence(t *testing.T) {
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenStartSequence {
		t.Fatalf("first token kind = %v, want %v", tok.Kind, TokenStartSequence)
	}
	if got := len(reader.seqDelimiters); got != 1 {
		t.Fatalf("stack depth after sequence start = %d, want 1", got)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenEndSequence {
		t.Fatalf("second token kind = %v, want %v", tok.Kind, TokenEndSequence)
	}
	if got := len(reader.seqDelimiters); got != 0 {
		t.Fatalf("stack depth after sequence end = %d, want 0", got)
	}

	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after empty undefined sequence, got %v", err)
	}
}

func TestNextReadsEmptyUndefinedLengthItem(t *testing.T) {
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	wantKinds := []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenEndItem,
		TokenEndSequence,
	}
	wantDepths := []int{1, 2, 1, 0}

	for i, want := range wantKinds {
		tok, err := reader.Next()
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if tok.Kind != want {
			t.Fatalf("token %d kind = %v, want %v", i, tok.Kind, want)
		}
		if got := len(reader.seqDelimiters); got != wantDepths[i] {
			t.Fatalf("token %d stack depth = %d, want %d", i, got, wantDepths[i])
		}
	}

	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after empty undefined item, got %v", err)
	}
}

func TestNextRejectsPrematureEOFInsideUndefinedLengthSequence(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenStartSequence {
		t.Fatalf("first token kind = %v, want %v", tok.Kind, TokenStartSequence)
	}
	if got := len(reader.seqDelimiters); got != 1 {
		t.Fatalf("stack depth after sequence start = %d, want 1", got)
	}

	_, err = reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected non-EOF error, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
	if got := len(reader.seqDelimiters); got != 1 {
		t.Fatalf("stack depth after premature EOF = %d, want 1", got)
	}
}

func TestNextRejectsPrematureEOFInsideUndefinedLengthItem(t *testing.T) {
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	wantKinds := []TokenKind{
		TokenStartSequence,
		TokenStartItem,
	}
	wantDepths := []int{1, 2}
	for i, want := range wantKinds {
		tok, err := reader.Next()
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if tok.Kind != want {
			t.Fatalf("token %d kind = %v, want %v", i, tok.Kind, want)
		}
		if got := len(reader.seqDelimiters); got != wantDepths[i] {
			t.Fatalf("token %d stack depth = %d, want %d", i, got, wantDepths[i])
		}
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected non-EOF error, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
	if got := len(reader.seqDelimiters); got != 2 {
		t.Fatalf("stack depth after premature EOF = %d, want 2", got)
	}
}

func TestReadDataSetRejectsMissingUndefinedItemDelimiter(t *testing.T) {
	stream := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "BROKEN^ITEM"), transfer.ExplicitVRLittleEndian),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.ReadDataSet()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected non-EOF error, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
}

func TestNextReadsDefinedLengthSequenceAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		dict   dictionary.DataDictionary
	}{
		{
			name:   "explicit vr",
			syntax: transfer.ExplicitVRLittleEndian,
			dict:   std.Dictionary,
		},
		{
			name:   "implicit vr",
			syntax: transfer.ImplicitVRLittleEndian,
			dict: &multiCountingDictionary{entries: map[core.Tag]core.VR{
				core.NewTag(0x0008, 0x1111): core.VRSQ,
				core.NewTag(0x0010, 0x0010): core.VRPN,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := definedElementBytes(tt.syntax, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("TEST"))
			item := dicomtest.SequenceControlBytes(tt.syntax.ByteOrder, core.TagItem, uint32(len(inner)))
			stream := bytes.Join([][]byte{
				dicomtest.SequenceHeaderBytes(tt.syntax, core.NewTag(0x0008, 0x1111), uint32(len(item)+len(inner))),
				item,
				inner,
			}, nil)

			reader := NewReader(bytes.NewReader(stream), tt.syntax, ReaderOptions{Dictionary: tt.dict})
			tokens, err := readAllTokens(reader)
			if err != nil {
				t.Fatal(err)
			}

			assertTokenKinds(t, tokens, []TokenKind{
				TokenStartSequence,
				TokenStartItem,
				TokenElement,
				TokenEndItem,
				TokenEndSequence,
			})
			if tokens[2].Element.Tag() != core.NewTag(0x0010, 0x0010) {
				t.Fatalf("inner element tag = %s, want %s", tokens[2].Element.Tag(), core.NewTag(0x0010, 0x0010))
			}
			if got := len(reader.seqDelimiters); got != 0 {
				t.Fatalf("final stack depth = %d, want 0", got)
			}
		})
	}
}

func TestNextAutoClosesDefinedLengthItemWithMultipleElements(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		dict   dictionary.DataDictionary
	}{
		{
			name:   "explicit vr",
			syntax: transfer.ExplicitVRLittleEndian,
			dict:   std.Dictionary,
		},
		{
			name:   "implicit vr",
			syntax: transfer.ImplicitVRLittleEndian,
			dict: &multiCountingDictionary{entries: map[core.Tag]core.VR{
				core.NewTag(0x0008, 0x1111): core.VRSQ,
				core.NewTag(0x0010, 0x0010): core.VRPN,
				core.NewTag(0x0010, 0x0020): core.VRLO,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elem1 := definedElementBytes(tt.syntax, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("ABCD"))
			elem2 := definedElementBytes(tt.syntax, core.NewTag(0x0010, 0x0020), core.VRLO, 4, []byte("ID01"))
			itemValue := append(append([]byte{}, elem1...), elem2...)
			stream := bytes.Join([][]byte{
				dicomtest.SequenceHeaderBytes(tt.syntax, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(tt.syntax.ByteOrder, core.TagItem, uint32(len(itemValue))),
				itemValue,
				dicomtest.SequenceControlBytes(tt.syntax.ByteOrder, core.TagSequenceDelimitationItem, 0),
			}, nil)

			reader := NewReader(bytes.NewReader(stream), tt.syntax, ReaderOptions{Dictionary: tt.dict})
			tokens, err := readAllTokens(reader)
			if err != nil {
				t.Fatal(err)
			}

			assertTokenKinds(t, tokens, []TokenKind{
				TokenStartSequence,
				TokenStartItem,
				TokenElement,
				TokenElement,
				TokenEndItem,
				TokenEndSequence,
			})
			if tokens[2].Element.Tag() != core.NewTag(0x0010, 0x0010) {
				t.Fatalf("first item element tag = %s, want %s", tokens[2].Element.Tag(), core.NewTag(0x0010, 0x0010))
			}
			if tokens[3].Element.Tag() != core.NewTag(0x0010, 0x0020) {
				t.Fatalf("second item element tag = %s, want %s", tokens[3].Element.Tag(), core.NewTag(0x0010, 0x0020))
			}
		})
	}
}

func TestNextReadsSequenceWithMultipleItems(t *testing.T) {
	elem1 := definedElementBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("ONE1"))
	elem2 := definedElementBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("TWO2"))
	elem3 := definedElementBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("TRE3"))
	items := [][]byte{
		bytes.Join([][]byte{dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(elem1))), elem1}, nil),
		bytes.Join([][]byte{dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(elem2))), elem2}, nil),
		bytes.Join([][]byte{dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(elem3))), elem3}, nil),
	}
	sequenceValue := bytes.Join(items, nil)
	stream := append(dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), uint32(len(sequenceValue))), sequenceValue...)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}

	assertTokenKinds(t, tokens, []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenEndSequence,
	})
	if got := len(reader.seqDelimiters); got != 0 {
		t.Fatalf("final stack depth = %d, want 0", got)
	}
}

func TestNextReadsNestedSequences(t *testing.T) {
	innerElem := definedElementBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("NEST"))
	innerItem := bytes.Join([][]byte{
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(innerElem))),
		innerElem,
	}, nil)
	innerSeq := append(dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x2222), uint32(len(innerItem))), innerItem...)
	stream := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		innerSeq,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}

	assertTokenKinds(t, tokens, []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenStartSequence,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenEndSequence,
		TokenEndItem,
		TokenEndSequence,
	})
	if got := len(reader.seqDelimiters); got != 0 {
		t.Fatalf("final stack depth = %d, want 0", got)
	}
}

func TestNextAllowsMaxSequenceDepthAtLimit(t *testing.T) {
	stream := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:       std.Dictionary,
		MaxSequenceDepth: 2,
	})
	if _, err := readAllTokens(reader); err != nil {
		t.Fatalf("expected nesting at limit to succeed, got %v", err)
	}
}

func TestNextZeroMaxSequenceDepthAllowsNestedStructures(t *testing.T) {
	stream := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x2222), 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x3333), 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 {
		t.Fatal("expected nested token stream")
	}
	if got := len(reader.seqDelimiters); got != 0 {
		t.Fatalf("final stack depth = %d, want 0", got)
	}
}

func TestNextContextValidationErrorsIncludeParseContext(t *testing.T) {
	tests := []struct {
		name       string
		stream     []byte
		syntax     transfer.Syntax
		skip       int
		dict       dictionary.DataDictionary
		wantErr    error
		wantTag    core.Tag
		wantOffset int64
		wantOp     Op
	}{
		{
			name:       "item delimiter at top level",
			stream:     sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			syntax:     transfer.ExplicitVRLittleEndian,
			wantErr:    ErrUnexpectedItemDelimiter,
			wantTag:    core.TagItemDelimitationItem,
			wantOffset: 8,
			wantOp:     OpReadTag,
		},
		{
			name: "sequence delimiter inside item context",
			stream: bytes.Join([][]byte{
				sequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			syntax:     transfer.ExplicitVRLittleEndian,
			skip:       2,
			wantErr:    ErrUnexpectedSequenceDelimiter,
			wantTag:    core.TagSequenceDelimitationItem,
			wantOffset: 28,
			wantOp:     OpReadTag,
		},
		{
			name: "sequence delimiter when defined-length sequence is open",
			stream: bytes.Join([][]byte{
				sequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 4),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			syntax:     transfer.ExplicitVRLittleEndian,
			skip:       1,
			wantErr:    ErrUnexpectedSequenceDelimiter,
			wantTag:    core.TagSequenceDelimitationItem,
			wantOffset: 20,
			wantOp:     OpReadTag,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dict := tt.dict
			if dict == nil {
				dict = std.Dictionary
			}
			reader := NewReader(bytes.NewReader(tt.stream), tt.syntax, ReaderOptions{Dictionary: dict})
			for i := 0; i < tt.skip; i++ {
				if _, err := reader.Next(); err != nil {
					t.Fatalf("preparing token stream: %v", err)
				}
			}

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != tt.wantOp {
				t.Fatalf("parse error op = %s, want %s", parseErr.Op, tt.wantOp)
			}
			if parseErr.Tag != tt.wantTag {
				t.Fatalf("parse error tag = %s, want %s", parseErr.Tag, tt.wantTag)
			}
			if parseErr.Offset != tt.wantOffset {
				t.Fatalf("parse error offset = %d, want %d", parseErr.Offset, tt.wantOffset)
			}
		})
	}
}

func TestNextImplicitVRSequenceUsesDictionaryOnlyForDataElements(t *testing.T) {
	dict := &multiCountingDictionary{entries: map[core.Tag]core.VR{
		core.NewTag(0x0008, 0x1111): core.VRSQ,
		core.NewTag(0x0010, 0x0010): core.VRPN,
	}}
	inner := definedElementBytes(transfer.ImplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("TEST"))
	stream := bytes.Join([][]byte{
		sequenceHeaderBytes(transfer.ImplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner))),
		inner,
		sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: dict})
	tokens, err := readAllTokens(reader)
	if err != nil {
		t.Fatal(err)
	}

	assertTokenKinds(t, tokens, []TokenKind{
		TokenStartSequence,
		TokenStartItem,
		TokenElement,
		TokenEndItem,
		TokenEndSequence,
	})
	if dict.byTagCalls != 2 {
		t.Fatalf("dictionary call count = %d, want 2 (SQ header + inner element only)", dict.byTagCalls)
	}
}

func TestNextRejectsUnexpectedDelimitersByContext(t *testing.T) {
	tests := []struct {
		name    string
		syntax  transfer.Syntax
		stream  []byte
		skip    int
		dict    dictionary.DataDictionary
		wantErr error
	}{
		{
			name:   "item delimiter while sequence is open",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:    1,
			wantErr: ErrUnexpectedItemDelimiter,
		},
		{
			name:   "sequence delimiter while item is open",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:    2,
			wantErr: ErrUnexpectedSequenceDelimiter,
		},
		{
			name:   "item delimiter for defined-length item",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 4),
				sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:    2,
			wantErr: ErrUnexpectedItemDelimiter,
		},
		{
			name:   "sequence delimiter for defined-length sequence",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 4),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:    1,
			wantErr: ErrUnexpectedSequenceDelimiter,
		},
		{
			name:   "implicit item delimiter for defined-length item",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				sequenceControlBytes(binary.LittleEndian, core.TagItem, 4),
				sequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:    2,
			dict:    &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
			wantErr: ErrUnexpectedItemDelimiter,
		},
		{
			name:   "implicit sequence delimiter for defined-length sequence",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 4),
				sequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:    1,
			dict:    &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
			wantErr: ErrUnexpectedSequenceDelimiter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dict := tt.dict
			if dict == nil {
				dict = std.Dictionary
			}
			reader := NewReader(bytes.NewReader(tt.stream), tt.syntax, ReaderOptions{Dictionary: dict})
			for i := 0; i < tt.skip; i++ {
				if _, err := reader.Next(); err != nil {
					t.Fatalf("preparing token stream: %v", err)
				}
			}

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestNextRejectsItemOutsideSequenceContext(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF)),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnexpectedSequenceControlTag) {
		t.Fatalf("expected unexpected-control-tag error, got %v", err)
	}
}

func TestNextRejectsMaxSequenceDepth(t *testing.T) {
	nestedSeq := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x2222), core.VRSQ, [2]byte{}, 0xFFFFFFFF)
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		sequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		nestedSeq,
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:       std.Dictionary,
		MaxSequenceDepth: 2,
	})

	for i := 0; i < 2; i++ {
		if _, err := reader.Next(); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected max-depth error, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckDepth {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
}

func TestNextRejectsUnknownSequenceControlTag(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(sequenceControlBytes(binary.LittleEndian, core.NewTag(0xFFFE, 0x0000), 0)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnexpectedSequenceControlTag) {
		t.Fatalf("expected unexpected-control-tag error, got %v", err)
	}
}

func TestNextRejectsNonZeroDelimiterLength(t *testing.T) {
	tests := []struct {
		name string
		tag  core.Tag
	}{
		{name: "item delimitation", tag: core.TagItemDelimitationItem},
		{name: "sequence delimitation", tag: core.TagSequenceDelimitationItem},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(
				bytes.NewReader(sequenceControlBytes(binary.LittleEndian, tt.tag, 4)),
				transfer.ExplicitVRLittleEndian,
				ReaderOptions{Dictionary: std.Dictionary},
			)

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrUnexpectedDelimiterLength) {
				t.Fatalf("expected delimiter-length error, got %v", err)
			}
		})
	}
}

func TestReadHeaderImplicitVRSkipsDictionaryForSequenceControlTags(t *testing.T) {
	dict := &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0010, 0x0010), core.VRPN)}
	reader := NewReader(
		bytes.NewReader(sequenceControlBytes(binary.LittleEndian, core.TagItem, 0)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: dict},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.Tag != core.TagItem {
		t.Fatalf("unexpected tag: %s", header.Tag)
	}
	if header.VR != core.VRUN {
		t.Fatalf("sequence control VR = %s, want %s", header.VR, core.VRUN)
	}
	if dict.byTagCalls != 0 {
		t.Fatalf("dictionary was consulted for sequence control tag: %d calls", dict.byTagCalls)
	}
}

func TestReadHeaderImplicitVRSpecialCasesPixelDataAndOverlayData(t *testing.T) {
	tests := []struct {
		name string
		tag  core.Tag
	}{
		{name: "pixel data", tag: core.NewTag(0x7FE0, 0x0010)},
		{name: "overlay data", tag: core.NewTag(0x6000, 0x3000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(
				bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, tt.tag, 8)),
				transfer.ImplicitVRLittleEndian,
				ReaderOptions{Dictionary: &countingDictionary{}},
			)

			header, err := reader.readHeader()
			if err != nil {
				t.Fatal(err)
			}
			if header.VR != core.VROW {
				t.Fatalf("implicit VR for %s = %s, want %s", tt.tag, header.VR, core.VROW)
			}
		})
	}
}

func TestReadHeaderImplicitVRDoesNotTreatOddOverlayGroupAsOverlayData(t *testing.T) {
	dict := &countingDictionary{entry: dictionaryEntry(core.NewTag(0x6001, 0x3000), core.VRUN)}
	reader := NewReader(
		bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x6001, 0x3000), 8)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: dict},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.VR != core.VRUN {
		t.Fatalf("implicit VR for odd overlay-like tag = %s, want %s", header.VR, core.VRUN)
	}
	if dict.byTagCalls != 1 {
		t.Fatalf("dictionary call count = %d, want 1", dict.byTagCalls)
	}
}

func TestReadHeaderImplicitVRUsesDictionaryForNormalElements(t *testing.T) {
	dict := &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0010, 0x0010), core.VRPN)}
	reader := NewReader(
		bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0010, 0x0010), 4)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: dict},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.VR != core.VRPN {
		t.Fatalf("header VR = %s, want %s", header.VR, core.VRPN)
	}
	if dict.byTagCalls != 1 {
		t.Fatalf("dictionary call count = %d, want 1", dict.byTagCalls)
	}
}

type countingDictionary struct {
	entry      dictEntry
	byTagCalls int
}

type multiCountingDictionary struct {
	entries    map[core.Tag]core.VR
	byTagCalls int
}

type dictEntry struct {
	tag core.Tag
	vr  core.VR
}

func dictionaryEntry(tag core.Tag, vr core.VR) dictEntry {
	return dictEntry{tag: tag, vr: vr}
}

func (d *countingDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if d == nil {
		return dictionary.Entry{}, false
	}
	d.byTagCalls++
	if d.entry.tag != tag {
		return dictionary.Entry{}, false
	}
	return dictionary.Entry{Tag: d.entry.tag, VR: d.entry.vr}, true
}

func (d *countingDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func (d *multiCountingDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if d == nil {
		return dictionary.Entry{}, false
	}
	d.byTagCalls++
	vr, ok := d.entries[tag]
	if !ok {
		return dictionary.Entry{}, false
	}
	return dictionary.Entry{Tag: tag, VR: vr}, true
}

func (d *multiCountingDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func explicitLongHeaderBytes(order binary.ByteOrder, tag core.Tag, vr core.VR, reserved [2]byte, length uint32) []byte {
	if reserved == [2]byte{} {
		return dicomtest.ExplicitLongHeaderBytes(order, tag, vr, length)
	}
	var buf bytes.Buffer
	writeTag(&buf, order, tag)
	buf.WriteString(vr.String())
	buf.Write(reserved[:])
	writeUint32(&buf, order, length)
	return buf.Bytes()
}

func implicitHeaderBytes(order binary.ByteOrder, tag core.Tag, length uint32) []byte {
	var buf bytes.Buffer
	writeTag(&buf, order, tag)
	writeUint32(&buf, order, length)
	return buf.Bytes()
}

func sequenceControlBytes(order binary.ByteOrder, tag core.Tag, length uint32) []byte {
	return dicomtest.SequenceControlBytes(order, tag, length)
}

func sequenceHeaderBytes(syntax transfer.Syntax, tag core.Tag, length uint32) []byte {
	return dicomtest.SequenceHeaderBytes(syntax, tag, length)
}

type integrationElement struct {
	tag        core.Tag
	vr         core.VR
	value      []byte
	wantString string
}

func encapsulatedPixelDataBytes(offsetTable []byte, fragments ...[]byte) []byte {
	return dicomtest.EncodeElement(
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, offsetTable, fragments...),
		transfer.JPEGBaseline,
	)
}

func readAllTokens(reader *Reader) ([]Token, error) {
	var tokens []Token
	for {
		tok, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return tokens, nil
		}
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
}

func assertTokenKinds(t *testing.T, tokens []Token, want []TokenKind) {
	t.Helper()
	if len(tokens) != len(want) {
		t.Fatalf("token count = %d, want %d", len(tokens), len(want))
	}
	for i, wantKind := range want {
		if tokens[i].Kind != wantKind {
			t.Fatalf("token %d kind = %v, want %v", i, tokens[i].Kind, wantKind)
		}
	}
}

func testRejectOddLengthAcrossTransferSyntaxes(t *testing.T, name string, opts ReaderOptions) {
	t.Helper()

	tests := []struct {
		name   string
		syntax transfer.Syntax
	}{
		{name: "explicit little endian", syntax: transfer.ExplicitVRLittleEndian},
		{name: "implicit little endian", syntax: transfer.ImplicitVRLittleEndian},
		{name: "explicit big endian", syntax: transfer.ExplicitVRBigEndian},
	}

	for _, tt := range tests {
		t.Run(name+"/"+tt.name, func(t *testing.T) {
			assertOddLengthReject(t, tt.syntax, opts, core.NewTag(0x0010, 0x0010), core.VRPN)
			if tt.syntax.ExplicitVR {
				assertOddLengthReject(t, tt.syntax, opts, core.NewTag(0x0010, 0x0010), core.VROB)
			}
		})
	}
}

func assertOddLengthReject(t *testing.T, syntax transfer.Syntax, opts ReaderOptions, tag core.Tag, vr core.VR) {
	t.Helper()

	buf := definedElementBytes(syntax, tag, vr, 3, []byte("ODD"))
	reader := NewReader(bytes.NewReader(buf), syntax, opts)

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrOddElementLength) {
		t.Fatalf("expected odd-length error, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	wantOffset := definedHeaderLength(syntax, vr)
	if parseErr.Offset != wantOffset {
		t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, wantOffset)
	}
	if parseErr.Tag != tag {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if parseErr.VR != vr {
		t.Fatalf("unexpected VR: %s", parseErr.VR)
	}
	if parseErr.Length != 3 {
		t.Fatalf("unexpected length: %s", parseErr.Length)
	}
	if got := reader.Position(); got != wantOffset {
		t.Fatalf("reader position after odd-length error = %d, want %d", got, wantOffset)
	}
}

func definedElementBytes(syntax transfer.Syntax, tag core.Tag, vr core.VR, length uint32, value []byte) []byte {
	var buf bytes.Buffer
	writeTag(&buf, syntax.ByteOrder, tag)

	if syntax.ExplicitVR {
		buf.WriteString(vr.String())
		if vr.UsesLongExplicitLength() {
			buf.Write([]byte{0x00, 0x00})
			writeUint32(&buf, syntax.ByteOrder, length)
		} else {
			if length > 0xFFFF {
				panic("definedElementBytes: short explicit VR length exceeds uint16")
			}
			writeUint16(&buf, syntax.ByteOrder, uint16(length))
		}
	} else {
		writeUint32(&buf, syntax.ByteOrder, length)
	}

	buf.Write(value)
	return buf.Bytes()
}

func definedHeaderLength(syntax transfer.Syntax, vr core.VR) int64 {
	if !syntax.ExplicitVR {
		return 8
	}
	if vr.UsesLongExplicitLength() {
		return 12
	}
	return 8
}

func writeTag(buf *bytes.Buffer, order binary.ByteOrder, tag core.Tag) {
	writeUint16(buf, order, tag.Group)
	writeUint16(buf, order, tag.Element)
}

func writeUint16(buf *bytes.Buffer, order binary.ByteOrder, value uint16) {
	var raw [2]byte
	order.PutUint16(raw[:], value)
	buf.Write(raw[:])
}

func writeUint32(buf *bytes.Buffer, order binary.ByteOrder, value uint32) {
	var raw [4]byte
	order.PutUint32(raw[:], value)
	buf.Write(raw[:])
}
