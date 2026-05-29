package sr

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	// ErrNoDataset reports a nil dataset passed to an SR reader.
	ErrNoDataset = errors.New("dicom/sr: nil dataset")
	// ErrInvalidGraphicData reports SCOORD or SCOORD3D Graphic Data whose
	// values cannot be grouped into complete coordinate tuples.
	ErrInvalidGraphicData = errors.New("dicom/sr: invalid Graphic Data")
	// ErrResourceLimitExceeded reports an SR document whose content tree is too
	// deeply nested to decode safely.
	ErrResourceLimitExceeded = errors.New("dicom/sr: resource limit exceeded")
)

const maxContentTreeDepth = 64

// ReadOptions bounds generic SR tree decoding. Reference validation is opt-in;
// the legacy ReadDocument behavior remains permissive for real-world SRs.
type ReadOptions struct {
	MaxDepth          int
	MaxItems          int
	MaxReferences     int
	MaxPathComponents int
	ResolveReferences bool
	ReferenceMode     ValidationMode
	MaxFindings       int
}

// DefaultReadOptions returns bounded, permissive legacy-compatible defaults.
func DefaultReadOptions() ReadOptions {
	return ReadOptions{
		MaxDepth:          maxContentTreeDepth,
		MaxItems:          100_000,
		MaxReferences:     100_000,
		MaxPathComponents: 65,
		ReferenceMode:     ValidationModeWarn,
		MaxFindings:       1_024,
	}
}

// ReadResult includes the preserved document and, when requested, its resolved
// reference graph and value-free diagnostics.
type ReadResult struct {
	Document         *Document
	References       *ReferenceIndex
	Report           ValidationReport
	TemplateMetadata TemplateValidationMetadata
}

func normalizeReadOptions(options ReadOptions) ReadOptions {
	defaults := DefaultReadOptions()
	if options.MaxDepth == 0 {
		options.MaxDepth = defaults.MaxDepth
	}
	if options.MaxItems == 0 {
		options.MaxItems = defaults.MaxItems
	}
	if options.MaxReferences == 0 {
		options.MaxReferences = defaults.MaxReferences
	}
	if options.MaxPathComponents == 0 {
		options.MaxPathComponents = defaults.MaxPathComponents
	}
	if options.ReferenceMode == 0 {
		options.ReferenceMode = defaults.ReferenceMode
	}
	if options.MaxFindings == 0 {
		options.MaxFindings = defaults.MaxFindings
	}
	return options
}

type decoder struct {
	err               error
	maxDepth          int
	maxItems          int
	maxReferences     int
	maxPathComponents int
	itemCount         int
	referenceCount    int
	metadataCount     int
	templateMetadata  TemplateValidationMetadata
}

func (d *decoder) fail(err error) {
	if d.err == nil && err != nil {
		d.err = err
	}
}

func (d *decoder) reserveMetadataItems(count int) bool {
	if count <= 0 {
		return true
	}
	if d.maxItems > 0 && count > d.maxItems-d.metadataCount {
		d.fail(fmt.Errorf("%w: code metadata count exceeds %d", ErrResourceLimitExceeded, d.maxItems))
		return false
	}
	d.metadataCount += count
	return true
}

func (d *decoder) addCodeContexts(contexts ContentItemCodeContexts) {
	if !d.reserveMetadataItems(1) {
		return
	}
	d.templateMetadata.CodeContexts = append(d.templateMetadata.CodeContexts, contexts)
}

func (d *decoder) addTemplateIdentification(identification TemplateIdentification) {
	if !d.reserveMetadataItems(1) {
		return
	}
	d.templateMetadata.TemplateIdentifications = append(d.templateMetadata.TemplateIdentifications, identification)
}

func (d *decoder) string(obj *object.Object, tag core.Tag) string {
	if d.err != nil || obj == nil || !obj.Has(tag) {
		return ""
	}
	value, err := obj.LookupString(tag)
	if err != nil {
		d.fail(fmt.Errorf("decode %s: %w", tag, err))
		return ""
	}
	return clean(value)
}

