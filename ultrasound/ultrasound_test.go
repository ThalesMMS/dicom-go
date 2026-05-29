package ultrasound

import (
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/sr"
)

func TestReadUsesPerFrameSharedAndLegacyCalibrationWithoutLeakage(t *testing.T) {
	legacy := regionDataSet(binary.LittleEndian, 0, 0, 99, 99, UnitCentimeter, UnitCentimeter, 0.2, 0.2)
	shared := regionDataSet(binary.LittleEndian, 0, 0, 49, 49, UnitCentimeter, UnitCentimeter, 0.1, 0.1)
	frameOne := regionDataSet(binary.LittleEndian, 10, 10, 29, 29, UnitCentimeter, UnitCentimeter, 0.05, 0.05)
	root := derivedio.Object(
		derivedio.IS(tagNumberOfFrames, 2),
		derivedio.Seq(tagUSRegions, legacy),
		derivedio.Seq(tagSharedFunctionalGroups,
			derivedio.DataSet(derivedio.Seq(tagUSRegions, shared)),
		),
		derivedio.Seq(tagPerFrameFunctionalGroups,
			derivedio.DataSet(derivedio.Seq(tagUSRegions, frameOne)),
			derivedio.DataSet(),
		),
	)

	got, err := Read(&object.File{Dataset: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(got.Frames))
	}
	if delta := got.Frames[0].Regions[0].DeltaX; delta != 0.05 {
		t.Fatalf("frame 1 delta = %v, want per-frame 0.05", delta)
	}
	if delta := got.Frames[1].Regions[0].DeltaX; delta != 0.1 {
		t.Fatalf("frame 2 delta = %v, want shared 0.1; per-frame metadata leaked", delta)
	}
}

func TestReadRegionItemsRejectsOutOfRangeDataTypeAndFlags(t *testing.T) {
	uint64Element := func(tag core.Tag, value uint64) core.Element {
		raw := make([]byte, 8)
		binary.LittleEndian.PutUint64(raw, value)
		return derivedio.Raw(tag, core.VRUV, raw)
	}
	tests := []struct {
		name    string
		element core.Element
	}{
		{name: "negative data type", element: rawInt(tagDataType, core.VRSL, binary.LittleEndian, -1)},
		{name: "data type above uint16", element: rawInt(tagDataType, core.VRUL, binary.LittleEndian, math.MaxUint16+1)},
		{name: "negative flags", element: rawInt(tagFlags, core.VRSL, binary.LittleEndian, -1)},
		{name: "flags above uint32", element: uint64Element(tagFlags, math.MaxUint32+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := object.FromDataSet(
				regionDataSet(binary.LittleEndian, 0, 0, 9, 9, UnitCentimeter, UnitCentimeter, 1, 1),
				nil,
			)
			item.Put(tt.element)
			if _, err := readRegionItems([]*object.Object{item}); err == nil {
				t.Fatal("readRegionItems() error = nil")
			}
		})
	}

	item := object.FromDataSet(
		regionDataSet(binary.LittleEndian, 0, 0, 9, 9, UnitCentimeter, UnitCentimeter, 1, 1),
		nil,
	)
	item.Put(rawInt(tagDataType, core.VRUL, binary.LittleEndian, math.MaxUint16))
	item.Put(uint64Element(tagFlags, math.MaxUint32))
	regions, err := readRegionItems([]*object.Object{item})
	if err != nil {
		t.Fatalf("readRegionItems(max values): %v", err)
	}
	if regions[0].DataType != math.MaxUint16 || regions[0].Flags != math.MaxUint32 {
		t.Fatalf("max values = DataType %d Flags %d", regions[0].DataType, regions[0].Flags)
	}
}

func TestReadBinaryVRHonorsObjectByteOrder(t *testing.T) {
	for _, test := range []struct {
		name  string
		order binary.ByteOrder
	}{
		{name: "little", order: binary.LittleEndian},
		{name: "big", order: binary.BigEndian},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := derivedio.Object(
				derivedio.Seq(tagUSRegions,
					regionDataSet(test.order, 4, 6, 104, 206, UnitSecond, UnitCentimeterPerSec, 0.004, -2.5),
				),
			)
			root.SetValueByteOrder(test.order)
			got, err := Read(&object.File{Dataset: root})
			if err != nil {
				t.Fatal(err)
			}
			region := got.Frames[0].Regions[0]
			if region.Bounds != (image.Rectangle{Min: image.Pt(4, 6), Max: image.Pt(104, 206)}) {
				t.Fatalf("bounds = %v", region.Bounds)
			}
			if region.UnitsX != UnitSecond || region.UnitsY != UnitCentimeterPerSec ||
				region.DeltaX != 0.004 || region.DeltaY != -2.5 {
				t.Fatalf("decoded region = %#v", region)
			}
		})
	}
}

