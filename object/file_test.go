package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadFileWithMinimalSyntheticFixture(t *testing.T) {
	file, err := ReadFile(bytes.NewReader(dicomtest.MinimalFile()))
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0020)); !ok || got != "TESTID001" {
		t.Fatalf("unexpected patient id: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("unexpected SOP instance uid: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0020, 0x000D)); !ok || got != dicomtest.TestStudyInstanceUID {
		t.Fatalf("unexpected study instance uid: %q ok=%v", got, ok)
	}
	if got := file.TransferSyntax.UID; got != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q", got)
	}
	if file.Meta == nil || file.Dataset == nil {
		t.Fatal("expected meta and dataset objects to be populated")
	}
}

func TestFileCharacterSetNilReceiver(t *testing.T) {
	var file *File

	_, err := file.CharacterSet()
	if err == nil {
		t.Fatal("CharacterSet() on nil file should return an error")
	}

	file = &File{}
	_, err = file.CharacterSet()
	if err == nil {
		t.Fatal("CharacterSet() on file without dataset should return an error")
	}
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
	if got := seqValue.Items[0].Elements[0].StringValue(); got != dicomtest.TestSOPClassUID {
		t.Fatalf("first nested UID = %q, want %q", got, dicomtest.TestSOPClassUID)
	}
	nestedElem := seqValue.Items[0].Elements[1]
	if nestedElem.Tag() != innerSqTag {
		t.Fatalf("nested sequence tag = %s, want %s", nestedElem.Tag(), innerSqTag)
	}
	nestedValue, ok := nestedElem.Value.(core.SequenceValue)
	if !ok {
		t.Fatalf("nested sequence value type = %T, want core.SequenceValue", nestedElem.Value)
	}
	if len(nestedValue.Items) != 1 || len(nestedValue.Items[0].Elements) != 1 {
		t.Fatalf("nested sequence shape = %d items / %d elements, want 1 / 1", len(nestedValue.Items), len(nestedValue.Items[0].Elements))
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

func TestReadFileReturnsUnsupportedErrorForEncapsulatedTransferSyntaxWithoutCodec(t *testing.T) {
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

	_, err = ReadFile(bytes.NewReader(data))
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax for %q, got %v", transfer.JPEGBaseline.UID, err)
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

func TestReadFileRejectsUnsupportedEncapsulatedTransferSyntax(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.JPEGBaseline,
		append(dicomtest.MinimalDataset(), dicomtest.NewFragmentSequenceElement(
			core.TagPixelData,
			[]byte{0x00, 0x00, 0x00, 0x00},
			[]byte{0xFF, 0xD8, 0xFF, 0xD9},
			[]byte{0x00, 0x01},
		))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{})
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
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

func TestDicomRSIntegrationReadMappings(t *testing.T) {
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
				if got := file.TransferSyntax.UID; got != transfer.ExplicitVRLittleEndian.UID {
					t.Fatalf("unexpected transfer syntax uid: %q", got)
				}
				if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
					t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
				}
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
				if got, ok := obj.GetString(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
					t.Fatalf("unexpected SOP instance uid: %q ok=%v", got, ok)
				}
				if obj.Has(tagTransferSyntaxUID) {
					t.Fatal("raw dataset reader should not synthesize TransferSyntaxUID file meta")
				}
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
				if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
					t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
				}
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

func TestWriteFileRoundTripDerivesMetaAndUsesExplicitLEFileMeta(t *testing.T) {
	dataset := core.DataSet{Elements: dicomtest.MinimalDataset()}
	file := &File{
		Dataset:        FromDataSet(dataset, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if len(data) < 140 {
		t.Fatalf("encoded Part 10 length = %d, want at least 140", len(data))
	}
	if !bytes.Equal(data[:128], make([]byte, 128)) {
		t.Fatal("default preamble should be zero-filled")
	}
	if got := string(data[128:132]); got != "DICM" {
		t.Fatalf("magic marker = %q, want %q", got, "DICM")
	}
	wantMetaPrefix := []byte{0x02, 0x00, 0x00, 0x00, 'U', 'L', 0x04, 0x00}
	if !bytes.Equal(data[132:140], wantMetaPrefix) {
		t.Fatalf("file meta prefix = % X, want % X", data[132:140], wantMetaPrefix)
	}

	roundTrip, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.TransferSyntax.UID; got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax uid = %q, want %q", got, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := roundTrip.Meta.GetString(tagImplementationClassUID); !ok || got != implementationClassUID {
		t.Fatalf("implementation class uid = %q ok=%v, want %q", got, ok, implementationClassUID)
	}
	if diff := dicomtest.DiffDataSet(roundTrip.Dataset.ToDataSet(), dataset); diff != "" {
		t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
	}

	meta, _, datasetOffset, err := readFileMeta(bytes.NewReader(data[132:]), 132, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawGroupLength, ok := meta.GetRaw(tagFileMetaInformationGroupLength)
	if !ok || len(rawGroupLength) != 4 {
		t.Fatalf("group length raw bytes = %v ok=%v, want 4-byte raw value", rawGroupLength, ok)
	}
	gotGroupLength := binary.LittleEndian.Uint32(rawGroupLength)
	wantGroupLength := uint32(datasetOffset - 132 - 12)
	if gotGroupLength != wantGroupLength {
		t.Fatalf("file meta group length = %d, want %d", gotGroupLength, wantGroupLength)
	}
}

func TestWriteFileWithOptionsPreservesCustomPreamble(t *testing.T) {
	preamble := bytes.Repeat([]byte{0x5A}, 128)
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFileWithOptions(&buf, file, WriteFileOptions{Preamble: preamble}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes()[:128], preamble) {
		t.Fatal("custom preamble was not preserved in output")
	}

	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip.Preamble, preamble) {
		t.Fatal("custom preamble was not preserved after round-trip")
	}
}

func TestWriteFilePreservesExistingFilePreambleWhenOptionsOmitIt(t *testing.T) {
	preamble := bytes.Repeat([]byte{0x41}, 128)
	file := &File{
		Preamble:       preamble,
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes()[:128], preamble) {
		t.Fatal("existing file preamble was not preserved")
	}
}

func TestWriteFileRejectsInvalidPreambleLength(t *testing.T) {
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err := WriteFileWithOptions(io.Discard, file, WriteFileOptions{Preamble: []byte{0x00}})
	if !errors.Is(err, ErrInvalidPreambleLength) {
		t.Fatalf("expected ErrInvalidPreambleLength, got %v", err)
	}
}

func TestWriteFileInjectsResolvedSOPUIDsIntoDataset(t *testing.T) {
	dataset := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST^PATIENT"),
			dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "TESTID001"),
		},
	}
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.NewUIElement(tagMediaStorageSOPClassUID, dicomtest.TestSOPClassUID),
			dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, dicomtest.TestSOPInstanceUID),
		}, std.Dictionary),
		Dataset:        FromDataSet(dataset, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := roundTrip.Dataset.GetString(tagSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
		t.Fatalf("dataset SOPClassUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
	}
	if got, ok := roundTrip.Dataset.GetString(tagSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("dataset SOPInstanceUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
}

func TestWriteFileReconcilesMetaWithDatasetAndTransferSyntax(t *testing.T) {
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.Uint32Element(tagFileMetaInformationGroupLength, core.VRUL, binary.LittleEndian, 1),
			dicomtest.NewUIElement(tagMediaStorageSOPClassUID, "9.8.7"),
			dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, "9.8.7.6"),
			dicomtest.NewUIElement(tagTransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID),
		}, std.Dictionary),
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}

	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
		t.Fatalf("media storage SOP class uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
	}
	if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("media storage SOP instance uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
	if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.ImplicitVRLittleEndian.UID)
	}
}

