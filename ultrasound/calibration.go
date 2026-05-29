// Package ultrasound reads and applies DICOM Ultrasound Region Calibration.
//
// Region calibration is frame-scoped: enhanced per-frame functional groups
// override shared functional groups, which override a top-level legacy
// Sequence of Ultrasound Regions. Pixel Data is never decoded by this package.
package ultrasound

import (
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagNumberOfFrames           = core.NewTag(0x0028, 0x0008)
	tagSharedFunctionalGroups   = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups = core.NewTag(0x5200, 0x9230)
	tagUSRegions                = core.NewTag(0x0018, 0x6011)
	tagSpatialFormat            = core.NewTag(0x0018, 0x6012)
	tagDataType                 = core.NewTag(0x0018, 0x6014)
	tagFlags                    = core.NewTag(0x0018, 0x6016)
	tagMinX                     = core.NewTag(0x0018, 0x6018)
	tagMinY                     = core.NewTag(0x0018, 0x601A)
	tagMaxX                     = core.NewTag(0x0018, 0x601C)
	tagMaxY                     = core.NewTag(0x0018, 0x601E)
	tagReferenceX               = core.NewTag(0x0018, 0x6020)
	tagReferenceY               = core.NewTag(0x0018, 0x6022)
	tagUnitsX                   = core.NewTag(0x0018, 0x6024)
	tagUnitsY                   = core.NewTag(0x0018, 0x6026)
	tagReferenceValueX          = core.NewTag(0x0018, 0x6028)
	tagReferenceValueY          = core.NewTag(0x0018, 0x602A)
	tagDeltaX                   = core.NewTag(0x0018, 0x602C)
	tagDeltaY                   = core.NewTag(0x0018, 0x602E)
)

var (
	ErrUncalibrated     = errors.New("dicom/ultrasound: no compatible calibration")
	ErrCrossRegion      = errors.New("dicom/ultrasound: measurement crosses incompatible regions")
	ErrAmbiguousRegion  = errors.New("dicom/ultrasound: overlapping calibrations are ambiguous")
	ErrUnsupportedUnits = errors.New("dicom/ultrasound: unsupported physical units")
)

// SpatialFormat is Region Spatial Format (0018,6012).
type SpatialFormat uint16

const (
	SpatialNone     SpatialFormat = 0
	Spatial2D       SpatialFormat = 1
	SpatialMMode    SpatialFormat = 2
	SpatialSpectral SpatialFormat = 3
	SpatialWaveform SpatialFormat = 4
	SpatialGraphics SpatialFormat = 5
)

// PhysicalUnit is the DICOM US Region physical-unit enumeration.
type PhysicalUnit uint16

const (
	UnitNone             PhysicalUnit = 0
	UnitPercent          PhysicalUnit = 1
	UnitDecibel          PhysicalUnit = 2
	UnitCentimeter       PhysicalUnit = 3
	UnitSecond           PhysicalUnit = 4
	UnitHertz            PhysicalUnit = 5
	UnitDecibelPerSec    PhysicalUnit = 6
	UnitCentimeterPerSec PhysicalUnit = 7
	UnitSquareCentimeter PhysicalUnit = 8
	UnitSquareCMPerSec   PhysicalUnit = 9
	UnitCubicCentimeter  PhysicalUnit = 10
	UnitCubicCMPerSec    PhysicalUnit = 11
	UnitDegree           PhysicalUnit = 12
)

func (u PhysicalUnit) String() string {
	switch u {
	case UnitPercent:
		return "%"
	case UnitDecibel:
		return "dB"
	case UnitCentimeter:
		return "cm"
	case UnitSecond:
		return "s"
	case UnitHertz:
		return "Hz"
	case UnitDecibelPerSec:
		return "dB/s"
	case UnitCentimeterPerSec:
		return "cm/s"
	case UnitSquareCentimeter:
		return "cm2"
	case UnitSquareCMPerSec:
		return "cm2/s"
	case UnitCubicCentimeter:
		return "cm3"
	case UnitCubicCMPerSec:
		return "cm3/s"
	case UnitDegree:
		return "deg"
	default:
		return "none"
	}
}

// Region describes one item of Sequence of Ultrasound Regions. Bounds are
// inclusive DICOM image coordinates. ReferencePixel is relative to Bounds.Min.
type Region struct {
	Index int

	Bounds        image.Rectangle
	SpatialFormat SpatialFormat
	DataType      uint16
	Flags         uint32
	UnitsX        PhysicalUnit
	UnitsY        PhysicalUnit
	DeltaX        float64
	DeltaY        float64

	ReferencePixelX int
	ReferencePixelY int
	ReferenceValueX float64
	ReferenceValueY float64
}

