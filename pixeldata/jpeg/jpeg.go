package jpeg

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"strings"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

// DICOM JPEG transfer syntax UIDs supported by this adapter.
const (
	// UID is the DICOM JPEG Baseline (Process 1) transfer syntax UID.
	UID = "1.2.840.10008.1.2.4.50"
	// UIDExtended is the DICOM JPEG Extended (Process 2 & 4) transfer syntax
	// UID. This adapter supports the 8-bit SOF0/SOF1 subset accepted by
	// image/jpeg, not 12-bit Process 4.
	UIDExtended = "1.2.840.10008.1.2.4.51"
)

var (
	ErrInvalidFragment            = errors.New("dicom: invalid JPEG still-image fragment")
	ErrUnsupportedBitsAllocated   = errors.New("dicom: unsupported JPEG still-image BitsAllocated")
	ErrUnsupportedSamplesPerPixel = errors.New("dicom: unsupported JPEG still-image SamplesPerPixel")
	ErrImageSizeMismatch          = errors.New("dicom: JPEG still-image size does not match metadata")
)

// Codec decodes DICOM JPEG Baseline and supported JPEG Extended encapsulated
// pixel data using image/jpeg.
type Codec struct{}

// New returns a DICOM JPEG Baseline/Extended pixel data codec.
func New() *Codec {
	return &Codec{}
}

// Register registers the JPEG Baseline and JPEG Extended codecs in the provided
// pixel data registry.
func Register(registry pixeldata.Registry) error {
	if registry == nil {
		return pixeldata.ErrCodecRegistryNil
	}
	if err := registry.RegisterCodec(UID, New()); err != nil {
		return err
	}
	return registry.RegisterCodec(UIDExtended, New())
}

// RegisterDefault registers the JPEG Baseline and JPEG Extended codecs in
// pixeldata.DefaultRegistry.
func RegisterDefault() error {
	if err := pixeldata.RegisterCodec(UID, New()); err != nil {
		return err
	}
	return pixeldata.RegisterCodec(UIDExtended, New())
}

func (*Codec) Decode(pixel pixeldata.PixelData, obj *object.Object) (pixeldata.Frames, error) {
	if !pixel.Encapsulated {
		return pixeldata.Frames{}, fmt.Errorf("%w: JPEG still-image codec requires encapsulated pixel data", pixeldata.ErrIncompatiblePixelData)
	}

	metadata, err := pixeldata.ExtractMetadata(obj)
	if err != nil {
		return pixeldata.Frames{}, err
	}
	if metadata.BitsAllocated != 8 {
		return pixeldata.Frames{}, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if metadata.SamplesPerPixel != 1 && metadata.SamplesPerPixel != 3 {
		return pixeldata.Frames{}, fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedSamplesPerPixel, metadata.SamplesPerPixel)
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

	fragments := pixel.Sequence.Fragments
	if len(fragments) == 0 {
		return pixeldata.Frames{}, fmt.Errorf("%w: no frame fragments", ErrInvalidFragment)
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