func TestRegionPhysicalReferenceIsRelativeToRegionMinimum(t *testing.T) {
	region := Region{
		Bounds: image.Rectangle{Min: image.Pt(100, 50), Max: image.Pt(200, 150)},
		DeltaX: 0.1, DeltaY: -0.2,
		ReferencePixelX: 5, ReferencePixelY: -2,
		ReferenceValueX: 1, ReferenceValueY: 10,
	}
	x, y := region.Physical(image.Pt(115, 58))
	if x != 2 || y != 8 {
		t.Fatalf("Physical() = (%v,%v), want (2,8)", x, y)
	}
}

func TestRegionAwareMeasurementsAndDoppler(t *testing.T) {
	spatial := Region{
		Index: 0, Bounds: image.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(99, 99)},
		SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter,
		DeltaX: 0.1, DeltaY: 0.2,
	}
	doppler := Region{
		Index: 1, Bounds: image.Rectangle{Min: image.Pt(100, 0), Max: image.Pt(199, 99)},
		SpatialFormat: SpatialSpectral, UnitsX: UnitSecond, UnitsY: UnitCentimeterPerSec,
		DeltaX: 0.01, DeltaY: -1,
		ReferencePixelX: 10, ReferencePixelY: 50,
	}
	frame := FrameCalibration{FrameIndex: 2, Regions: []Region{spatial, doppler}}

	distance, err := Distance(frame, image.Pt(0, 0), image.Pt(30, 20))
	if err != nil {
		t.Fatal(err)
	}
	if distance.Value != 5 || distance.Unit != UnitCentimeter ||
		distance.Reference != (RegionReference{FrameNumber: 3, RegionIndex: 0}) {
		t.Fatalf("distance = %#v", distance)
	}
	area, err := EllipseArea(frame, image.Rect(0, 0, 20, 10))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(area.Value-math.Pi) > 1e-12 || area.Unit != UnitSquareCentimeter {
		t.Fatalf("ellipse area = %#v, want pi cm2", area)
	}
	axes, err := Doppler(frame, image.Pt(130, 25))
	if err != nil {
		t.Fatal(err)
	}
	if axes.X != 0.2 || axes.Y != 25 || axes.XUnit != UnitSecond || axes.YUnit != UnitCentimeterPerSec {
		t.Fatalf("Doppler() = %#v", axes)
	}
	frequencyScale := doppler
	frequencyScale.Flags = 1 << 2
	_, err = Doppler(FrameCalibration{Regions: []Region{frequencyScale}}, image.Pt(130, 25))
	if !errors.Is(err, ErrUnsupportedUnits) {
		t.Fatalf("frequency Doppler error = %v, want ErrUnsupportedUnits", err)
	}
}

func TestMeasurementsRejectCrossRegionAmbiguityAndMissingCalibration(t *testing.T) {
	left := Region{
		Index: 0, Bounds: image.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(49, 99)},
		UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 0.1, DeltaY: 0.1,
	}
	right := Region{
		Index: 1, Bounds: image.Rectangle{Min: image.Pt(50, 0), Max: image.Pt(99, 99)},
		UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 0.2, DeltaY: 0.2,
	}
	_, err := Distance(FrameCalibration{Regions: []Region{left, right}}, image.Pt(10, 10), image.Pt(60, 10))
	if !errors.Is(err, ErrCrossRegion) {
		t.Fatalf("cross-region error = %v, want ErrCrossRegion", err)
	}
	_, err = Distance(FrameCalibration{Regions: []Region{left}}, image.Pt(10, 10), image.Pt(60, 10))
	if !errors.Is(err, ErrCrossRegion) {
		t.Fatalf("region-boundary error = %v, want ErrCrossRegion", err)
	}

	equivalentOverlap := left
	equivalentOverlap.Index = 1
	equivalentOverlap.ReferencePixelX = 5
	equivalentOverlap.ReferenceValueX = 12
	if _, err = Distance(FrameCalibration{Regions: []Region{left, equivalentOverlap}}, image.Pt(10, 10), image.Pt(20, 20)); err != nil {
		t.Fatalf("equivalent overlapping spatial scales: %v", err)
	}

	overlap := right
	overlap.Bounds = left.Bounds
	_, err = Distance(FrameCalibration{Regions: []Region{left, overlap}}, image.Pt(10, 10), image.Pt(20, 20))
	if !errors.Is(err, ErrAmbiguousRegion) {
		t.Fatalf("overlap error = %v, want ErrAmbiguousRegion", err)
	}

	_, err = Distance(FrameCalibration{}, image.Pt(0, 0), image.Pt(1, 1))
	if !errors.Is(err, ErrUncalibrated) {
		t.Fatalf("missing error = %v, want ErrUncalibrated", err)
	}
}

func TestBiometryTemplatesUseStandardCodesWithoutClinicalInference(t *testing.T) {
	frame := FrameCalibration{Regions: []Region{{
		Bounds: image.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(99, 99)},
		UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 0.1, DeltaY: 0.1,
	}}}
	measurement, template, err := MeasureBiometry(frame, BiometryBiparietalDiameter, []image.Point{image.Pt(0, 0), image.Pt(30, 40)})
	if err != nil {
		t.Fatal(err)
	}
	if measurement.Value != 5 || template.Concept.CodeValue != "11820-8" || template.Concept.CodingSchemeDesignator != "LN" {
		t.Fatalf("biometry = %#v template %#v", measurement, template)
	}

	_, template, err = MeasureBiometry(frame, BiometryHeadCircumference, []image.Point{
		image.Pt(0, 0), image.Pt(10, 0), image.Pt(10, 10), image.Pt(0, 10),
	})
	if err != nil || template.Concept.CodeValue != "11984-2" {
		t.Fatalf("head circumference template = %#v err=%v", template, err)
	}
}

