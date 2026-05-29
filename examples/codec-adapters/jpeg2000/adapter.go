// Package jpeg2000 adapts a pure Go JPEG 2000 / HTJ2K decoder to
// pixeldata.Codec.
//
// The package is a supported opt-in nested module. It deliberately keeps the
// JPEG 2000 dependency outside the root dicom-go module while providing
// explicit registration, metadata validation, and fragment assembly boundaries.
package jpeg2000

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"

	j2k "github.com/mrjoshuak/go-jpeg2000"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrUnsupportedMetadata       = errors.New("jpeg2000adapter: unsupported JPEG 2000 metadata")
	ErrUnsupportedFragmentLayout = errors.New("jpeg2000adapter: unsupported JPEG 2000 fragment layout")
	ErrImageSizeMismatch         = errors.New("jpeg2000adapter: decoded image size does not match metadata")
	ErrMalformedCodestream       = errors.New("jpeg2000adapter: malformed JPEG 2000 / HTJ2K codestream")
	ErrDecoderUnavailable        = errors.New("jpeg2000adapter: decoder unavailable")
)

// Decoder decodes one JPEG 2000 / HTJ2K codestream payload into native frame
// bytes matching the supplied DICOM pixel metadata.
type Decoder interface {
	DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error)
}

// Codec decodes JPEG 2000 / HTJ2K encapsulated still-image pixel data.
type Codec struct {
	decoder Decoder
}

type pureGoDecoder struct{}

// New returns a JPEG 2000 / HTJ2K codec adapter.
func New() *Codec {
	return NewWithDecoder(pureGoDecoder{})
}

// NewWithDecoder returns a JPEG 2000 / HTJ2K codec adapter backed by decoder.
func NewWithDecoder(decoder Decoder) *Codec {
	if decoder == nil {
		decoder = pureGoDecoder{}
	}
	return &Codec{decoder: decoder}
}

// Register registers supported JPEG 2000 and HTJ2K transfer syntaxes.
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

// RegisterDefault registers supported JPEG 2000 and HTJ2K transfer syntaxes in
// pixeldata.DefaultRegistry.
func RegisterDefault() error {
	if pixeldata.DefaultRegistry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return Register(pixeldata.DefaultRegistry)
}

// Decode decodes supported JPEG 2000 / HTJ2K encapsulated still-image frames.
func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if c == nil || c.decoder == nil {
		return pixeldata.Frames{}, ErrDecoderUnavailable
	}
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG 2000 requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
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
		frame, err := c.decoder.DecodeFrame(payload, metadata)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("decode JPEG 2000 frame %d: %w", i, err)
		}
		frames[i] = frame
	}

	return pixeldata.Frames{
		Rows:    int(metadata.Rows),
		Columns: int(metadata.Columns),
		Data:    frames,
	}, nil
}

func (pureGoDecoder) DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error) {
	if metadata.PixelRepresentation != 0 {
		return nil, fmt.Errorf("%w: pure-Go backend does not support PixelRepresentation=%d", ErrUnsupportedMetadata, metadata.PixelRepresentation)
	}
	streamMetadata, err := j2k.DecodeMetadata(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedCodestream, err)
	}
	if err := validateCodestreamMetadata(metadata, streamMetadata); err != nil {
		return nil, err
	}

	img, err := j2k.Decode(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedCodestream, err)
	}
	return imageToFrameBytes(img, metadata)
}

