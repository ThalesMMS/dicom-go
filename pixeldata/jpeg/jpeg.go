package jpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
)

// DICOM JPEG transfer syntax UIDs supported by this adapter.
const (
	// UID is the DICOM JPEG Baseline (Process 1) transfer syntax UID.
	UID = "1.2.840.10008.1.2.4.50"
	// UIDExtended is the DICOM JPEG Extended (Process 2 & 4) transfer syntax
	// UID. The codec supports 8-bit Process 2 through image/jpeg and the DICOM
	// 12-bit unsigned monochrome Process 4 subset through its pure-Go decoder.
	UIDExtended = "1.2.840.10008.1.2.4.51"
)

var (
	ErrInvalidFragment            = errors.New("dicom: invalid JPEG still-image fragment")
	ErrUnsupportedBitsAllocated   = errors.New("dicom: unsupported JPEG still-image BitsAllocated")
	ErrUnsupportedBitsStored      = errors.New("dicom: unsupported JPEG still-image BitsStored")
	ErrUnsupportedHighBit         = errors.New("dicom: unsupported JPEG still-image HighBit")
	ErrUnsupportedSamplesPerPixel = errors.New("dicom: unsupported JPEG still-image SamplesPerPixel")
	ErrImageSizeMismatch          = errors.New("dicom: JPEG still-image size does not match metadata")
)

type codecMode uint8

const (
	codecModeAuto codecMode = iota
	codecModeBaseline
	codecModeExtended
)

// Codec decodes DICOM JPEG Baseline and supported JPEG Extended encapsulated
// pixel data. Registered instances are transfer-syntax-specific; New retains
// the historical dual-syntax behavior for direct callers.
type Codec struct {
	mode codecMode
}

// New returns a DICOM JPEG Baseline/Extended pixel data codec.
func New() *Codec {
	return &Codec{mode: codecModeAuto}
}

func newBaselineCodec() *Codec { return &Codec{mode: codecModeBaseline} }

func newExtendedCodec() *Codec { return &Codec{mode: codecModeExtended} }

// Register registers the JPEG Baseline and JPEG Extended codecs in the provided
// pixel data registry.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	if err := registry.RegisterCodec(UID, newBaselineCodec()); err != nil {
		return err
	}
	return registry.RegisterCodec(UIDExtended, newExtendedCodec())
}

// RegisterDefault registers the JPEG Baseline and JPEG Extended codecs in
// pixeldata.DefaultRegistry.
func RegisterDefault() error {
	if err := pixeldata.RegisterCodec(UID, newBaselineCodec()); err != nil {
		return err
	}
	return pixeldata.RegisterCodec(UIDExtended, newExtendedCodec())
}

