package jpeg2000

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	j2k "github.com/mrjoshuak/go-jpeg2000"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
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
)

func TestRegisterRegistersStillImageSyntaxes(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()

	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, uid := range supportedUIDs() {
		if _, ok := registry.GetCodec(uid); !ok {
			t.Fatalf("codec for %s not registered", uid)
		}
	}
	if _, ok := registry.GetCodec(transfer.JPIPReferenced.UID); ok {
		t.Fatal("JPIP codec registered, want still-image syntaxes only")
	}
}

func TestDecodeRejectsUninitializedDecoder(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)

	_, err := (&Codec{}).Decode(pixel, obj)
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("Decode() error = %v, want ErrDecoderUnavailable", err)
	}
}

func TestOpenJPEGDecoderReportsDependencyUnavailable(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)

	_, err := NewOpenJPEGCodec(OpenJPEGExecutable(filepath.Join(t.TempDir(), "missing-opj_decompress"))).Decode(pixel, obj)
	if !errors.Is(err, ErrOpenJPEGUnavailable) {
		t.Fatalf("Decode() error = %v, want ErrOpenJPEGUnavailable", err)
	}
}

func TestRegisterOpenJPEGSplitsClassicAndPureGoFallbacks(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()
	if err := RegisterOpenJPEG(registry, OpenJPEGExecutable(filepath.Join(t.TempDir(), "missing-opj_decompress"))); err != nil {
		t.Fatal(err)
	}

	classicEncoded := encodeGrayJ2K(t, false, []byte{0, 255})
	classicObj, classicPixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, classicEncoded)
	if _, err := registry.DecodeFrames(transfer.JPEG2000LosslessOnly.UID, classicPixel, classicObj); !errors.Is(err, ErrOpenJPEGUnavailable) {
		t.Fatalf("JPEG 2000 DecodeFrames() error = %v, want ErrOpenJPEGUnavailable from OpenJPEG backend", err)
	}

	htj2kEncoded := encodeGrayJ2K(t, true, []byte{0, 255})
	htj2kObj, htj2kPixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, htj2kEncoded)
	frames, err := registry.DecodeFrames(transfer.HTJ2KLossless.UID, htj2kPixel, htj2kObj)
	if err != nil {
		t.Fatalf("HTJ2K DecodeFrames() error = %v, want pure-Go fallback", err)
	}
	assertSingleFrame(t, frames, 1, 2, 2)
}

