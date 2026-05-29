package sr

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	EnhancedSRStorage        = "1.2.840.10008.5.1.4.1.1.88.22"
	ComprehensiveSRStorage   = "1.2.840.10008.5.1.4.1.1.88.33"
	Comprehensive3DSRStorage = "1.2.840.10008.5.1.4.1.1.88.34"

	GraphicTypePoint3D = "POINT"
)

var (
	ErrInvalidMeasurementReport = errors.New("dicom/sr: invalid measurement report")
	ErrUnsupportedSRStorage     = errors.New("dicom/sr: unsupported SR storage")
)

var (
	tagReferencedSegmentNumber = core.NewTag(0x0062, 0x000B)
)

var (
	trackingIdentifierConcept       = CodedEntry{CodeValue: "112039", CodingSchemeDesignator: "DCM", CodeMeaning: "Tracking Identifier"}
	trackingUniqueIdentifierConcept = CodedEntry{CodeValue: "112040", CodingSchemeDesignator: "DCM", CodeMeaning: "Tracking Unique Identifier"}
	referencedSegmentConcept        = CodedEntry{CodeValue: "121191", CodingSchemeDesignator: "DCM", CodeMeaning: "Referenced Segment"}
)

type TrackingIdentifier struct {
	UID        string
	Identifier string
}

type SegmentReference struct {
	SOPClassUID    string
	SOPInstanceUID string
	SegmentNumber  int
}

type Point3D struct {
	X float64
	Y float64
	Z float64
}

type SpatialReference struct {
	FrameOfReferenceUID string
	GraphicType         string
	Coordinates         []Point3D
	FiducialUID         string
}

type ReportMeasurement struct {
	ConceptName CodedEntry
	Value       float64
	Units       CodedEntry
	Image       ImageReference
	Spatial     SpatialReference
}

type MeasurementGroup struct {
	Tracking          TrackingIdentifier
	ReferencedSegment SegmentReference
	Measurements      []ReportMeasurement
}

type MeasurementReport struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	ContentDate         string
	ContentTime         string
	SeriesNumber        string
	InstanceNumber      string
	CompletionFlag      string
	VerificationFlag    string
	ContinuityOfContent string
	Title               CodedEntry
	Groups              []MeasurementGroup
}

// WriteMeasurementReport encodes a measurement report as a DICOM file.
// It defaults report.SOPClassUID to Comprehensive3DSRStorage if empty and report.Title to a standard "Imaging Measurement Report" coded entry if empty.
// It returns ErrInvalidMeasurementReport if report is nil or ErrUnsupportedSRStorage if the SOP Class UID is not one of the supported SR storage types.
func WriteMeasurementReport(report *MeasurementReport) (*object.File, error) {
	if report == nil {
		return nil, fmt.Errorf("%w: report is nil", ErrInvalidMeasurementReport)
	}
	sopClassUID := report.SOPClassUID
	if sopClassUID == "" {
		sopClassUID = Comprehensive3DSRStorage
	}
	if !measurementSOPClass(sopClassUID) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSRStorage, sopClassUID)
	}
	completion, verification, err := validateDocumentState(report.CompletionFlag, report.VerificationFlag)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMeasurementReport, err)
	}
	title := report.Title
	if title.Empty() {
		title = CodedEntry{CodeValue: "126000", CodingSchemeDesignator: "DCM", CodeMeaning: "Imaging Measurement Report"}
	}
	elements := []core.Element{
		derivedio.UI(tagSOPClassUID, sopClassUID),
		derivedio.UI(tagSOPInstanceUID, report.SOPInstanceUID),
		derivedio.CS(tagModality, "SR"),
		derivedio.Str(tagSeriesNumber, core.VRIS, defaultString(report.SeriesNumber, "1")),
		derivedio.Str(tagInstanceNumber, core.VRIS, defaultString(report.InstanceNumber, "1")),
		derivedio.CS(tagValueType, string(ValueContainer)),
		derivedio.CS(tagContinuityOfContent, defaultString(report.ContinuityOfContent, "SEPARATE")),
		derivedio.CS(tagCompletionFlag, completion),
		derivedio.CS(tagVerificationFlag, verification),
		codeSequence(tagConceptNameCodeSeq, title),
	}
	if report.StudyInstanceUID != "" {
		elements = append(elements, derivedio.UI(derivedio.TagStudyInstanceUID, report.StudyInstanceUID))
	}
	if report.SeriesInstanceUID != "" {
		elements = append(elements, derivedio.UI(derivedio.TagSeriesInstanceUID, report.SeriesInstanceUID))
	}
	if report.FrameOfReferenceUID != "" {
		elements = append(elements, derivedio.UI(derivedio.TagFrameOfReferenceUID, report.FrameOfReferenceUID))
	}
	contentDate, contentTime := report.ContentDate, report.ContentTime
	if contentDate == "" || contentTime == "" {
		currentDate, currentTime := currentContentDateTime()
		if contentDate == "" {
			contentDate = currentDate
		}
		if contentTime == "" {
			contentTime = currentTime
		}
	}
	elements = append(elements,
		derivedio.Str(tagContentDate, core.VRDA, contentDate),
		derivedio.Str(tagContentTime, core.VRTM, contentTime),
	)
	elements = append(elements, measurementGroupsSequence(report.Groups))
	return derivedio.File(sopClassUID, report.SOPInstanceUID, derivedio.Object(elements...))
}

