package jpeg

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"strconv"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	pixelframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	tagPlanarConfiguration       = core.NewTag(0x0028, 0x0006)
	tagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
	tagWindowCenter              = core.NewTag(0x0028, 0x1050)
	tagWindowWidth               = core.NewTag(0x0028, 0x1051)
)

func TestJPEGBaselineDecodeAndGetImage(t *testing.T) {
	prev := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = pixeldata.NewMemoryRegistry()
	t.Cleanup(func() {
		pixeldata.DefaultRegistry = prev
	})
	if err := RegisterDefault(); err != nil {
		t.Fatal(err)
	}

	data, err := dicomtest.Part10File(
		transfer.JPEGBaseline,
		append(jpegMetadataElements(1, 2, 1, "MONOCHROME2", 1),
			dicomtest.NewStringElement(tagWindowCenter, core.VRDS, "128"),
			dicomtest.NewStringElement(tagWindowWidth, core.VRDS, "256"),
			dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, encodeGrayJPEG(t, 2, 1, []byte{0, 255})),
		)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	frames, err := pixelframe.ExtractFrames(file)
	if err != nil {
		t.Fatalf("ExtractFrames() error = %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames))
	}
	if !frames[0].IsEncapsulated() {
		t.Fatal("decoded JPEG frame IsEncapsulated() = false, want true")
	}
	img, err := frames[0].GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	gray, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("image type = %T, want *image.Gray", img)
	}
	if gray.Pix[0] > 32 {
		t.Fatalf("black JPEG pixel = %d, want <= 32", gray.Pix[0])
	}
	if gray.Pix[1] < 223 {
		t.Fatalf("white JPEG pixel = %d, want >= 223", gray.Pix[1])
	}
}

