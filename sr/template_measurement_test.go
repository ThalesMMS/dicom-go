package sr

import (
	"context"
	"strings"
	"testing"
)

func TestCurrentMeasurementReportTemplateValidatesTheStructureEmittedToday(t *testing.T) {
	definition := CurrentMeasurementReportTemplate()
	if strings.Contains(strings.ToUpper(definition.Key.Identifier), "1500") {
		t.Fatalf("current implementation template must not claim TID 1500: %+v", definition.Key)
	}
	registry, err := NewTemplateRegistry([]TemplateDefinition{definition}, nil)
	if err != nil {
		t.Fatalf("NewTemplateRegistry: %v", err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report := &MeasurementReport{
		SOPClassUID:    Comprehensive3DSRStorage,
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.628.1",
		ContentDate:    "20260807",
		ContentTime:    "120000",
		Groups: []MeasurementGroup{{
			Tracking: TrackingIdentifier{Identifier: "fixture", UID: "1.2.826.0.1.3680043.9.7433.628.2"},
			ReferencedSegment: SegmentReference{
				SOPClassUID: "1.2.840.10008.5.1.4.1.1.66.4", SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.628.3", SegmentNumber: 1,
			},
			Measurements: []ReportMeasurement{{
				ConceptName: CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"},
				Value:       12, Units: MillimeterUnit(),
				Image:   ImageReference{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.628.4"},
				Spatial: SpatialReference{FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.628.5", GraphicType: GraphicTypePoint3D, Coordinates: []Point3D{{X: 1, Y: 2, Z: 3}}},
			}},
		}},
	}
	file, err := WriteMeasurementReport(report)
	if err != nil {
		t.Fatalf("WriteMeasurementReport: %v", err)
	}
	document, err := ReadDocument(file.Dataset)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	validation, err := validator.Validate(context.Background(), document, definition.Key)
	if err != nil || len(validation.Findings) != 0 {
		t.Fatalf("Validate(emitted measurement report) = %+v, %v; want clean report", validation, err)
	}

	emptyValidation, err := validator.Validate(context.Background(), &Document{}, definition.Key)
	if err != nil || len(emptyValidation.Findings) != 0 {
		t.Fatalf("Validate(report with no optional groups) = %+v, %v; want clean report", emptyValidation, err)
	}
}
