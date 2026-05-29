// Package rtdose parses, validates, samples, and analyses DICOM RT Dose
// objects. Geometry and semantics are deliberately fail-closed: callers never
// receive a dose value when the grid cannot be related unambiguously to patient
// coordinates.
package rtdose

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/render"
)

const RTDoseStorage = "1.2.840.10008.5.1.4.1.1.481.2"

const (
	DoseUnitsGray     = "GY"
	DoseUnitsRelative = "RELATIVE"

	DoseTypePhysical  = "PHYSICAL"
	DoseTypeEffective = "EFFECTIVE"
	DoseTypeError     = "ERROR"
)

var (
	ErrUnsupportedSOPClass = errors.New("dicom/rtdose: unsupported SOP class")
	ErrInvalidObject       = errors.New("dicom/rtdose: invalid object")
	ErrGeometryMismatch    = errors.New("dicom/rtdose: geometry mismatch")
	ErrUnsafeSemantics     = errors.New("dicom/rtdose: unsafe dose semantics")
	ErrMemoryLimit         = errors.New("dicom/rtdose: memory limit exceeded")
)

var (
	tagImagePositionPatient      = core.NewTag(0x0020, 0x0032)
	tagImageOrientationPatient   = core.NewTag(0x0020, 0x0037)
	tagSliceThickness            = core.NewTag(0x0018, 0x0050)
	tagPixelSpacing              = core.NewTag(0x0028, 0x0030)
	tagDoseUnits                 = core.NewTag(0x3004, 0x0002)
	tagDoseType                  = core.NewTag(0x3004, 0x0004)
	tagDoseSummationType         = core.NewTag(0x3004, 0x000A)
	tagGridFrameOffsetVector     = core.NewTag(0x3004, 0x000C)
	tagDoseGridScaling           = core.NewTag(0x3004, 0x000E)
	tagDVHSequence               = core.NewTag(0x3004, 0x0050)
	tagDVHDoseScaling            = core.NewTag(0x3004, 0x0052)
	tagDVHVolumeUnits            = core.NewTag(0x3004, 0x0054)
	tagDVHNumberOfBins           = core.NewTag(0x3004, 0x0056)
	tagDVHData                   = core.NewTag(0x3004, 0x0058)
	tagDVHReferencedROISequence  = core.NewTag(0x3004, 0x0060)
	tagDVHROIContributionType    = core.NewTag(0x3004, 0x0062)
	tagDVHMinimumDose            = core.NewTag(0x3004, 0x0070)
	tagDVHMaximumDose            = core.NewTag(0x3004, 0x0072)
	tagDVHMeanDose               = core.NewTag(0x3004, 0x0074)
	tagReferencedROINumber       = core.NewTag(0x3006, 0x0084)
	tagReferencedRTPlanSequence  = core.NewTag(0x300C, 0x0002)
	tagReferencedStructureSetSeq = core.NewTag(0x300C, 0x0060)
)

// Reference identifies an RT object referenced by a dose instance.
type Reference struct {
	SOPClassUID    string
	SOPInstanceUID string
}

// EmbeddedDVH is one DVH item carried in the RT Dose object. Data contains the
// DICOM pairs (dose-bin width, volume) without reinterpretation.
type EmbeddedDVH struct {
	Type             string
	DoseUnits        string
	DoseScaling      float64
	VolumeUnits      string
	NumberOfBins     int
	Data             []float64
	ReferencedROIs   []int
	ContributionType string
	MinimumDose      float64
	MaximumDose      float64
	MeanDose         float64
}

// Dose is a validated RT Dose grid. Values are scaled into the units named by
// Units and ordered slice-major, row-major, column-major.
type Dose struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string

	Units          string
	Type           string
	SummationType  string
	GridScaling    float64
	SliceThickness float64

	Rows         int
	Columns      int
	Frames       int
	Geometry     render.VolumeGeometry
	Values       []float64
	Maximum      float64
	References   []Reference
	EmbeddedDVHs []EmbeddedDVH
}

// Read parses and validates an RT Dose file. Only native integer dose grids are
// accepted; encapsulated grids fail closed rather than being guessed.
func Read(file *object.File) (*Dose, error) {
	if file == nil || file.Dataset == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	return ReadDataset(file.Dataset)
}

