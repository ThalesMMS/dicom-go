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

func TestWriterHeaderFormatsByteForByte(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		syntax       transfer.Syntax
		elem         core.Element
		wantHeader   []byte
		wantValueLen int
	}{
		{
			name:   "explicit_le_short_header",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{"AB"},
			},
			wantHeader:   []byte{0x10, 0x00, 0x10, 0x00, 'P', 'N', 0x02, 0x00},
			wantValueLen: 2,
		},
		{
			name:   "explicit_le_long_header",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0002, 0x0001), VR: core.VROB},
				Value:  core.RawValue([]byte{0x01, 0x02}),
			},
			wantHeader:   []byte{0x02, 0x00, 0x01, 0x00, 'O', 'B', 0x00, 0x00, 0x02, 0x00, 0x00, 0x00},
			wantValueLen: 2,
		},
		{
			name:   "explicit_be_short_header",
			syntax: transfer.ExplicitVRBigEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS},
				Value:  core.RawValue([]byte{0x01, 0x02}),
			},
			wantHeader:   []byte{0x00, 0x28, 0x00, 0x10, 'U', 'S', 0x00, 0x02},
			wantValueLen: 2,
		},
		{
			name:   "explicit_be_long_header",
			syntax: transfer.ExplicitVRBigEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010), VR: core.VRUN},
				Value:  core.RawValue([]byte("AB")),
			},
			wantHeader:   []byte{0x77, 0x77, 0x00, 0x10, 'U', 'N', 0x00, 0x00, 0x00, 0x00, 0x00, 0x02},
			wantValueLen: 2,
		},
		{
			name:   "explicit_le_empty_vr_normalizes_to_un_long_header",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010)},
				Value:  core.RawValue([]byte("AB")),
			},
			wantHeader:   []byte{0x77, 0x77, 0x10, 0x00, 'U', 'N', 0x00, 0x00, 0x02, 0x00, 0x00, 0x00},
			wantValueLen: 2,
		},
		{
			name:   "implicit_le_header_without_vr",
			syntax: transfer.ImplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{"AB"},
			},
			wantHeader:   []byte{0x10, 0x00, 0x10, 0x00, 0x02, 0x00, 0x00, 0x00},
			wantValueLen: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := writeElementBytes(t, tt.syntax, tt.elem)
			if len(got) != len(tt.wantHeader)+tt.wantValueLen {
				t.Fatalf("encoded length = %d, want %d", len(got), len(tt.wantHeader)+tt.wantValueLen)
			}
			if !bytes.Equal(got[:len(tt.wantHeader)], tt.wantHeader) {
				t.Fatalf("header bytes = % X, want % X", got[:len(tt.wantHeader)], tt.wantHeader)
			}
		})
	}
}

func TestWriterValueEncodingByteForByte(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		syntax transfer.Syntax
		elem   core.Element
		want   []byte
	}{
		{
			name:   "pn_space_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{"DOE^J"},
			},
			want: []byte{0x10, 0x00, 0x10, 0x00, 'P', 'N', 0x06, 0x00, 'D', 'O', 'E', '^', 'J', 0x20},
		},
		{
			name:   "ui_nul_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI},
				Value:  core.StringValue{"1.2.3"},
			},
			want: []byte{0x08, 0x00, 0x16, 0x00, 'U', 'I', 0x06, 0x00, '1', '.', '2', '.', '3', 0x00},
		},
		{
			name:   "us_little_endian_value",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS},
				Value:  core.RawValue(u16Bytes(binary.LittleEndian, 0x0102)),
			},
			want: []byte{0x28, 0x00, 0x10, 0x00, 'U', 'S', 0x02, 0x00, 0x02, 0x01},
		},
		{
			name:   "ul_little_endian_value",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0002, 0x0000), VR: core.VRUL},
				Value:  core.RawValue(u32Bytes(binary.LittleEndian, 0x01020304)),
			},
			want: []byte{0x02, 0x00, 0x00, 0x00, 'U', 'L', 0x04, 0x00, 0x04, 0x03, 0x02, 0x01},
		},
		{
			name:   "ob_nul_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0002, 0x0001), VR: core.VROB},
				Value:  core.RawValue([]byte{0x01, 0x02, 0x03}),
			},
			want: []byte{0x02, 0x00, 0x01, 0x00, 'O', 'B', 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x00},
		},
		{
			name:   "un_nul_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010), VR: core.VRUN},
				Value:  core.RawValue([]byte("ABC")),
			},
			want: []byte{0x77, 0x77, 0x10, 0x00, 'U', 'N', 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 'A', 'B', 'C', 0x00},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := writeElementBytes(t, tt.syntax, tt.elem)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("encoded bytes = % X, want % X", got, tt.want)
			}
		})
	}
}

