// Package display implements the DICOM grayscale display pipeline as a small,
// staged interface that callers can use without sequencing the individual
// transforms themselves.
//
// The pipeline follows the order defined by DICOM PS3.3 C.11 and PS3.4 Annex N:
//
//	stored pixel value -> Modality transform -> VOI transform -> display value
//
// The Modality transform is either a linear Rescale Slope/Intercept or a
// Modality LUT Sequence; the VOI transform is either a Window Center/Width with
// a window function or a VOI LUT Sequence. Presentation-stage behavior
// (MONOCHROME1 inversion, Presentation LUT, overlays, shutters) and color paths
// (palette, YBR) are layered on top of this package by later work; this first
// slice covers grayscale modality and VOI handling with MONOCHROME2 output.
package display

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
)

var (
	// ErrInvalidFrame reports geometry or pixel-format values that cannot
	// describe a renderable grayscale frame.
	ErrInvalidFrame = errors.New("dicom/display: invalid frame metadata")
	// ErrUnsupportedBitsAllocated reports a BitsAllocated the requested stage
	// does not handle. Modality decoding supports 8, 16, and 32 bits; RenderGray
	// intentionally remains limited to 8 and 16 bits.
	ErrUnsupportedBitsAllocated = errors.New("dicom/display: unsupported BitsAllocated")
	// ErrPixelDataTooShort reports pixel data shorter than the frame requires.
	ErrPixelDataTooShort = errors.New("dicom/display: pixel data too short for frame")
	// ErrDestinationTooShort reports a caller-provided modality buffer that
	// cannot hold one value for every frame pixel.
	ErrDestinationTooShort = errors.New("dicom/display: modality destination too short")
)

// PixelFormat describes how to read a single stored sample from a frame's pixel
// bytes. It mirrors the DICOM Image Pixel module attributes that govern stored
// value interpretation.
type PixelFormat struct {
	BitsAllocated int
	BitsStored    int
	HighBit       int
	// Signed reports whether stored values are two's-complement (Pixel
	// Representation 1).
	Signed bool
	// ByteOrder is the byte order of multi-byte samples; nil defaults to
	// little-endian.
	ByteOrder binary.ByteOrder
}

// Frame is the input to the grayscale display pipeline: one frame's stored
// pixel bytes plus the transforms to apply.
type Frame struct {
	Rows    int
	Columns int
	// Pixels holds one frame of stored, single-sample grayscale pixel data.
	Pixels []byte
	Format PixelFormat
	// Modality maps stored values to modality values.
	Modality ModalityLUT
	// VOI maps modality values to display values. When neither a window nor a
	// VOI LUT is set, a full-range default window is derived from the pixel
	// format and modality transform.
	VOI VOILUT
}

// Pipeline renders DICOM pixel data to a displayable image, applying the
// modality, VOI, and color transforms in DICOM-defined order so callers need
// not sequence the transforms themselves.
type Pipeline interface {
	RenderGray(Frame) (*image.Gray, error)
	RenderColor(ColorFrame) (*image.RGBA, error)
}

// pipeline is the default Pipeline implementation.
type pipeline struct{}

// New returns the default display Pipeline.
func New() Pipeline { return pipeline{} }

// RenderGray renders frame through the default pipeline.
func (pipeline) RenderGray(frame Frame) (*image.Gray, error) {
	return RenderGray(frame)
}

// RenderColor renders a color frame through the default pipeline.
func (pipeline) RenderColor(frame ColorFrame) (*image.RGBA, error) {
	return RenderColor(frame)
}