func TestPrepareFileMetaIgnoresNonFileMetaElements(t *testing.T) {
	meta, err := prepareFileMeta(&File{
		Meta: FromElements([]core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0002, 0x0013), core.VRSH, "DICOMGO_TEST"),
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "SHOULD^NOT^MOVE"),
		}, std.Dictionary),
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	metaObj := FromElements(meta, std.Dictionary)
	if metaObj.Has(core.NewTag(0x0010, 0x0010)) {
		t.Fatal("prepareFileMeta should ignore non-(0002,xxxx) elements from file.Meta")
	}
	assertGroupLengthMatchesEncodedMeta(t, metaObj)
}

func TestWriteFileRejectsMissingRequiredUIDs(t *testing.T) {
	datasetWithoutSOPClass := append([]core.Element(nil), dicomtest.MinimalDataset()[1:]...)
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: datasetWithoutSOPClass}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err := WriteFile(io.Discard, file)
	if !errors.Is(err, ErrMissingSOPClassUID) {
		t.Fatalf("expected ErrMissingSOPClassUID, got %v", err)
	}

	datasetWithoutSOPInstance := append([]core.Element(nil), dicomtest.MinimalDataset()[:3]...)
	file = &File{
		Dataset:        FromDataSet(core.DataSet{Elements: datasetWithoutSOPInstance}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err = WriteFile(io.Discard, file)
	if !errors.Is(err, ErrMissingSOPInstanceUID) {
		t.Fatalf("expected ErrMissingSOPInstanceUID, got %v", err)
	}
}

func TestWriteFileRejectsDeflatedTransferSyntax(t *testing.T) {
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.DeflatedExplicitVRLittleEndian,
	}

	err := WriteFile(io.Discard, file)
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}

