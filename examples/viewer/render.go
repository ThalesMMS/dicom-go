package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
)

func renderFrame(frame Frame, window Window) (*image.Gray, error) {
	if frame.DecodeErr != nil {
		return blankImage(512, 512), frame.DecodeErr
	}
	metadata := frame.Metadata
	rows := int(metadata.Rows)
	cols := int(metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return blankImage(512, 512), fmt.Errorf("viewer: invalid frame size %dx%d", cols, rows)
	}
	if metadata.SamplesPerPixel != 1 {
		return blankImage(cols, rows), fmt.Errorf("viewer: SamplesPerPixel=%d is not supported", metadata.SamplesPerPixel)
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return blankImage(cols, rows), fmt.Errorf("viewer: BitsAllocated=%d is not supported", metadata.BitsAllocated)
	}
	photometric := normalizedPhotometric(metadata.PhotometricInterpretation)
	if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
		return blankImage(cols, rows), fmt.Errorf("viewer: PhotometricInterpretation=%q is not supported", metadata.PhotometricInterpretation)
	}

	bytesPerSample := int(metadata.BitsAllocated / 8)
	expected := rows * cols * bytesPerSample
	if len(frame.PixelBytes) < expected {
		return blankImage(cols, rows), fmt.Errorf("viewer: pixel data too short: got %d bytes, want %d", len(frame.PixelBytes), expected)
	}

	order := frame.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	signed := metadata.PixelRepresentation != 0
	invert := photometric == "MONOCHROME1"
	out := image.NewGray(image.Rect(0, 0, cols, rows))

	for pixelIndex := 0; pixelIndex < rows*cols; pixelIndex++ {
		offset := pixelIndex * bytesPerSample
		stored := storedPixelValue(frame.PixelBytes[offset:], metadata.BitsAllocated, metadata.BitsStored, metadata.HighBit, signed, order)
		modality := float64(stored)*frame.Rescale.Slope + frame.Rescale.Intercept
		gray := windowPixel(modality, window.Center, window.Width)
		if invert {
			gray = 255 - gray
		}
		out.Pix[pixelIndex] = gray
	}

	return out, nil
}

func storedPixelValue(data []byte, bitsAllocated, bitsStored, highBit uint16, signed bool, order binary.ByteOrder) int32 {
	var raw uint32
	switch bitsAllocated {
	case 8:
		raw = uint32(data[0])
	case 16:
		raw = uint32(order.Uint16(data[:2]))
	}
	if bitsStored == 0 || bitsStored > bitsAllocated {
		bitsStored = bitsAllocated
	}
	if highBit+1 < bitsStored || highBit >= bitsAllocated {
		highBit = bitsStored - 1
	}
	shift := highBit + 1 - bitsStored
	raw >>= shift
	raw &= (uint32(1) << bitsStored) - 1
	if signed {
		signBit := uint32(1) << (bitsStored - 1)
		if raw&signBit != 0 {
			raw |= ^uint32(0) << bitsStored
		}
	}
	return int32(raw)
}

func windowPixel(value, center, width float64) uint8 {
	if width <= 1 {
		if value > center {
			return 255
		}
		return 0
	}

	low := center - 0.5 - (width-1)/2
	high := center - 0.5 + (width-1)/2
	switch {
	case value <= low:
		return 0
	case value > high:
		return 255
	default:
		scaled := ((value-(center-0.5))/(width-1) + 0.5) * 255
		return uint8(math.Round(clampFloat(scaled, 0, 255)))
	}
}

func normalizedPhotometric(value string) string {
	return strings.Trim(strings.ToUpper(value), " \x00")
}

func blankImage(width, height int) *image.Gray {
	if width <= 0 {
		width = 512
	}
	if height <= 0 {
		height = 512
	}
	img := image.NewGray(image.Rect(0, 0, width, height))
	fill := color.Gray{Y: 24}
	for i := range img.Pix {
		img.Pix[i] = fill.Y
	}
	return img
}

func clampFloat(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}

func validateWindow(window Window) error {
	if math.IsNaN(window.Center) || math.IsInf(window.Center, 0) {
		return errors.New("viewer: window center must be finite")
	}
	if math.IsNaN(window.Width) || math.IsInf(window.Width, 0) || window.Width <= 0 {
		return errors.New("viewer: window width must be positive and finite")
	}
	return nil
}
