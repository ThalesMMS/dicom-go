package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type preservingDictionary struct {
	name string
}

func (preservingDictionary) ByTag(core.Tag) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func (preservingDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func TestReadFileWithMinimalSyntheticFixture(t *testing.T) {
	file, err := ReadFile(bytes.NewReader(dicomtest.MinimalFile()))
	if err != nil {
		t.Fatal(err)
	}

	requireMinimalFile(t, file)
}
func TestReadFilePreservesSequenceValueInDatasetObject(t *testing.T) {
	sqTag := core.NewTag(0x0008, 0x1111)
	innerSqTag := core.NewTag(0x0008, 0x1115)
	sequence := dicomtest.NewSequenceElement(
		sqTag,
		core.DataSet{
			Elements: []core.Element{
				dicomtest.NewStringElement(core.NewTag(0x0008, 0x1150), core.VRUI, dicomtest.TestSOPClassUID),
				dicomtest.NewSequenceElement(
					innerSqTag,
					core.DataSet{
						Elements: []core.Element{
							dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "NEST^PATIENT"),
						},
					},
				),
			},
		},
		core.DataSet{
			Elements: []core.Element{
				dicomtest.NewStringElement(core.NewTag(0x0008, 0x1155), core.VRUI, dicomtest.TestSOPInstanceUID),
			},
		},
	)
	expected := core.DataSet{Elements: append(dicomtest.MinimalDataset(), sequence)}
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, expected.Elements...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	seqElem, ok := file.Dataset.Get(sqTag)
	if !ok {
		t.Fatalf("missing sequence element %s", sqTag)
	}
	seqValue, ok := seqElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("sequence value type = %T, want core.SequenceValue", seqElem.Value)
	}
	if len(seqValue.Items) != 2 {
		t.Fatalf("sequence item count = %d, want 2", len(seqValue.Items))
	}
	firstItem := seqValue.Items[0]
	if len(firstItem.Elements) < 2 {
		t.Fatalf("first sequence item element count = %d, want at least 2", len(firstItem.Elements))
	}
	if got := firstItem.Elements[0].StringValue(); got != dicomtest.TestSOPClassUID {
		t.Fatalf("first nested UID = %q, want %q", got, dicomtest.TestSOPClassUID)
	}
	nestedElem := firstItem.Elements[1]
	if nestedElem.Tag() != innerSqTag {
		t.Fatalf("nested sequence tag = %s, want %s", nestedElem.Tag(), innerSqTag)
	}
	nestedValue, ok := nestedElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("nested sequence value type = %T, want core.SequenceValue", nestedElem.Value)
	}
	itemsLen := len(nestedValue.Items)
	elementsLen := 0
	if itemsLen > 0 {
		elementsLen = len(nestedValue.Items[0].Elements)
	}
	if itemsLen != 1 || elementsLen != 1 {
		t.Fatalf("nested sequence shape = %d items / %d elements, want 1 / 1", itemsLen, elementsLen)
	}
	if got := nestedValue.Items[0].Elements[0].StringValue(); got != "NEST^PATIENT" {
		t.Fatalf("nested patient name = %q, want %q", got, "NEST^PATIENT")
	}

	got := core.DataSet{Elements: file.Dataset.Elements()}
	if diff := dicomtest.DiffDataSet(got, expected); diff != "" {
		t.Fatalf("dataset mismatch:\n%s", diff)
	}
}
func TestReadFileReturnsTypedErrorForUnknownTransferSyntax(t *testing.T) {
	data := buildPart10FileWithTransferSyntaxUID("1.2.840.10008.999.1")

	_, err := ReadFile(bytes.NewReader(data))
	if !errors.Is(err, transfer.ErrUnknownTransferSyntax) {
		t.Fatalf("expected ErrUnknownTransferSyntax, got %v", err)
	}
	if errors.Is(err, ErrFileMeta) {
		t.Fatalf("transfer syntax resolution error should not be classified as file meta error: %v", err)
	}
	if errors.Is(err, ErrDataSet) {
		t.Fatalf("transfer syntax resolution error should not be classified as dataset error: %v", err)
	}
	if errors.Is(err, ErrInvalidFileMetaGroupLength) || errors.Is(err, ErrInvalidFileMetaGroupLengthValue) {
		t.Fatalf("unknown transfer syntax should not match file meta group length sentinels: %v", err)
	}
}
func TestReadFileReturnsMissingTransferSyntaxForPaddedUID(t *testing.T) {
	data := buildPart10FileWithTransferSyntaxUID(" \x00")

	_, err := ReadFile(bytes.NewReader(data))
	if !errors.Is(err, ErrMissingTransferSyntax) {
		t.Fatalf("expected ErrMissingTransferSyntax, got %v", err)
	}
	if errors.Is(err, ErrFileMeta) {
		t.Fatalf("missing transfer syntax should not be classified as generic file meta error: %v", err)
	}
	if errors.Is(err, transfer.ErrUnknownTransferSyntax) || errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("missing transfer syntax should not match transfer syntax lookup errors: %v", err)
	}
}
func TestReadFileReturnsTypedErrorForUnsupportedTransferSyntax(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.DeflatedExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadFile(bytes.NewReader(data))
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax for %q, got %v", transfer.DeflatedExplicitVRLittleEndian.UID, err)
	}
	if errors.Is(err, ErrFileMeta) || errors.Is(err, ErrDataSet) {
		t.Fatalf("unsupported transfer syntax should remain distinct from meta/dataset errors: %v", err)
	}
}
func TestReadFileReturnsMissingPreambleAndKeepsErrorDistinct(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)

	_, err := ReadFile(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMissingPreamble) {
		t.Fatalf("expected ErrMissingPreamble, got %v", err)
	}
	if errors.Is(err, ErrMissingTransferSyntax) ||
		errors.Is(err, ErrInvalidFileMetaGroupLength) ||
		errors.Is(err, ErrInvalidFileMetaGroupLengthValue) ||
		errors.Is(err, transfer.ErrUnknownTransferSyntax) ||
		errors.Is(err, transfer.ErrUnsupportedTransferSyntax) ||
		errors.Is(err, ErrFileMeta) ||
		errors.Is(err, ErrDataSet) {
		t.Fatalf("missing preamble should remain distinct from all other classified read errors: %v", err)
	}
}
func TestReadFileRejectsUnsupportedEncapsulatedTransferSyntax(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
		[]byte{0x00, 0x01},
	)
	data, err := dicomtest.Part10File(transfer.JPEGBaseline, append(dicomtest.MinimalDataset(), pixel)...)
	if err != nil {
		t.Fatal(err)
	}

	// Keep both public entry points covered: ReadFile uses default options,
	// while ReadFileWithOptions exercises the explicit options path.
	tests := []struct {
		name string
		read func(*bytes.Reader) error
	}{
		{
			name: "ReadFile",
			read: func(reader *bytes.Reader) error {
				_, err := ReadFile(reader)
				return err
			},
		},
		{
			name: "ReadFileWithOptions",
			read: func(reader *bytes.Reader) error {
				_, err := ReadFileWithOptions(reader, ReadFileOptions{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.read(bytes.NewReader(data))
			if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
				t.Fatalf("expected ErrUnsupportedTransferSyntax for %q, got %v", transfer.JPEGBaseline.UID, err)
			}
		})
	}
}
func TestReadFilePropagatesAbsoluteParserOffsets(t *testing.T) {
	elem := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST")
	datasetBytes := dicomtest.EncodeElement(elem, transfer.ExplicitVRLittleEndian)
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, elem)
	if err != nil {
		t.Fatal(err)
	}

	truncated := data[:len(data)-2]
	_, err = ReadFile(bytes.NewReader(truncated))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrDataSet) {
		t.Fatalf("expected dataset error classification, got %v", err)
	}
	if errors.Is(err, ErrFileMeta) {
		t.Fatalf("dataset parse failure should not be classified as file meta error: %v", err)
	}
	if errors.Is(err, ErrInvalidFileMetaGroupLength) || errors.Is(err, ErrInvalidFileMetaGroupLengthValue) {
		t.Fatalf("dataset parse failure should not match file meta group length sentinels: %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("partial dataset value should not match io.EOF: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}

	var parseErr *parser.ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}

	wantOffset := int64(len(data) - len(datasetBytes) + 8)
	if parseErr.Offset != wantOffset {
		t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, wantOffset)
	}
	if parseErr.Tag != elem.Tag() {
		t.Fatalf("unexpected tag: got %s want %s", parseErr.Tag, elem.Tag())
	}
	if parseErr.VR != elem.VR() {
		t.Fatalf("unexpected VR: got %s want %s", parseErr.VR, elem.VR())
	}
}
func TestReadFileReturnsTypedErrorForInvalidFileMetaGroupLengthTag(t *testing.T) {
	meta := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewUIElement(core.NewTag(0x0002, 0x0010), transfer.ExplicitVRLittleEndian.UID),
	)
	data := buildMalformedPart10File(meta, dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...))

	_, err := ReadFile(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrFileMeta) {
		t.Fatalf("expected file meta error classification, got %v", err)
	}
	if !errors.Is(err, ErrInvalidFileMetaGroupLength) {
		t.Fatalf("expected ErrInvalidFileMetaGroupLength, got %v", err)
	}
	if errors.Is(err, ErrDataSet) {
		t.Fatalf("file meta error should not be classified as dataset error: %v", err)
	}
}
func TestReadFileReturnsTypedErrorForInvalidFileMetaGroupLengthValue(t *testing.T) {
	meta := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.Uint16Element(core.NewTag(0x0002, 0x0000), core.VRUS, binary.LittleEndian, 8),
		dicomtest.NewUIElement(core.NewTag(0x0002, 0x0010), transfer.ExplicitVRLittleEndian.UID),
	)
	data := buildMalformedPart10File(meta, dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...))

	_, err := ReadFile(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrFileMeta) {
		t.Fatalf("expected file meta error classification, got %v", err)
	}
	if !errors.Is(err, ErrInvalidFileMetaGroupLengthValue) {
		t.Fatalf("expected ErrInvalidFileMetaGroupLengthValue, got %v", err)
	}
	if errors.Is(err, ErrInvalidFileMetaGroupLength) {
		t.Fatalf("invalid group length value should not match invalid group length tag sentinel: %v", err)
	}
}
func TestReadFileClassifiesInvalidVRInDatasetAsErrDataSet(t *testing.T) {
	meta := dicomtest.NewFileMetaBuilder().WithTransferSyntax(transfer.ExplicitVRLittleEndian.UID).Encode()
	dataset := []byte{
		0x10, 0x00, 0x10, 0x00,
		'Z', 'Z',
		0x04, 0x00,
		'T', 'E', 'S', 'T',
	}
	data := buildMalformedPart10File(meta, dataset)

	_, err := ReadFile(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrDataSet) {
		t.Fatalf("expected dataset error classification, got %v", err)
	}
	if errors.Is(err, ErrFileMeta) {
		t.Fatalf("dataset VR parse failure should not be classified as file meta error: %v", err)
	}

	var parseErr *parser.ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != parser.OpReadVR {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
}
func TestReadDataSetReadsRawImplicitVRLittleEndianDataSet(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)

	obj, err := ReadDataSet(bytes.NewReader(data), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := obj.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}
	if got, ok := obj.GetString(core.NewTag(0x0010, 0x0020)); !ok || got != "TESTID001" {
		t.Fatalf("unexpected patient id: %q ok=%v", got, ok)
	}
	if obj.Has(core.NewTag(0x0002, 0x0010)) {
		t.Fatal("raw dataset reader should not synthesize Part 10 file meta")
	}
}
func TestReadDataSetReadsRawExplicitVRLittleEndianDataSet(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)

	obj, err := ReadDataSet(bytes.NewReader(data), transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := obj.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}
	if obj.Has(core.NewTag(0x0002, 0x0010)) {
		t.Fatal("raw dataset reader should not synthesize Part 10 file meta")
	}
}
func TestIntegrationReadMappings(t *testing.T) {
	// Integration tests covering multiple read-path scenarios:
	// - read data with preamble / auto-detect preamble
	// - explicit VR LE without file meta / auto-detect without preamble
	// - OB value with unknown length
	//
	// The Go API uses separate entry points instead of a single auto-detecting
	// reader: ReadFile handles Part 10 streams with preamble, while ReadDataSet
	// handles raw datasets without file meta.
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "part10_with_preamble",
			run: func(t *testing.T) {
				data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
				if err != nil {
					t.Fatal(err)
				}

				file, err := ReadFile(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				requireMinimalFile(t, file)
			},
		},
		{
			name: "raw_dataset_without_file_meta",
			run: func(t *testing.T) {
				data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)

				obj, err := ReadDataSet(bytes.NewReader(data), transfer.ExplicitVRLittleEndian)
				if err != nil {
					t.Fatal(err)
				}
				requireRawDataSetWithoutFileMeta(t, obj)
			},
		},
		{
			name: "encapsulated_pixeldata_requires_supported_transfer_syntax",
			run: func(t *testing.T) {
				offsetTable := []byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00}
				pixel := dicomtest.NewFragmentSequenceElement(
					core.TagPixelData,
					offsetTable,
					[]byte{0xFF, 0x4F, 0xFF, 0x51},
					[]byte{0x00, 0x01, 0xFF, 0xD9},
				)
				data, err := dicomtest.Part10File(transfer.JPEGBaseline, append(dicomtest.MinimalDataset(), pixel)...)
				if err != nil {
					t.Fatal(err)
				}

				_, err = ReadFile(bytes.NewReader(data))
				requireUnsupportedTransferSyntax(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
func TestReadFileReadsValidNativePart10WithImplicitVRLittleEndian(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if got := file.TransferSyntax.UID; got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q", got)
	}
	if got, ok := file.Meta.GetString(core.NewTag(0x0002, 0x0010)); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("unexpected file meta transfer syntax uid: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}
}
func TestReadDataSetRejectsEncapsulatedTransferSyntaxWithoutCodec(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
	)
	data := dicomtest.EncodeElements(transfer.JPEGBaseline, append(dicomtest.MinimalDataset(), pixel)...)

	_, err := ReadDataSet(bytes.NewReader(data), transfer.JPEGBaseline)
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}
func TestReadDataSetWithOptionsAppliesParserLimits(t *testing.T) {
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(core.NewTag(0x7FE0, 0x0010), []byte{0x01, 0x02, 0x03, 0x04}))...,
	)

	_, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
		MaxElementBytes: 2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, parser.ErrMaxElementBytesExceeded) {
		t.Fatalf("expected ErrMaxElementBytesExceeded, got %v", err)
	}
}
func TestReadDataSetWithOptionsDefersValuesWhenInlineThresholdSet(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := []byte{0x01, 0x02, 0x03, 0x04}
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewOBElement(tag, want),
	)
	path := filepath.Join(t.TempDir(), "raw.dcm")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		read func() (*Object, error)
	}{
		{
			name: "ReadDataSetWithOptions",
			read: func() (*Object, error) {
				return ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
					InlineValueBytesThreshold: 1,
				})
			},
		},
		{
			name: "OpenDataSetWithOptions",
			read: func() (*Object, error) {
				return OpenDataSetWithOptions(path, transfer.ExplicitVRLittleEndian, ReadFileOptions{
					InlineValueBytesThreshold: 1,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, err := tt.read()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = obj.Close() })
			elem, ok := obj.Get(tag)
			if !ok {
				t.Fatalf("missing element %s", tag)
			}
			if elem.Value != nil {
				t.Fatalf("expected deferred nil value for %s before CopyValueTo, got %T", tag, elem.Value)
			}

			var got bytes.Buffer
			n, err := obj.CopyValueTo(tag, &got)
			if err != nil {
				t.Fatal(err)
			}
			if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
			}
		})
	}
}
func TestReadDataSetWithOptionsCloseClearsValueProvider(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := []byte{0x01, 0x02, 0x03, 0x04}
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewOBElement(tag, want),
	)

	obj, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
		InlineValueBytesThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	if _, err := obj.CopyValueTo(tag, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied % X, want % X", got.Bytes(), want)
	}

	if err := obj.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := obj.CopyValueTo(tag, &bytes.Buffer{}); err == nil {
		t.Fatal("CopyValueTo after Object.Close(): expected error, got nil")
	} else if !strings.Contains(err.Error(), "no value provider") {
		t.Fatalf("CopyValueTo after Object.Close() error = %v, want no value provider", err)
	}
}
func TestReadDataSetWithOptionsRejectsDeferredDuplicateTagReplay(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	first := []byte{0x01, 0x02, 0x03, 0x04}
	second := []byte{0x05, 0x06, 0x07, 0x08}
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewOBElement(tag, first),
		dicomtest.NewOBElement(tag, second),
	)

	obj, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
		InlineValueBytesThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	elem, ok := obj.Get(tag)
	if !ok {
		t.Fatalf("missing element %s", tag)
	}
	if elem.Value != nil {
		t.Fatalf("expected deferred duplicate value for %s, got %T", tag, elem.Value)
	}
	if obj.valueProvider != nil {
		t.Fatal("duplicate deferred tag should not attach a tag-only valueProvider")
	}

	var got bytes.Buffer
	_, err = obj.CopyValueTo(tag, &got)
	if err == nil {
		t.Fatal("CopyValueTo on deferred duplicate tag: expected error, got nil")
	}
	if bytes.Equal(got.Bytes(), first) {
		t.Fatalf("CopyValueTo returned first duplicate bytes % X for ambiguous tag", got.Bytes())
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("CopyValueTo duplicate error = %v, want ambiguous", err)
	}
}
func TestReadDataSetWithOptionsMaterializesSequenceItemValuesWhenInlineThresholdSet(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1111)
	valueTag := core.NewTag(0x7FE0, 0x0010)
	want := []byte{0x01, 0x02, 0x03, 0x04}
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewSequenceElement(seqTag, core.DataSet{
			Elements: []core.Element{dicomtest.NewOBElement(valueTag, want)},
		}),
	)

	obj, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
		InlineValueBytesThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.deferredCount != 0 {
		t.Fatalf("deferredCount = %d, want 0 for sequence item values", obj.deferredCount)
	}
	items, ok := obj.GetSequence(seqTag)
	if !ok || len(items) != 1 {
		t.Fatalf("GetSequence(%s) returned %d items, ok=%v; want 1 item", seqTag, len(items), ok)
	}
	elem, ok := items[0].Get(valueTag)
	if !ok {
		t.Fatalf("missing item element %s", valueTag)
	}
	if elem.Value == nil {
		t.Fatalf("expected item element %s to be materialized", valueTag)
	}

	var got bytes.Buffer
	n, err := items[0].CopyValueTo(valueTag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}
}
func TestReadDataSetWithOptionsPreservesConfiguredObjectDictionary(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	dict := preservingDictionary{name: "dataset"}

	obj, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
		Dictionary: dict,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.dict != dict {
		t.Fatalf("Object dictionary = %#v, want %#v", obj.dict, dict)
	}
}
func TestReadDataSetRejectsDeflatedTransferSyntax(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)

	_, err := ReadDataSet(bytes.NewReader(data), transfer.DeflatedExplicitVRLittleEndian)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}
