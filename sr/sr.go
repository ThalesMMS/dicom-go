// Package sr provides typed primitives for DICOM Structured Reporting content.
// It models KOS and SR document headers, content-item trees, common scalar,
// reference, spatial and temporal value types, and numeric measurements.
package sr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

// Well-known SR SOP Class UIDs.
const (
	KeyObjectSelectionDocumentStorage = "1.2.840.10008.5.1.4.1.1.88.59"
	BasicTextSRStorage                = "1.2.840.10008.5.1.4.1.1.88.11"
	ExtensibleSRStorage               = "1.2.840.10008.5.1.4.1.1.88.35"
)

const (
	completionPartial      = "PARTIAL"
	completionComplete     = "COMPLETE"
	verificationUnverified = "UNVERIFIED"
	verificationVerified   = "VERIFIED"
)

var ErrInvalidDocument = errors.New("dicom/sr: invalid document")

// ValueType is the SR content item value type (PS3.3 C.17.3, Value Type
// (0040,A040)).
type ValueType string

const (
	ValueContainer ValueType = "CONTAINER"
	ValueText      ValueType = "TEXT"
	ValueCode      ValueType = "CODE"
	ValueImage     ValueType = "IMAGE"
	ValueNum       ValueType = "NUM"
	ValueDateTime  ValueType = "DATETIME"
	ValueDate      ValueType = "DATE"
	ValueTime      ValueType = "TIME"
	ValueUIDRef    ValueType = "UIDREF"
	ValuePName     ValueType = "PNAME"
	ValueComposite ValueType = "COMPOSITE"
	ValueWaveform  ValueType = "WAVEFORM"
	ValueSCoord    ValueType = "SCOORD"
	ValueSCoord3D  ValueType = "SCOORD3D"
	ValueTCoord    ValueType = "TCOORD"
	ValueTable     ValueType = "TABLE"
)

// Relationship types (Relationship Type (0040,A010)).
const (
	RelationshipContains      = "CONTAINS"
	RelationshipHasObsContext = "HAS OBS CONTEXT"
	RelationshipHasAcqContext = "HAS ACQ CONTEXT"
	RelationshipHasConceptMod = "HAS CONCEPT MOD"
	RelationshipHasProperties = "HAS PROPERTIES"
	RelationshipInferredFrom  = "INFERRED FROM"
	RelationshipSelectedFrom  = "SELECTED FROM"
)

// CodedEntry is a DICOM coded concept: a Code Sequence item with Code Value
// (0008,0100), Coding Scheme Designator (0008,0102), and Code Meaning
// (0008,0104).
type CodedEntry struct {
	CodeValue              string
	CodingSchemeDesignator string
	CodeMeaning            string
}

// Empty reports whether the coded entry carries no code.
func (c CodedEntry) Empty() bool {
	return c.CodeValue == "" && c.CodingSchemeDesignator == "" && c.CodeMeaning == ""
}

// ImageReference references an image SOP instance (a Referenced SOP Sequence
// item).
type ImageReference struct {
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPClassUID       string
	SOPInstanceUID    string
	// Frames are optional referenced frame numbers (1-based) for multi-frame
	// images.
	Frames []int
}

// Measurement is a numeric value with coded units (a NUM content item).
type Measurement struct {
	Value float64
	Units CodedEntry
}

type CompositeReference struct {
	SOPClassUID    string
	SOPInstanceUID string
}

type WaveformChannel struct {
	MultiplexGroupNumber uint16
	ChannelNumber        uint16
}

type WaveformReference struct {
	CompositeReference
	Channels []WaveformChannel
}

type Point2D struct {
	X float64
	Y float64
}

type SpatialCoordinates struct {
	GraphicType               string
	Coordinates               []Point2D
	PixelOriginInterpretation string
	FiducialUID               string
}

type TemporalCoordinates struct {
	RangeType       string
	SamplePositions []uint32
	TimeOffsets     []float64
	DateTimes       []string
}

