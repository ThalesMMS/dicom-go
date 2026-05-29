// Package parametricmap parses and lazily decodes DICOM Parametric Map
// instances. Quantitative values retain their Real World Value Mapping,
// quantity, and units and are never routed through ordinary image VOI.
package parametricmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/roi"
)

const ParametricMapStorage = "1.2.840.10008.5.1.4.1.1.30"

var (
	ErrUnsupportedSOPClass   = errors.New("dicom/parametricmap: unsupported SOP class")
	ErrInvalidObject         = errors.New("dicom/parametricmap: invalid object")
	ErrMissingMapping        = errors.New("dicom/parametricmap: missing real world value mapping")
	ErrUnsupportedDimensions = errors.New("dicom/parametricmap: unsupported dimensions")
	ErrGeometryMismatch      = errors.New("dicom/parametricmap: geometry mismatch")
	ErrNonFinite             = errors.New("dicom/parametricmap: non-finite quantitative value")
	ErrMemoryLimit           = errors.New("dicom/parametricmap: memory limit exceeded")
	ErrPayloadUnavailable    = errors.New("dicom/parametricmap: pixel payload was not retained")
)

var (
	tagFloatPixelData           = core.NewTag(0x7FE0, 0x0008)
	tagDoubleFloatPixelData     = core.NewTag(0x7FE0, 0x0009)
	tagSharedFunctionalGroups   = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups = core.NewTag(0x5200, 0x9230)
	tagRealWorldValueMappingSeq = core.NewTag(0x0040, 0x9096)
	tagRWVFirstMapped           = core.NewTag(0x0040, 0x9216)
	tagRWVLastMapped            = core.NewTag(0x0040, 0x9211)
	tagDoubleRWVFirstMapped     = core.NewTag(0x0040, 0x9214)
	tagDoubleRWVLastMapped      = core.NewTag(0x0040, 0x9213)
	tagRWVLUTData               = core.NewTag(0x0040, 0x9212)
	tagRWVIntercept             = core.NewTag(0x0040, 0x9224)
	tagRWVSlope                 = core.NewTag(0x0040, 0x9225)
	tagMeasurementUnitsCodeSeq  = core.NewTag(0x0040, 0x08EA)
	tagQuantityDefinitionSeq    = core.NewTag(0x0040, 0x9220)
	tagConceptNameCodeSeq       = core.NewTag(0x0040, 0xA043)
	tagConceptCodeSeq           = core.NewTag(0x0040, 0xA168)
	tagCodeValue                = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator   = core.NewTag(0x0008, 0x0102)
	tagCodeMeaning              = core.NewTag(0x0008, 0x0104)
	tagDimensionIndexSeq        = core.NewTag(0x0020, 0x9222)
	tagDimensionIndexPointer    = core.NewTag(0x0020, 0x9165)
	tagDimensionIndexValues     = core.NewTag(0x0020, 0x9157)
	tagFrameContentSeq          = core.NewTag(0x0020, 0x9111)
	tagInStackPositionNumber    = core.NewTag(0x0020, 0x9057)
	tagImagePositionPatient     = core.NewTag(0x0020, 0x0032)
	tagReferencedSOPClassUID    = core.NewTag(0x0008, 0x1150)
	tagReferencedSOPInstanceUID = core.NewTag(0x0008, 0x1155)
)

type PayloadKind string

const (
	PayloadFloat32 PayloadKind = "float32"
	PayloadFloat64 PayloadKind = "float64"
	PayloadInteger PayloadKind = "integer"
)

// Code is a coded DICOM concept.
type Code struct {
	Value   string
	Scheme  string
	Meaning string
}

func (c Code) String() string {
	if strings.TrimSpace(c.Meaning) != "" {
		return strings.TrimSpace(c.Meaning)
	}
	if c.Value == "" {
		return ""
	}
	if c.Scheme == "" {
		return c.Value
	}
	return c.Scheme + ":" + c.Value
}

type Reference struct {
	SOPClassUID    string
	SOPInstanceUID string
}

// Mapping converts a stored sample to its calibrated real-world value.
type Mapping struct {
	FirstValue float64
	LastValue  float64
	Slope      float64
	Intercept  float64
	LUT        []float64
	Units      Code
	Quantity   Code
}