func TestDecodeFramesJPEG2000Grayscale(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	frames, err := registry.DecodeFrames(transfer.JPEG2000LosslessOnly.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 1 || frames.Columns != 2 || len(frames.Data) != 1 || len(frames.Data[0]) != 2 {
		t.Fatalf("DecodeFrames() = %#v, want one 1x2 8-bit frame", frames)
	}
	if frames.Data[0][0] > 16 || frames.Data[0][1] < 239 {
		t.Fatalf("DecodeFrames() data = %v, want approximate [0 255]", frames.Data[0])
	}
}

func TestDecompressFileUsesJPEG2000OptionalAdapter(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	obj, _ := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	got, err := pixeldata.DecompressFile(&object.File{
		Dataset:        obj,
		TransferSyntax: transfer.JPEG2000LosslessOnly,
	}, pixeldata.DecompressOptions{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if got.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntax = %q, want %q", got.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	raw, ok := got.Dataset.GetRaw(core.TagPixelData)
	if !ok {
		t.Fatal("decompressed Pixel Data missing")
	}
	if len(raw) != 2 || raw[0] > 16 || raw[1] < 239 {
		t.Fatalf("decompressed Pixel Data = %v, want approximate [0 255]", raw)
	}
}

func TestDecodeFramesJPEG2000LossyGrayscale(t *testing.T) {
	encoded := encodeGrayJ2KWithOptions(t, false, false, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	frames, err := registry.DecodeFrames(transfer.JPEG2000.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 1 || frames.Columns != 2 || len(frames.Data) != 1 || len(frames.Data[0]) != 2 {
		t.Fatalf("DecodeFrames() = %#v, want one 1x2 8-bit frame", frames)
	}
	if frames.Data[0][0] > 64 || frames.Data[0][1] < 191 {
		t.Fatalf("DecodeFrames() data = %v, want lossy approximation of [0 255]", frames.Data[0])
	}
}

func TestDecodeFramesHTJ2KGrayscale(t *testing.T) {
	encoded := encodeGrayJ2K(t, true, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	frames, err := registry.DecodeFrames(transfer.HTJ2KLossless.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 || len(frames.Data[0]) != 2 {
		t.Fatalf("DecodeFrames() frame lengths = %v, want one 2-byte frame", frameLengths(frames.Data))
	}
}

func TestDecodeFramesJPEG2000Grayscale16Bit(t *testing.T) {
	want := littleEndianUint16Bytes(0, 4096, 8192, 32768)
	encoded := encodeGray16J2K(t, false, true, []uint16{0, 4096, 8192, 32768})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{
		rows:          2,
		columns:       2,
		bitsAllocated: 16,
	}, encoded)

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleFrame(t, frames, 2, 2, len(want))
	if !slices.Equal(frames.Data[0], want) {
		t.Fatalf("Decode() data = %v, want exact %v", frames.Data[0], want)
	}
}

func TestDecodeFramesJPEG2000Grayscale12BitStoredIn16BitContainer(t *testing.T) {
	stored := []uint16{0, 1024, 2048, 4095}
	encoded := encodeGray16J2KWithPrecision(t, false, true, 12, fullScaleStoredSamples(12, stored))
	want := littleEndianUint16Bytes(stored...)
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{
		rows:          2,
		columns:       2,
		bitsAllocated: 16,
		bitsStored:    12,
	}, encoded)

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleFrame(t, frames, 2, 2, len(want))
	if !uint16SamplesWithinTolerance(frames.Data[0], want, 1) {
		t.Fatalf("Decode() data = %v, want 12-bit stored samples %v within 1 LSB", frames.Data[0], want)
	}
}

func TestDecodeFramesJPEG2000Grayscale16BitMaxValueExactnessGap(t *testing.T) {
	want := littleEndianUint16Bytes(0, 4096, 32768, 65535)
	encoded := encodeGray16J2K(t, false, true, []uint16{0, 4096, 32768, 65535})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{
		rows:          2,
		columns:       2,
		bitsAllocated: 16,
	}, encoded)

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleFrame(t, frames, 2, 2, len(want))
	if !uint16SamplesWithinTolerance(frames.Data[0], want, 1) {
		t.Fatalf("Decode() data = %v, want %v within documented 1 LSB max-value gap", frames.Data[0], want)
	}
}

func TestDecodeFramesJPEG2000RGB(t *testing.T) {
	encoded := encodeRGBJ2K(t, false, []color.RGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
	})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{
		samplesPerPixel: 3,
		photometric:     "RGB",
	}, encoded)

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 || len(frames.Data[0]) != 6 {
		t.Fatalf("Decode() frame lengths = %v, want one 6-byte RGB frame", frameLengths(frames.Data))
	}
	if want := []byte{255, 0, 0, 0, 255, 0}; !slices.Equal(frames.Data[0], want) {
		t.Fatalf("Decode() data = %v, want exact RGB bytes %v", frames.Data[0], want)
	}
}

func TestDecodeFramesJPEG2000RGB16Bit(t *testing.T) {
	want := littleEndianUint16Bytes(65535, 0, 0, 0, 32896, 65535)
	encoded := encodeRGB16J2K(t, false, true, []color.RGBA{
		{R: 255, A: 255},
		{G: 128, B: 255, A: 255},
	})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{
		samplesPerPixel: 3,
		bitsAllocated:   16,
		photometric:     "RGB",
	}, encoded)

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleFrame(t, frames, 1, 2, len(want))
	if !uint16SamplesWithinTolerance(frames.Data[0], want, 1) {
		t.Fatalf("Decode() data = %v, want %v within 1 LSB", frames.Data[0], want)
	}
}

func TestDecodeRejectsCodestreamMetadataMismatch(t *testing.T) {
	tests := []struct {
		name          string
		encoded       []byte
		opts          jpeg2000MetadataOptions
		want          error
		wantSubstring string
	}{
		{
			name:          "component count",
			encoded:       encodeRGB16J2K(t, false, true, []color.RGBA{{R: 255, A: 255}, {G: 128, B: 255, A: 255}}),
			opts:          jpeg2000MetadataOptions{samplesPerPixel: 1, bitsAllocated: 16},
			want:          ErrImageSizeMismatch,
			wantSubstring: "JPEG 2000 components=3 SamplesPerPixel=1",
		},
		{
			name:          "bit depth",
			encoded:       encodeGray16J2K(t, false, true, []uint16{0, 4096, 32768, 65534}),
			opts:          jpeg2000MetadataOptions{rows: 2, columns: 2, bitsAllocated: 8},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "JPEG 2000 precision=16 BitsStored=8",
		},
		{
			name:          "dimensions",
			encoded:       encodeGrayJ2K(t, false, []byte{0, 255}),
			opts:          jpeg2000MetadataOptions{rows: 2},
			want:          ErrImageSizeMismatch,
			wantSubstring: "JPEG 2000 size=2x1 Columns=2 Rows=2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, pixel := jpeg2000Object(t, tt.opts, tt.encoded)

			_, err := New().Decode(pixel, obj)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decode() error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Fatalf("Decode() error = %q, want substring %q", err, tt.wantSubstring)
			}
		})
	}
}

