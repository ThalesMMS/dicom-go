package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
	"strings"
	"testing"
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
