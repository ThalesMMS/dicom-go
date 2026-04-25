package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"strings"
	"testing"
)

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
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
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
