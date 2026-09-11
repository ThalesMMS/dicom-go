package frame

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestDecodedFrameMetadataNormalizesCodecRGBOutput(t *testing.T) {
	for _, syntax := range []transfer.Syntax{
		transfer.JPEG2000,
		transfer.JPEG2000Part2,
		transfer.HTJ2K,
		transfer.JPEGXL,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			got := decodedFrameMetadata(pixeldata.Metadata{
				SamplesPerPixel:            3,
				PhotometricInterpretation:  "YBR_ICT",
				PlanarConfiguration:        1,
				PlanarConfigurationPresent: true,
			}, syntax.UID)
			if got.PhotometricInterpretation != "RGB" || got.PlanarConfiguration != 0 {
				t.Fatalf("decoded metadata = %+v, want interleaved RGB", got)
			}
		})
	}
}

func TestNativeFrameGetImageUnsigned8Monochrome2(t *testing.T) {
	frame := NewNativeFrame(
		0,
		[]byte{0, 128, 255},
		testMetadata(1, 3, 8, 8, 7, 0, "MONOCHROME2"),
		WithWindow(128, 256),
	)

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray := requireGray(t, img)
	if gray.Pix[0] != 0 {
		t.Fatalf("first pixel = %d, want 0", gray.Pix[0])
	}
	if gray.Pix[1] < 127 || gray.Pix[1] > 128 {
		t.Fatalf("center pixel = %d, want mid gray", gray.Pix[1])
	}
	if gray.Pix[2] != 255 {
		t.Fatalf("last pixel = %d, want 255", gray.Pix[2])
	}
}

func TestNativeFrameGetImageUnsigned16Monochrome2(t *testing.T) {
	data := make([]byte, 6)
	binary.LittleEndian.PutUint16(data[0:2], 0)
	binary.LittleEndian.PutUint16(data[2:4], 2048)
	binary.LittleEndian.PutUint16(data[4:6], 4095)
	frame := NewNativeFrame(
		0,
		data,
		testMetadata(1, 3, 16, 12, 11, 0, "MONOCHROME2"),
		WithWindow(2048, 4096),
	)

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray := requireGray(t, img)
	if gray.Pix[0] != 0 {
		t.Fatalf("first pixel = %d, want 0", gray.Pix[0])
	}
	if gray.Pix[1] < 127 || gray.Pix[1] > 128 {
		t.Fatalf("center pixel = %d, want mid gray", gray.Pix[1])
	}
	if gray.Pix[2] != 255 {
		t.Fatalf("last pixel = %d, want 255", gray.Pix[2])
	}
}

func TestNativeFrameGetImageSigned16AppliesRescale(t *testing.T) {
	data := make([]byte, 4)
	low := int16(-1024)
	mid := int16(0)
	binary.LittleEndian.PutUint16(data[0:2], uint16(low))
	binary.LittleEndian.PutUint16(data[2:4], uint16(mid))
	frame := NewNativeFrame(
		0,
		data,
		testMetadata(1, 2, 16, 16, 15, 1, "MONOCHROME2"),
		WithRescale(1, 1024),
		WithWindow(1024, 2048),
	)

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray := requireGray(t, img)
	if gray.Pix[0] != 0 {
		t.Fatalf("rescaled low pixel = %d, want 0", gray.Pix[0])
	}
	if gray.Pix[1] < 127 || gray.Pix[1] > 128 {
		t.Fatalf("rescaled center pixel = %d, want mid gray", gray.Pix[1])
	}
}

func TestNativeFrameGetImageMonochrome1Inverts(t *testing.T) {
	frame := NewNativeFrame(
		0,
		[]byte{0, 255},
		testMetadata(1, 2, 8, 8, 7, 0, "MONOCHROME1"),
		WithWindow(128, 256),
	)

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray := requireGray(t, img)
	if gray.Pix[0] != 255 {
		t.Fatalf("first pixel = %d, want inverted white", gray.Pix[0])
	}
	if gray.Pix[1] != 0 {
		t.Fatalf("second pixel = %d, want inverted black", gray.Pix[1])
	}
}

