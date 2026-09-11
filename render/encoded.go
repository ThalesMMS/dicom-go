package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"image/png"
	"math"
	"mime"
	"strings"
)

const (
	MediaTypePNG  = "image/png"
	MediaTypeJPEG = "image/jpeg"
)

var (
	ErrInvalidEncodedRequest       = errors.New("render: invalid encoded image request")
	ErrUnsupportedEncodedMediaType = errors.New("render: unsupported encoded image media type")
	ErrEncodedImageResourceLimit   = errors.New("render: encoded image resource limit exceeded")
)

// EncodedImageLimits bounds source rendering, resizing, and encoded output.
// Zero fields receive conservative defaults from DefaultEncodedImageLimits.
type EncodedImageLimits struct {
	MaxWidth       int
	MaxHeight      int
	MaxPixels      int64
	MaxOutputBytes int64
}

// DefaultEncodedImageLimits returns finite defaults suitable for server-side
// rendered representations and thumbnails.
func DefaultEncodedImageLimits() EncodedImageLimits {
	return EncodedImageLimits{
		MaxWidth:       4096,
		MaxHeight:      4096,
		MaxPixels:      16 * 1024 * 1024,
		MaxOutputBytes: 32 << 20,
	}
}

// EncodedRequest describes one already-decoded DICOM frame representation.
// SourceRect is cropped before scaling; its zero value selects the complete
// image. FlipHorizontal and FlipVertical are applied to that selected region.
// Width and Height define a bounding box that preserves source aspect ratio.
// When both are zero, the selected source dimensions are retained.
type EncodedRequest struct {
	Frame          *Frame
	Window         WindowLevel
	SourceRect     image.Rectangle
	FlipHorizontal bool
	FlipVertical   bool
	Width          int
	Height         int
	MediaType      string
	JPEGQuality    int
}

// EncodedImage is a complete bounded PNG or JPEG representation.
type EncodedImage struct {
	Data      []byte
	MediaType string
	Width     int
	Height    int
}

// EncodedRenderer converts one decoded DICOM frame into a negotiated still
// image representation. Resource lookup and DICOMweb routing remain caller
// responsibilities.
type EncodedRenderer interface {
	RenderEncoded(context.Context, EncodedRequest) (EncodedImage, error)
}

// StandardEncodedRenderer is the dependency-free renderer supplied by
// dicom-go. It uses the package's shared grayscale/window/color pipeline.
type StandardEncodedRenderer struct {
	limits EncodedImageLimits
}

// NewStandardEncodedRenderer constructs a renderer with finite limits.
func NewStandardEncodedRenderer(limits EncodedImageLimits) (*StandardEncodedRenderer, error) {
	normalized, err := normalizeEncodedImageLimits(limits)
	if err != nil {
		return nil, err
	}
	return &StandardEncodedRenderer{limits: normalized}, nil
}

