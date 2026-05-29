package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrFrameTypeNotPresent                  = errors.New("dicom: requested frame type is not present")
	ErrUnsupportedSamplesPerPixel           = errors.New("dicom: unsupported samples per pixel")
	ErrUnsupportedBitsAllocated             = errors.New("dicom: unsupported BitsAllocated")
	ErrUnsupportedPhotometricInterpretation = errors.New("dicom: unsupported PhotometricInterpretation")
	ErrUnsupportedPlanarConfiguration       = errors.New("dicom: unsupported PlanarConfiguration")
	ErrInvalidFrameMetadata                 = errors.New("dicom: invalid frame metadata")
	ErrPixelDataTooShort                    = errors.New("dicom: pixel data too short for frame")

	tagWindowCenter     = core.NewTag(0x0028, 0x1050)
	tagWindowWidth      = core.NewTag(0x0028, 0x1051)
	tagRescaleIntercept = core.NewTag(0x0028, 0x1052)
	tagRescaleSlope     = core.NewTag(0x0028, 0x1053)
)

// CommonFrame is implemented by native and decoded encapsulated frame values.
type CommonFrame interface {
	GetImage() (image.Image, error)
	IsEncapsulated() bool
	GetNativeFrame() (*NativeFrame, error)
	GetEncapsulatedFrame() (*EncapsulatedFrame, error)
}

// Frame wraps a single native or decoded encapsulated Pixel Data frame.
type Frame struct {
	Encapsulated     bool
	NativeData       *NativeFrame
	EncapsulatedData *EncapsulatedFrame
}

func (f *Frame) IsEncapsulated() bool {
	return f != nil && f.Encapsulated
}

func (f *Frame) GetNativeFrame() (*NativeFrame, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil frame", ErrFrameTypeNotPresent)
	}
	if f.Encapsulated {
		return nil, ErrFrameTypeNotPresent
	}
	if f.NativeData == nil {
		return nil, fmt.Errorf("%w: native frame data is nil", ErrFrameTypeNotPresent)
	}
	return f.NativeData, nil
}

func (f *Frame) GetEncapsulatedFrame() (*EncapsulatedFrame, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil frame", ErrFrameTypeNotPresent)
	}
	if !f.Encapsulated {
		return nil, ErrFrameTypeNotPresent
	}
	if f.EncapsulatedData == nil {
		return nil, fmt.Errorf("%w: encapsulated frame data is nil", ErrFrameTypeNotPresent)
	}
	return f.EncapsulatedData, nil
}

func (f *Frame) GetImage() (image.Image, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil frame", ErrFrameTypeNotPresent)
	}
	if f.Encapsulated {
		frame, err := f.GetEncapsulatedFrame()
		if err != nil {
			return nil, err
		}
		return frame.GetImage()
	}
	frame, err := f.GetNativeFrame()
	if err != nil {
		return nil, err
	}
	return frame.GetImage()
}

// NativeFrame contains one uncompressed native frame.
type NativeFrame struct {
	Index     int
	Data      []byte
	Metadata  pixeldata.Metadata
	ByteOrder binary.ByteOrder
	Window    Window
	Rescale   Rescale
}

func (f *NativeFrame) IsEncapsulated() bool {
	return false
}

func (f *NativeFrame) GetNativeFrame() (*NativeFrame, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil native frame", ErrFrameTypeNotPresent)
	}
	return f, nil
}

func (f *NativeFrame) GetEncapsulatedFrame() (*EncapsulatedFrame, error) {
	return nil, ErrFrameTypeNotPresent
}

func (f *NativeFrame) GetImage() (image.Image, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil native frame", ErrFrameTypeNotPresent)
	}
	return renderImage(f.Data, f.Metadata, f.ByteOrder, f.Rescale, f.Window)
}

// EncapsulatedFrame contains one frame decoded from an encapsulated transfer
// syntax. Data is decoded pixel bytes, not the compressed fragment bytes.
type EncapsulatedFrame struct {
	Index     int
	Data      []byte
	Metadata  pixeldata.Metadata
	ByteOrder binary.ByteOrder
	Window    Window
	Rescale   Rescale
}

