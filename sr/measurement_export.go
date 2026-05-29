package sr

import (
	"errors"
	"fmt"
	"strings"
)

type MeasurementKind string

const (
	MeasurementKindLength MeasurementKind = "length"
	MeasurementKindAngle  MeasurementKind = "angle"
	MeasurementKindArea   MeasurementKind = "area"
)

var ErrMissingReference = errors.New("dicom/sr: missing measurement reference")

type ViewerMeasurement struct {
	Tracking    TrackingIdentifier
	Kind        MeasurementKind
	Label       string
	Value       float64
	ConceptName CodedEntry
	Units       CodedEntry
	SourceImage ImageReference
	Spatial     SpatialReference
}

type MeasurementExportOptions struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	ContentDate         string
	ContentTime         string
	Title               CodedEntry
	Measurements        []ViewerMeasurement
}

// FromMeasurements maps viewer-level measurement primitives to the measurement
// report model used by WriteMeasurementReport. Each measurement must carry a
// complete source image reference so the resulting SR NUM item can be traced
// back to image evidence.
func FromMeasurements(opts MeasurementExportOptions) (*MeasurementReport, error) {
	if strings.TrimSpace(opts.SOPInstanceUID) == "" {
		return nil, fmt.Errorf("%w: SOP Instance UID is required", ErrInvalidMeasurementReport)
	}
	if strings.TrimSpace(opts.StudyInstanceUID) == "" || strings.TrimSpace(opts.SeriesInstanceUID) == "" {
		return nil, fmt.Errorf("%w: study and series instance UIDs are required", ErrMissingReference)
	}
	report := &MeasurementReport{
		SOPClassUID:         firstMeasurementNonEmpty(opts.SOPClassUID, Comprehensive3DSRStorage),
		SOPInstanceUID:      strings.TrimSpace(opts.SOPInstanceUID),
		StudyInstanceUID:    strings.TrimSpace(opts.StudyInstanceUID),
		SeriesInstanceUID:   strings.TrimSpace(opts.SeriesInstanceUID),
		FrameOfReferenceUID: strings.TrimSpace(opts.FrameOfReferenceUID),
		ContentDate:         strings.TrimSpace(opts.ContentDate),
		ContentTime:         strings.TrimSpace(opts.ContentTime),
		Title:               opts.Title,
		Groups:              make([]MeasurementGroup, 0, len(opts.Measurements)),
	}
	for index, measurement := range opts.Measurements {
		group, err := measurementGroupFromViewer(report.SOPInstanceUID, index, measurement)
		if err != nil {
			return nil, err
		}
		report.Groups = append(report.Groups, group)
	}
	return report, nil
}

func measurementGroupFromViewer(reportUID string, index int, measurement ViewerMeasurement) (MeasurementGroup, error) {
	if err := validateMeasurementSourceImage(measurement.SourceImage); err != nil {
		return MeasurementGroup{}, err
	}
	if err := validateMeasurementSpatialReference(measurement.Spatial); err != nil {
		return MeasurementGroup{}, err
	}
	concept, units, err := conceptAndUnitsForMeasurement(measurement)
	if err != nil {
		return MeasurementGroup{}, err
	}
	return MeasurementGroup{
		Tracking: trackingForViewerMeasurement(reportUID, index, measurement),
		Measurements: []ReportMeasurement{{
			ConceptName: concept,
			Value:       measurement.Value,
			Units:       units,
			Image:       normalizedImageReference(measurement.SourceImage),
			Spatial:     normalizedSpatialReference(measurement.Spatial),
		}},
	}, nil
}

func conceptAndUnitsForMeasurement(measurement ViewerMeasurement) (CodedEntry, CodedEntry, error) {
	concept, units, ok := defaultConceptAndUnits(measurement.Kind)
	if !ok {
		return CodedEntry{}, CodedEntry{}, fmt.Errorf("%w: unsupported measurement kind %q", ErrInvalidMeasurementReport, measurement.Kind)
	}
	if !measurement.ConceptName.Empty() {
		concept = measurement.ConceptName
	}
	if !measurement.Units.Empty() {
		units = measurement.Units
	}
	return concept, units, nil
}

