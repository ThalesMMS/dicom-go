package microscopy

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

type Viewport struct {
	CenterX             float64
	CenterY             float64
	MatrixPixelsPerView float64
	ScreenWidth         int
	ScreenHeight        int
	RotationDegrees     int
	OpticalPaths        []Channel
	FocalPlane          int
}

type Channel struct {
	OpticalPath string
	Tint        color.NRGBA
	Opacity     float64
}

type TileLayer struct {
	Tile    Tile
	Image   image.Image
	Tint    color.NRGBA
	Opacity float64
}

type ScaleBar struct {
	Pixels      float64
	Millimeters float64
	Label       string
}

func (v Viewport) Bounds(level Level) image.Rectangle {
	if v.ScreenWidth <= 0 || v.ScreenHeight <= 0 {
		return image.Rectangle{}
	}
	scale := v.MatrixPixelsPerView
	if !finitePositive(scale) {
		scale = 1
	}
	width := int(math.Ceil(float64(v.ScreenWidth) * scale))
	height := int(math.Ceil(float64(v.ScreenHeight) * scale))
	centerX := v.CenterX
	centerY := v.CenterY
	if math.IsNaN(centerX) || math.IsInf(centerX, 0) {
		centerX = float64(level.MatrixWidth) / 2
	}
	if math.IsNaN(centerY) || math.IsInf(centerY, 0) {
		centerY = float64(level.MatrixHeight) / 2
	}
	left := int(math.Floor(centerX - float64(width)/2))
	top := int(math.Floor(centerY - float64(height)/2))
	return image.Rect(left, top, left+width, top+height).Intersect(level.MatrixBounds())
}

func (v Viewport) VisibleTiles(level Level) []Tile {
	return v.VisibleTilesLimit(level, 0)
}

