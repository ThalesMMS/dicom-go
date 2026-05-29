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

var tagPixelDataProviderURL = core.NewTag(0x0028, 0x7FE0)

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
	data, err := dicomtest.Part10File(transfer.XMLEncoding, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadFile(bytes.NewReader(data))
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax for %q, got %v", transfer.XMLEncoding.UID, err)
	}
	if strings.Contains(err.Error(), "scaffold") {
		t.Fatalf("unsupported transfer syntax error should not mention scaffold: %v", err)
	}
	if errors.Is(err, ErrFileMeta) || errors.Is(err, ErrDataSet) {
		t.Fatalf("unsupported transfer syntax should remain distinct from meta/dataset errors: %v", err)
	}
}
func TestReadFileInflatesDeflatedExplicitVRLittleEndian(t *testing.T) {
	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	data := buildDeflatedPart10File(t, want.Elements...)

	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if got := file.TransferSyntax.UID; got != transfer.DeflatedExplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax = %q, want %q", got, transfer.DeflatedExplicitVRLittleEndian.UID)
	}
	if got, ok := file.Meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.DeflatedExplicitVRLittleEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.DeflatedExplicitVRLittleEndian.UID)
	}
	if diff := dicomtest.DiffDataSet(file.Dataset.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after inflated read:\n%s", diff)
	}
}
func TestReadFilePreservesJPIPReferencedMetadata(t *testing.T) {
	const providerURL = "https://jpip.example/studies/1/frames.jp2"
	tests := []struct {
		name     string
		syntax   transfer.Syntax
		deflated bool
	}{
		{name: "jpip", syntax: transfer.JPIPReferenced},
		{name: "jpip deflate", syntax: transfer.JPIPReferencedDeflate, deflated: true},
		{name: "jpip htj2k", syntax: transfer.JPIPHTJ2KReferenced},
		{name: "jpip htj2k deflate", syntax: transfer.JPIPHTJ2KReferencedDeflate, deflated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataset := jpipReferencedDataset(providerURL)
			var data []byte
			if tt.deflated {
				data = buildDeflatedPart10FileWithTransferSyntax(t, tt.syntax, dataset...)
			} else {
				var err error
				data, err = dicomtest.Part10File(tt.syntax, dataset...)
				if err != nil {
					t.Fatal(err)
				}
			}

			file, err := ReadFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}

			if got := file.TransferSyntax.UID; got != tt.syntax.UID {
				t.Fatalf("transfer syntax = %q, want %q", got, tt.syntax.UID)
			}
			if got, ok := file.Dataset.GetString(tagPixelDataProviderURL); !ok || got != providerURL {
				t.Fatalf("PixelDataProviderURL = %q ok=%v, want %q", got, ok, providerURL)
			}
			if _, ok := file.Dataset.Get(core.TagPixelData); ok {
				t.Fatal("JPIP referenced metadata dataset should not contain Pixel Data")
			}
		})
	}
}
func jpipReferencedDataset(providerURL string) []core.Element {
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	out := elements[:0]
	for _, elem := range elements {
		if elem.Tag() == core.TagPixelData {
			continue
		}
		out = append(out, elem)
	}
	return append(out, dicomtest.NewStringElement(tagPixelDataProviderURL, core.VRUR, providerURL))
}
func TestReadFilePreservesEncapsulatedVideoMediaPayload(t *testing.T) {
	wantFragments := [][]byte{
		{0x00, 0x00, 0x01, 0x09, 0x10, 0x00},
		{0x00, 0x00, 0x01, 0x65, 0x88, 0x00},
	}
	data, err := dicomtest.Part10File(transfer.MPEG4HP41F, videoMediaDataset(wantFragments...)...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if got := file.TransferSyntax.UID; got != transfer.MPEG4HP41F.UID {
		t.Fatalf("transfer syntax = %q, want %q", got, transfer.MPEG4HP41F.UID)
	}
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("Pixel Data missing after video metadata read")
	}
	fragments, ok := elem.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
	}
	if len(fragments.Fragments) != len(wantFragments) {
		t.Fatalf("fragment count = %d, want %d", len(fragments.Fragments), len(wantFragments))
	}
	for i := range wantFragments {
		if !bytes.Equal(fragments.Fragments[i], wantFragments[i]) {
			t.Fatalf("fragment %d = % X, want % X", i, fragments.Fragments[i], wantFragments[i])
		}
	}
}
func TestReadFilePreservesEncapsulatedJPEGXLPayload(t *testing.T) {
	wantFragments := [][]byte{
		{0xFF, 0x0A, 0x20, 0x01},
		{0x00, 0x00, 0x00, 0x0C, 0x4A, 0x58, 0x4C, 0x20},
	}
	data, err := dicomtest.Part10File(transfer.JPEGXLLossless, jpegXLDataset(wantFragments...)...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if got := file.TransferSyntax.UID; got != transfer.JPEGXLLossless.UID {
		t.Fatalf("transfer syntax = %q, want %q", got, transfer.JPEGXLLossless.UID)
	}
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("Pixel Data missing after JPEG XL metadata read")
	}
	fragments, ok := elem.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
	}
	if len(fragments.Fragments) != len(wantFragments) {
		t.Fatalf("fragment count = %d, want %d", len(fragments.Fragments), len(wantFragments))
	}
	for i := range wantFragments {
		if !bytes.Equal(fragments.Fragments[i], wantFragments[i]) {
			t.Fatalf("fragment %d = % X, want % X", i, fragments.Fragments[i], wantFragments[i])
		}
	}
}
func TestReadFilePreservesSupportedStillImagePayloads(t *testing.T) {
	wantFragments := [][]byte{
		{0xFF, 0xD8, 0xFF, 0xC1, 0x00, 0x0B},
		{0xFF, 0xD8, 0xFF, 0xC3, 0x00, 0x0B},
	}
	for _, syntax := range []transfer.Syntax{
		transfer.JPEGExtended,
		transfer.JPEGLosslessNonHierarchical,
		transfer.JPEGLosslessSV1,
		transfer.RLELossless,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			data, err := dicomtest.Part10File(syntax, encapsulatedStillImageDataset(wantFragments...)...)
			if err != nil {
				t.Fatal(err)
			}

			file, err := ReadFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}

			if got := file.TransferSyntax.UID; got != syntax.UID {
				t.Fatalf("transfer syntax = %q, want %q", got, syntax.UID)
			}
			elem, ok := file.Dataset.Get(core.TagPixelData)
			if !ok {
				t.Fatal("Pixel Data missing after supported JPEG metadata read")
			}
			fragments, ok := elem.Value.(core.FragmentSequence)
			if !ok {
				t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
			}
			if len(fragments.Fragments) != len(wantFragments) {
				t.Fatalf("fragment count = %d, want %d", len(fragments.Fragments), len(wantFragments))
			}
			for i := range wantFragments {
				if !bytes.Equal(fragments.Fragments[i], wantFragments[i]) {
					t.Fatalf("fragment %d = % X, want % X", i, fragments.Fragments[i], wantFragments[i])
				}
			}
		})
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
func TestReadFileAcceptsOptionalStillImageCodecFragmentSequences(t *testing.T) {
	wantFragments := [][]byte{
		{0xFF, 0x4F, 0xFF, 0x51},
		{0x00, 0x01, 0xFF, 0xD9},
	}
	for _, syntax := range []transfer.Syntax{
		transfer.JPEGLSLossless,
		transfer.JPEGLSNearLossless,
		transfer.JPEG2000LosslessOnly,
		transfer.JPEG2000,
		transfer.HTJ2KLossless,
		transfer.HTJ2K,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			pixel := dicomtest.NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00},
				wantFragments...,
			)
			data, err := dicomtest.Part10File(syntax, append(dicomtest.MinimalDataset(), pixel)...)
			if err != nil {
				t.Fatal(err)
			}

			// Keep both public entry points covered: ReadFile uses default
			// options, while ReadFileWithOptions exercises the explicit path.
			tests := []struct {
				name string
				read func(*bytes.Reader) (*File, error)
			}{
				{
					name: "ReadFile",
					read: func(reader *bytes.Reader) (*File, error) {
						return ReadFile(reader)
					},
				},
				{
					name: "ReadFileWithOptions",
					read: func(reader *bytes.Reader) (*File, error) {
						return ReadFileWithOptions(reader, ReadFileOptions{})
					},
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					file, err := tt.read(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					if got := file.TransferSyntax.UID; got != syntax.UID {
						t.Fatalf("transfer syntax = %q, want %q", got, syntax.UID)
					}
					elem, ok := file.Dataset.Get(core.TagPixelData)
					if !ok {
						t.Fatal("missing Pixel Data")
					}
					value, ok := elem.Value.(core.FragmentSequence)
					if !ok {
						t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
					}
					if len(value.Fragments) != len(wantFragments) {
						t.Fatalf("fragments = %d, want %d", len(value.Fragments), len(wantFragments))
					}
					for i := range wantFragments {
						if !bytes.Equal(value.Fragments[i], wantFragments[i]) {
							t.Fatalf("fragment %d = %v, want %v", i, value.Fragments[i], wantFragments[i])
						}
					}
				})
			}
		})
	}
}

