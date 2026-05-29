package microscopy

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

const maxAnnotationPoints = 10_000_000

var (
	tagAnnotationCoordinateType       = core.NewTag(0x006A, 0x0001)
	tagAnnotationGroupSequence        = core.NewTag(0x006A, 0x0002)
	tagAnnotationGroupUID             = core.NewTag(0x006A, 0x0003)
	tagAnnotationGroupLabel           = core.NewTag(0x006A, 0x0005)
	tagAnnotationGroupDescription     = core.NewTag(0x006A, 0x0006)
	tagAnnotationGroupGenerationType  = core.NewTag(0x006A, 0x0007)
	tagNumberOfAnnotations            = core.NewTag(0x006A, 0x000C)
	tagAppliesAllOpticalPaths         = core.NewTag(0x006A, 0x000D)
	tagReferencedOpticalPath          = core.NewTag(0x006A, 0x000E)
	tagAppliesAllZPlanes              = core.NewTag(0x006A, 0x000F)
	tagCommonZCoordinate              = core.NewTag(0x006A, 0x0010)
	tagAnnotationIndexList            = core.NewTag(0x006A, 0x0011)
	tagPointCoordinatesData           = core.NewTag(0x0066, 0x0016)
	tagDoublePointCoordinatesData     = core.NewTag(0x0066, 0x0022)
	tagLongPrimitivePointIndexList    = core.NewTag(0x0066, 0x0040)
	tagMeasurementsSequence           = core.NewTag(0x0066, 0x0121)
	tagFloatingPointValues            = core.NewTag(0x0066, 0x0125)
	tagMeasurementValuesSequence      = core.NewTag(0x0066, 0x0132)
	tagGraphicType                    = core.NewTag(0x0070, 0x0023)
	tagConceptNameCodeSequence        = core.NewTag(0x0040, 0xA043)
	tagMeasurementUnitsCodeSequence   = core.NewTag(0x0040, 0x08EA)
	tagCodeValue                      = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator         = core.NewTag(0x0008, 0x0102)
	tagCodeMeaning                    = core.NewTag(0x0008, 0x0104)
	tagAnnotationPropertyCategoryCode = core.NewTag(0x006A, 0x0009)
	tagAnnotationPropertyTypeCode     = core.NewTag(0x006A, 0x000A)
	tagPixelOriginInterpretation      = core.NewTag(0x0048, 0x0301)
)

type CodedConcept struct {
	CodeValue              string
	CodingSchemeDesignator string
	CodeMeaning            string
}

type AnnotationPoint struct {
	X float64
	Y float64
	Z float64
}

type Primitive struct {
	Index  int
	Points []AnnotationPoint
}

type AnnotationMeasurement struct {
	Concept CodedConcept
	Units   CodedConcept
	Values  []float64
	Indices []int
}

type AnnotationGroup struct {
	UID                    string
	Label                  string
	Description            string
	GenerationType         string
	CoordinateType         string
	PixelOrigin            string
	GraphicType            string
	Category               CodedConcept
	Type                   CodedConcept
	AppliesAllOpticalPaths bool
	OpticalPaths           []string
	AppliesAllZPlanes      bool
	CommonZCoordinates     []float64
	Primitives             []Primitive
	Measurements           []AnnotationMeasurement
}