func (v Viewport) VisibleTilesLimit(level Level, limit int) []Tile {
	bounds := v.Bounds(level)
	if len(v.OpticalPaths) == 0 {
		return level.TilesForViewportLimit(bounds, "", v.FocalPlane, limit)
	}
	var out []Tile
	perChannelLimit := 0
	if limit > 0 {
		perChannelLimit = max(1, limit/len(v.OpticalPaths))
	}
	for _, channel := range v.OpticalPaths {
		out = append(out, level.TilesForViewportLimit(bounds, channel.OpticalPath, v.FocalPlane, perChannelLimit)...)
	}
	out = deduplicateTiles(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (v Viewport) ScaleBar(level Level) (ScaleBar, error) {
	if !level.Calibrated() {
		return ScaleBar{}, ErrUncalibratedSlide
	}
	scale := v.MatrixPixelsPerView
	if !finitePositive(scale) {
		scale = 1
	}
	mmPerScreenPixel := level.PixelSpacingX * scale
	targetMillimeters := mmPerScreenPixel * 100
	value := niceScaleValue(targetMillimeters)
	pixels := value / mmPerScreenPixel
	return ScaleBar{
		Pixels: pixels, Millimeters: value,
		Label: scaleBarLabel(value),
	}, nil
}

// OverviewViewport maps the visible rectangle into overview-image pixels.
func (v Viewport) OverviewViewport(level Level, overviewSize image.Point) image.Rectangle {
	bounds := v.Bounds(level)
	if bounds.Empty() || overviewSize.X <= 0 || overviewSize.Y <= 0 {
		return image.Rectangle{}
	}
	mapMinX := func(value int) int {
		return int(math.Floor(float64(value) * float64(overviewSize.X) / float64(level.MatrixWidth)))
	}
	mapMaxX := func(value int) int {
		return int(math.Ceil(float64(value) * float64(overviewSize.X) / float64(level.MatrixWidth)))
	}
	mapMinY := func(value int) int {
		return int(math.Floor(float64(value) * float64(overviewSize.Y) / float64(level.MatrixHeight)))
	}
	mapMaxY := func(value int) int {
		return int(math.Ceil(float64(value) * float64(overviewSize.Y) / float64(level.MatrixHeight)))
	}
	return image.Rect(mapMinX(bounds.Min.X), mapMinY(bounds.Min.Y), mapMaxX(bounds.Max.X), mapMaxY(bounds.Max.Y)).
		Intersect(image.Rect(0, 0, overviewSize.X, overviewSize.Y))
}

// ComposeViewport composites only the supplied viewport-sized tile layers. It
// never allocates the Total Pixel Matrix. Coordinates are nearest-neighbor to
// preserve source pixels; UI toolkits may resample the bounded result.
func ComposeViewport(viewport image.Rectangle, layers []TileLayer, rotationDegrees int) (*image.RGBA, error) {
	return ComposeViewportToSize(viewport, viewport.Size(), layers, rotationDegrees)
}

// ComposeViewportToSize samples visible tiles directly into a caller-bounded
// output. This remains safe even when one screen pixel spans many Total Pixel
// Matrix pixels at overview zoom levels.
func ComposeViewportToSize(viewport image.Rectangle, outputSize image.Point, layers []TileLayer, rotationDegrees int) (*image.RGBA, error) {
	if viewport.Empty() {
		return nil, fmt.Errorf("dicom/microscopy: empty viewport")
	}
	if outputSize.X <= 0 || outputSize.Y <= 0 {
		return nil, fmt.Errorf("dicom/microscopy: empty output")
	}
	if int64(outputSize.X)*int64(outputSize.Y) > 100_000_000 {
		return nil, fmt.Errorf("dicom/microscopy: output %dx%d exceeds safety limit", outputSize.X, outputSize.Y)
	}
	out := image.NewRGBA(image.Rect(0, 0, outputSize.X, outputSize.Y))
	for _, layer := range layers {
		if layer.Image == nil {
			continue
		}
		intersection := layer.Tile.Bounds().Intersect(viewport)
		if intersection.Empty() {
			continue
		}
		opacity := layer.Opacity
		if opacity <= 0 {
			continue
		}
		if opacity > 1 {
			opacity = 1
		}
		tint := layer.Tint
		if tint.A == 0 {
			tint = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
		}
		sourceBounds := layer.Image.Bounds()
		targetMinX := int(math.Floor(float64(intersection.Min.X-viewport.Min.X) * float64(outputSize.X) / float64(viewport.Dx())))
		targetMaxX := int(math.Ceil(float64(intersection.Max.X-viewport.Min.X) * float64(outputSize.X) / float64(viewport.Dx())))
		targetMinY := int(math.Floor(float64(intersection.Min.Y-viewport.Min.Y) * float64(outputSize.Y) / float64(viewport.Dy())))
		targetMaxY := int(math.Ceil(float64(intersection.Max.Y-viewport.Min.Y) * float64(outputSize.Y) / float64(viewport.Dy())))
		targetBounds := image.Rect(targetMinX, targetMinY, targetMaxX, targetMaxY).Intersect(out.Bounds())
		for targetY := targetBounds.Min.Y; targetY < targetBounds.Max.Y; targetY++ {
			matrixY := viewport.Min.Y + int((float64(targetY)+0.5)*float64(viewport.Dy())/float64(outputSize.Y))
			sourceY := sourceBounds.Min.Y + (matrixY-layer.Tile.Row)*sourceBounds.Dy()/max(1, layer.Tile.Height)
			if sourceY < sourceBounds.Min.Y || sourceY >= sourceBounds.Max.Y {
				continue
			}
			for targetX := targetBounds.Min.X; targetX < targetBounds.Max.X; targetX++ {
				matrixX := viewport.Min.X + int((float64(targetX)+0.5)*float64(viewport.Dx())/float64(outputSize.X))
				sourceX := sourceBounds.Min.X + (matrixX-layer.Tile.Column)*sourceBounds.Dx()/max(1, layer.Tile.Width)
				if sourceX < sourceBounds.Min.X || sourceX >= sourceBounds.Max.X {
					continue
				}
				source := color.NRGBAModel.Convert(layer.Image.At(sourceX, sourceY)).(color.NRGBA)
				alpha := opacity * float64(source.A) / 255 * float64(tint.A) / 255
				blendNRGBA(out, targetX, targetY, color.NRGBA{
					R: uint8(uint16(source.R) * uint16(tint.R) / 255),
					G: uint8(uint16(source.G) * uint16(tint.G) / 255),
					B: uint8(uint16(source.B) * uint16(tint.B) / 255),
					A: uint8(math.Round(alpha * 255)),
				})
			}
		}
	}
	return rotateRightAngles(out, rotationDegrees)
}

// RotateViewport applies a lossless right-angle presentation rotation to a
// bounded rendered viewport.
func RotateViewport(source *image.RGBA, rotationDegrees int) (*image.RGBA, error) {
	if source == nil {
		return nil, fmt.Errorf("dicom/microscopy: nil viewport image")
	}
	return rotateRightAngles(source, rotationDegrees)
}

func blendNRGBA(destination *image.RGBA, x, y int, source color.NRGBA) {
	if source.A == 0 {
		return
	}
	current := color.NRGBAModel.Convert(destination.At(x, y)).(color.NRGBA)
	sourceAlpha := float64(source.A) / 255
	destinationAlpha := float64(current.A) / 255
	outAlpha := sourceAlpha + destinationAlpha*(1-sourceAlpha)
	if outAlpha == 0 {
		return
	}
	blend := func(src, dst uint8) uint8 {
		value := (float64(src)*sourceAlpha + float64(dst)*destinationAlpha*(1-sourceAlpha)) / outAlpha
		return uint8(math.Round(value))
	}
	destination.Set(x, y, color.NRGBA{
		R: blend(source.R, current.R),
		G: blend(source.G, current.G),
		B: blend(source.B, current.B),
		A: uint8(math.Round(outAlpha * 255)),
	})
}

func rotateRightAngles(source *image.RGBA, degrees int) (*image.RGBA, error) {
	degrees = ((degrees % 360) + 360) % 360
	if degrees%90 != 0 {
		return nil, fmt.Errorf("dicom/microscopy: rotation must be a multiple of 90 degrees")
	}
	if degrees == 0 {
		return source, nil
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var out *image.RGBA
	if degrees == 90 || degrees == 270 {
		out = image.NewRGBA(image.Rect(0, 0, height, width))
	} else {
		out = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			switch degrees {
			case 90:
				out.Set(height-1-y, x, source.At(bounds.Min.X+x, bounds.Min.Y+y))
			case 180:
				out.Set(width-1-x, height-1-y, source.At(bounds.Min.X+x, bounds.Min.Y+y))
			case 270:
				out.Set(y, width-1-x, source.At(bounds.Min.X+x, bounds.Min.Y+y))
			}
		}
	}
	return out, nil
}

func niceScaleValue(value float64) float64 {
	if !finitePositive(value) {
		return 0
	}
	exponent := math.Floor(math.Log10(value))
	fraction := value / math.Pow(10, exponent)
	var nice float64
	switch {
	case fraction < 1.5:
		nice = 1
	case fraction < 3.5:
		nice = 2
	case fraction < 7.5:
		nice = 5
	default:
		nice = 10
	}
	return nice * math.Pow(10, exponent)
}

func scaleBarLabel(millimeters float64) string {
	if millimeters < 1 {
		return fmt.Sprintf("%g µm", millimeters*1000)
	}
	return fmt.Sprintf("%g mm", millimeters)
}