func TestAnnotationsFromMeasurementReportPreservesCodesUnitsFrameRegionAndSpatialEvidence(t *testing.T) {
	report := &sr.MeasurementReport{Groups: []sr.MeasurementGroup{{
		Tracking: sr.TrackingIdentifier{
			UID: "1.2.3", Identifier: "Measurement 1 length [US frame 3 region 2]",
		},
		Measurements: []sr.ReportMeasurement{{
			ConceptName: sr.CodedEntry{CodeValue: "11963-6", CodingSchemeDesignator: "LN", CodeMeaning: "Femur Length"},
			Value:       4.2,
			Units:       sr.CodedEntry{CodeValue: "cm", CodingSchemeDesignator: "UCUM", CodeMeaning: "centimeter"},
			Image:       sr.ImageReference{SOPClassUID: "1.2", SOPInstanceUID: "1.2.3.4", Frames: []int{3}},
			Spatial: sr.SpatialReference{
				FrameOfReferenceUID: "9.8.7", GraphicType: sr.GraphicTypePoint3D,
				Coordinates: []sr.Point3D{{X: 1, Y: 2, Z: 3}, {X: 4, Y: 5, Z: 6}},
			},
		}},
	}}}
	annotations := AnnotationsFromMeasurementReport(report)
	if len(annotations) != 1 {
		t.Fatalf("annotations = %d, want 1", len(annotations))
	}
	got := annotations[0]
	if got.Template.Kind != BiometryFemurLength || got.Value != 4.2 || got.Units.CodeValue != "cm" ||
		got.Region == nil || *got.Region != (RegionReference{FrameNumber: 3, RegionIndex: 1}) ||
		len(got.SourceImage.Frames) != 1 || got.SourceImage.Frames[0] != 3 ||
		len(got.Spatial.Coordinates) != 2 {
		t.Fatalf("annotation = %#v", got)
	}
	if err := got.ValidateEvidence(); err != nil {
		t.Fatal(err)
	}
	report.Groups[0].Measurements[0].Image.Frames[0] = 99
	report.Groups[0].Measurements[0].Spatial.Coordinates[0].X = 99
	if got.SourceImage.Frames[0] != 3 || got.Spatial.Coordinates[0].X != 1 {
		t.Fatal("annotation evidence aliases the source report")
	}
}

func BenchmarkDistance(b *testing.B) {
	frame := FrameCalibration{Regions: []Region{{
		Bounds: image.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(511, 511)},
		UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 0.01, DeltaY: 0.01,
	}}}
	a, z := image.Pt(1, 2), image.Pt(500, 400)
	b.ReportAllocs()
	for range b.N {
		if _, err := Distance(frame, a, z); err != nil {
			b.Fatal(err)
		}
	}
}

func regionDataSet(order binary.ByteOrder, minX, minY, maxX, maxY int, unitsX, unitsY PhysicalUnit, deltaX, deltaY float64) core.DataSet {
	return derivedio.DataSet(
		rawInt(tagSpatialFormat, core.VRUS, order, 1),
		rawInt(tagDataType, core.VRUS, order, 1),
		rawInt(tagFlags, core.VRUL, order, 0),
		rawInt(tagMinX, core.VRUL, order, int64(minX)),
		rawInt(tagMinY, core.VRUL, order, int64(minY)),
		rawInt(tagMaxX, core.VRUL, order, int64(maxX)),
		rawInt(tagMaxY, core.VRUL, order, int64(maxY)),
		rawInt(tagReferenceX, core.VRSL, order, 0),
		rawInt(tagReferenceY, core.VRSL, order, 0),
		rawInt(tagUnitsX, core.VRUS, order, int64(unitsX)),
		rawInt(tagUnitsY, core.VRUS, order, int64(unitsY)),
		rawFloat(tagReferenceValueX, order, 0),
		rawFloat(tagReferenceValueY, order, 0),
		rawFloat(tagDeltaX, order, deltaX),
		rawFloat(tagDeltaY, order, deltaY),
	)
}

func rawInt(tag core.Tag, vr core.VR, order binary.ByteOrder, value int64) core.Element {
	width := 4
	if vr == core.VRUS {
		width = 2
	}
	raw := make([]byte, width)
	if width == 2 {
		order.PutUint16(raw, uint16(value))
	} else {
		order.PutUint32(raw, uint32(value))
	}
	return derivedio.Raw(tag, vr, raw)
}

func rawFloat(tag core.Tag, order binary.ByteOrder, value float64) core.Element {
	raw := make([]byte, 8)
	order.PutUint64(raw, math.Float64bits(value))
	return derivedio.Raw(tag, core.VRFD, raw)
}