func TestReadFileAcceptsJPEGBaselineFragmentSequence(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		nil,
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
	)
	data, err := dicomtest.Part10File(transfer.JPEGBaseline, append(dicomtest.MinimalDataset(), pixel)...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := file.TransferSyntax.UID; got != transfer.JPEGBaseline.UID {
		t.Fatalf("transfer syntax = %q, want %q", got, transfer.JPEGBaseline.UID)
	}
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("missing Pixel Data")
	}
	value, ok := elem.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("Pixel Data value = %T, want core.FragmentSequence", elem.Value)
	}
	if len(value.Fragments) != 1 {
		t.Fatalf("fragment count = %d, want 1", len(value.Fragments))
	}
}

func TestReadFileOptionsSkipProcessingPixelDataValueCompatibilityNoOp(t *testing.T) {
	pixel := sequentialFrameBytes(4)
	elements := append(dicomtest.MinimalDataset(), nativeFrameStreamingElements(pixel, "1")...)
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		SkipProcessingPixelDataValue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := file.Dataset.GetRaw(core.TagPixelData)
	if !ok {
		t.Fatal("Pixel Data was not materialized as raw bytes")
	}
	if !bytes.Equal(raw, pixel) {
		t.Fatalf("Pixel Data = % X, want % X", raw, pixel)
	}
}

