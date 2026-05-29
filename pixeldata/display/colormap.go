package display

// At maps a stored pixel value through the palette's red/green/blue LUTs and
// returns the resulting 8-bit RGB color. It is the same per-channel lookup
// renderPalette uses for PALETTE COLOR images, exposed directly for callers
// (e.g. a false-color overlay) that already have a single windowed/scaled
// value per pixel and don't need the full ColorFrame/RenderColor pipeline.
func (p *PaletteColorLUT) At(value int) (r, g, b uint8) {
	if p == nil || p.Red == nil || p.Green == nil || p.Blue == nil {
		return 0, 0, 0
	}
	return paletteChannel(p.Red, value), paletteChannel(p.Green, value), paletteChannel(p.Blue, value)
}

// HotIronLUT returns an approximate "Hot Iron" false-color palette (black ->
// red -> yellow -> white) commonly used for PET activity overlays, as an
// 8-bit-indexed (0-255) PaletteColorLUT. This is a generated three-segment
// ramp in the visual style associated with the DICOM Hot Iron Palette Color
// LUT SOP Instance (dictionary/uid.HotIronPalette), not a byte-for-byte
// reproduction of that Supplement 24 reference palette.
func HotIronLUT() *PaletteColorLUT {
	return &PaletteColorLUT{
		Red:   uint8RampLUT(hotIronRed),
		Green: uint8RampLUT(hotIronGreen),
		Blue:  uint8RampLUT(hotIronBlue),
	}
}

// HotMetalBlueLUT returns an approximate "Hot Metal Blue" false-color palette
// (black -> blue -> magenta -> red/orange -> yellow), a common alternative
// PET overlay palette that keeps low-activity regions visually distinct from
// a red/orange CT-fusion base. Like HotIronLUT, this is a generated
// approximation, not the exact DICOM reference palette
// (dictionary/uid.HotMetalBluePalette).
func HotMetalBlueLUT() *PaletteColorLUT {
	return &PaletteColorLUT{
		Red:   uint8RampLUT(hotMetalBlueRed),
		Green: uint8RampLUT(hotMetalBlueGreen),
		Blue:  uint8RampLUT(hotMetalBlueBlue),
	}
}

func uint8RampLUT(at func(i int) uint8) *LUT {
	entries := make([]uint16, 256)
	for i := range entries {
		entries[i] = uint16(at(i))
	}
	return &LUT{FirstMapped: 0, BitsPerEntry: 8, Entries: entries}
}

// hotIronRed/Green/Blue implement the classic three-segment "hot" ramp:
// black -> red (0-84) -> yellow (85-169) -> white (170-255).
func hotIronRed(i int) uint8 {
	if i < 85 {
		return uint8(i * 3)
	}
	return 255
}

func hotIronGreen(i int) uint8 {
	switch {
	case i < 85:
		return 0
	case i < 170:
		return uint8((i - 85) * 3)
	default:
		return 255
	}
}

func hotIronBlue(i int) uint8 {
	if i < 170 {
		return 0
	}
	return uint8((i - 170) * 3)
}

// hotMetalBlueRed/Green/Blue implement a four-segment ramp: black -> blue
// (0-63) -> magenta -> red/orange (64-191) -> yellow (192-255).
func hotMetalBlueRed(i int) uint8 {
	switch {
	case i < 64:
		return 0
	case i < 192:
		return uint8((i - 64) * 255 / 128)
	default:
		return 255
	}
}

func hotMetalBlueGreen(i int) uint8 {
	if i < 192 {
		return 0
	}
	return uint8((i - 192) * 255 / 63)
}

func hotMetalBlueBlue(i int) uint8 {
	switch {
	case i < 64:
		return uint8(i * 255 / 63)
	case i < 128:
		return uint8(255 - (i-64)*255/63)
	default:
		return 0
	}
}
