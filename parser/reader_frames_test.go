package parser

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestFrameSinkDoesNotInterfereWithEncapsulatedPixelData(t *testing.T) {
	sink := &testFrameSink{}
	reader := NewReader(
		bytes.NewReader(encapsulatedPixelDataBytes(nil, []byte{0x01, 0x02}, []byte{0x03, 0x04})),
		transfer.RLELossless,
		ReaderOptions{FrameSink: sink},
	)

	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if sink.closeCount != 1 {
		t.Fatalf("FrameSink.Close count = %d, want 1", sink.closeCount)
	}
	if len(sink.frames) != 0 {
		t.Fatalf("encapsulated Pixel Data emitted %d frame(s), want 0", len(sink.frames))
	}
	if len(got.Elements) != 1 {
		t.Fatalf("dataset element count = %d, want 1", len(got.Elements))
	}
	value, ok := got.Elements[0].Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("Pixel Data value = %T, want core.FragmentSequence", got.Elements[0].Value)
	}
	if len(value.Fragments) != 2 {
		t.Fatalf("fragment count = %d, want 2", len(value.Fragments))
	}
}

func TestDeferPixelDataIncludesFloatAndDoublePayloads(t *testing.T) {
	for _, test := range []struct {
		name string
		tag  core.Tag
		vr   core.VR
	}{
		{name: "float", tag: core.NewTag(0x7FE0, 0x0008), vr: core.VROF},
		{name: "double", tag: core.NewTag(0x7FE0, 0x0009), vr: core.VROD},
	} {
		t.Run(test.name, func(t *testing.T) {
			trailing := core.NewTag(0x0010, 0x0010)
			stream := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
				core.NewRawElement(test.tag, test.vr, make([]byte, 16)),
				dicomtest.NewPNElement(trailing, "AFTER"),
			)
			reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{DeferPixelData: true})
			dataset, err := reader.ReadDataSet()
			if err != nil {
				t.Fatal(err)
			}
			if dataset.Elements[0].Tag() != test.tag || dataset.Elements[0].Value != nil || dataset.Elements[0].Length() != 16 {
				t.Fatalf("deferred element = %+v", dataset.Elements[0])
			}
			if dataset.Elements[1].StringValue() != "AFTER" {
				t.Fatalf("trailing value = %q, want AFTER", dataset.Elements[1].StringValue())
			}
		})
	}
}

func TestFrameSinkAcceptsPaddedOddLengthNativePixelData(t *testing.T) {
	pixel := make([]byte, 25)
	for i := range pixel {
		pixel[i] = byte(i + 1)
	}
	trailingPatientName := core.NewTag(0x0010, 0x0010)
	stream := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "MONOCHROME2"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 5),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 5),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0100), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0101), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0102), 7),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0103), 0),
		dicomtest.NewOBElement(core.TagPixelData, pixel),
		dicomtest.NewPNElement(trailingPatientName, "AFTER"),
	)
	sink := &testFrameSink{}
	reader := NewReader(
		bytes.NewReader(stream),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{FrameSink: sink},
	)

	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if sink.closeCount != 1 {
		t.Fatalf("FrameSink.Close count = %d, want 1", sink.closeCount)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(sink.frames))
	}
	if !bytes.Equal(sink.frames[0].Data, pixel) {
		t.Fatalf("frame data = % X, want % X", sink.frames[0].Data, pixel)
	}
	if len(sink.frames[0].Data)%2 == 0 {
		t.Fatalf("frame data length = %d, want odd unpadded payload", len(sink.frames[0].Data))
	}
	var pixelElem core.Element
	var foundPixel bool
	var trailing string
	var foundTrailing bool
	for _, elem := range got.Elements {
		switch elem.Tag() {
		case core.TagPixelData:
			pixelElem = elem
			foundPixel = true
		case trailingPatientName:
			trailing = elem.StringValue()
			foundTrailing = true
		}
	}
	if !foundPixel {
		t.Fatal("missing Pixel Data")
	}
	if pixelElem.Value != nil {
		t.Fatalf("Pixel Data value = %T, want nil after streaming", pixelElem.Value)
	}
	if !foundTrailing || trailing != "AFTER" {
		t.Fatalf("trailing PatientName = %q ok=%v, want AFTER true", trailing, foundTrailing)
	}
}