func (c *Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG still-image codec requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}

	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	highPrecision := metadata.BitsAllocated == 16 && c.mode != codecModeBaseline
	if c.mode == codecModeBaseline && metadata.BitsAllocated != 8 {
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if metadata.BitsAllocated != 8 && !highPrecision {
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if !highPrecision && metadata.BitsStored != 8 {
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsStored=%d", ErrUnsupportedBitsStored, metadata.BitsStored)
	}
	if !highPrecision && metadata.HighBit != 7 {
		return pixeldata.Frames{}, fmt.Errorf("%w: HighBit=%d", ErrUnsupportedHighBit, metadata.HighBit)
	}
	if highPrecision && metadata.BitsStored != extendedProcess4Precision {
		// Preserve the historical classification for a 16-bit native layout
		// that is not the qualified Process 4 representation.
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsStored=%d", ErrUnsupportedBitsAllocated, metadata.BitsStored)
	}
	if highPrecision && metadata.HighBit != extendedProcess4Precision-1 {
		return pixeldata.Frames{}, fmt.Errorf("%w: HighBit=%d", ErrUnsupportedHighBit, metadata.HighBit)
	}
	if metadata.SamplesPerPixel != 1 && metadata.SamplesPerPixel != 3 {
		return pixeldata.Frames{}, fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedSamplesPerPixel, metadata.SamplesPerPixel)
	}
	if highPrecision && metadata.SamplesPerPixel != 1 {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG Extended Process 4 requires one component", ErrUnsupportedSamplesPerPixel)
	}
	if metadata.PixelRepresentation != 0 {
		return pixeldata.Frames{}, fmt.Errorf("%w: PixelRepresentation=%d", pixeldata.ErrUnsupportedPixelRepresentation, metadata.PixelRepresentation)
	}
	if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 {
		return pixeldata.Frames{}, fmt.Errorf("%w: PlanarConfiguration=%d", pixeldata.ErrUnsupportedPlanarConfiguration, metadata.PlanarConfiguration)
	}
	if !supportedPhotometricInterpretation(metadata) {
		return pixeldata.Frames{}, fmt.Errorf(
			"%w: PhotometricInterpretation=%s SamplesPerPixel=%d",
			pixeldata.ErrUnsupportedPhotometricInterpretation,
			strings.TrimSpace(metadata.PhotometricInterpretation),
			metadata.SamplesPerPixel,
		)
	}

	codestreams, err := encapsulated.FromFragments(context.Background(), pixel.Sequence, obj, metadata.NumberOfFrames, encapsulated.JPEG, encapsulated.Limits{})
	if err != nil {
		if errors.Is(err, encapsulated.ErrFrameCount) {
			err = errors.Join(pixeldata.ErrPixelDataSizeMismatch, err)
		}
		return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
	}
	if highPrecision {
		if err := validateExtendedProcess4Request(metadata, pixel.Sequence.Fragments); err != nil {
			return pixeldata.Frames{}, err
		}
	}
	// Retained native output, source payload, the largest joined compressed
	// frame and one decoder image coexist. Check them before any image decode.
	frameBytes := metadata.FrameSize()
	const maxRequestBytes = uint64(512 << 20)
	if frameBytes <= 0 || uint64(frameBytes) > maxRequestBytes/uint64(metadata.NumberOfFrames) {
		return pixeldata.Frames{}, fmt.Errorf("%w: decoded request exceeds resource limit", ErrInvalidFragment)
	}
	requestBytes := uint64(frameBytes)*uint64(metadata.NumberOfFrames) + codestreams.InputBytes() + codestreams.MaxFrameBytes() + uint64(metadata.Rows)*uint64(metadata.Columns)*4
	if requestBytes > maxRequestBytes {
		return pixeldata.Frames{}, fmt.Errorf("%w: request working set exceeds resource limit", ErrInvalidFragment)
	}

	frames := make([][]byte, codestreams.Len())
	for i := range frames {
		view, err := codestreams.Frame(context.Background(), i)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
		}
		fragment := view.Data
		sof, err := jpegFrameSOFMarker(fragment)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrInvalidFragment, i, err)
		}
		if c.mode == codecModeBaseline && sof != 0xc0 {
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: JPEG Baseline requires SOF0", ErrInvalidFragment, i)
		}
		if c.mode == codecModeExtended && sof != 0xc1 {
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: JPEG Extended requires SOF1", ErrInvalidFragment, i)
		}
		if highPrecision {
			if sof != 0xc1 {
				return pixeldata.Frames{}, fmt.Errorf("%w: BitsAllocated=%d requires JPEG Extended SOF1", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
			}
			frame, err := decodeExtendedProcess4(fragment, metadata)
			if err != nil {
				return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrInvalidFragment, i, err)
			}
			frames[i] = frame
			continue
		}
		if err := encapsulated.ValidateFrame(context.Background(), fragment, encapsulated.JPEG); err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
		}
		config, err := stdjpeg.DecodeConfig(bytes.NewReader(fragment))
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
		}
		if config.Width != int(metadata.Columns) || config.Height != int(metadata.Rows) {
			return pixeldata.Frames{}, ErrImageSizeMismatch
		}
		img, err := stdjpeg.Decode(bytes.NewReader(fragment))
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("%w: frame %d: %w", ErrInvalidFragment, i, err)
		}
		frame, err := imageToFrameBytes(img, metadata)
		if err != nil {
			return pixeldata.Frames{}, fmt.Errorf("decode JPEG still-image frame %d: %w", i, err)
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
		return photometric == "MONOCHROME1" || photometric == "MONOCHROME2"
	case 3:
		return photometric == "RGB" || photometric == "YBR_FULL" || photometric == "YBR_FULL_422"
	default:
		return false
	}
}

func imageToFrameBytes(img image.Image, metadata pixeldata.Metadata) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("%w: decoded image is nil", ErrInvalidFragment)
	}
	bounds := img.Bounds()
	rows := int(metadata.Rows)
	columns := int(metadata.Columns)
	if bounds.Dx() != columns || bounds.Dy() != rows {
		return nil, fmt.Errorf("%w: got %dx%d want %dx%d", ErrImageSizeMismatch, bounds.Dx(), bounds.Dy(), columns, rows)
	}

	switch metadata.SamplesPerPixel {
	case 1:
		out := make([]byte, rows*columns)
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				gray := color.GrayModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.Gray)
				out[y*columns+x] = gray.Y
			}
		}
		return out, nil
	case 3:
		out := make([]byte, rows*columns*3)
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				rgba := color.RGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.RGBA)
				offset := (y*columns + x) * 3
				out[offset] = rgba.R
				out[offset+1] = rgba.G
				out[offset+2] = rgba.B
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedSamplesPerPixel, metadata.SamplesPerPixel)
	}
}
