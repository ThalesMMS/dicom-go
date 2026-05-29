package nifti

import (
	"context"
	"encoding/binary"
	"io"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func encodeStoredFrame(ctx context.Context, raw []byte, metadata pixeldata.Metadata, order binary.ByteOrder, transform linearTransform, datatype int16) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if order == nil {
		order = binary.LittleEndian
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated ||
		metadata.HighBit >= metadata.BitsAllocated || metadata.HighBit+1 < metadata.BitsStored {
		return nil, ErrUnsupportedPixels
	}
	width := int(metadata.BitsAllocated / 8)
	count64, ok := checkedMul(uint64(metadata.Rows), uint64(metadata.Columns))
	if !ok || count64 > uint64(math.MaxInt) || width <= 0 {
		return nil, ErrUnsupportedPixels
	}
	count := int(count64)
	want, ok := checkedMul(count64, uint64(width))
	if !ok || want != uint64(len(raw)) {
		return nil, ErrUnsupportedPixels
	}
	outWidth, ok := datatypeBytes(datatype)
	if !ok || count > math.MaxInt/outWidth {
		return nil, ErrUnsupportedPixels
	}
	out := make([]byte, count*outWidth)
	shift := metadata.HighBit + 1 - metadata.BitsStored
	mask := uint64(math.MaxUint64)
	if metadata.BitsStored < 64 {
		mask = uint64(1)<<metadata.BitsStored - 1
	}
	signBit := uint64(0)
	if metadata.PixelRepresentation == 1 {
		signBit = uint64(1) << (metadata.BitsStored - 1)
	}
	for index := 0; index < count; index++ {
		if index&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		source := raw[index*width:]
		var unsigned uint64
		switch width {
		case 1:
			unsigned = uint64(source[0])
		case 2:
			unsigned = uint64(order.Uint16(source))
		case 4:
			unsigned = uint64(order.Uint32(source))
		default:
			return nil, ErrUnsupportedPixels
		}
		unsigned = (unsigned >> shift) & mask
		signed := int64(unsigned)
		if signBit != 0 && unsigned&signBit != 0 {
			signed = int64(unsigned | ^mask)
		}
		destination := out[index*outWidth:]
		switch datatype {
		case DatatypeUint8:
			destination[0] = byte(unsigned)
		case DatatypeInt8:
			destination[0] = byte(int8(signed))
		case DatatypeUint16:
			binary.LittleEndian.PutUint16(destination, uint16(unsigned))
		case DatatypeInt16:
			binary.LittleEndian.PutUint16(destination, uint16(int16(signed)))
		case DatatypeUint32:
			binary.LittleEndian.PutUint32(destination, uint32(unsigned))
		case DatatypeInt32:
			binary.LittleEndian.PutUint32(destination, uint32(int32(signed)))
		case DatatypeFloat32:
			value := storedValueFloat(unsigned, signed, metadata.PixelRepresentation == 1)*transform.slope + transform.intercept
			converted := float32(value)
			if !finite(value) || math.IsNaN(float64(converted)) || math.IsInf(float64(converted), 0) {
				return nil, ErrUnsupportedScaling
			}
			binary.LittleEndian.PutUint32(destination, math.Float32bits(converted))
		case DatatypeFloat64:
			value := storedValueFloat(unsigned, signed, metadata.PixelRepresentation == 1)*transform.slope + transform.intercept
			if !finite(value) {
				return nil, ErrUnsupportedScaling
			}
			binary.LittleEndian.PutUint64(destination, math.Float64bits(value))
		default:
			return nil, ErrUnsupportedPixels
		}
	}
	return out, nil
}

func encodeFloat64Values(ctx context.Context, values []float64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(values) > math.MaxInt/8 {
		return nil, ErrLimitExceeded
	}
	out := make([]byte, len(values)*8)
	if err := encodeFloat64ValuesInto(ctx, out, values); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeFloat64ValuesInto(ctx context.Context, destination []byte, values []float64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(values) > math.MaxInt/8 || len(destination) < len(values)*8 {
		return ErrLimitExceeded
	}
	for index, value := range values {
		if index&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if !finite(value) {
			return ErrUnsupportedPixels
		}
		binary.LittleEndian.PutUint64(destination[index*8:], math.Float64bits(value))
	}
	return nil
}

func storedValueFloat(unsigned uint64, signed int64, isSigned bool) float64 {
	if isSigned {
		return float64(signed)
	}
	return float64(unsigned)
}

func datatypeBytes(datatype int16) (int, bool) {
	switch datatype {
	case DatatypeUint8, DatatypeInt8:
		return 1, true
	case DatatypeInt16, DatatypeUint16:
		return 2, true
	case DatatypeInt32, DatatypeUint32, DatatypeFloat32:
		return 4, true
	case DatatypeFloat64:
		return 8, true
	default:
		return 0, false
	}
}

func writeExact(destination io.Writer, payload []byte) error {
	written, err := destination.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}
