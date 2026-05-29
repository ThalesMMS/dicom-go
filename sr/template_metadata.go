package sr

import (
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
)

// ElementsWithTemplateMetadata builds a document while preserving the
// Enhanced Code Sequence and Content Template Sequence metadata returned by
// ReadDocumentWithOptions. Elements keeps its historical output unchanged when
// this opt-in sidecar is not supplied.
func (d *Document) ElementsWithTemplateMetadata(metadata TemplateValidationMetadata) ([]core.Element, error) {
	elements, err := d.Elements()
	if err != nil {
		return nil, err
	}
	index, err := newTemplateMetadataIndex(metadata)
	if err != nil {
		return nil, err
	}
	root := ContentItemIdentifier{1}
	if err := applyCodeContexts(&elements, d.Title, nil, index.code[root.String()]); err != nil {
		return nil, err
	}
	if _, present := index.code[root.String()]; present {
		index.usedCode[root.String()] = struct{}{}
	}
	if identification, present := index.identification[root.String()]; present {
		if err := applyTemplateIdentification(&elements, identification); err != nil {
			return nil, err
		}
		index.usedIdentification[root.String()] = struct{}{}
	}
	if err := applyContentMetadata(&elements, d.Content, root, index); err != nil {
		return nil, err
	}
	if len(index.usedCode) != len(index.code) || len(index.usedIdentification) != len(index.identification) {
		return nil, &TemplateValidationError{Code: TemplateCodeCodeContext}
	}
	return elements, nil
}

// DatasetWithTemplateMetadata is the Object facade for
// ElementsWithTemplateMetadata.
func (d *Document) DatasetWithTemplateMetadata(metadata TemplateValidationMetadata) (*object.Object, error) {
	elements, err := d.ElementsWithTemplateMetadata(metadata)
	if err != nil {
		return nil, err
	}
	return object.FromElements(elements, nil), nil
}

type templateMetadataIndex struct {
	code               map[string]ContentItemCodeContexts
	identification     map[string]TemplateIdentification
	usedCode           map[string]struct{}
	usedIdentification map[string]struct{}
}

