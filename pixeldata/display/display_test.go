package display

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

func u16le(values ...uint16) []byte {
	data := make([]byte, len(values)*2)
	for i, v := range values {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return data
}

func TestRenderGrayUnsigned8Monochrome2(t *testing.T) {
	img, err := RenderGray(Frame{
		Rows:    1,
		Columns: 3,
		Pixels:  []byte{0, 128, 255},
		Format:  PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		VOI:     VOILUT{Center: 128, Width: 256},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	if img.Pix[0] != 0 {
		t.Errorf("pixel 0 = %d, want 0", img.Pix[0])
	}
	if img.Pix[1] < 127 || img.Pix[1] > 128 {
		t.Errorf("pixel 1 = %d, want mid gray", img.Pix[1])
	}
	if img.Pix[2] != 255 {
		t.Errorf("pixel 2 = %d, want 255", img.Pix[2])
	}
}

func TestRenderGrayUnsigned16Window(t *testing.T) {
	img, err := RenderGray(Frame{
		Rows:    1,
		Columns: 3,
		Pixels:  u16le(0, 2048, 4095),
		Format:  PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 11},
		VOI:     VOILUT{Center: 2048, Width: 4096},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	if img.Pix[0] != 0 || img.Pix[2] != 255 {
		t.Errorf("pixels = %v, want first 0 last 255", img.Pix)
	}
	if img.Pix[1] < 127 || img.Pix[1] > 128 {
		t.Errorf("pixel 1 = %d, want mid gray", img.Pix[1])
	}
}

func TestRenderGraySigned16AppliesRescale(t *testing.T) {
	// -1024 and 0 stored; rescale slope 1 intercept 1024 -> modality 0 and 1024.
	low := int16(-1024)
	img, err := RenderGray(Frame{
		Rows:     1,
		Columns:  2,
		Pixels:   u16le(uint16(low), 0),
		Format:   PixelFormat{BitsAllocated: 16, BitsStored: 16, HighBit: 15, Signed: true},
		Modality: ModalityLUT{Slope: 1, Intercept: 1024},
		VOI:      VOILUT{Center: 1024, Width: 2048},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	if img.Pix[0] != 0 {
		t.Errorf("rescaled low pixel = %d, want 0", img.Pix[0])
	}
	if img.Pix[1] < 127 || img.Pix[1] > 128 {
		t.Errorf("rescaled mid pixel = %d, want mid gray", img.Pix[1])
	}
}

func TestRenderGrayModalityLUTApplied(t *testing.T) {
	// Without the LUT, stored 0/1/2 with window(20,20) would all clamp to 0.
	// The Modality LUT remaps them to 10/20/30 so the spread becomes visible.
	modLUT, err := NewLUT([]int{3, 0, 16}, []uint16{10, 20, 30})
	if err != nil {
		t.Fatal(err)
	}
	img, err := RenderGray(Frame{
		Rows:     1,
		Columns:  3,
		Pixels:   []byte{0, 1, 2},
		Format:   PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		Modality: ModalityLUT{LUT: modLUT},
		VOI:      VOILUT{Center: 20, Width: 20},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	if img.Pix[0] != 0 {
		t.Errorf("modality LUT low pixel = %d, want 0", img.Pix[0])
	}
	if img.Pix[2] != 255 {
		t.Errorf("modality LUT high pixel = %d, want 255 (proves LUT applied)", img.Pix[2])
	}
	if img.Pix[1] <= img.Pix[0] || img.Pix[1] >= img.Pix[2] {
		t.Errorf("modality LUT mid pixel = %d, want strictly between ends", img.Pix[1])
	}
}

func TestRenderGrayVOILUTApplied(t *testing.T) {
	voiLUT, err := NewLUT([]int{3, 0, 16}, []uint16{0, 2000, 4000})
	if err != nil {
		t.Fatal(err)
	}
	img, err := RenderGray(Frame{
		Rows:    1,
		Columns: 3,
		Pixels:  []byte{0, 1, 2},
		Format:  PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		VOI:     VOILUT{LUT: voiLUT},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	want := []uint8{0, 128, 255} // entries scaled across [0,4000] -> [0,255]
	for i, w := range want {
		if img.Pix[i] != w {
			t.Errorf("VOI LUT pixel %d = %d, want %d", i, img.Pix[i], w)
		}
	}
}

func TestRenderGrayDefaultWindowFullRange(t *testing.T) {
	// No window and no LUT: a default full-range window should map the stored
	// extremes to black and white.
	img, err := RenderGray(Frame{
		Rows:    1,
		Columns: 2,
		Pixels:  []byte{0, 255},
		Format:  PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	// The DICOM LINEAR function carries a half-LSB offset, so the stored minimum
	// maps to near-black (0 or 1) rather than exactly 0; the maximum is white.
	if img.Pix[0] > 1 {
		t.Errorf("default-window low pixel = %d, want near black", img.Pix[0])
	}
	if img.Pix[1] != 255 {
		t.Errorf("default-window high pixel = %d, want 255", img.Pix[1])
	}
}

func TestRenderGrayBigEndian(t *testing.T) {
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data[0:], 0)
	binary.BigEndian.PutUint16(data[2:], 4095)
	img, err := RenderGray(Frame{
		Rows:    1,
		Columns: 2,
		Pixels:  data,
		Format:  PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 11, ByteOrder: binary.BigEndian},
		VOI:     VOILUT{Center: 2048, Width: 4096},
	})
	if err != nil {
		t.Fatalf("RenderGray() error = %v", err)
	}
	if img.Pix[0] != 0 || img.Pix[1] != 255 {
		t.Errorf("big-endian pixels = %v, want [0 255]", img.Pix)
	}
}

func TestRenderGrayErrors(t *testing.T) {
	tests := []struct {
		name    string
		frame   Frame
		wantErr error
	}{
		{
			name:    "zero rows",
			frame:   Frame{Rows: 0, Columns: 2, Pixels: []byte{0, 0}, Format: PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}},
			wantErr: ErrInvalidFrame,
		},
		{
			name:    "unsupported bits allocated",
			frame:   Frame{Rows: 1, Columns: 1, Pixels: []byte{0, 0, 0, 0}, Format: PixelFormat{BitsAllocated: 32, BitsStored: 32, HighBit: 31}},
			wantErr: ErrUnsupportedBitsAllocated,
		},
		{
			name:    "pixel data too short",
			frame:   Frame{Rows: 1, Columns: 4, Pixels: []byte{0, 0}, Format: PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}},
			wantErr: ErrPixelDataTooShort,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderGray(tc.frame); !errors.Is(err, tc.wantErr) {
				t.Fatalf("RenderGray() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPipelineRenderGray(t *testing.T) {
	img, err := New().RenderGray(Frame{
		Rows:    1,
		Columns: 2,
		Pixels:  []byte{0, 255},
		Format:  PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		VOI:     VOILUT{Center: 128, Width: 256},
	})
	if err != nil {
		t.Fatalf("Pipeline.RenderGray() error = %v", err)
	}
	if img.Pix[0] != 0 || img.Pix[1] != 255 {
		t.Errorf("pipeline pixels = %v, want [0 255]", img.Pix)
	}
}

func TestModalityLUTFromObjectLinear(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagRescaleSlope, core.VRDS, "2"),
		dicomtest.NewStringElement(tagRescaleIntercept, core.VRDS, "-1024"),
	}, nil)
	mod, err := ModalityLUTFromObject(obj)
	if err != nil {
		t.Fatalf("ModalityLUTFromObject() error = %v", err)
	}
	if mod.Slope != 2 || mod.Intercept != -1024 || mod.LUT != nil {
		t.Fatalf("ModalityLUTFromObject() = %#v", mod)
	}
	if got := mod.Apply(100); got != 2*100-1024 {
		t.Fatalf("Apply(100) = %v, want %v", got, 2*100-1024)
	}
}

func TestModalityLUTFromObjectSequence(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		dicomtest.Uint16Element(tagLUTDescriptor, core.VRUS, nil, 3, 0, 16),
		dicomtest.Uint16Element(tagLUTData, core.VRUS, nil, 10, 20, 30),
	}}
	obj := object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(tagModalityLUTSequence, item),
	}, nil)
	mod, err := ModalityLUTFromObject(obj)
	if err != nil {
		t.Fatalf("ModalityLUTFromObject() error = %v", err)
	}
	if mod.LUT == nil {
		t.Fatal("ModalityLUTFromObject() LUT = nil, want non-nil")
	}
	if got := mod.LUT.Lookup(1); got != 20 {
		t.Fatalf("LUT.Lookup(1) = %d, want 20", got)
	}
}

func TestModalityLUTFromObjectSequenceUsesObjectValueByteOrder(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		dicomtest.Uint16Element(tagLUTDescriptor, core.VRUS, binary.BigEndian, 3, 0, 16),
		dicomtest.Uint16Element(tagLUTData, core.VRUS, binary.BigEndian, 10, 20, 30),
	}}
	obj := readBigEndianDisplayObject(t, dicomtest.NewSequenceElement(tagModalityLUTSequence, item))

	mod, err := ModalityLUTFromObject(obj)
	if err != nil {
		t.Fatalf("ModalityLUTFromObject() error = %v", err)
	}
	if mod.LUT == nil {
		t.Fatal("ModalityLUTFromObject() LUT = nil, want non-nil")
	}
	if got := mod.LUT.Lookup(1); got != 20 {
		t.Fatalf("LUT.Lookup(1) = %d, want 20", got)
	}
}

func TestVOIFromObjectWindowAndFunction(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagWindowCenter, core.VRDS, "40"),
		dicomtest.NewStringElement(tagWindowWidth, core.VRDS, "80"),
		dicomtest.NewStringElement(tagVOILUTFunction, core.VRCS, "SIGMOID"),
	}, nil)
	voi, err := VOIFromObject(obj)
	if err != nil {
		t.Fatalf("VOIFromObject() error = %v", err)
	}
	if voi.Center != 40 || voi.Width != 80 {
		t.Fatalf("VOIFromObject() window = (%v, %v), want (40, 80)", voi.Center, voi.Width)
	}
	if voi.Function != VOISigmoid {
		t.Fatalf("VOIFromObject() function = %v, want VOISigmoid", voi.Function)
	}
}

