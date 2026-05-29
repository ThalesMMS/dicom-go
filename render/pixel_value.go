package render

import (
	"encoding/binary"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

type PixelValue struct {
	X        int
	Y        int
	Stored   int32
	Rescaled float64
}

func PixelValueAt(slice *Frame, x, y int) (PixelValue, bool) {
	if slice == nil {
		return PixelValue{}, false
	}
	return SamplePixelValue(&slice.Metadata, slice.ByteOrder, slice.PixelBytes, slice.Rescale, x, y)
}

// SamplePixelValue samples one single-channel native pixel without constructing
// a Frame. It exists for adapters that already own the frame fields and need a
// hot path for MPR/VR sampling.
func SamplePixelValue(metadata *pixeldata.Metadata, order binary.ByteOrder, pixelBytes []byte, rescale Rescale, x, y int) (PixelValue, bool) {
	if metadata == nil || x < 0 || y < 0 {
		return PixelValue{}, false
	}
	if metadata.SamplesPerPixel != 1 {
		return PixelValue{}, false
	}
	cols := int(metadata.Columns)
	rows := int(metadata.Rows)
	if cols <= 0 || rows <= 0 || x >= cols || y >= rows {
		return PixelValue{}, false
	}
	bytesPerSample := int(metadata.BitsAllocated / 8)
	if bytesPerSample != 1 && bytesPerSample != 2 {
		return PixelValue{}, false
	}
	offset := (y*cols + x) * bytesPerSample
	if offset < 0 || offset+bytesPerSample > len(pixelBytes) {
		return PixelValue{}, false
	}
	if order == nil {
		order = binary.LittleEndian
	}
	stored := storedPixelValue(pixelBytes[offset:], metadata.BitsAllocated, metadata.BitsStored, metadata.HighBit, metadata.PixelRepresentation != 0, order)
	slope := rescale.Slope
	if slope == 0 || math.IsNaN(slope) || math.IsInf(slope, 0) {
		slope = 1
	}
	intercept := rescale.Intercept
	if math.IsNaN(intercept) || math.IsInf(intercept, 0) {
		intercept = 0
	}
	return PixelValue{
		X:        x,
		Y:        y,
		Stored:   stored,
		Rescaled: float64(stored)*slope + intercept,
	}, true
}

// AutoWindow computes a full-dynamic window/level from a slice's actual pixel
// value range (rescaled): center at the midpoint, width spanning min→max. Used by
// the windowing "Full dynamic" action (toolbar-plan.md §5.12). Returns false when
// the slice has no readable single-sample pixels or a degenerate range.
func AutoWindow(slice *Frame) (WindowLevel, bool) {
	if slice == nil {
		return WindowLevel{}, false
	}
	if slice.Metadata.SamplesPerPixel != 1 {
		return WindowLevel{}, false
	}
	cols := int(slice.Metadata.Columns)
	rows := int(slice.Metadata.Rows)
	if cols <= 0 || rows <= 0 {
		return WindowLevel{}, false
	}
	bytesPerSample := int(slice.Metadata.BitsAllocated / 8)
	if bytesPerSample != 1 && bytesPerSample != 2 {
		return WindowLevel{}, false
	}
	order := slice.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	slope := slice.Rescale.Slope
	if slope == 0 || math.IsNaN(slope) || math.IsInf(slope, 0) {
		slope = 1
	}
	intercept := slice.Rescale.Intercept
	if math.IsNaN(intercept) || math.IsInf(intercept, 0) {
		intercept = 0
	}

	min := math.Inf(1)
	max := math.Inf(-1)
	found := false
	pixels := rows * cols
	for i := 0; i < pixels; i++ {
		offset := i * bytesPerSample
		if offset < 0 || offset+bytesPerSample > len(slice.PixelBytes) {
			break
		}
		stored := storedPixelValue(slice.PixelBytes[offset:], slice.Metadata.BitsAllocated, slice.Metadata.BitsStored, slice.Metadata.HighBit, slice.Metadata.PixelRepresentation != 0, order)
		rescaled := float64(stored)*slope + intercept
		found = true
		if rescaled < min {
			min = rescaled
		}
		if rescaled > max {
			max = rescaled
		}
	}
	if !found || max <= min {
		return WindowLevel{}, false
	}
	return WindowLevel{Center: (min + max) / 2, Width: max - min}, true
}

func storedPixelValue(data []byte, bitsAllocated, bitsStored, highBit uint16, signed bool, order binary.ByteOrder) int32 {
	if bitsAllocated != 8 && bitsAllocated != 16 {
		return 0
	}
	if bitsStored == 0 || bitsStored > bitsAllocated {
		bitsStored = bitsAllocated
	}
	if highBit+1 < bitsStored || highBit >= bitsAllocated {
		highBit = bitsStored - 1
	}
	var raw uint32
	if bitsAllocated == 8 {
		raw = uint32(data[0])
	} else {
		raw = uint32(order.Uint16(data[:2]))
	}
	shift := highBit + 1 - bitsStored
	raw = (raw >> shift) & ((1 << bitsStored) - 1)
	if signed && bitsStored > 0 {
		signBit := uint32(1) << (bitsStored - 1)
		if raw&signBit != 0 {
			raw |= ^uint32(0) << bitsStored
		}
	}
	return int32(raw)
}
