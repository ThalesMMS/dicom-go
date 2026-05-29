package rle

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// UID is the DICOM RLE Lossless transfer syntax UID.
const UID = "1.2.840.10008.1.2.5"

var (
	ErrInvalidHeader              = errors.New("dicom: invalid RLE header")
	ErrInvalidSegmentCount        = errors.New("dicom: invalid RLE segment count")
	ErrInvalidSegmentOffset       = errors.New("dicom: invalid RLE segment offset")
	ErrSegmentDecodeFailed        = errors.New("dicom: RLE segment decode failed")
	ErrUnsupportedBitsAllocated   = errors.New("dicom: unsupported RLE BitsAllocated")
	ErrUnsupportedSamplesPerPixel = errors.New("dicom: unsupported RLE SamplesPerPixel")
)

// Codec decodes DICOM RLE Lossless encapsulated pixel data.
type Codec struct{}

// New returns a DICOM RLE Lossless pixel data codec.
func New() *Codec {
	return &Codec{}
}

// Register registers the RLE Lossless codec in the provided pixel data registry.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return registry.RegisterCodec(UID, New())
}

// RegisterDefault registers the RLE Lossless codec in pixeldata.DefaultRegistry.
func RegisterDefault() error {
	return pixeldata.RegisterCodec(UID, New())
}

// Decode decodes one RLE fragment per output frame.
func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	return c.DecodeContext(context.Background(), pixel, obj)
}

// DecodeContext decodes one RLE fragment per output frame while honoring
// cancellation between frames, segments, and PackBits runs.
func (*Codec) DecodeContext(ctx context.Context, pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pixeldata.Frames{}, err
	}
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: RLE requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}

	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated || metadata.HighBit != metadata.BitsStored-1 {
		return pixeldata.Frames{}, fmt.Errorf("%w: invalid stored-bit metadata", pixeldata.ErrIncompatiblePixelData)
	}
	if metadata.SamplesPerPixel == 0 || metadata.SamplesPerPixel > 3 {
		return pixeldata.Frames{}, fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedSamplesPerPixel, metadata.SamplesPerPixel)
	}
	if metadata.PixelRepresentation != 0 && metadata.PixelRepresentation != 1 {
		return pixeldata.Frames{}, fmt.Errorf("%w: PixelRepresentation=%d", pixeldata.ErrUnsupportedPixelRepresentation, metadata.PixelRepresentation)
	}
	if !supportedPhotometricInterpretation(metadata) {
		return pixeldata.Frames{}, fmt.Errorf(
			"%w: PhotometricInterpretation=%s SamplesPerPixel=%d",
			pixeldata.ErrUnsupportedPhotometricInterpretation,
			strings.TrimSpace(metadata.PhotometricInterpretation),
			metadata.SamplesPerPixel,
		)
	}
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	if metadata.SamplesPerPixel == 1 && metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 ||
		metadata.SamplesPerPixel == 3 && (!metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 0) {
		return pixeldata.Frames{}, fmt.Errorf("%w: invalid PlanarConfiguration", pixeldata.ErrUnsupportedPlanarConfiguration)
	}
	if metadata.PixelRepresentation == 1 && photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
		return pixeldata.Frames{}, fmt.Errorf("%w: PixelRepresentation=%d", pixeldata.ErrUnsupportedPixelRepresentation, metadata.PixelRepresentation)
	}

	fragments := pixel.Sequence.Fragments
	if len(fragments) == 0 {
		return pixeldata.Frames{}, fmt.Errorf("%w: no RLE frame fragments", ErrInvalidHeader)
	}
	if metadata.NumberOfFrames != len(fragments) {
		return pixeldata.Frames{}, fmt.Errorf(
			"%w: NumberOfFrames=%d fragments=%d",
			pixeldata.ErrPixelDataSizeMismatch,
			metadata.NumberOfFrames,
			len(fragments),
		)
	}

	frames := make([][]byte, len(fragments))
	for i, fragment := range fragments {
		if err := ctx.Err(); err != nil {
			return pixeldata.Frames{}, err
		}
		frame, err := decodeFragmentContext(
			ctx,
			fragment,
			int(metadata.Rows),
			int(metadata.Columns),
			int(metadata.SamplesPerPixel),
			int(metadata.BitsAllocated),
		)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("decode RLE frame %d: %w", i, err)
		}
		frames[i] = frame
	}

	return pixeldata.Frames{
		Rows:    int(metadata.Rows),
		Columns: int(metadata.Columns),
		Data:    frames,
	}, nil
}

func supportedPhotometricInterpretation(metadata pixeldata.Metadata) bool {
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch metadata.SamplesPerPixel {
	case 1:
		return photometric == "MONOCHROME1" || photometric == "MONOCHROME2" || photometric == "PALETTE COLOR"
	case 3:
		return photometric == "RGB"
	default:
		return false
	}
}

func parseHeader(fragment []byte) ([]uint32, error) {
	if len(fragment) < 64 {
		return nil, fmt.Errorf("%w: got %d bytes", ErrInvalidHeader, len(fragment))
	}
	if uint64(len(fragment)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("%w: fragment too large: %d bytes", ErrInvalidHeader, len(fragment))
	}

	count := binary.LittleEndian.Uint32(fragment[:4])
	if count == 0 || count > 15 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidSegmentCount, count)
	}

	offsets := make([]uint32, int(count)+1)
	for i := 0; i < int(count); i++ {
		offset := binary.LittleEndian.Uint32(fragment[4+i*4 : 8+i*4])
		if offset < 64 || offset > uint32(len(fragment)) {
			return nil, fmt.Errorf("%w: segment %d offset=%d fragment_length=%d", ErrInvalidSegmentOffset, i, offset, len(fragment))
		}
		if i > 0 && offset < offsets[i-1] {
			return nil, fmt.Errorf("%w: segment %d offset=%d previous_offset=%d", ErrInvalidSegmentOffset, i, offset, offsets[i-1])
		}
		offsets[i] = offset
	}
	offsets[len(offsets)-1] = uint32(len(fragment))

	return offsets, nil
}

