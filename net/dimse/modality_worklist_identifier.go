package dimse

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomencoding "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/validation"
)

var ErrModalityWorklistIdentifier = errors.New("dicom dimse: MWL Identifier does not match SOP Class")

// ParsedModalityWorklistIdentifier is a validated typed MWL Identifier. Its
// private request projection is retained so result datasets can include only
// attributes requested by the SCU.
type ParsedModalityWorklistIdentifier struct {
	Query                   ModalityWorklistQuery
	UnsupportedOptionalKeys []core.Tag

	requestedTopLevel []core.Tag
	requestedStep     []core.Tag
}

// ParseModalityWorklistIdentifier validates and decodes the MWL profile
// supported by this package. Values are detached from identifier.
func ParseModalityWorklistIdentifier(identifier *object.Object) (ParsedModalityWorklistIdentifier, error) {
	if identifier == nil {
		return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(core.Tag{}, "missing Identifier")
	}
	parsed := ParsedModalityWorklistIdentifier{}
	for _, element := range identifier.Elements() {
		tag := element.Tag()
		switch tag {
		case tagMWLSpecificCharacterSet:
			if element.VR() != core.VRCS {
				return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(tag, "unexpected VR")
			}
			values, ok := identifier.GetStrings(tag)
			if !ok || len(values) == 0 {
				return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(tag, "invalid character set")
			}
			parsed.Query.SpecificCharacterSet = append([]string(nil), values...)
		case tagMWLTimezoneOffsetFromUTC:
			if element.VR() != core.VRSH {
				return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(tag, "unexpected VR")
			}
			values := element.StringValues()
			if len(values) != 1 || validateMWLTimezoneOffset(values[0]) != nil {
				return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(tag, "invalid timezone offset")
			}
			parsed.Query.TimezoneOffsetFromUTC = values[0]
		case tagMWLPatientName:
			key, err := parseMWLKey(identifier, tag, core.VRPN, mwlMatchWildcard)
			if err != nil {
				return ParsedModalityWorklistIdentifier{}, err
			}
			parsed.Query.PatientName = key
			parsed.requestedTopLevel = append(parsed.requestedTopLevel, tag)
		case tagMWLPatientID:
			key, err := parseMWLKey(identifier, tag, core.VRLO, mwlMatchSingle)
			if err != nil {
				return ParsedModalityWorklistIdentifier{}, err
			}
			parsed.Query.PatientID = key
			parsed.requestedTopLevel = append(parsed.requestedTopLevel, tag)
		case tagMWLAccessionNumber:
			key, err := parseMWLKey(identifier, tag, core.VRSH, mwlMatchSingle)
			if err != nil {
				return ParsedModalityWorklistIdentifier{}, err
			}
			parsed.Query.AccessionNumber = key
			parsed.requestedTopLevel = append(parsed.requestedTopLevel, tag)
		case tagMWLRequestedProcedureID:
			key, err := parseMWLKey(identifier, tag, core.VRSH, mwlMatchSingle)
			if err != nil {
				return ParsedModalityWorklistIdentifier{}, err
			}
			parsed.Query.RequestedProcedureID = key
			parsed.requestedTopLevel = append(parsed.requestedTopLevel, tag)
		case tagMWLRequestedProcedureDescription:
			key, err := parseMWLKey(identifier, tag, core.VRLO, mwlMatchSingle)
			if err != nil {
				return ParsedModalityWorklistIdentifier{}, err
			}
			parsed.Query.RequestedProcedureDescription = key
			parsed.requestedTopLevel = append(parsed.requestedTopLevel, tag)
		case tagMWLScheduledProcedureStepSequence:
			step, requested, unsupported, err := parseMWLScheduledProcedureStep(identifier, element)
			if err != nil {
				return ParsedModalityWorklistIdentifier{}, err
			}
			parsed.Query.ScheduledProcedureStep = step
			parsed.requestedStep = requested
			parsed.UnsupportedOptionalKeys = append(parsed.UnsupportedOptionalKeys, unsupported...)
		default:
			return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(tag, "attribute is outside supported MWL model")
		}
	}
	if parsed.Query.TimezoneOffsetFromUTC != "" && !modalityWorklistQueryHasTemporalKey(parsed.Query) {
		return ParsedModalityWorklistIdentifier{}, mwlIdentifierError(tagMWLTimezoneOffsetFromUTC, "timezone offset requires a DA or TM key")
	}
	return parsed, nil
}