func TestResolveWriteTransferSyntaxReturnsCanonicalSyntax(t *testing.T) {
	got, err := resolveWriteTransferSyntax(&File{
		TransferSyntax: transfer.Syntax{UID: transfer.ExplicitVRBigEndian.UID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != transfer.ExplicitVRBigEndian {
		t.Fatalf("resolved syntax = %#v, want %#v", got, transfer.ExplicitVRBigEndian)
	}
}

func TestWriteFileRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		syntax   transfer.Syntax
		preamble []byte
	}{
		{name: "explicit_le_zero_preamble", syntax: transfer.ExplicitVRLittleEndian},
		{name: "explicit_le_custom_preamble", syntax: transfer.ExplicitVRLittleEndian, preamble: bytes.Repeat([]byte{0x7A}, 128)},
		{name: "implicit_le_zero_preamble", syntax: transfer.ImplicitVRLittleEndian},
		{name: "implicit_le_custom_preamble", syntax: transfer.ImplicitVRLittleEndian, preamble: bytes.Repeat([]byte{0x33}, 128)},
	}

	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &File{
				Dataset:        FromDataSet(want, std.Dictionary),
				TransferSyntax: tt.syntax,
			}

			var buf bytes.Buffer
			if err := WriteFileWithOptions(&buf, file, WriteFileOptions{Preamble: tt.preamble}); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()

			wantPreamble := tt.preamble
			if wantPreamble == nil {
				wantPreamble = make([]byte, 128)
			}
			if !bytes.Equal(data[:128], wantPreamble) {
				t.Fatalf("preamble mismatch: got % X want % X", data[:128], wantPreamble)
			}
			if got := string(data[128:132]); got != "DICM" {
				t.Fatalf("magic marker = %q, want %q", got, "DICM")
			}

			roundTrip, err := ReadFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if diff := dicomtest.DiffDataSet(roundTrip.Dataset.ToDataSet(), want); diff != "" {
				t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
			}
			if got := roundTrip.TransferSyntax.UID; got != tt.syntax.UID {
				t.Fatalf("transfer syntax uid = %q, want %q", got, tt.syntax.UID)
			}
			if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
				t.Fatalf("media storage SOP class uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
			}
			if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
				t.Fatalf("media storage SOP instance uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
			}
			if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != tt.syntax.UID {
				t.Fatalf("transfer syntax uid in file meta = %q ok=%v, want %q", got, ok, tt.syntax.UID)
			}
			if got, ok := roundTrip.Meta.GetString(tagImplementationClassUID); !ok || got != implementationClassUID {
				t.Fatalf("implementation class uid = %q ok=%v, want %q", got, ok, implementationClassUID)
			}
			assertGroupLengthMatchesEncodedMeta(t, roundTrip.Meta)
		})
	}
}