// ReadMeasurementReport deserializes a DICOM measurement report from an object dataset.
// It returns ErrNoDataset if obj is nil, or ErrUnsupportedSRStorage if the dataset's SOP Class UID is not supported.
func ReadMeasurementReport(obj *object.Object) (*MeasurementReport, error) {
	if obj == nil {
		return nil, ErrNoDataset
	}
	dec := &decoder{}
	sopClassUID := dec.string(obj, tagSOPClassUID)
	if dec.err != nil {
		return nil, dec.err
	}
	if !measurementSOPClass(sopClassUID) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSRStorage, sopClassUID)
	}
	report := &MeasurementReport{
		SOPClassUID:         sopClassUID,
		SOPInstanceUID:      dec.string(obj, tagSOPInstanceUID),
		StudyInstanceUID:    dec.string(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:   dec.string(obj, derivedio.TagSeriesInstanceUID),
		FrameOfReferenceUID: dec.string(obj, derivedio.TagFrameOfReferenceUID),
		ContentDate:         dec.string(obj, tagContentDate),
		ContentTime:         dec.string(obj, tagContentTime),
		SeriesNumber:        dec.string(obj, tagSeriesNumber),
		InstanceNumber:      dec.string(obj, tagInstanceNumber),
		CompletionFlag:      dec.string(obj, tagCompletionFlag),
		VerificationFlag:    dec.string(obj, tagVerificationFlag),
		ContinuityOfContent: dec.string(obj, tagContinuityOfContent),
	}
	report.Title, _ = decodeCode(dec, obj, tagConceptNameCodeSeq)
	groups, _ := dec.sequence(obj, tagContentSequence)
	for index, groupObj := range groups {
		group, err := readMeasurementGroup(dec, groupObj)
		if err != nil {
			return nil, fmt.Errorf("measurement group %d: %w", index, err)
		}
		report.Groups = append(report.Groups, group)
	}
	if dec.err != nil {
		return nil, dec.err
	}
	return report, nil
}

// measurementGroupsSequence encodes measurement groups as a DICOM ContentSequence element.
func measurementGroupsSequence(groups []MeasurementGroup) core.Element {
	items := make([]core.DataSet, 0, len(groups))
	for _, group := range groups {
		items = append(items, measurementGroupDataSet(group))
	}
	return derivedio.Seq(tagContentSequence, items...)
}

// measurementGroupDataSet builds a DICOM dataset representing a measurement group.
func measurementGroupDataSet(group MeasurementGroup) core.DataSet {
	children := []core.DataSet{
		textItem(trackingIdentifierConcept, group.Tracking.Identifier),
		uidItem(trackingUniqueIdentifierConcept, group.Tracking.UID),
	}
	if hasSegmentReference(group.ReferencedSegment) {
		children = append(children, segmentItem(group.ReferencedSegment))
	}
	for _, measurement := range group.Measurements {
		children = append(children, measurementItem(measurement))
	}
	return derivedio.DataSet(
		derivedio.CS(tagRelationshipType, RelationshipContains),
		derivedio.CS(tagValueType, string(ValueContainer)),
		derivedio.CS(tagContinuityOfContent, "SEPARATE"),
		codeSequence(tagConceptNameCodeSeq, CodedEntry{CodeValue: "125007", CodingSchemeDesignator: "DCM", CodeMeaning: "Measurement Group"}),
		derivedio.Seq(tagContentSequence, children...),
	)
}

func hasSegmentReference(ref SegmentReference) bool {
	return ref.SOPClassUID != "" || ref.SOPInstanceUID != "" || ref.SegmentNumber != 0
}