func newTemplateMetadataIndex(metadata TemplateValidationMetadata) (*templateMetadataIndex, error) {
	limit := DefaultTemplateValidationOptions().MaxItems
	if _, ok := templateMetadataItemCount(metadata, limit); !ok {
		return nil, &TemplateValidationError{Code: TemplateCodeResourceLimit}
	}
	index := &templateMetadataIndex{
		code:           make(map[string]ContentItemCodeContexts, len(metadata.CodeContexts)),
		identification: make(map[string]TemplateIdentification, len(metadata.TemplateIdentifications)),
		usedCode:       map[string]struct{}{}, usedIdentification: map[string]struct{}{},
	}
	for _, contexts := range metadata.CodeContexts {
		if contexts.Path == "" || len(contexts.Path) > maxRegistryString || codeContextsEmpty(contexts) ||
			!validCodeContextShape(contexts.ConceptName) || !validCodeContextShape(contexts.Value) ||
			!validCodeContextShape(contexts.MeasurementUnits) || !validCodeContextShape(contexts.NumericValueQualifier) ||
			!validEquivalentCodeEntries(contexts.ConceptNameEquivalentCodes) ||
			!validEquivalentCodeEntries(contexts.ValueEquivalentCodes) ||
			!validEquivalentCodeEntries(contexts.MeasurementUnitsEquivalentCodes) ||
			!validEquivalentCodeEntries(contexts.NumericValueQualifierEquivalents) {
			return nil, &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		if _, duplicate := index.code[contexts.Path]; duplicate {
			return nil, &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		index.code[contexts.Path] = contexts
	}
	for _, identification := range metadata.TemplateIdentifications {
		if !validTemplateIdentification(identification) || identification.MappingResource == "" || identification.Identifier == "" {
			return nil, &TemplateValidationError{Code: TemplateCodeTemplateIdentification}
		}
		if _, duplicate := index.identification[identification.Path]; duplicate {
			return nil, &TemplateValidationError{Code: TemplateCodeTemplateIdentification}
		}
		index.identification[identification.Path] = identification
	}
	return index, nil
}

func templateMetadataItemCount(metadata TemplateValidationMetadata, limit int) (int, bool) {
	count := 0
	add := func(items int) bool {
		if items < 0 || items > limit-count {
			return false
		}
		count += items
		return true
	}
	if !add(len(metadata.CodeContexts)) || !add(len(metadata.TemplateIdentifications)) {
		return 0, false
	}
	for _, contexts := range metadata.CodeContexts {
		if !add(len(contexts.ConceptNameEquivalentCodes)) || !add(len(contexts.ValueEquivalentCodes)) ||
			!add(len(contexts.MeasurementUnitsEquivalentCodes)) || !add(len(contexts.NumericValueQualifierEquivalents)) {
			return 0, false
		}
	}
	return count, true
}

func validEquivalentCodeEntries(entries []EquivalentCodeEntry) bool {
	for _, entry := range entries {
		if !validCodeContextShape(entry.Context) || !validCodeKey(codeKeyForEntry(entry.Code, entry.Context)) {
			return false
		}
	}
	return true
}

func applyContentMetadata(elements *[]core.Element, items []ContentItem, parentPath ContentItemIdentifier, index *templateMetadataIndex) error {
	sequenceIndex := elementIndex(*elements, tagContentSequence)
	if len(items) == 0 {
		return nil
	}
	if sequenceIndex < 0 {
		return &TemplateValidationError{Code: TemplateCodeCodeContext}
	}
	sequence, ok := (*elements)[sequenceIndex].Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != len(items) {
		return &TemplateValidationError{Code: TemplateCodeCodeContext}
	}
	for itemIndex := range items {
		path := appendPath(parentPath, uint32(itemIndex+1))
		pathKey := path.String()
		dataSet := &sequence.Items[itemIndex]
		contexts, hasContexts := index.code[pathKey]
		if err := applyCodeContexts(&dataSet.Elements, items[itemIndex].ConceptName, &items[itemIndex], contexts); err != nil {
			return err
		}
		if hasContexts {
			index.usedCode[pathKey] = struct{}{}
		}
		if identification, present := index.identification[pathKey]; present {
			if items[itemIndex].ValueType != ValueContainer {
				return &TemplateValidationError{Code: TemplateCodeTemplateIdentification}
			}
			if err := applyTemplateIdentification(&dataSet.Elements, identification); err != nil {
				return err
			}
			index.usedIdentification[pathKey] = struct{}{}
		}
		if err := applyContentMetadata(&dataSet.Elements, items[itemIndex].Children, path, index); err != nil {
			return err
		}
	}
	(*elements)[sequenceIndex].Value = sequence
	return nil
}

func applyCodeContexts(elements *[]core.Element, conceptName CodedEntry, item *ContentItem, contexts ContentItemCodeContexts) error {
	if !codeContextEmpty(contexts.ConceptName) || len(contexts.ConceptNameEquivalentCodes) != 0 {
		if conceptName.Empty() {
			return &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		sequence, err := codeSequenceWithContext(tagConceptNameCodeSeq, conceptName, contexts.ConceptName, contexts.ConceptNameEquivalentCodes)
		if err != nil {
			return err
		}
		replaceOrAppendElement(elements, sequence)
	}
	if item == nil {
		if !codeContextEmpty(contexts.Value) || len(contexts.ValueEquivalentCodes) != 0 ||
			!codeContextEmpty(contexts.MeasurementUnits) || len(contexts.MeasurementUnitsEquivalentCodes) != 0 ||
			!codeContextEmpty(contexts.NumericValueQualifier) || len(contexts.NumericValueQualifierEquivalents) != 0 {
			return &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		return nil
	}
	if !codeContextEmpty(contexts.Value) || len(contexts.ValueEquivalentCodes) != 0 {
		if item.ValueType != ValueCode {
			return &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		sequence, err := codeSequenceWithContext(tagConceptCodeSeq, item.Code, contexts.Value, contexts.ValueEquivalentCodes)
		if err != nil {
			return err
		}
		replaceOrAppendElement(elements, sequence)
	}
	if !codeContextEmpty(contexts.MeasurementUnits) || len(contexts.MeasurementUnitsEquivalentCodes) != 0 {
		if item.ValueType != ValueNum || item.Measurement == nil {
			return &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		units, err := codeSequenceWithContext(tagMeasurementUnitsCode, item.Measurement.Units, contexts.MeasurementUnits, contexts.MeasurementUnitsEquivalentCodes)
		if err != nil {
			return err
		}
		measurement := seqElement(tagMeasuredValueSeq, core.DataSet{Elements: []core.Element{
			derivedio.DS(tagNumericValue, item.Measurement.Value), units,
		}})
		replaceOrAppendElement(elements, measurement)
	}
	if !codeContextEmpty(contexts.NumericValueQualifier) || len(contexts.NumericValueQualifierEquivalents) != 0 {
		if item.ValueType != ValueNum || item.NumericValueQualifier.Empty() {
			return &TemplateValidationError{Code: TemplateCodeCodeContext}
		}
		qualifier, err := codeSequenceWithContext(
			tagNumericValueQualifierCodeSeq,
			item.NumericValueQualifier,
			contexts.NumericValueQualifier,
			contexts.NumericValueQualifierEquivalents,
		)
		if err != nil {
			return err
		}
		replaceOrAppendElement(elements, qualifier)
	}
	return nil
}

func codeSequenceWithContext(tag core.Tag, code CodedEntry, context CodeContext, equivalents []EquivalentCodeEntry) (core.Element, error) {
	if !validCodeContextShape(context) {
		return core.Element{}, &TemplateValidationError{Code: TemplateCodeCodeContext}
	}
	key := codeKeyForEntry(code, context)
	if !validCodeKey(key) {
		return core.Element{}, &TemplateValidationError{Code: TemplateCodeCodeContext}
	}
	elements := make([]core.Element, 0, 12)
	switch {
	case key.LongCodeValue != "":
		elements = append(elements, strElem(tagLongCodeValue, core.VRUC, key.LongCodeValue))
	case key.URNCodeValue != "":
		elements = append(elements, strElem(tagURNCodeValue, core.VRUR, key.URNCodeValue))
	default:
		elements = append(elements, strElem(tagCodeValue, core.VRSH, key.CodeValue))
	}
	if key.CodingSchemeDesignator != "" {
		elements = append(elements, strElem(tagCodingScheme, core.VRSH, key.CodingSchemeDesignator))
	}
	elements = append(elements, strElem(tagCodeMeaning, core.VRLO, code.CodeMeaning))
	appendContext := func(tag core.Tag, vr core.VR, value string) {
		if value != "" {
			elements = append(elements, strElem(tag, vr, value))
		}
	}
	appendContext(tagCodingSchemeVersion, core.VRSH, context.CodingSchemeVersion)
	appendContext(tagContextIdentifier, core.VRCS, context.ContextIdentifier)
	appendContext(tagMappingResource, core.VRCS, context.MappingResource)
	appendContext(tagContextGroupVersion, core.VRDT, context.ContextGroupVersion)
	appendContext(tagContextGroupLocalVersion, core.VRDT, context.ContextGroupLocalVersion)
	appendContext(tagContextGroupExtensionFlag, core.VRCS, context.ExtensionFlag)
	appendContext(tagContextGroupExtensionCreatorUID, core.VRUI, context.ExtensionCreatorUID)
	appendContext(tagContextUID, core.VRUI, context.ContextUID)
	appendContext(tagMappingResourceUID, core.VRUI, context.MappingResourceUID)
	appendContext(tagMappingResourceName, core.VRLO, context.MappingResourceName)
	if len(equivalents) > 0 {
		items := make([]core.DataSet, len(equivalents))
		for index, equivalent := range equivalents {
			encoded, err := codeSequenceWithContext(tagEquivalentCodeSequence, equivalent.Code, equivalent.Context, nil)
			if err != nil {
				return core.Element{}, err
			}
			sequence := encoded.Value.(core.SequenceValue)
			items[index] = sequence.Items[0]
		}
		elements = append(elements, seqElement(tagEquivalentCodeSequence, items...))
	}
	return seqElement(tag, core.DataSet{Elements: elements}), nil
}

func applyTemplateIdentification(elements *[]core.Element, identification TemplateIdentification) error {
	if identification.MappingResource == "" || identification.Identifier == "" {
		return &TemplateValidationError{Code: TemplateCodeTemplateIdentification}
	}
	item := []core.Element{
		strElem(tagMappingResource, core.VRCS, identification.MappingResource),
		strElem(tagTemplateIdentifier, core.VRCS, identification.Identifier),
	}
	if identification.MappingResourceUID != "" {
		item = append(item, strElem(tagMappingResourceUID, core.VRUI, identification.MappingResourceUID))
	}
	if identification.Version != "" {
		item = append(item, strElem(tagTemplateVersion, core.VRDT, identification.Version))
	}
	replaceOrAppendElement(elements, seqElement(tagContentTemplateSequence, core.DataSet{Elements: item}))
	return nil
}

func replaceOrAppendElement(elements *[]core.Element, replacement core.Element) {
	if index := elementIndex(*elements, replacement.Tag()); index >= 0 {
		(*elements)[index] = replacement
		return
	}
	*elements = append(*elements, replacement)
}

func elementIndex(elements []core.Element, tag core.Tag) int {
	for index := range elements {
		if elements[index].Tag() == tag {
			return index
		}
	}
	return -1
}

func decodeTemplateIdentification(dec *decoder, obj *object.Object, path ContentItemIdentifier) (TemplateIdentification, bool) {
	items, ok := obj.GetSequence(tagContentTemplateSequence)
	if !ok || len(items) == 0 || items[0] == nil {
		return TemplateIdentification{}, false
	}
	item := items[0]
	return TemplateIdentification{
		Path:               path.String(),
		MappingResource:    cleanString(item, tagMappingResource),
		MappingResourceUID: cleanString(item, tagMappingResourceUID),
		Identifier:         cleanString(item, tagTemplateIdentifier),
		Version:            cleanString(item, tagTemplateVersion),
	}, true
}

func cloneTemplateValidationMetadata(metadata TemplateValidationMetadata) TemplateValidationMetadata {
	out := TemplateValidationMetadata{
		CodeContexts:            append([]ContentItemCodeContexts(nil), metadata.CodeContexts...),
		TemplateIdentifications: append([]TemplateIdentification(nil), metadata.TemplateIdentifications...),
	}
	for index := range out.CodeContexts {
		out.CodeContexts[index].ConceptNameEquivalentCodes = append([]EquivalentCodeEntry(nil), metadata.CodeContexts[index].ConceptNameEquivalentCodes...)
		out.CodeContexts[index].ValueEquivalentCodes = append([]EquivalentCodeEntry(nil), metadata.CodeContexts[index].ValueEquivalentCodes...)
		out.CodeContexts[index].MeasurementUnitsEquivalentCodes = append([]EquivalentCodeEntry(nil), metadata.CodeContexts[index].MeasurementUnitsEquivalentCodes...)
		out.CodeContexts[index].NumericValueQualifierEquivalents = append([]EquivalentCodeEntry(nil), metadata.CodeContexts[index].NumericValueQualifierEquivalents...)
	}
	return out
}

func codeContextsEmpty(contexts ContentItemCodeContexts) bool {
	return codeContextEmpty(contexts.ConceptName) && len(contexts.ConceptNameEquivalentCodes) == 0 &&
		codeContextEmpty(contexts.Value) && len(contexts.ValueEquivalentCodes) == 0 &&
		codeContextEmpty(contexts.MeasurementUnits) && len(contexts.MeasurementUnitsEquivalentCodes) == 0 &&
		codeContextEmpty(contexts.NumericValueQualifier) && len(contexts.NumericValueQualifierEquivalents) == 0
}

func codeContextEmpty(context CodeContext) bool { return context == (CodeContext{}) }