func TestWriterRoundTripDefinedElementsAcrossTransferSyntaxes(t *testing.T) {
	t.Parallel()

	syntaxes := []struct {
		name   string
		syntax transfer.Syntax
	}{
		{name: "explicit_le", syntax: transfer.ExplicitVRLittleEndian},
		{name: "explicit_be", syntax: transfer.ExplicitVRBigEndian},
		{name: "implicit_le", syntax: transfer.ImplicitVRLittleEndian},
	}

	for _, syntaxCase := range syntaxes {
		syntaxCase := syntaxCase
		t.Run(syntaxCase.name, func(t *testing.T) {
			t.Parallel()

			tests := []struct {
				name string
				elem core.Element
			}{
				{
					name: "pn",
					elem: core.Element{
						Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
						Value:  core.RawValue([]byte("DOE^J ")),
					},
				},
				{
					name: "ui",
					elem: core.Element{
						Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI},
						Value:  core.RawValue([]byte("1.2.3\x00")),
					},
				},
				{
					name: "us",
					elem: core.Element{
						Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS},
						Value:  core.RawValue(u16Bytes(syntaxCase.syntax.ByteOrder, 0x0102)),
					},
				},
				{
					name: "ul",
					elem: core.Element{
						Header: core.ElementHeader{Tag: core.NewTag(0x0002, 0x0000), VR: core.VRUL},
						Value:  core.RawValue(u32Bytes(syntaxCase.syntax.ByteOrder, 0x01020304)),
					},
				},
				{
					name: "ob",
					elem: core.Element{
						Header: core.ElementHeader{Tag: core.NewTag(0x0002, 0x0001), VR: core.VROB},
						Value:  core.RawValue([]byte{0x01, 0x02, 0x03, 0x00}),
					},
				},
				{
					name: "un",
					elem: core.Element{
						Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010), VR: core.VRUN},
						Value:  core.RawValue([]byte("ABC\x00")),
					},
				},
			}

			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					got := roundTripElement(t, syntaxCase.syntax, tt.elem)
					assertRoundTripMatch(t, got, tt.elem)
				})
			}
		})
	}
}

func TestWriterRoundTripMultiValueString(t *testing.T) {
	t.Parallel()

	syntaxes := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.ImplicitVRLittleEndian,
	}

	for _, syntax := range syntaxes {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()

			elem := core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{"ONE", "TWO"},
			}

			got := roundTripElement(t, syntax, elem)
			wantRaw := []byte("ONE\\TWO ")

			if got.Tag() != elem.Tag() {
				t.Fatalf("tag = %s, want %s", got.Tag(), elem.Tag())
			}
			if got.VR() != elem.VR() {
				t.Fatalf("VR = %s, want %s", got.VR(), elem.VR())
			}
			raw, ok := got.RawBytes()
			if !ok {
				t.Fatalf("round-trip value type = %T, want raw bytes", got.Value)
			}
			if !bytes.Equal(raw, wantRaw) {
				t.Fatalf("raw bytes = % X, want % X", raw, wantRaw)
			}

			values := got.StringValues()
			if len(values) != 2 || values[0] != "ONE" || values[1] != "TWO" {
				t.Fatalf("StringValues() = %v, want [ONE TWO]", values)
			}
		})
	}
}

func TestWriterSequenceUndefinedLengthEncodingByteForByte(t *testing.T) {
	t.Parallel()

	elem := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
		Value: core.SequenceValue{
			Items: []core.DataSet{
				{
					Elements: []core.Element{
						{
							Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
							Value:  core.StringValue{"DOE^J"},
						},
					},
				},
			},
		},
	}

	got := writeElementBytes(t, transfer.ExplicitVRLittleEndian, elem)
	want := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, elem.Tag(), uint32(core.UndefinedLength)),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(core.UndefinedLength)),
		dicomtest.ExplicitElement(core.Element{
			Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
			Value:  core.StringValue{"DOE^J"},
		}),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes = % X, want % X", got, want)
	}
}

func TestWriterEmptySequenceStillWritesSequenceDelimiter(t *testing.T) {
	t.Parallel()

	elem := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
		Value:  core.SequenceValue{},
	}

	got := writeElementBytes(t, transfer.ExplicitVRLittleEndian, elem)
	want := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, elem.Tag(), uint32(core.UndefinedLength)),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)

	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes = % X, want % X", got, want)
	}
}

