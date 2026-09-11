package display

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestDisplayRejectsOverflowingFrameGeometry(t *testing.T) {
	huge := int(^uint(0) >> 1)
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "gray",
			run: func() error {
				_, err := RenderGray(Frame{Rows: huge, Columns: 2, Format: PixelFormat{BitsAllocated: 8}})
				return err
			},
		},
		{
			name: "modality",
			run: func() error {
				_, err := DecodeModality(Frame{Rows: huge, Columns: 2, Format: PixelFormat{BitsAllocated: 16}})
				return err
			},
		},
		{
			name: "color",
			run: func() error {
				_, err := RenderColor(ColorFrame{
					Rows: huge, Columns: 2, Photometric: "RGB", SamplesPerPixel: 3,
					Format: PixelFormat{BitsAllocated: 8},
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("display API panicked for overflowing metadata: %v", recovered)
				}
			}()
			if err := tt.run(); !errors.Is(err, ErrFrameSizeOverflow) {
				t.Fatalf("display API error = %v, want ErrFrameSizeOverflow", err)
			}
		})
	}
}

func TestDisplayCheckedLayoutRejectsEveryOverflowStage(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	paletteLUT, err := NewLUT([]int{1, 0, 8}, []uint16{0})
	if err != nil {
		t.Fatal(err)
	}
	palette := &PaletteColorLUT{Red: paletteLUT, Green: paletteLUT, Blue: paletteLUT}
	tests := []struct {
		name      string
		wantField string
		run       func() error
	}{
		{
			name: "pixel count", wantField: "PixelCount",
			run: func() error {
				_, err := RenderGray(Frame{Rows: maxInt, Columns: 2, Format: PixelFormat{BitsAllocated: 8}})
				return err
			},
		},
		{
			name: "source bytes", wantField: "SourceBytes",
			run: func() error {
				_, err := DecodeModality(Frame{Rows: maxInt/2 + 1, Columns: 1, Format: PixelFormat{BitsAllocated: 16}})
				return err
			},
		},
		{
			name: "source stride", wantField: "SourceStride",
			run: func() error {
				_, err := RenderColor(ColorFrame{
					Rows: maxInt/3 + 1, Columns: 1, Photometric: "RGB", SamplesPerPixel: 3,
					Format: PixelFormat{BitsAllocated: 8},
				})
				return err
			},
		},
		{
			name: "output bytes", wantField: "OutputBytes",
			run: func() error {
				_, err := RenderColor(ColorFrame{
					Rows: maxInt/4 + 1, Columns: 1, Photometric: "PALETTE COLOR", SamplesPerPixel: 1,
					Format: PixelFormat{BitsAllocated: 8}, Palette: palette,
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errors.Is(err, ErrFrameSizeOverflow) {
				t.Fatalf("display API error = %v, want ErrFrameSizeOverflow", err)
			}
			var validationErr *FrameValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tt.wantField {
				t.Fatalf("display API error = %#v, want field %q", err, tt.wantField)
			}
		})
	}
}

func TestDisplayMetadataErrorsAreTypedAndDoNotEchoValues(t *testing.T) {
	secretNumber := 987654321
	tests := []struct {
		name      string
		want      error
		wantField string
		run       func() error
	}{
		{
			name: "negative rows", want: ErrInvalidFrame, wantField: "Rows",
			run: func() error {
				_, err := RenderGray(Frame{Rows: -secretNumber, Columns: 1, Format: PixelFormat{BitsAllocated: 8}})
				return err
			},
		},
		{
			name: "gray bit depth", want: ErrUnsupportedBitsAllocated, wantField: "BitsAllocated",
			run: func() error {
				_, err := RenderGray(Frame{Rows: 1, Columns: 1, Format: PixelFormat{BitsAllocated: secretNumber}})
				return err
			},
		},
		{
			name: "color samples", want: ErrUnsupportedColorLayout, wantField: "SamplesPerPixel",
			run: func() error {
				_, err := RenderColor(ColorFrame{Rows: 1, Columns: 1, Photometric: "RGB", SamplesPerPixel: secretNumber, Format: PixelFormat{BitsAllocated: 8}})
				return err
			},
		},
		{
			name: "color bit depth", want: ErrUnsupportedColorLayout, wantField: "BitsAllocated",
			run: func() error {
				_, err := RenderColor(ColorFrame{Rows: 1, Columns: 1, Photometric: "RGB", SamplesPerPixel: 3, Format: PixelFormat{BitsAllocated: secretNumber}})
				return err
			},
		},
		{
			name: "photometric", want: ErrUnsupportedColorPhotometric, wantField: "PhotometricInterpretation",
			run: func() error {
				_, err := RenderColor(ColorFrame{Rows: 1, Columns: 1, Photometric: "SECRET-PHOTOMETRIC"})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errors.Is(err, tt.want) {
				t.Fatalf("display API error = %v, want %v", err, tt.want)
			}
			var validationErr *FrameValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tt.wantField {
				t.Fatalf("display API error = %#v, want typed field %q", err, tt.wantField)
			}
			if strings.Contains(err.Error(), strconv.Itoa(secretNumber)) || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("display API error echoed caller-controlled metadata: %q", err)
			}
		})
	}
}

func TestDisplayCheckedLayoutPreservesValidOutputBytes(t *testing.T) {
	grayPixels := make([]byte, 6)
	binary.LittleEndian.PutUint16(grayPixels[0:], 0)
	binary.LittleEndian.PutUint16(grayPixels[2:], 2048)
	binary.LittleEndian.PutUint16(grayPixels[4:], 4095)
	gray, err := RenderGray(Frame{
		Rows: 1, Columns: 3, Pixels: grayPixels,
		Format: PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 11},
		VOI:    VOILUT{Center: 2048, Width: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 128, 255}; !reflect.DeepEqual(gray.Pix, want) {
		t.Fatalf("gray bytes = %v, want %v", gray.Pix, want)
	}

	rgba, err := RenderColor(ColorFrame{
		Rows: 1, Columns: 2, Pixels: []byte{1, 2, 3, 4, 5, 6},
		Photometric: "RGB", SamplesPerPixel: 3, Format: PixelFormat{BitsAllocated: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 2, 3, 255, 4, 5, 6, 255}; !reflect.DeepEqual(rgba.Pix, want) {
		t.Fatalf("RGBA bytes = %v, want %v", rgba.Pix, want)
	}
}

func FuzzDisplayFrameMetadata(f *testing.F) {
	f.Add(int64(1), int64(1), int64(1), int64(8), byte(0), []byte{0, 0, 0, 0})
	f.Add(int64(^uint64(0)>>1), int64(2), int64(3), int64(16), byte(1), []byte{})
	f.Add(int64(^uint64(0)>>1), int64(2), int64(3), int64(8), byte(0), []byte{})
	f.Fuzz(func(t *testing.T, rows64, columns64, samples64, bits64 int64, mode byte, pixels []byte) {
		if len(pixels) > 1<<20 {
			return
		}
		rows, columns := int(rows64), int(columns64)
		bits, samples := int(bits64), int(samples64)
		gray := Frame{
			Rows: rows, Columns: columns, Pixels: pixels,
			Format: PixelFormat{BitsAllocated: bits, BitsStored: bits, HighBit: bits - 1},
		}
		_, _ = RenderGray(gray)
		_, _ = DecodeModality(gray)
		_, _ = DecodeModalityFloat32(gray)
		_ = DecodeModalityFloat32Into(make([]float32, 64), gray)

		photometric := "RGB"
		planar := int(mode % 3)
		if mode%3 == 1 {
			photometric = "YBR_FULL_422"
		} else if mode%3 == 2 {
			photometric = "PALETTE COLOR"
		}
		_, _ = RenderColor(ColorFrame{
			Rows: rows, Columns: columns, Pixels: pixels, Photometric: photometric,
			SamplesPerPixel: samples, PlanarConfiguration: planar,
			Format: PixelFormat{BitsAllocated: bits, BitsStored: bits, HighBit: bits - 1},
		})
	})
}