func TestDecodeFramesJPEG2000Part2Lossless(t *testing.T) {
	encoded := encodeGrayJ2KWithProfile(t, j2k.FormatJ2K, j2k.ProfilePart2, false, true, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleFrame(t, frames, 1, 2, 2)
}

func TestDecodeFramesHTJ2KRPCLAndLossy(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		opts   encodeOptions
		want   []byte
	}{
		{
			name:   "RPCL lossless",
			syntax: transfer.HTJ2KLosslessRPCL,
			opts: encodeOptions{
				highThroughput:   true,
				lossless:         true,
				progressionOrder: j2k.RPCL,
			},
			want: []byte{0, 255},
		},
		{
			name:   "HTJ2K lossy",
			syntax: transfer.HTJ2K,
			opts: encodeOptions{
				highThroughput: true,
				lossless:       false,
			},
			want: []byte{0, 255},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := encodeGrayJ2KWithEncodeOptions(t, tt.opts, []byte{0, 255})
			obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded)
			registry := pixeldata.NewMemoryRegistry()
			if err := Register(registry); err != nil {
				t.Fatal(err)
			}

			frames, err := registry.DecodeFrames(tt.syntax.UID, pixel, obj)
			if err != nil {
				t.Fatal(err)
			}
			assertSingleFrame(t, frames, 1, 2, 2)
			if !bytesWithinTolerance(frames.Data[0], tt.want, 64) {
				t.Fatalf("DecodeFrames() data = %v, want approximate %v", frames.Data[0], tt.want)
			}
		})
	}
}

func TestDecodeFramesSingleFrameMultiFragmentAssembly(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	split := len(encoded) / 2
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, encoded[:split], encoded[split:])

	frames, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 || len(frames.Data[0]) != 2 {
		t.Fatalf("Decode() frame lengths = %v, want one assembled 2-byte frame", frameLengths(frames.Data))
	}
}

func TestDecodeRejectsUnsupportedMultiFrameFragmentLayout(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{numberOfFrames: 2}, encoded, encoded, encoded)

	_, err := New().Decode(pixel, obj)
	if !errors.Is(err, ErrUnsupportedFragmentLayout) {
		t.Fatalf("Decode() error = %v, want ErrUnsupportedFragmentLayout", err)
	}
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodeRejectsMalformedCodestreamWithTypedError(t *testing.T) {
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte{0xFF, 0x4F, 0x00, 0x01})

	_, err := New().Decode(pixel, obj)
	if !errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("Decode() error = %v, want ErrMalformedCodestream", err)
	}
	if !strings.Contains(err.Error(), "frame 0") {
		t.Fatalf("Decode() error = %q, want frame context", err)
	}
}