func TestVOIFromObjectSequence(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		dicomtest.Uint16Element(tagLUTDescriptor, core.VRUS, nil, 3, 0, 16),
		dicomtest.Uint16Element(tagLUTData, core.VRUS, nil, 0, 2000, 4000),
	}}
	obj := object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(tagVOILUTSequence, item),
	}, nil)
	voi, err := VOIFromObject(obj)
	if err != nil {
		t.Fatalf("VOIFromObject() error = %v", err)
	}
	if voi.LUT == nil {
		t.Fatal("VOIFromObject() LUT = nil, want non-nil")
	}
	if got := voi.LUT.Lookup(2); got != 4000 {
		t.Fatalf("LUT.Lookup(2) = %d, want 4000", got)
	}
}

func TestVOIFromObjectInvalidLUTReturnsError(t *testing.T) {
	// A malformed VOI LUT descriptor (7 bits per entry) must surface an explicit
	// error rather than silently rendering wrong pixels; the window is preserved.
	item := core.DataSet{Elements: []core.Element{
		dicomtest.Uint16Element(tagLUTDescriptor, core.VRUS, nil, 3, 0, 7),
		dicomtest.Uint16Element(tagLUTData, core.VRUS, nil, 0, 2000, 4000),
	}}
	obj := object.FromElements([]core.Element{
		dicomtest.NewStringElement(tagWindowCenter, core.VRDS, "40"),
		dicomtest.NewStringElement(tagWindowWidth, core.VRDS, "80"),
		dicomtest.NewSequenceElement(tagVOILUTSequence, item),
	}, nil)
	voi, err := VOIFromObject(obj)
	if !errors.Is(err, ErrInvalidLUTDescriptor) {
		t.Fatalf("VOIFromObject() error = %v, want ErrInvalidLUTDescriptor", err)
	}
	if voi.LUT != nil {
		t.Fatal("VOIFromObject() LUT should be nil on invalid descriptor")
	}
	if voi.Center != 40 || voi.Width != 80 {
		t.Fatalf("VOIFromObject() window not preserved: (%v, %v)", voi.Center, voi.Width)
	}
}

