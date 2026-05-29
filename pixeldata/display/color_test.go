package display

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/color"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestRenderColorRGBInterleaved(t *testing.T) {
	img, err := RenderColor(ColorFrame{
		Rows:            1,
		Columns:         3,
		Pixels:          []byte{255, 0, 0, 0, 255, 0, 0, 0, 255},
		Photometric:     "RGB",
		SamplesPerPixel: 3,
		Format:          PixelFormat{BitsAllocated: 8},
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	want := []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}}
	for x, w := range want {
		if got := img.RGBAAt(x, 0); got != w {
			t.Errorf("pixel %d = %#v, want %#v", x, got, w)
		}
	}
}

func TestRenderColorRGBPlanar(t *testing.T) {
	// Planar: all R, then all G, then all B.
	img, err := RenderColor(ColorFrame{
		Rows:                1,
		Columns:             3,
		Pixels:              []byte{255, 0, 0 /*R*/, 0, 255, 0 /*G*/, 0, 0, 255 /*B*/},
		Photometric:         "RGB",
		SamplesPerPixel:     3,
		PlanarConfiguration: 1,
		Format:              PixelFormat{BitsAllocated: 8},
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	// pixel0 = R,G,B planes index 0 = (255,0,0); pixel1 = (0,255,0); pixel2 = (0,0,255)
	want := []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}}
	for x, w := range want {
		if got := img.RGBAAt(x, 0); got != w {
			t.Errorf("planar pixel %d = %#v, want %#v", x, got, w)
		}
	}
}