// ReadDataset parses an RT Dose dataset whose value byte order has been set by
// the DICOM reader. It is useful to archive/catalog callers that retain the
// dataset without the enclosing Part 10 file.
func ReadDataset(obj *object.Object) (*Dose, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	sopClassUID := derivedio.CleanUID(obj, derivedio.TagSOPClassUID)
	if sopClassUID != RTDoseStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}

	out := &Dose{
		SOPClassUID:         sopClassUID,
		SOPInstanceUID:      derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:    derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:   derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		FrameOfReferenceUID: derivedio.CleanUID(obj, derivedio.TagFrameOfReferenceUID),
		Units:               upper(derivedio.CleanString(obj, tagDoseUnits)),
		Type:                upper(derivedio.CleanString(obj, tagDoseType)),
		SummationType:       upper(derivedio.CleanString(obj, tagDoseSummationType)),
	}
	if err := validateSemantics(out); err != nil {
		return nil, err
	}
	scaling := derivedio.Floats(obj, tagDoseGridScaling)
	if len(scaling) != 1 || !finitePositive(scaling[0]) {
		return nil, fmt.Errorf("%w: DoseGridScaling must be one positive finite value", ErrInvalidObject)
	}
	out.GridScaling = scaling[0]
	if thickness := derivedio.Floats(obj, tagSliceThickness); len(thickness) > 0 {
		if len(thickness) != 1 || !finitePositive(thickness[0]) {
			return nil, fmt.Errorf("%w: SliceThickness must be positive when present", ErrGeometryMismatch)
		}
		out.SliceThickness = thickness[0]
	}

	native, err := pixeldata.ExtractNativeFramesView(obj)
	if err != nil {
		return nil, fmt.Errorf("%w: native Pixel Data: %v", ErrInvalidObject, err)
	}
	meta := native.Metadata
	out.Rows, out.Columns, out.Frames = int(meta.Rows), int(meta.Columns), meta.NumberOfFrames
	if out.Rows <= 0 || out.Columns <= 0 || out.Frames <= 0 ||
		meta.SamplesPerPixel != 1 || (meta.BitsAllocated != 16 && meta.BitsAllocated != 32) ||
		meta.BitsStored == 0 || meta.BitsStored > meta.BitsAllocated ||
		meta.HighBit+1 < meta.BitsStored || meta.PixelRepresentation != 0 {
		return nil, fmt.Errorf("%w: unsupported RT Dose pixel metadata %+v", ErrInvalidObject, meta)
	}
	voxelCount, ok := checkedProduct(out.Rows, out.Columns, out.Frames)
	if !ok || voxelCount > 64*1024*1024 {
		return nil, fmt.Errorf("%w: dose grid has %d x %d x %d voxels", ErrMemoryLimit, out.Columns, out.Rows, out.Frames)
	}

	geometries, reverse, err := doseGeometries(obj, out.Rows, out.Columns, out.Frames)
	if err != nil {
		return nil, err
	}
	out.Geometry = render.BuildVolumeGeometry(geometries, render.DefaultGeometryTolerances())
	if len(out.Geometry.Slices) != out.Frames || !out.Geometry.OrientationStable || out.Geometry.GantryTilted {
		return nil, fmt.Errorf("%w: unstable RT Dose volume", ErrGeometryMismatch)
	}

	out.Values, out.Maximum, err = decodeValues(native, obj.ValueByteOrder(), out.GridScaling, reverse)
	if err != nil {
		return nil, err
	}
	if len(out.Values) != voxelCount {
		return nil, fmt.Errorf("%w: decoded %d values, expected %d", ErrInvalidObject, len(out.Values), voxelCount)
	}
	out.References = readReferences(obj)
	out.EmbeddedDVHs = readEmbeddedDVHs(obj, out.Units)
	return out, nil
}

func validateSemantics(dose *Dose) error {
	if dose.SOPInstanceUID == "" || dose.SeriesInstanceUID == "" || dose.StudyInstanceUID == "" ||
		dose.FrameOfReferenceUID == "" {
		return fmt.Errorf("%w: missing RT Dose identity or frame of reference", ErrInvalidObject)
	}
	switch dose.Units {
	case DoseUnitsGray, DoseUnitsRelative:
	default:
		return fmt.Errorf("%w: unsupported DoseUnits %q", ErrUnsafeSemantics, dose.Units)
	}
	switch dose.Type {
	case DoseTypePhysical, DoseTypeEffective, DoseTypeError:
	default:
		return fmt.Errorf("%w: unsupported DoseType %q", ErrUnsafeSemantics, dose.Type)
	}
	if dose.SummationType == "" {
		return fmt.Errorf("%w: missing DoseSummationType", ErrUnsafeSemantics)
	}
	return nil
}

