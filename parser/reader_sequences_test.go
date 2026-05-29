package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"testing"
)

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
	firstItemOffset := int64(len(dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, seqTag, 0xFFFFFFFF)))
	secondItemOffset := firstItemOffset + 8 + int64(len(itemOne))
	if !seqValue.Items[0].ItemOffsetSet || seqValue.Items[0].ItemOffset != firstItemOffset {
		t.Fatalf("first item offset = %d set=%v, want %d/true", seqValue.Items[0].ItemOffset, seqValue.Items[0].ItemOffsetSet, firstItemOffset)
	}
	if !seqValue.Items[1].ItemOffsetSet || seqValue.Items[1].ItemOffset != secondItemOffset {
		t.Fatalf("second item offset = %d set=%v, want %d/true", seqValue.Items[1].ItemOffset, seqValue.Items[1].ItemOffsetSet, secondItemOffset)
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
func TestNextReturnsSequenceAndItemControlTokensWithoutReadingBodies(t *testing.T) {
	inner := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		inner,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
	if tok.Offset != 12 {
		t.Fatalf("item token offset = %d, want 12", tok.Offset)
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
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
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
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
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
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
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
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
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
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
	item := dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner)))
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
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0),
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
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
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
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
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
func TestNextImplicitVRSequenceUsesDictionaryOnlyForDataElements(t *testing.T) {
	dict := &multiCountingDictionary{entries: map[core.Tag]core.VR{
		core.NewTag(0x0008, 0x1111): core.VRSQ,
		core.NewTag(0x0010, 0x0010): core.VRPN,
	}}
	inner := definedElementBytes(transfer.ImplicitVRLittleEndian, core.NewTag(0x0010, 0x0010), core.VRPN, 4, []byte("TEST"))
	stream := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ImplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner))),
		inner,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
	if calls := dict.byTagCalls.Load(); calls != 2 {
		t.Fatalf("dictionary call count = %d, want 2 (SQ header + inner element only)", calls)
	}
}