// ContentItem is one node of the SR content tree.
type ContentItem struct {
	ValueType        ValueType
	RelationshipType string
	ConceptName      CodedEntry

	Text        string         // ValueText
	Code        CodedEntry     // ValueCode (Concept Code Sequence)
	Image       ImageReference // ValueImage (Referenced SOP Sequence)
	Measurement *Measurement   // ValueNum (Measured Value Sequence)
	// NumericValueQualifier qualifies a numeric value or explains why
	// Measurement is nil. A nil measurement defaults to DCM 114010 (Value
	// unknown), as required by the Numeric Measurement Macro.
	NumericValueQualifier CodedEntry
	DateTime              string // ValueDateTime
	Date                  string // ValueDate
	Time                  string // ValueTime
	UID                   string // ValueUIDRef
	PersonName            string // ValuePName
	Composite             CompositeReference
	Waveform              WaveformReference
	Spatial               SpatialCoordinates // ValueSCoord
	Spatial3D             SpatialReference   // ValueSCoord3D
	Temporal              TemporalCoordinates

	// ValueElements preserves payload attributes for newer value types that are
	// not yet modeled (for example TABLE) so read/write does not discard them.
	ValueElements []core.Element

	ContinuityOfContent string
	// ReferencedContentItemIdentifier encodes a by-reference relationship as
	// one-based ordinals from the SR root. When non-empty, the item carries the
	// relationship and this path only; ValueType, ConceptName, value fields and
	// Children belong to the referenced target and must remain empty here.
	ReferencedContentItemIdentifier ContentItemIdentifier
	// Encoding preserves value-free facts needed to diagnose malformed input.
	Encoding ContentItemEncoding
	Children []ContentItem // CONTAINS-related child items
}

// ContentItemEncoding contains structural provenance from the encoded item.
type ContentItemEncoding struct {
	ByValueMacroPresent        bool
	EmptyReferenceMacroPresent bool
}

// Document is a KOS or Basic Text SR document.
type Document struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	Modality            string // "KO" for KOS, "SR" otherwise
	Title               CodedEntry
	ContentDate         string
	ContentTime         string
	SeriesNumber        string
	InstanceNumber      string
	CompletionFlag      string
	VerificationFlag    string
	ContinuityOfContent string
	Content             []ContentItem // children of the root CONTAINER
}

