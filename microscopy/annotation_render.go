package microscopy

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

type AnnotationRenderOptions struct {
	OpticalPath string
	FocalZ      float64
	ZTolerance  float64
	Color       color.NRGBA
}

// RenderAnnotations draws Microscopy Bulk Simple Annotations into a bounded
// viewport image. It supports Total Pixel Matrix ("VOLUME") coordinates and
// calibrated 3D slide coordinates without allocating the complete slide.
func RenderAnnotations(
	target *image.RGBA,
	viewport image.Rectangle,
	level Level,
	groups []AnnotationGroup,
	options AnnotationRenderOptions,
) error {
	if target == nil || target.Bounds().Empty() || viewport.Empty() {
		return fmt.Errorf("dicom/microscopy: annotation target and viewport are required")
	}
	ink := options.Color
	if ink.A == 0 {
		ink = color.NRGBA{R: 255, G: 214, B: 64, A: 255}
	}
	for _, group := range groups {
		if !annotationPathVisible(group, options.OpticalPath) ||
			!annotationPlaneVisible(group, options.FocalZ, options.ZTolerance) {
			continue
		}
		primitives, err := annotationMatrixPrimitives(group, level)
		if err != nil {
			return err
		}
		for _, primitive := range primitives {
			points := make([]image.Point, 0, len(primitive.Points))
			for _, point := range primitive.Points {
				matrixPoint := image.Pt(int(math.Round(point.X)), int(math.Round(point.Y)))
				points = append(points, annotationTargetPoint(matrixPoint, viewport, target.Bounds()))
			}
			drawAnnotationPrimitive(target, points, group.GraphicType, ink)
		}
	}
	return nil
}

func annotationMatrixPrimitives(group AnnotationGroup, level Level) ([]Primitive, error) {
	if group.CoordinateType == "2D" {
		if group.PixelOrigin != "VOLUME" {
			return nil, fmt.Errorf("dicom/microscopy: FRAME-relative annotations require a frame transform")
		}
		return clonePrimitives(group.Primitives), nil
	}
	out := clonePrimitives(group.Primitives)
	for primitiveIndex := range out {
		for pointIndex, point := range out[primitiveIndex].Points {
			x, y, err := level.PixelCoordinate(SlidePoint{X: point.X, Y: point.Y, Z: point.Z})
			if err != nil {
				return nil, err
			}
			out[primitiveIndex].Points[pointIndex].X = x
			out[primitiveIndex].Points[pointIndex].Y = y
		}
	}
	return out, nil
}

func annotationPathVisible(group AnnotationGroup, selected string) bool {
	if selected == "" || group.AppliesAllOpticalPaths {
		return true
	}
	for _, identifier := range group.OpticalPaths {
		if identifier == selected {
			return true
		}
	}
	return false
}

func annotationPlaneVisible(group AnnotationGroup, focalZ, tolerance float64) bool {
	if group.AppliesAllZPlanes || len(group.CommonZCoordinates) == 0 {
		return true
	}
	if tolerance < 0 {
		tolerance = 0
	}
	for _, z := range group.CommonZCoordinates {
		if math.Abs(z-focalZ) <= tolerance {
			return true
		}
	}
	return false
}

func annotationTargetPoint(matrix image.Point, viewport, target image.Rectangle) image.Point {
	x := target.Min.X + int(math.Round(
		float64(matrix.X-viewport.Min.X)*float64(target.Dx())/float64(viewport.Dx()),
	))
	y := target.Min.Y + int(math.Round(
		float64(matrix.Y-viewport.Min.Y)*float64(target.Dy())/float64(viewport.Dy()),
	))
	return image.Pt(x, y)
}

func drawAnnotationPrimitive(target *image.RGBA, points []image.Point, graphicType string, ink color.NRGBA) {
	if len(points) == 0 {
		return
	}
	if graphicType == "POINT" || len(points) == 1 {
		point := points[0]
		for delta := -2; delta <= 2; delta++ {
			setAnnotationPixel(target, point.X+delta, point.Y, ink)
			setAnnotationPixel(target, point.X, point.Y+delta, ink)
		}
		return
	}
	if graphicType == "ELLIPSE" && len(points) == 4 && drawAnnotationEllipse(target, points, ink) {
		return
	}
	for index := 1; index < len(points); index++ {
		drawAnnotationLine(target, points[index-1], points[index], ink)
	}
	if graphicType == "POLYGON" || graphicType == "RECTANGLE" || graphicType == "ELLIPSE" {
		drawAnnotationLine(target, points[len(points)-1], points[0], ink)
	}
}

