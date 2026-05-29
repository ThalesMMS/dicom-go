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

func TestLargeDefinedLengthValueIsNotMaterializedAndReaderAdvances(t *testing.T) {
	largeTag := core.NewTag(0x7FE1, 0x0010) // arbitrary non-sequence tag
	patientNameTag := core.NewTag(0x0010, 0x0010)

	large := bytes.Repeat([]byte{0xAB}, 32)
	stream := bytes.Join([][]byte{
		dicomtest.ExplicitLongHeaderBytes(binary.LittleEndian, largeTag, core.VROB, uint32(len(large))),
		large,
		dicomtest.EncodeElement(dicomtest.NewPNElement(patientNameTag, "AFTER"), transfer.ExplicitVRLittleEndian),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:                std.Dictionary,
		InlineValueBytesThreshold: 8,
	})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != TokenElement {
		t.Fatalf("first token kind = %v, want %v", tok.Kind, TokenElement)
	}
	if tok.Element.Tag() != largeTag {
		t.Fatalf("first tag = %s, want %s", tok.Element.Tag(), largeTag)
	}
	if tok.Element.Value != nil {
		t.Fatalf("large element value = %T, want nil", tok.Element.Value)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != patientNameTag {
		t.Fatalf("second tag = %s, want %s", tok.Element.Tag(), patientNameTag)
	}
	if got := tok.Element.StringValue(); got != "AFTER" {
		t.Fatalf("patient name = %q, want %q", got, "AFTER")
	}
}

func TestUTValueIsMaterializedWhenInlineThresholdSet(t *testing.T) {
	tag := core.NewTag(0x0040, 0xA160)
	nextTag := core.NewTag(0x7FE1, 0x0010)
	value := []byte("REPORT")
	stream := bytes.Join([][]byte{
		dicomtest.ExplicitLongHeaderBytes(binary.LittleEndian, tag, core.VRUT, uint32(len(value))),
		value,
		dicomtest.EncodeElement(dicomtest.NewOBElement(nextTag, []byte{0x01, 0x02, 0x03, 0x04}), transfer.ExplicitVRLittleEndian),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:                std.Dictionary,
		InlineValueBytesThreshold: 4,
	})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != tag {
		t.Fatalf("first tag = %s, want %s", tok.Element.Tag(), tag)
	}
	raw, ok := tok.Element.RawBytes()
	if !ok {
		t.Fatalf("UT value = %T, want raw bytes", tok.Element.Value)
	}
	if !bytes.Equal(raw, value) {
		t.Fatalf("UT raw value = % X, want % X", raw, value)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != nextTag {
		t.Fatalf("second tag = %s, want %s", tok.Element.Tag(), nextTag)
	}
}

func TestExplicitSVAndUVUseLongHeaderAndReaderStaysAligned(t *testing.T) {
	patientNameTag := core.NewTag(0x0010, 0x0010)
	value := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	tests := []struct {
		name string
		tag  core.Tag
		vr   core.VR
	}{
		{name: "SV", tag: core.NewTag(0x0018, 0x9901), vr: core.VRSV},
		{name: "UV", tag: core.NewTag(0x0018, 0x9902), vr: core.VRUV},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, tt.tag, tt.vr, [2]byte{}, uint32(len(value))),
				value,
				dicomtest.EncodeElement(dicomtest.NewPNElement(patientNameTag, "AFTER"), transfer.ExplicitVRLittleEndian),
			}, nil)
			reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tok.Element.Tag() != tt.tag || tok.Element.VR() != tt.vr {
				t.Fatalf("first element = %s %s, want %s %s", tok.Element.Tag(), tok.Element.VR(), tt.tag, tt.vr)
			}
			raw, ok := tok.Element.RawBytes()
			if !ok || !bytes.Equal(raw, value) {
				t.Fatalf("%s raw value = % X ok=%v, want % X", tt.vr, raw, ok, value)
			}

			tok, err = reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tok.Element.Tag() != patientNameTag {
				t.Fatalf("second tag = %s, want %s", tok.Element.Tag(), patientNameTag)
			}
			if got := tok.Element.StringValue(); got != "AFTER" {
				t.Fatalf("patient name = %q, want AFTER", got)
			}
		})
	}
}