func TestReadFileOptionsAllowMismatchPixelDataLengthCompatibilityNoOp(t *testing.T) {
	pixel := sequentialFrameBytes(6) // metadata below describes one 2x2 8-bit frame, which is 4 bytes.
	elements := append(dicomtest.MinimalDataset(), nativeFrameStreamingElements(pixel, "1")...)
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		allow bool
	}{
		{name: "false", allow: false},
		{name: "true", allow: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
				AllowMismatchPixelDataLength: tt.allow,
			})
			if err != nil {
				t.Fatal(err)
			}
			raw, ok := file.Dataset.GetRaw(core.TagPixelData)
			if !ok {
				t.Fatal("Pixel Data was not materialized as raw bytes")
			}
			if !bytes.Equal(raw, pixel) {
				t.Fatalf("Pixel Data = % X, want % X", raw, pixel)
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
			name: "encapsulated_pixeldata_preserves_metadata_readable_codec_syntax",
			run: func(t *testing.T) {
				offsetTable := []byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00}
				pixel := dicomtest.NewFragmentSequenceElement(
					core.TagPixelData,
					offsetTable,
					[]byte{0xFF, 0x4F, 0xFF, 0x51},
					[]byte{0x00, 0x01, 0xFF, 0xD9},
				)
				data, err := dicomtest.Part10File(transfer.HTJ2KLossless, append(dicomtest.MinimalDataset(), pixel)...)
				if err != nil {
					t.Fatal(err)
				}

				file, err := ReadFile(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if got := file.TransferSyntax.UID; got != transfer.HTJ2KLossless.UID {
					t.Fatalf("transfer syntax = %q, want %q", got, transfer.HTJ2KLossless.UID)
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
func TestReadDataSetAcceptsOptionalStillImageCodecFragmentSequence(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
	)
	data := dicomtest.EncodeElements(transfer.JPEGLSLossless, append(dicomtest.MinimalDataset(), pixel)...)

	obj, err := ReadDataSet(bytes.NewReader(data), transfer.JPEGLSLossless)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.Get(core.TagPixelData); !ok {
		t.Fatal("missing Pixel Data")
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

func TestReadDataSetWithOptionsMaterializesValueFromNonSeekableSource(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	want := []byte{0x01, 0x02, 0x03, 0x04}
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewOBElement(tag, want),
	)

	obj, err := ReadDataSetWithOptions(bytes.NewBuffer(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{
		InlineValueBytesThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.deferredCount != 0 {
		t.Fatalf("deferredCount = %d, want 0 for non-seekable ReadDataSetWithOptions source", obj.deferredCount)
	}
	elem, ok := obj.Get(tag)
	if !ok {
		t.Fatalf("missing element %s", tag)
	}
	if elem.Value == nil {
		t.Fatalf("expected %s to be materialized for non-seekable ReadDataSetWithOptions source", tag)
	}

	var got bytes.Buffer
	n, err := obj.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}
}

func TestReadDataSetWithOptionsCopyValueToUsesRecordedOffset(t *testing.T) {
	tag := core.NewTag(0x7777, 0x0010)
	prefix := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "BEFORE")
	want := []byte("OFFSET-AWARE-VALUE-BYTES")
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		prefix,
		dicomtest.NewOBElement(tag, want),
	)
	valueOffset := bytes.Index(data, want)
	if valueOffset < 0 {
		t.Fatal("test fixture does not contain deferred value bytes")
	}
	source := &guardedReadSeeker{r: bytes.NewReader(data)}

	obj, err := ReadDataSetWithOptions(source, transfer.ExplicitVRLittleEndian, ReadFileOptions{
		InlineValueBytesThreshold: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	elem, ok := obj.Get(tag)
	if !ok {
		t.Fatalf("missing element %s", tag)
	}
	if elem.Value != nil {
		t.Fatalf("expected deferred nil value for %s before CopyValueTo, got %T", tag, elem.Value)
	}

	source.guard = true
	source.minRead = int64(valueOffset)
	var got bytes.Buffer
	n, err := obj.CopyValueTo(tag, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
	}
}

func TestReadFileWithOptionsDeferredPixelDataCopyValueToUsesRecordedOffset(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xAA, 0xBB, 0xCC, 0xDD},
	)
	data, err := dicomtest.Part10File(transfer.JPEGBaseline, append(dicomtest.MinimalDataset(), pixel)...)
	if err != nil {
		t.Fatal(err)
	}
	encodedPixel := dicomtest.EncodeElement(pixel, transfer.JPEGBaseline)
	const pixelHeaderLength = 12
	want := encodedPixel[pixelHeaderLength:]
	valueOffset := bytes.Index(data, want)
	if valueOffset < 0 {
		t.Fatal("test fixture does not contain encoded Pixel Data value bytes")
	}
	source := &guardedReadSeeker{r: bytes.NewReader(data)}

	file, err := ReadFileWithOptions(source, ReadFileOptions{
		DeferPixelData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("missing Pixel Data")
	}
	if elem.Value != nil {
		t.Fatalf("expected deferred nil Pixel Data before CopyValueTo, got %T", elem.Value)
	}

	source.guard = true
	source.minRead = int64(valueOffset)
	var got bytes.Buffer
	n, err := file.Dataset.CopyValueTo(core.TagPixelData, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(want), want)
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

func TestReadFileWithOptionsDefersNestedWaveformDataAndExposesLocations(t *testing.T) {
	waveformSequenceTag := core.NewTag(0x5400, 0x0100)
	waveformDataTag := core.NewTag(0x5400, 0x1010)
	groupData := [][]byte{
		{0x01, 0x00, 0x02, 0x00},
		{0x10, 0x00, 0x20, 0x00, 0x30, 0x00},
	}
	elements := append([]core.Element{}, dicomtest.MinimalDataset()...)
	elements = append(elements, dicomtest.NewSequenceElement(
		waveformSequenceTag,
		core.DataSet{Elements: []core.Element{
			core.NewRawElement(waveformDataTag, core.VROW, groupData[0]),
		}},
		core.DataSet{Elements: []core.Element{
			core.NewRawElement(waveformDataTag, core.VROW, groupData[1]),
		}},
	))
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		DeferWaveformData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := file.Dataset.GetSequence(waveformSequenceTag)
	if !ok || len(items) != len(groupData) {
		t.Fatalf("WaveformSequence items = %d, ok=%v; want %d", len(items), ok, len(groupData))
	}
	for index, item := range items {
		elem, ok := item.Get(waveformDataTag)
		if !ok {
			t.Fatalf("item %d is missing WaveformData", index)
		}
		if elem.Value != nil {
			t.Fatalf("item %d WaveformData value = %T, want deferred nil", index, elem.Value)
		}
	}

	locations := file.ValueLocations(waveformDataTag)
	if len(locations) != len(groupData) {
		t.Fatalf("ValueLocations count = %d, want %d", len(locations), len(groupData))
	}
	for index, location := range locations {
		if location.Tag != waveformDataTag {
			t.Fatalf("location %d tag = %s, want %s", index, location.Tag, waveformDataTag)
		}
		if location.Length != int64(len(groupData[index])) {
			t.Fatalf("location %d length = %d, want %d", index, location.Length, len(groupData[index]))
		}
		itemOffset, itemOffsetSet := items[index].ItemOffset()
		if !location.ItemOffsetSet || !itemOffsetSet || location.ItemOffset != itemOffset {
			t.Fatalf(
				"location %d item offset = %d/%v, want %d/%v",
				index,
				location.ItemOffset,
				location.ItemOffsetSet,
				itemOffset,
				itemOffsetSet,
			)
		}
		end := location.ValueOffset + location.Length
		if location.ValueOffset < 0 || end > int64(len(data)) {
			t.Fatalf("location %d = %#v outside Part 10 source length %d", index, location, len(data))
		}
		if got := data[location.ValueOffset:end]; !bytes.Equal(got, groupData[index]) {
			t.Fatalf("source bytes at location %d = % X, want % X", index, got, groupData[index])
		}
	}

	locations[0].Length = 0
	fresh := file.ValueLocations(waveformDataTag)
	if fresh[0].Length != int64(len(groupData[0])) {
		t.Fatalf("File.ValueLocations returned aliased storage: first length = %d", fresh[0].Length)
	}
	var output bytes.Buffer
	if err := WriteFile(&output, file); !errors.Is(err, ErrDeferredSequenceValueWrite) {
		t.Fatalf("WriteFile() deferred nested value error = %v, want ErrDeferredSequenceValueWrite", err)
	}
}

func TestReadOptionsRejectDeferredWaveformDataFromNonSeekableSource(t *testing.T) {
	waveformDataTag := core.NewTag(0x5400, 0x1010)
	dataset := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		core.NewRawElement(waveformDataTag, core.VROW, []byte{0x01, 0x00}),
	)
	part10, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(
			append([]core.Element{}, dicomtest.MinimalDataset()...),
			core.NewRawElement(waveformDataTag, core.VROW, []byte{0x01, 0x00}),
		)...,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("dataset", func(t *testing.T) {
		_, err := ReadDataSetWithOptions(
			bytes.NewBuffer(dataset),
			transfer.ExplicitVRLittleEndian,
			ReadFileOptions{DeferWaveformData: true},
		)
		if !errors.Is(err, ErrDeferredValueRequiresSeekable) {
			t.Fatalf("ReadDataSetWithOptions() error = %v, want ErrDeferredValueRequiresSeekable", err)
		}
	})
	t.Run("part 10", func(t *testing.T) {
		_, err := ReadFileWithOptions(
			bytes.NewBuffer(part10),
			ReadFileOptions{DeferWaveformData: true},
		)
		if !errors.Is(err, ErrDeferredValueRequiresSeekable) {
			t.Fatalf("ReadFileWithOptions() error = %v, want ErrDeferredValueRequiresSeekable", err)
		}
	})
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
func TestReadDataSetInflatesDeflatedExplicitVRLittleEndian(t *testing.T) {
	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	data := deflateBytes(t, dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, want.Elements...))

	got, err := ReadDataSet(bytes.NewReader(data), transfer.DeflatedExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if diff := dicomtest.DiffDataSet(got.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after inflated read:\n%s", diff)
	}
}
func TestReadDataSetRejectsUnsupportedDeflatedVariant(t *testing.T) {
	data := deflateBytes(t, dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...))

	_, err := ReadDataSet(bytes.NewReader(data), transfer.DeflatedImageFrameCompression)
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}
func TestReadDataSetInflatesJPIPReferencedDeflateMetadata(t *testing.T) {
	const providerURL = "https://jpip.example/metadata-only.jp2"
	data := deflateBytes(t, dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		jpipReferencedDataset(providerURL)...,
	))

	got, err := ReadDataSet(bytes.NewReader(data), transfer.JPIPReferencedDeflate)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := got.GetString(tagPixelDataProviderURL); !ok || value != providerURL {
		t.Fatalf("PixelDataProviderURL = %q ok=%v, want %q", value, ok, providerURL)
	}
	if _, ok := got.Get(core.TagPixelData); ok {
		t.Fatal("JPIP referenced metadata dataset should not contain Pixel Data")
	}
}
func TestReadDataSetRejectsDeferredPixelDataForDeflatedSyntax(t *testing.T) {
	pixel := dicomtest.NewOBElement(core.TagPixelData, []byte{0x01, 0x02, 0x03, 0x04})
	data := deflateBytes(t, dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, append(dicomtest.MinimalDataset(), pixel)...))

	_, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.DeflatedExplicitVRLittleEndian, ReadFileOptions{
		DeferPixelData: true,
	})
	if !errors.Is(err, ErrDeferredValueRequiresSeekable) {
		t.Fatalf("expected ErrDeferredValueRequiresSeekable, got %v", err)
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

func TestReadFileWithOptionsRespectsInitialReaderOffset(t *testing.T) {
	tag := core.NewTag(0x7777, 0x0010)
	want := bytes.Repeat([]byte{0xCD}, 128)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(tag, want))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	prefix := bytes.Repeat([]byte{0xEE}, 37)
	container := append(append([]byte(nil), prefix...), data...)
	source := bytes.NewReader(container)
	if _, err := source.Seek(int64(len(prefix)), io.SeekStart); err != nil {
		t.Fatal(err)
	}

	file, err := ReadFileWithOptions(source, ReadFileOptions{
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
	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("PatientName = %q ok=%v, want TEST^PATIENT true", got, ok)
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

func TestReadDataSetWithOptionsDefersEncapsulatedPixelData(t *testing.T) {
	syntax := transfer.EncapsulatedUncompressedExplicitVRLittleEndian
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		bytes.Repeat([]byte{0xAB}, 128),
		[]byte{0x01, 0x02, 0x03, 0x00},
	)
	data := dicomtest.EncodeElement(pixel, syntax)
	wantValue := encodedPixelValueBytes(t, pixel, syntax)

	obj, err := ReadDataSetWithOptions(bytes.NewReader(data), syntax, ReadFileOptions{
		DeferPixelData: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	elem, ok := obj.Get(core.TagPixelData)
	if !ok {
		t.Fatal("missing Pixel Data")
	}
	if elem.Value != nil {
		t.Fatalf("Pixel Data value = %T, want deferred nil", elem.Value)
	}

	var got bytes.Buffer
	n, err := obj.CopyValueTo(core.TagPixelData, &got)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(wantValue)) || !bytes.Equal(got.Bytes(), wantValue) {
		t.Fatalf("CopyValueTo copied %d bytes % X, want %d bytes % X", n, got.Bytes(), len(wantValue), wantValue)
	}
}

func TestReadDataSetWithOptionsDefersFloatPixelData(t *testing.T) {
	for _, test := range []struct {
		name string
		tag  core.Tag
		vr   core.VR
	}{
		{name: "float", tag: core.NewTag(0x7FE0, 0x0008), vr: core.VROF},
		{name: "double", tag: core.NewTag(0x7FE0, 0x0009), vr: core.VROD},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := bytes.Repeat([]byte{0xAB}, 64)
			data := dicomtest.EncodeElement(core.NewRawElement(test.tag, test.vr, want), transfer.ExplicitVRLittleEndian)
			obj, err := ReadDataSetWithOptions(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReadFileOptions{DeferPixelData: true})
			if err != nil {
				t.Fatal(err)
			}
			element, ok := obj.Get(test.tag)
			if !ok || element.Value != nil {
				t.Fatalf("deferred element = %+v ok=%v", element, ok)
			}
			var got bytes.Buffer
			count, err := obj.CopyValueTo(test.tag, &got)
			if err != nil {
				t.Fatal(err)
			}
			if count != int64(len(want)) || !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("CopyValueTo = %d bytes % X", count, got.Bytes())
			}
		})
	}
}

func TestReadDataSetWithOptionsRejectsDeferredPixelDataFromNonSeekableSource(t *testing.T) {
	syntax := transfer.EncapsulatedUncompressedExplicitVRLittleEndian
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		nil,
		[]byte{0x01, 0x02, 0x03, 0x00},
	)
	data := dicomtest.EncodeElement(pixel, syntax)

	_, err := ReadDataSetWithOptions(bytes.NewBuffer(data), syntax, ReadFileOptions{
		DeferPixelData: true,
	})
	if !errors.Is(err, ErrDeferredValueRequiresSeekable) {
		t.Fatalf("ReadDataSetWithOptions() error = %v, want ErrDeferredValueRequiresSeekable", err)
	}
}

func TestWriteFileRoundTripDeferredEncapsulatedPixelData(t *testing.T) {
	syntax := transfer.EncapsulatedUncompressedExplicitVRLittleEndian
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		bytes.Repeat([]byte{0xCD}, 96),
		[]byte{0x10, 0x11, 0x12, 0x00},
	)
	inputDataSet := append(dicomtest.MinimalDataset(), pixel)
	data, err := dicomtest.Part10File(syntax, inputDataSet...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lazy-encapsulated.dcm")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := OpenFileWithOptions(path, ReadFileOptions{
		DeferPixelData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("missing Pixel Data")
	}
	if elem.Value != nil {
		t.Fatalf("Pixel Data value = %T, want deferred nil", elem.Value)
	}

	var out bytes.Buffer
	if err := WriteFile(&out, file); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	wantDataSet := core.DataSet{Elements: sortedElementsForTest(inputDataSet)}
	if diff := dicomtest.DiffDataSet(got.Dataset.ToDataSet(), wantDataSet); diff != "" {
		t.Fatalf("dataset mismatch after deferred Pixel Data round-trip:\n%s", diff)
	}
}