// textItem builds a DICOM dataset for textual content with the specified concept and text value.
func textItem(concept CodedEntry, value string) core.DataSet {
	return derivedio.DataSet(
		derivedio.CS(tagRelationshipType, RelationshipContains),
		derivedio.CS(tagValueType, string(ValueText)),
		codeSequence(tagConceptNameCodeSeq, concept),
		derivedio.Str(tagTextValue, core.VRUT, value),
	)
}

// uidItem returns a DICOM dataset representing a UID reference with the given concept and UID value.
func uidItem(concept CodedEntry, value string) core.DataSet {
	return derivedio.DataSet(
		derivedio.CS(tagRelationshipType, RelationshipHasProperties),
		derivedio.CS(tagValueType, "UIDREF"),
		codeSequence(tagConceptNameCodeSeq, concept),
		derivedio.UI(tagUID, value),
	)
}

// segmentItem builds a dataset representing a referenced segment.
func segmentItem(ref SegmentReference) core.DataSet {
	return derivedio.DataSet(
		derivedio.CS(tagRelationshipType, RelationshipInferredFrom),
		derivedio.CS(tagValueType, string(ValueImage)),
		codeSequence(tagConceptNameCodeSeq, referencedSegmentConcept),
		derivedio.Seq(tagRefSOPSequence, derivedio.DataSet(
			derivedio.UI(tagRefSOPClassUID, ref.SOPClassUID),
			derivedio.UI(tagRefSOPInstanceUID, ref.SOPInstanceUID),
			derivedio.IS(tagReferencedSegmentNumber, ref.SegmentNumber),
		)),
	)
}

// measurementItem constructs a DICOM dataset representing a measurement, including its concept name, numeric value and units, and optional image and spatial references.
func measurementItem(measurement ReportMeasurement) core.DataSet {
	children := []core.DataSet{}
	if hasImageReference(measurement.Image) {
		children = append(children, imageItem(measurement.Image))
	}
	if hasSpatialReference(measurement.Spatial) {
		children = append(children, spatialItem(measurement.Spatial))
	}
	elements := []core.Element{
		derivedio.CS(tagRelationshipType, RelationshipContains),
		derivedio.CS(tagValueType, string(ValueNum)),
		codeSequence(tagConceptNameCodeSeq, measurement.ConceptName),
		measuredValueSequence(Measurement{Value: measurement.Value, Units: measurement.Units}),
	}
	if len(children) > 0 {
		elements = append(elements, derivedio.Seq(tagContentSequence, children...))
	}
	return derivedio.DataSet(elements...)
}

func hasImageReference(ref ImageReference) bool {
	return ref.SOPClassUID != "" || ref.SOPInstanceUID != "" || len(ref.Frames) > 0
}

func hasSpatialReference(ref SpatialReference) bool {
	return ref.FrameOfReferenceUID != "" || ref.GraphicType != "" || len(ref.Coordinates) > 0
}

// imageItem returns a dataset representing an image reference.
func imageItem(ref ImageReference) core.DataSet {
	return derivedio.DataSet(
		derivedio.CS(tagRelationshipType, RelationshipInferredFrom),
		derivedio.CS(tagValueType, string(ValueImage)),
		referencedSOPSequence(ref),
	)
}

// spatialItem encodes 3D spatial coordinates as a DICOM dataset.
func spatialItem(ref SpatialReference) core.DataSet {
	elements := append([]core.Element{
		derivedio.CS(tagRelationshipType, RelationshipInferredFrom),
		derivedio.CS(tagValueType, "SCOORD3D"),
	}, spatialCoordinate3DElements(ref)...)
	return derivedio.DataSet(elements...)
}

// ReadMeasurementGroup parses a DICOM dataset to extract a measurement group.
func readMeasurementGroup(dec *decoder, obj *object.Object) (MeasurementGroup, error) {
	var group MeasurementGroup
	children, _ := dec.sequence(obj, tagContentSequence)
	for index, child := range children {
		concept, _ := decodeCode(dec, child, tagConceptNameCodeSeq)
		switch {
		case matchesConcept(concept, trackingIdentifierConcept):
			group.Tracking.Identifier = dec.string(child, tagTextValue)
		case matchesConcept(concept, trackingUniqueIdentifierConcept):
			group.Tracking.UID = dec.string(child, tagUID)
		case matchesConcept(concept, referencedSegmentConcept):
			group.ReferencedSegment = readSegmentReference(dec, child)
		case ValueType(dec.string(child, tagValueType)) == ValueNum:
			measurement, err := readReportMeasurement(dec, child)
			if err != nil {
				return MeasurementGroup{}, fmt.Errorf("measurement item %d: %w", index, err)
			}
			group.Measurements = append(group.Measurements, measurement)
		}
	}
	return group, nil
}