func TestWriterSequencePreserveLengthPolicyEncodesDefinedLengths(t *testing.T) {
	t.Parallel()

	elem := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
		Value: core.SequenceValue{
			Items: []core.DataSet{
				{
					Elements: []core.Element{
						dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^J"),
						dicomtest.NewUIElement(core.NewTag(0x0008, 0x0018), "1.2.3"),
					},
				},
			},
		},
	}

	got := writeElementBytesWithOptions(t, transfer.ExplicitVRLittleEndian, elem, WriterOptions{
		LengthPolicy: LengthPolicyPreserve,
	})
	reader := NewReader(bytes.NewReader(got), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() error = %v", err)
	}
	if tok.Kind != TokenStartSequence {
		t.Fatalf("token kind = %s, want %s", tok.Kind, TokenStartSequence)
	}
	if tok.Header.Length.IsUndefined() {
		t.Fatalf("sequence length = %s, want defined length", tok.Header.Length)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() second error = %v", err)
	}
	if tok.Kind != TokenStartItem {
		t.Fatalf("second token kind = %s, want %s", tok.Kind, TokenStartItem)
	}
	if tok.Header.Length.IsUndefined() {
		t.Fatalf("item length = %s, want defined length", tok.Header.Length)
	}

	for {
		tok, err = reader.Next()
		if err != nil {
			t.Fatalf("Reader.Next() tail error = %v", err)
		}
		if tok.Kind == TokenEndSequence {
			break
		}
		if tok.Kind == TokenEndItem {
			continue
		}
	}
}

func TestWriterSequencePreserveLengthPolicyMultipleItemsKeepsDefinedLengths(t *testing.T) {
	t.Parallel()

	itemOne := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^ONE"),
			dicomtest.NewUIElement(core.NewTag(0x0008, 0x0018), "1.2.3"),
		},
	}
	itemTwo := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^TWO"),
		},
	}
	elem := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{itemOne, itemTwo}},
	}

	itemOneBytes := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, itemOne.Elements...)
	itemTwoBytes := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, itemTwo.Elements...)
	wantSequenceLength := core.Length(8 + len(itemOneBytes) + 8 + len(itemTwoBytes))

	got := writeElementBytesWithOptions(t, transfer.ExplicitVRLittleEndian, elem, WriterOptions{
		LengthPolicy: LengthPolicyPreserve,
	})
	reader := NewReader(bytes.NewReader(got), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() sequence error = %v", err)
	}
	if tok.Kind != TokenStartSequence {
		t.Fatalf("token kind = %s, want %s", tok.Kind, TokenStartSequence)
	}
	if tok.Header.Length != wantSequenceLength {
		t.Fatalf("sequence length = %s, want %s", tok.Header.Length, wantSequenceLength)
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() first item error = %v", err)
	}
	if tok.Kind != TokenStartItem {
		t.Fatalf("first item token kind = %s, want %s", tok.Kind, TokenStartItem)
	}
	if tok.Header.Length != core.Length(len(itemOneBytes)) {
		t.Fatalf("first item length = %s, want %d", tok.Header.Length, len(itemOneBytes))
	}

	for {
		tok, err = reader.Next()
		if err != nil {
			t.Fatalf("Reader.Next() after first item error = %v", err)
		}
		if tok.Kind == TokenEndItem {
			break
		}
	}

	tok, err = reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() second item error = %v", err)
	}
	if tok.Kind != TokenStartItem {
		t.Fatalf("second item token kind = %s, want %s", tok.Kind, TokenStartItem)
	}
	if tok.Header.Length != core.Length(len(itemTwoBytes)) {
		t.Fatalf("second item length = %s, want %d", tok.Header.Length, len(itemTwoBytes))
	}

	for {
		tok, err = reader.Next()
		if err != nil {
			t.Fatalf("Reader.Next() tail error = %v", err)
		}
		if tok.Kind == TokenEndSequence {
			break
		}
	}
}

