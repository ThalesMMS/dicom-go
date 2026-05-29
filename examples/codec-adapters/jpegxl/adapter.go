// Package jpegxladapter provides the supported opt-in JPEG XL pixel data
// adapter boundary.
//
// The adapter lives in a nested module so the base dicom-go module remains
// dependency-free. The default backend uses the djxl command line decoder when
// this module is built with the jpegxl_djxl build tag.
package jpegxladapter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrDjxlUnavailable           = errors.New("jpegxladapter: djxl decoder unavailable")
	ErrUnsupportedMetadata       = errors.New("jpegxladapter: unsupported JPEG XL metadata")
	ErrUnsupportedFragmentLayout = errors.New("jpegxladapter: unsupported JPEG XL fragment layout")
	ErrImageSizeMismatch         = errors.New("jpegxladapter: decoded image size does not match metadata")
	ErrMalformedCodestream       = errors.New("jpegxladapter: malformed JPEG XL codestream")
)

// Decoder decodes one JPEG XL codestream payload into native frame bytes
// matching the supplied DICOM pixel metadata.
type Decoder interface {
	DecodeFrame(fragment []byte, metadata pixeldata.Metadata) ([]byte, error)
}

// Codec decodes JPEG XL encapsulated still-image pixel data.
type Codec struct {
	decoder Decoder
}

// New returns a JPEG XL codec backed by the default djxl decoder.
func New() *Codec {
	return NewWithDecoder(NewDjxlDecoder())
}

// NewWithDecoder returns a JPEG XL codec backed by decoder.
func NewWithDecoder(decoder Decoder) *Codec {
	if decoder == nil {
		decoder = NewDjxlDecoder()
	}
	return &Codec{decoder: decoder}
}

// Register registers supported JPEG XL transfer syntaxes in registry.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	codec := New()
	for _, uid := range supportedUIDs() {
		if err := registry.RegisterCodec(uid, codec); err != nil {
			return err
		}
	}
	return nil
}

// RegisterDefault registers supported JPEG XL transfer syntaxes in
// pixeldata.DefaultRegistry.
func RegisterDefault() error {
	if pixeldata.DefaultRegistry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return Register(pixeldata.DefaultRegistry)
}

// Decode decodes supported JPEG XL encapsulated still-image frames.
func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG XL requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}
	if c == nil || c.decoder == nil {
		return pixeldata.Frames{}, ErrDjxlUnavailable
	}

	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if err := validateMetadata(metadata); err != nil {
		return pixeldata.Frames{}, err
	}

	payloads, err := framePayloads(pixel, metadata.NumberOfFrames)
	if err != nil {
		return pixeldata.Frames{}, err
	}

	frames := make([][]byte, len(payloads))
	for i, payload := range payloads {
		decoded, err := c.decoder.DecodeFrame(payload, metadata)
		if err != nil {
			if errors.Is(err, ErrDjxlUnavailable) {
				if err != ErrDjxlUnavailable {
					return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrDjxlUnavailable, i, err)
				}
				return pixeldata.Frames{}, fmt.Errorf("%w: frame %d", ErrDjxlUnavailable, i)
			}
			if errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) ||
				errors.Is(err, ErrUnsupportedMetadata) ||
				errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation) ||
				errors.Is(err, pixeldata.ErrUnsupportedPlanarConfiguration) ||
				errors.Is(err, ErrImageSizeMismatch) {
				return pixeldata.Frames{}, fmt.Errorf("frame %d: %w", i, err)
			}
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrMalformedCodestream, i, err)
		}
		if got, want := int64(len(decoded)), decodedFrameSize(metadata); got != want {
			return pixeldata.Frames{}, fmt.Errorf(
				"%w: frame %d decoded=%d expected=%d",
				pixeldata.ErrPixelDataSizeMismatch,
				i,
				got,
				want,
			)
		}
		frames[i] = append([]byte(nil), decoded...)
	}

	return pixeldata.Frames{
		Rows:    int(metadata.Rows),
		Columns: int(metadata.Columns),
		Data:    frames,
	}, nil
}

func decodedFrameSize(metadata pixeldata.Metadata) int64 {
	bytesPerSample := int64(metadata.BitsAllocated / 8)
	return int64(metadata.Rows) * int64(metadata.Columns) * int64(metadata.SamplesPerPixel) * bytesPerSample
}

func supportedUIDs() []string {
	return []string{
		transfer.JPEGXLLossless.UID,
		transfer.JPEGXLJPEGRecompression.UID,
		transfer.JPEGXL.UID,
	}
}