// RenderGray applies the modality and VOI transforms to a grayscale frame and
// returns an 8-bit image using the MONOCHROME2 convention (larger display value
// is brighter). MONOCHROME1 inversion is a presentation-stage concern handled
// by callers/later pipeline stages.
func RenderGray(frame Frame) (*image.Gray, error) {
	rows, cols := frame.Rows, frame.Columns
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("%w: Rows=%d Columns=%d", ErrInvalidFrame, rows, cols)
	}
	bitsAllocated := frame.Format.BitsAllocated
	if bitsAllocated != 8 && bitsAllocated != 16 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedBitsAllocated, bitsAllocated)
	}

	bytesPerSample := bitsAllocated / 8
	expected := rows * cols * bytesPerSample
	if len(frame.Pixels) < expected {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(frame.Pixels), expected)
	}

	order := frame.Format.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}

	voi := frame.VOI
	if voi.LUT == nil && !voi.hasWindow() {
		// Derive a default full-range window when none is supplied.
		voi = defaultVOI(frame.Format, frame.Modality)
	}
	mapper := NewVOIByteMapper(voi)

	out := image.NewGray(image.Rect(0, 0, cols, rows))
	reader := newStoredPixelReader(frame.Format, order)
	if frame.Modality.LUT != nil {
		for pixelIndex := 0; pixelIndex < rows*cols; pixelIndex++ {
			offset := pixelIndex * bytesPerSample
			modality := float64(frame.Modality.LUT.lookupInt64(reader.value(frame.Pixels[offset:])))
			out.Pix[pixelIndex] = mapper.Byte(modality)
		}
	} else {
		slope := frame.Modality.Slope
		if slope == 0 {
			slope = 1
		}
		intercept := frame.Modality.Intercept
		for pixelIndex := 0; pixelIndex < rows*cols; pixelIndex++ {
			offset := pixelIndex * bytesPerSample
			modality := float64(reader.value(frame.Pixels[offset:]))*slope + intercept
			out.Pix[pixelIndex] = mapper.Byte(modality)
		}
	}
	return out, nil
}

// DecodeModality extracts each stored pixel of a grayscale frame and applies the
// modality transform, returning one modality value per pixel in row-major order.
// It is the decode half of the grayscale pipeline, exposed for callers (such as
// render caches) that window the same modality values repeatedly without
// re-extracting stored pixels.
func DecodeModality(frame Frame) ([]float64, error) {
	return decodeModality[float64](frame)
}

// DecodeModalityFloat32 is DecodeModality with float32 output. It is intended
// for memory-sensitive render caches whose downstream calculations already use
// float32 precision, avoiding a transient float64 buffer and conversion pass.
func DecodeModalityFloat32(frame Frame) ([]float32, error) {
	return decodeModality[float32](frame)
}

// DecodeModalityFloat32Into is DecodeModalityFloat32 without an output
// allocation. It writes Rows*Columns values into destination and leaves any
// remaining capacity untouched.
func DecodeModalityFloat32Into(destination []float32, frame Frame) error {
	return decodeModalityInto(destination, frame)
}

func decodeModality[T ~float32 | ~float64](frame Frame) ([]T, error) {
	count, err := modalitySampleCount(frame)
	if err != nil {
		return nil, err
	}
	out := make([]T, count)
	if err := decodeModalityInto(out, frame); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeModalityInto[T ~float32 | ~float64](out []T, frame Frame) error {
	count, err := modalitySampleCount(frame)
	if err != nil {
		return err
	}
	if len(out) < count {
		return fmt.Errorf("%w: got %d values, want %d", ErrDestinationTooShort, len(out), count)
	}
	out = out[:count]
	bitsAllocated := frame.Format.BitsAllocated
	bytesPerSample := bitsAllocated / 8
	order := frame.Format.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	reader := newStoredPixelReader(frame.Format, order)
	if frame.Modality.LUT != nil {
		for i := range out {
			stored := reader.value(frame.Pixels[i*bytesPerSample:])
			out[i] = T(frame.Modality.LUT.lookupInt64(stored))
		}
	} else {
		slope := frame.Modality.Slope
		if slope == 0 {
			slope = 1
		}
		intercept := frame.Modality.Intercept
		for i := range out {
			stored := reader.value(frame.Pixels[i*bytesPerSample:])
			out[i] = T(float64(stored)*slope + intercept)
		}
	}
	return nil
}

func modalitySampleCount(frame Frame) (int, error) {
	rows, cols := frame.Rows, frame.Columns
	if rows <= 0 || cols <= 0 {
		return 0, fmt.Errorf("%w: Rows=%d Columns=%d", ErrInvalidFrame, rows, cols)
	}
	bitsAllocated := frame.Format.BitsAllocated
	if bitsAllocated != 8 && bitsAllocated != 16 && bitsAllocated != 32 {
		return 0, fmt.Errorf("%w: %d", ErrUnsupportedBitsAllocated, bitsAllocated)
	}
	bytesPerSample := bitsAllocated / 8
	expected := rows * cols * bytesPerSample
	if len(frame.Pixels) < expected {
		return 0, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(frame.Pixels), expected)
	}
	return rows * cols, nil
}

