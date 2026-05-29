package render

import (
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
	dicomframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type decodedFrame struct {
	rows        int
	cols        int
	photometric string
	grayscale   []float32
	rgba        []byte
}

func decodeSliceFrame(slice *Frame) (*decodedFrame, error) {
	if slice == nil {
		return nil, fmt.Errorf("render: nil slice")
	}
	metadata := effectiveFrameMetadata(slice)
	rows := int(metadata.Rows)
	cols := int(metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("%w: Rows=%d Columns=%d", dicomframe.ErrInvalidFrameMetadata, metadata.Rows, metadata.Columns)
	}
	photometric := normalizedPhotometric(metadata.PhotometricInterpretation)
	switch metadata.SamplesPerPixel {
	case 1:
		return decodeGrayscaleSlice(slice, metadata, photometric, rows, cols)
	case 3:
		return decodeRGBSlice(slice, metadata, photometric, rows, cols)
	default:
		return nil, fmt.Errorf("%w: SamplesPerPixel=%d", dicomframe.ErrUnsupportedSamplesPerPixel, metadata.SamplesPerPixel)
	}
}

func decodeGrayscaleSlice(slice *Frame, metadata pixeldata.Metadata, photometric string, rows, cols int) (*decodedFrame, error) {
	frame, err := grayscaleDisplayFrame(slice, metadata, photometric, rows, cols)
	if err != nil {
		return nil, err
	}
	modality, err := display.DecodeModalityFloat32(frame)
	if err != nil {
		return nil, err
	}
	return &decodedFrame{rows: rows, cols: cols, photometric: photometric, grayscale: modality}, nil
}

func decodeGrayscaleSliceInto(destination []float32, slice *Frame) error {
	if slice == nil {
		return fmt.Errorf("render: nil slice")
	}
	metadata := effectiveFrameMetadata(slice)
	rows := int(metadata.Rows)
	cols := int(metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return fmt.Errorf("%w: Rows=%d Columns=%d", dicomframe.ErrInvalidFrameMetadata, metadata.Rows, metadata.Columns)
	}
	frame, err := grayscaleDisplayFrame(slice, metadata, normalizedPhotometric(metadata.PhotometricInterpretation), rows, cols)
	if err != nil {
		return err
	}
	return display.DecodeModalityFloat32Into(destination, frame)
}

func grayscaleDisplayFrame(slice *Frame, metadata pixeldata.Metadata, photometric string, rows, cols int) (display.Frame, error) {
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 && metadata.BitsAllocated != 32 {
		return display.Frame{}, fmt.Errorf("%w: BitsAllocated=%d", dicomframe.ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
		return display.Frame{}, fmt.Errorf("%w: %q", dicomframe.ErrUnsupportedPhotometricInterpretation, metadata.PhotometricInterpretation)
	}
	bytesPerSample := int(metadata.BitsAllocated / 8)
	if expected := rows * cols * bytesPerSample; len(slice.PixelBytes) < expected {
		return display.Frame{}, fmt.Errorf("%w: got %d bytes, want %d", dicomframe.ErrPixelDataTooShort, len(slice.PixelBytes), expected)
	}
	slope := slice.Rescale.Slope
	if slope == 0 || math.IsNaN(slope) || math.IsInf(slope, 0) {
		slope = 1
	}
	intercept := slice.Rescale.Intercept
	if math.IsNaN(intercept) || math.IsInf(intercept, 0) {
		intercept = 0
	}
	// Decode stored pixels to modality values through the dicom-go display
	// pipeline so this cached decode shares the library's stored-pixel and
	// modality transform logic. The viewer keeps only the slope/intercept
	// sanitization for non-finite rescale metadata.
	return display.Frame{
		Rows:    rows,
		Columns: cols,
		Pixels:  slice.PixelBytes,
		Format: display.PixelFormat{
			BitsAllocated: int(metadata.BitsAllocated),
			BitsStored:    int(metadata.BitsStored),
			HighBit:       int(metadata.HighBit),
			Signed:        metadata.PixelRepresentation != 0,
			ByteOrder:     slice.ByteOrder,
		},
		Modality: display.ModalityLUT{Slope: slope, Intercept: intercept},
	}, nil
}

func decodeRGBSlice(slice *Frame, metadata pixeldata.Metadata, photometric string, rows, cols int) (*decodedFrame, error) {
	if metadata.BitsAllocated != 8 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d for RGB", dicomframe.ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if metadata.PixelRepresentation != 0 {
		return nil, fmt.Errorf("%w: PixelRepresentation=%d for RGB", dicomframe.ErrInvalidFrameMetadata, metadata.PixelRepresentation)
	}
	if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 {
		return nil, fmt.Errorf("%w: PlanarConfiguration=%d for RGB", dicomframe.ErrUnsupportedPlanarConfiguration, metadata.PlanarConfiguration)
	}
	if photometric != "RGB" {
		return nil, fmt.Errorf("%w: %q for SamplesPerPixel=3", dicomframe.ErrUnsupportedPhotometricInterpretation, metadata.PhotometricInterpretation)
	}
	const samplesPerPixel = 3
	expected := rows * cols * samplesPerPixel
	if len(slice.PixelBytes) < expected {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", dicomframe.ErrPixelDataTooShort, len(slice.PixelBytes), expected)
	}
	rgba := make([]byte, rows*cols*4)
	for pixelIndex := 0; pixelIndex < rows*cols; pixelIndex++ {
		src := pixelIndex * samplesPerPixel
		dst := pixelIndex * 4
		rgba[dst] = slice.PixelBytes[src]
		rgba[dst+1] = slice.PixelBytes[src+1]
		rgba[dst+2] = slice.PixelBytes[src+2]
		rgba[dst+3] = 255
	}
	return &decodedFrame{rows: rows, cols: cols, photometric: photometric, rgba: rgba}, nil
}

func effectiveFrameMetadata(slice *Frame) pixeldata.Metadata {
	if slice == nil {
		return pixeldata.Metadata{}
	}
	metadata := slice.Metadata
	if !slice.Encapsulated || metadata.SamplesPerPixel != 3 {
		return metadata
	}
	uid := transfer.NormalizeUID(slice.TransferSyntaxUID)
	switch uid {
	case transfer.JPEGBaseline.UID, transfer.JPEGExtended.UID:
		switch normalizedPhotometric(metadata.PhotometricInterpretation) {
		case "YBR_FULL", "YBR_FULL_422":
			metadata.PhotometricInterpretation = "RGB"
			metadata.PlanarConfiguration = 0
			metadata.PlanarConfigurationPresent = true
		}
	default:
		if transfer.IsJPEG2000TransferSyntax(uid) || transfer.IsJPEGXLTransferSyntax(uid) {
			switch normalizedPhotometric(metadata.PhotometricInterpretation) {
			case "YBR_FULL", "YBR_FULL_422", "YBR_RCT", "YBR_ICT":
				metadata.PhotometricInterpretation = "RGB"
				metadata.PlanarConfiguration = 0
				metadata.PlanarConfigurationPresent = true
			}
		}
	}
	return metadata
}

func renderDecodedFrame(decoded *decodedFrame, window WindowLevel) (image.Image, error) {
	if decoded == nil {
		return nil, fmt.Errorf("render: nil decoded frame")
	}
	if decoded.rgba != nil {
		out := image.NewRGBA(image.Rect(0, 0, decoded.cols, decoded.rows))
		copy(out.Pix, decoded.rgba)
		return out, nil
	}
	if decoded.grayscale == nil {
		return nil, fmt.Errorf("%w: missing grayscale pixels", dicomframe.ErrInvalidFrameMetadata)
	}
	invert := decoded.photometric == "MONOCHROME1"
	out := image.NewGray(image.Rect(0, 0, decoded.cols, decoded.rows))
	mapper := display.NewVOIByteMapper(display.VOILUT{Center: window.Center, Width: window.Width, Function: window.Function, LUT: window.LUT})
	for pixelIndex, modality := range decoded.grayscale {
		gray := mapper.Byte(float64(modality))
		if invert {
			gray = 255 - gray
		}
		out.Pix[pixelIndex] = gray
	}
	return out, nil
}

func (d *decodedFrame) cacheBytes() int64 {
	if d == nil {
		return 0
	}
	return int64(len(d.grayscale)*4 + len(d.rgba))
}

func normalizedPhotometric(value string) string {
	return strings.Trim(strings.ToUpper(value), " \x00")
}