func (m Mapping) Apply(stored float64) (float64, error) {
	var value float64
	if len(m.LUT) > 0 {
		index := int(math.Round(stored - m.FirstValue))
		if index < 0 || index >= len(m.LUT) {
			return 0, fmt.Errorf("%w: stored value %.8g outside mapping [%.8g, %.8g]", ErrMissingMapping, stored, m.FirstValue, m.LastValue)
		}
		value = m.LUT[index]
	} else {
		value = stored*m.Slope + m.Intercept
	}
	if !finite(value) {
		return 0, fmt.Errorf("%w: mapped value %.8g", ErrNonFinite, value)
	}
	return value, nil
}

type Frame struct {
	Geometry             render.SliceGeometry
	DimensionIndexValues []int
	Mapping              Mapping
}

// Map retains encoded frames and decodes calibrated values only when requested.
// The small LRU cache is bounded and can be evicted by an application memory
// budget through SetCacheLimitBytes/EvictBytes.
type Map struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	Rows                int
	Columns             int
	NumberOfFrames      int
	Payload             PayloadKind
	Frames              []Frame
	References          []Reference
	Units               Code
	Quantity            Code

	order           binary.ByteOrder
	floatRaw        []byte
	native          *pixeldata.NativeFrames
	cacheMu         sync.Mutex
	cache           map[int][]float64
	cacheOrder      []int
	cacheBytes      int64
	cacheLimit      int64
	payloadRetained bool
}

// Read parses metadata and encoded frame boundaries without decoding the
// quantitative payload.
func Read(file *object.File) (*Map, error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	return readDataset(file.Dataset, true)
}

func ReadDataset(obj *object.Object) (*Map, error) {
	return readDataset(obj, true)
}

// ReadMetadata parses geometry, dimensions, mappings, units, and encoded
// payload boundaries without requiring the Pixel Data value to be retained.
// FrameValues returns ErrPayloadUnavailable on the resulting Map. This lets
// two-pass consumers plan large exports with DeferPixelData enabled.
func ReadMetadata(file *object.File) (*Map, error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	return readDataset(file.Dataset, false)
}

