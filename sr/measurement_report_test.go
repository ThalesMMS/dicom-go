package sr

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

func Test_TID1500MeasurementReport_round_trips_measurements_and_references(t *testing.T) {
	// Given: a TID 1500-style measurement report with image, SCOORD3D, and SEG refs.
	report := MeasurementReport{
		SOPClassUID:         Comprehensive3DSRStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.88.34.1",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.88.34.study",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.88.34.series",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.88.34.frame",
		ContentDate:         "20260621",
		ContentTime:         "120000",
		Title: CodedEntry{
			CodeValue:              "126000",
			CodingSchemeDesignator: "DCM",
			CodeMeaning:            "Imaging Measurement Report",
		},
		Groups: []MeasurementGroup{{
			Tracking: TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.88.34.2", Identifier: "Lesion 1"},
			ReferencedSegment: SegmentReference{
				SOPClassUID:    "1.2.840.10008.5.1.4.1.1.66.4",
				SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.3",
				SegmentNumber:  1,
			},
			Measurements: []ReportMeasurement{{
				ConceptName: CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"},
				Value:       42.5,
				Units:       CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"},
				Image: ImageReference{
					SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.4",
					Frames:         []int{1},
				},
				Spatial: SpatialReference{
					FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.88.34.frame",
					GraphicType:         GraphicTypePoint3D,
					Coordinates:         []Point3D{{X: 1, Y: 2, Z: 3}},
				},
			}},
		}},
	}
	file, err := WriteMeasurementReport(&report)
	if err != nil {
		t.Fatalf("WriteMeasurementReport: %v", err)
	}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}

	// When: the SR object is read back from a Part 10 round-trip.
	readFile, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile: %v", err)
	}
	roundTrip, err := ReadMeasurementReport(readFile.Dataset)
	if err != nil {
		t.Fatalf("ReadMeasurementReport: %v", err)
	}

	// Then: TID1500 grouping, numeric value, and references are preserved.
	if roundTrip.SOPClassUID != Comprehensive3DSRStorage {
		t.Fatalf("SOPClassUID = %q, want Comprehensive3DSRStorage", roundTrip.SOPClassUID)
	}
	if roundTrip.StudyInstanceUID != report.StudyInstanceUID || roundTrip.SeriesInstanceUID != report.SeriesInstanceUID || roundTrip.FrameOfReferenceUID != report.FrameOfReferenceUID {
		t.Fatalf("round-trip identity = %q/%q/%q, want %q/%q/%q", roundTrip.StudyInstanceUID, roundTrip.SeriesInstanceUID, roundTrip.FrameOfReferenceUID, report.StudyInstanceUID, report.SeriesInstanceUID, report.FrameOfReferenceUID)
	}
	if len(roundTrip.Groups) != 1 || roundTrip.Groups[0].Tracking.Identifier != "Lesion 1" {
		t.Fatalf("Groups = %+v, want tracked lesion", roundTrip.Groups)
	}
	got := roundTrip.Groups[0].Measurements[0]
	if got.Value != 42.5 || got.Units.CodeValue != "mm" {
		t.Fatalf("measurement = %+v, want 42.5 mm", got)
	}
	if got.Spatial.Coordinates[0] != (Point3D{X: 1, Y: 2, Z: 3}) {
		t.Fatalf("spatial coordinates = %+v, want 1/2/3", got.Spatial.Coordinates)
	}
	if got.Spatial.FrameOfReferenceUID != report.FrameOfReferenceUID {
		t.Fatalf("spatial frame UID = %q, want %q", got.Spatial.FrameOfReferenceUID, report.FrameOfReferenceUID)
	}
	if roundTrip.Groups[0].ReferencedSegment.SegmentNumber != 1 {
		t.Fatalf("segment number = %d, want 1", roundTrip.Groups[0].ReferencedSegment.SegmentNumber)
	}
}

func Test_MeasurementReport_WriteMeasurementReport_rejects_nil_report(t *testing.T) {
	_, err := WriteMeasurementReport(nil)
	if !errors.Is(err, ErrInvalidMeasurementReport) {
		t.Fatalf("WriteMeasurementReport(nil) error = %v, want ErrInvalidMeasurementReport", err)
	}
}