var (
	tagSOPClassUID                       = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID                    = core.NewTag(0x0008, 0x0018)
	tagModality                          = core.NewTag(0x0008, 0x0060)
	tagContentDate                       = core.NewTag(0x0008, 0x0023)
	tagContentTime                       = core.NewTag(0x0008, 0x0033)
	tagStudyInstanceUID                  = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID                 = core.NewTag(0x0020, 0x000E)
	tagSeriesNumber                      = core.NewTag(0x0020, 0x0011)
	tagInstanceNumber                    = core.NewTag(0x0020, 0x0013)
	tagReferencedSeriesSequence          = core.NewTag(0x0008, 0x1115)
	tagRefSOPSequence                    = core.NewTag(0x0008, 0x1199)
	tagRefSOPClassUID                    = core.NewTag(0x0008, 0x1150)
	tagRefSOPInstanceUID                 = core.NewTag(0x0008, 0x1155)
	tagRefFrameNumber                    = core.NewTag(0x0008, 0x1160)
	tagCodeValue                         = core.NewTag(0x0008, 0x0100)
	tagCodingScheme                      = core.NewTag(0x0008, 0x0102)
	tagCodingSchemeVersion               = core.NewTag(0x0008, 0x0103)
	tagCodeMeaning                       = core.NewTag(0x0008, 0x0104)
	tagMappingResource                   = core.NewTag(0x0008, 0x0105)
	tagContextGroupVersion               = core.NewTag(0x0008, 0x0106)
	tagContextGroupLocalVersion          = core.NewTag(0x0008, 0x0107)
	tagContextGroupExtensionFlag         = core.NewTag(0x0008, 0x010B)
	tagContextGroupExtensionCreatorUID   = core.NewTag(0x0008, 0x010D)
	tagContextIdentifier                 = core.NewTag(0x0008, 0x010F)
	tagContextUID                        = core.NewTag(0x0008, 0x0117)
	tagMappingResourceUID                = core.NewTag(0x0008, 0x0118)
	tagLongCodeValue                     = core.NewTag(0x0008, 0x0119)
	tagURNCodeValue                      = core.NewTag(0x0008, 0x0120)
	tagEquivalentCodeSequence            = core.NewTag(0x0008, 0x0121)
	tagMappingResourceName               = core.NewTag(0x0008, 0x0122)
	tagMeasurementUnitsCode              = core.NewTag(0x0040, 0x08EA)
	tagRelationshipType                  = core.NewTag(0x0040, 0xA010)
	tagValueType                         = core.NewTag(0x0040, 0xA040)
	tagConceptNameCodeSeq                = core.NewTag(0x0040, 0xA043)
	tagNumericValue                      = core.NewTag(0x0040, 0xA30A)
	tagMeasuredValueSeq                  = core.NewTag(0x0040, 0xA300)
	tagNumericValueQualifierCodeSeq      = core.NewTag(0x0040, 0xA301)
	tagConceptCodeSeq                    = core.NewTag(0x0040, 0xA168)
	tagTextValue                         = core.NewTag(0x0040, 0xA160)
	tagContentSequence                   = core.NewTag(0x0040, 0xA730)
	tagReferencedContentItemIdentifier   = core.NewTag(0x0040, 0xDB73)
	tagContentTemplateSequence           = core.NewTag(0x0040, 0xA504)
	tagTemplateIdentifier                = core.NewTag(0x0040, 0xDB00)
	tagTemplateVersion                   = core.NewTag(0x0040, 0xDB06)
	tagContinuityOfContent               = core.NewTag(0x0040, 0xA050)
	tagCurrentRequestedProcedureEvidence = core.NewTag(0x0040, 0xA375)
	tagCompletionFlag                    = core.NewTag(0x0040, 0xA491)
	tagVerificationFlag                  = core.NewTag(0x0040, 0xA493)
	tagDateTimeValue                     = core.NewTag(0x0040, 0xA120)
	tagDateValue                         = core.NewTag(0x0040, 0xA121)
	tagTimeValue                         = core.NewTag(0x0040, 0xA122)
	tagPersonNameValue                   = core.NewTag(0x0040, 0xA123)
	tagUID                               = core.NewTag(0x0040, 0xA124)
	tagReferencedWaveformCh              = core.NewTag(0x0040, 0xA0B0)
	tagTemporalRangeType                 = core.NewTag(0x0040, 0xA130)
	tagReferencedSamples                 = core.NewTag(0x0040, 0xA132)
	tagReferencedTimeOffset              = core.NewTag(0x0040, 0xA138)
	tagReferencedDateTime                = core.NewTag(0x0040, 0xA13A)
	tagGraphicData                       = core.NewTag(0x0070, 0x0022)
	tagGraphicType                       = core.NewTag(0x0070, 0x0023)
	tagFiducialUID                       = core.NewTag(0x0070, 0x031A)
	tagPixelOriginInterpret              = core.NewTag(0x0048, 0x0301)
	tagReferencedFrameOfRefUID           = core.NewTag(0x3006, 0x0024)
)