func TestWriterSequencePreserveLengthPolicyNormalizesUndefinedItemsInDefinedSequence(t *testing.T) {
	t.Parallel()

	// Verifies that a defined-length sequence with undefined-length items is
	// normalized correctly during write. The Go model preserves the outer
	// sequence as defined-length, but item header
	// lengths are normalized because core.SequenceValue does not retain item
	// length metadata after parsing. This fixture uses a consistent explicit
	// sequence length for the same structural pattern so the scaffold parser can
	// materialize it before the writer round-trip.
	source := bytes.Join([][]byte{
		dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0018, 0x6011), 62),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(core.UndefinedLength)),
		dicomtest.EncodeElements(
			transfer.ExplicitVRLittleEndian,
			dicomtest.Uint16Element(core.NewTag(0x0018, 0x6012), core.VRUS, binary.LittleEndian, 1),
			dicomtest.Uint16Element(core.NewTag(0x0018, 0x6014), core.VRUS, binary.LittleEndian, 2),
		),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(core.UndefinedLength)),
		dicomtest.EncodeElements(
			transfer.ExplicitVRLittleEndian,
			dicomtest.Uint16Element(core.NewTag(0x0018, 0x6012), core.VRUS, binary.LittleEndian, 4),
		),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
	}, nil)

	reader := NewReader(bytes.NewReader(source), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	parsed, err := reader.ReadDataSet()
	if err != nil {
		t.Fatalf("ReadDataSet() error = %v", err)
	}

	got := roundTripDataSet(t, transfer.ExplicitVRLittleEndian, parsed, WriterOptions{
		LengthPolicy: LengthPolicyPreserve,
	}, std.Dictionary)
	want := parsed
	want.Elements = append([]core.Element(nil), parsed.Elements...)
	want.Elements[0].Header.Length = 46
	if diff := dicomtest.DiffDataSet(got, want); diff != "" {
		t.Fatalf("round-trip dataset mismatch: %s", diff)
	}

	encoded := writeElementBytesWithOptions(t, transfer.ExplicitVRLittleEndian, parsed.Elements[0], WriterOptions{
		LengthPolicy: LengthPolicyPreserve,
	})
	tokenReader := NewReader(bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	tok, err := tokenReader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() sequence error = %v", err)
	}
	if tok.Kind != TokenStartSequence {
		t.Fatalf("token kind = %s, want %s", tok.Kind, TokenStartSequence)
	}
	if tok.Header.Length != 46 {
		t.Fatalf("sequence length = %s, want 46", tok.Header.Length)
	}

	tok, err = tokenReader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() first item error = %v", err)
	}
	if tok.Kind != TokenStartItem {
		t.Fatalf("first item token kind = %s, want %s", tok.Kind, TokenStartItem)
	}
	if tok.Header.Length != 20 {
		t.Fatalf("first item length = %s, want 20", tok.Header.Length)
	}

	for {
		tok, err = tokenReader.Next()
		if err != nil {
			t.Fatalf("Reader.Next() after first item error = %v", err)
		}
		if tok.Kind == TokenEndItem {
			break
		}
	}

	tok, err = tokenReader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() second item error = %v", err)
	}
	if tok.Kind != TokenStartItem {
		t.Fatalf("second item token kind = %s, want %s", tok.Kind, TokenStartItem)
	}
	if tok.Header.Length != 10 {
		t.Fatalf("second item length = %s, want 10", tok.Header.Length)
	}
}

func TestWriterFragmentSequenceEncodingByteForByte(t *testing.T) {
	t.Parallel()

	elem := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0x01, 0x02, 0x03},
		[]byte{0x04, 0x05},
	)

	got := writeElementBytes(t, transfer.ExplicitVRLittleEndian, elem)
	want := dicomtest.ExplicitElement(elem)

	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes = % X, want % X", got, want)
	}
}

func TestWriterFragmentSequenceUsesOBHeaderForExplicitSyntax(t *testing.T) {
	t.Parallel()

	elem := core.Element{
		Header: core.ElementHeader{
			Tag:       core.TagPixelData,
			VR:        core.VROW,
			Length:    core.UndefinedLength,
			LengthSet: true,
		},
		Value: core.FragmentSequence{
			OffsetTable: nil,
			Fragments:   [][]byte{{0x01, 0x02, 0x03}},
		},
	}

	got := writeElementBytes(t, transfer.ExplicitVRLittleEndian, elem)
	wantHeader := []byte{0xE0, 0x7F, 0x10, 0x00, 'O', 'B', 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF}
	if !bytes.Equal(got[:len(wantHeader)], wantHeader) {
		t.Fatalf("pixel data header = % X, want % X", got[:len(wantHeader)], wantHeader)
	}
	if bytes.Contains(got, []byte{0xFE, 0xFF, 0x0D, 0xE0}) {
		t.Fatalf("fragment sequence contains unexpected item delimiter: % X", got)
	}
}

