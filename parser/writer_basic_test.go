package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
	"math"
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
			name:   "explicit_le_sv_long_header",
			syntax: transfer.ExplicitVRLittleEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0018, 0x9901), VR: core.VRSV},
				Value:  core.RawValue([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
			},
			wantHeader:   []byte{0x18, 0x00, 0x01, 0x99, 'S', 'V', 0x00, 0x00, 0x08, 0x00, 0x00, 0x00},
			wantValueLen: 8,
		},
		{
			name:   "explicit_be_uv_long_header",
			syntax: transfer.ExplicitVRBigEndian,
			elem: core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0018, 0x9902), VR: core.VRUV},
				Value:  core.RawValue([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
			},
			wantHeader:   []byte{0x00, 0x18, 0x99, 0x02, 'U', 'V', 0x00, 0x00, 0x00, 0x00, 0x00, 0x08},
			wantValueLen: 8,
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

func TestWriterIntLengthRejectsLengthsAbovePlatformInt(t *testing.T) {
	_, err := intLengthWithMax(core.Length(math.MaxInt32)+1, math.MaxInt32)
	if err == nil {
		t.Fatal("intLengthWithMax() error = nil, want overflow")
	}
	if !errors.Is(err, dicomenc.ErrLengthOverflow) {
		t.Fatalf("intLengthWithMax() error = %v, want ErrLengthOverflow", err)
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

func TestEncodeStringValueComputesLengthAndPadding(t *testing.T) {
	tests := []struct {
		name       string
		vr         core.VR
		value      core.StringValue
		wantBytes  []byte
		wantLength core.Length
	}{
		{
			name:       "empty",
			vr:         core.VRLO,
			value:      core.StringValue{},
			wantBytes:  []byte{},
			wantLength: 0,
		},
		{
			name:       "even single value",
			vr:         core.VRPN,
			value:      core.StringValue{"AB"},
			wantBytes:  []byte("AB"),
			wantLength: 2,
		},
		{
			name:       "multi value space padded",
			vr:         core.VRPN,
			value:      core.StringValue{"ONE", "TWO"},
			wantBytes:  []byte("ONE\\TWO "),
			wantLength: 8,
		},
		{
			name:       "ui nul padded",
			vr:         core.VRUI,
			value:      core.StringValue{"1.2.3"},
			wantBytes:  []byte("1.2.3\x00"),
			wantLength: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBytes, gotLength, err := encodeStringValue(tt.vr, tt.value)
			if err != nil {
				t.Fatalf("encodeStringValue() error = %v", err)
			}
			if gotLength != tt.wantLength {
				t.Fatalf("length = %d, want %d", gotLength, tt.wantLength)
			}
			if !bytes.Equal(gotBytes, tt.wantBytes) {
				t.Fatalf("bytes = % X, want % X", gotBytes, tt.wantBytes)
			}
		})
	}
}

func TestWriterEncodesStringValuesWithDerivedLength(t *testing.T) {
	tests := []struct {
		name         string
		characterSet string
		value        string
		want         []byte
	}{
		{name: "default character set", value: "DOE^J", want: []byte("DOE^J ")},
		{name: "configured Latin-1", characterSet: "ISO_IR 100", value: "José^Silva", want: []byte("Jos\xe9^Silva")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := defaultWriterOptions()
			if tt.characterSet != "" {
				characterSet, err := dicomenc.ParseCharacterSet(tt.characterSet)
				if err != nil {
					t.Fatalf("ParseCharacterSet() error = %v", err)
				}
				opts.CharacterSet = characterSet
			}
			var got bytes.Buffer
			element := core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{tt.value},
			}
			if err := NewWriterWithOptions(&got, transfer.ExplicitVRLittleEndian, opts).WriteElement(element); err != nil {
				t.Fatalf("WriteElement() error = %v", err)
			}
			if gotLength := binary.LittleEndian.Uint16(got.Bytes()[6:8]); gotLength != uint16(len(tt.want)) {
				t.Fatalf("encoded length = %d, want %d", gotLength, len(tt.want))
			}
			if !bytes.Equal(got.Bytes()[8:], tt.want) {
				t.Fatalf("encoded PN = % X, want % X", got.Bytes()[8:], tt.want)
			}
		})
	}
}