func readDataset(obj *object.Object, retainPayload bool) (*Map, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	if sop := derivedio.CleanUID(obj, derivedio.TagSOPClassUID); sop != ParametricMapStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sop)
	}
	rows, columns := derivedio.Int(obj, derivedio.TagRows), derivedio.Int(obj, derivedio.TagColumns)
	frames := derivedio.Int(obj, derivedio.TagNumberOfFrames)
	if frames == 0 {
		frames = 1
	}
	if rows <= 0 || columns <= 0 || frames <= 0 {
		return nil, fmt.Errorf("%w: invalid dimensions %dx%dx%d", ErrInvalidObject, columns, rows, frames)
	}
	voxels, ok := checkedProduct(rows, columns, frames)
	if !ok || voxels > 64*1024*1024 {
		return nil, fmt.Errorf("%w: %dx%dx%d samples", ErrMemoryLimit, columns, rows, frames)
	}
	out := &Map{
		SOPClassUID:         ParametricMapStorage,
		SOPInstanceUID:      derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:    derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:   derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		FrameOfReferenceUID: derivedio.CleanUID(obj, derivedio.TagFrameOfReferenceUID),
		Rows:                rows,
		Columns:             columns,
		NumberOfFrames:      frames,
		order:               obj.ValueByteOrder(),
		cache:               make(map[int][]float64),
		cacheLimit:          32 << 20,
		payloadRetained:     retainPayload,
	}
	if out.SOPInstanceUID == "" || out.StudyInstanceUID == "" || out.SeriesInstanceUID == "" || out.FrameOfReferenceUID == "" {
		return nil, fmt.Errorf("%w: missing identity or FrameOfReferenceUID", ErrInvalidObject)
	}

	if element, found := obj.Get(tagFloatPixelData); found {
		raw, rawOK := element.RawBytes()
		length := int(element.Length())
		if (retainPayload && !rawOK) || length != voxels*4 {
			return nil, fmt.Errorf("%w: Float Pixel Data length %d, want %d", ErrInvalidObject, length, voxels*4)
		}
		out.Payload = PayloadFloat32
		if retainPayload {
			out.floatRaw = raw
		}
	} else if element, found := obj.Get(tagDoubleFloatPixelData); found {
		raw, rawOK := element.RawBytes()
		length := int(element.Length())
		if (retainPayload && !rawOK) || length != voxels*8 {
			return nil, fmt.Errorf("%w: Double Float Pixel Data length %d, want %d", ErrInvalidObject, length, voxels*8)
		}
		out.Payload = PayloadFloat64
		if retainPayload {
			out.floatRaw = raw
		}
	} else {
		metadata, err := pixeldata.ExtractMetadata(obj)
		if err != nil {
			return nil, fmt.Errorf("%w: integer Pixel Data metadata: %v", ErrInvalidObject, err)
		}
		if metadata.SamplesPerPixel != 1 || metadata.PixelRepresentation > 1 ||
			(metadata.BitsAllocated != 8 && metadata.BitsAllocated != 16 && metadata.BitsAllocated != 32) {
			return nil, fmt.Errorf("%w: unsupported integer pixel metadata %+v", ErrInvalidObject, metadata)
		}
		if metadata.BitsStored == 0 || metadata.BitsStored > metadata.BitsAllocated ||
			metadata.HighBit >= metadata.BitsAllocated ||
			int(metadata.HighBit)+1 < int(metadata.BitsStored) {
			return nil, fmt.Errorf("%w: unsupported integer bit layout %+v", ErrInvalidObject, metadata)
		}
		element, found := obj.Get(core.TagPixelData)
		if !found {
			return nil, fmt.Errorf("%w: integer Pixel Data is missing", ErrInvalidObject)
		}
		frameBytes := rows * columns * int(metadata.BitsAllocated/8)
		wantBytes := frameBytes * frames
		length := int(element.Length())
		if length != wantBytes && !(wantBytes%2 == 1 && length == wantBytes+1) {
			return nil, fmt.Errorf("%w: integer Pixel Data length %d, want %d", ErrInvalidObject, length, wantBytes)
		}
		out.Payload = PayloadInteger
		if retainPayload {
			native, nativeErr := pixeldata.ExtractNativeFramesView(obj)
			if nativeErr != nil {
				return nil, fmt.Errorf("%w: integer Pixel Data: %v", ErrInvalidObject, nativeErr)
			}
			if len(native.Data) != frames {
				return nil, fmt.Errorf("%w: %d encoded frames for %d frames", ErrInvalidObject, len(native.Data), frames)
			}
			for index, frame := range native.Data {
				if len(frame) != frameBytes {
					return nil, fmt.Errorf("%w: frame %d has %d bytes, want %d", ErrInvalidObject, index+1, len(frame), frameBytes)
				}
			}
			out.native = native
		}
	}

	topMapping, topOK := mappingFromContainer(obj)
	shared := firstItem(obj, tagSharedFunctionalGroups)
	sharedMapping, sharedOK := mappingFromContainer(shared)
	perFrame := sequence(obj, tagPerFrameFunctionalGroups)
	if len(perFrame) != 0 && len(perFrame) != frames {
		return nil, fmt.Errorf("%w: %d per-frame groups for %d frames", ErrInvalidObject, len(perFrame), frames)
	}
	out.Frames = make([]Frame, frames)
	for index := 0; index < frames; index++ {
		mapping, mappingOK := topMapping, topOK
		if sharedOK {
			mapping, mappingOK = sharedMapping, true
		}
		if index < len(perFrame) {
			if perMapping, ok := mappingFromContainer(perFrame[index]); ok {
				mapping, mappingOK = perMapping, true
			}
		}
		if !mappingOK || mapping.Units.String() == "" {
			return nil, fmt.Errorf("%w: frame %d", ErrMissingMapping, index+1)
		}
		geometry, err := frameGeometry(obj.FrameGeometryAt(index), rows, columns)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", index+1, err)
		}
		out.Frames[index] = Frame{Geometry: geometry, Mapping: mapping}
	}
	if err := readDimensions(obj, perFrame, out.Frames); err != nil {
		return nil, err
	}
	out.Units = out.Frames[0].Mapping.Units
	out.Quantity = out.Frames[0].Mapping.Quantity
	for index := 1; index < len(out.Frames); index++ {
		if out.Frames[index].Mapping.Units != out.Units || out.Frames[index].Mapping.Quantity != out.Quantity {
			return nil, fmt.Errorf("%w: units or quantity vary across frames", ErrUnsupportedDimensions)
		}
		if out.Frames[index].Geometry.RowDir.Dot(out.Frames[0].Geometry.RowDir) <
			render.DefaultGeometryTolerances().OrientationCos ||
			out.Frames[index].Geometry.ColDir.Dot(out.Frames[0].Geometry.ColDir) <
				render.DefaultGeometryTolerances().OrientationCos {
			return nil, fmt.Errorf("%w: frame orientation varies", ErrUnsupportedDimensions)
		}
	}
	out.References = readReferences(obj)
	return out, nil
}