func TestNativePixelDataDefinedLengthNotMaterializedAndReaderAdvances(t *testing.T) {
	pixelDataTag := core.TagPixelData
	patientNameTag := core.NewTag(0x0010, 0x0010)

	pixel := bytes.Repeat([]byte{0x11}, 64)
	stream := bytes.Join([][]byte{
		dicomtest.ExplicitLongHeaderBytes(binary.LittleEndian, pixelDataTag, core.VROB, uint32(len(pixel))),
		pixel,
		dicomtest.EncodeElement(dicomtest.NewPNElement(patientNameTag, "AFTER"), transfer.ExplicitVRLittleEndian),
	}, nil)

	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:                std.Dictionary,
		InlineValueBytesThreshold: 8,
	})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != pixelDataTag {
		t.Fatalf("first tag = %s, want %s", tok.Element.Tag(), pixelDataTag)
	}
	if tok.Element.Value != nil {
		t.Fatalf("pixel data value = %T, want nil", tok.Element.Value)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Tag() != patientNameTag {
		t.Fatalf("second tag = %s, want %s", tok.Element.Tag(), patientNameTag)
	}
	if got := tok.Element.StringValue(); got != "AFTER" {
		t.Fatalf("patient name = %q, want %q", got, "AFTER")
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
func TestImplicitVRPrivateUndefinedLengthSequence(t *testing.T) {
	privateDataTag := core.NewTag(0x0011, 0x1001)
	patientNameTag := core.NewTag(0x0010, 0x0010)
	inner := definedElementBytes(transfer.ImplicitVRLittleEndian, patientNameTag, core.VRPN, 10, []byte("INNER^TEST"))
	tests := []struct {
		name     string
		stream   []byte
		options  ReaderOptions
		validate func(*testing.T, core.DataSet, error)
	}{
		{
			name: "parses as sequence",
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, privateDataTag, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner))),
				inner,
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			options: ReaderOptions{Dictionary: std.Dictionary},
			validate: func(t *testing.T, dataset core.DataSet, err error) {
				if err != nil {
					t.Fatal(err)
				}
				outer := onlyElement(t, dataset)
				if outer.Tag() != privateDataTag || outer.VR() != core.VRUN {
					t.Fatalf("private sequence = %s %s, want %s UN", outer.Tag(), outer.VR(), privateDataTag)
				}
				sequence, ok := outer.Value.(core.SequenceValue)
				if !ok || len(sequence.Items) != 1 || len(sequence.Items[0].Elements) != 1 {
					t.Fatalf("private sequence value = %#v, want one item/element", outer.Value)
				}
				if got := sequence.Items[0].Elements[0].StringValue(); got != "INNER^TEST" {
					t.Fatalf("private nested value = %q, want INNER^TEST", got)
				}
			},
		},
		{
			name: "rejects malformed sequence body",
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, privateDataTag, 0xFFFFFFFF),
				definedElementBytes(transfer.ImplicitVRLittleEndian, patientNameTag, core.VRPN, 10, []byte("AFTER^TEST")),
			}, nil),
			options: ReaderOptions{Dictionary: std.Dictionary},
			validate: func(t *testing.T, _ core.DataSet, err error) {
				if err == nil {
					t.Fatal("ReadDataSet() error = nil, want malformed sequence body error")
				}
				var parseErr *ParseError
				if !errors.As(err, &parseErr) || parseErr.Op != OpReadValue || parseErr.Tag != patientNameTag {
					t.Fatalf("ReadDataSet() error = %v, want OpReadValue ParseError for %s", err, patientNameTag)
				}
			},
		},
		{
			name: "honors sequence depth limit",
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, privateDataTag, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			options: ReaderOptions{Dictionary: std.Dictionary, MaxSequenceDepth: 1},
			validate: func(t *testing.T, _ core.DataSet, err error) {
				if !errors.Is(err, ErrMaxDepthExceeded) {
					t.Fatalf("ReadDataSet() error = %v, want ErrMaxDepthExceeded", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(bytes.NewReader(tt.stream), transfer.ImplicitVRLittleEndian, tt.options)
			dataset, err := reader.ReadDataSet()
			tt.validate(t, dataset, err)
		})
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
	if dict.byTagCalls.Load() == 0 {
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
func TestCopyElementValueToUsesSeekOriginSeparateFromBaseOffset(t *testing.T) {
	const baseOffset = int64(132)
	tag := core.NewTag(0x7FE0, 0x0010)
	want := []byte{0x01, 0x02, 0x03, 0x04}
	data := dicomtest.EncodeElement(dicomtest.NewOBElement(tag, want), transfer.ExplicitVRLittleEndian)
	source := append(bytes.Repeat([]byte{0x00}, int(baseOffset)), data...)
	section := io.NewSectionReader(bytes.NewReader(source), baseOffset, int64(len(data)))
	reader := NewReader(section, transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:                std.Dictionary,
		BaseOffset:                baseOffset,
		InlineValueBytesThreshold: 1,
	})

	tok, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if tok.Element.Value != nil {
		t.Fatalf("expected deferred value, got %T", tok.Element.Value)
	}
	if pos := reader.Position(); pos != baseOffset+int64(len(data)) {
		t.Fatalf("Position() after deferred read = %d, want %d", pos, baseOffset+int64(len(data)))
	}

	var got bytes.Buffer
	n, err := reader.CopyElementValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyElementValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
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