func (d *decoder) strings(obj *object.Object, tag core.Tag) []string {
	if d.err != nil || obj == nil || !obj.Has(tag) {
		return nil
	}
	values, err := obj.LookupStrings(tag)
	if err != nil {
		d.fail(fmt.Errorf("decode %s: %w", tag, err))
		return nil
	}
	for i := range values {
		values[i] = clean(values[i])
	}
	return values
}

func (d *decoder) sequence(obj *object.Object, tag core.Tag) ([]*object.Object, bool) {
	if d.err != nil || obj == nil {
		return nil, false
	}
	element, present := obj.Get(tag)
	if !present {
		return nil, false
	}
	if element.VR() != core.VRSQ {
		d.fail(fmt.Errorf("decode %s: VR is %s, want SQ", tag, element.VR()))
		return nil, true
	}
	items, ok := obj.GetSequence(tag)
	if !ok {
		d.fail(fmt.Errorf("decode %s: value is not a sequence", tag))
		return nil, true
	}
	return items, true
}

func (d *decoder) ints(obj *object.Object, tag core.Tag) []int64 {
	if d.err != nil || obj == nil || !obj.Has(tag) {
		return nil
	}
	values, err := derivedio.LookupInts(obj, tag)
	if err != nil {
		d.fail(fmt.Errorf("decode %s: %w", tag, err))
		return nil
	}
	return values
}

func (d *decoder) contentItemIdentifier(obj *object.Object) (ContentItemIdentifier, bool) {
	if d.err != nil || obj == nil {
		return nil, false
	}
	element, present := obj.Get(tagReferencedContentItemIdentifier)
	if !present {
		return nil, false
	}
	if element.VR() != core.VRUL {
		d.fail(fmt.Errorf("decode %s: VR is %s, want UL", tagReferencedContentItemIdentifier, element.VR()))
		return nil, true
	}
	if typed, ok := element.Value.(core.Uint32Value); ok {
		return ContentItemIdentifier(append([]uint32(nil), typed...)), true
	}
	values, err := derivedio.LookupInts(obj, tagReferencedContentItemIdentifier)
	if err != nil {
		d.fail(fmt.Errorf("decode %s: %w", tagReferencedContentItemIdentifier, err))
		return nil, true
	}
	identifier := make(ContentItemIdentifier, len(values))
	for index, value := range values {
		if value < 0 || uint64(value) > uint64(^uint32(0)) {
			d.fail(fmt.Errorf("decode %s: component is outside UL", tagReferencedContentItemIdentifier))
			return nil, true
		}
		identifier[index] = uint32(value)
	}
	return identifier, true
}

func (d *decoder) floats(obj *object.Object, tag core.Tag) []float64 {
	if d.err != nil || obj == nil || !obj.Has(tag) {
		return nil
	}
	values, err := derivedio.LookupFloats(obj, tag)
	if err != nil {
		d.fail(fmt.Errorf("decode %s: %w", tag, err))
		return nil
	}
	return values
}

// ReadDocument parses an SR / KOS document from a dataset, recovering the
// header, document title, and the content-item tree.
func ReadDocument(obj *object.Object) (*Document, error) {
	result, err := readDocumentWithOptions(obj, DefaultReadOptions())
	return result.Document, err
}

// ReadDocumentWithOptions parses a document and optionally resolves references
// after the full tree is available. Strict reference failures return the
// preserved Document and Report together with a typed error.
func ReadDocumentWithOptions(obj *object.Object, options ReadOptions) (ReadResult, error) {
	options = normalizeReadOptions(options)
	if options.MaxDepth < 1 || options.MaxItems < 1 || options.MaxReferences < 1 || options.MaxPathComponents < 1 || options.MaxFindings < 1 {
		return ReadResult{}, fmt.Errorf("%w: invalid read limits", ErrResourceLimitExceeded)
	}
	return readDocumentWithOptions(obj, options)
}