func TestFrameSinkUsesPackedFrameSizeForOneBitNativePixelData(t *testing.T) {
	pixel := []byte{0b10101010, 0b10000000}
	stream := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "MONOCHROME2"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 1),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 9),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0100), 1),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0101), 1),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0102), 0),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0103), 0),
		dicomtest.NewOBElement(core.TagPixelData, pixel),
	)
	sink := &testFrameSink{}
	reader := NewReader(
		bytes.NewReader(stream),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{FrameSink: sink},
	)

	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(sink.frames))
	}
	if got, want := sink.frames[0].Metadata.FrameSize(), int64(2); got != want {
		t.Fatalf("FrameSize = %d, want %d", got, want)
	}
	if !bytes.Equal(sink.frames[0].Data, pixel) {
		t.Fatalf("frame data = %08b, want %08b", sink.frames[0].Data, pixel)
	}
}

func TestFrameSinkUsesSubsampledYBRFull422FrameSize(t *testing.T) {
	pixel := []byte{10, 20, 30, 40, 128, 128, 128, 128}
	stream := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 3),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "YBR_FULL_422"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 2),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 2),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0100), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0101), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0102), 7),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0103), 0),
		dicomtest.NewOBElement(core.TagPixelData, pixel),
	)
	sink := &testFrameSink{}
	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{FrameSink: sink})

	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(sink.frames))
	}
	if got, want := sink.frames[0].Metadata.FrameSize(), int64(len(pixel)); got != want {
		t.Fatalf("FrameSize = %d, want %d", got, want)
	}
	if !bytes.Equal(sink.frames[0].Data, pixel) {
		t.Fatalf("frame data = % X, want % X", sink.frames[0].Data, pixel)
	}
}

func TestFrameMetadataTotalSizeRejectsOverflow(t *testing.T) {
	metadata := FrameMetadata{
		Rows:            math.MaxUint16,
		Columns:         math.MaxUint16,
		SamplesPerPixel: math.MaxUint16,
		BitsAllocated:   16,
		NumberOfFrames:  math.MaxInt32,
	}
	if got := metadata.TotalSize(); got != 0 {
		t.Fatalf("TotalSize = %d, want 0 for overflow", got)
	}
}

func TestFrameSinkRejectsExcessiveNumberOfFrames(t *testing.T) {
	stream := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "2147483648"),
	)
	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{FrameSink: &testFrameSink{}})

	_, err := reader.ReadDataSet()
	if !errors.Is(err, ErrInvalidFrameMetadata) {
		t.Fatalf("ReadDataSet() error = %v, want ErrInvalidFrameMetadata", err)
	}
}

func TestFrameSinkCapturesPlanarConfigurationMetadata(t *testing.T) {
	stream := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 3),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "RGB"),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0006), 1),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0010), 1),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0011), 2),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0100), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0101), 8),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0102), 7),
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0103), 0),
		dicomtest.NewOBElement(core.TagPixelData, []byte{255, 0, 0, 255, 0, 0}),
	)
	sink := &testFrameSink{}
	reader := NewReader(
		bytes.NewReader(stream),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{FrameSink: sink},
	)

	if _, err := reader.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(sink.frames))
	}
	metadata := sink.frames[0].Metadata
	if !metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 1 {
		t.Fatalf("PlanarConfiguration = %d present=%v, want 1 true", metadata.PlanarConfiguration, metadata.PlanarConfigurationPresent)
	}
}

type testFrameSink struct {
	frames     []Frame
	closeCount int
}

func (s *testFrameSink) HandleFrame(frame Frame) error {
	s.frames = append(s.frames, frame)
	return nil
}

func (s *testFrameSink) Close() error {
	s.closeCount++
	return nil
}
