package parser

import (
	"bytes"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"strings"
	"testing"
)

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