func defaultConceptAndUnits(kind MeasurementKind) (CodedEntry, CodedEntry, bool) {
	switch kind {
	case MeasurementKindLength:
		return CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"}, MillimeterUnit(), true
	case MeasurementKindAngle:
		return CodedEntry{CodeValue: "121207", CodingSchemeDesignator: "DCM", CodeMeaning: "Angle"}, DegreeUnit(), true
	case MeasurementKindArea:
		return CodedEntry{CodeValue: "42798000", CodingSchemeDesignator: "SCT", CodeMeaning: "Area"}, SquareMillimeterUnit(), true
	default:
		return CodedEntry{}, CodedEntry{}, false
	}
}

func validateMeasurementSourceImage(ref ImageReference) error {
	if strings.TrimSpace(ref.SOPClassUID) == "" || strings.TrimSpace(ref.SOPInstanceUID) == "" {
		return fmt.Errorf("%w: source image SOP class and instance UIDs are required", ErrMissingReference)
	}
	for _, frame := range ref.Frames {
		if frame <= 0 {
			return fmt.Errorf("%w: referenced frame numbers must be one-based", ErrMissingReference)
		}
	}
	return nil
}

func validateMeasurementSpatialReference(ref SpatialReference) error {
	if !hasSpatialReference(ref) {
		return nil
	}
	if strings.TrimSpace(ref.FrameOfReferenceUID) == "" {
		return fmt.Errorf("%w: SCOORD3D requires referenced frame of reference UID", ErrMissingReference)
	}
	if strings.TrimSpace(ref.GraphicType) == "" || len(ref.Coordinates) == 0 {
		return fmt.Errorf("%w: SCOORD3D requires graphic type and coordinates", ErrMissingReference)
	}
	return nil
}

func trackingForViewerMeasurement(reportUID string, index int, measurement ViewerMeasurement) TrackingIdentifier {
	tracking := measurement.Tracking
	tracking.UID = strings.TrimSpace(tracking.UID)
	tracking.Identifier = firstMeasurementNonEmpty(tracking.Identifier, measurement.Label, defaultMeasurementIdentifier(measurement.Kind, index))
	if tracking.UID == "" {
		tracking.UID = fmt.Sprintf("%s.%d", strings.TrimSpace(reportUID), index+1)
	}
	return tracking
}

func defaultMeasurementIdentifier(kind MeasurementKind, index int) string {
	if kind == "" {
		return fmt.Sprintf("Measurement %d", index+1)
	}
	return fmt.Sprintf("Measurement %d %s", index+1, kind)
}

func normalizedImageReference(ref ImageReference) ImageReference {
	out := ImageReference{
		SOPClassUID:    strings.TrimSpace(ref.SOPClassUID),
		SOPInstanceUID: strings.TrimSpace(ref.SOPInstanceUID),
	}
	if len(ref.Frames) > 0 {
		out.Frames = append([]int(nil), ref.Frames...)
	}
	return out
}

func normalizedSpatialReference(ref SpatialReference) SpatialReference {
	out := SpatialReference{
		FrameOfReferenceUID: strings.TrimSpace(ref.FrameOfReferenceUID),
		GraphicType:         strings.TrimSpace(ref.GraphicType),
		FiducialUID:         strings.TrimSpace(ref.FiducialUID),
	}
	if len(ref.Coordinates) > 0 {
		out.Coordinates = append([]Point3D(nil), ref.Coordinates...)
	}
	return out
}

func MillimeterUnit() CodedEntry {
	return CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"}
}

func DegreeUnit() CodedEntry {
	return CodedEntry{CodeValue: "deg", CodingSchemeDesignator: "UCUM", CodeMeaning: "degree"}
}

func SquareMillimeterUnit() CodedEntry {
	return CodedEntry{CodeValue: "mm2", CodingSchemeDesignator: "UCUM", CodeMeaning: "square millimeter"}
}

func firstMeasurementNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
