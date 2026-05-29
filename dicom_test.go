package dicom

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadFileWithOptionsDelegatesToObjectLayer(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(core.TagPixelData, []byte{0x01, 0x02, 0x03, 0x04}))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		MaxElementBytes: 2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, parser.ErrMaxElementBytesExceeded) {
		t.Fatalf("expected ErrMaxElementBytesExceeded, got %v", err)
	}
}

func TestParseCompatibilityFacadeReadsExplicitLittleEndianPart10(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}

	dataset, err := Parse(bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		t.Fatal(err)
	}

	requireCompatibilityDataset(t, dataset, transfer.ExplicitVRLittleEndian.UID, true)
}

func TestParseCompatibilityFacadeReadsImplicitLittleEndianPart10(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}

	dataset, err := Parse(bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		t.Fatal(err)
	}

	requireCompatibilityDataset(t, dataset, transfer.ImplicitVRLittleEndian.UID, false)
}

func TestParseCompatibilityFacadeReadsPart10WithoutPixelData(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}

	dataset, err := Parse(bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		t.Fatal(err)
	}

	requireCompatibilityDataset(t, dataset, transfer.ExplicitVRLittleEndian.UID, false)
}

func TestParseUntilEOFCompatibilityFacadeReadsUnknownSizeReader(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}

	dataset, err := ParseUntilEOF(bytes.NewBuffer(data), nil)
	if err != nil {
		t.Fatal(err)
	}

	requireCompatibilityDataset(t, dataset, transfer.ExplicitVRLittleEndian.UID, false)
}

func TestParseFileCompatibilityFacadeReadsPart10Path(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	dataset, err := ParseFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}

	requireCompatibilityDataset(t, dataset, transfer.ExplicitVRLittleEndian.UID, false)
}

func TestParseCompatibilityFacadesStreamFrameChannel(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "frames.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		read func(chan Frame) (Dataset, error)
	}{
		{
			name: "Parse",
			read: func(ch chan Frame) (Dataset, error) {
				return Parse(bytes.NewReader(data), int64(len(data)), ch)
			},
		},
		{
			name: "ParseUntilEOF",
			read: func(ch chan Frame) (Dataset, error) {
				return ParseUntilEOF(bytes.NewReader(data), ch)
			},
		},
		{
			name: "ParseFile",
			read: func(ch chan Frame) (Dataset, error) {
				return ParseFile(path, ch)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan Frame, 1)
			dataset, err := tt.read(ch)
			if err != nil {
				t.Fatal(err)
			}

			var frames []Frame
			for frame := range ch {
				frames = append(frames, frame)
			}
			if len(frames) != 1 {
				t.Fatalf("frame count = %d, want 1", len(frames))
			}
			if frames[0].Metadata.Rows != 8 || frames[0].Metadata.Columns != 8 {
				t.Fatalf("frame metadata = %#v, want rows=8 columns=8", frames[0].Metadata)
			}
			if got := frames[0].TransferSyntax.UID; got != transfer.ExplicitVRLittleEndian.UID {
				t.Fatalf("frame transfer syntax = %q, want %q", got, transfer.ExplicitVRLittleEndian.UID)
			}
			elem, ok := datasetElement(dataset, core.TagPixelData)
			if !ok {
				t.Fatal("PixelData element missing")
			}
			if elem.Value != nil {
				t.Fatalf("PixelData value type = %T, want nil streamed value", elem.Value)
			}
		})
	}
}

func TestParseCompatibilityFacadeRejectsUnsupportedFrameChannelType(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.dcm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		read func() error
	}{
		{
			name: "Parse",
			read: func() error {
				_, err := Parse(bytes.NewReader(data), int64(len(data)), make(chan struct{}))
				return err
			},
		},
		{
			name: "ParseUntilEOF",
			read: func() error {
				_, err := ParseUntilEOF(bytes.NewReader(data), make(chan struct{}))
				return err
			},
		},
		{
			name: "ParseFile",
			read: func() error {
				_, err := ParseFile(path, make(chan struct{}))
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.read()
			if !errors.Is(err, ErrFrameChannelUnsupported) {
				t.Fatalf("expected ErrFrameChannelUnsupported, got %v", err)
			}
		})
	}
}

func TestParseOptionSkipPixelDataAvoidsMaterializingValue(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}

	dataset, err := Parse(bytes.NewReader(data), int64(len(data)), nil, SkipPixelData())
	if err != nil {
		t.Fatal(err)
	}
	elem, ok := datasetElement(dataset, core.TagPixelData)
	if !ok {
		t.Fatal("PixelData element missing")
	}
	if elem.Value != nil {
		t.Fatalf("PixelData value type = %T, want nil skipped value", elem.Value)
	}

	defaultDataset, err := Parse(bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defaultElem, ok := datasetElement(defaultDataset, core.TagPixelData)
	if !ok {
		t.Fatal("default PixelData element missing")
	}
	raw, ok := defaultElem.RawBytes()
	if !ok || len(raw) == 0 {
		t.Fatalf("default PixelData raw bytes len = %d ok=%v, want materialized raw value", len(raw), ok)
	}
}

