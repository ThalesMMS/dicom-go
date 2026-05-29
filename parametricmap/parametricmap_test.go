package parametricmap

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/roi"
)

var (
	testPlanePositionSeq    = core.NewTag(0x0020, 0x9113)
	testPlaneOrientationSeq = core.NewTag(0x0020, 0x9116)
	testPixelMeasuresSeq    = core.NewTag(0x0028, 0x9110)
	testPixelSpacing        = core.NewTag(0x0028, 0x0030)
	testImageOrientation    = core.NewTag(0x0020, 0x0037)
	testImagePosition       = core.NewTag(0x0020, 0x0032)
)

func TestReadFloatMapLazilyMapsDimensionsGeometryAndReferences(t *testing.T) {
	file := testMapFile(t, PayloadFloat32, []float64{
		1, 2, 3, 4,
		5, 6, 7, 8,
	}, 2, true, tagInStackPositionNumber)
	m, err := Read(file)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if m.Payload != PayloadFloat32 || m.CachedBytes() != 0 || m.Units.String() != "millimeter" ||
		m.Quantity.String() != "Apparent diffusion coefficient" {
		t.Fatalf("parsed map = payload %q cache %d units %q quantity %q", m.Payload, m.CachedBytes(), m.Units, m.Quantity)
	}
	if len(m.Frames) != 2 || len(m.Frames[1].DimensionIndexValues) != 1 ||
		m.Frames[1].DimensionIndexValues[0] != 2 {
		t.Fatalf("dimension indices = %#v", m.Frames)
	}
	if len(m.References) != 1 || m.References[0].SOPInstanceUID != "1.2.source" {
		t.Fatalf("references = %#v", m.References)
	}
	values, err := m.FrameValues(0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := values, []float64{3, 5, 7, 9}; !equalFloats(got, want) {
		t.Fatalf("mapped float values = %v, want %v", got, want)
	}
	if m.CachedBytes() != 4*8 {
		t.Fatalf("CachedBytes = %d, want 32", m.CachedBytes())
	}
	uncached := make([]float64, 4)
	if err := m.FrameValuesInto(1, uncached); err != nil {
		t.Fatalf("FrameValuesInto() error = %v", err)
	}
	if !equalFloats(uncached, []float64{11, 13, 15, 17}) || m.CachedBytes() != 4*8 {
		t.Fatalf("FrameValuesInto() = %v cache=%d, want mapped values without cache growth", uncached, m.CachedBytes())
	}
	value, ok, err := m.SamplePatient(render.Vec3{X: 0.5, Y: 0.5, Z: 0})
	if err != nil || !ok || math.Abs(value-6) > 1e-9 {
		t.Fatalf("SamplePatient = %v, %v, %v, want 6, true, nil", value, ok, err)
	}
	if _, ok, err := m.SamplePatient(render.Vec3{X: 10, Y: 10, Z: 0}); err != nil || ok {
		t.Fatalf("partial-overlap outside sample = ok %v err %v", ok, err)
	}
	mask := roi.NewRasterMask(2, 2)
	mask.Set(0, 0, true)
	mask.Set(1, 1, true)
	stats, err := m.MaskStatistics(0, mask)
	if err != nil || stats.Count != 2 || stats.Min != 3 || stats.Max != 9 || stats.Mean != 6 {
		t.Fatalf("MaskStatistics = %+v, %v", stats, err)
	}
	if evicted := m.EvictBytes(1); evicted != 32 || m.CachedBytes() != 0 {
		t.Fatalf("EvictBytes = %d, cache %d", evicted, m.CachedBytes())
	}
}

func TestReadDoubleAndIntegerMappings(t *testing.T) {
	doubleMap, err := Read(testMapFile(t, PayloadFloat64, []float64{1.25, 2.5, 3.75, 4}, 1, true, core.Tag{}))
	if err != nil {
		t.Fatal(err)
	}
	values, err := doubleMap.FrameValues(0)
	if err != nil || !equalFloats(values, []float64{3.5, 6, 8.5, 9}) {
		t.Fatalf("double mapping = %v, %v", values, err)
	}

	integerMap, err := Read(testMapFile(t, PayloadInteger, []float64{1, 2, 3, 4}, 1, true, core.Tag{}))
	if err != nil {
		t.Fatal(err)
	}
	values, err = integerMap.FrameValues(0)
	if err != nil || !equalFloats(values, []float64{3, 5, 7, 9}) {
		t.Fatalf("integer mapping = %v, %v", values, err)
	}
}

func TestReadMetadataAcceptsDeferredFloatPayload(t *testing.T) {
	file := testMapFile(t, PayloadFloat32, []float64{1, 2, 3, 4}, 1, true, core.Tag{})
	element, ok := file.Dataset.Get(tagFloatPixelData)
	if !ok {
		t.Fatal("test object is missing Float Pixel Data")
	}
	element.Value = nil
	file.Dataset.Put(element)

	metadata, err := ReadMetadata(file)
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if metadata.Payload != PayloadFloat32 || metadata.NumberOfFrames != 1 {
		t.Fatalf("ReadMetadata() = payload %s frames %d", metadata.Payload, metadata.NumberOfFrames)
	}
	if _, err := metadata.FrameValues(0); !errors.Is(err, ErrPayloadUnavailable) {
		t.Fatalf("FrameValues() error = %v, want ErrPayloadUnavailable", err)
	}
	if _, err := Read(file); err == nil {
		t.Fatal("Read accepted a deferred payload without an attached value provider")
	}
}

func TestReadRejectsUnsafeIntegerPayloadLayout(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*object.Object)
	}{
		{
			name: "zero bits stored",
			mutate: func(obj *object.Object) {
				obj.Put(derivedio.US(derivedio.TagBitsStored, 0))
			},
		},
		{
			name: "bits stored exceeds allocated",
			mutate: func(obj *object.Object) {
				obj.Put(derivedio.US(derivedio.TagBitsStored, 17))
			},
		},
		{
			name: "high bit cannot contain stored bits",
			mutate: func(obj *object.Object) {
				obj.Put(derivedio.US(derivedio.TagHighBit, 14))
			},
		},
		{
			name: "high bit exceeds allocation",
			mutate: func(obj *object.Object) {
				obj.Put(derivedio.US(derivedio.TagHighBit, 16))
			},
		},
		{
			name: "truncated frame",
			mutate: func(obj *object.Object) {
				obj.Put(derivedio.Raw(derivedio.TagPixelData, core.VROW, make([]byte, 6)))
			},
		},
		{
			name: "missing encoded frame",
			mutate: func(obj *object.Object) {
				obj.Put(derivedio.Raw(derivedio.TagPixelData, core.VROW, make([]byte, 8)))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := 1
			values := []float64{1, 2, 3, 4}
			if tt.name == "missing encoded frame" {
				frames = 2
				values = []float64{1, 2, 3, 4, 5, 6, 7, 8}
			}
			file := testMapFile(t, PayloadInteger, values, frames, true, core.Tag{})
			tt.mutate(file.Dataset)
			if _, err := Read(file); !errors.Is(err, ErrInvalidObject) {
				t.Fatalf("Read() error = %v, want ErrInvalidObject", err)
			}
		})
	}
}