func TestRenderColorYBRFull(t *testing.T) {
	img, err := RenderColor(ColorFrame{
		Rows:            1,
		Columns:         2,
		Pixels:          []byte{128, 128, 128 /*neutral gray*/, 128, 128, 200 /*Cr high*/},
		Photometric:     "YBR_FULL",
		SamplesPerPixel: 3,
		Format:          PixelFormat{BitsAllocated: 8},
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	if got := img.RGBAAt(0, 0); got != (color.RGBA{R: 128, G: 128, B: 128, A: 255}) {
		t.Errorf("neutral YBR pixel = %#v, want gray 128", got)
	}
	if got := img.RGBAAt(1, 0); got != (color.RGBA{R: 229, G: 77, B: 128, A: 255}) {
		t.Errorf("Cr-high YBR pixel = %#v, want {229 77 128}", got)
	}
}

func TestRenderColorYBR422(t *testing.T) {
	// Two pixels share Cb/Cr: Y0 Y1 Cb Cr.
	img, err := RenderColor(ColorFrame{
		Rows:            1,
		Columns:         2,
		Pixels:          []byte{128, 128, 128, 200},
		Photometric:     "YBR_FULL_422",
		SamplesPerPixel: 3,
		Format:          PixelFormat{BitsAllocated: 8},
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	for x := 0; x < 2; x++ {
		if got := img.RGBAAt(x, 0); got != (color.RGBA{R: 229, G: 77, B: 128, A: 255}) {
			t.Errorf("YBR422 pixel %d = %#v, want {229 77 128}", x, got)
		}
	}
}

func TestRenderColorPalette16Bit(t *testing.T) {
	red, _ := NewLUT([]int{4, 0, 16}, []uint16{0, 0x4000, 0x8000, 0xFFFF})
	green, _ := NewLUT([]int{4, 0, 16}, []uint16{0, 0, 0, 0})
	blue, _ := NewLUT([]int{4, 0, 16}, []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF})
	img, err := RenderColor(ColorFrame{
		Rows:            1,
		Columns:         4,
		Pixels:          []byte{0, 1, 2, 3},
		Photometric:     "PALETTE COLOR",
		SamplesPerPixel: 1,
		Format:          PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		Palette:         &PaletteColorLUT{Red: red, Green: green, Blue: blue},
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	want := []color.RGBA{
		{R: 0, G: 0, B: 255, A: 255},
		{R: 64, G: 0, B: 255, A: 255},
		{R: 128, G: 0, B: 255, A: 255},
		{R: 255, G: 0, B: 255, A: 255},
	}
	for x, w := range want {
		if got := img.RGBAAt(x, 0); got != w {
			t.Errorf("palette pixel %d = %#v, want %#v", x, got, w)
		}
	}
}

func TestRenderColorPalette8Bit(t *testing.T) {
	red, _ := NewLUT([]int{4, 0, 8}, []uint16{0, 64, 128, 255})
	green, _ := NewLUT([]int{4, 0, 8}, []uint16{255, 255, 255, 255})
	blue, _ := NewLUT([]int{4, 0, 8}, []uint16{0, 0, 0, 0})
	img, err := RenderColor(ColorFrame{
		Rows:            1,
		Columns:         4,
		Pixels:          []byte{0, 1, 2, 3},
		Photometric:     "PALETTE COLOR",
		SamplesPerPixel: 1,
		Format:          PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		Palette:         &PaletteColorLUT{Red: red, Green: green, Blue: blue},
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	if got := img.RGBAAt(3, 0); got != (color.RGBA{R: 255, G: 255, B: 0, A: 255}) {
		t.Errorf("8-bit palette pixel 3 = %#v, want yellow", got)
	}
}

func TestRenderColorErrors(t *testing.T) {
	tests := []struct {
		name    string
		frame   ColorFrame
		wantErr error
	}{
		{
			name:    "unsupported photometric",
			frame:   ColorFrame{Rows: 1, Columns: 1, Pixels: []byte{0, 0, 0}, Photometric: "YBR_PARTIAL_420", SamplesPerPixel: 3, Format: PixelFormat{BitsAllocated: 8}},
			wantErr: ErrUnsupportedColorPhotometric,
		},
		{
			name:    "16-bit RGB unsupported",
			frame:   ColorFrame{Rows: 1, Columns: 1, Pixels: make([]byte, 6), Photometric: "RGB", SamplesPerPixel: 3, Format: PixelFormat{BitsAllocated: 16}},
			wantErr: ErrUnsupportedColorLayout,
		},
		{
			name:    "odd-width YBR422",
			frame:   ColorFrame{Rows: 1, Columns: 3, Pixels: make([]byte, 8), Photometric: "YBR_FULL_422", SamplesPerPixel: 3, Format: PixelFormat{BitsAllocated: 8}},
			wantErr: ErrUnsupportedColorLayout,
		},
		{
			name:    "palette missing LUTs",
			frame:   ColorFrame{Rows: 1, Columns: 1, Pixels: []byte{0}, Photometric: "PALETTE COLOR", SamplesPerPixel: 1, Format: PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}},
			wantErr: ErrMissingPalette,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderColor(tc.frame); !errors.Is(err, tc.wantErr) {
				t.Fatalf("RenderColor() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPaletteFromObject(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(tagRedPaletteDesc, core.VRUS, nil, 4, 0, 16),
		dicomtest.Uint16Element(tagRedPaletteData, core.VRUS, nil, 0, 0x4000, 0x8000, 0xFFFF),
		dicomtest.Uint16Element(tagGreenPaletteDesc, core.VRUS, nil, 4, 0, 16),
		dicomtest.Uint16Element(tagGreenPaletteData, core.VRUS, nil, 0, 0, 0, 0),
		dicomtest.Uint16Element(tagBluePaletteDesc, core.VRUS, nil, 4, 0, 16),
		dicomtest.Uint16Element(tagBluePaletteData, core.VRUS, nil, 0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF),
	}, nil)
	palette, err := PaletteFromObject(obj)
	if err != nil {
		t.Fatalf("PaletteFromObject() error = %v", err)
	}
	if palette == nil {
		t.Fatal("PaletteFromObject() = nil, want palette")
	}
	if got := palette.Red.Lookup(1); got != 0x4000 {
		t.Fatalf("Red.Lookup(1) = %#x, want 0x4000", got)
	}
	if got := palette.Blue.Lookup(0); got != 0xFFFF {
		t.Fatalf("Blue.Lookup(0) = %#x, want 0xffff", got)
	}
}

func TestPaletteFromObjectAcceptsPaddedOddLength8BitPalette(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(tagRedPaletteDesc, core.VRUS, nil, 3, 0, 8),
		dicomtest.NewOBElement(tagRedPaletteData, []byte{0, 128, 255, 0}),
		dicomtest.Uint16Element(tagGreenPaletteDesc, core.VRUS, nil, 3, 0, 8),
		dicomtest.NewOBElement(tagGreenPaletteData, []byte{255, 128, 0, 0}),
		dicomtest.Uint16Element(tagBluePaletteDesc, core.VRUS, nil, 3, 0, 8),
		dicomtest.NewOBElement(tagBluePaletteData, []byte{0, 64, 255, 0}),
	}, nil)

	palette, err := PaletteFromObject(obj)
	if err != nil {
		t.Fatalf("PaletteFromObject() error = %v", err)
	}
	if palette == nil {
		t.Fatal("PaletteFromObject() = nil, want palette")
	}
	if got, want := palette.Red.Entries, []uint16{0, 128, 255}; !reflect.DeepEqual(got, want) {
		t.Fatalf("red entries = %v, want %v", got, want)
	}
	if got := palette.Blue.Lookup(2); got != 255 {
		t.Fatalf("Blue.Lookup(2) = %d, want 255", got)
	}
}

func TestPaletteFromObjectSegmentedPalette(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(tagRedPaletteDesc, core.VRUS, nil, 4, 0, 8),
		dicomtest.Uint16Element(tagSegRedPalette, core.VROW, nil,
			0, 2, 0, 85, // discrete entries 0,85
			1, 2, 255, // linear entries 170,255
		),
		dicomtest.Uint16Element(tagGreenPaletteDesc, core.VRUS, nil, 4, 0, 8),
		dicomtest.Uint16Element(tagSegGreenPalette, core.VROW, nil,
			0, 4, 0, 10, 20, 30,
		),
		dicomtest.Uint16Element(tagBluePaletteDesc, core.VRUS, nil, 4, 0, 8),
		dicomtest.Uint16Element(tagSegBluePalette, core.VROW, nil,
			0, 4, 255, 255, 255, 255,
		),
	}, nil)
	palette, err := PaletteFromObject(obj)
	if err != nil {
		t.Fatalf("PaletteFromObject() error = %v", err)
	}
	if palette == nil {
		t.Fatal("PaletteFromObject() = nil, want palette")
	}
	if got, want := palette.Red.Entries, []uint16{0, 85, 170, 255}; !reflect.DeepEqual(got, want) {
		t.Fatalf("red entries = %v, want [0 85 170 255]", got)
	}

	img, err := RenderColor(ColorFrame{
		Rows:            1,
		Columns:         4,
		Pixels:          []byte{0, 1, 2, 3},
		Photometric:     "PALETTE COLOR",
		SamplesPerPixel: 1,
		Format:          PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		Palette:         palette,
	})
	if err != nil {
		t.Fatalf("RenderColor() error = %v", err)
	}
	if got := img.RGBAAt(2, 0); got != (color.RGBA{R: 170, G: 20, B: 255, A: 255}) {
		t.Fatalf("palette pixel 2 = %#v, want segmented LUT color", got)
	}
}

func TestPaletteFromObjectSegmentedPaletteIndirect(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(tagRedPaletteDesc, core.VRUS, nil, 6, 0, 8),
		dicomtest.Uint16Element(tagSegRedPalette, core.VROW, nil,
			0, 2, 1, 2, // segment 0: entries 1,2
			0, 1, 3, // segment 1: entry 3
			2, 2, 0, 0, // copy two segments from byte offset 0
		),
		dicomtest.Uint16Element(tagGreenPaletteDesc, core.VRUS, nil, 6, 0, 8),
		dicomtest.NewOBElement(tagGreenPaletteData, []byte{0, 0, 0, 0, 0, 0}),
		dicomtest.Uint16Element(tagBluePaletteDesc, core.VRUS, nil, 6, 0, 8),
		dicomtest.NewOBElement(tagBluePaletteData, []byte{0, 0, 0, 0, 0, 0}),
	}, nil)

	palette, err := PaletteFromObject(obj)
	if err != nil {
		t.Fatalf("PaletteFromObject() error = %v", err)
	}
	if got, want := palette.Red.Entries, []uint16{1, 2, 3, 1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("red entries = %v, want %v", got, want)
	}
}

func TestPaletteFromObjectAbsent(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagPhotometric, core.VRCS, "MONOCHROME2"),
	}, nil)
	palette, err := PaletteFromObject(obj)
	if err != nil || palette != nil {
		t.Fatalf("PaletteFromObject() = (%v, %v), want (nil, nil)", palette, err)
	}
}