// scaleToByte linearly maps a VOI LUT output value in [min, min+span] to the
// 8-bit display range. A zero span (constant LUT) maps everything to black.
func scaleToByte(value, minValue, span float64) uint8 {
	if span <= 0 {
		return 0
	}
	scaled := (value - minValue) / span * 255
	return uint8(math.Round(clampFloat(scaled, 0, 255)))
}

// storedPixelValue extracts one stored sample, honoring BitsStored, HighBit,
// and the signed/unsigned representation.
func storedPixelValue(data []byte, format PixelFormat, order binary.ByteOrder) int64 {
	reader := newStoredPixelReader(format, order)
	return reader.value(data)
}

type storedPixelReader struct {
	bitsAllocated uint16
	shift         uint16
	mask          uint32
	signBit       uint32
	signExtension uint32
	order         binary.ByteOrder
}

func newStoredPixelReader(format PixelFormat, order binary.ByteOrder) storedPixelReader {
	bitsAllocated := uint16(format.BitsAllocated)
	bitsStored := uint16(format.BitsStored)
	highBit := uint16(format.HighBit)
	if bitsStored == 0 || bitsStored > bitsAllocated {
		bitsStored = bitsAllocated
	}
	if highBit+1 < bitsStored || highBit >= bitsAllocated {
		highBit = bitsStored - 1
	}
	shift := highBit + 1 - bitsStored
	reader := storedPixelReader{
		bitsAllocated: bitsAllocated,
		shift:         shift,
		mask:          (uint32(1) << bitsStored) - 1,
		order:         order,
	}
	if format.Signed {
		reader.signBit = uint32(1) << (bitsStored - 1)
		reader.signExtension = ^uint32(0) << bitsStored
	}
	return reader
}

func (r *storedPixelReader) value(data []byte) int64 {
	var raw uint32
	switch r.bitsAllocated {
	case 8:
		raw = uint32(data[0])
	case 16:
		raw = uint32(r.order.Uint16(data[:2]))
	case 32:
		raw = r.order.Uint32(data[:4])
	}
	raw = (raw >> r.shift) & r.mask
	if r.signBit != 0 && raw&r.signBit != 0 {
		raw |= r.signExtension
	}
	if r.signBit != 0 {
		return int64(int32(raw))
	}
	return int64(raw)
}

// defaultVOI derives a full-range linear window from the pixel format and
// modality transform, used when no Window Center/Width or VOI LUT is present.
func defaultVOI(format PixelFormat, modality ModalityLUT) VOILUT {
	bitsStored := format.BitsStored
	if bitsStored <= 0 || bitsStored > format.BitsAllocated {
		bitsStored = format.BitsAllocated
	}
	if bitsStored <= 0 {
		return VOILUT{Center: 127.5, Width: 256}
	}

	var minStored, maxStored float64
	if format.Signed {
		half := math.Pow(2, float64(bitsStored-1))
		minStored = -half
		maxStored = half - 1
	} else {
		minStored = 0
		maxStored = math.Pow(2, float64(bitsStored)) - 1
	}

	minValue := modality.Apply(int(minStored))
	maxValue := modality.Apply(int(maxStored))
	if minValue > maxValue {
		minValue, maxValue = maxValue, minValue
	}
	return VOILUT{
		Center: (minValue + maxValue) / 2,
		Width:  maxValue - minValue + 1,
	}
}