func TestPrepareFileMetaDerivesMissingElements(t *testing.T) {
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0002, 0x0013), core.VRSH, "DICOMGO_TEST"),
		}, std.Dictionary),
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}

	meta, err := prepareFileMeta(file, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	metaObj := FromElements(meta, std.Dictionary)
	if got, ok := metaObj.GetString(tagMediaStorageSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
		t.Fatalf("derived MediaStorageSOPClassUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
	}
	if got, ok := metaObj.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("derived MediaStorageSOPInstanceUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
	if got, ok := metaObj.GetString(tagTransferSyntaxUID); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("derived TransferSyntaxUID = %q ok=%v, want %q", got, ok, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := metaObj.GetString(tagImplementationClassUID); !ok || got != implementationClassUID {
		t.Fatalf("derived ImplementationClassUID = %q ok=%v, want %q", got, ok, implementationClassUID)
	}
	versionRaw, ok := metaObj.GetRaw(tagFileMetaInformationVersion)
	if !ok || !bytes.Equal(versionRaw, []byte{0x00, 0x01}) {
		t.Fatalf("FileMetaInformationVersion raw = %v ok=%v, want [0 1]", versionRaw, ok)
	}
	assertGroupLengthMatchesEncodedMeta(t, metaObj)
}

func TestPrepareFileMetaReturnsErrorsWhenMandatoryValuesCannotBeResolved(t *testing.T) {
	tests := []struct {
		name string
		file *File
		want error
	}{
		{
			name: "missing SOP class",
			file: &File{
				Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()[1:]}, std.Dictionary),
				TransferSyntax: transfer.ExplicitVRLittleEndian,
			},
			want: ErrMissingSOPClassUID,
		},
		{
			name: "missing SOP instance",
			file: &File{
				Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()[:3]}, std.Dictionary),
				TransferSyntax: transfer.ExplicitVRLittleEndian,
			},
			want: ErrMissingSOPInstanceUID,
		},
		{
			name: "missing transfer syntax",
			file: &File{
				Dataset: FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
			},
			want: ErrMissingTransferSyntax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteFile(io.Discard, tt.file)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestPrepareFileMetaCalculatesKnownGroupLength(t *testing.T) {
	meta, err := prepareFileMeta(&File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	if len(meta) == 0 || meta[0].Tag() != tagFileMetaInformationGroupLength {
		t.Fatalf("first meta element = %v, want %v", meta[0].Tag(), tagFileMetaInformationGroupLength)
	}
	raw, ok := meta[0].RawBytes()
	if !ok || len(raw) != 4 {
		t.Fatalf("group length raw bytes = %v ok=%v, want 4-byte raw value", raw, ok)
	}
	got := binary.LittleEndian.Uint32(raw)
	want := uint32(len(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, meta[1:]...)))
	if got != want {
		t.Fatalf("group length = %d, want %d", got, want)
	}
}

func TestWriteFileAlwaysEncodesFileMetaInExplicitVRLittleEndian(t *testing.T) {
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRBigEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if got := data[132:140]; !bytes.Equal(got, []byte{0x02, 0x00, 0x00, 0x00, 'U', 'L', 0x04, 0x00}) {
		t.Fatalf("file meta first header = % X, want Explicit VR Little Endian UL header", got)
	}

	meta, _, datasetOffset, err := readFileMeta(bytes.NewReader(data[132:]), 132, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.ExplicitVRBigEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.ExplicitVRBigEndian.UID)
	}
	wantDatasetPrefix := dicomtest.EncodeElement(dicomtest.NewUIElement(tagSOPClassUID, dicomtest.TestSOPClassUID), transfer.ExplicitVRBigEndian)[:8]
	gotDatasetPrefix := data[datasetOffset : datasetOffset+8]
	if !bytes.Equal(gotDatasetPrefix, wantDatasetPrefix) {
		t.Fatalf("dataset first header = % X, want % X", gotDatasetPrefix, wantDatasetPrefix)
	}
}

func TestWriteFileRoundTripWithNestedSequences(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1111)
	nestedSeqTag := core.NewTag(0x0008, 0x1115)
	want := core.DataSet{
		Elements: append([]core.Element{}, dicomtest.MinimalDataset()...),
	}
	want.Elements = append(want.Elements, dicomtest.NewSequenceElement(
		seqTag,
		core.DataSet{
			Elements: []core.Element{
				dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "SEQ^ONE"),
				dicomtest.NewSequenceElement(
					nestedSeqTag,
					core.DataSet{
						Elements: []core.Element{
							dicomtest.NewUIElement(core.NewTag(0x0008, 0x1150), dicomtest.TestSOPClassUID),
						},
					},
				),
			},
		},
		core.DataSet{
			Elements: []core.Element{
				dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "SEQ^TWO"),
			},
		},
	))

	file := &File{
		Dataset:        FromDataSet(want, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if diff := dicomtest.DiffDataSet(got.Dataset.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after sequence round-trip:\n%s", diff)
	}
}

func TestWriteFileRejectsUnsupportedEncapsulatedTransferSyntax(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
		[]byte{0x01, 0x02, 0x03, 0x00},
	)
	want := core.DataSet{Elements: append(dicomtest.MinimalDataset(), pixel)}
	file := &File{
		Dataset:        FromDataSet(want, std.Dictionary),
		TransferSyntax: transfer.JPEGBaseline,
	}

	if err := WriteFile(io.Discard, file); !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}

