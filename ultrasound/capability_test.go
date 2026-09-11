package ultrasound

import (
	"errors"
	"image"
	"testing"
)

func TestInspectFrameCapabilitiesClassifiesSupportedAndUnsupportedRegions(t *testing.T) {
	frame := FrameCalibration{FrameIndex: 2, Regions: []Region{
		{Index: 0, Bounds: image.Rect(0, 0, 10, 10), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: .1, DeltaY: .1},
		{Index: 1, Bounds: image.Rect(20, 0, 30, 10), SpatialFormat: SpatialSpectral, UnitsX: UnitSecond, UnitsY: UnitCentimeterPerSec, DeltaX: .01, DeltaY: 2},
		{Index: 2, Bounds: image.Rect(40, 0, 50, 10), SpatialFormat: SpatialSpectral, Flags: 1 << 2, UnitsX: UnitSecond, UnitsY: UnitHertz, DeltaX: .01, DeltaY: 2},
		{Index: 3, Bounds: image.Rect(60, 0, 70, 10), SpatialFormat: SpatialGraphics, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 1, DeltaY: 1},
		{Index: 4, Bounds: image.Rect(80, 0, 90, 10), SpatialFormat: Spatial2D, UnitsX: UnitNone, UnitsY: UnitNone, DeltaX: 1, DeltaY: 1},
		{Index: 5, Bounds: image.Rect(100, 0, 110, 10), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 0, DeltaY: 1},
	}}

	got := InspectFrameCapabilities(frame)
	if got.Regions != 6 || got.BiometryRegions != 1 || got.DopplerRegions != 1 || got.UnsupportedRegions != 4 {
		t.Fatalf("capabilities = %+v", got)
	}
	assertCapabilityIssue(t, got.Issues, CapabilityIssueFrequencyDoppler, 1)
	assertCapabilityIssue(t, got.Issues, CapabilityIssueUnsupportedSpatialFormat, 1)
	assertCapabilityIssue(t, got.Issues, CapabilityIssueUnsupportedUnits, 1)
	assertCapabilityIssue(t, got.Issues, CapabilityIssueInvalidCalibration, 1)
}

func TestMeasurementsRejectInvalidCalibrationWithoutProducingClinicalValue(t *testing.T) {
	tests := []Region{
		{Index: 3, Bounds: image.Rect(0, 0, 10, 10), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: 0, DeltaY: .1},
		{Index: -1, Bounds: image.Rect(0, 0, 10, 10), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: .1, DeltaY: .1},
		{Index: 3, Bounds: image.Rect(-1, 0, 10, 10), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: .1, DeltaY: .1},
	}
	for _, region := range tests {
		frame := FrameCalibration{Regions: []Region{region}}
		if _, err := Distance(frame, image.Pt(1, 1), image.Pt(5, 1)); !errors.Is(err, ErrInvalidCalibration) {
			t.Fatalf("Distance invalid calibration %+v error = %v", region, err)
		}
	}
}

func TestInspectFrameCapabilitiesReportsNoAndAmbiguousCalibration(t *testing.T) {
	empty := InspectFrameCapabilities(FrameCalibration{})
	assertCapabilityIssue(t, empty.Issues, CapabilityIssueNoCalibration, 1)

	frame := FrameCalibration{Regions: []Region{
		{Index: 4, Bounds: image.Rect(0, 0, 10, 10), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: .1, DeltaY: .1},
		{Index: 9, Bounds: image.Rect(5, 5, 15, 15), SpatialFormat: Spatial2D, UnitsX: UnitCentimeter, UnitsY: UnitCentimeter, DeltaX: .2, DeltaY: .1},
	}}
	got := InspectFrameCapabilities(frame)
	if got.BiometryRegions != 2 || got.UnsupportedRegions != 0 {
		t.Fatalf("capabilities = %+v", got)
	}
	assertCapabilityIssue(t, got.Issues, CapabilityIssueAmbiguousCalibration, 1)
}

func assertCapabilityIssue(t *testing.T, issues []CapabilityIssue, code CapabilityIssueCode, count int) {
	t.Helper()
	for _, issue := range issues {
		if issue.Code == code {
			if issue.Count != count {
				t.Fatalf("issue %q count = %d, want %d", code, issue.Count, count)
			}
			return
		}
	}
	t.Fatalf("missing issue %q in %+v", code, issues)
}