func (f *EncapsulatedFrame) IsEncapsulated() bool {
	return true
}

func (f *EncapsulatedFrame) GetNativeFrame() (*NativeFrame, error) {
	return nil, ErrFrameTypeNotPresent
}

func (f *EncapsulatedFrame) GetEncapsulatedFrame() (*EncapsulatedFrame, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil encapsulated frame", ErrFrameTypeNotPresent)
	}
	return f, nil
}

func (f *EncapsulatedFrame) GetImage() (image.Image, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil encapsulated frame", ErrFrameTypeNotPresent)
	}
	return renderImage(f.Data, f.Metadata, f.ByteOrder, f.Rescale, f.Window)
}

type Window struct {
	Center   float64
	Width    float64
	Function display.VOIFunction
	LUT      *display.LUT
}

type Rescale struct {
	Slope     float64
	Intercept float64
}

type Option func(*options)

type options struct {
	byteOrder binary.ByteOrder
	window    Window
	rescale   Rescale
}

func WithByteOrder(order binary.ByteOrder) Option {
	return func(opts *options) {
		opts.byteOrder = order
	}
}

func WithWindow(center, width float64) Option {
	return func(opts *options) {
		opts.window.Center = center
		opts.window.Width = width
	}
}

// WithVOIFunction selects the DICOM windowing function used for grayscale
// rendering. The zero value remains the standard LINEAR function.
func WithVOIFunction(function display.VOIFunction) Option {
	return func(opts *options) {
		opts.window.Function = function
	}
}

// WithVOILUT selects a DICOM VOI LUT. A non-nil LUT takes precedence over
// Window Center/Width during grayscale rendering.
func WithVOILUT(lut *display.LUT) Option {
	return func(opts *options) {
		opts.window.LUT = lut
	}
}

func WithRescale(slope, intercept float64) Option {
	return func(opts *options) {
		opts.rescale = Rescale{Slope: slope, Intercept: intercept}
	}
}

func NewNativeFrame(index int, data []byte, metadata pixeldata.Metadata, opts ...Option) *Frame {
	options := frameOptions(opts...)
	return &Frame{
		NativeData: &NativeFrame{
			Index:     index,
			Data:      data,
			Metadata:  metadata,
			ByteOrder: options.byteOrder,
			Window:    options.window,
			Rescale:   options.rescale,
		},
	}
}

func NewEncapsulatedFrame(index int, data []byte, metadata pixeldata.Metadata, opts ...Option) *Frame {
	options := frameOptions(opts...)
	return &Frame{
		Encapsulated: true,
		EncapsulatedData: &EncapsulatedFrame{
			Index:     index,
			Data:      data,
			Metadata:  metadata,
			ByteOrder: options.byteOrder,
			Window:    options.window,
			Rescale:   options.rescale,
		},
	}
}

func FromNativeFrames(native *pixeldata.NativeFrames, opts ...Option) []*Frame {
	if native == nil {
		return nil
	}
	frames := make([]*Frame, len(native.Data))
	for i, data := range native.Data {
		frames[i] = NewNativeFrame(i, data, native.Metadata, opts...)
	}
	return frames
}

func FromDecodedFrames(metadata pixeldata.Metadata, data [][]byte, encapsulated bool, opts ...Option) []*Frame {
	frames := make([]*Frame, len(data))
	for i, frameData := range data {
		if encapsulated {
			frames[i] = NewEncapsulatedFrame(i, frameData, metadata, opts...)
			continue
		}
		frames[i] = NewNativeFrame(i, frameData, metadata, opts...)
	}
	return frames
}

