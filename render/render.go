package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/ThalesMMS/dicom-go/pixeldata/display"
	dicomframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
)

func RenderFrame(slice *Frame, window WindowLevel) (image.Image, error) {
	if slice == nil {
		return blankImage(512, 512), errors.New("render: nil slice")
	}
	if slice.DecodeErr != nil {
		return blankImage(int(slice.Metadata.Columns), int(slice.Metadata.Rows)), slice.DecodeErr
	}
	metadata := effectiveFrameMetadata(slice)
	window = normalizeWindow(window, slice.DefaultWindow)
	opts := []dicomframe.Option{
		dicomframe.WithByteOrder(slice.ByteOrder),
		dicomframe.WithWindow(window.Center, window.Width),
		dicomframe.WithVOIFunction(window.Function),
		dicomframe.WithVOILUT(window.LUT),
		dicomframe.WithRescale(slice.Rescale.Slope, slice.Rescale.Intercept),
	}
	var frame *dicomframe.Frame
	if slice.Encapsulated {
		frame = dicomframe.NewEncapsulatedFrame(slice.FrameIndex, slice.PixelBytes, metadata, opts...)
	} else {
		frame = dicomframe.NewNativeFrame(slice.FrameIndex, slice.PixelBytes, metadata, opts...)
	}
	img, err := frame.GetImage()
	if img == nil {
		return blankImage(int(slice.Metadata.Columns), int(slice.Metadata.Rows)), err
	}
	return img, err
}

func normalizeWindow(window, fallback WindowLevel) WindowLevel {
	if window.LUT != nil {
		return window
	}
	if !validWindowWidth(window.Width) {
		window = fallback
	}
	if !validWindowWidth(window.Width) {
		return WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth}
	}
	return window
}

func validWindowWidth(width float64) bool {
	return width > 0 && !math.IsNaN(width) && !math.IsInf(width, 0)
}

// windowedGray maps a modality value to 8-bit grayscale using the selected
// DICOM VOI function. It is a thin compatibility adapter over the display
// pipeline (pixeldata/display): the viewer keeps only the NaN guard for sampled
// voxels that fall outside a volume, delegating the windowing math to the
// library so the clinical display logic lives in one place.
type preparedVOI struct {
	mapper  display.VOIByteMapper
	invalid bool
}

func windowedGrayMapped(value float64, prepared preparedVOI) uint8 {
	if math.IsNaN(value) || prepared.invalid {
		return 0
	}
	return prepared.mapper.Byte(value)
}

// WindowedGray maps a rescaled modality value to an 8-bit grayscale value
// using the same DICOM VOI path as RenderFrame. Presentation widgets such as
// histograms can therefore match the displayed image without duplicating the
// windowing formula.
func WindowedGray(value float64, window WindowLevel) uint8 {
	return windowedGrayMapped(value, prepareWindow(window))
}

func prepareWindow(window WindowLevel) preparedVOI {
	return preparedVOI{
		mapper:  display.NewVOIByteMapper(display.VOILUT{Center: window.Center, Width: window.Width, Function: window.Function, LUT: window.LUT}),
		invalid: math.IsNaN(window.Center) || math.IsNaN(window.Width),
	}
}

func windowedUnit(value float64, window WindowLevel) float64 {
	if math.IsNaN(value) || math.IsNaN(window.Center) || math.IsNaN(window.Width) {
		return 0
	}
	return display.VOILUT{Center: window.Center, Width: window.Width, Function: window.Function}.WindowUnit(value)
}

func RenderFramePNG(slice *Frame, window WindowLevel) ([]byte, error) {
	img, err := RenderFrame(slice, window)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func RenderThumbnail(series *Stack) image.Image {
	if series == nil {
		return blankImage(128, 128)
	}
	img, err := RenderFrame(series.FirstDisplayFrame(), series.DefaultWindow)
	if err != nil || img == nil {
		return blankImage(128, 128)
	}
	return img
}

func WindowSliderRanges(window WindowLevel) (centerMin, centerMax, widthMax float64) {
	centerSpan := math.Max(2048, math.Abs(window.Center)*2+window.Width*2)
	centerMin = window.Center - centerSpan
	centerMax = window.Center + centerSpan
	widthMax = math.Max(4096, window.Width*4)
	return centerMin, centerMax, widthMax
}

func ValidateWindow(window WindowLevel) error {
	if window.LUT != nil && len(window.LUT.Entries) > 0 {
		return nil
	}
	if math.IsNaN(window.Center) || math.IsInf(window.Center, 0) {
		return fmt.Errorf("render: window center must be finite")
	}
	if math.IsNaN(window.Width) || math.IsInf(window.Width, 0) || window.Width <= 0 {
		return fmt.Errorf("render: window width must be positive and finite")
	}
	return nil
}

func blankImage(width, height int) *image.Gray {
	if width <= 0 {
		width = 512
	}
	if height <= 0 {
		height = 512
	}
	img := image.NewGray(image.Rect(0, 0, width, height))
	fill := color.Gray{Y: 24}
	for i := range img.Pix {
		img.Pix[i] = fill.Y
	}
	return img
}