func TestDecodeRejectsUnsupportedMetadata(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	tests := []struct {
		name          string
		opts          jpeg2000MetadataOptions
		want          error
		wantSubstring string
	}{
		{
			name:          "bits allocated",
			opts:          jpeg2000MetadataOptions{bitsAllocated: 12},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "BitsAllocated=12",
		},
		{
			name:          "bits stored exceeds allocated",
			opts:          jpeg2000MetadataOptions{bitsAllocated: 8, bitsStored: 12},
			want:          pixeldata.ErrInvalidMetadata,
			wantSubstring: "BitsStored=12 BitsAllocated=8",
		},
		{
			name:          "unsupported high bit alignment",
			opts:          jpeg2000MetadataOptions{bitsAllocated: 16, bitsStored: 12, highBit: uint16Ptr(15)},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "HighBit=15 BitsStored=12",
		},
		{
			name:          "samples per pixel",
			opts:          jpeg2000MetadataOptions{samplesPerPixel: 2},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "SamplesPerPixel=2",
		},
		{
			name:          "signed pixel data",
			opts:          jpeg2000MetadataOptions{pixelRepresentation: 1},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "PixelRepresentation=1",
		},
		{
			name: "planar configuration",
			opts: jpeg2000MetadataOptions{
				samplesPerPixel:     3,
				photometric:         "RGB",
				planarConfiguration: uint16Ptr(1),
			},
			want:          pixeldata.ErrUnsupportedPlanarConfiguration,
			wantSubstring: "PlanarConfiguration=1",
		},
		{
			name:          "photometric interpretation",
			opts:          jpeg2000MetadataOptions{photometric: "PALETTE COLOR"},
			want:          pixeldata.ErrUnsupportedPhotometricInterpretation,
			wantSubstring: "PhotometricInterpretation=PALETTE COLOR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, pixel := jpeg2000Object(t, tt.opts, encoded)
			_, err := New().Decode(pixel, obj)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decode() error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Fatalf("Decode() error = %q, want substring %q", err, tt.wantSubstring)
			}
		})
	}
}

func TestDecodeRejectsImageSizeMismatch(t *testing.T) {
	encoded := encodeGrayJ2K(t, false, []byte{0, 255})
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{rows: 2}, encoded)

	_, err := New().Decode(pixel, obj)
	if !errors.Is(err, ErrImageSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrImageSizeMismatch", err)
	}
}

func encodeGrayJ2K(t testing.TB, highThroughput bool, pixels []byte) []byte {
	t.Helper()
	return encodeGrayJ2KWithOptions(t, highThroughput, true, pixels)
}

func encodeGrayJ2KWithOptions(t testing.TB, highThroughput bool, lossless bool, pixels []byte) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, len(pixels), 1))
	if len(pixels) != len(img.Pix) {
		t.Fatalf("pixel count = %d, want %d", len(pixels), len(img.Pix))
	}
	copy(img.Pix, pixels)
	return encodeGrayJ2KWithEncodeOptions(t, encodeOptions{highThroughput: highThroughput, lossless: lossless}, pixels)
}

type encodeOptions struct {
	format           j2k.Format
	profile          j2k.Profile
	highThroughput   bool
	lossless         bool
	precision        int
	progressionOrder j2k.ProgressionOrder
}

func encodeGrayJ2KWithEncodeOptions(t testing.TB, enc encodeOptions, pixels []byte) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, len(pixels), 1))
	copy(img.Pix, pixels)
	return encodeJ2KWithEncodeOptions(t, img, enc)
}

func encodeGrayJ2KWithProfile(t testing.TB, format j2k.Format, profile j2k.Profile, highThroughput bool, lossless bool, pixels []byte) []byte {
	t.Helper()
	return encodeGrayJ2KWithEncodeOptions(t, encodeOptions{
		format:         format,
		profile:        profile,
		highThroughput: highThroughput,
		lossless:       lossless,
	}, pixels)
}

func encodeGray16J2K(t testing.TB, highThroughput bool, lossless bool, pixels []uint16) []byte {
	t.Helper()
	return encodeGray16J2KWithPrecision(t, highThroughput, lossless, 0, pixels)
}

func encodeGray16J2KWithPrecision(t testing.TB, highThroughput bool, lossless bool, precision int, pixels []uint16) []byte {
	t.Helper()
	img := image.NewGray16(image.Rect(0, 0, 2, 2))
	if len(pixels) != len(img.Pix)/2 {
		t.Fatalf("pixel count = %d, want %d", len(pixels), len(img.Pix)/2)
	}
	for i, pixel := range pixels {
		binary.BigEndian.PutUint16(img.Pix[i*2:], pixel)
	}
	return encodeJ2KWithEncodeOptions(t, img, encodeOptions{highThroughput: highThroughput, lossless: lossless, precision: precision})
}

