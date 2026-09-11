package render

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"image/png"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestStandardEncodedRendererPNGUsesSharedGrayscaleWindow(t *testing.T) {
	renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
	frame := testRenderFrame([]byte{0, 64, 128, 255})
	window := WindowLevel{Center: 64, Width: 128}

	encoded, err := renderer.RenderEncoded(context.Background(), EncodedRequest{
		Frame: frame, Window: window, MediaType: MediaTypePNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.MediaType != MediaTypePNG || encoded.Width != 2 || encoded.Height != 2 {
		t.Fatalf("encoded metadata = %#v, want 2x2 image/png", encoded)
	}
	img, err := png.Decode(bytes.NewReader(encoded.Data))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	for index, stored := range frame.PixelBytes {
		x, y := index%2, index/2
		got := color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
		want := WindowedGray(float64(stored), window)
		if got != want {
			t.Fatalf("pixel %d = %d, want shared window result %d", index, got, want)
		}
	}
}

func TestStandardEncodedRendererPNGPreservesSupportedColor(t *testing.T) {
	renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
	frame := encodedColorFrame(2, 1, []byte{255, 0, 0, 0, 255, 0})

	encoded, err := renderer.RenderEncoded(context.Background(), EncodedRequest{Frame: frame, MediaType: MediaTypePNG})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(encoded.Data))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	for x, want := range []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}} {
		if got := color.RGBAModel.Convert(img.At(x, 0)).(color.RGBA); got != want {
			t.Fatalf("pixel %d = %#v, want %#v", x, got, want)
		}
	}
}