func TestVOIWindowByte(t *testing.T) {
	voi := VOILUT{Center: 50, Width: 100}
	if got := voi.WindowByte(40); got != 103 {
		t.Fatalf("WindowByte(40) = %d, want 103", got)
	}
	if got := voi.WindowByte(-1); got != 0 {
		t.Fatalf("WindowByte(-1) = %d, want 0", got)
	}
	if got := voi.WindowByte(100); got != 255 {
		t.Fatalf("WindowByte(100) = %d, want 255", got)
	}
}

func TestDecodeModality(t *testing.T) {
	frame := Frame{
		Rows:     1,
		Columns:  3,
		Pixels:   []byte{0, 100, 200},
		Format:   PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7},
		Modality: ModalityLUT{Slope: 2, Intercept: -1000},
	}
	got, err := DecodeModality(frame)
	if err != nil {
		t.Fatalf("DecodeModality() error = %v", err)
	}
	want := []float64{-1000, -800, -600}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("modality[%d] = %v, want %v", i, got[i], w)
		}
	}
	got32, err := DecodeModalityFloat32(frame)
	if err != nil {
		t.Fatalf("DecodeModalityFloat32() error = %v", err)
	}
	for i, w := range want {
		if got32[i] != float32(w) {
			t.Fatalf("float32 modality[%d] = %v, want %v", i, got32[i], float32(w))
		}
	}
	into := []float32{99, 99, 99, 77}
	if err := DecodeModalityFloat32Into(into, frame); err != nil {
		t.Fatalf("DecodeModalityFloat32Into() error = %v", err)
	}
	for i, w := range want {
		if into[i] != float32(w) {
			t.Fatalf("float32 into modality[%d] = %v, want %v", i, into[i], float32(w))
		}
	}
	if into[3] != 77 {
		t.Fatalf("DecodeModalityFloat32Into() overwrote tail = %v", into[3])
	}
}