func TestNativeFrameGetImageRGB8(t *testing.T) {
	frame := NewNativeFrame(
		0,
		[]byte{
			255, 0, 0,
			0, 255, 0,
			0, 0, 255,
		},
		pixeldata.Metadata{
			Rows:                       1,
			Columns:                    3,
			SamplesPerPixel:            3,
			BitsAllocated:              8,
			BitsStored:                 8,
			HighBit:                    7,
			PixelRepresentation:        0,
			PlanarConfiguration:        0,
			PlanarConfigurationPresent: true,
			NumberOfFrames:             1,
			PhotometricInterpretation:  "RGB",
		},
	)

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	rgba := requireRGBA(t, img)
	for _, tc := range []struct {
		x    int
		want color.RGBA
	}{
		{x: 0, want: color.RGBA{R: 255, A: 255}},
		{x: 1, want: color.RGBA{G: 255, A: 255}},
		{x: 2, want: color.RGBA{B: 255, A: 255}},
	} {
		if got := rgba.RGBAAt(tc.x, 0); got != tc.want {
			t.Fatalf("pixel %d = %#v, want %#v", tc.x, got, tc.want)
		}
	}
}

func TestEncapsulatedFrameUsesDecodedPixelBytes(t *testing.T) {
	frame := NewEncapsulatedFrame(
		0,
		[]byte{0, 255},
		testMetadata(1, 2, 8, 8, 7, 0, "MONOCHROME2"),
		WithWindow(128, 256),
	)
	if !frame.IsEncapsulated() {
		t.Fatal("IsEncapsulated() = false, want true")
	}
	if _, err := frame.GetNativeFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("GetNativeFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray := requireGray(t, img)
	if gray.Pix[0] != 0 || gray.Pix[1] != 255 {
		t.Fatalf("encapsulated decoded pixels = %v, want [0 255]", gray.Pix)
	}
}

func TestFrameAccessorsRejectMissingOrWrongTypes(t *testing.T) {
	var nilFrame *Frame
	if _, err := nilFrame.GetNativeFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil GetNativeFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}
	if _, err := nilFrame.GetEncapsulatedFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil GetEncapsulatedFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}
	if _, err := nilFrame.GetImage(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil GetImage() error = %v, want ErrFrameTypeNotPresent", err)
	}

	nativeMissing := &Frame{}
	if nativeMissing.IsEncapsulated() {
		t.Fatal("zero frame IsEncapsulated() = true, want false")
	}
	if _, err := nativeMissing.GetNativeFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("missing native data error = %v, want ErrFrameTypeNotPresent", err)
	}
	if _, err := nativeMissing.GetEncapsulatedFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("wrong encapsulated accessor error = %v, want ErrFrameTypeNotPresent", err)
	}

	encapsulatedMissing := &Frame{Encapsulated: true}
	if _, err := encapsulatedMissing.GetEncapsulatedFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("missing encapsulated data error = %v, want ErrFrameTypeNotPresent", err)
	}
	if _, err := encapsulatedMissing.GetNativeFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("wrong native accessor error = %v, want ErrFrameTypeNotPresent", err)
	}
}