// FrameCalibration identifies a zero-based source frame and all of its regions.
type FrameCalibration struct {
	FrameIndex int
	Regions    []Region
}

type Calibration struct {
	Frames []FrameCalibration
}

// RegionReference is the evidence carried with a calibrated result.
type RegionReference struct {
	FrameNumber int // one-based DICOM frame number
	RegionIndex int // zero-based Sequence item index
}

func (r Region) Contains(point image.Point) bool {
	return point.X >= r.Bounds.Min.X && point.X <= r.Bounds.Max.X &&
		point.Y >= r.Bounds.Min.Y && point.Y <= r.Bounds.Max.Y
}

// Physical maps an image pixel to the region's physical axes. DICOM reference
// pixel coordinates are offsets from the region minimum, not the image origin.
func (r Region) Physical(point image.Point) (x, y float64) {
	referenceX := r.Bounds.Min.X + r.ReferencePixelX
	referenceY := r.Bounds.Min.Y + r.ReferencePixelY
	return r.ReferenceValueX + float64(point.X-referenceX)*r.DeltaX,
		r.ReferenceValueY + float64(point.Y-referenceY)*r.DeltaY
}

func (c Calibration) Frame(index int) (FrameCalibration, bool) {
	if index < 0 || index >= len(c.Frames) {
		return FrameCalibration{}, false
	}
	return c.Frames[index], true
}

// Read parses legacy, shared, and per-frame region calibration. Per-frame
// absence falls back to shared then legacy calibration without inspecting any
// sibling per-frame item, preventing metadata leakage between frames.
func Read(file *object.File) (Calibration, error) {
	if file == nil || file.Dataset == nil {
		return Calibration{}, fmt.Errorf("dicom/ultrasound: nil dataset")
	}
	root := file.Dataset
	frameCount := derivedio.Int(root, tagNumberOfFrames)
	perFrame := derivedio.Sequence(root, tagPerFrameFunctionalGroups)
	if frameCount <= 0 {
		frameCount = 1
		if len(perFrame) > 1 {
			frameCount = len(perFrame)
		}
	}
	if frameCount > 1_000_000 {
		return Calibration{}, fmt.Errorf("dicom/ultrasound: unreasonable frame count %d", frameCount)
	}
	if len(perFrame) != 0 && len(perFrame) != frameCount {
		return Calibration{}, fmt.Errorf("dicom/ultrasound: %d per-frame groups for %d frames", len(perFrame), frameCount)
	}

	legacy, err := readRegionItems(derivedio.Sequence(root, tagUSRegions))
	if err != nil {
		return Calibration{}, fmt.Errorf("dicom/ultrasound: top-level calibration: %w", err)
	}
	var shared []Region
	if items := derivedio.Sequence(root, tagSharedFunctionalGroups); len(items) > 0 {
		sharedItems := recursiveRegionItems(items[0], 0)
		shared, err = readRegionItems(sharedItems)
		if err != nil {
			return Calibration{}, fmt.Errorf("dicom/ultrasound: shared calibration: %w", err)
		}
	}

	out := Calibration{Frames: make([]FrameCalibration, frameCount)}
	for frameIndex := 0; frameIndex < frameCount; frameIndex++ {
		regions := shared
		if len(regions) == 0 {
			regions = legacy
		}
		if frameIndex < len(perFrame) {
			items := recursiveRegionItems(perFrame[frameIndex], 0)
			if len(items) > 0 {
				regions, err = readRegionItems(items)
				if err != nil {
					return Calibration{}, fmt.Errorf("dicom/ultrasound: frame %d calibration: %w", frameIndex+1, err)
				}
			}
		}
		out.Frames[frameIndex] = FrameCalibration{
			FrameIndex: frameIndex,
			Regions:    append([]Region(nil), regions...),
		}
	}
	return out, nil
}

func recursiveRegionItems(obj *object.Object, depth int) []*object.Object {
	if obj == nil || depth > 4 {
		return nil
	}
	if items := derivedio.Sequence(obj, tagUSRegions); len(items) > 0 {
		return items
	}
	for _, element := range obj.Elements() {
		if element.VR() != core.VRSQ {
			continue
		}
		items, ok := obj.GetSequence(element.Tag())
		if !ok {
			continue
		}
		for _, item := range items {
			if found := recursiveRegionItems(item, depth+1); len(found) > 0 {
				return found
			}
		}
	}
	return nil
}