func TestDecodeModalitySigned32(t *testing.T) {
	stored := []int32{-2147483648, -1, 0, 2147483647}
	pixels := make([]byte, 4*len(stored))
	for index, value := range stored {
		binary.LittleEndian.PutUint32(pixels[4*index:], uint32(value))
	}
	frame := Frame{
		Rows:     1,
		Columns:  len(stored),
		Pixels:   pixels,
		Format:   PixelFormat{BitsAllocated: 32, BitsStored: 32, HighBit: 31, Signed: true},
		Modality: ModalityLUT{Slope: 2, Intercept: 3},
	}

	got, err := DecodeModality(frame)
	if err != nil {
		t.Fatalf("DecodeModality() error = %v", err)
	}
	want := []float64{-4294967293, 1, 3, 4294967297}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("modality[%d] = %v, want %v", index, got[index], want[index])
		}
	}
	got32, err := DecodeModalityFloat32(frame)
	if err != nil {
		t.Fatalf("DecodeModalityFloat32() error = %v", err)
	}
	for index := range want {
		if got32[index] != float32(want[index]) {
			t.Fatalf("float32 modality[%d] = %v, want %v", index, got32[index], float32(want[index]))
		}
	}
}

func TestDecodeModalityUnsigned32PreservesFullRange(t *testing.T) {
	stored := []uint32{0, 2147483648, 4294967295}
	pixels := make([]byte, 4*len(stored))
	for index, value := range stored {
		binary.BigEndian.PutUint32(pixels[4*index:], value)
	}
	frame := Frame{
		Rows:     1,
		Columns:  len(stored),
		Pixels:   pixels,
		Format:   PixelFormat{BitsAllocated: 32, BitsStored: 32, HighBit: 31, ByteOrder: binary.BigEndian},
		Modality: ModalityLUT{Slope: 1, Intercept: -5},
	}

	got, err := DecodeModality(frame)
	if err != nil {
		t.Fatalf("DecodeModality() error = %v", err)
	}
	want := []float64{-5, 2147483643, 4294967290}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("modality[%d] = %v, want %v", index, got[index], want[index])
		}
	}
	got32, err := DecodeModalityFloat32(frame)
	if err != nil {
		t.Fatalf("DecodeModalityFloat32() error = %v", err)
	}
	for index := range want {
		if got32[index] != float32(want[index]) {
			t.Fatalf("float32 modality[%d] = %v, want %v", index, got32[index], float32(want[index]))
		}
	}
}