func TestConcreteFrameAccessorsAndFromDecodedFrames(t *testing.T) {
	metadata := testMetadata(1, 2, 8, 8, 7, 0, "MONOCHROME2")

	native := &NativeFrame{Index: 1, Data: []byte{1, 2}, Metadata: metadata}
	if native.IsEncapsulated() {
		t.Fatal("NativeFrame.IsEncapsulated() = true, want false")
	}
	if got, err := native.GetNativeFrame(); err != nil || got != native {
		t.Fatalf("NativeFrame.GetNativeFrame() = (%p, %v), want self nil", got, err)
	}
	if _, err := native.GetEncapsulatedFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("NativeFrame.GetEncapsulatedFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}
	var nilNative *NativeFrame
	if _, err := nilNative.GetNativeFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil NativeFrame.GetNativeFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}
	if _, err := nilNative.GetImage(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil NativeFrame.GetImage() error = %v, want ErrFrameTypeNotPresent", err)
	}

	encapsulated := &EncapsulatedFrame{Index: 2, Data: []byte{3, 4}, Metadata: metadata}
	if !encapsulated.IsEncapsulated() {
		t.Fatal("EncapsulatedFrame.IsEncapsulated() = false, want true")
	}
	if got, err := encapsulated.GetEncapsulatedFrame(); err != nil || got != encapsulated {
		t.Fatalf("EncapsulatedFrame.GetEncapsulatedFrame() = (%p, %v), want self nil", got, err)
	}
	if _, err := encapsulated.GetNativeFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("EncapsulatedFrame.GetNativeFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}
	var nilEncapsulated *EncapsulatedFrame
	if _, err := nilEncapsulated.GetEncapsulatedFrame(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil EncapsulatedFrame.GetEncapsulatedFrame() error = %v, want ErrFrameTypeNotPresent", err)
	}
	if _, err := nilEncapsulated.GetImage(); !errors.Is(err, ErrFrameTypeNotPresent) {
		t.Fatalf("nil EncapsulatedFrame.GetImage() error = %v, want ErrFrameTypeNotPresent", err)
	}

	nativeFrames := FromDecodedFrames(metadata, [][]byte{{1}, {2}}, false)
	if len(nativeFrames) != 2 {
		t.Fatalf("FromDecodedFrames(native) length = %d, want 2", len(nativeFrames))
	}
	if nativeFrames[1].IsEncapsulated() {
		t.Fatal("FromDecodedFrames(native)[1].IsEncapsulated() = true, want false")
	}
	nativeData, err := nativeFrames[1].GetNativeFrame()
	if err != nil {
		t.Fatalf("FromDecodedFrames(native) GetNativeFrame() error = %v", err)
	}
	if nativeData.Index != 1 || nativeData.Data[0] != 2 {
		t.Fatalf("FromDecodedFrames(native) data = %+v", nativeData)
	}

	encapsulatedFrames := FromDecodedFrames(metadata, [][]byte{{3}}, true, WithByteOrder(binary.BigEndian))
	if len(encapsulatedFrames) != 1 || !encapsulatedFrames[0].IsEncapsulated() {
		t.Fatalf("FromDecodedFrames(encapsulated) = %#v", encapsulatedFrames)
	}
	encapsulatedData, err := encapsulatedFrames[0].GetEncapsulatedFrame()
	if err != nil {
		t.Fatalf("FromDecodedFrames(encapsulated) GetEncapsulatedFrame() error = %v", err)
	}
	if encapsulatedData.Index != 0 || encapsulatedData.Data[0] != 3 || encapsulatedData.ByteOrder != binary.BigEndian {
		t.Fatalf("FromDecodedFrames(encapsulated) data = %+v", encapsulatedData)
	}
}

func TestFromParserFrame(t *testing.T) {
	source := parser.Frame{
		Index: 2,
		Data:  []byte{0, 255},
		Metadata: parser.FrameMetadata{
			Rows:                      1,
			Columns:                   2,
			SamplesPerPixel:           1,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PixelRepresentation:       0,
			NumberOfFrames:            3,
			PhotometricInterpretation: "MONOCHROME2",
		},
		TransferSyntax: transfer.ExplicitVRBigEndian,
	}

	frame := FromParserFrame(source, WithWindow(128, 256))
	native, err := frame.GetNativeFrame()
	if err != nil {
		t.Fatalf("GetNativeFrame() error = %v", err)
	}
	if native.Index != 2 {
		t.Fatalf("native index = %d, want 2", native.Index)
	}
	if native.ByteOrder != binary.BigEndian {
		t.Fatalf("native byte order = %T, want binary.BigEndian", native.ByteOrder)
	}
}

func TestFromParserFramePreservesPlanarConfiguration(t *testing.T) {
	source := parser.Frame{
		Index: 0,
		Data:  []byte{255, 0, 0},
		Metadata: parser.FrameMetadata{
			Rows:                       1,
			Columns:                    1,
			SamplesPerPixel:            3,
			BitsAllocated:              8,
			BitsStored:                 8,
			HighBit:                    7,
			PixelRepresentation:        0,
			PlanarConfiguration:        1,
			PlanarConfigurationPresent: true,
			NumberOfFrames:             1,
			PhotometricInterpretation:  "RGB",
		},
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	frame := FromParserFrame(source)
	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	if got := requireRGBA(t, img).RGBAAt(0, 0); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("planar RGB pixel = %#v, want opaque red", got)
	}
}

func TestWindowAndRescaleFromObject(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagWindowCenter, core.VRDS, "50"),
		dicomtest.NewStringElement(tagWindowWidth, core.VRDS, "100"),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x1056), core.VRCS, "SIGMOID"),
		dicomtest.NewStringElement(tagRescaleSlope, core.VRDS, "2"),
		dicomtest.NewStringElement(tagRescaleIntercept, core.VRDS, "-1024"),
	}, nil)

	window, ok := WindowFromObject(obj)
	if !ok {
		t.Fatal("WindowFromObject() ok = false, want true")
	}
	if window.Center != 50 || window.Width != 100 || window.Function != display.VOISigmoid {
		t.Fatalf("WindowFromObject() = %#v, want center=50 width=100 function=SIGMOID", window)
	}
	rescale := RescaleFromObject(obj)
	if rescale.Slope != 2 || rescale.Intercept != -1024 {
		t.Fatalf("RescaleFromObject() = %#v, want slope=2 intercept=-1024", rescale)
	}
}