func decodePackBits(segment []byte, expectedSize int) ([]byte, error) {
	return decodePackBitsContext(context.Background(), segment, expectedSize)
}

func decodePackBitsContext(ctx context.Context, segment []byte, expectedSize int) ([]byte, error) {
	if expectedSize < 0 {
		return nil, fmt.Errorf("%w: invalid expected size %d", ErrSegmentDecodeFailed, expectedSize)
	}
	out := make([]byte, 0, expectedSize)
	for i := 0; i < len(segment) && len(out) < expectedSize; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h := int(int8(segment[i]))
		i++

		switch {
		case h >= 0:
			n := h + 1
			if i+n > len(segment) {
				return nil, fmt.Errorf("%w: truncated literal run", ErrSegmentDecodeFailed)
			}
			if n > expectedSize-len(out) {
				return nil, fmt.Errorf("%w: literal run exceeds expected size", ErrSegmentDecodeFailed)
			}
			out = append(out, segment[i:i+n]...)
			i += n
		case h >= -127:
			if i >= len(segment) {
				return nil, fmt.Errorf("%w: truncated repeat run", ErrSegmentDecodeFailed)
			}
			n := 1 - h
			value := segment[i]
			i++
			if n > expectedSize-len(out) {
				return nil, fmt.Errorf("%w: repeat run exceeds expected size", ErrSegmentDecodeFailed)
			}
			for range n {
				out = append(out, value)
			}
		default:
			// h == -128 is a no-op in PackBits.
		}
	}
	if len(out) != expectedSize {
		return nil, fmt.Errorf("%w: decoded %d bytes, expected %d", ErrSegmentDecodeFailed, len(out), expectedSize)
	}

	return out, nil
}

func decodeFragment(fragment []byte, rows, cols, samplesPerPixel, bitsAllocated int) ([]byte, error) {
	return decodeFragmentContext(context.Background(), fragment, rows, cols, samplesPerPixel, bitsAllocated)
}

func decodeFragmentContext(ctx context.Context, fragment []byte, rows, cols, samplesPerPixel, bitsAllocated int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bitsAllocated != 8 && bitsAllocated != 16 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, bitsAllocated)
	}
	if rows <= 0 || cols <= 0 || samplesPerPixel <= 0 || samplesPerPixel > 3 {
		return nil, fmt.Errorf(
			"%w: rows=%d columns=%d samples_per_pixel=%d",
			ErrInvalidSegmentCount,
			rows,
			cols,
			samplesPerPixel,
		)
	}

	bytesPerSample := bitsAllocated / 8
	expectedSegments := samplesPerPixel * bytesPerSample

	offsets, err := parseHeader(fragment)
	if err != nil {
		return nil, err
	}
	if actualSegments := len(offsets) - 1; actualSegments != expectedSegments {
		return nil, fmt.Errorf("%w: got %d want %d", ErrInvalidSegmentCount, actualSegments, expectedSegments)
	}

	maxInt := int64(^uint(0) >> 1)
	pixelsPerSegment64 := int64(rows) * int64(cols)
	frameSize64 := pixelsPerSegment64 * int64(samplesPerPixel) * int64(bytesPerSample)
	if pixelsPerSegment64 <= 0 || frameSize64 <= 0 || pixelsPerSegment64 > maxInt || frameSize64 > maxInt {
		return nil, fmt.Errorf(
			"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d",
			ErrInvalidSegmentCount,
			rows,
			cols,
			samplesPerPixel,
			bitsAllocated,
		)
	}

	pixelsPerSegment := int(pixelsPerSegment64)
	frameSize := int(frameSize64)
	decodedSegments := make([][]byte, expectedSegments)
	for segmentIndex := range expectedSegments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segmentStart := int(offsets[segmentIndex])
		segmentEnd := int(offsets[segmentIndex+1])
		if segmentEnd < segmentStart {
			return nil, fmt.Errorf("%w: segment %d offset range %d..%d", ErrInvalidSegmentOffset, segmentIndex, segmentStart, segmentEnd)
		}

		decoded, err := decodePackBitsContext(ctx, fragment[segmentStart:segmentEnd], pixelsPerSegment)
		if err != nil {
			return nil, fmt.Errorf("%w: segment %d", err, segmentIndex)
		}
		if len(decoded) != pixelsPerSegment {
			return nil, fmt.Errorf(
				"%w: segment %d decoded %d bytes, expected %d",
				ErrSegmentDecodeFailed,
				segmentIndex,
				len(decoded),
				pixelsPerSegment,
			)
		}
		decodedSegments[segmentIndex] = decoded
	}

	out := make([]byte, frameSize)
	segmentIndex := 0
	for sample := 0; sample < samplesPerPixel; sample++ {
		for byteOffset := bytesPerSample - 1; byteOffset >= 0; byteOffset-- {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			decoded := decodedSegments[segmentIndex]
			start := sample*bytesPerSample + byteOffset
			step := samplesPerPixel * bytesPerSample
			for decodedIndex, dstIndex := 0, start; decodedIndex < len(decoded); decodedIndex, dstIndex = decodedIndex+1, dstIndex+step {
				out[dstIndex] = decoded[decodedIndex]
			}
			segmentIndex++
		}
	}

	return out, nil
}