// ReadAnnotations parses Microscopy Bulk Simple Annotations while retaining
// coordinate semantics, optical-path/Z applicability, coded properties, and
// per-annotation measurements.
func ReadAnnotations(file *object.File) ([]AnnotationGroup, error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("dicom/microscopy: nil annotation dataset")
	}
	root := file.Dataset
	if sopClass := derivedio.CleanUID(root, tagSOPClassUID); sopClass != MicroscopyBulkSimpleAnnotationsStorage {
		return nil, fmt.Errorf("dicom/microscopy: unsupported annotation SOP Class %q", sopClass)
	}
	coordinateType := strings.ToUpper(derivedio.CleanString(root, tagAnnotationCoordinateType))
	if coordinateType != "2D" && coordinateType != "3D" {
		return nil, fmt.Errorf("dicom/microscopy: invalid annotation coordinate type %q", coordinateType)
	}
	pixelOrigin := strings.ToUpper(derivedio.CleanString(root, tagPixelOriginInterpretation))
	if coordinateType == "2D" && pixelOrigin != "FRAME" && pixelOrigin != "VOLUME" {
		return nil, fmt.Errorf("dicom/microscopy: invalid pixel origin interpretation %q", pixelOrigin)
	}
	items := derivedio.Sequence(root, tagAnnotationGroupSequence)
	out := make([]AnnotationGroup, 0, len(items))
	for index, item := range items {
		group, err := readAnnotationGroup(item, coordinateType, pixelOrigin)
		if err != nil {
			return nil, fmt.Errorf("dicom/microscopy: annotation group %d: %w", index+1, err)
		}
		out = append(out, group)
	}
	return out, nil
}

func readAnnotationGroup(item *object.Object, coordinateType, pixelOrigin string) (AnnotationGroup, error) {
	if item == nil {
		return AnnotationGroup{}, fmt.Errorf("nil group")
	}
	number := derivedio.Int(item, tagNumberOfAnnotations)
	if number <= 0 {
		return AnnotationGroup{}, fmt.Errorf("invalid Number of Annotations %d", number)
	}
	graphicType := strings.ToUpper(derivedio.CleanString(item, tagGraphicType))
	dimensions := 2
	if coordinateType == "3D" && len(derivedio.Floats(item, tagCommonZCoordinate)) == 0 {
		dimensions = 3
	}
	coordinates, err := annotationCoordinates(item, dimensions)
	if err != nil {
		return AnnotationGroup{}, err
	}
	primitives, err := annotationPrimitives(item, graphicType, number, coordinates)
	if err != nil {
		return AnnotationGroup{}, err
	}
	group := AnnotationGroup{
		UID:            derivedio.CleanUID(item, tagAnnotationGroupUID),
		Label:          derivedio.CleanString(item, tagAnnotationGroupLabel),
		Description:    derivedio.CleanString(item, tagAnnotationGroupDescription),
		GenerationType: strings.ToUpper(derivedio.CleanString(item, tagAnnotationGroupGenerationType)),
		CoordinateType: coordinateType, PixelOrigin: pixelOrigin, GraphicType: graphicType,
		Category:               firstCode(item, tagAnnotationPropertyCategoryCode),
		Type:                   firstCode(item, tagAnnotationPropertyTypeCode),
		AppliesAllOpticalPaths: strings.EqualFold(derivedio.CleanString(item, tagAppliesAllOpticalPaths), "YES"),
		OpticalPaths:           stringsAt(item, tagReferencedOpticalPath),
		AppliesAllZPlanes:      strings.EqualFold(derivedio.CleanString(item, tagAppliesAllZPlanes), "YES"),
		CommonZCoordinates:     append([]float64(nil), derivedio.Floats(item, tagCommonZCoordinate)...),
		Primitives:             primitives,
	}
	for _, measurementItem := range derivedio.Sequence(item, tagMeasurementsSequence) {
		measurement, err := readAnnotationMeasurement(measurementItem, number)
		if err != nil {
			return AnnotationGroup{}, err
		}
		group.Measurements = append(group.Measurements, measurement)
	}
	return group, nil
}