func TestDecodeModalityErrors(t *testing.T) {
	if _, err := DecodeModality(Frame{Rows: 0, Columns: 1, Format: PixelFormat{BitsAllocated: 8}}); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("want ErrInvalidFrame")
	}
	if _, err := DecodeModality(Frame{Rows: 1, Columns: 4, Pixels: []byte{0}, Format: PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}}); !errors.Is(err, ErrPixelDataTooShort) {
		t.Fatalf("want ErrPixelDataTooShort")
	}
	if _, err := DecodeModalityFloat32(Frame{Rows: 0, Columns: 1, Format: PixelFormat{BitsAllocated: 8}}); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("float32: want ErrInvalidFrame")
	}
	if _, err := DecodeModalityFloat32(Frame{Rows: 1, Columns: 4, Pixels: []byte{0}, Format: PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}}); !errors.Is(err, ErrPixelDataTooShort) {
		t.Fatalf("float32: want ErrPixelDataTooShort")
	}
	if err := DecodeModalityFloat32Into(
		make([]float32, 3),
		Frame{Rows: 1, Columns: 4, Pixels: []byte{0, 1, 2, 3}, Format: PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}},
	); !errors.Is(err, ErrDestinationTooShort) {
		t.Fatalf("float32 into: error = %v, want ErrDestinationTooShort", err)
	}
}

func BenchmarkDecodeModality512(b *testing.B) {
	const pixels = 512 * 512
	data := make([]byte, pixels*2)
	for i := 0; i < pixels; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(i%4096))
	}
	frame := Frame{
		Rows:     512,
		Columns:  512,
		Pixels:   data,
		Format:   PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 11, ByteOrder: binary.LittleEndian},
		Modality: ModalityLUT{Slope: 0.75, Intercept: -1024},
	}

	b.Run("float64_then_float32", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			values64, err := DecodeModality(frame)
			if err != nil {
				b.Fatal(err)
			}
			values32 := make([]float32, len(values64))
			for j, value := range values64 {
				values32[j] = float32(value)
			}
			benchmarkModalityValue = values32[len(values32)-1]
		}
	})
	b.Run("float32_direct", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			values, err := DecodeModalityFloat32(frame)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkModalityValue = values[len(values)-1]
		}
	})
	b.Run("float32_into_reused", func(b *testing.B) {
		values := make([]float32, pixels)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := DecodeModalityFloat32Into(values, frame); err != nil {
				b.Fatal(err)
			}
			benchmarkModalityValue = values[len(values)-1]
		}
	})
}