func TestJPEGBaselineYBRFull422GetImageUsesDecodedRGB(t *testing.T) {
	prev := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = pixeldata.NewMemoryRegistry()
	t.Cleanup(func() {
		pixeldata.DefaultRegistry = prev
	})
	if err := RegisterDefault(); err != nil {
		t.Fatal(err)
	}

	data, err := dicomtest.Part10File(
		transfer.JPEGBaseline,
		append(jpegMetadataElements(1, 1, 3, "YBR_FULL_422", 1),
			dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, encodeColorJPEG(t, 1, 1, []color.RGBA{
				{R: 255, G: 0, B: 0, A: 255},
			})),
		)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	frames, err := pixelframe.ExtractFrames(file)
	if err != nil {
		t.Fatalf("ExtractFrames() error = %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames))
	}
	decoded, err := frames[0].GetEncapsulatedFrame()
	if err != nil {
		t.Fatalf("GetEncapsulatedFrame() error = %v", err)
	}
	if decoded.Metadata.PhotometricInterpretation != "RGB" {
		t.Fatalf("decoded PhotometricInterpretation = %q, want RGB", decoded.Metadata.PhotometricInterpretation)
	}
	img, err := frames[0].GetImage()
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	if _, ok := img.(*image.RGBA); !ok {
		t.Fatalf("image type = %T, want *image.RGBA", img)
	}
}

func TestDecodeFramesJPEGBaselineRGB(t *testing.T) {
	obj := object.FromElements(append(
		jpegMetadataElements(1, 2, 3, "RGB", 1),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, encodeColorJPEG(t, 2, 1, []color.RGBA{
			{R: 255, A: 255},
			{G: 255, A: 255},
		})),
	), nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	frames, err := registry.DecodeFrames(UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 1 || frames.Columns != 2 {
		t.Fatalf("DecodeFrames() rows=%d columns=%d, want 1x2", frames.Rows, frames.Columns)
	}
	if len(frames.Data) != 1 || len(frames.Data[0]) != 6 {
		t.Fatalf("DecodeFrames() frame lengths = %v, want one 6-byte RGB frame", frameLengths(frames.Data))
	}
}

func TestDecodeFramesJPEGExtendedSOF1(t *testing.T) {
	obj, pixel := jpegObjectWithFragments(t, 1, 2, makeJPEGExtendedSOF1(t, encodeGrayJPEG(t, 2, 1, []byte{0, 255})))
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	frames, err := registry.DecodeFrames(transfer.JPEGExtended.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 1 || frames.Columns != 2 || len(frames.Data) != 1 || len(frames.Data[0]) != 2 {
		t.Fatalf("DecodeFrames() = %#v, want one 1x2 grayscale frame", frames)
	}
	if frames.Data[0][0] > 32 {
		t.Fatalf("black JPEG Extended pixel = %d, want <= 32", frames.Data[0][0])
	}
	if frames.Data[0][1] < 223 {
		t.Fatalf("white JPEG Extended pixel = %d, want >= 223", frames.Data[0][1])
	}
}

func TestRegisterIncludesJPEGExtended(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	if _, ok := registry.GetCodec(transfer.JPEGBaseline.UID); !ok {
		t.Fatalf("Register() did not register JPEG Baseline UID %s", transfer.JPEGBaseline.UID)
	}
	if _, ok := registry.GetCodec(transfer.JPEGExtended.UID); !ok {
		t.Fatalf("Register() did not register JPEG Extended UID %s", transfer.JPEGExtended.UID)
	}
}

func TestDecodeFramesJPEGBaselineRejectsUnsupportedMetadata(t *testing.T) {
	tests := []struct {
		name          string
		samplesPixel  uint16
		photometric   string
		opts          jpegMetadataOptions
		want          error
		wantSubstring string
	}{
		{
			name:          "bits allocated",
			samplesPixel:  1,
			photometric:   "MONOCHROME2",
			opts:          jpegMetadataOptions{bitsAllocated: 16},
			want:          ErrUnsupportedBitsAllocated,
			wantSubstring: "BitsAllocated=16",
		},
		{
			name:          "samples per pixel",
			samplesPixel:  2,
			photometric:   "MONOCHROME2",
			want:          ErrUnsupportedSamplesPerPixel,
			wantSubstring: "SamplesPerPixel=2",
		},
		{
			name:          "photometric interpretation",
			samplesPixel:  1,
			photometric:   "PALETTE COLOR",
			want:          pixeldata.ErrUnsupportedPhotometricInterpretation,
			wantSubstring: "PhotometricInterpretation=PALETTE COLOR",
		},
		{
			name:          "pixel representation",
			samplesPixel:  1,
			photometric:   "MONOCHROME2",
			opts:          jpegMetadataOptions{pixelRepresentation: 1},
			want:          pixeldata.ErrUnsupportedPixelRepresentation,
			wantSubstring: "PixelRepresentation=1",
		},
		{
			name:          "planar configuration",
			samplesPixel:  3,
			photometric:   "RGB",
			opts:          jpegMetadataOptions{planarConfiguration: uint16Ptr(1)},
			want:          pixeldata.ErrUnsupportedPlanarConfiguration,
			wantSubstring: "PlanarConfiguration=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := object.FromElements(append(
				jpegMetadataElementsWithOptions(1, 1, tt.samplesPixel, tt.photometric, 1, tt.opts),
				dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, encodeGrayJPEG(t, 1, 1, []byte{127})),
			), nil)
			pixel, err := pixeldata.Extract(obj)
			if err != nil {
				t.Fatal(err)
			}
			registry := pixeldata.NewMemoryRegistry()
			if err := Register(registry); err != nil {
				t.Fatal(err)
			}

			_, err = registry.DecodeFrames(UID, pixel, obj)
			if !errors.Is(err, tt.want) {
				t.Fatalf("DecodeFrames() error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), tt.wantSubstring) {
				t.Fatalf("DecodeFrames() error = %q leaked backend detail %q", err, tt.wantSubstring)
			}
		})
	}
}

func TestDecodeFramesCodecAbsentReturnsTypedError(t *testing.T) {
	prev := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = pixeldata.NewMemoryRegistry()
	t.Cleanup(func() {
		pixeldata.DefaultRegistry = prev
	})

	obj, pixel := jpegObjectWithFragments(t, 1, 1, encodeGrayJPEG(t, 1, 1, []byte{127}))
	_, err := pixeldata.DecodeFrames(UID, pixel, obj)
	if !errors.Is(err, pixeldata.ErrCodecNotFound) {
		t.Fatalf("DecodeFrames() error = %v, want ErrCodecNotFound", err)
	}
}