func TestColorMetadataFromObject(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagPhotometric, core.VRCS, "RGB"),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, 3),
		dicomtest.Uint16Element(tagPlanarConfiguration, core.VRUS, nil, 0),
		dicomtest.NewStringElement(tagColorSpace, core.VRCS, "sRGB"),
		dicomtest.NewOBElement(tagICCProfile, []byte{1, 2, 3, 4}),
	}, nil)
	m := ColorMetadataFromObject(obj)
	if m.Photometric != "RGB" || m.SamplesPerPixel != 3 || !m.PlanarConfigurationPresent || m.PlanarConfiguration != 0 {
		t.Fatalf("ColorMetadata = %#v", m)
	}
	if m.ColorSpace != "sRGB" {
		t.Fatalf("ColorSpace = %q, want sRGB", m.ColorSpace)
	}
	if !bytes.Equal(m.ICCProfile, []byte{1, 2, 3, 4}) {
		t.Fatalf("ICCProfile = %v, want [1 2 3 4]", m.ICCProfile)
	}
}

func TestColorMetadataFromObjectUsesObjectValueByteOrder(t *testing.T) {
	obj := readBigEndianDisplayObject(t,
		dicomtest.NewStringElement(tagPhotometric, core.VRCS, "RGB"),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, binary.BigEndian, 3),
		dicomtest.Uint16Element(tagPlanarConfiguration, core.VRUS, binary.BigEndian, 1),
	)

	m := ColorMetadataFromObject(obj)
	if m.SamplesPerPixel != 3 || !m.PlanarConfigurationPresent || m.PlanarConfiguration != 1 {
		t.Fatalf("ColorMetadata = %#v, want big-endian US fields decoded", m)
	}
}

func readBigEndianDisplayObject(t *testing.T, elements ...core.Element) *object.Object {
	t.Helper()
	data := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, elements...)
	obj, err := object.ReadDataSet(bytes.NewReader(data), transfer.ExplicitVRBigEndian)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestPipelineRenderColor(t *testing.T) {
	img, err := New().RenderColor(ColorFrame{
		Rows:            1,
		Columns:         1,
		Pixels:          []byte{10, 20, 30},
		Photometric:     "RGB",
		SamplesPerPixel: 3,
		Format:          PixelFormat{BitsAllocated: 8},
	})
	if err != nil {
		t.Fatalf("Pipeline.RenderColor() error = %v", err)
	}
	if got := img.RGBAAt(0, 0); got != (color.RGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("pixel = %#v, want {10 20 30}", got)
	}
}