func TestExtractFramesFromNativeFile(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(frameMetadataElements(1, 2, 8, 8, 7, 0, "MONOCHROME2"),
			dicomtest.NewStringElement(tagWindowCenter, core.VRDS, "128"),
			dicomtest.NewStringElement(tagWindowWidth, core.VRDS, "256"),
			dicomtest.NewOBElement(core.TagPixelData, []byte{0, 255}),
		)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	frames, err := ExtractFrames(file)
	if err != nil {
		t.Fatalf("ExtractFrames() error = %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames))
	}
	img, err := frames[0].GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray := requireGray(t, img)
	if gray.Pix[0] != 0 || gray.Pix[1] != 255 {
		t.Fatalf("ExtractFrames image pixels = %v, want [0 255]", gray.Pix)
	}
}

func TestExtractFramesPreservesEncapsulatedOrderWithoutMutatingSource(t *testing.T) {
	const codecUID = "1.2.826.0.1.3680043.10.543.631.1"
	elements := append(frameMetadataElements(1, 2, 8, 8, 7, 0, "MONOCHROME2"),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "2"),
		dicomtest.NewFragmentSequenceElement(
			core.TagPixelData,
			[]byte{0, 0, 0, 0, 2, 0, 0, 0},
			[]byte{0xa0, 0xa1},
			[]byte{0xb0, 0xb1},
		),
	)
	dataset := object.FromElements(elements, nil)
	source, err := pixeldata.ExtractView(dataset)
	if err != nil {
		t.Fatalf("ExtractView(source) error = %v", err)
	}
	beforeOffsetTable := append([]byte(nil), source.Sequence.OffsetTable...)
	before := cloneFragments(source.Sequence.Fragments)
	codec := &readOnlyViewCodec{
		sourceOffsetTable: source.Sequence.OffsetTable,
		sourceFragments:   source.Sequence.Fragments,
	}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(codecUID, codec); err != nil {
		t.Fatalf("RegisterCodec() error = %v", err)
	}
	previousRegistry := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = registry
	t.Cleanup(func() { pixeldata.DefaultRegistry = previousRegistry })

	file := &object.File{
		Dataset: dataset,
		TransferSyntax: transfer.Syntax{
			UID: codecUID, Name: "read-only view test codec", ExplicitVR: true,
			ByteOrder: binary.LittleEndian, Encapsulated: true, CodecAvailable: true,
		},
	}
	frames, err := ExtractFrames(file)
	if err != nil {
		t.Fatalf("ExtractFrames() error = %v", err)
	}
	if !codec.receivedSourceView {
		t.Fatal("codec received a copied encapsulated payload, want the source read-only view")
	}
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2", len(frames))
	}
	for index, want := range [][]byte{{0x10, 0x11}, {0x20, 0x21}} {
		decoded, err := frames[index].GetEncapsulatedFrame()
		if err != nil {
			t.Fatalf("frame %d GetEncapsulatedFrame() error = %v", index, err)
		}
		if decoded.Index != index || !bytes.Equal(decoded.Data, want) {
			t.Fatalf("frame %d = index %d data %v, want index %d data %v", index, decoded.Index, decoded.Data, index, want)
		}
	}
	after, err := pixeldata.ExtractView(dataset)
	if err != nil {
		t.Fatalf("ExtractView(after decode) error = %v", err)
	}
	if !bytes.Equal(after.Sequence.OffsetTable, beforeOffsetTable) {
		t.Fatalf("source offset table mutated: got %v, want %v", after.Sequence.OffsetTable, beforeOffsetTable)
	}
	for index := range before {
		if !bytes.Equal(after.Sequence.Fragments[index], before[index]) {
			t.Fatalf("source fragment %d mutated: got %v, want %v", index, after.Sequence.Fragments[index], before[index])
		}
	}
}

