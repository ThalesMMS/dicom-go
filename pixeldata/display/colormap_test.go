package display

import "testing"

func TestHotIronLUTEndpointsAndMidpoint(t *testing.T) {
	lut := HotIronLUT()
	if r, g, b := lut.At(0); r != 0 || g != 0 || b != 0 {
		t.Fatalf("HotIron At(0) = %d,%d,%d, want black", r, g, b)
	}
	if r, g, b := lut.At(255); r != 255 || g != 255 || b != 255 {
		t.Fatalf("HotIron At(255) = %d,%d,%d, want white", r, g, b)
	}
	// Early ramp: red rising, green/blue still zero.
	if r, g, b := lut.At(40); r == 0 || g != 0 || b != 0 {
		t.Fatalf("HotIron At(40) = %d,%d,%d, want red>0, green=0, blue=0", r, g, b)
	}
	// Late ramp: red/green saturated, blue rising toward white.
	if r, g, b := lut.At(200); r != 255 || g != 255 || b == 0 {
		t.Fatalf("HotIron At(200) = %d,%d,%d, want red=255, green=255, blue>0", r, g, b)
	}
}

func TestHotMetalBlueLUTEndpointsAndMidpoint(t *testing.T) {
	lut := HotMetalBlueLUT()
	if r, g, b := lut.At(0); r != 0 || g != 0 || b != 0 {
		t.Fatalf("HotMetalBlue At(0) = %d,%d,%d, want black", r, g, b)
	}
	if r, g, b := lut.At(255); r != 255 || g != 255 || b != 0 {
		t.Fatalf("HotMetalBlue At(255) = %d,%d,%d, want yellow (255,255,0)", r, g, b)
	}
	// Early ramp: blue rising, red/green still zero.
	if r, g, b := lut.At(30); r != 0 || g != 0 || b == 0 {
		t.Fatalf("HotMetalBlue At(30) = %d,%d,%d, want red=0, green=0, blue>0", r, g, b)
	}
}

func TestPaletteColorLUTAtNilSafety(t *testing.T) {
	var lut *PaletteColorLUT
	if r, g, b := lut.At(10); r != 0 || g != 0 || b != 0 {
		t.Fatalf("nil PaletteColorLUT.At() = %d,%d,%d, want 0,0,0", r, g, b)
	}
	incomplete := &PaletteColorLUT{Red: uint8RampLUT(hotIronRed)}
	if r, g, b := incomplete.At(10); r != 0 || g != 0 || b != 0 {
		t.Fatalf("incomplete PaletteColorLUT.At() = %d,%d,%d, want 0,0,0", r, g, b)
	}
}

func TestUint8RampLUTHas256Entries(t *testing.T) {
	lut := uint8RampLUT(hotIronRed)
	if len(lut.Entries) != 256 {
		t.Fatalf("ramp LUT entries = %d, want 256", len(lut.Entries))
	}
	if lut.BitsPerEntry != 8 || lut.FirstMapped != 0 {
		t.Fatalf("ramp LUT descriptor = FirstMapped=%d BitsPerEntry=%d, want 0/8", lut.FirstMapped, lut.BitsPerEntry)
	}
}