func TestStandardEncodedRendererJPEGBaselineHonorsAspectBound(t *testing.T) {
	renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
	pixels := make([]byte, 4*2*3)
	for index := 0; index < len(pixels); index += 3 {
		pixels[index] = 220
		pixels[index+1] = 20
		pixels[index+2] = 10
	}

	encoded, err := renderer.RenderEncoded(context.Background(), EncodedRequest{
		Frame: encodedColorFrame(4, 2, pixels), MediaType: MediaTypeJPEG,
		Width: 2, Height: 2, JPEGQuality: 95,
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.MediaType != MediaTypeJPEG || encoded.Width != 2 || encoded.Height != 1 {
		t.Fatalf("encoded metadata = %#v, want aspect-fit 2x1 image/jpeg", encoded)
	}
	if !bytes.Contains(encoded.Data, []byte{0xff, 0xc0}) || bytes.Contains(encoded.Data, []byte{0xff, 0xc1}) {
		t.Fatal("JPEG representation is not an 8-bit baseline SOF0 codestream")
	}
	img, err := stdjpeg.Decode(bytes.NewReader(encoded.Data))
	if err != nil {
		t.Fatalf("jpeg.Decode() error = %v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 1 {
		t.Fatalf("JPEG dimensions = %v, want 2x1", img.Bounds())
	}
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 < 190 || g>>8 > 45 || b>>8 > 35 {
		t.Fatalf("JPEG pixel = (%d,%d,%d), want bounded-loss red", r>>8, g>>8, b>>8)
	}
}

func TestStandardEncodedRendererCropsBeforeScaling(t *testing.T) {
	renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
	frame := encodedColorFrame(4, 2, []byte{
		255, 0, 0, 255, 0, 0, 0, 0, 255, 0, 255, 0,
		255, 0, 0, 255, 0, 0, 255, 255, 0, 0, 255, 255,
	})

	encoded, err := renderer.RenderEncoded(context.Background(), EncodedRequest{
		Frame: frame, MediaType: MediaTypePNG,
		SourceRect: image.Rect(2, 0, 4, 2), Width: 4, Height: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Width != 4 || encoded.Height != 4 {
		t.Fatalf("encoded dimensions = %dx%d, want cropped aspect-fit 4x4", encoded.Width, encoded.Height)
	}
	img, err := png.Decode(bytes.NewReader(encoded.Data))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(img.At(0, 0)).(color.RGBA); got.B != 255 || got.R != 0 || got.G != 0 {
		t.Fatalf("cropped first pixel = %#v, want blue", got)
	}

	for _, test := range []struct {
		name           string
		flipHorizontal bool
		flipVertical   bool
		want           color.RGBA
	}{
		{name: "horizontal", flipHorizontal: true, want: color.RGBA{G: 255, A: 255}},
		{name: "vertical", flipVertical: true, want: color.RGBA{R: 255, G: 255, A: 255}},
		{name: "both", flipHorizontal: true, flipVertical: true, want: color.RGBA{G: 255, B: 255, A: 255}},
	} {
		t.Run(test.name, func(t *testing.T) {
			flipped, err := renderer.RenderEncoded(context.Background(), EncodedRequest{
				Frame: frame, MediaType: MediaTypePNG,
				SourceRect: image.Rect(2, 0, 4, 2), Width: 2, Height: 2,
				FlipHorizontal: test.flipHorizontal, FlipVertical: test.flipVertical,
			})
			if err != nil {
				t.Fatal(err)
			}
			flippedImage, err := png.Decode(bytes.NewReader(flipped.Data))
			if err != nil {
				t.Fatal(err)
			}
			if got := color.RGBAModel.Convert(flippedImage.At(0, 0)).(color.RGBA); got != test.want {
				t.Fatalf("flipped first pixel = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestStandardEncodedRendererFailsClosedForLimitsMediaAndCancellation(t *testing.T) {
	frame := testRenderFrame([]byte{0, 64, 128, 255})

	t.Run("output bytes", func(t *testing.T) {
		renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{MaxOutputBytes: 8})
		_, err := renderer.RenderEncoded(context.Background(), EncodedRequest{Frame: frame, MediaType: MediaTypePNG})
		if !errors.Is(err, ErrEncodedImageResourceLimit) {
			t.Fatalf("RenderEncoded() error = %v, want ErrEncodedImageResourceLimit", err)
		}
	})

	t.Run("dimensions", func(t *testing.T) {
		renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{MaxWidth: 1})
		_, err := renderer.RenderEncoded(context.Background(), EncodedRequest{Frame: frame, MediaType: MediaTypePNG})
		if !errors.Is(err, ErrEncodedImageResourceLimit) {
			t.Fatalf("RenderEncoded() error = %v, want ErrEncodedImageResourceLimit", err)
		}
	})

	t.Run("media type", func(t *testing.T) {
		renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
		_, err := renderer.RenderEncoded(context.Background(), EncodedRequest{Frame: frame, MediaType: "image/webp"})
		if !errors.Is(err, ErrUnsupportedEncodedMediaType) {
			t.Fatalf("RenderEncoded() error = %v, want ErrUnsupportedEncodedMediaType", err)
		}
	})

	t.Run("empty non-default source rectangle", func(t *testing.T) {
		renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
		_, err := renderer.RenderEncoded(context.Background(), EncodedRequest{
			Frame:      frame,
			SourceRect: image.Rect(1, 0, 1, 1),
			MediaType:  MediaTypePNG,
		})
		if !errors.Is(err, ErrInvalidEncodedRequest) {
			t.Fatalf("RenderEncoded() error = %v, want ErrInvalidEncodedRequest", err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		renderer := mustStandardEncodedRenderer(t, EncodedImageLimits{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := renderer.RenderEncoded(ctx, EncodedRequest{Frame: frame, MediaType: MediaTypePNG})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RenderEncoded() error = %v, want context.Canceled", err)
		}
	})

	t.Run("invalid constructor", func(t *testing.T) {
		if _, err := NewStandardEncodedRenderer(EncodedImageLimits{MaxPixels: -1}); !errors.Is(err, ErrInvalidEncodedRequest) {
			t.Fatalf("NewStandardEncodedRenderer() error = %v, want ErrInvalidEncodedRequest", err)
		}
	})
}

func mustStandardEncodedRenderer(t *testing.T, limits EncodedImageLimits) *StandardEncodedRenderer {
	t.Helper()
	renderer, err := NewStandardEncodedRenderer(limits)
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}

func encodedColorFrame(columns, rows int, pixels []byte) *Frame {
	return &Frame{
		Metadata: pixeldata.Metadata{
			Rows:                       uint16(rows),
			Columns:                    uint16(columns),
			SamplesPerPixel:            3,
			BitsAllocated:              8,
			BitsStored:                 8,
			HighBit:                    7,
			PixelRepresentation:        0,
			PlanarConfiguration:        0,
			PlanarConfigurationPresent: true,
			PhotometricInterpretation:  "RGB",
		},
		ByteOrder:  binary.LittleEndian,
		PixelBytes: pixels,
		Rescale:    Rescale{Slope: 1},
	}
}