type rgbTestColor struct {
	r uint16
	g uint16
	b uint16
}

func (c rgbTestColor) RGBA() (r uint32, g uint32, b uint32, a uint32) {
	return uint32(c.r), uint32(c.g), uint32(c.b), 0xffff
}

var rgbTestColorModel = color.ModelFunc(func(c color.Color) color.Color {
	r, g, b, _ := c.RGBA()
	return rgbTestColor{r: uint16(r), g: uint16(g), b: uint16(b)}
})

type rgbTestImage struct {
	rect   image.Rectangle
	pixels []rgbTestColor
}

func newRGBTestImage(t testing.TB, width int, height int, pixels []color.RGBA) *rgbTestImage {
	t.Helper()
	if len(pixels) != width*height {
		t.Fatalf("pixel count = %d, want %d", len(pixels), width*height)
	}
	img := &rgbTestImage{
		rect:   image.Rect(0, 0, width, height),
		pixels: make([]rgbTestColor, len(pixels)),
	}
	for i, pixel := range pixels {
		img.pixels[i] = rgbTestColor{
			r: uint16(pixel.R) * 0x101,
			g: uint16(pixel.G) * 0x101,
			b: uint16(pixel.B) * 0x101,
		}
	}
	return img
}

func (img *rgbTestImage) ColorModel() color.Model {
	return rgbTestColorModel
}

func (img *rgbTestImage) Bounds() image.Rectangle {
	return img.rect
}

func (img *rgbTestImage) At(x int, y int) color.Color {
	if !image.Pt(x, y).In(img.rect) {
		return rgbTestColor{}
	}
	idx := (y-img.rect.Min.Y)*img.rect.Dx() + (x - img.rect.Min.X)
	return img.pixels[idx]
}

func encodeRGBJ2K(t testing.TB, highThroughput bool, pixels []color.RGBA) []byte {
	t.Helper()
	return encodeRGBJ2KWithEncodeOptions(t, encodeOptions{highThroughput: highThroughput, lossless: true}, pixels)
}

func encodeRGB16J2K(t testing.TB, highThroughput bool, lossless bool, pixels []color.RGBA) []byte {
	t.Helper()
	return encodeRGBJ2KWithEncodeOptions(t, encodeOptions{highThroughput: highThroughput, lossless: lossless, precision: 16}, pixels)
}

func encodeRGBJ2KWithEncodeOptions(t testing.TB, enc encodeOptions, pixels []color.RGBA) []byte {
	t.Helper()
	return encodeJ2KWithEncodeOptions(t, newRGBTestImage(t, 2, 1, pixels), enc)
}

func encodeJ2K(t testing.TB, img image.Image, highThroughput bool, lossless bool) []byte {
	t.Helper()
	return encodeJ2KWithEncodeOptions(t, img, encodeOptions{highThroughput: highThroughput, lossless: lossless})
}