func parseMWLScheduledProcedureStep(identifier *object.Object, element core.Element) (*ModalityWorklistScheduledProcedureStep, []core.Tag, []core.Tag, error) {
	if element.VR() != core.VRSQ {
		return nil, nil, nil, mwlIdentifierError(element.Tag(), "unexpected VR")
	}
	items, ok := identifier.GetSequence(tagMWLScheduledProcedureStepSequence)
	if !ok || len(items) > 1 {
		return nil, nil, nil, mwlIdentifierError(element.Tag(), "sequence requires zero or one item")
	}
	step := &ModalityWorklistScheduledProcedureStep{}
	if len(items) == 0 {
		return step, nil, nil, nil
	}
	item := items[0]
	requested := make([]core.Tag, 0, item.Len())
	unsupported := make([]core.Tag, 0, 1)
	for _, nested := range item.Elements() {
		tag := nested.Tag()
		var (
			key MWLKey
			err error
		)
		switch tag {
		case tagMWLScheduledStationAETitle:
			key, err = parseMWLKey(item, tag, core.VRAE, mwlMatchSingle)
			step.ScheduledStationAETitle = key
		case tagMWLModality:
			key, err = parseMWLKey(item, tag, core.VRCS, mwlMatchSingle)
			step.Modality = key
		case tagMWLScheduledProcedureStepStartDate:
			key, err = parseMWLKey(item, tag, core.VRDA, mwlMatchDateRange)
			step.ScheduledProcedureStepStartDate = key
		case tagMWLScheduledProcedureStepStartTime:
			key, err = parseMWLKey(item, tag, core.VRTM, mwlMatchTimeRange)
			step.ScheduledProcedureStepStartTime = key
		case tagMWLScheduledPerformingPhysicianName:
			key, err = parseMWLKey(item, tag, core.VRPN, mwlMatchWildcard)
			step.ScheduledPerformingPhysicianName = key
		case tagMWLScheduledProcedureStepDescription:
			key, err = parseMWLKey(item, tag, core.VRLO, mwlMatchSingle)
			step.ScheduledProcedureStepDescription = key
		case tagMWLScheduledProcedureStepID:
			key, err = parseMWLKey(item, tag, core.VRSH, mwlMatchSingle)
			step.ScheduledProcedureStepID = key
		case tagMWLScheduledStationName:
			key, err = parseMWLKey(item, tag, core.VRSH, mwlMatchSingle)
			step.ScheduledStationName = key
		case tagMWLScheduledProcedureStepLocation:
			key, err = parseMWLKey(item, tag, core.VRSH, mwlMatchSingle)
			step.ScheduledProcedureStepLocation = key
		case tagMWLScheduledProtocolCodeSequence:
			if nested.VR() != core.VRSQ {
				return nil, nil, nil, mwlIdentifierError(tag, "unexpected VR")
			}
			unsupported = append(unsupported, tag)
			requested = append(requested, tag)
			continue
		default:
			return nil, nil, nil, mwlIdentifierError(tag, "nested attribute is outside supported MWL model")
		}
		if err != nil {
			return nil, nil, nil, err
		}
		requested = append(requested, tag)
	}
	return step, requested, unsupported, nil
}

func modalityWorklistPendingStatus(parsed ParsedModalityWorklistIdentifier, candidate *object.Object) uint16 {
	for _, unsupported := range parsed.UnsupportedOptionalKeys {
		nested := false
		for _, requested := range parsed.requestedStep {
			if requested == unsupported {
				nested = true
				break
			}
		}
		if !nested {
			if candidate == nil || !candidate.Has(unsupported) {
				return StatusPendingWarning
			}
			continue
		}
		sequenceElement, ok := candidate.Get(tagMWLScheduledProcedureStepSequence)
		sequence, sequenceOK := sequenceElement.Value.(core.SequenceValue)
		if !ok || !sequenceOK || len(sequence.Items) != 1 {
			return StatusPendingWarning
		}
		if _, present := modalityWorklistDataSetElement(sequence.Items[0], unsupported); !present {
			return StatusPendingWarning
		}
	}
	return StatusPending
}