func FromParserFrame(source parser.Frame, opts ...Option) *Frame {
	metadata := pixeldata.Metadata{
		Rows:                       source.Metadata.Rows,
		Columns:                    source.Metadata.Columns,
		SamplesPerPixel:            source.Metadata.SamplesPerPixel,
		BitsAllocated:              source.Metadata.BitsAllocated,
		BitsStored:                 source.Metadata.BitsStored,
		HighBit:                    source.Metadata.HighBit,
		PixelRepresentation:        source.Metadata.PixelRepresentation,
		PlanarConfiguration:        source.Metadata.PlanarConfiguration,
		PlanarConfigurationPresent: source.Metadata.PlanarConfigurationPresent,
		NumberOfFrames:             source.Metadata.NumberOfFrames,
		PhotometricInterpretation:  source.Metadata.PhotometricInterpretation,
	}
	if source.TransferSyntax.ByteOrder != nil {
		opts = append([]Option{WithByteOrder(source.TransferSyntax.ByteOrder)}, opts...)
	}
	return NewNativeFrame(source.Index, source.Data, metadata, opts...)
}

func ExtractFrames(file *object.File, opts ...Option) ([]*Frame, error) {
	if file == nil || file.Dataset == nil {
		return nil, errors.New("dicom: file has no dataset")
	}
	defaults := []Option{WithByteOrder(file.TransferSyntax.ByteOrder)}
	if window, ok := WindowFromObject(file.Dataset); ok {
		defaults = append(defaults, WithWindow(window.Center, window.Width), WithVOIFunction(window.Function))
	}
	rescale := RescaleFromObject(file.Dataset)
	defaults = append(defaults, WithRescale(rescale.Slope, rescale.Intercept))
	opts = append(defaults, opts...)

	if pixeldata.IsJPIPReferencedTransferSyntax(file.TransferSyntax.UID) {
		_, err := pixeldata.DecodeFrames(file.TransferSyntax.UID, pixeldata.PixelData{}, file.Dataset)
		return nil, err
	}
	if transfer.IsVideoTransferSyntax(file.TransferSyntax.UID) {
		pixels, err := pixeldata.ExtractView(file.Dataset)
		if err != nil {
			return nil, err
		}
		_, err = pixeldata.DecodeFrames(file.TransferSyntax.UID, pixels, file.Dataset)
		return nil, err
	}

	native, err := pixeldata.ExtractNativeFrames(file.Dataset)
	if err == nil {
		return FromNativeFrames(native, opts...), nil
	}
	if !errors.Is(err, pixeldata.ErrEncapsulatedPixelData) {
		return nil, err
	}

	metadata, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		return nil, err
	}
	pixels, err := pixeldata.ExtractView(file.Dataset)
	if err != nil {
		return nil, err
	}
	decoded, err := pixeldata.DecodeFrames(file.TransferSyntax.UID, pixels, file.Dataset)
	if err != nil {
		return nil, err
	}
	metadata = decodedFrameMetadata(metadata, file.TransferSyntax.UID)
	return FromDecodedFrames(metadata, decoded.Data, true, opts...), nil
}