// Elements builds the dataset elements that represent the document: the SR
// header (SOP Class/Instance, Modality, content date/time, document title), the
// root CONTAINER value type, and the content-item tree as a Content Sequence.
func (d *Document) Elements() ([]core.Element, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: document is nil", ErrInvalidDocument)
	}
	if _, _, err := ResolveReferences(d, DefaultReferenceOptions()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	completion, verification := "", ""
	if d.SOPClassUID != KeyObjectSelectionDocumentStorage {
		var err error
		completion, verification, err = validateDocumentState(d.CompletionFlag, d.VerificationFlag)
		if err != nil {
			return nil, err
		}
	}
	refs := contentImageReferences(d.Content)
	if d.SOPClassUID == KeyObjectSelectionDocumentStorage {
		if err := validateEvidenceReferences(refs); err != nil {
			return nil, err
		}
	}
	elems := []core.Element{
		strElem(tagSOPClassUID, core.VRUI, d.SOPClassUID),
		strElem(tagSOPInstanceUID, core.VRUI, d.SOPInstanceUID),
		strElem(tagModality, core.VRCS, d.modality()),
		strElem(tagSeriesNumber, core.VRIS, defaultString(d.SeriesNumber, "1")),
		strElem(tagInstanceNumber, core.VRIS, defaultString(d.InstanceNumber, "1")),
		strElem(tagValueType, core.VRCS, string(ValueContainer)),
		strElem(tagContinuityOfContent, core.VRCS, defaultString(d.ContinuityOfContent, "SEPARATE")),
	}
	if d.SOPClassUID != KeyObjectSelectionDocumentStorage {
		elems = append(elems,
			strElem(tagCompletionFlag, core.VRCS, completion),
			strElem(tagVerificationFlag, core.VRCS, verification),
		)
	}
	if d.StudyInstanceUID != "" {
		elems = append(elems, strElem(tagStudyInstanceUID, core.VRUI, d.StudyInstanceUID))
	}
	if d.SeriesInstanceUID != "" {
		elems = append(elems, strElem(tagSeriesInstanceUID, core.VRUI, d.SeriesInstanceUID))
	}
	contentDate, contentTime := d.ContentDate, d.ContentTime
	if contentDate == "" || contentTime == "" {
		currentDate, currentTime := currentContentDateTime()
		if contentDate == "" {
			contentDate = currentDate
		}
		if contentTime == "" {
			contentTime = currentTime
		}
	}
	elems = append(elems,
		strElem(tagContentDate, core.VRDA, contentDate),
		strElem(tagContentTime, core.VRTM, contentTime),
	)
	if !d.Title.Empty() {
		elems = append(elems, codeSequence(tagConceptNameCodeSeq, d.Title))
	}
	if len(d.Content) > 0 {
		sequence, err := contentSequence(d.Content)
		if err != nil {
			return nil, err
		}
		elems = append(elems, sequence)
	}
	if d.SOPClassUID == KeyObjectSelectionDocumentStorage {
		elems = append(elems, currentRequestedProcedureEvidenceSequence(refs))
	}
	return elems, nil
}

// Dataset builds an in-memory object.Object for the document.
func (d *Document) Dataset() (*object.Object, error) {
	elements, err := d.Elements()
	if err != nil {
		return nil, err
	}
	return object.FromElements(elements, nil), nil
}

func validateDocumentState(completion, verification string) (string, string, error) {
	completion = defaultString(strings.TrimSpace(completion), completionPartial)
	verification = defaultString(strings.TrimSpace(verification), verificationUnverified)
	if completion != completionPartial && completion != completionComplete {
		return "", "", fmt.Errorf("%w: unsupported Completion Flag %q", ErrInvalidDocument, completion)
	}
	if verification != verificationUnverified && verification != verificationVerified {
		return "", "", fmt.Errorf("%w: unsupported Verification Flag %q", ErrInvalidDocument, verification)
	}
	if verification == verificationVerified && completion != completionComplete {
		return "", "", fmt.Errorf("%w: VERIFIED requires COMPLETE", ErrInvalidDocument)
	}
	if verification == verificationVerified {
		return "", "", fmt.Errorf("%w: VERIFIED requires Verifying Observer Sequence, which is not modeled", ErrInvalidDocument)
	}
	return completion, verification, nil
}

func (d *Document) modality() string {
	if d.Modality != "" {
		return d.Modality
	}
	if d.SOPClassUID == KeyObjectSelectionDocumentStorage {
		return "KO"
	}
	return "SR"
}

func contentSequence(items []ContentItem) (core.Element, error) {
	datasets := make([]core.DataSet, len(items))
	for i, item := range items {
		dataSet, err := contentItemDataSet(item)
		if err != nil {
			return core.Element{}, fmt.Errorf("%w: content item %d", err, i+1)
		}
		datasets[i] = dataSet
	}
	return seqElement(tagContentSequence, datasets...), nil
}