func TestEncodeTypedNumericValuesUsesTransferSyntaxByteOrder(t *testing.T) {
	tests := []struct {
		name   string
		vr     core.VR
		value  core.Value
		wantLE []byte
		wantBE []byte
	}{
		{"US", core.VRUS, core.Uint16Value{0x0102, 0x0304}, []byte{0x02, 0x01, 0x04, 0x03}, []byte{0x01, 0x02, 0x03, 0x04}},
		{"SS", core.VRSS, core.Int16Value{-2, 0x0102}, []byte{0xfe, 0xff, 0x02, 0x01}, []byte{0xff, 0xfe, 0x01, 0x02}},
		{"UL", core.VRUL, core.Uint32Value{0x01020304}, []byte{0x04, 0x03, 0x02, 0x01}, []byte{0x01, 0x02, 0x03, 0x04}},
		{"SL", core.VRSL, core.Int32Value{-2}, []byte{0xfe, 0xff, 0xff, 0xff}, []byte{0xff, 0xff, 0xff, 0xfe}},
		{"UV", core.VRUV, core.Uint64Value{0x0102030405060708}, []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}},
		{"SV", core.VRSV, core.Int64Value{-2}, []byte{0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}},
		{"FL", core.VRFL, core.Float32Value{1.5}, []byte{0x00, 0x00, 0xc0, 0x3f}, []byte{0x3f, 0xc0, 0x00, 0x00}},
		{"FD", core.VRFD, core.Float64Value{1.5}, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf8, 0x3f}, []byte{0x3f, 0xf8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"AT", core.VRAT, core.TagValue{core.NewTag(0x0010, 0x0020)}, []byte{0x10, 0x00, 0x20, 0x00}, []byte{0x00, 0x10, 0x00, 0x20}},
	}
	for _, tt := range tests {
		for _, syntaxCase := range []struct {
			name   string
			syntax transfer.Syntax
			want   []byte
		}{
			{"little", transfer.ExplicitVRLittleEndian, tt.wantLE},
			{"big", transfer.ExplicitVRBigEndian, tt.wantBE},
		} {
			t.Run(tt.name+"/"+syntaxCase.name, func(t *testing.T) {
				writer := NewWriter(&bytes.Buffer{}, syntaxCase.syntax)
				element := core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0001), VR: tt.vr}, Value: tt.value}
				if err := writer.validateElement(element); err != nil {
					t.Fatalf("validateElement: %v", err)
				}
				got, length, err := writer.encodeValue(element)
				if err != nil {
					t.Fatalf("encodeValue: %v", err)
				}
				if !bytes.Equal(got, syntaxCase.want) || length != core.Length(len(syntaxCase.want)) {
					t.Fatalf("encodeValue() = % x length %d, want % x length %d", got, length, syntaxCase.want, len(syntaxCase.want))
				}
			})
		}
	}
}

func TestWriterTypedNumericVRCompatibility(t *testing.T) {
	valid := []core.Element{
		{Header: core.ElementHeader{VR: core.VROL}, Value: core.Uint32Value{1}},
		{Header: core.ElementHeader{VR: core.VROV}, Value: core.Uint64Value{1}},
		{Header: core.ElementHeader{VR: core.VROF}, Value: core.Float32Value{1}},
		{Header: core.ElementHeader{VR: core.VROD}, Value: core.Float64Value{1}},
	}
	writer := NewWriter(&bytes.Buffer{}, transfer.ExplicitVRLittleEndian)
	for _, element := range valid {
		if err := writer.validateElement(element); err != nil {
			t.Errorf("validateElement(%T, %s): %v", element.Value, element.VR(), err)
		}
	}

	invalid := core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0002), VR: core.VRSS}, Value: core.Uint16Value{1}}
	err := writer.WriteElement(invalid)
	if err == nil || !strings.Contains(err.Error(), "incompatible with VR SS") {
		t.Fatalf("WriteElement() error = %v, want incompatible typed value/VR error", err)
	}
}

func TestWriterSerializesTypedNumericElementWithDerivedLength(t *testing.T) {
	element := core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS},
		Value:  core.Uint16Value{0x0102, 0x0304},
	}
	var got bytes.Buffer
	if err := NewWriter(&got, transfer.ExplicitVRLittleEndian).WriteElement(element); err != nil {
		t.Fatalf("WriteElement: %v", err)
	}
	want := []byte{
		0x28, 0x00, 0x10, 0x00, 'U', 'S', 0x04, 0x00,
		0x02, 0x01, 0x04, 0x03,
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("WriteElement() = % x, want % x", got.Bytes(), want)
	}
}

func BenchmarkEncodeStringValueManyComponents(b *testing.B) {
	value := repeatedStringValue(256, "ABCDE")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := encodeStringValue(core.VRLO, value); err != nil {
			b.Fatal(err)
		}
	}
}

func repeatedStringValue(count int, value string) core.StringValue {
	out := make(core.StringValue, count)
	for i := range out {
		out[i] = value
	}
	return out
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

func TestPadRawValueToEvenLengthOnlyCopiesOddInput(t *testing.T) {
	tests := []struct {
		name        string
		vr          core.VR
		input       []byte
		wantAlias   bool
		wantPadding byte
	}{
		{name: "even reuses storage", vr: core.VROB, input: []byte{1, 2}, wantAlias: true},
		{name: "odd copies and pads with space", vr: core.VRPN, input: []byte{1, 2, 3}, wantPadding: ' '},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := padRawValueToEvenLength(test.vr, test.input)
			result[0] = 9
			if aliased := test.input[0] == 9; aliased != test.wantAlias {
				t.Fatalf("result aliases input = %v, want %v", aliased, test.wantAlias)
			}
			if test.wantPadding != 0 && result[len(result)-1] != test.wantPadding {
				t.Fatalf("padding byte = %#x, want %#x", result[len(result)-1], test.wantPadding)
			}
		})
	}
}
