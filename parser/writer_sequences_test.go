package parser

import (
	"bytes"
	"encoding/binary"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"strings"
	"testing"
)

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

func TestWriterPrivateUNSequenceByTransferSyntax(t *testing.T) {
	privateTag := core.NewTag(0x0011, 0x1001)
	dataset := core.DataSet{Elements: []core.Element{{
		Header: core.ElementHeader{Tag: privateTag, VR: core.VRUN},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "PRIVATE^ITEM"),
		}}}},
	}}}
	tests := []struct {
		name      string
		syntax    transfer.Syntax
		roundTrip bool
		errorText string
	}{
		{name: "implicit VR round trip", syntax: transfer.ImplicitVRLittleEndian, roundTrip: true},
		{name: "explicit VR rejection", syntax: transfer.ExplicitVRLittleEndian, errorText: "UN in Implicit VR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.roundTrip {
				got := roundTripDataSet(t, tt.syntax, dataset, defaultWriterOptions(), std.Dictionary)
				outer := onlyElement(t, got)
				if outer.Tag() != privateTag || outer.VR() != core.VRUN {
					t.Fatalf("round-trip private sequence = %s %s, want %s UN", outer.Tag(), outer.VR(), privateTag)
				}
				sequence, ok := outer.Value.(core.SequenceValue)
				if !ok || len(sequence.Items) != 1 || len(sequence.Items[0].Elements) != 1 {
					t.Fatalf("round-trip private sequence value = %#v", outer.Value)
				}
				if got := sequence.Items[0].Elements[0].StringValue(); got != "PRIVATE^ITEM" {
					t.Fatalf("round-trip nested value = %q, want PRIVATE^ITEM", got)
				}
				return
			}
			err := NewWriter(&bytes.Buffer{}, tt.syntax).WriteElement(dataset.Elements[0])
			if err == nil || !strings.Contains(err.Error(), tt.errorText) {
				t.Fatalf("WriteElement() error = %v, want %q", err, tt.errorText)
			}
		})
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

	// The Go model preserves the outer sequence as defined-length, but item
	// header lengths are normalized because core.SequenceValue does not retain
	// item length metadata after parsing. This fixture uses a consistent
	// explicit sequence length for the same structural pattern so the scaffold
	// parser can materialize it before the writer round-trip.
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