func contentItemDataSet(item ContentItem) (core.DataSet, error) {
	if len(item.ReferencedContentItemIdentifier) > 0 {
		if err := validateEncodedReferenceItem(item, DefaultReferenceOptions().MaxPathComponents); err != nil {
			return core.DataSet{}, err
		}
		return core.DataSet{Elements: []core.Element{
			strElem(tagRelationshipType, core.VRCS, item.RelationshipType),
			contentItemIdentifierElement(item.ReferencedContentItemIdentifier),
		}}, nil
	}
	elems := []core.Element{}
	if item.RelationshipType != "" {
		elems = append(elems, strElem(tagRelationshipType, core.VRCS, item.RelationshipType))
	}
	elems = append(elems, strElem(tagValueType, core.VRCS, string(item.ValueType)))
	if !item.ConceptName.Empty() {
		elems = append(elems, codeSequence(tagConceptNameCodeSeq, item.ConceptName))
	}
	switch item.ValueType {
	case ValueText:
		elems = append(elems, strElem(tagTextValue, core.VRUT, item.Text))
	case ValueCode:
		elems = append(elems, codeSequence(tagConceptCodeSeq, item.Code))
	case ValueImage:
		elems = append(elems, referencedSOPSequence(item.Image))
	case ValueNum:
		if item.Measurement != nil {
			elems = append(elems, measuredValueSequence(*item.Measurement))
		} else {
			elems = append(elems, seqElement(tagMeasuredValueSeq))
		}
		qualifier := item.NumericValueQualifier
		if item.Measurement == nil && qualifier.Empty() {
			qualifier = CodedEntry{CodeValue: "114010", CodingSchemeDesignator: "DCM", CodeMeaning: "Value unknown"}
		}
		if !qualifier.Empty() {
			elems = append(elems, codeSequence(tagNumericValueQualifierCodeSeq, qualifier))
		}
	case ValueContainer:
		elems = append(elems, strElem(tagContinuityOfContent, core.VRCS, defaultString(item.ContinuityOfContent, "SEPARATE")))
	case ValueDateTime:
		elems = append(elems, strElem(tagDateTimeValue, core.VRDT, item.DateTime))
	case ValueDate:
		elems = append(elems, strElem(tagDateValue, core.VRDA, item.Date))
	case ValueTime:
		elems = append(elems, strElem(tagTimeValue, core.VRTM, item.Time))
	case ValueUIDRef:
		elems = append(elems, strElem(tagUID, core.VRUI, item.UID))
	case ValuePName:
		elems = append(elems, strElem(tagPersonNameValue, core.VRPN, item.PersonName))
	case ValueComposite:
		elems = append(elems, compositeReferenceSequence(item.Composite))
	case ValueWaveform:
		elems = append(elems, waveformReferenceSequence(item.Waveform))
	case ValueSCoord:
		elems = append(elems, spatialCoordinateElements(item.Spatial)...)
	case ValueSCoord3D:
		elems = append(elems, spatialCoordinate3DElements(item.Spatial3D)...)
	case ValueTCoord:
		elems = append(elems, temporalCoordinateElements(item.Temporal)...)
	default:
		elems = append(elems, item.ValueElements...)
	}
	if isModeledValueType(item.ValueType) {
		elems = append(elems, item.ValueElements...)
	}
	if len(item.Children) > 0 {
		sequence, err := contentSequence(item.Children)
		if err != nil {
			return core.DataSet{}, err
		}
		elems = append(elems, sequence)
	}
	return core.DataSet{Elements: elems}, nil
}

func isModeledValueType(valueType ValueType) bool {
	switch valueType {
	case ValueText, ValueCode, ValueImage, ValueNum, ValueContainer, ValueDateTime, ValueDate, ValueTime,
		ValueUIDRef, ValuePName, ValueComposite, ValueWaveform, ValueSCoord, ValueSCoord3D, ValueTCoord:
		return true
	default:
		return false
	}
}

func contentItemIdentifierElement(identifier ContentItemIdentifier) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tagReferencedContentItemIdentifier, VR: core.VRUL},
		Value:  core.Uint32Value(identifier.Clone()),
	}
}

func codeSequence(tag core.Tag, code CodedEntry) core.Element {
	item := core.DataSet{Elements: []core.Element{
		strElem(tagCodeValue, core.VRSH, code.CodeValue),
		strElem(tagCodingScheme, core.VRSH, code.CodingSchemeDesignator),
		strElem(tagCodeMeaning, core.VRLO, code.CodeMeaning),
	}}
	return seqElement(tag, item)
}