func matchesConcept(got, want CodedEntry) bool {
	if got.CodingSchemeDesignator == want.CodingSchemeDesignator {
		return got.CodeValue == want.CodeValue
	}
	return got.CodeMeaning == want.CodeMeaning
}

// readReportMeasurement extracts measurement data from a DICOM dataset object, including concept name, numeric value and units, and any associated image or spatial references.
func readReportMeasurement(dec *decoder, obj *object.Object) (ReportMeasurement, error) {
	reportMeasurement := ReportMeasurement{}
	reportMeasurement.ConceptName, _ = decodeCode(dec, obj, tagConceptNameCodeSeq)
	if measurement, _, _, ok := readMeasurement(dec, obj); ok {
		reportMeasurement.Value = measurement.Value
		reportMeasurement.Units = measurement.Units
	}
	children, _ := dec.sequence(obj, tagContentSequence)
	for index, child := range children {
		switch ValueType(dec.string(child, tagValueType)) {
		case ValueImage:
			reportMeasurement.Image = readImageReference(dec, child)
		case ValueSCoord3D:
			spatial, err := readSpatialReference(dec, child)
			if err != nil {
				return ReportMeasurement{}, fmt.Errorf("child content item %d: %w", index, err)
			}
			reportMeasurement.Spatial = spatial
		}
	}
	return reportMeasurement, nil
}

// readSegmentReference extracts a segment reference from a DICOM object's Referenced SOP Sequence.
// If the sequence contains no items, it returns a zero-value SegmentReference.
func readSegmentReference(dec *decoder, obj *object.Object) SegmentReference {
	items, _ := dec.sequence(obj, tagRefSOPSequence)
	if len(items) == 0 {
		return SegmentReference{}
	}
	segmentNumbers := dec.ints(items[0], tagReferencedSegmentNumber)
	segmentNumber := 0
	if len(segmentNumbers) > 0 {
		segmentNumber = int(segmentNumbers[0])
	}
	return SegmentReference{
		SOPClassUID:    dec.string(items[0], tagRefSOPClassUID),
		SOPInstanceUID: dec.string(items[0], tagRefSOPInstanceUID),
		SegmentNumber:  segmentNumber,
	}
}

// readSpatialReference decodes DICOM spatial coordinate data into a SpatialReference structure, extracting the graphic type and parsing flattened float values as Point3D coordinates.
func readSpatialReference(dec *decoder, obj *object.Object) (SpatialReference, error) {
	ref := SpatialReference{
		FrameOfReferenceUID: dec.string(obj, tagReferencedFrameOfRefUID),
		GraphicType:         dec.string(obj, tagGraphicType),
		FiducialUID:         dec.string(obj, tagFiducialUID),
	}
	values := dec.floats(obj, tagGraphicData)
	if len(values)%3 != 0 {
		return SpatialReference{}, fmt.Errorf("%w: SCOORD3D has %d values, want complete x/y/z triples", ErrInvalidGraphicData, len(values))
	}
	for i := 0; i+2 < len(values); i += 3 {
		ref.Coordinates = append(ref.Coordinates, Point3D{X: values[i], Y: values[i+1], Z: values[i+2]})
	}
	return ref, nil
}

// conceptMeaning returns the code meaning from the concept name code sequence.
// It returns an empty string if the code cannot be read.
func conceptMeaning(obj *object.Object) string {
	code, ok := readCode(obj, tagConceptNameCodeSeq)
	if !ok {
		return ""
	}
	return code.CodeMeaning
}

// flattenPoint3D converts a slice of Point3D values into a flat float64 slice
// ordered as X1, Y1, Z1, X2, Y2, Z2, and so on.
func flattenPoint3D(points []Point3D) []float64 {
	out := make([]float64, 0, len(points)*3)
	for _, point := range points {
		out = append(out, point.X, point.Y, point.Z)
	}
	return out
}

// measurementSOPClass reports whether uid is a supported measurement SR storage SOP Class UID.
func measurementSOPClass(uid string) bool {
	switch uid {
	case EnhancedSRStorage, ComprehensiveSRStorage, Comprehensive3DSRStorage:
		return true
	default:
		return false
	}
}