func TestSkipProcessingPixelDataValuePreservesWritableBytes(t *testing.T) {
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...)
	if err != nil {
		t.Fatal(err)
	}
	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		SkipProcessingPixelDataValue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := pixelDataRaw(t, file)

	var out bytes.Buffer
	if err := object.WriteFile(&out, file); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadFileWithOptions(bytes.NewReader(out.Bytes()), ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := pixelDataRaw(t, roundTrip)
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip PixelData bytes differ: got %d bytes, want %d", len(got), len(want))
	}
}

func TestAllowUnknownSpecificCharacterSetUsesFallback(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append([]core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0008, 0x0005), core.VRCS, "UNKNOWN"),
		}, dicomtest.MinimalDataset()...)...,
	)
	if err != nil {
		t.Fatal(err)
	}

	file, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Dataset.LookupString(core.NewTag(0x0010, 0x0010)); err == nil {
		t.Fatal("expected unsupported charset error without fallback")
	}

	opts := parseReadFileOptions([]ParseOption{AllowUnknownSpecificCharacterSet()})
	file, err = ReadFileWithOptions(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := file.Dataset.LookupString(core.NewTag(0x0010, 0x0010))
	if err != nil {
		t.Fatal(err)
	}
	if got != "TEST^PATIENT" {
		t.Fatalf("PatientName = %q, want TEST^PATIENT", got)
	}
}

func TestAllowMissingMetaElementGroupLengthReadsGroupTwoMeta(t *testing.T) {
	data := part10WithoutFileMetaGroupLength(t, transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)

	_, err := ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{})
	if !errors.Is(err, object.ErrInvalidFileMetaGroupLength) {
		t.Fatalf("expected ErrInvalidFileMetaGroupLength without option, got %v", err)
	}

	opts := parseReadFileOptions([]ParseOption{AllowMissingMetaElementGroupLength()})
	file, err := ReadFileWithOptions(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := file.TransferSyntax.UID; got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntaxUID = %q, want %q", got, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("PatientName = %q ok=%v, want TEST^PATIENT", got, ok)
	}
}

func TestParseOptionsSetCompatibilityFlags(t *testing.T) {
	opts := parseReadFileOptions([]ParseOption{
		AllowMismatchPixelDataLength(),
		SkipMetadataReadOnNewParserInit(),
		SkipProcessingPixelDataValue(),
	})
	if !opts.AllowMismatchPixelDataLength {
		t.Fatal("AllowMismatchPixelDataLength did not set ReadFileOptions flag")
	}
	if !opts.SkipMetadataReadOnNewParserInit {
		t.Fatal("SkipMetadataReadOnNewParserInit did not set ReadFileOptions flag")
	}
	if !opts.SkipProcessingPixelDataValue {
		t.Fatal("SkipProcessingPixelDataValue did not set ReadFileOptions flag")
	}
}

func requireCompatibilityDataset(t *testing.T, dataset Dataset, wantTransferSyntaxUID string, wantPixelData bool) {
	t.Helper()
	if len(dataset.Elements) == 0 {
		t.Fatal("compatibility dataset has no elements")
	}

	if got, ok := datasetStringValue(dataset, core.NewTag(0x0002, 0x0010)); !ok || got != wantTransferSyntaxUID {
		t.Fatalf("TransferSyntaxUID = %q ok=%v, want %q", got, ok, wantTransferSyntaxUID)
	}
	if got, ok := datasetStringValue(dataset, core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("PatientName = %q ok=%v, want TEST^PATIENT", got, ok)
	}

	_, hasPixelData := datasetElement(dataset, core.TagPixelData)
	if hasPixelData != wantPixelData {
		t.Fatalf("PixelData present = %v, want %v", hasPixelData, wantPixelData)
	}
}

func datasetStringValue(dataset Dataset, tag core.Tag) (string, bool) {
	elem, ok := datasetElement(dataset, tag)
	if !ok {
		return "", false
	}
	return elem.StringValue(), true
}

func datasetElement(dataset Dataset, tag core.Tag) (Element, bool) {
	for _, elem := range dataset.Elements {
		if elem.Tag() == tag {
			return elem, true
		}
	}
	return Element{}, false
}

func pixelDataRaw(t *testing.T, file *File) []byte {
	t.Helper()
	elem, ok := file.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("PixelData element missing")
	}
	raw, ok := elem.RawBytes()
	if !ok {
		t.Fatalf("PixelData value type = %T, want raw bytes", elem.Value)
	}
	return raw
}

func part10WithoutFileMetaGroupLength(t *testing.T, syntax transfer.Syntax, dataset ...core.Element) []byte {
	t.Helper()
	meta := dicomtest.NewFileMetaBuilder().WithTransferSyntax(syntax.UID).Build()
	if len(meta) == 0 || meta[0].Tag() != core.NewTag(0x0002, 0x0000) {
		t.Fatal("test file meta builder did not produce group length first")
	}
	meta = meta[1:]

	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, meta...))
	buf.Write(dicomtest.EncodeElements(syntax, dataset...))
	return buf.Bytes()
}