func doseGeometries(obj *object.Object, rows, columns, frames int) ([]render.SliceGeometry, bool, error) {
	position := derivedio.Floats(obj, tagImagePositionPatient)
	orientation := derivedio.Floats(obj, tagImageOrientationPatient)
	spacing := derivedio.Floats(obj, tagPixelSpacing)
	offsets := derivedio.Floats(obj, tagGridFrameOffsetVector)
	if len(position) != 3 || len(orientation) != 6 || len(spacing) != 2 ||
		!finitePositive(spacing[0]) || !finitePositive(spacing[1]) {
		return nil, false, fmt.Errorf("%w: incomplete RT Dose patient geometry", ErrGeometryMismatch)
	}
	if frames == 1 && len(offsets) == 0 {
		offsets = []float64{0}
	}
	if len(offsets) != frames {
		return nil, false, fmt.Errorf("%w: GridFrameOffsetVector length %d, frames %d", ErrGeometryMismatch, len(offsets), frames)
	}
	for _, value := range append(append([]float64(nil), position...), append(orientation, offsets...)...) {
		if !isFinite(value) {
			return nil, false, fmt.Errorf("%w: non-finite patient geometry", ErrGeometryMismatch)
		}
	}

	rowRaw := render.Vec3{X: orientation[0], Y: orientation[1], Z: orientation[2]}
	colRaw := render.Vec3{X: orientation[3], Y: orientation[4], Z: orientation[5]}
	if math.Abs(rowRaw.Length()-1) > 1e-4 || math.Abs(colRaw.Length()-1) > 1e-4 {
		return nil, false, fmt.Errorf("%w: ImageOrientationPatient directions are not unit vectors", ErrGeometryMismatch)
	}
	rowDir := rowRaw.Normalize()
	colDir := colRaw.Normalize()
	normal := rowDir.Cross(colDir).Normalize()
	if rowDir == (render.Vec3{}) || colDir == (render.Vec3{}) || normal == (render.Vec3{}) ||
		math.Abs(rowDir.Dot(colDir)) > 1e-4 {
		return nil, false, fmt.Errorf("%w: invalid ImageOrientationPatient", ErrGeometryMismatch)
	}
	origin := render.Vec3{X: position[0], Y: position[1], Z: position[2]}

	// DICOM permits either offsets relative to ImagePositionPatient or absolute
	// patient Z values for exactly axial identity orientation.
	if len(offsets) > 0 && math.Abs(offsets[0]) > 1e-5 {
		axialIdentity := near(rowDir.X, 1) && near(rowDir.Y, 0) && near(rowDir.Z, 0) &&
			near(colDir.X, 0) && near(colDir.Y, 1) && near(colDir.Z, 0) &&
			near(offsets[0], origin.Z)
		if !axialIdentity {
			return nil, false, fmt.Errorf("%w: ambiguous GridFrameOffsetVector", ErrGeometryMismatch)
		}
		for i := range offsets {
			offsets[i] -= origin.Z
		}
	}
	reverse := false
	if len(offsets) > 1 {
		increasing, decreasing := true, true
		for i := 1; i < len(offsets); i++ {
			increasing = increasing && offsets[i] > offsets[i-1]
			decreasing = decreasing && offsets[i] < offsets[i-1]
		}
		if !increasing && !decreasing {
			return nil, false, fmt.Errorf("%w: GridFrameOffsetVector is not strictly monotonic", ErrGeometryMismatch)
		}
		reverse = decreasing
		if reverse {
			for left, right := 0, len(offsets)-1; left < right; left, right = left+1, right-1 {
				offsets[left], offsets[right] = offsets[right], offsets[left]
			}
		}
	}

	out := make([]render.SliceGeometry, frames)
	for index, offset := range offsets {
		out[index] = render.SliceGeometry{
			Origin:     origin.Add(normal.Scale(offset)),
			RowDir:     rowDir,
			ColDir:     colDir,
			Normal:     normal,
			RowSpacing: spacing[0],
			ColSpacing: spacing[1],
			Rows:       rows,
			Columns:    columns,
		}
	}
	return out, reverse, nil
}