func TestExtractFramesFromJPIPReferencedFileRequiresExternalRetrieval(t *testing.T) {
	const providerURL = "https://jpip-sensitive.example/images/abc.jp2?token=frame-secret"
	data, err := dicomtest.Part10File(
		transfer.JPIPReferenced,
		append(frameMetadataElements(1, 2, 8, 8, 7, 0, "MONOCHROME2"),
			dicomtest.NewStringElement(core.NewTag(0x0028, 0x7FE0), core.VRUR, providerURL),
		)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ExtractFrames(file)
	if !errors.Is(err, pixeldata.ErrJPIPRetrievalRequired) {
		t.Fatalf("ExtractFrames() error = %v, want ErrJPIPRetrievalRequired", err)
	}
	diagnostic := err.Error()
	for _, want := range []string{
		transfer.JPIPReferenced.UID,
		transfer.JPIPReferenced.Name,
		"Pixel Data Provider URL present (redacted)",
	} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("ExtractFrames() error = %q, want %q", err, want)
		}
	}
	for _, sensitive := range []string{providerURL, "jpip-sensitive.example", "frame-secret"} {
		if strings.Contains(diagnostic, sensitive) {
			t.Fatalf("ExtractFrames() error = %q, leaked %q", err, sensitive)
		}
	}
}