func decodedFrameMetadata(metadata pixeldata.Metadata, transferSyntaxUID string) pixeldata.Metadata {
	if metadata.SamplesPerPixel != 3 {
		return metadata
	}
	uid := transfer.NormalizeUID(transferSyntaxUID)
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

func WindowFromObject(obj *object.Object) (Window, bool) {
	voi, _ := display.VOIFromObject(obj)
	if voi.Width <= 0 {
		return Window{}, false
	}
	return Window{Center: voi.Center, Width: voi.Width, Function: voi.Function}, true
}

func RescaleFromObject(obj *object.Object) Rescale {
	out := Rescale{Slope: 1}
	if obj == nil {
		return out
	}
	if slope, err := obj.GetFloat(tagRescaleSlope); err == nil && slope != 0 {
		out.Slope = slope
	}
	if intercept, err := obj.GetFloat(tagRescaleIntercept); err == nil {
		out.Intercept = intercept
	}
	return out
}

func renderImage(data []byte, metadata pixeldata.Metadata, order binary.ByteOrder, rescale Rescale, window Window) (image.Image, error) {
	rows := int(metadata.Rows)
	cols := int(metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("%w: Rows=%d Columns=%d", ErrInvalidFrameMetadata, metadata.Rows, metadata.Columns)
	}
	photometric := normalizedPhotometric(metadata.PhotometricInterpretation)
	switch metadata.SamplesPerPixel {
	case 1:
		return renderGrayscaleImage(data, metadata, photometric, rows, cols, order, rescale, window)
	case 3:
		return renderRGBImage(data, metadata, photometric, rows, cols)
	default:
		return nil, fmt.Errorf("%w: SamplesPerPixel=%d", ErrUnsupportedSamplesPerPixel, metadata.SamplesPerPixel)
	}
}

// renderGrayscaleImage renders a single-sample frame through the shared
// dicom-go display pipeline (pixeldata/display), which owns the stored-pixel,
// modality, and VOI transform sequencing. MONOCHROME1 inversion is applied here
// as a presentation step on top of the pipeline's MONOCHROME2 output.
func renderGrayscaleImage(data []byte, metadata pixeldata.Metadata, photometric string, rows, cols int, order binary.ByteOrder, rescale Rescale, window Window) (*image.Gray, error) {
	if metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedPhotometricInterpretation, metadata.PhotometricInterpretation)
	}

	bytesPerSample := int(metadata.BitsAllocated / 8)
	if expected := rows * cols * bytesPerSample; len(data) < expected {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(data), expected)
	}

	out, err := display.RenderGray(display.Frame{
		Rows:    rows,
		Columns: cols,
		Pixels:  data,
		Format: display.PixelFormat{
			BitsAllocated: int(metadata.BitsAllocated),
			BitsStored:    int(metadata.BitsStored),
			HighBit:       int(metadata.HighBit),
			Signed:        metadata.PixelRepresentation != 0,
			ByteOrder:     order,
		},
		Modality: display.ModalityLUT{Slope: rescale.Slope, Intercept: rescale.Intercept},
		VOI:      display.VOILUT{Center: window.Center, Width: window.Width, Function: window.Function, LUT: window.LUT},
	})
	if err != nil {
		return nil, err
	}
	// MONOCHROME1 inversion is a presentation-stage transform applied after VOI
	// mapping (see pixeldata/display.Presentation).
	display.Presentation{Invert: photometric == "MONOCHROME1"}.Apply(out)
	return out, nil
}

func renderRGBImage(data []byte, metadata pixeldata.Metadata, photometric string, rows, cols int) (*image.RGBA, error) {
	if metadata.BitsAllocated != 8 {
		return nil, fmt.Errorf("%w: BitsAllocated=%d for RGB", ErrUnsupportedBitsAllocated, metadata.BitsAllocated)
	}
	if metadata.PixelRepresentation != 0 {
		return nil, fmt.Errorf("%w: PixelRepresentation=%d for RGB", ErrInvalidFrameMetadata, metadata.PixelRepresentation)
	}
	if metadata.PlanarConfigurationPresent && metadata.PlanarConfiguration != 0 {
		return nil, fmt.Errorf("%w: PlanarConfiguration=%d for RGB", ErrUnsupportedPlanarConfiguration, metadata.PlanarConfiguration)
	}
	if photometric != "RGB" {
		return nil, fmt.Errorf("%w: %q for SamplesPerPixel=3", ErrUnsupportedPhotometricInterpretation, metadata.PhotometricInterpretation)
	}

	const samplesPerPixel = 3
	expected := rows * cols * samplesPerPixel
	if len(data) < expected {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrPixelDataTooShort, len(data), expected)
	}

	out := image.NewRGBA(image.Rect(0, 0, cols, rows))
	for pixelIndex := 0; pixelIndex < rows*cols; pixelIndex++ {
		src := pixelIndex * samplesPerPixel
		dst := pixelIndex * 4
		out.Pix[dst] = data[src]
		out.Pix[dst+1] = data[src+1]
		out.Pix[dst+2] = data[src+2]
		out.Pix[dst+3] = 255
	}
	return out, nil
}

func frameOptions(opts ...Option) options {
	out := options{
		byteOrder: binary.LittleEndian,
		rescale:   Rescale{Slope: 1},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&out)
		}
	}
	if out.byteOrder == nil {
		out.byteOrder = binary.LittleEndian
	}
	return out
}

func normalizedPhotometric(value string) string {
	return strings.Trim(strings.ToUpper(value), " \x00")
}