func encodeJ2KWithEncodeOptions(t testing.TB, img image.Image, enc encodeOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	opts := j2k.DefaultOptions()
	if enc.format == 0 {
		opts.Format = j2k.FormatJ2K
	} else {
		opts.Format = enc.format
	}
	opts.Profile = enc.profile
	opts.Lossless = enc.lossless
	if !enc.lossless {
		opts.Quality = 90
	}
	opts.HighThroughput = enc.highThroughput
	if enc.progressionOrder != 0 {
		opts.ProgressionOrder = enc.progressionOrder
	}
	opts.Precision = enc.precision
	if err := j2k.Encode(&buf, img, opts); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type jpeg2000MetadataOptions struct {
	rows                uint16
	columns             uint16
	samplesPerPixel     uint16
	bitsAllocated       uint16
	bitsStored          uint16
	highBit             *uint16
	pixelRepresentation uint16
	photometric         string
	numberOfFrames      int
	planarConfiguration *uint16
}

func jpeg2000Object(t testing.TB, opts jpeg2000MetadataOptions, fragments ...[]byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	obj := object.FromElements(append(jpeg2000MetadataElements(opts), fragmentElement(fragments...)), nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func jpeg2000MetadataElements(opts jpeg2000MetadataOptions) []core.Element {
	rows := opts.rows
	if rows == 0 {
		rows = 1
	}
	columns := opts.columns
	if columns == 0 {
		columns = 2
	}
	samplesPerPixel := opts.samplesPerPixel
	if samplesPerPixel == 0 {
		samplesPerPixel = 1
	}
	bitsAllocated := opts.bitsAllocated
	if bitsAllocated == 0 {
		bitsAllocated = 8
	}
	bitsStored := opts.bitsStored
	if bitsStored == 0 {
		bitsStored = bitsAllocated
	}
	highBit := bitsStored - 1
	if opts.highBit != nil {
		highBit = *opts.highBit
	}
	photometric := opts.photometric
	if photometric == "" {
		photometric = "MONOCHROME2"
	}
	numberOfFrames := opts.numberOfFrames
	if numberOfFrames == 0 {
		numberOfFrames = 1
	}

	elements := []core.Element{
		uint16Element(tagRows, rows),
		uint16Element(tagColumns, columns),
		uint16Element(tagSamplesPerPixel, samplesPerPixel),
		stringElement(tagPhotometricInterpretation, core.VRCS, photometric),
		stringElement(tagNumberOfFrames, core.VRIS, strconv.Itoa(numberOfFrames)),
		uint16Element(tagBitsAllocated, bitsAllocated),
		uint16Element(tagBitsStored, bitsStored),
		uint16Element(tagHighBit, highBit),
		uint16Element(tagPixelRepresentation, opts.pixelRepresentation),
	}
	if opts.planarConfiguration != nil {
		elements = append(elements, uint16Element(tagPlanarConfiguration, *opts.planarConfiguration))
	}
	return elements
}

func uint16Element(tag core.Tag, value uint16) core.Element {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return core.NewRawElement(tag, core.VRUS, raw)
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.NewRawElement(tag, vr, []byte(value))
}

func fragmentElement(fragments ...[]byte) core.Element {
	cloned := make([][]byte, len(fragments))
	for i := range fragments {
		cloned[i] = append([]byte(nil), fragments[i]...)
	}
	return core.Element{
		Header: core.ElementHeader{
			Tag:       core.TagPixelData,
			VR:        core.VROB,
			Length:    core.UndefinedLength,
			LengthSet: true,
		},
		Value: core.FragmentSequence{Fragments: cloned},
	}
}

func uint16Ptr(value uint16) *uint16 {
	return &value
}

func frameLengths(frames [][]byte) []int {
	lengths := make([]int, len(frames))
	for i := range frames {
		lengths[i] = len(frames[i])
	}
	return lengths
}

func assertSingleFrame(t *testing.T, frames pixeldata.Frames, rows, columns, bytes int) {
	t.Helper()
	if frames.Rows != rows || frames.Columns != columns || len(frames.Data) != 1 || len(frames.Data[0]) != bytes {
		t.Fatalf("frames = rows %d columns %d lengths %v, want %dx%d one %d-byte frame", frames.Rows, frames.Columns, frameLengths(frames.Data), rows, columns, bytes)
	}
}

func littleEndianUint16Bytes(values ...uint16) []byte {
	out := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(out[i*2:], value)
	}
	return out
}

func fullScaleStoredSamples(bitsStored uint16, values []uint16) []uint16 {
	maxStored := uint32(1<<bitsStored) - 1
	out := make([]uint16, len(values))
	for i, value := range values {
		out[i] = uint16((uint32(value)*65535 + maxStored/2) / maxStored)
	}
	return out
}

func bytesWithinTolerance(got, want []byte, tolerance byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		delta := int(got[i]) - int(want[i])
		if delta < 0 {
			delta = -delta
		}
		if delta > int(tolerance) {
			return false
		}
	}
	return true
}

func uint16SamplesWithinTolerance(got, want []byte, tolerance uint16) bool {
	if len(got) != len(want) || len(got)%2 != 0 {
		return false
	}
	for offset := 0; offset < len(got); offset += 2 {
		gotValue := binary.LittleEndian.Uint16(got[offset:])
		wantValue := binary.LittleEndian.Uint16(want[offset:])
		var delta uint16
		if gotValue > wantValue {
			delta = gotValue - wantValue
		} else {
			delta = wantValue - gotValue
		}
		if delta > tolerance {
			return false
		}
	}
	return true
}

func equalFrames(got, want [][]byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}