func TestWriteDataSetProducesRawDatasetOutput(t *testing.T) {
	wantElements := dicomtest.MinimalDataset()
	obj := FromDataSet(core.DataSet{Elements: wantElements}, std.Dictionary)

	var buf bytes.Buffer
	if err := WriteDataSet(&buf, obj, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, wantElements...)
	got := buf.Bytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
	}
	if len(got) >= 132 && bytes.Equal(got[:128], make([]byte, 128)) && string(got[128:132]) == "DICM" {
		t.Fatal("WriteDataSet should not write a Part 10 preamble or DICM marker")
	}
	if bytes.Contains(got, []byte("DICM")) {
		t.Fatal("WriteDataSet output should not contain the Part 10 marker")
	}
}

func TestWriteDataSetRejectsNilObject(t *testing.T) {
	err := WriteDataSet(io.Discard, nil, transfer.ExplicitVRLittleEndian)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "dicom: nil object passed to WriteDataSet" {
		t.Fatalf("error = %q, want %q", got, "dicom: nil object passed to WriteDataSet")
	}
}

func TestWriteDataSetCanonicalizesRegisteredUIDOnlySyntax(t *testing.T) {
	obj := FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary)

	var buf bytes.Buffer
	if err := WriteDataSet(&buf, obj, transfer.Syntax{UID: transfer.ExplicitVRBigEndian.UID}); err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, dicomtest.MinimalDataset()...)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
	}
}

func TestWriteDataSetRoundTripAcrossTransferSyntaxes(t *testing.T) {
	tests := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	}

	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	obj := FromDataSet(want, std.Dictionary)
	for _, syntax := range tests {
		t.Run(syntax.UID, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteDataSet(&buf, obj, syntax); err != nil {
				t.Fatal(err)
			}
			got, err := ReadDataSet(bytes.NewReader(buf.Bytes()), syntax)
			if err != nil {
				t.Fatal(err)
			}
			if diff := dicomtest.DiffDataSet(got.ToDataSet(), want); diff != "" {
				t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
			}
		})
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

