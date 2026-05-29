package sr

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/object"
)

func TestFromMeasurementsMapsLengthAngleAndAreaToReportItems(t *testing.T) {
	report, err := FromMeasurements(MeasurementExportOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.99.1",
		StudyInstanceUID:    "1.2.3.study",
		SeriesInstanceUID:   "1.2.3.sr.series",
		FrameOfReferenceUID: "1.2.3.frame",
		ContentDate:         "20260622",
		ContentTime:         "091500",
		Measurements: []ViewerMeasurement{
			{
				Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.11", Identifier: "Length 1"},
				Kind:        MeasurementKindLength,
				Value:       12.5,
				SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
				Spatial:     SpatialReference{FrameOfReferenceUID: "1.2.3.frame", GraphicType: GraphicTypePoint3D, Coordinates: []Point3D{{X: 1, Y: 2, Z: 3}, {X: 4, Y: 5, Z: 6}}},
			},
			{
				Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.12", Identifier: "Angle 1"},
				Kind:        MeasurementKindAngle,
				Value:       90,
				SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
			},
			{
				Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.13", Identifier: "Area 1"},
				Kind:        MeasurementKindArea,
				Value:       25,
				SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.2", Frames: []int{3}},
			},
		},
	})
	if err != nil {
		t.Fatalf("FromMeasurements: %v", err)
	}

	if report.SOPClassUID != Comprehensive3DSRStorage {
		t.Fatalf("SOPClassUID = %q, want Comprehensive3DSRStorage", report.SOPClassUID)
	}
	if report.ContentDate != "20260622" || report.ContentTime != "091500" {
		t.Fatalf("content date/time = %q/%q, want explicit values", report.ContentDate, report.ContentTime)
	}
	if report.StudyInstanceUID != "1.2.3.study" || report.SeriesInstanceUID != "1.2.3.sr.series" || report.FrameOfReferenceUID != "1.2.3.frame" {
		t.Fatalf("report identity = %q/%q/%q, want study/series/frame", report.StudyInstanceUID, report.SeriesInstanceUID, report.FrameOfReferenceUID)
	}
	if len(report.Groups) != 3 {
		t.Fatalf("groups = %d, want one group per measurement", len(report.Groups))
	}
	checks := []struct {
		index       int
		concept     string
		unit        string
		imageSOP    string
		frameNumber int
	}{
		{index: 0, concept: "Distance", unit: "mm", imageSOP: "1.2.3.image.1"},
		{index: 1, concept: "Angle", unit: "deg", imageSOP: "1.2.3.image.1"},
		{index: 2, concept: "Area", unit: "mm2", imageSOP: "1.2.3.image.2", frameNumber: 3},
	}
	for _, check := range checks {
		got := report.Groups[check.index].Measurements[0]
		if got.ConceptName.CodeMeaning != check.concept {
			t.Fatalf("measurement %d concept = %+v, want %s", check.index, got.ConceptName, check.concept)
		}
		if got.Units.CodeValue != check.unit {
			t.Fatalf("measurement %d unit = %+v, want %s", check.index, got.Units, check.unit)
		}
		if got.Image.SOPInstanceUID != check.imageSOP {
			t.Fatalf("measurement %d image ref = %+v, want SOP %s", check.index, got.Image, check.imageSOP)
		}
		if check.frameNumber != 0 && (len(got.Image.Frames) != 1 || got.Image.Frames[0] != check.frameNumber) {
			t.Fatalf("measurement %d frame refs = %+v, want %d", check.index, got.Image.Frames, check.frameNumber)
		}
	}
}

func TestFromMeasurementsRejectsMissingSourceImageReference(t *testing.T) {
	_, err := FromMeasurements(MeasurementExportOptions{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.99.2",
		StudyInstanceUID:  "1.2.3.study",
		SeriesInstanceUID: "1.2.3.sr.series",
		Measurements: []ViewerMeasurement{{
			Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.21", Identifier: "Length 1"},
			Kind:        MeasurementKindLength,
			Value:       12.5,
			SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2"},
		}},
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("FromMeasurements error = %v, want ErrMissingReference", err)
	}
}

func TestFromMeasurementsRejectsMissingStudyOrSeriesIdentity(t *testing.T) {
	_, err := FromMeasurements(MeasurementExportOptions{
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.99.4",
		Measurements: []ViewerMeasurement{{
			Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.41", Identifier: "Length 1"},
			Kind:        MeasurementKindLength,
			Value:       12.5,
			SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
		}},
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("FromMeasurements error = %v, want ErrMissingReference", err)
	}
}

func TestFromMeasurementsRejectsSpatialReferenceWithoutFrameOfReference(t *testing.T) {
	_, err := FromMeasurements(MeasurementExportOptions{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.99.5",
		StudyInstanceUID:  "1.2.3.study",
		SeriesInstanceUID: "1.2.3.sr.series",
		Measurements: []ViewerMeasurement{{
			Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.51", Identifier: "Length 1"},
			Kind:        MeasurementKindLength,
			Value:       12.5,
			SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
			Spatial:     SpatialReference{GraphicType: GraphicTypePoint3D, Coordinates: []Point3D{{X: 1, Y: 2, Z: 3}}},
		}},
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("FromMeasurements error = %v, want ErrMissingReference", err)
	}
}

func TestFromMeasurementsProducesDeterministicEncodedDatasets(t *testing.T) {
	opts := MeasurementExportOptions{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.99.3",
		StudyInstanceUID:  "1.2.3.study",
		SeriesInstanceUID: "1.2.3.sr.series",
		ContentDate:       "20260622",
		ContentTime:       "092000",
		Measurements: []ViewerMeasurement{{
			Tracking:    TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.99.31", Identifier: "Length 1"},
			Kind:        MeasurementKindLength,
			Value:       8,
			SourceImage: ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
		}},
	}
	first := encodedMeasurementReport(t, opts)
	second := encodedMeasurementReport(t, opts)
	if !bytes.Equal(first, second) {
		t.Fatal("encoded measurement reports differ for identical input")
	}
}

func encodedMeasurementReport(t *testing.T, opts MeasurementExportOptions) []byte {
	t.Helper()
	report, err := FromMeasurements(opts)
	if err != nil {
		t.Fatalf("FromMeasurements: %v", err)
	}
	file, err := WriteMeasurementReport(report)
	if err != nil {
		t.Fatalf("WriteMeasurementReport: %v", err)
	}
	var out bytes.Buffer
	if err := object.WriteFile(&out, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}
	return out.Bytes()
}