func drawAnnotationLine(target *image.RGBA, from, to image.Point, ink color.NRGBA) {
	var visible bool
	from, to, visible = clipAnnotationLine(target.Bounds(), from, to)
	if !visible {
		return
	}
	dx := absInt(to.X - from.X)
	dy := -absInt(to.Y - from.Y)
	stepX, stepY := -1, -1
	if from.X < to.X {
		stepX = 1
	}
	if from.Y < to.Y {
		stepY = 1
	}
	err := dx + dy
	for {
		setAnnotationPixel(target, from.X, from.Y, ink)
		if from == to {
			return
		}
		twice := 2 * err
		if twice >= dy {
			err += dy
			from.X += stepX
		}
		if twice <= dx {
			err += dx
			from.Y += stepY
		}
	}
}

func drawAnnotationEllipse(target *image.RGBA, points []image.Point, ink color.NRGBA) bool {
	majorX := (float64(points[1].X) - float64(points[0].X)) / 2
	majorY := (float64(points[1].Y) - float64(points[0].Y)) / 2
	minorX := (float64(points[3].X) - float64(points[2].X)) / 2
	minorY := (float64(points[3].Y) - float64(points[2].Y)) / 2
	if math.Hypot(majorX, majorY) == 0 || math.Hypot(minorX, minorY) == 0 {
		return false
	}
	centerX := (float64(points[0].X) + float64(points[1].X) + float64(points[2].X) + float64(points[3].X)) / 4
	centerY := (float64(points[0].Y) + float64(points[1].Y) + float64(points[2].Y) + float64(points[3].Y)) / 4
	maxSteps := max(32, 4*(target.Bounds().Dx()+target.Bounds().Dy()))
	estimatedSteps := math.Ceil(2 * math.Pi * math.Max(
		math.Hypot(majorX, majorY),
		math.Hypot(minorX, minorY),
	))
	steps := maxSteps
	if estimatedSteps < float64(maxSteps) {
		steps = max(32, int(estimatedSteps))
	}
	pointAt := func(angle float64) image.Point {
		cosine, sine := math.Cos(angle), math.Sin(angle)
		return image.Pt(
			int(math.Round(centerX+majorX*cosine+minorX*sine)),
			int(math.Round(centerY+majorY*cosine+minorY*sine)),
		)
	}
	previous := pointAt(0)
	for step := 1; step <= steps; step++ {
		current := pointAt(2 * math.Pi * float64(step) / float64(steps))
		drawAnnotationLine(target, previous, current, ink)
		previous = current
	}
	return true
}

func clipAnnotationLine(bounds image.Rectangle, from, to image.Point) (image.Point, image.Point, bool) {
	if bounds.Empty() {
		return image.Point{}, image.Point{}, false
	}
	x0, y0 := float64(from.X), float64(from.Y)
	dx := float64(to.X) - x0
	dy := float64(to.Y) - y0
	minX, minY := float64(bounds.Min.X), float64(bounds.Min.Y)
	maxX, maxY := float64(bounds.Max.X-1), float64(bounds.Max.Y-1)
	start, end := 0.0, 1.0
	for _, edge := range []struct {
		delta    float64
		distance float64
	}{
		{delta: -dx, distance: x0 - minX},
		{delta: dx, distance: maxX - x0},
		{delta: -dy, distance: y0 - minY},
		{delta: dy, distance: maxY - y0},
	} {
		if edge.delta == 0 {
			if edge.distance < 0 {
				return image.Point{}, image.Point{}, false
			}
			continue
		}
		ratio := edge.distance / edge.delta
		if edge.delta < 0 {
			if ratio > end {
				return image.Point{}, image.Point{}, false
			}
			start = math.Max(start, ratio)
		} else {
			if ratio < start {
				return image.Point{}, image.Point{}, false
			}
			end = math.Min(end, ratio)
		}
	}
	clippedPoint := func(ratio float64) image.Point {
		return image.Pt(
			min(bounds.Max.X-1, max(bounds.Min.X, int(math.Round(x0+ratio*dx)))),
			min(bounds.Max.Y-1, max(bounds.Min.Y, int(math.Round(y0+ratio*dy)))),
		)
	}
	return clippedPoint(start), clippedPoint(end), true
}

func setAnnotationPixel(target *image.RGBA, x, y int, ink color.NRGBA) {
	if image.Pt(x, y).In(target.Bounds()) {
		target.Set(x, y, ink)
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