func annotationCoordinates(item *object.Object, dimensions int) ([]AnnotationPoint, error) {
	values, err := binaryFloats(item, tagDoublePointCoordinatesData)
	if err != nil {
		values, err = binaryFloats(item, tagPointCoordinatesData)
	}
	if err != nil {
		return nil, fmt.Errorf("missing point coordinates")
	}
	if dimensions != 2 && dimensions != 3 {
		return nil, fmt.Errorf("unsupported coordinate dimension %d", dimensions)
	}
	if len(values)%dimensions != 0 || len(values)/dimensions > maxAnnotationPoints {
		return nil, fmt.Errorf("invalid coordinate count %d for %dD", len(values), dimensions)
	}
	out := make([]AnnotationPoint, len(values)/dimensions)
	for index := range out {
		out[index].X = values[index*dimensions]
		out[index].Y = values[index*dimensions+1]
		if dimensions == 3 {
			out[index].Z = values[index*dimensions+2]
		}
	}
	return out, nil
}

func annotationPrimitives(item *object.Object, graphicType string, number int, points []AnnotationPoint) ([]Primitive, error) {
	starts, _ := binaryUint32s(item, tagLongPrimitivePointIndexList)
	if len(starts) == 0 {
		switch graphicType {
		case "POINT":
			if len(points) != number {
				return nil, fmt.Errorf("%d POINT coordinates for %d annotations", len(points), number)
			}
			out := make([]Primitive, number)
			for index := range out {
				out[index] = Primitive{Index: index, Points: []AnnotationPoint{points[index]}}
			}
			return out, nil
		case "RECTANGLE", "ELLIPSE":
			if len(points) != number*4 {
				return nil, fmt.Errorf("%s requires four points per annotation", graphicType)
			}
			out := make([]Primitive, number)
			for index := range out {
				out[index] = Primitive{Index: index, Points: append([]AnnotationPoint(nil), points[index*4:(index+1)*4]...)}
			}
			return out, nil
		default:
			if number == 1 && len(points) > 0 {
				return []Primitive{{Index: 0, Points: append([]AnnotationPoint(nil), points...)}}, nil
			}
			return nil, fmt.Errorf("%s annotations require Long Primitive Point Index List", graphicType)
		}
	}
	if len(starts) != number {
		return nil, fmt.Errorf("%d primitive indices for %d annotations", len(starts), number)
	}
	out := make([]Primitive, number)
	for index, oneBased := range starts {
		if oneBased == 0 {
			return nil, fmt.Errorf("primitive index is not one-based")
		}
		start := int(oneBased - 1)
		end := len(points)
		if index+1 < len(starts) {
			end = int(starts[index+1] - 1)
		}
		if start < 0 || end <= start || end > len(points) {
			return nil, fmt.Errorf("invalid primitive point range [%d,%d)", start, end)
		}
		out[index] = Primitive{Index: index, Points: append([]AnnotationPoint(nil), points[start:end]...)}
	}
	return out, nil
}

func readAnnotationMeasurement(item *object.Object, annotationCount int) (AnnotationMeasurement, error) {
	out := AnnotationMeasurement{
		Concept: firstCode(item, tagConceptNameCodeSequence),
		Units:   firstCode(item, tagMeasurementUnitsCodeSequence),
	}
	for _, valuesItem := range derivedio.Sequence(item, tagMeasurementValuesSequence) {
		values, err := binaryFloats(valuesItem, tagFloatingPointValues)
		if err != nil {
			return AnnotationMeasurement{}, fmt.Errorf("measurement values: %w", err)
		}
		indices, _ := binaryUint32s(valuesItem, tagAnnotationIndexList)
		for _, index := range indices {
			if index == 0 || int(index) > annotationCount {
				return AnnotationMeasurement{}, fmt.Errorf("measurement annotation index %d out of range", index)
			}
			out.Indices = append(out.Indices, int(index-1))
		}
		if len(indices) > 0 && len(indices) != len(values) {
			return AnnotationMeasurement{}, fmt.Errorf("measurement values and indices differ")
		}
		out.Values = append(out.Values, values...)
	}
	if len(out.Indices) == 0 && len(out.Values) != annotationCount {
		return AnnotationMeasurement{}, fmt.Errorf("%d measurement values for %d annotations", len(out.Values), annotationCount)
	}
	return out, nil
}