func decodeValues(native *pixeldata.NativeFrames, order binary.ByteOrder, scaling float64, reverse bool) ([]float64, float64, error) {
	if native == nil || order == nil {
		return nil, 0, fmt.Errorf("%w: missing native Pixel Data byte order", ErrInvalidObject)
	}
	meta := native.Metadata
	perFrame, ok := checkedProduct(int(meta.Rows), int(meta.Columns))
	if !ok {
		return nil, 0, ErrMemoryLimit
	}
	out := make([]float64, perFrame*len(native.Data))
	maximum := 0.0
	for sourceIndex, frame := range native.Data {
		targetIndex := sourceIndex
		if reverse {
			targetIndex = len(native.Data) - 1 - sourceIndex
		}
		bytesPerSample := int(meta.BitsAllocated / 8)
		if len(frame) != perFrame*bytesPerSample {
			return nil, 0, fmt.Errorf("%w: frame %d has %d bytes", ErrInvalidObject, sourceIndex, len(frame))
		}
		for pixelIndex := 0; pixelIndex < perFrame; pixelIndex++ {
			rawOffset := pixelIndex * bytesPerSample
			var value int64
			if bytesPerSample == 2 {
				raw := order.Uint16(frame[rawOffset:])
				value = storedValue(uint64(raw), meta.BitsStored, meta.HighBit, meta.PixelRepresentation == 1)
			} else {
				raw := order.Uint32(frame[rawOffset:])
				value = storedValue(uint64(raw), meta.BitsStored, meta.HighBit, meta.PixelRepresentation == 1)
			}
			scaled := float64(value) * scaling
			if !isFinite(scaled) {
				return nil, 0, fmt.Errorf("%w: non-finite scaled dose", ErrInvalidObject)
			}
			out[targetIndex*perFrame+pixelIndex] = scaled
			if scaled > maximum {
				maximum = scaled
			}
		}
	}
	return out, maximum, nil
}

func storedValue(raw uint64, bitsStored, highBit uint16, signed bool) int64 {
	shift := highBit + 1 - bitsStored
	raw >>= shift
	mask := uint64(1)<<bitsStored - 1
	raw &= mask
	if signed && raw&(uint64(1)<<(bitsStored-1)) != 0 {
		return int64(raw | ^mask)
	}
	return int64(raw)
}

