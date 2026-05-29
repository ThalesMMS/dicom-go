package parser

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadDataSetUndefinedLengthBoundaries(t *testing.T) {
	patientNameTag := core.NewTag(0x0010, 0x0010)
	seqTag := core.NewTag(0x0008, 0x1111)
	unTag := core.NewTag(0x7777, 0x0010)

	inner := dicomtest.EncodeElement(
		dicomtest.NewPNElement(patientNameTag, "ITEM^ONE"),
		transfer.ExplicitVRLittleEndian,
	)

	tests := []struct {
		name  string
		input []byte
		check func(*testing.T, core.DataSet)
	}{
		{
			name: "undefined length SQ is structured",
			input: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, seqTag, core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner))),
				inner,
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			check: func(t *testing.T, dataSet core.DataSet) {
				t.Helper()
				elem := onlyElement(t, dataSet)
				if elem.Tag() != seqTag || elem.VR() != core.VRSQ {
					t.Fatalf("sequence element = %s %s, want %s %s", elem.Tag(), elem.VR(), seqTag, core.VRSQ)
				}
				value, ok := elem.Value.(core.SequenceValue)
				if !ok {
					t.Fatalf("sequence value = %T, want core.SequenceValue", elem.Value)
				}
				if len(value.Items) != 1 || len(value.Items[0].Elements) != 1 {
					t.Fatalf("sequence items = %#v, want one item with one element", value.Items)
				}
			},
		},
		{
			name:  "undefined length encapsulated Pixel Data is structured",
			input: encapsulatedPixelDataBytes([]byte{0, 0, 0, 0}, []byte{1, 2, 3, 4}),
			check: func(t *testing.T, dataSet core.DataSet) {
				t.Helper()
				elem := onlyElement(t, dataSet)
				if elem.Tag() != core.TagPixelData {
					t.Fatalf("element tag = %s, want Pixel Data", elem.Tag())
				}
				value, ok := elem.Value.(core.FragmentSequence)
				if !ok {
					t.Fatalf("Pixel Data value = %T, want core.FragmentSequence", elem.Value)
				}
				if len(value.Fragments) != 1 {
					t.Fatalf("fragment count = %d, want 1", len(value.Fragments))
				}
			},
		},
		{
			name: "defined length UN is raw",
			input: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, unTag, core.VRUN, [2]byte{}, 4),
				[]byte("DATA"),
			}, nil),
			check: func(t *testing.T, dataSet core.DataSet) {
				t.Helper()
				elem := onlyElement(t, dataSet)
				if elem.Tag() != unTag || elem.VR() != core.VRUN {
					t.Fatalf("UN element = %s %s, want %s %s", elem.Tag(), elem.VR(), unTag, core.VRUN)
				}
				raw, ok := elem.RawBytes()
				if !ok || !bytes.Equal(raw, []byte("DATA")) {
					t.Fatalf("UN raw = % X ok=%v, want DATA", raw, ok)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(bytes.NewReader(tt.input), transfer.ExplicitVRLittleEndian, ReaderOptions{})
			dataSet, err := reader.ReadDataSet()
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, dataSet)
		})
	}
}

func TestReadDataSetParsesExplicitVRUndefinedLengthUNAsImplicitVRSequence(t *testing.T) {
	unTag := core.NewTag(0x7777, 0x0010)
	patientNameTag := core.NewTag(0x0010, 0x0010)
	inner := dicomtest.EncodeElement(
		dicomtest.NewPNElement(patientNameTag, "PRIVATE^ITEM"),
		transfer.ImplicitVRLittleEndian,
	)
	for _, tt := range []struct {
		name   string
		syntax transfer.Syntax
		order  binary.ByteOrder
	}{
		{name: "explicit VR little endian", syntax: transfer.ExplicitVRLittleEndian, order: binary.LittleEndian},
		{name: "explicit VR big endian", syntax: transfer.ExplicitVRBigEndian, order: binary.BigEndian},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := bytes.Join([][]byte{
				explicitLongHeaderBytes(tt.order, unTag, core.VRUN, [2]byte{}, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner))),
				inner,
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil)

			reader := NewReader(bytes.NewReader(input), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})
			dataSet, err := reader.ReadDataSet()
			if err != nil {
				t.Fatalf("ReadDataSet() error = %v", err)
			}
			outer := onlyElement(t, dataSet)
			if outer.Tag() != unTag || outer.VR() != core.VRUN {
				t.Fatalf("outer element = %s %s, want %s UN", outer.Tag(), outer.VR(), unTag)
			}
			sequence, ok := outer.Value.(core.SequenceValue)
			if !ok || len(sequence.Items) != 1 || len(sequence.Items[0].Elements) != 1 {
				t.Fatalf("outer value = %#v, want one sequence item", outer.Value)
			}
			innerElement := sequence.Items[0].Elements[0]
			if innerElement.Tag() != patientNameTag || innerElement.VR() != core.VRPN || innerElement.StringValue() != "PRIVATE^ITEM" {
				t.Fatalf("inner element = %s %s %q, want %s PN PRIVATE^ITEM", innerElement.Tag(), innerElement.VR(), innerElement.StringValue(), patientNameTag)
			}
		})
	}
}

func onlyElement(t *testing.T, dataSet core.DataSet) core.Element {
	t.Helper()
	if len(dataSet.Elements) != 1 {
		t.Fatalf("element count = %d, want 1", len(dataSet.Elements))
	}
	return dataSet.Elements[0]
}
