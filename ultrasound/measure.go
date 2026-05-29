package ultrasound

import (
	"fmt"
	"image"
	"math"
)

type Measurement struct {
	Value     float64
	Unit      PhysicalUnit
	Reference RegionReference
}

// AxisMeasurement carries both calibrated axes for Doppler/time workflows.
type AxisMeasurement struct {
	X         float64
	XUnit     PhysicalUnit
	Y         float64
	YUnit     PhysicalUnit
	Reference RegionReference
}

func Distance(frame FrameCalibration, a, b image.Point) (Measurement, error) {
	region, err := compatibleRegion(frame, []image.Point{a, b}, UnitCentimeter, UnitCentimeter, false)
	if err != nil {
		return Measurement{}, err
	}
	ax, ay := region.Physical(a)
	bx, by := region.Physical(b)
	return result(frame, region, math.Hypot(bx-ax, by-ay), UnitCentimeter), nil
}

func PolylineLength(frame FrameCalibration, points []image.Point) (Measurement, error) {
	if len(points) < 2 {
		return Measurement{}, fmt.Errorf("dicom/ultrasound: polyline requires at least two points")
	}
	region, err := compatibleRegion(frame, points, UnitCentimeter, UnitCentimeter, false)
	if err != nil {
		return Measurement{}, err
	}
	var total float64
	px, py := region.Physical(points[0])
	for _, point := range points[1:] {
		x, y := region.Physical(point)
		total += math.Hypot(x-px, y-py)
		px, py = x, y
	}
	return result(frame, region, total, UnitCentimeter), nil
}

func Circumference(frame FrameCalibration, points []image.Point) (Measurement, error) {
	if len(points) < 3 {
		return Measurement{}, fmt.Errorf("dicom/ultrasound: circumference requires at least three points")
	}
	closed := append(append([]image.Point(nil), points...), points[0])
	return PolylineLength(frame, closed)
}

func RectangleArea(frame FrameCalibration, a, b image.Point) (Measurement, error) {
	points := []image.Point{a, b, image.Pt(a.X, b.Y), image.Pt(b.X, a.Y)}
	region, err := compatibleRegion(frame, points, UnitCentimeter, UnitCentimeter, false)
	if err != nil {
		return Measurement{}, err
	}
	ax, ay := region.Physical(a)
	bx, by := region.Physical(b)
	return result(frame, region, math.Abs((bx-ax)*(by-ay)), UnitSquareCentimeter), nil
}

func EllipseArea(frame FrameCalibration, bounds image.Rectangle) (Measurement, error) {
	a, b := bounds.Min, bounds.Max
	points := []image.Point{a, b, image.Pt(a.X, b.Y), image.Pt(b.X, a.Y)}
	region, err := compatibleRegion(frame, points, UnitCentimeter, UnitCentimeter, false)
	if err != nil {
		return Measurement{}, err
	}
	ax, ay := region.Physical(a)
	bx, by := region.Physical(b)
	return result(frame, region, math.Pi*math.Abs(bx-ax)*math.Abs(by-ay)/4, UnitSquareCentimeter), nil
}

func PolygonArea(frame FrameCalibration, points []image.Point) (Measurement, error) {
	if len(points) < 3 {
		return Measurement{}, fmt.Errorf("dicom/ultrasound: polygon requires at least three points")
	}
	region, err := compatibleRegion(frame, points, UnitCentimeter, UnitCentimeter, false)
	if err != nil {
		return Measurement{}, err
	}
	var twiceArea float64
	for index, point := range points {
		next := points[(index+1)%len(points)]
		x1, y1 := region.Physical(point)
		x2, y2 := region.Physical(next)
		twiceArea += x1*y2 - x2*y1
	}
	return result(frame, region, math.Abs(twiceArea)/2, UnitSquareCentimeter), nil
}

