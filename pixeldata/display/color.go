package display

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
)

var (
	// ErrUnsupportedColorPhotometric reports a color Photometric Interpretation
	// the pipeline does not convert (e.g. YBR_PARTIAL_420, YBR_ICT, YBR_RCT).
	ErrUnsupportedColorPhotometric = errors.New("dicom/display: unsupported color photometric interpretation")
	// ErrUnsupportedColorLayout reports a color pixel layout the pipeline cannot
	// interpret (e.g. non-8-bit RGB/YBR samples, odd-width 4:2:2, wrong samples
	// per pixel) so callers avoid producing misleading images.
	ErrUnsupportedColorLayout = errors.New("dicom/display: unsupported color pixel layout")
	// ErrMissingPalette reports a PALETTE COLOR frame without red/green/blue
	// palette LUTs.
	ErrMissingPalette = errors.New("dicom/display: PALETTE COLOR frame is missing palette LUTs")
	// ErrUnsupportedSegmentedPalette reports a segmented palette color LUT
	// construct outside the supported discrete/linear/indirect segment set.
	ErrUnsupportedSegmentedPalette = errors.New("dicom/display: unsupported segmented palette color LUT")
)

// PaletteColorLUT holds the red, green, and blue palette color lookup tables of
// a PALETTE COLOR image (PS3.3 C.7.6.3.1.5).
type PaletteColorLUT struct {
	Red, Green, Blue *LUT
}

// ColorFrame is the input to the color display pipeline: one frame's stored
// pixel bytes plus the attributes needed to convert them to RGB.
type ColorFrame struct {
	Rows, Columns       int
	Pixels              []byte
	Photometric         string
	SamplesPerPixel     int
	PlanarConfiguration int // 0 interleaved, 1 planar (color-by-plane)
	Format              PixelFormat
	// Palette is required for PALETTE COLOR images and ignored otherwise.
	Palette *PaletteColorLUT
}

// RenderColor converts a color frame to an RGBA image. Supported photometric
// interpretations are RGB (interleaved or planar), PALETTE COLOR, YBR_FULL
// (interleaved or planar), and YBR_FULL_422 (interleaved). Other layouts return
// a typed error rather than a misleading image.
func RenderColor(frame ColorFrame) (*image.RGBA, error) {
	rows, cols := frame.Rows, frame.Columns
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("%w: Rows=%d Columns=%d", ErrInvalidFrame, rows, cols)
	}
	switch normalizePhotometric(frame.Photometric) {
	case "PALETTE COLOR":
		return renderPalette(frame, rows, cols)
	case "RGB":
		return renderThreeSample(frame, rows, cols, passthroughRGB)
	case "YBR_FULL":
		return renderThreeSample(frame, rows, cols, ybrFullToRGB)
	case "YBR_FULL_422":
		return renderYBR422(frame, rows, cols)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedColorPhotometric, frame.Photometric)
	}
}

// renderThreeSample renders an interleaved or planar 3-sample image, applying a
// per-pixel sample conversion (identity for RGB, YBR conversion for YBR_FULL).
func renderThreeSample(frame ColorFrame, rows, cols int, convert func(a, b, c uint8) (uint8, uint8, uint8)) (*image.RGBA, error) {
	if frame.SamplesPerPixel != 3 {
		return nil, fmt.Errorf("%w: SamplesPerPixel=%d, want 3", ErrUnsupportedColorLayout, frame.SamplesPerPixel)
	}
	if frame.Format.BitsAllocated != 8 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d, want 8", ErrUnsupportedColorLayout, frame.Format.BitsAllocated)
	}
	n := rows * cols
	if len(frame.Pixels) < n*3 {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(frame.Pixels), n*3)
	}

	planar := frame.PlanarConfiguration == 1
	out := image.NewRGBA(image.Rect(0, 0, cols, rows))
	for i := 0; i < n; i++ {
		var s0, s1, s2 uint8
		if planar {
			s0 = frame.Pixels[i]
			s1 = frame.Pixels[n+i]
			s2 = frame.Pixels[2*n+i]
		} else {
			s0 = frame.Pixels[i*3]
			s1 = frame.Pixels[i*3+1]
			s2 = frame.Pixels[i*3+2]
		}
		r, g, b := convert(s0, s1, s2)
		dst := i * 4
		out.Pix[dst] = r
		out.Pix[dst+1] = g
		out.Pix[dst+2] = b
		out.Pix[dst+3] = 255
	}
	return out, nil
}