func TestReadDataSetCanonicalizesRegisteredUIDOnlySyntax(t *testing.T) {
	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	data := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, want.Elements...)

	got, err := ReadDataSet(bytes.NewReader(data), transfer.Syntax{UID: transfer.ExplicitVRBigEndian.UID})
	if err != nil {
		t.Fatal(err)
	}
	if diff := dicomtest.DiffDataSet(got.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after canonicalized read:\n%s", diff)
	}
}
func TestReadDataSetAcceptsCallerProvidedUnknownSyntaxWithHints(t *testing.T) {
	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, want.Elements...)

	got, err := ReadDataSet(bytes.NewReader(data), transfer.Syntax{
		UID:        "9.8.7.6.5",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
		Supported:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff := dicomtest.DiffDataSet(got.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after caller-provided syntax read:\n%s", diff)
	}
}
func TestReadDataSetRejectsCallerProvidedUnknownDeflatedSyntax(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)

	_, err := ReadDataSet(bytes.NewReader(data), transfer.Syntax{
		UID:        "9.8.7.6.5",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
		Deflated:   true,
	})
	if !errors.Is(err, transfer.ErrUnknownTransferSyntax) {
		t.Fatalf("expected ErrUnknownTransferSyntax, got %v", err)
	}
}
func TestOpenDataSetReadsRawDataSetFromPath(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	path := filepath.Join(t.TempDir(), "raw.dcm")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	obj, err := OpenDataSet(path, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := obj.GetString(core.NewTag(0x0010, 0x0020)); !ok || got != "TESTID001" {
		t.Fatalf("unexpected patient id: %q ok=%v", got, ok)
	}
}
func TestOpenDataSetWithOptionsDoesNotKeepSourceWithoutDeferredValues(t *testing.T) {
	data := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	path := filepath.Join(t.TempDir(), "raw.dcm")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	obj, err := OpenDataSetWithOptions(path, transfer.ExplicitVRLittleEndian, ReadFileOptions{
		InlineValueBytesThreshold: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.valueProvider != nil {
		t.Fatal("unexpected valueProvider without deferred values")
	}
	if obj.source != nil {
		t.Fatal("OpenDataSetWithOptions retained source without deferred values")
	}
}
func TestReadFileWithZeroValuedOptionsPreservesValidFileCompatibility(t *testing.T) {
	data := dicomtest.MinimalFile()

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}

	requireMinimalFile(t, file)
}
func TestReadFileWithExplicitHighLimitsPreservesValidFileCompatibility(t *testing.T) {
	data := dicomtest.MinimalFile()

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		MaxElementBytes:  64 << 20,
		MaxTotalBytes:    512 << 20,
		MaxSequenceDepth: 64,
		MaxElements:      100000,
		MaxFragments:     10000,
	})
	if err != nil {
		t.Fatal(err)
	}

	requireMinimalFile(t, file)
}
func TestReadFileWithOptionsPreservesConfiguredObjectDictionaries(t *testing.T) {
	data := dicomtest.MinimalFile()
	datasetDict := preservingDictionary{name: "dataset"}
	metaDict := preservingDictionary{name: "meta"}

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		Dictionary:         datasetDict,
		FileMetaDictionary: metaDict,
	})
	if err != nil {
		t.Fatal(err)
	}
	if file.Dataset.dict != datasetDict {
		t.Fatalf("Dataset dictionary = %#v, want %#v", file.Dataset.dict, datasetDict)
	}
	if file.Meta.dict != metaDict {
		t.Fatalf("Meta dictionary = %#v, want %#v", file.Meta.dict, metaDict)
	}
}
func TestReadFileWithOptionsStreamsSkippedValueFromSeekableSource(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := bytes.Repeat([]byte{0xAB}, 128)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		InlineValueBytesThreshold: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	elem, ok := file.Dataset.Get(tag)
	if !ok {
		t.Fatalf("missing element %s", tag)
	}
	if elem.Value != nil {
		t.Fatalf("expected %s to be skipped, got value type %T", tag, elem.Value)
	}

	var got bytes.Buffer
	n, err := file.Dataset.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}
}
func TestReadFileWithOptionsMaterializesValueFromNonSeekableSource(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := bytes.Repeat([]byte{0xAB}, 128)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFileWithOptions(bytes.NewBuffer(data), ReadFileOptions{
		InlineValueBytesThreshold: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	elem, ok := file.Dataset.Get(tag)
	if !ok {
		t.Fatalf("missing element %s", tag)
	}
	if elem.Value == nil {
		t.Fatalf("expected %s to be materialized for non-seekable ReadFileWithOptions source", tag)
	}

	var got bytes.Buffer
	n, err := file.Dataset.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}
}
func TestOpenFileReadsMinimalPart10FromDisk(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "minimal.dcm")
	if err := os.WriteFile(path, dicomtest.MinimalFile(), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}

	requireMinimalFile(t, file)
}
func TestOpenFileWithOptionsKeepsLazySourceOpen(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := bytes.Repeat([]byte{0xAB}, 128)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))...,
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lazy.dcm")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := OpenFileWithOptions(path, ReadFileOptions{
		InlineValueBytesThreshold: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	var got bytes.Buffer
	n, err := file.Dataset.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}
}
func TestOpenFileWithOptionsDoesNotKeepSourceWithoutDeferredValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "minimal.dcm")
	if err := os.WriteFile(path, dicomtest.MinimalFile(), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := OpenFileWithOptions(path, ReadFileOptions{
		InlineValueBytesThreshold: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if file.Dataset.valueProvider != nil {
		t.Fatal("unexpected valueProvider without deferred values")
	}
	if file.source != nil {
		t.Fatal("OpenFileWithOptions retained source without deferred values")
	}
}
func TestOpenFileWithOptionsLazySourceFailsAfterClose(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := bytes.Repeat([]byte{0xAB}, 128)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))...,
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lazy-close.dcm")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := OpenFileWithOptions(path, ReadFileOptions{
		InlineValueBytesThreshold: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	var got bytes.Buffer
	n, err := file.Dataset.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}

	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := file.Dataset.CopyValueTo(tag, &bytes.Buffer{}); err == nil {
		t.Fatal("CopyValueTo after file.Close(): expected error, got nil")
	}
}
func TestOpenFileReturnsOSErrorForMissingPath(t *testing.T) {
	t.Parallel()

	_, err := OpenFile(filepath.Join(t.TempDir(), "does_not_exist.dcm"))
	if err == nil {
		t.Fatal("OpenFile on missing path: expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenFile on missing path: error = %v, want os.ErrNotExist", err)
	}
}