func readDocumentWithOptions(obj *object.Object, options ReadOptions) (ReadResult, error) {
	if obj == nil {
		return ReadResult{}, ErrNoDataset
	}
	dec := &decoder{
		maxDepth: options.MaxDepth, maxItems: options.MaxItems,
		maxReferences: options.MaxReferences, maxPathComponents: options.MaxPathComponents,
	}
	doc := &Document{
		SOPClassUID:         dec.string(obj, tagSOPClassUID),
		SOPInstanceUID:      dec.string(obj, tagSOPInstanceUID),
		StudyInstanceUID:    dec.string(obj, tagStudyInstanceUID),
		SeriesInstanceUID:   dec.string(obj, tagSeriesInstanceUID),
		Modality:            dec.string(obj, tagModality),
		ContentDate:         dec.string(obj, tagContentDate),
		ContentTime:         dec.string(obj, tagContentTime),
		SeriesNumber:        dec.string(obj, tagSeriesNumber),
		InstanceNumber:      dec.string(obj, tagInstanceNumber),
		CompletionFlag:      dec.string(obj, tagCompletionFlag),
		VerificationFlag:    dec.string(obj, tagVerificationFlag),
		ContinuityOfContent: dec.string(obj, tagContinuityOfContent),
	}
	rootPath := ContentItemIdentifier{1}
	var rootContexts ContentItemCodeContexts
	rootContexts.Path = rootPath.String()
	doc.Title, rootContexts.ConceptName, rootContexts.ConceptNameEquivalentCodes, _ = decodeCodeWithMetadata(dec, obj, tagConceptNameCodeSeq)
	if !codeContextsEmpty(rootContexts) {
		dec.addCodeContexts(rootContexts)
	}
	if identification, ok := decodeTemplateIdentification(dec, obj, rootPath); ok {
		dec.addTemplateIdentification(identification)
	}
	evidence := readCurrentRequestedProcedureEvidence(dec, obj)
	if items, ok := dec.sequence(obj, tagContentSequence); ok {
		content, err := readContentItems(dec, items, 1, rootPath)
		if err != nil {
			return ReadResult{Document: doc, TemplateMetadata: dec.templateMetadata}, err
		}
		doc.Content = content
		applyEvidenceHierarchy(doc.Content, evidence)
	}
	if dec.err != nil {
		return ReadResult{Document: doc, TemplateMetadata: dec.templateMetadata}, dec.err
	}
	result := ReadResult{Document: doc, TemplateMetadata: cloneTemplateValidationMetadata(dec.templateMetadata)}
	if options.ResolveReferences {
		referenceOptions := DefaultReferenceOptions()
		referenceOptions.Mode = options.ReferenceMode
		referenceOptions.MaxDepth = options.MaxDepth
		referenceOptions.MaxItems = options.MaxItems
		referenceOptions.MaxReferences = options.MaxReferences
		referenceOptions.MaxPathComponents = options.MaxPathComponents
		referenceOptions.MaxFindings = options.MaxFindings
		index, report, err := ResolveReferences(doc, referenceOptions)
		result.References = index
		result.Report = report
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func readContentItems(dec *decoder, items []*object.Object, depth int, parentPath ContentItemIdentifier) ([]ContentItem, error) {
	if depth > dec.maxDepth {
		return nil, fmt.Errorf("%w: content tree depth exceeds %d", ErrResourceLimitExceeded, dec.maxDepth)
	}
	remaining := dec.maxItems - dec.itemCount
	if dec.maxItems > 0 && len(items) > remaining {
		return nil, fmt.Errorf("%w: content item count exceeds %d", ErrResourceLimitExceeded, dec.maxItems)
	}
	out := make([]ContentItem, 0, len(items))
	for index, it := range items {
		itemPath := appendPath(parentPath, uint32(index+1))
		dec.itemCount++
		if dec.itemCount > dec.maxItems {
			return nil, fmt.Errorf("%w: content item count exceeds %d", ErrResourceLimitExceeded, dec.maxItems)
		}
		if it == nil {
			return nil, fmt.Errorf("%w: content item %d is nil", ErrInvalidDocument, index+1)
		}
		ci := ContentItem{
			ValueType:           ValueType(dec.string(it, tagValueType)),
			RelationshipType:    dec.string(it, tagRelationshipType),
			ContinuityOfContent: dec.string(it, tagContinuityOfContent),
		}
		var referenceMacroPresent bool
		ci.ReferencedContentItemIdentifier, referenceMacroPresent = dec.contentItemIdentifier(it)
		ci.Encoding.EmptyReferenceMacroPresent = referenceMacroPresent && len(ci.ReferencedContentItemIdentifier) == 0
		ci.ValueElements = unmodeledValueElements(it, ci.ValueType)
		if referenceMacroPresent {
			ci.Encoding.ByValueMacroPresent = byValueMacroPresent(it)
			dec.referenceCount++
			if dec.referenceCount > dec.maxReferences {
				return nil, fmt.Errorf("%w: reference count exceeds %d", ErrResourceLimitExceeded, dec.maxReferences)
			}
			if len(ci.ReferencedContentItemIdentifier) > dec.maxPathComponents {
				return nil, fmt.Errorf("%w: reference path exceeds %d components", ErrResourceLimitExceeded, dec.maxPathComponents)
			}
		}
		itemContexts := ContentItemCodeContexts{Path: itemPath.String()}
		ci.ConceptName, itemContexts.ConceptName, itemContexts.ConceptNameEquivalentCodes, _ = decodeCodeWithMetadata(dec, it, tagConceptNameCodeSeq)
		if children, ok := dec.sequence(it, tagContentSequence); ok {
			parsedChildren, err := readContentItems(dec, children, depth+1, itemPath)
			if err != nil {
				return nil, fmt.Errorf("content item %d children: %w", index, err)
			}
			ci.Children = parsedChildren
		}
		if referenceMacroPresent {
			if !codeContextsEmpty(itemContexts) {
				dec.addCodeContexts(itemContexts)
			}
			out = append(out, ci)
			continue
		}
		switch ci.ValueType {
		case ValueText:
			ci.Text = dec.string(it, tagTextValue)
		case ValueCode:
			ci.Code, itemContexts.Value, itemContexts.ValueEquivalentCodes, _ = decodeCodeWithMetadata(dec, it, tagConceptCodeSeq)
		case ValueImage:
			ci.Image = readImageReference(dec, it)
		case ValueNum:
			if m, unitsContext, unitEquivalents, ok := readMeasurement(dec, it); ok {
				ci.Measurement = &m
				itemContexts.MeasurementUnits = unitsContext
				itemContexts.MeasurementUnitsEquivalentCodes = unitEquivalents
			}
			ci.NumericValueQualifier, itemContexts.NumericValueQualifier, itemContexts.NumericValueQualifierEquivalents, _ =
				decodeCodeWithMetadata(dec, it, tagNumericValueQualifierCodeSeq)
		case ValueDateTime:
			ci.DateTime = dec.string(it, tagDateTimeValue)
		case ValueDate:
			ci.Date = dec.string(it, tagDateValue)
		case ValueTime:
			ci.Time = dec.string(it, tagTimeValue)
		case ValueUIDRef:
			ci.UID = dec.string(it, tagUID)
		case ValuePName:
			ci.PersonName = dec.string(it, tagPersonNameValue)
		case ValueComposite:
			ci.Composite = readCompositeReference(dec, it)
		case ValueWaveform:
			ci.Waveform = readWaveformReference(dec, it)
		case ValueSCoord:
			spatial, err := readSpatialCoordinates(dec, it)
			if err != nil {
				return nil, fmt.Errorf("content item %d: %w", index, err)
			}
			ci.Spatial = spatial
		case ValueSCoord3D:
			spatial, err := readSpatialReference(dec, it)
			if err != nil {
				return nil, fmt.Errorf("content item %d: %w", index, err)
			}
			ci.Spatial3D = spatial
		case ValueTCoord:
			ci.Temporal = readTemporalCoordinates(dec, it)
		case ValueContainer:
		default:
		}
		if !codeContextsEmpty(itemContexts) {
			dec.addCodeContexts(itemContexts)
		}
		if identification, ok := decodeTemplateIdentification(dec, it, itemPath); ok {
			dec.addTemplateIdentification(identification)
		}
		out = append(out, ci)
	}
	return out, nil
}

type evidenceHierarchy struct {
	studyUID  string
	seriesUID string
}

func readCurrentRequestedProcedureEvidence(dec *decoder, obj *object.Object) map[string]evidenceHierarchy {
	out := map[string]evidenceHierarchy{}
	studies, _ := dec.sequence(obj, tagCurrentRequestedProcedureEvidence)
	for _, study := range studies {
		studyUID := dec.string(study, tagStudyInstanceUID)
		seriesItems, _ := dec.sequence(study, tagReferencedSeriesSequence)
		for _, series := range seriesItems {
			seriesUID := dec.string(series, tagSeriesInstanceUID)
			sopItems, _ := dec.sequence(series, tagRefSOPSequence)
			for _, sop := range sopItems {
				sopInstanceUID := dec.string(sop, tagRefSOPInstanceUID)
				if sopInstanceUID != "" {
					out[sopInstanceUID] = evidenceHierarchy{studyUID: studyUID, seriesUID: seriesUID}
				}
			}
		}
	}
	return out
}

func applyEvidenceHierarchy(items []ContentItem, evidence map[string]evidenceHierarchy) {
	for i := range items {
		if items[i].ValueType == ValueImage {
			if hierarchy, ok := evidence[clean(items[i].Image.SOPInstanceUID)]; ok {
				items[i].Image.StudyInstanceUID = hierarchy.studyUID
				items[i].Image.SeriesInstanceUID = hierarchy.seriesUID
			}
		}
		applyEvidenceHierarchy(items[i].Children, evidence)
	}
}

func decodeCode(dec *decoder, obj *object.Object, tag core.Tag) (CodedEntry, bool) {
	code, _, _, ok := decodeCodeWithMetadata(dec, obj, tag)
	return code, ok
}

func decodeCodeWithContext(dec *decoder, obj *object.Object, tag core.Tag) (CodedEntry, CodeContext, bool) {
	code, codeContext, _, ok := decodeCodeWithMetadata(dec, obj, tag)
	return code, codeContext, ok
}

func decodeCodeWithMetadata(dec *decoder, obj *object.Object, tag core.Tag) (CodedEntry, CodeContext, []EquivalentCodeEntry, bool) {
	items, ok := dec.sequence(obj, tag)
	if !ok || len(items) == 0 || items[0] == nil {
		return CodedEntry{}, CodeContext{}, nil, false
	}
	item := items[0]
	code := CodedEntry{
		CodeValue:              dec.string(item, tagCodeValue),
		CodingSchemeDesignator: dec.string(item, tagCodingScheme),
		CodeMeaning:            dec.string(item, tagCodeMeaning),
	}
	context := decodeCodeContext(item)
	var equivalents []EquivalentCodeEntry
	if equivalentItems, present := item.GetSequence(tagEquivalentCodeSequence); present {
		if !dec.reserveMetadataItems(len(equivalentItems)) {
			return code, context, nil, true
		}
		equivalents = make([]EquivalentCodeEntry, 0, len(equivalentItems))
		for _, equivalent := range equivalentItems {
			if equivalent == nil {
				continue
			}
			equivalents = append(equivalents, EquivalentCodeEntry{
				Code: CodedEntry{
					CodeValue:              cleanString(equivalent, tagCodeValue),
					CodingSchemeDesignator: cleanString(equivalent, tagCodingScheme),
					CodeMeaning:            cleanString(equivalent, tagCodeMeaning),
				},
				Context: decodeCodeContext(equivalent),
			})
		}
	}
	return code, context, equivalents, true
}

func decodeCodeContext(item *object.Object) CodeContext {
	return CodeContext{
		CodingSchemeVersion:      cleanString(item, tagCodingSchemeVersion),
		ContextIdentifier:        cleanString(item, tagContextIdentifier),
		MappingResource:          cleanString(item, tagMappingResource),
		ContextGroupVersion:      cleanString(item, tagContextGroupVersion),
		ContextGroupLocalVersion: cleanString(item, tagContextGroupLocalVersion),
		ExtensionFlag:            cleanString(item, tagContextGroupExtensionFlag),
		ExtensionCreatorUID:      cleanString(item, tagContextGroupExtensionCreatorUID),
		ContextUID:               cleanString(item, tagContextUID),
		MappingResourceUID:       cleanString(item, tagMappingResourceUID),
		MappingResourceName:      cleanString(item, tagMappingResourceName),
		LongCodeValue:            cleanString(item, tagLongCodeValue),
		URNCodeValue:             cleanString(item, tagURNCodeValue),
	}
}

func readCode(obj *object.Object, tag core.Tag) (CodedEntry, bool) {
	return decodeCode(&decoder{}, obj, tag)
}

func readImageReference(dec *decoder, it *object.Object) ImageReference {
	items, ok := dec.sequence(it, tagRefSOPSequence)
	if !ok || len(items) == 0 || items[0] == nil {
		return ImageReference{}
	}
	ref := items[0]
	out := ImageReference{
		SOPClassUID:    dec.string(ref, tagRefSOPClassUID),
		SOPInstanceUID: dec.string(ref, tagRefSOPInstanceUID),
	}
	if frames := dec.ints(ref, tagRefFrameNumber); len(frames) > 0 {
		out.Frames = make([]int, len(frames))
		for i, f := range frames {
			out.Frames[i] = int(f)
		}
	}
	return out
}

func readCompositeReference(dec *decoder, it *object.Object) CompositeReference {
	items, ok := dec.sequence(it, tagRefSOPSequence)
	if !ok || len(items) == 0 || items[0] == nil {
		return CompositeReference{}
	}
	return CompositeReference{
		SOPClassUID:    dec.string(items[0], tagRefSOPClassUID),
		SOPInstanceUID: dec.string(items[0], tagRefSOPInstanceUID),
	}
}

func readWaveformReference(dec *decoder, it *object.Object) WaveformReference {
	items, ok := dec.sequence(it, tagRefSOPSequence)
	if !ok || len(items) == 0 || items[0] == nil {
		return WaveformReference{}
	}
	refItem := items[0]
	out := WaveformReference{CompositeReference: CompositeReference{
		SOPClassUID:    dec.string(refItem, tagRefSOPClassUID),
		SOPInstanceUID: dec.string(refItem, tagRefSOPInstanceUID),
	}}
	channels := dec.ints(refItem, tagReferencedWaveformCh)
	if len(channels)%2 != 0 {
		dec.fail(fmt.Errorf("decode %s: waveform channel list has %d values, want pairs", tagReferencedWaveformCh, len(channels)))
		return out
	}
	for _, value := range channels {
		if value < 0 || value > 1<<16-1 {
			dec.fail(fmt.Errorf("decode %s: waveform channel value %d is outside US", tagReferencedWaveformCh, value))
			return out
		}
	}
	for i := 0; i+1 < len(channels); i += 2 {
		out.Channels = append(out.Channels, WaveformChannel{
			MultiplexGroupNumber: uint16(channels[i]),
			ChannelNumber:        uint16(channels[i+1]),
		})
	}
	return out
}

func readSpatialCoordinates(dec *decoder, it *object.Object) (SpatialCoordinates, error) {
	out := SpatialCoordinates{
		GraphicType:               dec.string(it, tagGraphicType),
		PixelOriginInterpretation: dec.string(it, tagPixelOriginInterpret),
		FiducialUID:               dec.string(it, tagFiducialUID),
	}
	values := dec.floats(it, tagGraphicData)
	if len(values)%2 != 0 {
		return SpatialCoordinates{}, fmt.Errorf("%w: SCOORD has %d values, want complete x/y pairs", ErrInvalidGraphicData, len(values))
	}
	for i := 0; i+1 < len(values); i += 2 {
		out.Coordinates = append(out.Coordinates, Point2D{X: values[i], Y: values[i+1]})
	}
	return out, nil
}

func readTemporalCoordinates(dec *decoder, it *object.Object) TemporalCoordinates {
	out := TemporalCoordinates{RangeType: dec.string(it, tagTemporalRangeType)}
	for _, value := range dec.ints(it, tagReferencedSamples) {
		if value >= 0 && uint64(value) <= uint64(^uint32(0)) {
			out.SamplePositions = append(out.SamplePositions, uint32(value))
		}
	}
	out.TimeOffsets = dec.floats(it, tagReferencedTimeOffset)
	if values := dec.strings(it, tagReferencedDateTime); len(values) > 0 {
		out.DateTimes = make([]string, len(values))
		for i, value := range values {
			out.DateTimes[i] = clean(value)
		}
	}
	return out
}

func unmodeledValueElements(it *object.Object, valueType ValueType) []core.Element {
	if it == nil {
		return nil
	}
	var out []core.Element
	for _, element := range it.Elements() {
		switch element.Tag() {
		case tagRelationshipType, tagValueType, tagConceptNameCodeSeq, tagContentSequence, tagReferencedContentItemIdentifier:
			continue
		}
		if isModeledValueType(valueType) {
			switch element.Tag() {
			case tagTextValue, tagConceptCodeSeq, tagRefSOPSequence, tagMeasuredValueSeq,
				tagNumericValueQualifierCodeSeq, tagDateTimeValue, tagDateValue, tagTimeValue,
				tagUID, tagPersonNameValue, tagGraphicData, tagGraphicType, tagFiducialUID,
				tagPixelOriginInterpret, tagReferencedFrameOfRefUID, tagTemporalRangeType, tagReferencedSamples,
				tagReferencedTimeOffset, tagReferencedDateTime:
				continue
			}
			if valueType == ValueContainer && element.Tag() == tagContinuityOfContent {
				continue
			}
		}
		out = append(out, element)
	}
	return out
}

func byValueMacroPresent(item *object.Object) bool {
	if item == nil {
		return false
	}
	for _, tag := range []core.Tag{
		tagValueType, tagConceptNameCodeSeq, tagContentSequence, tagContinuityOfContent,
		tagTextValue, tagConceptCodeSeq, tagRefSOPSequence, tagMeasuredValueSeq,
		tagNumericValueQualifierCodeSeq, tagDateTimeValue, tagDateValue, tagTimeValue,
		tagUID, tagPersonNameValue, tagGraphicData, tagGraphicType, tagFiducialUID,
		tagPixelOriginInterpret, tagReferencedFrameOfRefUID, tagTemporalRangeType,
		tagReferencedSamples, tagReferencedTimeOffset, tagReferencedDateTime,
	} {
		if item.Has(tag) {
			return true
		}
	}
	return false
}

func readMeasurement(dec *decoder, it *object.Object) (Measurement, CodeContext, []EquivalentCodeEntry, bool) {
	items, ok := dec.sequence(it, tagMeasuredValueSeq)
	if !ok || len(items) == 0 || items[0] == nil {
		return Measurement{}, CodeContext{}, nil, false
	}
	mv := items[0]
	values := dec.floats(mv, tagNumericValue)
	if len(values) == 0 {
		return Measurement{}, CodeContext{}, nil, false
	}
	units, unitsContext, equivalents, _ := decodeCodeWithMetadata(dec, mv, tagMeasurementUnitsCode)
	return Measurement{Value: values[0], Units: units}, unitsContext, equivalents, true
}

func cleanString(obj *object.Object, tag core.Tag) string {
	value, ok := obj.GetString(tag)
	if !ok {
		return ""
	}
	return clean(value)
}