func BenchmarkRenderGray512(b *testing.B) {
	const pixels = 512 * 512
	data := make([]byte, pixels*2)
	for i := 0; i < pixels; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(i%4096))
	}
	frame := Frame{
		Rows:     512,
		Columns:  512,
		Pixels:   data,
		Format:   PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 11, ByteOrder: binary.LittleEndian},
		Modality: ModalityLUT{Slope: 0.75, Intercept: -1024},
		VOI:      VOILUT{Center: 40, Width: 400},
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		img, err := RenderGray(frame)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkGrayValue = img.Pix[len(img.Pix)-1]
	}
}

var benchmarkModalityValue float32
var benchmarkGrayValue uint8

func TestStoredPixelValueSignExtendsBitsStored(t *testing.T) {
	got := storedPixelValue([]byte{0x00, 0x08}, PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 11, Signed: true}, binary.LittleEndian)
	if got != -2048 {
		t.Fatalf("storedPixelValue() = %d, want -2048", got)
	}
}

func TestStoredPixelValueUsesHighBit(t *testing.T) {
	got := storedPixelValue([]byte{0x00, 0xF0}, PixelFormat{BitsAllocated: 16, BitsStored: 12, HighBit: 15, Signed: false}, binary.LittleEndian)
	if got != 0xF00 {
		t.Fatalf("storedPixelValue() = %#x, want 0xf00", got)
	}
}

func TestStoredPixelValueUnsigned8(t *testing.T) {
	got := storedPixelValue([]byte{200}, PixelFormat{BitsAllocated: 8, BitsStored: 8, HighBit: 7}, binary.LittleEndian)
	if got != 200 {
		t.Fatalf("storedPixelValue() = %d, want 200", got)
	}
}

func TestVOILinearExactBoundarySymmetry(t *testing.T) {
	// PS3.3 C.11.2.1.3.2 has no additive offset, so the function is symmetric
	// about its boundaries: just inside the lower edge maps to 0, just inside the
	// upper edge maps to 255.
	voi := VOILUT{Center: 100, Width: 100, Function: VOILinearExact} // low=50, high=150
	if got := voi.windowToByte(50.001); got != 0 {
		t.Fatalf("just above low = %d, want 0", got)
	}
	if got := voi.windowToByte(149.999); got != 255 {
		t.Fatalf("just below high = %d, want 255", got)
	}
}

func TestVOIWindowFunctionsMonotonic(t *testing.T) {
	// All three window functions must be monotonically non-decreasing and span
	// the full output range for inputs across the window.
	for _, fn := range []VOIFunction{VOILinear, VOILinearExact, VOISigmoid} {
		voi := VOILUT{Center: 100, Width: 100, Function: fn}
		prev := -1
		for x := 0; x <= 200; x += 10 {
			got := int(voi.windowToByte(float64(x)))
			if got < prev {
				t.Fatalf("function %v not monotonic at x=%d: %d < %d", fn, x, got, prev)
			}
			prev = got
		}
		if voi.windowToByte(0) > 5 {
			t.Errorf("function %v at x=0 = %d, want near black", fn, voi.windowToByte(0))
		}
		if voi.windowToByte(200) < 250 {
			t.Errorf("function %v at x=200 = %d, want near white", fn, voi.windowToByte(200))
		}
	}
}

func TestVOIWindowUnitMatchesByteOutputWithoutQuantizingIntermediateValue(t *testing.T) {
	for _, fn := range []VOIFunction{VOILinear, VOILinearExact, VOISigmoid} {
		voi := VOILUT{Center: 50, Width: 100, Function: fn}
		for _, value := range []float64{1, 25, 49.5, 50, 75, 99} {
			unit := voi.WindowUnit(value)
			if unit < 0 || unit > 1 {
				t.Fatalf("function %v value %v WindowUnit = %v, want [0,1]", fn, value, unit)
			}
			if got, want := uint8(math.Round(unit*255)), voi.WindowByte(value); got != want {
				t.Fatalf("function %v value %v normalized byte = %d, want %d", fn, value, got, want)
			}
		}
	}
}