func TestWriterRejectsBasicOffsetTableWithNonU32Length(t *testing.T) {
	t.Parallel()

	elem := core.Element{
		Header: core.ElementHeader{
			Tag:       core.TagPixelData,
			VR:        core.VROB,
			Length:    core.UndefinedLength,
			LengthSet: true,
		},
		Value: core.FragmentSequence{
			OffsetTable: []byte{0x00, 0x00, 0x00},
			Fragments:   [][]byte{{0x01, 0x02}},
		},
	}

	writer := NewWriter(&bytes.Buffer{}, transfer.ExplicitVRLittleEndian)
	err := writer.WriteElement(elem)
	if err == nil {
		t.Fatal("WriteElement() error = nil, want error")
	}

	var writeErr *WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("WriteElement() error type = %T, want *WriteError", err)
	}
	if writeErr.Tag != core.TagPixelData {
		t.Fatalf("WriteError.Tag = %s, want %s", writeErr.Tag, core.TagPixelData)
	}
	if !strings.Contains(err.Error(), "multiple of 4") {
		t.Fatalf("error message %q missing BOT validation detail", err.Error())
	}
}

func TestWriterRoundTripNestedSequencesAcrossTransferSyntaxes(t *testing.T) {
	t.Parallel()

	syntaxes := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.ImplicitVRLittleEndian,
	}

	want := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewSequenceElement(
				core.NewTag(0x0008, 0x1111),
				core.DataSet{
					Elements: []core.Element{
						dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^JOHN"),
						dicomtest.NewSequenceElement(
							core.NewTag(0x0008, 0x1140),
							core.DataSet{
								Elements: []core.Element{
									dicomtest.NewUIElement(core.NewTag(0x0008, 0x1155), "1.2.3.4"),
								},
							},
						),
					},
				},
				core.DataSet{
					Elements: []core.Element{
						dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^JANE"),
					},
				},
			),
		},
	}
	for _, syntax := range syntaxes {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()

			dict := &multiCountingDictionary{
				entries: map[core.Tag]core.VR{
					core.NewTag(0x0008, 0x1111): core.VRSQ,
					core.NewTag(0x0008, 0x1140): core.VRSQ,
					core.NewTag(0x0008, 0x1155): core.VRUI,
					core.NewTag(0x0010, 0x0010): core.VRPN,
				},
			}
			got := roundTripDataSet(t, syntax, want, defaultWriterOptions(), dict)
			if diff := dicomtest.DiffDataSet(got, want); diff != "" {
				t.Fatalf("round-trip dataset mismatch: %s", diff)
			}
		})
	}
}

func TestWriterRoundTripFragmentSequenceAcrossTransferSyntaxes(t *testing.T) {
	t.Parallel()

	syntaxes := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.ImplicitVRLittleEndian,
	}

	baseWant := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00},
				[]byte{0x01, 0x02, 0x03, 0x04},
				[]byte{0x05, 0x06, 0x07, 0x00},
			),
		},
	}

	for _, syntax := range syntaxes {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()

			want := baseWant
			got := roundTripDataSet(t, syntax, want, defaultWriterOptions(), std.Dictionary)
			if syntax == transfer.ImplicitVRLittleEndian {
				want = core.DataSet{
					Elements: []core.Element{
						{
							Header: core.ElementHeader{
								Tag:       core.TagPixelData,
								VR:        core.VROW,
								Length:    core.UndefinedLength,
								LengthSet: true,
							},
							Value: want.Elements[0].Value,
						},
					},
				}
			}
			if diff := dicomtest.DiffDataSet(got, want); diff != "" {
				t.Fatalf("round-trip dataset mismatch: %s", diff)
			}
		})
	}
}

func TestWriterRoundTripSimpleSequencesAcrossTransferSyntaxes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want core.DataSet
	}{
		{
			name: "single_item",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewSequenceElement(
						core.NewTag(0x0008, 0x1111),
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^JOHN"),
								dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "PAT-001"),
							},
						},
					),
				},
			},
		},
		{
			name: "multiple_items",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewSequenceElement(
						core.NewTag(0x0008, 0x1111),
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^JOHN"),
							},
						},
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^JANE"),
								dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "PAT-002"),
							},
						},
					),
				},
			},
		},
		{
			name: "empty_sequence",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewSequenceElement(core.NewTag(0x0008, 0x1111)),
				},
			},
		},
	}

	for _, syntax := range roundTripTransferSyntaxes() {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()

			dict := sequenceRoundTripDictionary()
			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					got := roundTripDataSet(t, syntax, tt.want, defaultWriterOptions(), dict)
					if diff := dicomtest.DiffDataSet(got, tt.want); diff != "" {
						t.Fatalf("round-trip dataset mismatch: %s", diff)
					}

					seq, ok := got.Elements[0].Value.(core.SequenceValue)
					if !ok {
						t.Fatalf("sequence value type = %T, want core.SequenceValue", got.Elements[0].Value)
					}
					if len(seq.Items) != len(tt.want.Elements[0].Value.(core.SequenceValue).Items) {
						t.Fatalf("item count = %d, want %d", len(seq.Items), len(tt.want.Elements[0].Value.(core.SequenceValue).Items))
					}
				})
			}
		})
	}
}