func TestReadDataSetWithOptionsStreamsNativePixelDataFrames(t *testing.T) {
	pixel := sequentialFrameBytes(12)
	trailingPatientName := core.NewTag(0x0010, 0x0010)
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		append(nativeFrameStreamingElements(pixel, "3"), dicomtest.NewPNElement(trailingPatientName, "AFTER"))...,
	)
	source := &frameTrackingReader{r: bytes.NewReader(data)}
	var firstFrameReadAt int64
	sink := &recordingFrameSink{
		onFrame: func(frame parser.Frame) error {
			if frame.Index == 0 {
				firstFrameReadAt = source.read
			}
			return nil
		},
	}

	obj, err := ReadDataSetWithOptions(source, transfer.ExplicitVRLittleEndian, ReadFileOptions{
		FrameSink: sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstFrameReadAt <= 0 || firstFrameReadAt >= int64(len(data)) {
		t.Fatalf("first frame was emitted after reading %d byte(s), total stream %d", firstFrameReadAt, len(data))
	}
	if sink.closeCount != 1 {
		t.Fatalf("FrameSink.Close count = %d, want 1", sink.closeCount)
	}
	if len(sink.frames) != 3 {
		t.Fatalf("frame count = %d, want 3", len(sink.frames))
	}
	for i, frame := range sink.frames {
		start := i * 4
		if !bytes.Equal(frame.Data, pixel[start:start+4]) {
			t.Fatalf("frame %d data = % X, want % X", i, frame.Data, pixel[start:start+4])
		}
		if frame.Index != i {
			t.Fatalf("frame index = %d, want %d", frame.Index, i)
		}
		if frame.Metadata.Rows != 2 || frame.Metadata.Columns != 2 || frame.Metadata.NumberOfFrames != 3 {
			t.Fatalf("frame metadata = %#v, want rows=2 columns=2 frames=3", frame.Metadata)
		}
		if frame.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
			t.Fatalf("frame transfer syntax = %q, want %q", frame.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
		}
	}
	elem, ok := obj.Get(core.TagPixelData)
	if !ok {
		t.Fatal("missing Pixel Data")
	}
	if elem.Value != nil {
		t.Fatalf("Pixel Data value = %T, want nil after streaming", elem.Value)
	}
	if got, ok := obj.GetString(trailingPatientName); !ok || got != "AFTER" {
		t.Fatalf("trailing PatientName = %q, %v; want AFTER, true", got, ok)
	}
}

func TestReadDataSetWithOptionsClosesFrameChannelSink(t *testing.T) {
	pixel := sequentialFrameBytes(12)
	ch := make(chan Frame, 3)

	obj, err := ReadDataSetWithOptions(
		bytes.NewReader(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, nativeFrameStreamingElements(pixel, "3")...)),
		transfer.ExplicitVRLittleEndian,
		ReadFileOptions{FrameSink: NewFrameChannelSink(ch)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if obj == nil {
		t.Fatal("ReadDataSetWithOptions() returned nil object")
	}

	var frames []Frame
	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				if len(frames) != 3 {
					t.Fatalf("closed frame channel after %d frame(s), want 3", len(frames))
				}
				return
			}
			frames = append(frames, frame)
		default:
			t.Fatal("frame channel was not closed after ReadDataSetWithOptions returned")
		}
	}
}