func mappingFromContainer(container *object.Object) (Mapping, bool) {
	if container == nil {
		return Mapping{}, false
	}
	items := sequence(container, tagRealWorldValueMappingSeq)
	if len(items) == 0 {
		return Mapping{}, false
	}
	item := items[0]
	mapping := Mapping{
		Slope:     firstFloat(item, tagRWVSlope, 1),
		Intercept: firstFloat(item, tagRWVIntercept, 0),
		Units:     readCode(firstItem(item, tagMeasurementUnitsCodeSeq)),
		Quantity:  readQuantity(item),
	}
	if values := derivedio.Floats(item, tagDoubleRWVFirstMapped); len(values) > 0 {
		mapping.FirstValue = values[0]
	} else if values := derivedio.Ints(item, tagRWVFirstMapped); len(values) > 0 {
		mapping.FirstValue = float64(values[0])
	}
	if values := derivedio.Floats(item, tagDoubleRWVLastMapped); len(values) > 0 {
		mapping.LastValue = values[0]
	} else if values := derivedio.Ints(item, tagRWVLastMapped); len(values) > 0 {
		mapping.LastValue = float64(values[0])
	}
	mapping.LUT = derivedio.Floats(item, tagRWVLUTData)
	if len(mapping.LUT) > 0 {
		if mapping.LastValue < mapping.FirstValue || int(math.Round(mapping.LastValue-mapping.FirstValue))+1 != len(mapping.LUT) {
			return Mapping{}, false
		}
	} else if !finite(mapping.Slope) || !finite(mapping.Intercept) {
		return Mapping{}, false
	}
	return mapping, true
}

func frameGeometry(g object.FrameGeometry, rows, columns int) (render.SliceGeometry, error) {
	if len(g.ImagePositionPatient) != 3 || len(g.ImageOrientationPatient) != 6 || len(g.PixelSpacing) != 2 ||
		!positiveFinite(g.PixelSpacing[0]) || !positiveFinite(g.PixelSpacing[1]) {
		return render.SliceGeometry{}, fmt.Errorf("%w: incomplete patient geometry", ErrGeometryMismatch)
	}
	rowRaw := render.Vec3{X: g.ImageOrientationPatient[0], Y: g.ImageOrientationPatient[1], Z: g.ImageOrientationPatient[2]}
	colRaw := render.Vec3{X: g.ImageOrientationPatient[3], Y: g.ImageOrientationPatient[4], Z: g.ImageOrientationPatient[5]}
	origin := render.Vec3{X: g.ImagePositionPatient[0], Y: g.ImagePositionPatient[1], Z: g.ImagePositionPatient[2]}
	if !finiteVec(origin) || !finiteVec(rowRaw) || !finiteVec(colRaw) ||
		math.Abs(rowRaw.Length()-1) > 1e-4 || math.Abs(colRaw.Length()-1) > 1e-4 || math.Abs(rowRaw.Dot(colRaw)) > 1e-4 {
		return render.SliceGeometry{}, fmt.Errorf("%w: invalid orientation", ErrGeometryMismatch)
	}
	normal := rowRaw.Cross(colRaw).Normalize()
	if normal == (render.Vec3{}) {
		return render.SliceGeometry{}, fmt.Errorf("%w: degenerate orientation", ErrGeometryMismatch)
	}
	return render.SliceGeometry{
		Origin: origin,
		RowDir: rowRaw, ColDir: colRaw, Normal: normal,
		RowSpacing: g.PixelSpacing[0], ColSpacing: g.PixelSpacing[1],
		Rows: rows, Columns: columns,
	}, nil
}