func TestReadAndDecodeFailuresAreExplicit(t *testing.T) {
	missing := testMapFile(t, PayloadFloat32, []float64{1, 2, 3, 4}, 1, false, core.Tag{})
	if _, err := Read(missing); !errors.Is(err, ErrMissingMapping) {
		t.Fatalf("missing mapping error = %v", err)
	}

	nonFinite, err := Read(testMapFile(t, PayloadFloat32, []float64{1, math.NaN(), 3, 4}, 1, true, core.Tag{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nonFinite.FrameValues(0); !errors.Is(err, ErrNonFinite) {
		t.Fatalf("NaN decode error = %v, want ErrNonFinite", err)
	}

	unsupported := testMapFile(t, PayloadFloat32, []float64{1, 2, 3, 4, 5, 6, 7, 8}, 2, true, core.NewTag(0x0020, 0x9128))
	if _, err := Read(unsupported); !errors.Is(err, ErrUnsupportedDimensions) {
		t.Fatalf("unsupported dimension error = %v", err)
	}

	brokenGeometry := testMapFile(t, PayloadFloat32, []float64{1, 2, 3, 4}, 1, true, core.Tag{})
	brokenGeometry.Dataset.Remove(tagSharedFunctionalGroups)
	brokenGeometry.Dataset.Put(testMappingSequence())
	if _, err := Read(brokenGeometry); !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("geometry error = %v, want ErrGeometryMismatch", err)
	}

	valid, err := Read(testMapFile(t, PayloadFloat32, []float64{1, 2, 3, 4}, 1, true, core.Tag{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := valid.SamplePatient(render.Vec3{X: math.NaN()}); !errors.Is(err, ErrInvalidObject) || ok {
		t.Fatalf("non-finite point = ok %v err %v, want ErrInvalidObject", ok, err)
	}
	if _, _, err := valid.Resample(render.SliceGeometry{
		Origin: render.Vec3{}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1},
		RowSpacing: math.Inf(1), ColSpacing: 1, Rows: 2, Columns: 2,
	}); !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("invalid target geometry error = %v, want ErrGeometryMismatch", err)
	}
}

func TestCacheLimitEvictsDecodedFrames(t *testing.T) {
	m, err := Read(testMapFile(t, PayloadFloat32, []float64{1, 2, 3, 4, 5, 6, 7, 8}, 2, true, core.Tag{}))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCacheLimitBytes(32)
	if _, err := m.FrameValues(0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FrameValues(1); err != nil {
		t.Fatal(err)
	}
	if m.CachedBytes() != 32 {
		t.Fatalf("bounded cache = %d bytes, want one 32-byte frame", m.CachedBytes())
	}
}

func BenchmarkFrameValuesCached(b *testing.B) {
	m, err := Read(testMapFile(b, PayloadFloat32, []float64{1, 2, 3, 4}, 1, true, core.Tag{}))
	if err != nil {
		b.Fatal(err)
	}
	if _, err := m.FrameValues(0); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := m.FrameValues(0); err != nil {
			b.Fatal(err)
		}
	}
}

func testMapFile(t testing.TB, payload PayloadKind, values []float64, frames int, includeMapping bool, dimensionPointer core.Tag) *object.File {
	t.Helper()
	sharedElements := []core.Element{
		derivedio.Seq(testPlaneOrientationSeq, derivedio.DataSet(
			derivedio.DS(testImageOrientation, 1, 0, 0, 0, 1, 0),
		)),
		derivedio.Seq(testPixelMeasuresSeq, derivedio.DataSet(
			derivedio.DS(testPixelSpacing, 1, 1),
		)),
	}
	if includeMapping {
		sharedElements = append(sharedElements, testMappingSequence())
	}
	perFrame := make([]core.DataSet, frames)
	for index := 0; index < frames; index++ {
		elements := []core.Element{
			derivedio.Seq(testPlanePositionSeq, derivedio.DataSet(
				derivedio.DS(testImagePosition, 0, 0, float64(index)),
			)),
		}
		if dimensionPointer != (core.Tag{}) {
			elements = append(elements, derivedio.Seq(tagFrameContentSeq, derivedio.DataSet(
				rawUL(tagDimensionIndexValues, uint32(index+1)),
			)))
		}
		perFrame[index] = derivedio.DataSet(elements...)
	}
	elements := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, ParametricMapStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.map"),
		derivedio.UI(derivedio.TagStudyInstanceUID, "1.2.study"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, "1.2.series"),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, "1.2.frame"),
		derivedio.US(derivedio.TagRows, 2),
		derivedio.US(derivedio.TagColumns, 2),
		derivedio.IS(derivedio.TagNumberOfFrames, frames),
		derivedio.Seq(tagSharedFunctionalGroups, derivedio.DataSet(sharedElements...)),
		derivedio.Seq(tagPerFrameFunctionalGroups, perFrame...),
		derivedio.Seq(core.NewTag(0x0008, 0x2112), derivedio.DataSet(
			derivedio.UI(tagReferencedSOPClassUID, "1.2.840.10008.5.1.4.1.1.2"),
			derivedio.UI(tagReferencedSOPInstanceUID, "1.2.source"),
		)),
	}
	if dimensionPointer != (core.Tag{}) {
		elements = append(elements, derivedio.Seq(tagDimensionIndexSeq, derivedio.DataSet(
			rawAT(tagDimensionIndexPointer, dimensionPointer),
		)))
	}
	switch payload {
	case PayloadFloat32:
		raw := make([]byte, len(values)*4)
		for index, value := range values {
			binary.LittleEndian.PutUint32(raw[index*4:], math.Float32bits(float32(value)))
		}
		elements = append(elements, derivedio.Raw(tagFloatPixelData, core.VROF, raw))
	case PayloadFloat64:
		raw := make([]byte, len(values)*8)
		for index, value := range values {
			binary.LittleEndian.PutUint64(raw[index*8:], math.Float64bits(value))
		}
		elements = append(elements, derivedio.Raw(tagDoubleFloatPixelData, core.VROD, raw))
	case PayloadInteger:
		raw := make([]byte, len(values)*2)
		for index, value := range values {
			binary.LittleEndian.PutUint16(raw[index*2:], uint16(value))
		}
		elements = append(elements,
			derivedio.US(derivedio.TagSamplesPerPixel, 1),
			derivedio.CS(derivedio.TagPhotometricInterpretation, "MONOCHROME2"),
			derivedio.US(derivedio.TagBitsAllocated, 16),
			derivedio.US(derivedio.TagBitsStored, 16),
			derivedio.US(derivedio.TagHighBit, 15),
			derivedio.US(derivedio.TagPixelRepresentation, 0),
			derivedio.Raw(derivedio.TagPixelData, core.VROW, raw),
		)
	}
	file, err := derivedio.File(ParametricMapStorage, "1.2.map", derivedio.Object(elements...))
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func testMappingSequence() core.Element {
	return derivedio.Seq(tagRealWorldValueMappingSeq, derivedio.DataSet(
		rawFD(tagRWVSlope, 2),
		rawFD(tagRWVIntercept, 1),
		derivedio.Seq(tagMeasurementUnitsCodeSeq, derivedio.DataSet(
			derivedio.SH(tagCodeValue, "mm"),
			derivedio.SH(tagCodingSchemeDesignator, "UCUM"),
			derivedio.LO(tagCodeMeaning, "millimeter"),
		)),
		derivedio.Seq(tagQuantityDefinitionSeq, derivedio.DataSet(
			derivedio.Seq(tagConceptCodeSeq, derivedio.DataSet(
				derivedio.SH(tagCodeValue, "113041"),
				derivedio.SH(tagCodingSchemeDesignator, "DCM"),
				derivedio.LO(tagCodeMeaning, "Apparent diffusion coefficient"),
			)),
		)),
	))
}

func rawAT(tag core.Tag, value core.Tag) core.Element {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint16(raw, value.Group)
	binary.LittleEndian.PutUint16(raw[2:], value.Element)
	return derivedio.Raw(tag, core.VRAT, raw)
}

func rawUL(tag core.Tag, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(raw[index*4:], value)
	}
	return derivedio.Raw(tag, core.VRUL, raw)
}

func rawFD(tag core.Tag, values ...float64) core.Element {
	raw := make([]byte, len(values)*8)
	for index, value := range values {
		binary.LittleEndian.PutUint64(raw[index*8:], math.Float64bits(value))
	}
	return derivedio.Raw(tag, core.VRFD, raw)
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