// RenderEncoded renders, optionally resizes, and encodes one frame. The
// operation observes cancellation before rendering, during resizing, and on
// every encoder write.
func (r *StandardEncodedRenderer) RenderEncoded(ctx context.Context, request EncodedRequest) (EncodedImage, error) {
	if r == nil {
		return EncodedImage{}, fmt.Errorf("%w: nil renderer", ErrInvalidEncodedRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EncodedImage{}, err
	}
	if request.Frame == nil {
		return EncodedImage{}, fmt.Errorf("%w: frame is required", ErrInvalidEncodedRequest)
	}
	if request.Width < 0 || request.Height < 0 {
		return EncodedImage{}, fmt.Errorf("%w: requested dimensions must not be negative", ErrInvalidEncodedRequest)
	}
	if request.Width > r.limits.MaxWidth || request.Height > r.limits.MaxHeight {
		return EncodedImage{}, fmt.Errorf("%w: requested bounds %dx%d exceed %dx%d", ErrEncodedImageResourceLimit, request.Width, request.Height, r.limits.MaxWidth, r.limits.MaxHeight)
	}
	mediaType, err := normalizeEncodedMediaType(request.MediaType)
	if err != nil {
		return EncodedImage{}, err
	}
	quality := request.JPEGQuality
	if quality == 0 {
		quality = 90
	}
	if quality < 1 || quality > 100 {
		return EncodedImage{}, fmt.Errorf("%w: JPEG quality must be in 1..100", ErrInvalidEncodedRequest)
	}
	if err := r.validateDimensions(int(request.Frame.Metadata.Columns), int(request.Frame.Metadata.Rows)); err != nil {
		return EncodedImage{}, err
	}

	img, err := RenderFrame(request.Frame, request.Window)
	if err != nil {
		return EncodedImage{}, err
	}
	sourceWidth, sourceHeight := img.Bounds().Dx(), img.Bounds().Dy()
	if err := r.validateDimensions(sourceWidth, sourceHeight); err != nil {
		return EncodedImage{}, err
	}
	region, err := normalizeSourceRegion(img.Bounds(), request.SourceRect, request.FlipHorizontal, request.FlipVertical)
	if err != nil {
		return EncodedImage{}, err
	}
	width, height, err := encodedOutputDimensions(region.width, region.height, request.Width, request.Height)
	if err != nil {
		return EncodedImage{}, err
	}
	if err := r.validateDimensions(width, height); err != nil {
		return EncodedImage{}, err
	}
	img, err = renderViewportContext(ctx, img, region, width, height)
	if err != nil {
		return EncodedImage{}, err
	}

	writer := &encodedLimitWriter{ctx: ctx, maximum: r.limits.MaxOutputBytes}
	switch mediaType {
	case MediaTypePNG:
		err = png.Encode(writer, img)
	case MediaTypeJPEG:
		err = stdjpeg.Encode(writer, img, &stdjpeg.Options{Quality: quality})
	default:
		panic("unreachable encoded media type")
	}
	if err != nil {
		return EncodedImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return EncodedImage{}, err
	}
	return EncodedImage{
		Data:      append([]byte(nil), writer.Bytes()...),
		MediaType: mediaType,
		Width:     width,
		Height:    height,
	}, nil
}

func normalizeEncodedImageLimits(limits EncodedImageLimits) (EncodedImageLimits, error) {
	if limits.MaxWidth < 0 || limits.MaxHeight < 0 || limits.MaxPixels < 0 || limits.MaxOutputBytes < 0 {
		return EncodedImageLimits{}, fmt.Errorf("%w: negative limit", ErrInvalidEncodedRequest)
	}
	defaults := DefaultEncodedImageLimits()
	if limits.MaxWidth == 0 {
		limits.MaxWidth = defaults.MaxWidth
	}
	if limits.MaxHeight == 0 {
		limits.MaxHeight = defaults.MaxHeight
	}
	if limits.MaxPixels == 0 {
		limits.MaxPixels = defaults.MaxPixels
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = defaults.MaxOutputBytes
	}
	return limits, nil
}

func normalizeEncodedMediaType(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return MediaTypePNG, nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedEncodedMediaType, value)
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case MediaTypePNG, MediaTypeJPEG:
		return mediaType, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedEncodedMediaType, value)
	}
}

func (r *StandardEncodedRenderer) validateDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: dimensions must be positive", ErrInvalidEncodedRequest)
	}
	if width > r.limits.MaxWidth || height > r.limits.MaxHeight {
		return fmt.Errorf("%w: dimensions %dx%d exceed %dx%d", ErrEncodedImageResourceLimit, width, height, r.limits.MaxWidth, r.limits.MaxHeight)
	}
	pixels := int64(width) * int64(height)
	if pixels <= 0 || pixels > r.limits.MaxPixels {
		return fmt.Errorf("%w: %d pixels exceed %d", ErrEncodedImageResourceLimit, pixels, r.limits.MaxPixels)
	}
	return nil
}

func encodedOutputDimensions(sourceWidth, sourceHeight float64, width, height int) (int, int, error) {
	if sourceWidth <= 0 || sourceHeight <= 0 || width < 0 || height < 0 || math.IsNaN(sourceWidth) || math.IsNaN(sourceHeight) {
		return 0, 0, fmt.Errorf("%w: invalid dimensions", ErrInvalidEncodedRequest)
	}
	switch {
	case width == 0 && height == 0:
		width, height = max(1, int(math.Round(sourceWidth))), max(1, int(math.Round(sourceHeight)))
	case width == 0:
		width = int(math.Round(sourceWidth * float64(height) / sourceHeight))
		if width < 1 {
			width = 1
		}
	case height == 0:
		height = int(math.Round(sourceHeight * float64(width) / sourceWidth))
		if height < 1 {
			height = 1
		}
	default:
		if sourceWidth*float64(height) > float64(width)*sourceHeight {
			height = int(math.Round(sourceHeight * float64(width) / sourceWidth))
			if height < 1 {
				height = 1
			}
		} else {
			width = int(math.Round(sourceWidth * float64(height) / sourceHeight))
			if width < 1 {
				width = 1
			}
		}
	}
	return width, height, nil
}

type normalizedSourceRegion struct {
	x, y          float64
	width, height float64
	flipX, flipY  bool
	full          bool
}