// FrameValues returns a defensive copy of calibrated values for one frame.
func (m *Map) FrameValues(index int) ([]float64, error) {
	if m == nil || index < 0 || index >= m.NumberOfFrames {
		return nil, fmt.Errorf("%w: frame %d", ErrInvalidObject, index)
	}
	if !m.payloadRetained {
		return nil, ErrPayloadUnavailable
	}
	m.cacheMu.Lock()
	if values, ok := m.cache[index]; ok {
		m.touchLocked(index)
		out := append([]float64(nil), values...)
		m.cacheMu.Unlock()
		return out, nil
	}
	m.cacheMu.Unlock()
	values, err := m.decodeFrame(index)
	if err != nil {
		return nil, err
	}
	m.cacheMu.Lock()
	if existing, ok := m.cache[index]; ok {
		values = existing
	} else {
		m.cache[index] = values
		m.cacheOrder = append(m.cacheOrder, index)
		m.cacheBytes += int64(len(values) * 8)
		m.trimLocked()
	}
	out := append([]float64(nil), values...)
	m.cacheMu.Unlock()
	return out, nil
}

// FrameValuesInto decodes one calibrated frame into destination without
// populating the Map cache or allocating a defensive copy. It is intended for
// bounded streaming consumers that own their per-frame buffer.
func (m *Map) FrameValuesInto(index int, destination []float64) error {
	if m == nil || index < 0 || index >= m.NumberOfFrames {
		return fmt.Errorf("%w: frame %d", ErrInvalidObject, index)
	}
	if !m.payloadRetained {
		return ErrPayloadUnavailable
	}
	count := m.Rows * m.Columns
	if len(destination) < count {
		return fmt.Errorf("%w: destination has %d values, want %d", ErrInvalidObject, len(destination), count)
	}
	destination = destination[:count]
	mapping := m.Frames[index].Mapping
	for pixel := 0; pixel < count; pixel++ {
		stored, err := m.storedValue(index, pixel)
		if err != nil {
			return err
		}
		if !finite(stored) {
			return fmt.Errorf("%w: frame %d pixel %d", ErrNonFinite, index+1, pixel)
		}
		value, err := mapping.Apply(stored)
		if err != nil {
			return fmt.Errorf("frame %d pixel %d: %w", index+1, pixel, err)
		}
		destination[pixel] = value
	}
	return nil
}