// Doppler maps a spectral/waveform point to time and velocity. Frequency
// Doppler axes are deliberately rejected rather than silently relabeled.
func Doppler(frame FrameCalibration, point image.Point) (AxisMeasurement, error) {
	region, err := compatibleRegion(frame, []image.Point{point}, UnitSecond, UnitCentimeterPerSec, true)
	if err != nil {
		return AxisMeasurement{}, err
	}
	if region.SpatialFormat != SpatialSpectral && region.SpatialFormat != SpatialWaveform {
		return AxisMeasurement{}, fmt.Errorf("%w: region %d is not spectral or waveform data", ErrUnsupportedUnits, region.Index)
	}
	if region.Flags&(1<<2) != 0 {
		return AxisMeasurement{}, fmt.Errorf("%w: region %d uses a frequency Doppler scale", ErrUnsupportedUnits, region.Index)
	}
	x, y := region.Physical(point)
	return AxisMeasurement{
		X: x, XUnit: region.UnitsX,
		Y: y, YUnit: region.UnitsY,
		Reference: RegionReference{FrameNumber: frame.FrameIndex + 1, RegionIndex: region.Index},
	}, nil
}

func result(frame FrameCalibration, region Region, value float64, unit PhysicalUnit) Measurement {
	return Measurement{
		Value: value,
		Unit:  unit,
		Reference: RegionReference{
			FrameNumber: frame.FrameIndex + 1,
			RegionIndex: region.Index,
		},
	}
}

func compatibleRegion(frame FrameCalibration, points []image.Point, unitsX, unitsY PhysicalUnit, absoluteAxes bool) (Region, error) {
	if len(points) == 0 {
		return Region{}, ErrUncalibrated
	}
	var candidate Region
	candidateCount := 0
	for _, region := range frame.Regions {
		if region.UnitsX != unitsX || region.UnitsY != unitsY {
			continue
		}
		containsAll := true
		for _, point := range points {
			if !region.Contains(point) {
				containsAll = false
				break
			}
		}
		if containsAll {
			if candidateCount == 0 {
				candidate = region
			} else if !sameScaling(candidate, region, absoluteAxes) {
				return Region{}, ErrAmbiguousRegion
			}
			candidateCount++
		}
	}
	if candidateCount > 0 {
		return candidate, nil
	}
	for _, point := range points {
		for _, region := range frame.Regions {
			if region.Contains(point) && (region.UnitsX != unitsX || region.UnitsY != unitsY) {
				return Region{}, fmt.Errorf("%w: point %v uses %s/%s", ErrUnsupportedUnits, point, region.UnitsX, region.UnitsY)
			}
		}
	}
	if pointsOccupyDifferentRegions(frame.Regions, points) {
		return Region{}, ErrCrossRegion
	}
	return Region{}, ErrUncalibrated
}

func sameScaling(a, b Region, absoluteAxes bool) bool {
	if a.UnitsX != b.UnitsX || a.UnitsY != b.UnitsY ||
		a.DeltaX != b.DeltaX || a.DeltaY != b.DeltaY {
		return false
	}
	if !absoluteAxes {
		return true
	}
	return a.ReferenceValueX == b.ReferenceValueX && a.ReferenceValueY == b.ReferenceValueY &&
		a.Bounds.Min.X+a.ReferencePixelX == b.Bounds.Min.X+b.ReferencePixelX &&
		a.Bounds.Min.Y+a.ReferencePixelY == b.Bounds.Min.Y+b.ReferencePixelY
}

func pointsOccupyDifferentRegions(regions []Region, points []image.Point) bool {
	var first = -1
	inside := 0
	for _, point := range points {
		current := -1
		for _, region := range regions {
			if region.Contains(point) {
				current = region.Index
				break
			}
		}
		if current < 0 {
			continue
		}
		inside++
		if first < 0 {
			first = current
		} else if first != current {
			return true
		}
	}
	return inside > 0 && inside < len(points)
}