// SlidePrimitives converts 2D Total Pixel Matrix annotation coordinates to
// calibrated slide coordinates. FRAME-relative coordinates are rejected
// because they require an explicit referenced-frame transform.
func (g AnnotationGroup) SlidePrimitives(level Level) ([]Primitive, error) {
	if g.CoordinateType == "3D" {
		return clonePrimitives(g.Primitives), nil
	}
	if g.PixelOrigin != "VOLUME" {
		return nil, fmt.Errorf("dicom/microscopy: FRAME-relative annotations require a frame transform")
	}
	out := clonePrimitives(g.Primitives)
	for primitiveIndex := range out {
		for pointIndex := range out[primitiveIndex].Points {
			point := out[primitiveIndex].Points[pointIndex]
			slide, err := level.SlideCoordinate(
				imagePoint(point.X, point.Y),
				commonZ(g.CommonZCoordinates),
			)
			if err != nil {
				return nil, err
			}
			out[primitiveIndex].Points[pointIndex] = AnnotationPoint{X: slide.X, Y: slide.Y, Z: slide.Z}
		}
	}
	return out, nil
}

func binaryFloats(item *object.Object, tag core.Tag) ([]float64, error) {
	if item == nil {
		return nil, fmt.Errorf("nil object")
	}
	element, ok := item.Get(tag)
	if !ok {
		return nil, fmt.Errorf("missing %s", tag)
	}
	raw, ok := element.RawBytes()
	if !ok {
		return nil, fmt.Errorf("%s has no binary value", tag)
	}
	order := item.ValueByteOrder()
	switch element.VR() {
	case core.VROF, core.VRFL:
		if len(raw)%4 != 0 {
			return nil, fmt.Errorf("%s has invalid OF length", tag)
		}
		out := make([]float64, len(raw)/4)
		for index := range out {
			out[index] = float64(math.Float32frombits(order.Uint32(raw[index*4:])))
		}
		return out, nil
	case core.VROD, core.VRFD:
		if len(raw)%8 != 0 {
			return nil, fmt.Errorf("%s has invalid OD length", tag)
		}
		out := make([]float64, len(raw)/8)
		for index := range out {
			out[index] = math.Float64frombits(order.Uint64(raw[index*8:]))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s has unsupported VR %s", tag, element.VR())
	}
}

func binaryUint32s(item *object.Object, tag core.Tag) ([]uint32, error) {
	if item == nil {
		return nil, fmt.Errorf("nil object")
	}
	element, ok := item.Get(tag)
	if !ok {
		return nil, fmt.Errorf("missing %s", tag)
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw)%4 != 0 {
		return nil, fmt.Errorf("%s has invalid binary value", tag)
	}
	order := item.ValueByteOrder()
	if order == nil {
		order = binary.LittleEndian
	}
	out := make([]uint32, len(raw)/4)
	for index := range out {
		out[index] = order.Uint32(raw[index*4:])
	}
	return out, nil
}

func firstCode(item *object.Object, tag core.Tag) CodedConcept {
	items := derivedio.Sequence(item, tag)
	if len(items) == 0 {
		return CodedConcept{}
	}
	return CodedConcept{
		CodeValue:              derivedio.CleanString(items[0], tagCodeValue),
		CodingSchemeDesignator: derivedio.CleanString(items[0], tagCodingSchemeDesignator),
		CodeMeaning:            derivedio.CleanString(items[0], tagCodeMeaning),
	}
}

func stringsAt(item *object.Object, tag core.Tag) []string {
	values, err := item.LookupStrings(tag)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func clonePrimitives(values []Primitive) []Primitive {
	out := make([]Primitive, len(values))
	for index, value := range values {
		out[index] = value
		out[index].Points = append([]AnnotationPoint(nil), value.Points...)
	}
	return out
}

func imagePoint(x, y float64) image.Point {
	return image.Pt(int(math.Round(x)), int(math.Round(y)))
}

func commonZ(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}