func TestExtractFramesFromVideoMediaFileReturnsNonRenderableError(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.MPEG4HP41F,
		[]core.Element{
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, binary.LittleEndian, 3),
			dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "YBR_PARTIAL_420"),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0006), core.VRUS, binary.LittleEndian, 0),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, binary.LittleEndian, 2),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, binary.LittleEndian, 2),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, binary.LittleEndian, 8),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, binary.LittleEndian, 8),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, binary.LittleEndian, 7),
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, binary.LittleEndian, 0),
			dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "1"),
			dicomtest.NewFragmentSequenceElement(
				core.TagPixelData,
				nil,
				[]byte{0x00, 0x00, 0x01, 0x09, 0x10, 0x00},
				[]byte{0x00, 0x00, 0x01, 0x65, 0x88, 0x00},
			),
		}...,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ExtractFrames(file)
	if !errors.Is(err, pixeldata.ErrMediaPayloadNotRenderable) {
		t.Fatalf("ExtractFrames() error = %v, want ErrMediaPayloadNotRenderable", err)
	}
	for _, want := range []string{"video media payload", transfer.MPEG4HP41F.UID, "external media pipeline"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ExtractFrames() error = %q, want substring %q", err, want)
		}
	}
}

func TestNativeFrameGetImageRejectsUnsupportedSamplesPerPixel(t *testing.T) {
	frame := NewNativeFrame(
		0,
		[]byte{0, 0, 0, 0},
		pixeldata.Metadata{
			Rows:                      1,
			Columns:                   1,
			SamplesPerPixel:           4,
			BitsAllocated:             8,
			BitsStored:                8,
			HighBit:                   7,
			PhotometricInterpretation: "MONOCHROME2",
		},
	)
	_, err := frame.GetImage()
	if !errors.Is(err, ErrUnsupportedSamplesPerPixel) {
		t.Fatalf("GetImage() error = %v, want ErrUnsupportedSamplesPerPixel", err)
	}
}

func TestNativeFrameGetImageColorLayouts(t *testing.T) {
	tests := []struct {
		name        string
		photometric string
		planar      uint16
		data        []byte
		want        []color.RGBA
	}{
		{
			name:        "planar RGB",
			photometric: "RGB",
			planar:      1,
			data:        []byte{255, 0, 0, 0, 255, 0, 0, 0, 255},
			want:        []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}},
		},
		{
			name:        "interleaved YBR FULL",
			photometric: "YBR_FULL",
			data:        []byte{128, 128, 128, 128, 128, 200},
			want:        []color.RGBA{{R: 128, G: 128, B: 128, A: 255}, {R: 229, G: 77, B: 128, A: 255}},
		},
		{
			name:        "planar YBR FULL",
			photometric: "YBR_FULL",
			planar:      1,
			data:        []byte{128, 128, 128, 128, 128, 200},
			want:        []color.RGBA{{R: 128, G: 128, B: 128, A: 255}, {R: 229, G: 77, B: 128, A: 255}},
		},
		{
			name:        "interleaved YBR FULL 422",
			photometric: "YBR_FULL_422",
			data:        []byte{128, 128, 128, 200},
			want:        []color.RGBA{{R: 229, G: 77, B: 128, A: 255}, {R: 229, G: 77, B: 128, A: 255}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frame := NewNativeFrame(0, tc.data, colorTestMetadata(uint16(len(tc.want)), tc.photometric, tc.planar))
			img, err := frame.GetImage()
			if err != nil {
				t.Fatalf("GetImage() error = %v", err)
			}
			rgba := requireRGBA(t, img)
			for x, want := range tc.want {
				if got := rgba.RGBAAt(x, 0); got != want {
					t.Fatalf("pixel %d = %#v, want %#v", x, got, want)
				}
			}
		})
	}
}

func TestNativeFrameGetImageTreatsAbsentPlanarConfigurationAsInterleaved(t *testing.T) {
	metadata := colorTestMetadata(2, "RGB", 1)
	metadata.PlanarConfigurationPresent = false
	frame := NewNativeFrame(0, []byte{255, 0, 0, 0, 255, 0}, metadata)

	img, err := frame.GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	rgba := requireRGBA(t, img)
	if got := rgba.RGBAAt(0, 0); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("pixel 0 = %#v, want red", got)
	}
	if got := rgba.RGBAAt(1, 0); got != (color.RGBA{G: 255, A: 255}) {
		t.Fatalf("pixel 1 = %#v, want green", got)
	}
}