func (m *Map) decodeFrame(index int) ([]float64, error) {
	count := m.Rows * m.Columns
	out := make([]float64, count)
	if err := m.FrameValuesInto(index, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Map) storedValue(frame, pixel int) (float64, error) {
	count := m.Rows * m.Columns
	switch m.Payload {
	case PayloadFloat32:
		offset := (frame*count + pixel) * 4
		return float64(math.Float32frombits(m.order.Uint32(m.floatRaw[offset:]))), nil
	case PayloadFloat64:
		offset := (frame*count + pixel) * 8
		return math.Float64frombits(m.order.Uint64(m.floatRaw[offset:])), nil
	case PayloadInteger:
		meta := m.native.Metadata
		width := int(meta.BitsAllocated / 8)
		raw := m.native.Data[frame][pixel*width:]
		var unsigned uint64
		switch width {
		case 1:
			unsigned = uint64(raw[0])
		case 2:
			unsigned = uint64(m.order.Uint16(raw))
		case 4:
			unsigned = uint64(m.order.Uint32(raw))
		default:
			return 0, fmt.Errorf("%w: integer width %d", ErrInvalidObject, width)
		}
		shift := meta.HighBit + 1 - meta.BitsStored
		unsigned >>= shift
		mask := uint64(1)<<meta.BitsStored - 1
		unsigned &= mask
		if meta.PixelRepresentation == 1 && unsigned&(uint64(1)<<(meta.BitsStored-1)) != 0 {
			return float64(int64(unsigned | ^mask)), nil
		}
		return float64(unsigned), nil
	default:
		return 0, ErrInvalidObject
	}
}

// SamplePatient returns a calibrated bilinear sample from the nearest matching
// frame. It never extrapolates beyond a frame.
func (m *Map) SamplePatient(point render.Vec3) (float64, bool, error) {
	if m == nil || !finiteVec(point) {
		return 0, false, ErrInvalidObject
	}
	best, distance := -1, math.Inf(1)
	for index, frame := range m.Frames {
		d := math.Abs(point.Sub(frame.Geometry.Origin).Dot(frame.Geometry.Normal))
		if d < distance {
			best, distance = index, d
		}
	}
	tolerance := 0.5
	if len(m.Frames) > 1 {
		positions := make([]float64, len(m.Frames))
		for index, frame := range m.Frames {
			positions[index] = frame.Geometry.PositionAlong(frame.Geometry.Normal)
		}
		sort.Float64s(positions)
		minSpacing := math.Inf(1)
		for index := 1; index < len(positions); index++ {
			if spacing := positions[index] - positions[index-1]; spacing > 0 && spacing < minSpacing {
				minSpacing = spacing
			}
		}
		if finite(minSpacing) {
			tolerance = minSpacing/2 + 1e-5
		}
	}
	if best < 0 || distance > tolerance {
		return 0, false, nil
	}
	g := m.Frames[best].Geometry
	delta := point.Sub(g.Origin)
	column := delta.Dot(g.RowDir) / g.ColSpacing
	row := delta.Dot(g.ColDir) / g.RowSpacing
	if column < 0 || row < 0 || column > float64(m.Columns-1) || row > float64(m.Rows-1) {
		return 0, false, nil
	}
	values, err := m.FrameValues(best)
	if err != nil {
		return 0, false, err
	}
	x0, y0 := int(math.Floor(column)), int(math.Floor(row))
	x1, y1 := min(x0+1, m.Columns-1), min(y0+1, m.Rows-1)
	fx, fy := column-float64(x0), row-float64(y0)
	at := func(x, y int) float64 { return values[y*m.Columns+x] }
	top := at(x0, y0) + (at(x1, y0)-at(x0, y0))*fx
	bottom := at(x0, y1) + (at(x1, y1)-at(x0, y1))*fx
	return top + (bottom-top)*fy, true, nil
}

type Statistics struct {
	Count  int
	Min    float64
	Max    float64
	Mean   float64
	StdDev float64
}

// Resample aligns the nearest compatible map frame to a target slice geometry
// and returns calibrated values plus a validity mask. Unsupported orientation
// or non-overlapping planes fail explicitly.
func (m *Map) Resample(target render.SliceGeometry) ([]float64, []bool, error) {
	if m == nil || target.Rows <= 0 || target.Columns <= 0 ||
		!finiteVec(target.Origin) || !finiteVec(target.RowDir) || !finiteVec(target.ColDir) ||
		!positiveFinite(target.RowSpacing) || !positiveFinite(target.ColSpacing) ||
		math.Abs(target.RowDir.Length()-1) > 1e-4 || math.Abs(target.ColDir.Length()-1) > 1e-4 ||
		math.Abs(target.RowDir.Dot(target.ColDir)) > 1e-4 {
		return nil, nil, fmt.Errorf("%w: invalid target geometry", ErrGeometryMismatch)
	}
	best, distance := -1, math.Inf(1)
	for index, frame := range m.Frames {
		source := frame.Geometry
		if math.Abs(source.RowDir.Dot(target.RowDir)) < render.DefaultGeometryTolerances().OrientationCos ||
			math.Abs(source.ColDir.Dot(target.ColDir)) < render.DefaultGeometryTolerances().OrientationCos {
			continue
		}
		d := math.Abs(target.Origin.Sub(source.Origin).Dot(source.Normal))
		if d < distance {
			best, distance = index, d
		}
	}
	tolerance := 0.5
	if len(m.Frames) > 1 {
		positions := make([]float64, len(m.Frames))
		for index, frame := range m.Frames {
			positions[index] = frame.Geometry.PositionAlong(frame.Geometry.Normal)
		}
		sort.Float64s(positions)
		for index := 1; index < len(positions); index++ {
			if spacing := positions[index] - positions[index-1]; spacing > 0 && (tolerance == 0.5 || spacing/2 < tolerance) {
				tolerance = spacing/2 + 1e-5
			}
		}
	}
	if best < 0 || distance > tolerance {
		return nil, nil, fmt.Errorf("%w: target plane does not overlap the map", ErrGeometryMismatch)
	}
	source := m.Frames[best].Geometry
	sourceValues, err := m.FrameValues(best)
	if err != nil {
		return nil, nil, err
	}
	count, ok := checkedProduct(target.Rows, target.Columns)
	if !ok || count > 64*1024*1024 {
		return nil, nil, ErrMemoryLimit
	}
	values := make([]float64, count)
	valid := make([]bool, count)
	for row := 0; row < target.Rows; row++ {
		for column := 0; column < target.Columns; column++ {
			point := target.Origin.
				Add(target.RowDir.Scale(float64(column) * target.ColSpacing)).
				Add(target.ColDir.Scale(float64(row) * target.RowSpacing))
			delta := point.Sub(source.Origin)
			x := delta.Dot(source.RowDir) / source.ColSpacing
			y := delta.Dot(source.ColDir) / source.RowSpacing
			if x < 0 || y < 0 || x > float64(m.Columns-1) || y > float64(m.Rows-1) {
				continue
			}
			x0, y0 := int(math.Floor(x)), int(math.Floor(y))
			x1, y1 := min(x0+1, m.Columns-1), min(y0+1, m.Rows-1)
			fx, fy := x-float64(x0), y-float64(y0)
			at := func(px, py int) float64 { return sourceValues[py*m.Columns+px] }
			top := at(x0, y0) + (at(x1, y0)-at(x0, y0))*fx
			bottom := at(x0, y1) + (at(x1, y1)-at(x0, y1))*fx
			index := row*target.Columns + column
			values[index] = top + (bottom-top)*fy
			valid[index] = true
		}
	}
	return values, valid, nil
}

// MaskStatistics computes calibrated ROI statistics without VOI transforms.
func (m *Map) MaskStatistics(frame int, mask *roi.RasterMask) (Statistics, error) {
	if mask == nil || mask.Rows != m.Rows || mask.Columns != m.Columns {
		return Statistics{}, fmt.Errorf("%w: ROI mask dimensions", ErrGeometryMismatch)
	}
	values, err := m.FrameValues(frame)
	if err != nil {
		return Statistics{}, err
	}
	stats := roi.Stats2D(mask, func(x, y int) (float64, bool) {
		return values[y*m.Columns+x], true
	})
	return Statistics{Count: stats.Count, Min: stats.Min, Max: stats.Max, Mean: stats.Mean, StdDev: stats.StdDev}, nil
}

func (m *Map) SetCacheLimitBytes(limit int64) {
	if m == nil {
		return
	}
	m.cacheMu.Lock()
	m.cacheLimit = max(0, limit)
	m.trimLocked()
	m.cacheMu.Unlock()
}

func (m *Map) CachedBytes() int64 {
	if m == nil {
		return 0
	}
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	return m.cacheBytes
}

func (m *Map) EvictBytes(target int64) int64 {
	if m == nil || target <= 0 {
		return 0
	}
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	before := m.cacheBytes
	for m.cacheBytes > 0 && before-m.cacheBytes < target && len(m.cacheOrder) > 0 {
		m.evictOldestLocked()
	}
	return before - m.cacheBytes
}

func (m *Map) touchLocked(index int) {
	for i, value := range m.cacheOrder {
		if value == index {
			copy(m.cacheOrder[i:], m.cacheOrder[i+1:])
			m.cacheOrder[len(m.cacheOrder)-1] = index
			return
		}
	}
}

func (m *Map) trimLocked() {
	for m.cacheBytes > m.cacheLimit && len(m.cacheOrder) > 0 {
		m.evictOldestLocked()
	}
}

func (m *Map) evictOldestLocked() {
	index := m.cacheOrder[0]
	m.cacheOrder = m.cacheOrder[1:]
	m.cacheBytes -= int64(len(m.cache[index]) * 8)
	delete(m.cache, index)
}

func readDimensions(obj *object.Object, perFrame []*object.Object, frames []Frame) error {
	dimensions := sequence(obj, tagDimensionIndexSeq)
	if len(dimensions) == 0 {
		return nil
	}
	pointers := make([]core.Tag, len(dimensions))
	for index, item := range dimensions {
		tag, ok := readTag(item, tagDimensionIndexPointer)
		if !ok {
			return fmt.Errorf("%w: invalid DimensionIndexPointer", ErrUnsupportedDimensions)
		}
		pointers[index] = tag
	}
	for index := range frames {
		if index >= len(perFrame) {
			return fmt.Errorf("%w: dimensions require per-frame groups", ErrUnsupportedDimensions)
		}
		content := firstItem(perFrame[index], tagFrameContentSeq)
		values64 := derivedio.Ints(content, tagDimensionIndexValues)
		if len(values64) != len(pointers) {
			return fmt.Errorf("%w: frame %d dimension value count", ErrUnsupportedDimensions, index+1)
		}
		frames[index].DimensionIndexValues = make([]int, len(values64))
		for i, value := range values64 {
			frames[index].DimensionIndexValues[i] = int(value)
		}
	}
	for dimension, pointer := range pointers {
		if pointer == tagImagePositionPatient || pointer == tagInStackPositionNumber {
			continue
		}
		first := frames[0].DimensionIndexValues[dimension]
		for frame := 1; frame < len(frames); frame++ {
			if frames[frame].DimensionIndexValues[dimension] != first {
				return fmt.Errorf("%w: varying dimension %s", ErrUnsupportedDimensions, pointer)
			}
		}
	}
	return nil
}

func readReferences(obj *object.Object) []Reference {
	seen := map[string]Reference{}
	var walk func(*object.Object)
	walk = func(current *object.Object) {
		if current == nil {
			return
		}
		if uid := derivedio.CleanUID(current, tagReferencedSOPInstanceUID); uid != "" {
			seen[uid] = Reference{
				SOPClassUID:    derivedio.CleanUID(current, tagReferencedSOPClassUID),
				SOPInstanceUID: uid,
			}
		}
		for _, element := range current.Elements() {
			if element.VR() != core.VRSQ {
				continue
			}
			if items, ok := current.GetSequence(element.Tag()); ok {
				for _, item := range items {
					walk(item)
				}
			}
		}
	}
	walk(obj)
	out := make([]Reference, 0, len(seen))
	for _, ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SOPInstanceUID < out[j].SOPInstanceUID })
	return out
}