func referencedSOPSequence(ref ImageReference) core.Element {
	elems := referencedSOPElements(ref.SOPClassUID, ref.SOPInstanceUID)
	if len(ref.Frames) > 0 {
		parts := make([]string, len(ref.Frames))
		for i, f := range ref.Frames {
			parts[i] = strconv.Itoa(f)
		}
		elems = append(elems, core.Element{
			Header: core.ElementHeader{Tag: tagRefFrameNumber, VR: core.VRIS},
			Value:  core.StringValue(parts),
		})
	}
	return seqElement(tagRefSOPSequence, core.DataSet{Elements: elems})
}

func compositeReferenceSequence(ref CompositeReference) core.Element {
	return seqElement(tagRefSOPSequence, core.DataSet{Elements: referencedSOPElements(ref.SOPClassUID, ref.SOPInstanceUID)})
}

func waveformReferenceSequence(ref WaveformReference) core.Element {
	elems := referencedSOPElements(ref.SOPClassUID, ref.SOPInstanceUID)
	if len(ref.Channels) > 0 {
		channels := make([]uint16, 0, len(ref.Channels)*2)
		for _, channel := range ref.Channels {
			channels = append(channels, channel.MultiplexGroupNumber, channel.ChannelNumber)
		}
		elems = append(elems, uint16Element(tagReferencedWaveformCh, channels...))
	}
	return seqElement(tagRefSOPSequence, core.DataSet{Elements: elems})
}

func referencedSOPElements(sopClassUID, sopInstanceUID string) []core.Element {
	return []core.Element{
		strElem(tagRefSOPClassUID, core.VRUI, sopClassUID),
		strElem(tagRefSOPInstanceUID, core.VRUI, sopInstanceUID),
	}
}

func spatialCoordinateElements(spatial SpatialCoordinates) []core.Element {
	elements := []core.Element{
		strElem(tagGraphicType, core.VRCS, spatial.GraphicType),
		float32Element(tagGraphicData, flattenPoint2D(spatial.Coordinates)...),
	}
	if spatial.PixelOriginInterpretation != "" {
		elements = append(elements, strElem(tagPixelOriginInterpret, core.VRCS, spatial.PixelOriginInterpretation))
	}
	if spatial.FiducialUID != "" {
		elements = append(elements, strElem(tagFiducialUID, core.VRUI, spatial.FiducialUID))
	}
	return elements
}

func spatialCoordinate3DElements(spatial SpatialReference) []core.Element {
	elements := []core.Element{
		strElem(tagReferencedFrameOfRefUID, core.VRUI, spatial.FrameOfReferenceUID),
		strElem(tagGraphicType, core.VRCS, spatial.GraphicType),
		float32Element(tagGraphicData, flattenPoint3D(spatial.Coordinates)...),
	}
	if spatial.FiducialUID != "" {
		elements = append(elements, strElem(tagFiducialUID, core.VRUI, spatial.FiducialUID))
	}
	return elements
}

func temporalCoordinateElements(temporal TemporalCoordinates) []core.Element {
	elements := []core.Element{strElem(tagTemporalRangeType, core.VRCS, temporal.RangeType)}
	if len(temporal.SamplePositions) > 0 {
		elements = append(elements, uint32Element(tagReferencedSamples, temporal.SamplePositions...))
	}
	if len(temporal.TimeOffsets) > 0 {
		elements = append(elements, derivedio.DS(tagReferencedTimeOffset, temporal.TimeOffsets...))
	}
	if len(temporal.DateTimes) > 0 {
		elements = append(elements, core.Element{Header: core.ElementHeader{Tag: tagReferencedDateTime, VR: core.VRDT}, Value: core.StringValue(append([]string(nil), temporal.DateTimes...))})
	}
	return elements
}

func flattenPoint2D(points []Point2D) []float64 {
	out := make([]float64, 0, len(points)*2)
	for _, point := range points {
		out = append(out, point.X, point.Y)
	}
	return out
}

func float32Element(tag core.Tag, values ...float64) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(float32(value)))
	}
	return core.NewRawElement(tag, core.VRFL, raw)
}

func uint16Element(tag core.Tag, values ...uint16) core.Element {
	raw := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(raw[i*2:], value)
	}
	return core.NewRawElement(tag, core.VRUS, raw)
}