func supportedUIDs() []string {
	return []string{
		transfer.JPEG2000LosslessOnly.UID,
		transfer.JPEG2000.UID,
		transfer.JPEG2000Part2Lossless.UID,
		transfer.JPEG2000Part2.UID,
		transfer.HTJ2KLossless.UID,
		transfer.HTJ2KLosslessRPCL.UID,
		transfer.HTJ2K.UID,
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

func validateCodestreamMetadata(metadata pixeldata.Metadata, stream *j2k.Metadata) error {
	if stream == nil {
		return ErrMalformedCodestream
	}
	if stream.Width != int(metadata.Columns) || stream.Height != int(metadata.Rows) {
		return fmt.Errorf(
			"%w: JPEG 2000 size=%dx%d Columns=%d Rows=%d",
			ErrImageSizeMismatch,
			stream.Width,
			stream.Height,
			metadata.Columns,
			metadata.Rows,
		)
	}
	if stream.NumComponents != int(metadata.SamplesPerPixel) {
		return fmt.Errorf(
			"%w: JPEG 2000 components=%d SamplesPerPixel=%d",
			ErrImageSizeMismatch,
			stream.NumComponents,
			metadata.SamplesPerPixel,
		)
	}
	if len(stream.BitsPerComponent) != stream.NumComponents {
		return fmt.Errorf(
			"%w: JPEG 2000 components=%d precision_entries=%d",
			ErrUnsupportedMetadata,
			stream.NumComponents,
			len(stream.BitsPerComponent),
		)
	}
	for i, precision := range stream.BitsPerComponent {
		if precision != int(metadata.BitsStored) {
			return fmt.Errorf(
				"%w: JPEG 2000 precision=%d BitsStored=%d component=%d",
				ErrUnsupportedMetadata,
				precision,
				metadata.BitsStored,
				i,
			)
		}
	}
	if len(stream.Signed) != 0 && len(stream.Signed) != stream.NumComponents {
		return fmt.Errorf(
			"%w: JPEG 2000 components=%d signed_entries=%d",
			ErrUnsupportedMetadata,
			stream.NumComponents,
			len(stream.Signed),
		)
	}
	for i, signed := range stream.Signed {
		if signed && metadata.PixelRepresentation == 0 {
			return fmt.Errorf("%w: JPEG 2000 signed component=%d PixelRepresentation=0", ErrUnsupportedMetadata, i)
		}
	}
	return nil
}

func supportedPhotometricInterpretation(metadata pixeldata.Metadata) bool {
	photometric := strings.ToUpper(strings.TrimSpace(metadata.PhotometricInterpretation))
	switch metadata.SamplesPerPixel {
	case 1:
		return photometric == "MONOCHROME1" || photometric == "MONOCHROME2"
	case 3:
		return photometric == "RGB" || photometric == "YBR_RCT" || photometric == "YBR_ICT"
	default:
		return false
	}
}

func framePayloads(pixel pixeldata.PixelData, numberOfFrames int) ([][]byte, error) {
	fragments := pixel.Sequence.Fragments
	if len(fragments) == 0 {
		return nil, fmt.Errorf("%w: no JPEG 2000 frame fragments", ErrUnsupportedFragmentLayout)
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
	return nil, fmt.Errorf(
		"%w: %w: NumberOfFrames=%d fragments=%d",
		ErrUnsupportedFragmentLayout,
		pixeldata.ErrPixelDataSizeMismatch,
		numberOfFrames,
		len(fragments),
	)
}

func imageToFrameBytes(img image.Image, metadata pixeldata.Metadata) ([]byte, error) {
	if img == nil {
		return nil, errors.New("decoded image is nil")
	}
	bounds := img.Bounds()
	rows := int(metadata.Rows)
	columns := int(metadata.Columns)
	if bounds.Dx() != columns || bounds.Dy() != rows {
		return nil, fmt.Errorf("%w: got %dx%d want %dx%d", ErrImageSizeMismatch, bounds.Dx(), bounds.Dy(), columns, rows)
	}

	switch {
	case metadata.SamplesPerPixel == 1 && metadata.BitsAllocated == 8:
		out := make([]byte, rows*columns)
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				gray := color.GrayModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.Gray)
				out[y*columns+x] = byte(scaleDecodedSample(uint32(gray.Y), 255, metadata.BitsStored))
			}
		}
		return out, nil
	case metadata.SamplesPerPixel == 1 && metadata.BitsAllocated == 16:
		out := make([]byte, rows*columns*2)
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				gray := color.Gray16Model.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.Gray16)
				binary.LittleEndian.PutUint16(out[(y*columns+x)*2:], scaleDecodedSample(uint32(gray.Y), 65535, metadata.BitsStored))
			}
		}
		return out, nil
	case metadata.SamplesPerPixel == 3 && metadata.BitsAllocated == 8:
		out := make([]byte, rows*columns*3)
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				rgba := color.RGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.RGBA)
				offset := (y*columns + x) * 3
				out[offset] = byte(scaleDecodedSample(uint32(rgba.R), 255, metadata.BitsStored))
				out[offset+1] = byte(scaleDecodedSample(uint32(rgba.G), 255, metadata.BitsStored))
				out[offset+2] = byte(scaleDecodedSample(uint32(rgba.B), 255, metadata.BitsStored))
			}
		}
		return out, nil
	case metadata.SamplesPerPixel == 3 && metadata.BitsAllocated == 16:
		out := make([]byte, rows*columns*6)
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				rgba := color.RGBA64Model.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.RGBA64)
				offset := (y*columns + x) * 6
				binary.LittleEndian.PutUint16(out[offset:], scaleDecodedSample(uint32(rgba.R), 65535, metadata.BitsStored))
				binary.LittleEndian.PutUint16(out[offset+2:], scaleDecodedSample(uint32(rgba.G), 65535, metadata.BitsStored))
				binary.LittleEndian.PutUint16(out[offset+4:], scaleDecodedSample(uint32(rgba.B), 65535, metadata.BitsStored))
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf(
			"%w: SamplesPerPixel=%d BitsAllocated=%d",
			ErrUnsupportedMetadata,
			metadata.SamplesPerPixel,
			metadata.BitsAllocated,
		)
	}
}

func scaleDecodedSample(value uint32, sourceMax uint32, bitsStored uint16) uint16 {
	targetMax := uint32(1<<bitsStored) - 1
	if sourceMax == targetMax {
		return uint16(value)
	}
	return uint16((value*targetMax + sourceMax/2) / sourceMax)
}