func TestWriteDataSetAcceptsCallerProvidedUnknownSyntaxWithHints(t *testing.T) {
	obj := FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary)

	var buf bytes.Buffer
	err := WriteDataSet(&buf, obj, transfer.Syntax{
		UID:        "9.8.7.6.5",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
		Supported:  false,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
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

func TestWriteDataSetRejectsCallerProvidedUnknownDeflatedSyntax(t *testing.T) {
	obj := FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary)

	err := WriteDataSet(io.Discard, obj, transfer.Syntax{
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

func TestReadFileWithZeroValuedOptionsPreservesValidFileCompatibility(t *testing.T) {
	data := dicomtest.MinimalFile()

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}
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

	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0020)); !ok || got != "TESTID001" {
		t.Fatalf("unexpected patient id: %q ok=%v", got, ok)
	}
}

func TestFileAccessorsRouteMetaAndDatasetTags(t *testing.T) {
	file, err := ReadFile(bytes.NewReader(dicomtest.MinimalFile()))
	if err != nil {
		t.Fatal(err)
	}

	metaTag := core.NewTag(0x0002, 0x0010)
	nameTag := core.NewTag(0x0010, 0x0010)

	metaElem, ok := file.Get(metaTag)
	if !ok || metaElem.Tag() != metaTag {
		t.Fatalf("unexpected meta element: tag=%s ok=%v", metaElem.Tag(), ok)
	}
	nameElem, ok := file.Get(nameTag)
	if !ok || nameElem.Tag() != nameTag {
		t.Fatalf("unexpected dataset element: tag=%s ok=%v", nameElem.Tag(), ok)
	}

	if got, ok := file.GetString(metaTag); !ok || got != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q ok=%v", got, ok)
	}
	if got, ok := file.GetString(nameTag); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}

	metaRaw, ok := file.GetRaw(metaTag)
	if !ok || len(metaRaw) == 0 {
		t.Fatalf("unexpected meta raw bytes: %v ok=%v", metaRaw, ok)
	}
	nameRaw, ok := file.GetRaw(nameTag)
	if !ok || len(nameRaw) == 0 {
		t.Fatalf("unexpected dataset raw bytes: %v ok=%v", nameRaw, ok)
	}

	if !file.Has(metaTag) {
		t.Fatal("expected file meta element to be reachable through File.Has")
	}
	if !file.Has(nameTag) {
		t.Fatal("expected dataset element to be reachable through File.Has")
	}

	nameStrings, ok := file.GetStrings(nameTag)
	if !ok || len(nameStrings) != 1 || nameStrings[0] != "TEST^PATIENT" {
		t.Fatalf("unexpected dataset strings: %v ok=%v", nameStrings, ok)
	}

	seqTag := core.NewTag(0x0008, 0x1111)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewSequenceElement(
			seqTag,
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewPNElement(nameTag, "SEQ^PATIENT"),
				},
			},
		))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	fileWithSequence, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := fileWithSequence.GetSequence(seqTag)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected sequence items: len=%d ok=%v", len(items), ok)
	}
	if got, ok := items[0].GetString(nameTag); !ok || got != "SEQ^PATIENT" {
		t.Fatalf("unexpected nested sequence patient: %q ok=%v", got, ok)
	}
}

func buildPart10FileWithTransferSyntaxUID(uid string) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(uid).Encode())
	buf.Write(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...))
	return buf.Bytes()
}

func buildMalformedPart10File(meta, dataset []byte) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(meta)
	buf.Write(dataset)
	return buf.Bytes()
}

func assertGroupLengthMatchesEncodedMeta(t *testing.T, meta *Object) {
	t.Helper()
	raw, ok := meta.GetRaw(tagFileMetaInformationGroupLength)
	if !ok || len(raw) != 4 {
		t.Fatalf("group length raw bytes = %v ok=%v, want 4-byte raw value", raw, ok)
	}
	got := binary.LittleEndian.Uint32(raw)

	var rest []core.Element
	for _, el := range meta.SortedElements() {
		if el.Tag() == tagFileMetaInformationGroupLength {
			continue
		}
		rest = append(rest, el)
	}
	want := uint32(len(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, rest...)))
	if got != want {
		t.Fatalf("group length = %d, want %d", got, want)
	}
}