func validateMetadata(metadata pixeldata.Metadata) error {
	if metadata.Rows == 0 || metadata.Columns == 0 || metadata.NumberOfFrames <= 0 {
		return fmt.Errorf(
			"%w: rows=%d columns=%d number_of_frames=%d",
			pixeldata.ErrInvalidMetadata,
			metadata.Rows,
			metadata.Columns,
			metadata.NumberOfFrames,
		)
	}
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedMetadata, metadata.BitsAllocated)
	}
	if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated {
		return fmt.Errorf(
			"%w: BitsStored=%d BitsAllocated=%d",
			pixeldata.ErrInvalidMetadata,
			metadata.BitsStored,
			metadata.BitsAllocated,
		)
	}
	if metadata.HighBit != metadata.BitsStored-1 {
		return fmt.Errorf(
			"%w: HighBit=%d BitsStored=%d",
			ErrUnsupportedMetadata,
			metadata.HighBit,
			metadata.BitsStored,
		)
	}
	if metadata.SamplesPerPixel != 1 && metadata.SamplesPerPixel != 3 {
		return fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedMetadata, metadata.SamplesPerPixel)
	}
	if metadata.PixelRepresentation != 0 && metadata.PixelRepresentation != 1 {
		return fmt.Errorf("%w: PixelRepresentation=%d", ErrUnsupportedMetadata, metadata.PixelRepresentation)
	}
	if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 {
		return fmt.Errorf("%w: PlanarConfiguration=%d", pixeldata.ErrUnsupportedPlanarConfiguration, metadata.PlanarConfiguration)
	}
	if !supportedPhotometricInterpretation(metadata) {
		return fmt.Errorf(
			"%w: PhotometricInterpretation=%s SamplesPerPixel=%d",
			pixeldata.ErrUnsupportedPhotometricInterpretation,
			strings.TrimSpace(metadata.PhotometricInterpretation),
			metadata.SamplesPerPixel,
		)
	}
	return nil
}

func supportedPhotometricInterpretation(metadata pixeldata.Metadata) bool {
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch metadata.SamplesPerPixel {
	case 1:
		return photometric == "MONOCHROME1" || photometric == "MONOCHROME2"
	case 3:
		switch photometric {
		case "RGB", "YBR_FULL", "YBR_FULL_422", "YBR_PARTIAL_422", "YBR_PARTIAL_420", "YBR_ICT", "YBR_RCT":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func framePayloads(pixel pixeldata.PixelData, numberOfFrames int) ([][]byte, error) {
	fragments := pixel.Sequence.Fragments
	if len(fragments) == 0 {
		return nil, fmt.Errorf("%w: no JPEG XL frame fragments", ErrUnsupportedFragmentLayout)
	}
	if len(fragments) == numberOfFrames {
		payloads := make([][]byte, len(fragments))
		for i := range fragments {
			payloads[i] = append([]byte(nil), fragments[i]...)
		}
		return payloads, nil
	}
	if numberOfFrames == 1 {
		return [][]byte{bytes.Join(fragments, nil)}, nil
	}
	if len(pixel.Sequence.OffsetTable) > 0 {
		payloads, err := framePayloadsFromBasicOffsetTable(fragments, pixel.Sequence.OffsetTable, numberOfFrames)
		if err != nil {
			return nil, err
		}
		return payloads, nil
	}
	return nil, fmt.Errorf(
		"%w: %w: NumberOfFrames=%d fragments=%d",
		ErrUnsupportedFragmentLayout,
		pixeldata.ErrPixelDataSizeMismatch,
		numberOfFrames,
		len(fragments),
	)
}

func framePayloadsFromBasicOffsetTable(fragments [][]byte, table []byte, numberOfFrames int) ([][]byte, error) {
	if numberOfFrames <= 0 || len(table) != numberOfFrames*4 {
		return nil, fmt.Errorf("%w: NumberOfFrames=%d offset_table_bytes=%d", ErrUnsupportedFragmentLayout, numberOfFrames, len(table))
	}
	fragmentOffsets := make([]uint64, len(fragments))
	offset := uint64(0)
	for i, fragment := range fragments {
		fragmentOffsets[i] = offset
		itemLength := uint64(8 + len(fragment))
		if len(fragment)%2 != 0 {
			itemLength++
		}
		offset += itemLength
	}

	starts := make([]int, numberOfFrames+1)
	for frame := 0; frame < numberOfFrames; frame++ {
		want := uint64(binary.LittleEndian.Uint32(table[frame*4:]))
		if frame > 0 && want <= uint64(binary.LittleEndian.Uint32(table[(frame-1)*4:])) {
			return nil, fmt.Errorf("%w: non-increasing basic offset table", ErrUnsupportedFragmentLayout)
		}
		index := -1
		for i, got := range fragmentOffsets {
			if got == want {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("%w: basic offset %d does not match a fragment boundary", ErrUnsupportedFragmentLayout, want)
		}
		starts[frame] = index
	}
	starts[numberOfFrames] = len(fragments)

	payloads := make([][]byte, numberOfFrames)
	for frame := 0; frame < numberOfFrames; frame++ {
		if starts[frame] >= starts[frame+1] {
			return nil, fmt.Errorf("%w: empty frame %d in basic offset table", ErrUnsupportedFragmentLayout, frame)
		}
		payloads[frame] = bytes.Join(fragments[starts[frame]:starts[frame+1]], nil)
	}
	return payloads, nil
}