func TestReadDataSetWithOptionsFrameSinkConsumerErrorClosesOnce(t *testing.T) {
	consumerErr := errors.New("consumer stopped")
	pixel := sequentialFrameBytes(12)
	trailingPatientName := core.NewTag(0x0010, 0x0010)
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		append(nativeFrameStreamingElements(pixel, "3"), dicomtest.NewPNElement(trailingPatientName, "AFTER"))...,
	)
	source := &frameTrackingReader{r: bytes.NewReader(data)}
	sink := &recordingFrameSink{
		onFrame: func(frame parser.Frame) error {
			if frame.Index == 1 {
				return consumerErr
			}
			return nil
		},
	}

	_, err := ReadDataSetWithOptions(source, transfer.ExplicitVRLittleEndian, ReadFileOptions{
		FrameSink: sink,
	})
	if !errors.Is(err, parser.ErrFrameSink) {
		t.Fatalf("ReadDataSetWithOptions() error = %v, want ErrFrameSink", err)
	}
	if !errors.Is(err, consumerErr) {
		t.Fatalf("ReadDataSetWithOptions() error = %v, want consumer error", err)
	}
	if sink.closeCount != 1 {
		t.Fatalf("FrameSink.Close count = %d, want 1", sink.closeCount)
	}
	if len(sink.frames) != 2 {
		t.Fatalf("frame count before error = %d, want 2", len(sink.frames))
	}
	if source.read >= int64(len(data)) {
		t.Fatalf("consumer error did not interrupt read: consumed %d byte(s), total %d", source.read, len(data))
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

func encodedPixelValueBytes(t *testing.T, elem core.Element, syntax transfer.Syntax) []byte {
	t.Helper()
	encoded := dicomtest.EncodeElement(elem, syntax)
	headerLen := 8
	if syntax.ExplicitVR {
		headerLen = len(dicomtest.ExplicitLongHeaderBytes(syntax.ByteOrder, elem.Tag(), elem.VR(), uint32(core.UndefinedLength)))
	}
	if len(encoded) < headerLen {
		t.Fatalf("encoded element len = %d, want at least header len %d", len(encoded), headerLen)
	}
	return encoded[headerLen:]
}

type frameTrackingReader struct {
	r    *bytes.Reader
	read int64
}

func (r *frameTrackingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.read += int64(n)
	return n, err
}

type recordingFrameSink struct {
	frames     []parser.Frame
	closeCount int
	onFrame    func(parser.Frame) error
}

func (s *recordingFrameSink) HandleFrame(frame parser.Frame) error {
	s.frames = append(s.frames, frame)
	if s.onFrame == nil {
		return nil
	}
	return s.onFrame(frame)
}

func (s *recordingFrameSink) Close() error {
	s.closeCount++
	return nil
}

var errGuardedReadBeforeOffset = errors.New("guarded read before recorded value offset")

type guardedReadSeeker struct {
	r       *bytes.Reader
	guard   bool
	minRead int64
}

func (s *guardedReadSeeker) Read(p []byte) (int, error) {
	if s.guard {
		pos, err := s.r.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		if pos < s.minRead {
			return 0, errGuardedReadBeforeOffset
		}
	}
	return s.r.Read(p)
}

func (s *guardedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return s.r.Seek(offset, whence)
}

func nativeFrameStreamingElements(pixel []byte, numberOfFrames string) []core.Element {
	return []core.Element{
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, binary.LittleEndian, 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, numberOfFrames),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, binary.LittleEndian, 2),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, binary.LittleEndian, 2),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, binary.LittleEndian, 8),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, binary.LittleEndian, 8),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, binary.LittleEndian, 7),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, binary.LittleEndian, 0),
		dicomtest.NewOBElement(core.TagPixelData, pixel),
	}
}

func sequentialFrameBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}