func Test_MeasurementReport_WriteMeasurementReport_rejects_wrong_sop_class(t *testing.T) {
	report := &MeasurementReport{
		SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2", // CT, not SR
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.5",
	}
	_, err := WriteMeasurementReport(report)
	if !errors.Is(err, ErrUnsupportedSRStorage) {
		t.Fatalf("WriteMeasurementReport(wrong SOP) error = %v, want ErrUnsupportedSRStorage", err)
	}
}

func TestMeasurementReportRejectsInvalidDocumentState(t *testing.T) {
	tests := []struct {
		name         string
		completion   string
		verification string
	}{
		{name: "invalid completion", completion: "FINAL", verification: verificationUnverified},
		{name: "invalid verification", completion: completionComplete, verification: "ATTESTED"},
		{name: "partial verified", completion: completionPartial, verification: verificationVerified},
		{name: "verified without observer", completion: completionComplete, verification: verificationVerified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WriteMeasurementReport(&MeasurementReport{
				SOPInstanceUID:   "1.2.3.4",
				CompletionFlag:   tt.completion,
				VerificationFlag: tt.verification,
			})
			if !errors.Is(err, ErrInvalidMeasurementReport) {
				t.Fatalf("WriteMeasurementReport() error = %v, want ErrInvalidMeasurementReport", err)
			}
		})
	}
}

func Test_MeasurementReport_ReadMeasurementReport_rejects_nil_dataset(t *testing.T) {
	_, err := ReadMeasurementReport(nil)
	if !errors.Is(err, ErrNoDataset) {
		t.Fatalf("ReadMeasurementReport(nil) error = %v, want ErrNoDataset", err)
	}
}

func Test_MeasurementReport_ReadMeasurementReport_rejects_wrong_sop_class(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{"1.2.840.10008.5.1.4.1.1.2"}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{"1.2.826.0.1.3680043.9.7433.88.34.6"}},
	}, std.Dictionary)
	_, err := ReadMeasurementReport(dataset)
	if !errors.Is(err, ErrUnsupportedSRStorage) {
		t.Fatalf("ReadMeasurementReport(wrong SOP) error = %v, want ErrUnsupportedSRStorage", err)
	}
}

func Test_MeasurementReport_Write_uses_default_sop_class_when_empty(t *testing.T) {
	report := &MeasurementReport{
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.7",
		ContentDate:    "20260101",
		ContentTime:    "120000",
		// SOPClassUID intentionally empty
	}
	file, err := WriteMeasurementReport(report)
	if err != nil {
		t.Fatalf("WriteMeasurementReport with empty SOPClassUID: %v", err)
	}
	roundTrip, err := ReadMeasurementReport(file.Dataset)
	if err != nil {
		t.Fatalf("ReadMeasurementReport: %v", err)
	}
	if roundTrip.SOPClassUID != Comprehensive3DSRStorage {
		t.Fatalf("SOPClassUID = %q, want Comprehensive3DSR default", roundTrip.SOPClassUID)
	}
}

func Test_MeasurementReport_all_sr_storage_classes_accepted(t *testing.T) {
	for _, sopClass := range []string{EnhancedSRStorage, ComprehensiveSRStorage, Comprehensive3DSRStorage} {
		report := &MeasurementReport{
			SOPClassUID:    sopClass,
			SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.8",
			ContentDate:    "20260101",
			ContentTime:    "120000",
		}
		_, err := WriteMeasurementReport(report)
		if err != nil {
			t.Fatalf("WriteMeasurementReport(%s) error = %v", sopClass, err)
		}
	}
}

func Test_MeasurementReport_preserves_content_date_and_time(t *testing.T) {
	report := &MeasurementReport{
		SOPClassUID:    Comprehensive3DSRStorage,
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.9",
		ContentDate:    "20260315",
		ContentTime:    "093045",
	}
	file, err := WriteMeasurementReport(report)
	if err != nil {
		t.Fatalf("WriteMeasurementReport: %v", err)
	}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}
	readFile, err := object.ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile: %v", err)
	}
	roundTrip, err := ReadMeasurementReport(readFile.Dataset)
	if err != nil {
		t.Fatalf("ReadMeasurementReport: %v", err)
	}
	if roundTrip.ContentDate != "20260315" {
		t.Fatalf("ContentDate = %q, want 20260315", roundTrip.ContentDate)
	}
	if roundTrip.ContentTime != "093045" {
		t.Fatalf("ContentTime = %q, want 093045", roundTrip.ContentTime)
	}
}