func TestExtractFramesRendersNativeYBRFull422(t *testing.T) {
	elements := []core.Element{
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, binary.LittleEndian, 3),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "YBR_FULL_422"),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0006), core.VRUS, binary.LittleEndian, 0),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, binary.LittleEndian, 1),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, binary.LittleEndian, 2),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, binary.LittleEndian, 8),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, binary.LittleEndian, 8),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, binary.LittleEndian, 7),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, binary.LittleEndian, 0),
		dicomtest.NewOBElement(core.TagPixelData, []byte{128, 128, 128, 200}),
	}
	data, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := ExtractFrames(file)
	if err != nil {
		t.Fatalf("ExtractFrames() error = %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames))
	}
	img, err := frames[0].GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	for x := 0; x < 2; x++ {
		if got := requireRGBA(t, img).RGBAAt(x, 0); got != (color.RGBA{R: 229, G: 77, B: 128, A: 255}) {
			t.Fatalf("pixel %d = %#v, want converted YBR red", x, got)
		}
	}
}

func TestNativeFrameGetImageColorErrorsRemainTyped(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		metadata    pixeldata.Metadata
		wantErr     error
		wantDisplay error
	}{
		{name: "invalid planar configuration", data: make([]byte, 3), metadata: colorTestMetadata(1, "RGB", 2), wantErr: ErrUnsupportedPlanarConfiguration, wantDisplay: display.ErrUnsupportedColorLayout},
		{name: "planar YBR FULL 422", data: make([]byte, 4), metadata: colorTestMetadata(2, "YBR_FULL_422", 1), wantErr: ErrUnsupportedPlanarConfiguration, wantDisplay: display.ErrUnsupportedColorLayout},
		{name: "odd width YBR FULL 422", data: make([]byte, 8), metadata: colorTestMetadata(3, "YBR_FULL_422", 0), wantErr: ErrInvalidFrameMetadata, wantDisplay: display.ErrUnsupportedColorLayout},
		{name: "unsupported photometric", data: make([]byte, 3), metadata: colorTestMetadata(1, "YBR_PARTIAL_420", 0), wantErr: ErrUnsupportedPhotometricInterpretation, wantDisplay: display.ErrUnsupportedColorPhotometric},
		{name: "unsupported bits", data: make([]byte, 6), metadata: func() pixeldata.Metadata { m := colorTestMetadata(1, "RGB", 0); m.BitsAllocated = 16; return m }(), wantErr: ErrUnsupportedBitsAllocated, wantDisplay: display.ErrUnsupportedColorLayout},
		{name: "bits retain precedence over signed samples", data: make([]byte, 6), metadata: func() pixeldata.Metadata {
			m := colorTestMetadata(1, "RGB", 0)
			m.BitsAllocated = 16
			m.PixelRepresentation = 1
			return m
		}(), wantErr: ErrUnsupportedBitsAllocated, wantDisplay: display.ErrUnsupportedColorLayout},
		{name: "planar retains precedence over unsupported photometric", data: make([]byte, 3), metadata: colorTestMetadata(1, "YBR_PARTIAL_420", 1), wantErr: ErrUnsupportedPlanarConfiguration, wantDisplay: display.ErrUnsupportedColorLayout},
		{name: "signed samples", data: make([]byte, 3), metadata: func() pixeldata.Metadata { m := colorTestMetadata(1, "RGB", 0); m.PixelRepresentation = 1; return m }(), wantErr: ErrInvalidFrameMetadata},
		{name: "short pixels", data: []byte{0}, metadata: colorTestMetadata(1, "RGB", 0), wantErr: ErrPixelDataTooShort, wantDisplay: display.ErrPixelDataTooShort},
		{name: "palette remains outside frame adapter", data: []byte{0}, metadata: func() pixeldata.Metadata {
			m := colorTestMetadata(1, "PALETTE COLOR", 0)
			m.SamplesPerPixel = 1
			return m
		}(), wantErr: ErrUnsupportedPhotometricInterpretation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewNativeFrame(0, tc.data, tc.metadata).GetImage()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("GetImage() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantDisplay != nil && !errors.Is(err, tc.wantDisplay) {
				t.Fatalf("GetImage() error = %v, also want shared display error %v", err, tc.wantDisplay)
			}
		})
	}
}

