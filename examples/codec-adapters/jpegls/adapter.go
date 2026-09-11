// Package jpegls provides the supported opt-in JPEG-LS pixel data adapter
// boundary.
//
// The adapter registers JPEG-LS transfer syntax UIDs with pixeldata, validates
// DICOM pixel metadata, and delegates actual JPEG-LS byte decoding to an
// injected dependency-specific Decoder. It deliberately avoids importing a
// JPEG-LS implementation so the base dicom-go module remains dependency-free.
package jpegls

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrDecoderUnavailable        = errors.New("jpeglsadapter: JPEG-LS decoder unavailable")
	ErrUnsupportedMetadata       = errors.New("jpeglsadapter: unsupported JPEG-LS metadata")
	ErrUnsupportedFragmentLayout = errors.New("jpeglsadapter: unsupported JPEG-LS fragment layout")
	ErrMalformedFrame            = errors.New("jpeglsadapter: malformed JPEG-LS frame")
	ErrBackendInternal           = errors.New("jpeglsadapter: JPEG-LS backend internal failure")
)

// DecoderInput describes the DICOM metadata and syntax variant for one JPEG-LS
// frame decode.
type DecoderInput struct {
	Metadata     pixeldata.Metadata
	NearLossless bool
}

// Decoder is implemented by a dependency-specific JPEG-LS backend.
type Decoder interface {
	// DecodeJPEGLS receives a borrowed, read-only compressed fragment. It must
	// not retain or mutate fragment after returning. The adapter defensively
	// owns the decoded result before publishing it through pixeldata.Frames.
	DecodeJPEGLS(fragment []byte, input DecoderInput) ([]byte, error)
}

// Codec adapts a dependency-specific JPEG-LS decoder to pixeldata.Codec.
type Codec struct {
	decoder      Decoder
	nearLossless bool
}

// NewLossless returns a JPEG-LS Lossless codec adapter.
func NewLossless(decoder Decoder) *Codec {
	return &Codec{decoder: decoder}
}

// NewNearLossless returns a JPEG-LS Near-Lossless codec adapter.
func NewNearLossless(decoder Decoder) *Codec {
	return &Codec{decoder: decoder, nearLossless: true}
}

// Register registers JPEG-LS Lossless and Near-Lossless adapters in registry.
func Register(registry pixeldata.Registry, decoder Decoder) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	if err := registry.RegisterCodec(transfer.JPEGLSLossless.UID, NewLossless(decoder)); err != nil {
		return err
	}
	return RegisterNearLossless(registry, decoder)
}

// RegisterNearLossless registers only the JPEG-LS Near-Lossless adapter. It is
// intended for profiles that already contain dicom-go's built-in Lossless
// decoder and need CharLS only for transfer syntax 1.2.840.10008.1.2.4.81.
func RegisterNearLossless(registry pixeldata.Registry, decoder Decoder) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return registry.RegisterCodec(transfer.JPEGLSNearLossless.UID, NewNearLossless(decoder))
}

// RegisterDefault registers JPEG-LS adapters in pixeldata.DefaultRegistry.
func RegisterDefault(decoder Decoder) error {
	if pixeldata.DefaultRegistry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	return Register(pixeldata.DefaultRegistry, decoder)
}

// Decode assembles JPEG-LS Items through the shared bounded frame contract.
func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	return c.DecodeContext(context.Background(), pixel, obj)
}

// DecodeContext honors ctx between frames and while assembling fragments.
// Native CharLS backends are not guaranteed to abort a single in-flight
// frame; cancelation is checked before each frame is admitted.
func (c *Codec) DecodeContext(ctx context.Context, pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pixeldata.Frames{}, err
	}
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG-LS requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}
	if c == nil || c.decoder == nil {
		return pixeldata.Frames{}, ErrDecoderUnavailable
	}

	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if err := validateMetadata(metadata); err != nil {
		return pixeldata.Frames{}, err
	}
	const maxRequestBytes = uint64(512 << 20)
	frameBytes := metadata.FrameSize()
	if frameBytes <= 0 || uint64(frameBytes) > maxRequestBytes/uint64(metadata.NumberOfFrames) {
		return pixeldata.Frames{}, fmt.Errorf("%w: %w: decoded request exceeds limit", ErrUnsupportedMetadata, encapsulated.ErrResourceLimit)
	}

	if err := ctx.Err(); err != nil {
		return pixeldata.Frames{}, err
	}
	codestreams, err := encapsulated.FromFragments(ctx, pixel.Sequence, obj, metadata.NumberOfFrames, encapsulated.JPEGLS, encapsulated.Limits{})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return pixeldata.Frames{}, err
		}
		if errors.Is(err, encapsulated.ErrFrameCount) {
			err = errors.Join(pixeldata.ErrPixelDataSizeMismatch, err)
		}
		return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrUnsupportedFragmentLayout, err)
	}
	if uint64(frameBytes)*uint64(metadata.NumberOfFrames)+uint64(frameBytes)+codestreams.InputBytes()+codestreams.MaxFrameBytes() > maxRequestBytes {
		return pixeldata.Frames{}, fmt.Errorf("%w: %w: request working set exceeds limit", ErrUnsupportedMetadata, encapsulated.ErrResourceLimit)
	}
	frames := make([][]byte, codestreams.Len())
	for i := range frames {
		if err := ctx.Err(); err != nil {
			return pixeldata.Frames{}, err
		}
		view, err := codestreams.Frame(ctx, i)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return pixeldata.Frames{}, err
			}
			return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrUnsupportedFragmentLayout, err)
		}
		decoded, err := c.decoder.DecodeJPEGLS(view.Data, DecoderInput{
			Metadata:     metadata,
			NearLossless: c.nearLossless,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return pixeldata.Frames{}, err
			}
			if errors.Is(err, ErrDecoderUnavailable) {
				if err != ErrDecoderUnavailable {
					return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrDecoderUnavailable, i, err)
				}
				return pixeldata.Frames{}, fmt.Errorf("%w: frame %d", ErrDecoderUnavailable, i)
			}
			if errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) ||
				errors.Is(err, ErrUnsupportedMetadata) ||
				errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation) ||
				errors.Is(err, pixeldata.ErrUnsupportedPixelRepresentation) ||
				errors.Is(err, pixeldata.ErrUnsupportedPlanarConfiguration) ||
				errors.Is(err, ErrBackendInternal) {
				return pixeldata.Frames{}, fmt.Errorf("frame %d: %w", i, err)
			}
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrMalformedFrame, i, err)
		}
		if err := ctx.Err(); err != nil {
			return pixeldata.Frames{}, err
		}
		if got, want := int64(len(decoded)), metadata.FrameSize(); got != want {
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

	if err := ctx.Err(); err != nil {
		return pixeldata.Frames{}, err
	}
	return pixeldata.Frames{
		Rows:    int(metadata.Rows),
		Columns: int(metadata.Columns),
		Data:    frames,
	}, nil
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
	if metadata.SamplesPerPixel != 1 && metadata.SamplesPerPixel != 3 {
		return fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedMetadata, metadata.SamplesPerPixel)
	}
	if metadata.PixelRepresentation != 0 && metadata.PixelRepresentation != 1 {
		return fmt.Errorf("%w: PixelRepresentation=%d", pixeldata.ErrUnsupportedPixelRepresentation, metadata.PixelRepresentation)
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
		return photometric == "RGB"
	default:
		return false
	}
}
