package rle

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	rleHeaderLength      = 64
	maxRLEFragmentLength = uint64(1<<32 - 2)
)

// Encoder encodes canonical native frames as DICOM RLE Lossless frames.
// Encoder has no mutable state and is safe for concurrent use.
type Encoder struct{}

var _ pixeldata.FrameEncoder = (*Encoder)(nil)

// NewEncoder returns a pure Go DICOM RLE Lossless frame encoder.
func NewEncoder() *Encoder {
	return &Encoder{}
}

// RegisterEncoder registers a new RLE encoder in registry.
func RegisterEncoder(registry pixeldata.EncoderRegistry) error {
	if registry == nil {
		return pixeldata.ErrEncoderRegistryNil
	}
	return registry.RegisterEncoder(UID, NewEncoder())
}

// Capabilities describes the canonical native frames accepted by Encoder.
func (*Encoder) Capabilities() pixeldata.EncoderCapabilities {
	return pixeldata.EncoderCapabilities{
		TransferSyntaxUID:          UID,
		BitsAllocated:              []uint16{8, 16},
		PixelRepresentations:       []uint16{0, 1},
		SamplesPerPixel:            []uint16{1, 3},
		PhotometricInterpretations: []string{"MONOCHROME1", "MONOCHROME2", "PALETTE COLOR", "RGB"},
		Lossless:                   true,
		SupportsMultiFrame:         true,
		Backend:                    "pure-go",
	}
}

// EncodeFrame encodes one little-endian, sample-interleaved native frame.
// The result is one complete RLE frame suitable for one encapsulated fragment.
func (*Encoder) EncodeFrame(ctx context.Context, frame []byte, metadata pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pixeldata.EncodedFrame{}, err
	}

	bytesPerSample, segmentCount, expectedFrameLength, maxEncodedLength, err := validateEncoderInput(metadata)
	if err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	if uint64(len(frame)) != expectedFrameLength {
		return pixeldata.EncodedFrame{}, fmt.Errorf("%w: native frame length", pixeldata.ErrPixelDataSizeMismatch)
	}

	capacity := maxEncodedLength
	if capacity > uint64(maxInt()) {
		return pixeldata.EncodedFrame{}, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	encoded := make([]byte, rleHeaderLength, int(capacity))
	binary.LittleEndian.PutUint32(encoded[0:4], uint32(segmentCount))
	row := make([]byte, int(metadata.Columns))

	for sample := 0; sample < int(metadata.SamplesPerPixel); sample++ {
		for plane := bytesPerSample - 1; plane >= 0; plane-- {
			if err := ctx.Err(); err != nil {
				return pixeldata.EncodedFrame{}, err
			}
			segment := sample*bytesPerSample + bytesPerSample - 1 - plane
			binary.LittleEndian.PutUint32(encoded[4+segment*4:8+segment*4], uint32(len(encoded)))

			for y := 0; y < int(metadata.Rows); y++ {
				if err := ctx.Err(); err != nil {
					return pixeldata.EncodedFrame{}, err
				}
				for x := 0; x < int(metadata.Columns); x++ {
					sampleIndex := (y*int(metadata.Columns)+x)*int(metadata.SamplesPerPixel) + sample
					row[x] = frame[sampleIndex*bytesPerSample+plane]
				}
				encoded = appendPackBitsRow(encoded, row)
			}
			if len(encoded)%2 != 0 {
				encoded = append(encoded, 0)
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return pixeldata.EncodedFrame{}, err
	}
	return pixeldata.EncodedFrame{Data: encoded}, nil
}

func validateEncoderInput(metadata pixeldata.Metadata) (bytesPerSample int, segmentCount int, frameLength uint64, maxEncodedLength uint64, err error) {
	if metadata.Rows == 0 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("Rows")
	}
	if metadata.Columns == 0 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("Columns")
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("BitsAllocated")
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("BitsStored")
	}
	if metadata.HighBit != metadata.BitsStored-1 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("HighBit")
	}
	if metadata.PixelRepresentation > 1 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("PixelRepresentation")
	}
	if metadata.SamplesPerPixel != 1 && metadata.SamplesPerPixel != 3 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("SamplesPerPixel")
	}
	if metadata.NumberOfFrames < 1 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("NumberOfFrames")
	}
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch metadata.SamplesPerPixel {
	case 1:
		if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 {
			return 0, 0, 0, 0, unsupportedEncoderMetadata("PlanarConfiguration")
		}
		if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" && photometric != "PALETTE COLOR" {
			return 0, 0, 0, 0, unsupportedEncoderMetadata("PhotometricInterpretation")
		}
		if photometric == "PALETTE COLOR" && metadata.PixelRepresentation != 0 {
			return 0, 0, 0, 0, unsupportedEncoderMetadata("PixelRepresentation")
		}
	case 3:
		if photometric != "RGB" {
			return 0, 0, 0, 0, unsupportedEncoderMetadata("PhotometricInterpretation")
		}
		if metadata.PixelRepresentation != 0 {
			return 0, 0, 0, 0, unsupportedEncoderMetadata("PixelRepresentation")
		}
		if !metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 0 {
			return 0, 0, 0, 0, unsupportedEncoderMetadata("PlanarConfiguration")
		}
	}

	bytesPerSample = int(metadata.BitsAllocated / 8)
	segmentCount = int(metadata.SamplesPerPixel) * bytesPerSample
	if segmentCount == 0 || segmentCount > 15 {
		return 0, 0, 0, 0, unsupportedEncoderMetadata("SamplesPerPixel")
	}

	frameLength = uint64(metadata.Rows) * uint64(metadata.Columns) * uint64(metadata.SamplesPerPixel) * uint64(bytesPerSample)
	worstRowLength := uint64(metadata.Columns) + (uint64(metadata.Columns)+127)/128
	worstSegmentLength := uint64(metadata.Rows) * worstRowLength
	if worstSegmentLength%2 != 0 {
		worstSegmentLength++
	}
	maxEncodedLength = rleHeaderLength + uint64(segmentCount)*worstSegmentLength
	if maxEncodedLength > maxRLEFragmentLength || maxEncodedLength > uint64(maxInt()) {
		return 0, 0, 0, 0, fmt.Errorf("%w: encoded frame length", pixeldata.ErrEncoderOutputInvalid)
	}
	return bytesPerSample, segmentCount, frameLength, maxEncodedLength, nil
}

func unsupportedEncoderMetadata(field string) error {
	return &pixeldata.UnsupportedEncoderMetadataError{Field: field}
}

func appendPackBitsRow(dst, row []byte) []byte {
	literalStart := 0
	for i := 0; i < len(row); {
		runEnd := i + 1
		for runEnd < len(row) && row[runEnd] == row[i] {
			runEnd++
		}
		runLength := runEnd - i
		if runLength < 3 {
			i = runEnd
			continue
		}

		dst = appendPackBitsLiterals(dst, row[literalStart:i])
		for runLength > 128 {
			dst = append(dst, 0x81, row[i])
			i += 128
			runLength -= 128
		}
		if runLength >= 3 {
			dst = append(dst, byte(int8(1-runLength)), row[i])
			i += runLength
			literalStart = i
			continue
		}
		literalStart = i
		i += runLength
	}
	return appendPackBitsLiterals(dst, row[literalStart:])
}

func appendPackBitsLiterals(dst, literals []byte) []byte {
	for len(literals) > 0 {
		length := len(literals)
		if length > 128 {
			length = 128
		}
		dst = append(dst, byte(length-1))
		dst = append(dst, literals[:length]...)
		literals = literals[length:]
	}
	return dst
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