func uint32Element(tag core.Tag, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], value)
	}
	return core.NewRawElement(tag, core.VRUL, raw)
}

func measuredValueSequence(m Measurement) core.Element {
	item := core.DataSet{Elements: []core.Element{
		derivedio.DS(tagNumericValue, m.Value),
		codeSequence(tagMeasurementUnitsCode, m.Units),
	}}
	return seqElement(tagMeasuredValueSeq, item)
}

func contentImageReferences(items []ContentItem) []ImageReference {
	var refs []ImageReference
	for _, item := range items {
		if item.ValueType == ValueImage {
			refs = append(refs, item.Image)
		}
		refs = append(refs, contentImageReferences(item.Children)...)
	}
	return refs
}

func currentRequestedProcedureEvidenceSequence(refs []ImageReference) core.Element {
	type seriesEvidence struct {
		uid  string
		refs []ImageReference
		seen map[string]struct{}
	}
	type studyEvidence struct {
		uid         string
		series      []*seriesEvidence
		seriesIndex map[string]*seriesEvidence
	}

	studies := make([]*studyEvidence, 0)
	studyIndex := map[string]*studyEvidence{}
	for _, ref := range refs {
		studyUID := clean(ref.StudyInstanceUID)
		seriesUID := clean(ref.SeriesInstanceUID)
		sopClassUID := clean(ref.SOPClassUID)
		sopInstanceUID := clean(ref.SOPInstanceUID)
		study := studyIndex[studyUID]
		if study == nil {
			study = &studyEvidence{uid: studyUID, seriesIndex: map[string]*seriesEvidence{}}
			studyIndex[studyUID] = study
			studies = append(studies, study)
		}
		series := study.seriesIndex[seriesUID]
		if series == nil {
			series = &seriesEvidence{uid: seriesUID, seen: map[string]struct{}{}}
			study.seriesIndex[seriesUID] = series
			study.series = append(study.series, series)
		}
		if _, duplicate := series.seen[sopInstanceUID]; duplicate {
			continue
		}
		series.seen[sopInstanceUID] = struct{}{}
		series.refs = append(series.refs, ImageReference{SOPClassUID: sopClassUID, SOPInstanceUID: sopInstanceUID})
	}

	studyItems := make([]core.DataSet, 0, len(studies))
	for _, study := range studies {
		seriesItems := make([]core.DataSet, 0, len(study.series))
		for _, series := range study.series {
			sopItems := make([]core.DataSet, 0, len(series.refs))
			for _, ref := range series.refs {
				sopItems = append(sopItems, core.DataSet{Elements: referencedSOPElements(ref.SOPClassUID, ref.SOPInstanceUID)})
			}
			seriesItems = append(seriesItems, core.DataSet{Elements: []core.Element{
				strElem(tagSeriesInstanceUID, core.VRUI, series.uid),
				seqElement(tagRefSOPSequence, sopItems...),
			}})
		}
		studyItems = append(studyItems, core.DataSet{Elements: []core.Element{
			strElem(tagStudyInstanceUID, core.VRUI, study.uid),
			seqElement(tagReferencedSeriesSequence, seriesItems...),
		}})
	}
	return seqElement(tagCurrentRequestedProcedureEvidence, studyItems...)
}

func validateEvidenceReferences(refs []ImageReference) error {
	for i, ref := range refs {
		if clean(ref.StudyInstanceUID) == "" || clean(ref.SeriesInstanceUID) == "" ||
			clean(ref.SOPClassUID) == "" || clean(ref.SOPInstanceUID) == "" {
			return fmt.Errorf("%w: evidence reference %d requires study, series, SOP class, and SOP instance UIDs", ErrInvalidDocument, i)
		}
	}
	return nil
}

func strElem(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
}

func seqElement(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: items},
	}
}

func clean(value string) string {
	return strings.Trim(strings.TrimSpace(value), "\x00")
}

func defaultString(value, fallback string) string {
	if value = clean(value); value != "" {
		return value
	}
	return fallback
}

func currentContentDateTime() (string, string) {
	now := time.Now()
	return now.Format("20060102"), now.Format("150405.000000")
}