func TestWriterRoundTripNestedAndMixedDatasetsAcrossTransferSyntaxes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want core.DataSet
	}{
		{
			name: "two_level_nested_sequence",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewSequenceElement(
						core.NewTag(0x0008, 0x1111),
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewSequenceElement(
									core.NewTag(0x0008, 0x1140),
									core.DataSet{
										Elements: []core.Element{
											dicomtest.NewUIElement(core.NewTag(0x0008, 0x1155), "1.2.3.4"),
										},
									},
								),
							},
						},
					),
				},
			},
		},
		{
			name: "mixed_content",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^MIXED"),
					dicomtest.NewSequenceElement(
						core.NewTag(0x0008, 0x1111),
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewUIElement(core.NewTag(0x0008, 0x1155), "1.2.3"),
							},
						},
					),
					dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "AFTER-SEQ"),
				},
			},
		},
	}

	for _, syntax := range roundTripTransferSyntaxes() {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()

			dict := sequenceRoundTripDictionary()
			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					got := roundTripDataSet(t, syntax, tt.want, defaultWriterOptions(), dict)
					if diff := dicomtest.DiffDataSet(got, tt.want); diff != "" {
						t.Fatalf("round-trip dataset mismatch: %s", diff)
					}
				})
			}
		})
	}
}

func TestWriterRoundTripFragmentSequencesAcrossTransferSyntaxes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want core.DataSet
	}{
		{
			name: "empty_basic_offset_table",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewFragmentSequenceElement(
						core.TagPixelData,
						nil,
						[]byte{0x01, 0x02, 0x03, 0x04},
					),
				},
			},
		},
		{
			name: "populated_basic_offset_table",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewFragmentSequenceElement(
						core.TagPixelData,
						[]byte{0x00, 0x00, 0x00, 0x00, 0x0C, 0x00, 0x00, 0x00},
						[]byte{0x10, 0x11, 0x12, 0x13},
						[]byte{0x20, 0x21, 0x22, 0x23},
					),
				},
			},
		},
		{
			name: "multiple_fragments_with_padding",
			want: core.DataSet{
				Elements: []core.Element{
					dicomtest.NewFragmentSequenceElement(
						core.TagPixelData,
						[]byte{0x00, 0x00, 0x00, 0x00},
						[]byte{0x01, 0x02},
						[]byte{0x03, 0x04, 0x05, 0x00},
						[]byte{0x06, 0x07, 0x08, 0x09},
					),
				},
			},
		},
	}

	for _, syntax := range roundTripTransferSyntaxes() {
		syntax := syntax
		t.Run(syntax.Name, func(t *testing.T) {
			t.Parallel()

			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					got := roundTripDataSet(t, syntax, tt.want, defaultWriterOptions(), std.Dictionary)
					want := fragmentExpectedForSyntax(tt.want, syntax)
					if diff := dicomtest.DiffDataSet(got, want); diff != "" {
						t.Fatalf("round-trip dataset mismatch: %s", diff)
					}

					gotValue := got.Elements[0].Value.(core.FragmentSequence)
					wantValue := want.Elements[0].Value.(core.FragmentSequence)
					if !bytes.Equal(gotValue.OffsetTable, wantValue.OffsetTable) {
						t.Fatalf("offset table = % X, want % X", gotValue.OffsetTable, wantValue.OffsetTable)
					}
					if len(gotValue.Fragments) != len(wantValue.Fragments) {
						t.Fatalf("fragment count = %d, want %d", len(gotValue.Fragments), len(wantValue.Fragments))
					}
					for i := range wantValue.Fragments {
						if !bytes.Equal(gotValue.Fragments[i], wantValue.Fragments[i]) {
							t.Fatalf("fragment[%d] = % X, want % X", i, gotValue.Fragments[i], wantValue.Fragments[i])
						}
					}
				})
			}
		})
	}
}

func TestWriterRejectsInvalidStructuredValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		elem        core.Element
		wantMessage string
	}{
		{
			name: "sequence_vr_with_raw_value",
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
				Value:  core.RawValue([]byte{0x00, 0x00}),
			},
			wantMessage: "sequence VR requires core.SequenceValue",
		},
		{
			name: "sequence_value_with_non_sequence_vr",
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRUN},
				Value:  core.SequenceValue{},
			},
			wantMessage: "core.SequenceValue requires SQ VR",
		},
		{
			name: "fragment_sequence_with_non_pixel_tag",
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1010), VR: core.VROB},
				Value:  core.FragmentSequence{},
			},
			wantMessage: "only supported for Pixel Data",
		},
		{
			name: "item_tag",
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.TagItem, VR: core.VRUN},
				Value:  core.RawValue([]byte{0x00, 0x00}),
			},
			wantMessage: "items and delimiters cannot be written as standalone elements",
		},
		{
			name: "sequence_delimitation_tag",
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.TagSequenceDelimitationItem, VR: core.VRUN},
			},
			wantMessage: "items and delimiters cannot be written as standalone elements",
		},
		{
			name: "sequence_delimitation_tag_with_sq_vr_still_rejected",
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.TagSequenceDelimitationItem, VR: core.VRSQ},
				Value:  core.SequenceValue{},
			},
			wantMessage: "items and delimiters cannot be written as standalone elements",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			writer := NewWriter(&bytes.Buffer{}, transfer.ExplicitVRLittleEndian)
			err := writer.WriteElement(tt.elem)
			if err == nil {
				t.Fatal("WriteElement() error = nil, want error")
			}

			var writeErr *WriteError
			if !errors.As(err, &writeErr) {
				t.Fatalf("WriteElement() error type = %T, want *WriteError", err)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("error message %q missing %q", err.Error(), tt.wantMessage)
			}
		})
	}
}

func TestWriterWriteErrorUnwrapsUnderlyingIOError(t *testing.T) {
	t.Parallel()

	elem := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
		Value:  core.StringValue{"DOE^J"},
	}
	wantErr := &boomIOError{message: "boom"}
	writer := NewWriter(&failAfterWriter{
		limit: 9,
		err:   wantErr,
	}, transfer.ExplicitVRLittleEndian)

	err := writer.WriteElement(elem)
	if err == nil {
		t.Fatal("WriteElement() error = nil, want wrapped error")
	}

	var writeErr *WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("WriteElement() error type = %T, want *WriteError", err)
	}
	if writeErr.Op != OpWriteValue {
		t.Fatalf("WriteError.Op = %s, want %s", writeErr.Op, OpWriteValue)
	}
	if writeErr.Tag != elem.Tag() {
		t.Fatalf("WriteError.Tag = %s, want %s", writeErr.Tag, elem.Tag())
	}
	if writeErr.VR != elem.VR() {
		t.Fatalf("WriteError.VR = %s, want %s", writeErr.VR, elem.VR())
	}

	var gotIOErr *boomIOError
	if !errors.As(err, &gotIOErr) {
		t.Fatalf("WriteElement() error = %v, want wrapped *boomIOError", err)
	}
	if gotIOErr != wantErr {
		t.Fatalf("unwrapped error = %p, want %p", gotIOErr, wantErr)
	}
	if !strings.Contains(err.Error(), elem.Tag().String()) {
		t.Fatalf("error message %q missing tag %s", err.Error(), elem.Tag())
	}
	if !strings.Contains(err.Error(), elem.VR().String()) {
		t.Fatalf("error message %q missing VR %s", err.Error(), elem.VR())
	}
}

func TestWriterPadsOddLengthRawValueByVR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		elem   core.Element
		syntax transfer.Syntax
		want   []byte
	}{
		{
			name:   "pn_space_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.RawValue([]byte("ABC")),
			},
			want: []byte("ABC "),
		},
		{
			name:   "ui_nul_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI},
				Value:  core.RawValue([]byte("ABC")),
			},
			want: []byte("ABC\x00"),
		},
		{
			name:   "ob_nul_padding",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0002, 0x0001), VR: core.VROB},
				Value:  core.RawValue([]byte{0x01, 0x02, 0x03}),
			},
			want: []byte{0x01, 0x02, 0x03, 0x00},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := roundTripElement(t, tt.syntax, tt.elem)
			raw, ok := got.RawBytes()
			if !ok {
				t.Fatalf("round-trip value type = %T, want raw bytes", got.Value)
			}
			if !bytes.Equal(raw, tt.want) {
				t.Fatalf("raw bytes after padding = % X, want % X", raw, tt.want)
			}
		})
	}
}