func parseMWLKey(source *object.Object, tag core.Tag, wantVR core.VR, kind mwlMatchingKind) (MWLKey, error) {
	element, ok := source.Get(tag)
	if !ok || element.VR() != wantVR {
		return MWLKey{}, mwlIdentifierError(tag, "unexpected VR")
	}
	values, ok := source.GetStrings(tag)
	if !ok {
		return MWLKey{}, mwlIdentifierError(tag, "invalid string value")
	}
	if len(values) == 0 {
		values = []string{""}
	}
	key := MWLMatch(values...)
	if _, err := appendMWLKey(nil, tag, wantVR, key, false, kind); err != nil {
		return MWLKey{}, mwlIdentifierError(tag, "invalid matching form")
	}
	return key, nil
}

func mwlIdentifierError(tag core.Tag, reason string) error {
	if tag == (core.Tag{}) {
		return fmt.Errorf("%w: %s", ErrModalityWorklistIdentifier, reason)
	}
	return fmt.Errorf("%w: attribute %s: %s", ErrModalityWorklistIdentifier, tag, reason)
}

// ProjectModalityWorklistResult builds a detached pending response Identifier
// containing only attributes requested by parsed and the permitted Specific
// Character Set declaration.
func ProjectModalityWorklistResult(parsed ParsedModalityWorklistIdentifier, candidate *object.Object) (*object.Object, error) {
	return projectModalityWorklistResultWithLimits(parsed, candidate, defaultModalityWorklistResponseElements, MaxIdentifierBytes, defaultModalityWorklistResponseDepth)
}

func projectModalityWorklistResultWithLimits(parsed ParsedModalityWorklistIdentifier, candidate *object.Object, maxElements int, maxBytes int64, maxDepth int) (*object.Object, error) {
	if candidate == nil {
		return nil, fmt.Errorf("dicom dimse: nil MWL result")
	}
	if maxElements <= 0 || maxBytes <= 0 || maxDepth <= 0 {
		return nil, ErrModalityWorklistResourceLimit
	}
	characterSet, characterSetPresent := candidate.Get(tagMWLSpecificCharacterSet)
	var characterSetDeclaration *core.Element
	if characterSetPresent {
		characterSetDeclaration = &characterSet
	}
	budget := modalityWorklistCloneBudget{
		remainingElements: maxElements,
		remainingBytes:    maxBytes,
		maxDepth:          maxDepth,
		maxMultiplicity:   maxElements,
		characterSet:      characterSetDeclaration,
	}
	elements := make([]core.Element, 0, len(parsed.requestedTopLevel)+2)
	timezone, timezonePresent := candidate.Get(tagMWLTimezoneOffsetFromUTC)
	if timezonePresent && parsed.Query.TimezoneOffsetFromUTC != "" {
		values := timezone.StringValues()
		if len(values) != 1 || values[0] != parsed.Query.TimezoneOffsetFromUTC {
			return nil, fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
	}
	for _, tag := range parsed.requestedTopLevel {
		if element, ok := candidate.Get(tag); ok {
			cloned, err := budget.cloneElement(element, 0)
			if err != nil {
				return nil, err
			}
			elements = append(elements, cloned)
		} else if !parsed.modalityWorklistUnsupported(tag) {
			return nil, fmt.Errorf("dicom dimse: MWL result is missing requested attribute %s", tag)
		}
	}
	if parsed.Query.ScheduledProcedureStep != nil {
		sequenceElement, ok := candidate.Get(tagMWLScheduledProcedureStepSequence)
		sequence, sequenceOK := sequenceElement.Value.(core.SequenceValue)
		if !ok || !sequenceOK || len(sequence.Items) != 1 {
			return nil, fmt.Errorf("dicom dimse: MWL result requires one Scheduled Procedure Step item")
		}
		item := sequence.Items[0]
		if err := budget.consumeElement(); err != nil {
			return nil, err
		}
		if maxDepth < 2 {
			return nil, ErrModalityWorklistResourceLimit
		}
		if err := budget.consumeElement(); err != nil {
			return nil, err
		}
		requested := parsed.requestedStep
		if len(requested) == 0 {
			if len(item.Elements) > budget.remainingElements {
				return nil, ErrModalityWorklistResourceLimit
			}
		}
		capacity := len(requested)
		if len(requested) == 0 {
			capacity = len(item.Elements)
		}
		itemElements := make([]core.Element, 0, capacity)
		if len(requested) == 0 {
			for _, element := range item.Elements {
				cloned, err := budget.cloneElement(element, 2)
				if err != nil {
					return nil, err
				}
				itemElements = append(itemElements, cloned)
			}
		} else {
			for _, tag := range requested {
				if element, ok := modalityWorklistDataSetElement(item, tag); ok {
					cloned, err := budget.cloneElement(element, 2)
					if err != nil {
						return nil, err
					}
					itemElements = append(itemElements, cloned)
				} else if !parsed.modalityWorklistUnsupported(tag) {
					return nil, fmt.Errorf("dicom dimse: MWL result is missing requested nested attribute %s", tag)
				}
			}
		}
		projectedItem := core.DataSet{Elements: itemElements}
		if len(requested) == 0 {
			if err := validateUniversalModalityWorklistStep(projectedItem, characterSetDeclaration); err != nil {
				return nil, err
			}
		} else if modalityWorklistRequiresStepDescriptionAlternative(requested) {
			if err := validateModalityWorklistStepDescriptionAlternative(projectedItem, characterSetDeclaration); err != nil {
				return nil, err
			}
		}
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{projectedItem}},
		})
	}
	if modalityWorklistResponseContainsTemporalAttribute(elements) {
		if !timezonePresent && parsed.Query.TimezoneOffsetFromUTC != "" {
			timezone = core.Element{Header: core.ElementHeader{Tag: tagMWLTimezoneOffsetFromUTC, VR: core.VRSH}, Value: core.StringValue{parsed.Query.TimezoneOffsetFromUTC}}
			timezonePresent = true
		}
		if timezonePresent {
			cloned, err := budget.cloneElement(timezone, 0)
			if err != nil {
				return nil, err
			}
			elements = append([]core.Element{cloned}, elements...)
		}
	}
	if modalityWorklistResponseNeedsCharacterSet(elements) {
		if !characterSetPresent {
			return nil, fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
		cloned, err := budget.cloneElement(characterSet, 0)
		if err != nil {
			return nil, err
		}
		elements = append([]core.Element{cloned}, elements...)
	}
	return object.FromElements(elements, std.Dictionary), nil
}