func readQuantity(item *object.Object) Code {
	for _, quantityItem := range sequence(item, tagQuantityDefinitionSeq) {
		for _, tag := range []core.Tag{tagConceptCodeSeq, tagConceptNameCodeSeq} {
			if code := readCode(firstItem(quantityItem, tag)); code.String() != "" {
				return code
			}
		}
	}
	return Code{}
}

func readCode(item *object.Object) Code {
	if item == nil {
		return Code{}
	}
	return Code{
		Value:   derivedio.CleanString(item, tagCodeValue),
		Scheme:  derivedio.CleanString(item, tagCodingSchemeDesignator),
		Meaning: derivedio.CleanString(item, tagCodeMeaning),
	}
}

func firstItem(obj *object.Object, tag core.Tag) *object.Object {
	items := sequence(obj, tag)
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func sequence(obj *object.Object, tag core.Tag) []*object.Object {
	if obj == nil {
		return nil
	}
	return derivedio.Sequence(obj, tag)
}

func firstFloat(obj *object.Object, tag core.Tag, fallback float64) float64 {
	values := derivedio.Floats(obj, tag)
	if len(values) == 0 {
		return fallback
	}
	return values[0]
}

func readTag(obj *object.Object, tag core.Tag) (core.Tag, bool) {
	if obj == nil {
		return core.Tag{}, false
	}
	element, ok := obj.Get(tag)
	if !ok {
		return core.Tag{}, false
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw) < 4 {
		return core.Tag{}, false
	}
	order := obj.ValueByteOrder()
	return core.NewTag(order.Uint16(raw), order.Uint16(raw[2:])), true
}

func finite(value float64) bool         { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func positiveFinite(value float64) bool { return value > 0 && finite(value) }
func finiteVec(value render.Vec3) bool  { return finite(value.X) && finite(value.Y) && finite(value.Z) }

func checkedProduct(values ...int) (int, bool) {
	product := 1
	maxInt := int(^uint(0) >> 1)
	for _, value := range values {
		if value <= 0 || product > maxInt/value {
			return 0, false
		}
		product *= value
	}
	return product, true
}