func readReferences(obj *object.Object) []Reference {
	var out []Reference
	for _, sequenceTag := range []core.Tag{tagReferencedRTPlanSequence, tagReferencedStructureSetSeq} {
		for _, item := range derivedio.Sequence(obj, sequenceTag) {
			ref := Reference{
				SOPClassUID:    derivedio.CleanUID(item, derivedio.TagRefSOPClassUID),
				SOPInstanceUID: derivedio.CleanUID(item, derivedio.TagRefSOPInstanceUID),
			}
			if ref.SOPInstanceUID == "" {
				continue
			}
			duplicate := false
			for _, existing := range out {
				duplicate = existing.SOPInstanceUID == ref.SOPInstanceUID
				if duplicate {
					break
				}
			}
			if !duplicate {
				out = append(out, ref)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SOPInstanceUID < out[j].SOPInstanceUID })
	return out
}

func readEmbeddedDVHs(obj *object.Object, doseUnits string) []EmbeddedDVH {
	items := derivedio.Sequence(obj, tagDVHSequence)
	out := make([]EmbeddedDVH, 0, len(items))
	for _, item := range items {
		data := derivedio.Floats(item, tagDVHData)
		bins := derivedio.Int(item, tagDVHNumberOfBins)
		scaling := firstFloat(item, tagDVHDoseScaling)
		if bins <= 0 || len(data) != bins*2 || !finitePositive(scaling) {
			continue
		}
		entry := EmbeddedDVH{
			Type:         upper(derivedio.CleanString(item, core.NewTag(0x3004, 0x0001))),
			DoseUnits:    doseUnits,
			DoseScaling:  scaling,
			VolumeUnits:  upper(derivedio.CleanString(item, tagDVHVolumeUnits)),
			NumberOfBins: bins,
			Data:         append([]float64(nil), data...),
			MinimumDose:  firstFloat(item, tagDVHMinimumDose),
			MaximumDose:  firstFloat(item, tagDVHMaximumDose),
			MeanDose:     firstFloat(item, tagDVHMeanDose),
		}
		for _, ref := range derivedio.Sequence(item, tagDVHReferencedROISequence) {
			number := derivedio.Int(ref, tagReferencedROINumber)
			if number != 0 {
				entry.ReferencedROIs = append(entry.ReferencedROIs, number)
			}
			if entry.ContributionType == "" {
				entry.ContributionType = upper(derivedio.CleanString(ref, tagDVHROIContributionType))
			}
		}
		out = append(out, entry)
	}
	return out
}

// ValueAt returns a dose-grid value at an integer voxel coordinate.
func (d *Dose) ValueAt(column, row, frame int) (float64, bool) {
	if d == nil || column < 0 || column >= d.Columns || row < 0 || row >= d.Rows ||
		frame < 0 || frame >= d.Frames {
		return 0, false
	}
	index := (frame*d.Rows+row)*d.Columns + column
	if index < 0 || index >= len(d.Values) {
		return 0, false
	}
	return d.Values[index], true
}

// SamplePatient trilinearly samples a patient-space point. Points outside the
// dose grid return ok=false; no nearest-grid extrapolation is performed.
func (d *Dose) SamplePatient(point render.Vec3) (value float64, ok bool) {
	if d == nil || len(d.Geometry.Slices) != d.Frames || len(d.Geometry.Positions) != d.Frames || d.Frames == 0 {
		return 0, false
	}
	first := d.Geometry.Slices[0]
	delta := point.Sub(first.Origin)
	column := delta.Dot(first.RowDir) / first.ColSpacing
	row := delta.Dot(first.ColDir) / first.RowSpacing
	if column < 0 || row < 0 || column > float64(d.Columns-1) || row > float64(d.Rows-1) {
		return 0, false
	}
	position := point.Dot(d.Geometry.Normal)
	frame0, frame1, fraction, ok := bracket(d.Geometry.Positions, position)
	if !ok {
		return 0, false
	}
	column0, row0 := int(math.Floor(column)), int(math.Floor(row))
	column1, row1 := min(column0+1, d.Columns-1), min(row0+1, d.Rows-1)
	columnFraction, rowFraction := column-float64(column0), row-float64(row0)
	sampleFrame := func(frame int) (float64, bool) {
		v00, a := d.ValueAt(column0, row0, frame)
		v10, b := d.ValueAt(column1, row0, frame)
		v01, c := d.ValueAt(column0, row1, frame)
		v11, e := d.ValueAt(column1, row1, frame)
		if !(a && b && c && e) {
			return 0, false
		}
		top := v00 + (v10-v00)*columnFraction
		bottom := v01 + (v11-v01)*columnFraction
		return top + (bottom-top)*rowFraction, true
	}
	v0, ok0 := sampleFrame(frame0)
	v1, ok1 := sampleFrame(frame1)
	if !ok0 || !ok1 {
		return 0, false
	}
	return v0 + (v1-v0)*fraction, true
}

// DisplayUnit returns a semantics-safe unit label. Only physical GY is labelled
// as physical absorbed dose; relative and effective/biological values remain
// explicit.
func (d *Dose) DisplayUnit() string {
	if d == nil {
		return ""
	}
	switch {
	case d.Units == DoseUnitsGray && d.Type == DoseTypePhysical:
		return "Gy (physical)"
	case d.Units == DoseUnitsGray && d.Type == DoseTypeEffective:
		return "Gy (effective/biological)"
	case d.Units == DoseUnitsGray && d.Type == DoseTypeError:
		return "Gy (error)"
	case d.Units == DoseUnitsRelative:
		return "relative"
	default:
		return strings.ToLower(d.Units)
	}
}

// ProvenanceLabel is PHI-free text suitable for UI dose readouts.
func (d *Dose) ProvenanceLabel() string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("RTDOSE %s · %s · %s", d.SummationType, d.Type, d.DisplayUnit())
}

func bracket(values []float64, target float64) (lower, upper int, fraction float64, ok bool) {
	if len(values) == 0 || target < values[0]-1e-6 || target > values[len(values)-1]+1e-6 {
		return 0, 0, 0, false
	}
	if len(values) == 1 {
		return 0, 0, 0, math.Abs(target-values[0]) <= 1e-5
	}
	index := sort.Search(len(values), func(i int) bool { return values[i] >= target })
	if index == 0 {
		return 0, 0, 0, true
	}
	if index == len(values) {
		return len(values) - 1, len(values) - 1, 0, true
	}
	if math.Abs(values[index]-target) <= 1e-9 {
		return index, index, 0, true
	}
	lower, upper = index-1, index
	width := values[upper] - values[lower]
	if width <= 0 {
		return 0, 0, 0, false
	}
	return lower, upper, (target - values[lower]) / width, true
}

func firstFloat(obj *object.Object, tag core.Tag) float64 {
	values := derivedio.Floats(obj, tag)
	if len(values) == 0 || !isFinite(values[0]) {
		return 0
	}
	return values[0]
}

func upper(value string) string         { return strings.ToUpper(strings.TrimSpace(value)) }
func isFinite(value float64) bool       { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finitePositive(value float64) bool { return value > 0 && isFinite(value) }
func near(a, b float64) bool            { return math.Abs(a-b) <= 1e-5 }

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
