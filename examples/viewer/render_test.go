package main

import (
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestWindowPixelMapsCenterToMidGray(t *testing.T) {
	got := windowPixel(40, 40, 400)
	if got < 127 || got > 128 {
		t.Fatalf("windowPixel center = %d, want mid gray", got)
	}
}

func TestRenderUnsigned16Monochrome2(t *testing.T) {
	frame := testFrame([]uint16{0, 40, 240, 400}, false, "MONOCHROME2")
	img, err := renderFrame(frame, Window{Center: 40, Width: 400})
	if err != nil {
		t.Fatalf("renderFrame() error = %v", err)
	}
	if img.Pix[0] != 102 {
		t.Fatalf("first pixel = %d, want 102", img.Pix[0])
	}
	if img.Pix[1] < 127 || img.Pix[1] > 128 {
		t.Fatalf("center pixel = %d, want mid gray", img.Pix[1])
	}
	if img.Pix[3] != 255 {
		t.Fatalf("last pixel = %d, want 255", img.Pix[3])
	}
}

func TestRenderSigned16AppliesRescale(t *testing.T) {
	data := make([]byte, 4)
	low := int16(-1024)
	mid := int16(0)
	binary.LittleEndian.PutUint16(data[0:2], uint16(low))
	binary.LittleEndian.PutUint16(data[2:4], uint16(mid))
	frame := Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      1,
			Columns:                   2,
			SamplesPerPixel:           1,
			BitsAllocated:             16,
			BitsStored:                16,
			PixelRepresentation:       1,
			PhotometricInterpretation: "MONOCHROME2",
		},
		ByteOrder:  binary.LittleEndian,
		PixelBytes: data,
		Rescale:    Rescale{Slope: 1, Intercept: 1024},
	}
	img, err := renderFrame(frame, Window{Center: 1024, Width: 2048})
	if err != nil {
		t.Fatalf("renderFrame() error = %v", err)
	}
	if img.Pix[0] != 0 {
		t.Fatalf("rescaled low pixel = %d, want 0", img.Pix[0])
	}
	if img.Pix[1] < 127 || img.Pix[1] > 128 {
		t.Fatalf("rescaled center pixel = %d, want mid gray", img.Pix[1])
	}
}

func TestRenderMonochrome1Inverts(t *testing.T) {
	frame := testFrame([]uint16{0, 255}, false, "MONOCHROME1")
	img, err := renderFrame(frame, Window{Center: 128, Width: 256})
	if err != nil {
		t.Fatalf("renderFrame() error = %v", err)
	}
	if img.Pix[0] != 255 {
		t.Fatalf("first pixel = %d, want inverted white", img.Pix[0])
	}
	if img.Pix[1] != 0 {
		t.Fatalf("second pixel = %d, want inverted black", img.Pix[1])
	}
}

func TestStoredPixelValueSignExtendsBitsStored(t *testing.T) {
	data := []byte{0x00, 0x08}
	got := storedPixelValue(data, 16, 12, 11, true, binary.LittleEndian)
	if got != -2048 {
		t.Fatalf("storedPixelValue() = %d, want -2048", got)
	}
}

func TestStoredPixelValueUsesHighBit(t *testing.T) {
	data := []byte{0x00, 0xF0}
	got := storedPixelValue(data, 16, 12, 15, false, binary.LittleEndian)
	if got != 0xF00 {
		t.Fatalf("storedPixelValue() = %#x, want 0xf00", got)
	}
}

func testFrame(values []uint16, signed bool, photometric string) Frame {
	data := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(data[i*2:], value)
	}
	pixelRepresentation := uint16(0)
	if signed {
		pixelRepresentation = 1
	}
	return Frame{
		Metadata: pixeldata.Metadata{
			Rows:                      1,
			Columns:                   uint16(len(values)),
			SamplesPerPixel:           1,
			BitsAllocated:             16,
			BitsStored:                16,
			HighBit:                   15,
			PixelRepresentation:       pixelRepresentation,
			PhotometricInterpretation: photometric,
		},
		ByteOrder:  binary.LittleEndian,
		PixelBytes: data,
		Rescale:    Rescale{Slope: 1},
	}
}