func TestMeasurementReportEmitsMandatoryDocumentAndContainerAttributes(t *testing.T) {
	report := &MeasurementReport{
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.88.34.11",
		Groups:         []MeasurementGroup{{}},
	}
	file, err := WriteMeasurementReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[core.Tag]string{
		tagSeriesNumber:        "1",
		tagInstanceNumber:      "1",
		tagCompletionFlag:      "PARTIAL",
		tagVerificationFlag:    "UNVERIFIED",
		tagContinuityOfContent: "SEPARATE",
	} {
		if got, ok := file.Dataset.GetString(tag); !ok || got != want {
			t.Fatalf("%v = %q, ok=%v; want %q", tag, got, ok, want)
		}
	}
	if contentDate, ok := file.Dataset.GetString(tagContentDate); !ok || contentDate == "" {
		t.Fatalf("ContentDate = %q, ok=%v; want generated Type 1 value", contentDate, ok)
	}
	if contentTime, ok := file.Dataset.GetString(tagContentTime); !ok || contentTime == "" {
		t.Fatalf("ContentTime = %q, ok=%v; want generated Type 1 value", contentTime, ok)
	}
	groups, ok := file.Dataset.GetSequence(tagContentSequence)
	if !ok || len(groups) != 1 {
		t.Fatalf("ContentSequence = %d groups, ok=%v; want 1", len(groups), ok)
	}
	if got, ok := groups[0].GetString(tagContinuityOfContent); !ok || got != "SEPARATE" {
		t.Fatalf("group ContinuityOfContent = %q, ok=%v; want SEPARATE", got, ok)
	}

	round, err := ReadMeasurementReport(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if round.SeriesNumber != "1" || round.InstanceNumber != "1" || round.CompletionFlag != "PARTIAL" || round.VerificationFlag != "UNVERIFIED" || round.ContinuityOfContent != "SEPARATE" {
		t.Fatalf("mandatory attributes not preserved: %#v", round)
	}
}

func Test_measurementItem_omits_empty_optional_references(t *testing.T) {
	// Given: a measurement without image or SCOORD3D references.
	item := measurementItem(ReportMeasurement{
		ConceptName: CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"},
		Value:       12,
		Units:       CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"},
	})

	// Then: the generated NUM item does not contain empty IMAGE or SCOORD3D children.
	obj := object.FromDataSet(item, nil)
	if children, ok := obj.GetSequence(tagContentSequence); ok || len(children) != 0 {
		t.Fatalf("ContentSequence children = %d, ok=%v; want none", len(children), ok)
	}
}

func TestReadMeasurementReportRejectsIncompleteSCOORD3DTuple(t *testing.T) {
	spatial := derivedio.DataSet(
		derivedio.CS(tagValueType, string(ValueSCoord3D)),
		float32Element(tagGraphicData, 1, 2, 3, 4),
	)
	measurement := derivedio.DataSet(
		derivedio.CS(tagValueType, string(ValueNum)),
		derivedio.Seq(tagContentSequence, spatial),
	)
	group := derivedio.DataSet(
		derivedio.CS(tagValueType, string(ValueContainer)),
		derivedio.Seq(tagContentSequence, measurement),
	)
	obj := derivedio.Object(
		derivedio.UI(tagSOPClassUID, Comprehensive3DSRStorage),
		derivedio.Seq(tagContentSequence, group),
	)

	_, err := ReadMeasurementReport(obj)
	if !errors.Is(err, ErrInvalidGraphicData) {
		t.Fatalf("ReadMeasurementReport() error = %v, want ErrInvalidGraphicData", err)
	}
}

func Test_measurementGroupDataSet_omits_empty_segment_reference(t *testing.T) {
	item := measurementGroupDataSet(MeasurementGroup{
		Tracking: TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.88.34.10", Identifier: "Length 1"},
		Measurements: []ReportMeasurement{{
			ConceptName: CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"},
			Value:       12,
			Units:       MillimeterUnit(),
		}},
	})

	obj := object.FromDataSet(item, nil)
	children, ok := obj.GetSequence(tagContentSequence)
	if !ok {
		t.Fatal("measurement group ContentSequence missing")
	}
	for _, child := range children {
		if conceptMeaning(child) == "Referenced Segment" {
			t.Fatalf("measurement group emitted empty segment reference: %+v", child)
		}
	}
}

func TestMeasurementGroupUsesStandardConceptCodes(t *testing.T) {
	dataSet := measurementGroupDataSet(MeasurementGroup{
		Tracking: TrackingIdentifier{UID: "1.2.3.327", Identifier: "Lesion 327"},
		ReferencedSegment: SegmentReference{
			SOPClassUID: "1.2.840.10008.5.1.4.1.1.66.4", SOPInstanceUID: "1.2.3.327.1", SegmentNumber: 1,
		},
	})
	obj := object.FromDataSet(dataSet, std.Dictionary)
	children, ok := obj.GetSequence(tagContentSequence)
	if !ok {
		t.Fatal("measurement group ContentSequence missing")
	}

	want := map[string]string{
		"Tracking Identifier":        "112039",
		"Tracking Unique Identifier": "112040",
		"Referenced Segment":         "121191",
	}
	for _, child := range children {
		code, ok := readCode(child, tagConceptNameCodeSeq)
		if !ok {
			continue
		}
		codeValue, expected := want[code.CodeMeaning]
		if !expected {
			continue
		}
		if code.CodeValue != codeValue || code.CodingSchemeDesignator != "DCM" {
			t.Errorf("concept %q = %+v, want %s/DCM", code.CodeMeaning, code, codeValue)
		}
		if len(code.CodeValue) > 16 {
			t.Errorf("concept %q Code Value = %q (%d bytes), want at most 16", code.CodeMeaning, code.CodeValue, len(code.CodeValue))
		}
		delete(want, code.CodeMeaning)
	}
	if len(want) != 0 {
		t.Fatalf("missing standard concepts: %v", want)
	}
}

func TestReadMeasurementGroupMatchesStandardCodesNotMeanings(t *testing.T) {
	trackingID := derivedio.DataSet(
		derivedio.CS(tagValueType, string(ValueText)),
		codeSequence(tagConceptNameCodeSeq, CodedEntry{CodeValue: "112039", CodingSchemeDesignator: "DCM", CodeMeaning: "Tracking Unique Identifier"}),
		derivedio.Str(tagTextValue, core.VRUT, "Lesion 327"),
	)
	trackingUID := derivedio.DataSet(
		derivedio.CS(tagValueType, string(ValueUIDRef)),
		codeSequence(tagConceptNameCodeSeq, CodedEntry{CodeValue: "112040", CodingSchemeDesignator: "DCM", CodeMeaning: "Tracking Identifier"}),
		derivedio.UI(tagUID, "1.2.3.327"),
	)
	segment := derivedio.DataSet(
		derivedio.CS(tagValueType, string(ValueImage)),
		codeSequence(tagConceptNameCodeSeq, CodedEntry{CodeValue: "121191", CodingSchemeDesignator: "DCM", CodeMeaning: "Tracking Identifier"}),
		derivedio.Seq(tagRefSOPSequence, derivedio.DataSet(
			derivedio.UI(tagRefSOPClassUID, "1.2.840.10008.5.1.4.1.1.66.4"),
			derivedio.UI(tagRefSOPInstanceUID, "1.2.3.327.1"),
			derivedio.IS(tagReferencedSegmentNumber, 2),
		)),
	)
	obj := derivedio.Object(derivedio.Seq(tagContentSequence, trackingID, trackingUID, segment))

	dec := &decoder{}
	got, err := readMeasurementGroup(dec, obj)
	if err != nil {
		t.Fatal(err)
	}
	if dec.err != nil {
		t.Fatal(dec.err)
	}
	if got.Tracking.Identifier != "Lesion 327" || got.Tracking.UID != "1.2.3.327" {
		t.Fatalf("tracking = %+v, want standard codes to take precedence over meanings", got.Tracking)
	}
	if got.ReferencedSegment.SOPInstanceUID != "1.2.3.327.1" || got.ReferencedSegment.SegmentNumber != 2 {
		t.Fatalf("referenced segment = %+v, want standard code match", got.ReferencedSegment)
	}
}