func BenchmarkNativeFrameGetImageRGB512(b *testing.B) {
	frame := NewNativeFrame(0, make([]byte, 512*512*3), colorTestMetadata(512, "RGB", 0))
	frame.NativeData.Metadata.Rows = 512
	b.ReportAllocs()
	b.ResetTimer()
	var img image.Image
	var err error
	for i := 0; i < b.N; i++ {
		img, err = frame.GetImage()
		if err != nil {
			b.Fatal(err)
		}
	}
	if img == nil {
		b.Fatal("GetImage() returned nil")
	}
}

func requireGray(t *testing.T, img image.Image) *image.Gray {
	t.Helper()
	gray, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("image type = %T, want *image.Gray", img)
	}
	return gray
}

func requireRGBA(t *testing.T, img image.Image) *image.RGBA {
	t.Helper()
	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("image type = %T, want *image.RGBA", img)
	}
	return rgba
}

func testMetadata(rows, columns, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16, photometric string) pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:                      rows,
		Columns:                   columns,
		SamplesPerPixel:           1,
		BitsAllocated:             bitsAllocated,
		BitsStored:                bitsStored,
		HighBit:                   highBit,
		PixelRepresentation:       pixelRepresentation,
		NumberOfFrames:            1,
		PhotometricInterpretation: photometric,
	}
}

func colorTestMetadata(columns uint16, photometric string, planar uint16) pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:                       1,
		Columns:                    columns,
		SamplesPerPixel:            3,
		BitsAllocated:              8,
		BitsStored:                 8,
		HighBit:                    7,
		PlanarConfiguration:        planar,
		PlanarConfigurationPresent: true,
		NumberOfFrames:             1,
		PhotometricInterpretation:  photometric,
	}
}

func frameMetadataElements(rows, columns, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16, photometric string) []core.Element {
	return []core.Element{
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, binary.LittleEndian, 1),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, photometric),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, binary.LittleEndian, rows),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, binary.LittleEndian, columns),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, binary.LittleEndian, bitsAllocated),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, binary.LittleEndian, bitsStored),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, binary.LittleEndian, highBit),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, binary.LittleEndian, pixelRepresentation),
	}
}

type readOnlyViewCodec struct {
	sourceOffsetTable  []byte
	sourceFragments    [][]byte
	receivedSourceView bool
}

func (c *readOnlyViewCodec) Decode(pixel pixeldata.PixelData, _ *object.Object) (pixeldata.Frames, error) {
	c.receivedSourceView = pixel.Encapsulated && len(pixel.Sequence.OffsetTable) > 0 && len(c.sourceOffsetTable) > 0 &&
		&pixel.Sequence.OffsetTable[0] == &c.sourceOffsetTable[0] && len(pixel.Sequence.Fragments) == len(c.sourceFragments)
	if c.receivedSourceView {
		for index, fragment := range pixel.Sequence.Fragments {
			if len(fragment) == 0 || len(c.sourceFragments[index]) == 0 || &fragment[0] != &c.sourceFragments[index][0] {
				c.receivedSourceView = false
			}
		}
	}
	return pixeldata.Frames{Rows: 1, Columns: 2, Data: [][]byte{{0x10, 0x11}, {0x20, 0x21}}}, nil
}

func cloneFragments(source [][]byte) [][]byte {
	cloned := make([][]byte, len(source))
	for index := range source {
		cloned[index] = append([]byte(nil), source[index]...)
	}
	return cloned
}