func modalityWorklistDataSetElement(dataset core.DataSet, tag core.Tag) (core.Element, bool) {
	for index := len(dataset.Elements) - 1; index >= 0; index-- {
		if dataset.Elements[index].Tag() == tag {
			return dataset.Elements[index], true
		}
	}
	return core.Element{}, false
}

func validateUniversalModalityWorklistStep(item core.DataSet, characterSet *core.Element) error {
	typeOne := []core.Tag{tagMWLScheduledStationAETitle, tagMWLScheduledProcedureStepStartDate, tagMWLScheduledProcedureStepStartTime, tagMWLModality, tagMWLScheduledProcedureStepID}
	typeTwo := []core.Tag{tagMWLScheduledPerformingPhysicianName, tagMWLScheduledStationName, tagMWLScheduledProcedureStepLocation}
	for _, tag := range append(typeOne, typeTwo...) {
		element, ok := modalityWorklistDataSetElement(item, tag)
		if !ok || validateModalityWorklistResultElementWithCharacterSet(element, characterSet) != nil {
			return fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
	}
	return validateModalityWorklistStepDescriptionAlternative(item, characterSet)
}

func modalityWorklistRequiresStepDescriptionAlternative(requested []core.Tag) bool {
	for _, tag := range requested {
		if tag == tagMWLScheduledProcedureStepDescription {
			return true
		}
	}
	return false
}

func validateModalityWorklistStepDescriptionAlternative(item core.DataSet, characterSet *core.Element) error {
	description, descriptionOK := modalityWorklistDataSetElement(item, tagMWLScheduledProcedureStepDescription)
	protocol, protocolOK := modalityWorklistDataSetElement(item, tagMWLScheduledProtocolCodeSequence)
	descriptionPresent := descriptionOK && validateModalityWorklistResultElementWithCharacterSet(description, characterSet) == nil && len(description.StringValues()) == 1 && strings.TrimSpace(description.StringValues()[0]) != ""
	protocolPresent := protocolOK && validateModalityWorklistResultElementWithCharacterSet(protocol, characterSet) == nil
	if !descriptionPresent && !protocolPresent {
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	return nil
}

func modalityWorklistResponseContainsTemporalAttribute(elements []core.Element) bool {
	for _, element := range elements {
		if element.Tag() == tagMWLScheduledProcedureStepStartTime {
			return true
		}
		if sequence, ok := element.Value.(core.SequenceValue); ok {
			for _, item := range sequence.Items {
				if modalityWorklistResponseContainsTemporalAttribute(item.Elements) {
					return true
				}
			}
		}
	}
	return false
}

func modalityWorklistResponseNeedsCharacterSet(elements []core.Element) bool {
	for _, element := range elements {
		if element.Tag() == tagMWLSpecificCharacterSet {
			continue
		}
		switch value := element.Value.(type) {
		case core.StringValue:
			for _, text := range value {
				var err error
				if element.VR() == core.VRPN {
					_, err = dicomencoding.DefaultCharacterSet.EncodePersonName(text)
				} else {
					_, err = dicomencoding.DefaultCharacterSet.Encode(text)
				}
				if err != nil {
					return true
				}
			}
		case core.RawValue:
			for _, octet := range value {
				if octet == 0x1b || octet >= 0x80 {
					return true
				}
			}
		case core.SequenceValue:
			for _, item := range value.Items {
				if modalityWorklistResponseNeedsCharacterSet(item.Elements) {
					return true
				}
			}
		}
	}
	return false
}

type modalityWorklistCloneBudget struct {
	remainingElements int
	remainingBytes    int64
	maxDepth          int
	maxMultiplicity   int
	characterSet      *core.Element
}

func (budget *modalityWorklistCloneBudget) consumeElement() error {
	if budget == nil || budget.remainingElements <= 0 {
		return ErrModalityWorklistResourceLimit
	}
	budget.remainingElements--
	return nil
}

func (budget *modalityWorklistCloneBudget) consumeBytes(length int64) error {
	if budget == nil || length < 0 || length > budget.remainingBytes {
		return ErrModalityWorklistResourceLimit
	}
	budget.remainingBytes -= length
	return nil
}

func (budget *modalityWorklistCloneBudget) cloneElement(element core.Element, depth int) (core.Element, error) {
	if err := budget.consumeElement(); err != nil {
		return core.Element{}, err
	}
	if err := budget.reservePrimitiveValue(element.Value); err != nil {
		return core.Element{}, err
	}
	if err := validateModalityWorklistResultElementWithCharacterSet(element, budget.characterSet); err != nil {
		return core.Element{}, err
	}
	clone := element
	switch value := element.Value.(type) {
	case core.StringValue:
		clone.Value = core.StringValue(append([]string(nil), value...))
	case core.RawValue:
		clone.Value = core.RawValue(core.CloneBytes(value))
	case core.Uint16Value:
		clone.Value = append(core.Uint16Value(nil), value...)
	case core.Int16Value:
		clone.Value = append(core.Int16Value(nil), value...)
	case core.Uint32Value:
		clone.Value = append(core.Uint32Value(nil), value...)
	case core.Int32Value:
		clone.Value = append(core.Int32Value(nil), value...)
	case core.Uint64Value:
		clone.Value = append(core.Uint64Value(nil), value...)
	case core.Int64Value:
		clone.Value = append(core.Int64Value(nil), value...)
	case core.Float32Value:
		clone.Value = append(core.Float32Value(nil), value...)
	case core.Float64Value:
		clone.Value = append(core.Float64Value(nil), value...)
	case core.TagValue:
		clone.Value = append(core.TagValue(nil), value...)
	case core.SequenceValue:
		if depth+1 > budget.maxDepth {
			return core.Element{}, ErrModalityWorklistResourceLimit
		}
		if len(value.Items) > budget.remainingElements {
			return core.Element{}, ErrModalityWorklistResourceLimit
		}
		items := make([]core.DataSet, len(value.Items))
		for i, item := range value.Items {
			if depth+2 > budget.maxDepth {
				return core.Element{}, ErrModalityWorklistResourceLimit
			}
			if err := budget.consumeElement(); err != nil {
				return core.Element{}, err
			}
			if len(item.Elements) > budget.remainingElements {
				return core.Element{}, ErrModalityWorklistResourceLimit
			}
			elements := make([]core.Element, len(item.Elements))
			for j, nested := range item.Elements {
				cloned, err := budget.cloneElement(nested, depth+2)
				if err != nil {
					return core.Element{}, err
				}
				elements[j] = cloned
			}
			items[i] = core.DataSet{Elements: elements, ItemOffset: item.ItemOffset, ItemOffsetSet: item.ItemOffsetSet}
		}
		clone.Value = core.SequenceValue{Items: items}
	case core.FragmentSequence, core.BulkDataValue:
		return core.Element{}, fmt.Errorf("%w", ErrModalityWorklistProvider)
	default:
		return core.Element{}, fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	return clone, nil
}

func (budget *modalityWorklistCloneBudget) reservePrimitiveValue(value core.Value) error {
	switch typed := value.(type) {
	case core.SequenceValue:
		if len(typed.Items) > budget.remainingElements {
			return ErrModalityWorklistResourceLimit
		}
		remaining := budget.remainingElements - len(typed.Items)
		for _, item := range typed.Items {
			if len(item.Elements) > remaining {
				return ErrModalityWorklistResourceLimit
			}
			remaining -= len(item.Elements)
		}
		return nil
	case core.FragmentSequence, core.BulkDataValue:
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	case core.StringValue:
		if len(typed) > budget.maxMultiplicity {
			return ErrModalityWorklistResourceLimit
		}
	}
	length, ok := value.EncodedLength()
	if !ok {
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	return budget.consumeBytes(int64(length))
}

func validateModalityWorklistResultElement(element core.Element) error {
	return validateModalityWorklistResultElementWithCharacterSet(element, nil)
}

func validateModalityWorklistResultElementWithCharacterSet(element core.Element, characterSet *core.Element) error {
	wantVR, required, known := modalityWorklistResultRule(element.Tag())
	if known && element.VR() != wantVR {
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	validateKnown := known
	if !known {
		if entry, ok := std.Dictionary.ByTag(element.Tag()); ok {
			if element.VR() != entry.VR {
				return fmt.Errorf("%w", ErrModalityWorklistProvider)
			}
			validateKnown = true
		}
	}
	if element.VR() == core.VRSQ {
		if element.Tag() == tagMWLScheduledProtocolCodeSequence {
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok || len(sequence.Items) == 0 || validateModalityWorklistCodeSequence(sequence, characterSet) != nil {
				return fmt.Errorf("%w", ErrModalityWorklistProvider)
			}
		}
		return nil
	}
	if validateKnown {
		elements := []core.Element{element}
		if characterSet != nil && element.Tag() != tagMWLSpecificCharacterSet && element.VR().UsesSpecificCharacterSet() {
			elements = []core.Element{*characterSet, element}
		}
		if _, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: elements}, validation.Options{
			Mode: validation.ModeStrict, Dictionary: std.Dictionary, MaxFindings: 1, MaxDepth: 2, MaxElements: 4, StopFirst: true,
		}); err != nil {
			return fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
	}
	if element.Tag() == tagMWLSpecificCharacterSet {
		values := element.StringValues()
		if len(values) == 0 {
			return fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
		if _, err := dicomencoding.ParseCharacterSet(values...); err != nil {
			return fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
	}
	if element.Tag() == tagMWLTimezoneOffsetFromUTC {
		values := element.StringValues()
		if len(values) != 1 || validateMWLTimezoneOffset(values[0]) != nil {
			return fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
	}
	if required {
		values := element.StringValues()
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return fmt.Errorf("%w", ErrModalityWorklistProvider)
		}
	}
	return nil
}

func validateModalityWorklistCodeSequence(sequence core.SequenceValue, characterSet *core.Element) error {
	codeValueTags := []struct {
		tag core.Tag
		vr  core.VR
	}{
		{core.NewTag(0x0008, 0x0100), core.VRSH},
		{core.NewTag(0x0008, 0x0119), core.VRUC},
		{core.NewTag(0x0008, 0x0120), core.VRUR},
	}
	for _, item := range sequence.Items {
		codeValues := 0
		selectedCode := core.Tag{}
		selectedValue := ""
		for _, code := range codeValueTags {
			if element, ok := modalityWorklistDataSetElement(item, code.tag); ok {
				value, err := validateModalityWorklistCodeElement(element, code.vr, true, characterSet)
				if err != nil {
					return ErrModalityWorklistProvider
				}
				selectedCode = code.tag
				selectedValue = value
				codeValues++
			}
		}
		if codeValues != 1 {
			return ErrModalityWorklistProvider
		}
		codeLength := utf8.RuneCountInString(selectedValue)
		codeIsURI := modalityWorklistCodeValueIsURI(selectedValue)
		switch selectedCode {
		case core.NewTag(0x0008, 0x0100):
			if codeLength > 16 || codeIsURI {
				return ErrModalityWorklistProvider
			}
		case core.NewTag(0x0008, 0x0119):
			if codeLength <= 16 || codeIsURI {
				return ErrModalityWorklistProvider
			}
		case core.NewTag(0x0008, 0x0120):
			if !codeIsURI {
				return ErrModalityWorklistProvider
			}
		}
		codingSchemeTag := core.NewTag(0x0008, 0x0102)
		codingScheme, codingSchemePresent := modalityWorklistDataSetElement(item, codingSchemeTag)
		codingSchemeRequired := selectedCode != core.NewTag(0x0008, 0x0120)
		if codingSchemeRequired && !codingSchemePresent {
			return ErrModalityWorklistProvider
		}
		if codingSchemePresent {
			if _, err := validateModalityWorklistCodeElement(codingScheme, core.VRSH, codingSchemeRequired, characterSet); err != nil {
				return ErrModalityWorklistProvider
			}
		}
		if version, ok := modalityWorklistDataSetElement(item, core.NewTag(0x0008, 0x0103)); ok {
			if !codingSchemePresent {
				return ErrModalityWorklistProvider
			}
			if _, err := validateModalityWorklistCodeElement(version, core.VRSH, false, characterSet); err != nil {
				return ErrModalityWorklistProvider
			}
		}
		if meaning, ok := modalityWorklistDataSetElement(item, core.NewTag(0x0008, 0x0104)); ok {
			if _, err := validateModalityWorklistCodeElement(meaning, core.VRLO, false, characterSet); err != nil {
				return ErrModalityWorklistProvider
			}
		}
	}
	return nil
}

func validateModalityWorklistCodeElement(element core.Element, wantVR core.VR, required bool, characterSet *core.Element) (string, error) {
	values, ok := modalityWorklistCodeElementValues(element, characterSet)
	if !ok || element.VR() != wantVR || len(values) > 1 {
		return "", ErrModalityWorklistProvider
	}
	if required && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
		return "", ErrModalityWorklistProvider
	}
	if !required && (len(values) == 0 || strings.TrimSpace(values[0]) == "") {
		return "", nil
	}
	elements := []core.Element{element}
	if characterSet != nil && element.VR().UsesSpecificCharacterSet() {
		elements = []core.Element{*characterSet, element}
	}
	if _, err := validation.ValidateDataSet(context.Background(), core.DataSet{Elements: elements}, validation.Options{
		Mode: validation.ModeStrict, Dictionary: std.Dictionary, MaxFindings: 1, MaxDepth: 2, MaxElements: 4, StopFirst: true,
	}); err != nil {
		return "", ErrModalityWorklistProvider
	}
	return strings.TrimSpace(values[0]), nil
}

func modalityWorklistCodeElementValues(element core.Element, characterSet *core.Element) ([]string, bool) {
	elements := []core.Element{element}
	if characterSet != nil && element.VR().UsesSpecificCharacterSet() {
		elements = []core.Element{*characterSet, element}
	}
	return object.FromElements(elements, std.Dictionary).GetStrings(element.Tag())
}

func modalityWorklistCodeValueIsURI(value string) bool {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return false
	}
	remainderIndex := strings.IndexByte(value, ':')
	if remainderIndex < 0 || remainderIndex == len(value)-1 {
		return false
	}
	remainder := value[remainderIndex+1:]
	if strings.EqualFold(parsed.Scheme, "urn") {
		separator := strings.IndexByte(remainder, ':')
		if separator <= 0 || separator == len(remainder)-1 {
			return false
		}
		return validModalityWorklistURNNamespace(remainder[:separator])
	}
	return parsed.Host != "" || parsed.Opaque != "" || parsed.Path != ""
}

func validModalityWorklistURNNamespace(namespace string) bool {
	if len(namespace) < 2 || len(namespace) > 32 {
		return false
	}
	for index, character := range namespace {
		alphanumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphanumeric && (character != '-' || index == 0 || index == len(namespace)-1) {
			return false
		}
	}
	return true
}

func modalityWorklistResultRule(tag core.Tag) (core.VR, bool, bool) {
	switch tag {
	case tagMWLSpecificCharacterSet:
		return core.VRCS, false, true
	case tagMWLTimezoneOffsetFromUTC:
		return core.VRSH, true, true
	case tagMWLPatientName:
		return core.VRPN, true, true
	case tagMWLPatientID:
		return core.VRLO, true, true
	case tagMWLAccessionNumber:
		return core.VRSH, false, true
	case tagMWLRequestedProcedureID:
		return core.VRSH, true, true
	case tagMWLRequestedProcedureDescription:
		return core.VRLO, true, true
	case tagMWLScheduledProcedureStepSequence, tagMWLScheduledProtocolCodeSequence:
		return core.VRSQ, false, true
	case tagMWLScheduledStationAETitle:
		return core.VRAE, true, true
	case tagMWLModality:
		return core.VRCS, true, true
	case tagMWLScheduledProcedureStepStartDate:
		return core.VRDA, true, true
	case tagMWLScheduledProcedureStepStartTime:
		return core.VRTM, true, true
	case tagMWLScheduledPerformingPhysicianName:
		return core.VRPN, false, true
	case tagMWLScheduledProcedureStepDescription:
		return core.VRLO, false, true
	case tagMWLScheduledProcedureStepID:
		return core.VRSH, true, true
	case tagMWLScheduledStationName, tagMWLScheduledProcedureStepLocation:
		return core.VRSH, false, true
	default:
		return core.VRUnknown, false, false
	}
}

func (parsed ParsedModalityWorklistIdentifier) modalityWorklistUnsupported(tag core.Tag) bool {
	for _, unsupported := range parsed.UnsupportedOptionalKeys {
		if unsupported == tag {
			return true
		}
	}
	return false
}

func cloneMWLElement(element core.Element) core.Element {
	clone := element
	switch value := element.Value.(type) {
	case core.StringValue:
		clone.Value = core.StringValue(append([]string(nil), value...))
	case core.RawValue:
		clone.Value = core.RawValue(core.CloneBytes(value))
	case core.Uint16Value:
		clone.Value = core.Uint16Value(append([]uint16(nil), value...))
	case core.Int16Value:
		clone.Value = core.Int16Value(append([]int16(nil), value...))
	case core.Uint32Value:
		clone.Value = core.Uint32Value(append([]uint32(nil), value...))
	case core.Int32Value:
		clone.Value = core.Int32Value(append([]int32(nil), value...))
	case core.Uint64Value:
		clone.Value = core.Uint64Value(append([]uint64(nil), value...))
	case core.Int64Value:
		clone.Value = core.Int64Value(append([]int64(nil), value...))
	case core.Float32Value:
		clone.Value = core.Float32Value(append([]float32(nil), value...))
	case core.Float64Value:
		clone.Value = core.Float64Value(append([]float64(nil), value...))
	case core.TagValue:
		clone.Value = core.TagValue(append([]core.Tag(nil), value...))
	case core.SequenceValue:
		items := make([]core.DataSet, len(value.Items))
		for i, item := range value.Items {
			elements := make([]core.Element, len(item.Elements))
			for j, nested := range item.Elements {
				elements[j] = cloneMWLElement(nested)
			}
			items[i] = core.DataSet{Elements: elements, ItemOffset: item.ItemOffset, ItemOffsetSet: item.ItemOffsetSet}
		}
		clone.Value = core.SequenceValue{Items: items}
	}
	return clone
}