func writeElementBytes(t *testing.T, syntax transfer.Syntax, elem core.Element) []byte {
	return writeElementBytesWithOptions(t, syntax, elem, defaultWriterOptions())
}

func writeElementBytesWithOptions(t *testing.T, syntax transfer.Syntax, elem core.Element, opts WriterOptions) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriterWithOptions(&buf, syntax, opts)
	if err := writer.WriteElement(elem); err != nil {
		t.Fatalf("WriteElement() error = %v", err)
	}
	return buf.Bytes()
}

func roundTripElement(t *testing.T, syntax transfer.Syntax, elem core.Element) core.Element {
	t.Helper()

	data := writeElementBytes(t, syntax, elem)
	reader := NewReader(bytes.NewReader(data), syntax, ReaderOptions{Dictionary: std.Dictionary})
	tok, err := reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() error = %v", err)
	}
	if tok.Kind != TokenElement {
		t.Fatalf("token kind = %s, want %s", tok.Kind, TokenElement)
	}
	_, err = reader.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Reader.Next() error = %v, want EOF", err)
	}
	return tok.Element
}

func roundTripDataSet(t *testing.T, syntax transfer.Syntax, dataSet core.DataSet, opts WriterOptions, dict dictionary.DataDictionary) core.DataSet {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriterWithOptions(&buf, syntax, opts)
	for _, elem := range dataSet.Elements {
		if err := writer.WriteElement(elem); err != nil {
			t.Fatalf("WriteElement(%s) error = %v", elem.Tag(), err)
		}
	}

	reader := NewReader(bytes.NewReader(buf.Bytes()), syntax, ReaderOptions{Dictionary: dict})
	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatalf("ReadDataSet() error = %v", err)
	}
	return got
}

func roundTripTransferSyntaxes() []transfer.Syntax {
	return []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.ImplicitVRLittleEndian,
	}
}

func sequenceRoundTripDictionary() dictionary.DataDictionary {
	return &multiCountingDictionary{
		entries: map[core.Tag]core.VR{
			core.NewTag(0x0008, 0x1111): core.VRSQ,
			core.NewTag(0x0008, 0x1140): core.VRSQ,
			core.NewTag(0x0008, 0x1155): core.VRUI,
			core.NewTag(0x0010, 0x0010): core.VRPN,
			core.NewTag(0x0010, 0x0020): core.VRLO,
		},
	}
}

func fragmentExpectedForSyntax(base core.DataSet, syntax transfer.Syntax) core.DataSet {
	if syntax != transfer.ImplicitVRLittleEndian {
		return base
	}
	return core.DataSet{
		Elements: []core.Element{
			{
				Header: core.ElementHeader{
					Tag:       core.TagPixelData,
					VR:        core.VROW,
					Length:    core.UndefinedLength,
					LengthSet: true,
				},
				Value: base.Elements[0].Value,
			},
		},
	}
}

func assertRoundTripMatch(t *testing.T, got, want core.Element) {
	t.Helper()

	if got.Tag() != want.Tag() {
		t.Fatalf("tag = %s, want %s", got.Tag(), want.Tag())
	}
	if got.VR() != want.VR() {
		t.Fatalf("VR = %s, want %s", got.VR(), want.VR())
	}

	gotRaw, gotOK := got.RawBytes()
	wantRaw, wantOK := want.RawBytes()
	if gotOK != wantOK {
		t.Fatalf("raw-bytes flag = %v, want %v", gotOK, wantOK)
	}
	if !bytes.Equal(gotRaw, wantRaw) {
		t.Fatalf("raw bytes = % X, want % X", gotRaw, wantRaw)
	}
}

func u16Bytes(order binary.ByteOrder, value uint16) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	buf := make([]byte, 2)
	order.PutUint16(buf, value)
	return buf
}

func u32Bytes(order binary.ByteOrder, value uint32) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	buf := make([]byte, 4)
	order.PutUint32(buf, value)
	return buf
}

type boomIOError struct {
	message string
}

func (e *boomIOError) Error() string {
	return e.message
}

type failAfterWriter struct {
	limit int
	wrote int
	err   error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.wrote >= w.limit {
		return 0, w.err
	}

	remaining := w.limit - w.wrote
	if len(p) > remaining {
		w.wrote += remaining
		if w.err != nil {
			return remaining, w.err
		}
		return remaining, io.ErrShortWrite
	}
	w.wrote += len(p)
	return len(p), nil
}