// renderYBR422 renders interleaved YBR_FULL_422 data, where chroma is subsampled
// horizontally by two and each pixel pair is stored as Y0 Y1 Cb Cr.
func renderYBR422(frame ColorFrame, rows, cols int) (*image.RGBA, error) {
	if frame.Format.BitsAllocated != 8 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d, want 8", ErrUnsupportedColorLayout, frame.Format.BitsAllocated)
	}
	if cols%2 != 0 {
		return nil, fmt.Errorf("%w: YBR_FULL_422 requires an even column count, got %d", ErrUnsupportedColorLayout, cols)
	}
	expected := rows * cols * 2 // 4 bytes per 2 pixels
	if len(frame.Pixels) < expected {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(frame.Pixels), expected)
	}

	out := image.NewRGBA(image.Rect(0, 0, cols, rows))
	src := 0
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col += 2 {
			y0 := frame.Pixels[src]
			y1 := frame.Pixels[src+1]
			cb := frame.Pixels[src+2]
			cr := frame.Pixels[src+3]
			src += 4
			r0, g0, b0 := ybrFullToRGB(y0, cb, cr)
			r1, g1, b1 := ybrFullToRGB(y1, cb, cr)
			setRGBA(out, row*cols+col, r0, g0, b0)
			setRGBA(out, row*cols+col+1, r1, g1, b1)
		}
	}
	return out, nil
}

// renderPalette converts a PALETTE COLOR frame to RGB by indexing the stored
// pixel value into the red/green/blue palette LUTs.
func renderPalette(frame ColorFrame, rows, cols int) (*image.RGBA, error) {
	if frame.Palette == nil || frame.Palette.Red == nil || frame.Palette.Green == nil || frame.Palette.Blue == nil {
		return nil, ErrMissingPalette
	}
	bits := frame.Format.BitsAllocated
	if bits != 8 && bits != 16 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedBitsAllocated, bits)
	}
	bytesPerSample := bits / 8
	n := rows * cols
	if len(frame.Pixels) < n*bytesPerSample {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(frame.Pixels), n*bytesPerSample)
	}
	order := frame.Format.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}

	out := image.NewRGBA(image.Rect(0, 0, cols, rows))
	reader := newStoredPixelReader(frame.Format, order)
	for i := 0; i < n; i++ {
		stored := int(reader.value(frame.Pixels[i*bytesPerSample:]))
		r := paletteChannel(frame.Palette.Red, stored)
		g := paletteChannel(frame.Palette.Green, stored)
		b := paletteChannel(frame.Palette.Blue, stored)
		setRGBA(out, i, r, g, b)
	}
	return out, nil
}

// paletteChannel looks up a palette entry and scales it to 8 bits. 16-bit
// palette entries use the high byte (PS3.3 C.7.6.3.1.5).
func paletteChannel(lut *LUT, value int) uint8 {
	entry := lut.Lookup(value)
	if lut.BitsPerEntry == 16 {
		return uint8(entry >> 8)
	}
	return uint8(entry)
}

func setRGBA(img *image.RGBA, pixelIndex int, r, g, b uint8) {
	dst := pixelIndex * 4
	img.Pix[dst] = r
	img.Pix[dst+1] = g
	img.Pix[dst+2] = b
	img.Pix[dst+3] = 255
}

func passthroughRGB(r, g, b uint8) (uint8, uint8, uint8) { return r, g, b }

// ybrFullToRGB converts a full-range YBR triplet to RGB (PS3.3 C.7.6.3.1.2).
func ybrFullToRGB(y, cb, cr uint8) (uint8, uint8, uint8) {
	yf := float64(y)
	cbf := float64(cb) - 128
	crf := float64(cr) - 128
	r := yf + 1.402*crf
	g := yf - 0.344136*cbf - 0.714136*crf
	b := yf + 1.772*cbf
	return clampByte(r), clampByte(g), clampByte(b)
}

func clampByte(v float64) uint8 {
	return uint8(math.Round(clampFloat(v, 0, 255)))
}

func normalizePhotometric(value string) string {
	return strings.Trim(strings.ToUpper(value), " \x00")
}