func normalizeSourceRegion(bounds, requested image.Rectangle, flipHorizontal, flipVertical bool) (normalizedSourceRegion, error) {
	width, height := float64(bounds.Dx()), float64(bounds.Dy())
	if requested == (image.Rectangle{}) {
		return normalizedSourceRegion{width: width, height: height, flipX: flipHorizontal, flipY: flipVertical, full: !flipHorizontal && !flipVertical}, nil
	}
	if requested.Empty() {
		return normalizedSourceRegion{}, fmt.Errorf("%w: source rectangle %v is empty", ErrInvalidEncodedRequest, requested)
	}
	if requested.Min.X < bounds.Min.X || requested.Min.Y < bounds.Min.Y || requested.Max.X > bounds.Max.X || requested.Max.Y > bounds.Max.Y {
		return normalizedSourceRegion{}, fmt.Errorf("%w: source rectangle %v exceeds image bounds %v", ErrInvalidEncodedRequest, requested, bounds)
	}
	region := normalizedSourceRegion{
		x:      float64(requested.Min.X - bounds.Min.X),
		y:      float64(requested.Min.Y - bounds.Min.Y),
		width:  float64(requested.Dx()),
		height: float64(requested.Dy()),
		flipX:  flipHorizontal,
		flipY:  flipVertical,
	}
	return region, nil
}

func renderViewportContext(ctx context.Context, source image.Image, region normalizedSourceRegion, width, height int) (image.Image, error) {
	if region.full && source.Bounds().Dx() == width && source.Bounds().Dy() == height {
		return source, nil
	}
	destination := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sourceY := (float64(y)+0.5)*region.height/float64(height) - 0.5
		if region.flipY {
			sourceY = region.y + region.height - 1 - sourceY
		} else {
			sourceY += region.y
		}
		sourceY = clampFloat(sourceY, region.y, region.y+region.height-1)
		y0, y1, fy := interpolationCoordinates(sourceY, sourceHeight)
		for x := 0; x < width; x++ {
			sourceX := (float64(x)+0.5)*region.width/float64(width) - 0.5
			if region.flipX {
				sourceX = region.x + region.width - 1 - sourceX
			} else {
				sourceX += region.x
			}
			sourceX = clampFloat(sourceX, region.x, region.x+region.width-1)
			x0, x1, fx := interpolationCoordinates(sourceX, sourceWidth)
			topLeft := colorNRGBA(source.At(bounds.Min.X+x0, bounds.Min.Y+y0))
			topRight := colorNRGBA(source.At(bounds.Min.X+x1, bounds.Min.Y+y0))
			bottomLeft := colorNRGBA(source.At(bounds.Min.X+x0, bounds.Min.Y+y1))
			bottomRight := colorNRGBA(source.At(bounds.Min.X+x1, bounds.Min.Y+y1))
			destination.SetNRGBA(x, y, bilinearNRGBA(topLeft, topRight, bottomLeft, bottomRight, fx, fy))
		}
	}
	return destination, nil
}

func interpolationCoordinates(value float64, size int) (int, int, float64) {
	if value <= 0 || size <= 1 {
		return 0, 0, 0
	}
	if value >= float64(size-1) {
		last := size - 1
		return last, last, 0
	}
	first := int(math.Floor(value))
	return first, first + 1, value - float64(first)
}

func colorNRGBA(value color.Color) color.NRGBA {
	return color.NRGBAModel.Convert(value).(color.NRGBA)
}

func bilinearNRGBA(topLeft, topRight, bottomLeft, bottomRight color.NRGBA, fx, fy float64) color.NRGBA {
	interpolate := func(a, b, c, d uint8) uint8 {
		top := float64(a) + (float64(b)-float64(a))*fx
		bottom := float64(c) + (float64(d)-float64(c))*fx
		value := top + (bottom-top)*fy
		return uint8(math.Round(value))
	}
	return color.NRGBA{
		R: interpolate(topLeft.R, topRight.R, bottomLeft.R, bottomRight.R),
		G: interpolate(topLeft.G, topRight.G, bottomLeft.G, bottomRight.G),
		B: interpolate(topLeft.B, topRight.B, bottomLeft.B, bottomRight.B),
		A: interpolate(topLeft.A, topRight.A, bottomLeft.A, bottomRight.A),
	}
}

type encodedLimitWriter struct {
	bytes.Buffer
	ctx     context.Context
	maximum int64
}

func (w *encodedLimitWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(w.Len()) > w.maximum-int64(len(data)) {
		return 0, fmt.Errorf("%w: output exceeds %d bytes", ErrEncodedImageResourceLimit, w.maximum)
	}
	return w.Buffer.Write(data)
}