func TestDecodeFramesUnsupportedJPEGSyntaxReturnsTypedError(t *testing.T) {
	obj, pixel := jpegObjectWithFragments(t, 1, 1, encodeGrayJPEG(t, 1, 1, []byte{127}))
	registry := pixeldata.NewMemoryRegistry()

	_, err := registry.DecodeFrames(transfer.JPEG2000.UID, pixel, obj)
	if !errors.Is(err, pixeldata.ErrCodecNotFound) {
		t.Fatalf("DecodeFrames() error = %v, want ErrCodecNotFound", err)
	}
}

func TestDecodeFramesInvalidFragment(t *testing.T) {
	obj, pixel := jpegObjectWithFragments(t, 1, 1, []byte("not a jpeg fragment"))
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	_, err := registry.DecodeFrames(UID, pixel, obj)
	if !errors.Is(err, ErrInvalidFragment) {
		t.Fatalf("DecodeFrames() error = %v, want ErrInvalidFragment", err)
	}
}

func TestDecodeFramesRejectsFragmentCountMismatch(t *testing.T) {
	obj, pixel := jpegObjectWithFragments(t, 1, 1,
		encodeGrayJPEG(t, 1, 1, []byte{0}),
		encodeGrayJPEG(t, 1, 1, []byte{255}),
	)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	_, err := registry.DecodeFrames(UID, pixel, obj)
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("DecodeFrames() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodeRejectsNativePixelData(t *testing.T) {
	obj := object.FromElements(jpegMetadataElements(1, 1, 1, "MONOCHROME2", 1), nil)
	_, err := New().Decode(pixeldata.PixelData{Raw: []byte{0x00}}, obj)
	if !errors.Is(err, pixeldata.ErrIncompatiblePixelData) {
		t.Fatalf("Decode() error = %v, want ErrIncompatiblePixelData", err)
	}
}

func jpegObjectWithFragments(t *testing.T, rows, columns uint16, fragments ...[]byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	obj := object.FromElements(append(
		jpegMetadataElements(rows, columns, 1, "MONOCHROME2", 1),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...),
	), nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

type jpegMetadataOptions struct {
	bitsAllocated       uint16
	pixelRepresentation uint16
	planarConfiguration *uint16
}

func jpegMetadataElements(rows, columns, samplesPerPixel uint16, photometric string, numberOfFrames int) []core.Element {
	return jpegMetadataElementsWithOptions(rows, columns, samplesPerPixel, photometric, numberOfFrames, jpegMetadataOptions{})
}

func jpegMetadataElementsWithOptions(rows, columns, samplesPerPixel uint16, photometric string, numberOfFrames int, opts jpegMetadataOptions) []core.Element {
	bitsAllocated := opts.bitsAllocated
	if bitsAllocated == 0 {
		bitsAllocated = 8
	}
	highBit := uint16(0)
	if bitsAllocated > 0 {
		highBit = bitsAllocated - 1
	}

	elements := []core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, columns),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, samplesPerPixel),
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, photometric),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, strconv.Itoa(numberOfFrames)),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, highBit),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, opts.pixelRepresentation),
	}
	if opts.planarConfiguration != nil {
		elements = append(elements, dicomtest.Uint16Element(tagPlanarConfiguration, core.VRUS, nil, *opts.planarConfiguration))
	}
	return elements
}

func frameLengths(frames [][]byte) []int {
	lengths := make([]int, len(frames))
	for i := range frames {
		lengths[i] = len(frames[i])
	}
	return lengths
}

func uint16Ptr(value uint16) *uint16 {
	return &value
}

func encodeColorJPEG(t *testing.T, width, height int, pixels []color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	if len(pixels) != width*height {
		t.Fatalf("pixel count = %d, want %d", len(pixels), width*height)
	}
	for i, pixel := range pixels {
		img.SetRGBA(i%width, i/width, pixel)
	}
	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeGrayJPEG(t *testing.T, width, height int, pixels []byte) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, width, height))
	if len(pixels) != len(img.Pix) {
		t.Fatalf("pixel count = %d, want %d", len(pixels), len(img.Pix))
	}
	copy(img.Pix, pixels)
	for i := range img.Pix {
		img.SetGray(i%width, i/width, color.Gray{Y: img.Pix[i]})
	}
	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeJPEGExtendedSOF1(t *testing.T, data []byte) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == 0xFF && out[i+1] == 0xC0 {
			out[i+1] = 0xC1
			return out
		}
	}
	t.Fatal("encoded JPEG did not contain SOF0 marker")
	return nil
}