func readRegionItems(items []*object.Object) ([]Region, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]Region, len(items))
	for index, item := range items {
		if item == nil {
			return nil, fmt.Errorf("region %d is nil", index)
		}
		minX, okMinX := requiredInt(item, tagMinX)
		minY, okMinY := requiredInt(item, tagMinY)
		maxX, okMaxX := requiredInt(item, tagMaxX)
		maxY, okMaxY := requiredInt(item, tagMaxY)
		spatial, okSpatial := requiredInt(item, tagSpatialFormat)
		dataType, okDataType := requiredUint16(item, tagDataType)
		flags, okFlags := requiredUint32(item, tagFlags)
		unitsX, okUnitsX := requiredInt(item, tagUnitsX)
		unitsY, okUnitsY := requiredInt(item, tagUnitsY)
		deltaX, okDeltaX := requiredFloat(item, tagDeltaX)
		deltaY, okDeltaY := requiredFloat(item, tagDeltaY)
		if !(okMinX && okMinY && okMaxX && okMaxY && okSpatial && okDataType &&
			okFlags && okUnitsX && okUnitsY && okDeltaX && okDeltaY) {
			return nil, fmt.Errorf("region %d is missing required calibration attributes", index)
		}
		if minX < 0 || minY < 0 || maxX < minX || maxY < minY {
			return nil, fmt.Errorf("region %d has invalid bounds (%d,%d)-(%d,%d)", index, minX, minY, maxX, maxY)
		}
		if !finite(deltaX) || !finite(deltaY) || deltaX == 0 || deltaY == 0 {
			return nil, fmt.Errorf("region %d has invalid physical deltas", index)
		}
		referenceX, _ := optionalInt(item, tagReferenceX)
		referenceY, _ := optionalInt(item, tagReferenceY)
		referenceValueX, _ := optionalFloat(item, tagReferenceValueX)
		referenceValueY, _ := optionalFloat(item, tagReferenceValueY)
		if !finite(referenceValueX) || !finite(referenceValueY) {
			return nil, fmt.Errorf("region %d has non-finite reference values", index)
		}
		out[index] = Region{
			Index: index,
			Bounds: image.Rectangle{
				Min: image.Pt(minX, minY),
				Max: image.Pt(maxX, maxY),
			},
			SpatialFormat:   SpatialFormat(spatial),
			DataType:        dataType,
			Flags:           flags,
			UnitsX:          PhysicalUnit(unitsX),
			UnitsY:          PhysicalUnit(unitsY),
			DeltaX:          deltaX,
			DeltaY:          deltaY,
			ReferencePixelX: referenceX,
			ReferencePixelY: referenceY,
			ReferenceValueX: referenceValueX,
			ReferenceValueY: referenceValueY,
		}
	}
	return out, nil
}

func requiredInt(obj *object.Object, tag core.Tag) (int, bool) {
	values, err := derivedio.LookupInts(obj, tag)
	if err != nil || len(values) != 1 || values[0] < math.MinInt || values[0] > math.MaxInt {
		return 0, false
	}
	return int(values[0]), true
}

func requiredUint16(obj *object.Object, tag core.Tag) (uint16, bool) {
	values, err := derivedio.LookupInts(obj, tag)
	if err != nil || len(values) != 1 || values[0] < 0 || values[0] > math.MaxUint16 {
		return 0, false
	}
	return uint16(values[0]), true
}

func requiredUint32(obj *object.Object, tag core.Tag) (uint32, bool) {
	values, err := derivedio.LookupInts(obj, tag)
	if err != nil || len(values) != 1 || values[0] < 0 || values[0] > math.MaxUint32 {
		return 0, false
	}
	return uint32(values[0]), true
}

func optionalInt(obj *object.Object, tag core.Tag) (int, bool) {
	if _, ok := obj.Get(tag); !ok {
		return 0, false
	}
	return requiredInt(obj, tag)
}

func requiredFloat(obj *object.Object, tag core.Tag) (float64, bool) {
	values, err := derivedio.LookupFloats(obj, tag)
	if err != nil || len(values) != 1 || !finite(values[0]) {
		return 0, false
	}
	return values[0], true
}

func optionalFloat(obj *object.Object, tag core.Tag) (float64, bool) {
	if _, ok := obj.Get(tag); !ok {
		return 0, false
	}
	return requiredFloat(obj, tag)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
