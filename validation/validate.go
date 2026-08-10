package validation

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	asPattern               = regexp.MustCompile(`^[0-9]{3}[DWMY]$`)
	csPattern               = regexp.MustCompile(`^[A-Z0-9_ ]*$`)
	dsPattern               = regexp.MustCompile(`^[+\-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[Ee][+\-]?[0-9]+)?$`)
	isPattern               = regexp.MustCompile(`^[+\-]?[0-9]+$`)
	uiPattern               = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)
	tagSpecificCharacterSet = core.NewTag(0x0008, 0x0005)
)

type validationSession struct {
	ctx         context.Context
	opts        Options
	builder     *reportBuilder
	visited     int
	maxDepth    int
	maxElements int
}

func ValidateDataSet(ctx context.Context, dataset core.DataSet, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	builder, err := newReportBuilder(opts)
	if err != nil {
		return Result{}, err
	}
	session := validationSession{
		ctx: ctx, opts: opts, builder: builder,
		maxDepth: opts.MaxDepth, maxElements: opts.MaxElements,
	}
	if session.maxDepth == 0 {
		session.maxDepth = DefaultMaxDepth
	}
	if session.maxElements == 0 {
		session.maxElements = DefaultMaxElements
	}
	working, err := cloneDataSetWithLimits(ctx, dataset, session.maxDepth, session.maxElements)
	if err != nil {
		return Result{}, err
	}
	if err := session.runDataSetHook(HookPreValidation, nil, &working); err != nil {
		return Result{DataSet: working, Report: builder.report.Clone()}, err
	}
	elements, err := session.processElements(working, nil, 0, dicomenc.DefaultCharacterSet, true)
	working.Elements = elements
	if err == nil && !session.builder.shouldStop(opts) {
		err = session.validateRequiredUIDs(working)
	}
	if hookErr := func() error {
		if session.builder.shouldStop(opts) {
			return nil
		}
		return session.runDataSetHook(HookDataSetComplete, nil, &working)
	}(); err == nil && hookErr != nil {
		err = hookErr
	}
	if hookErr := func() error {
		if session.builder.shouldStop(opts) {
			return nil
		}
		return session.runDataSetHook(HookPostValidation, nil, &working)
	}(); err == nil && hookErr != nil {
		err = hookErr
	}
	result := Result{DataSet: working, Report: builder.report.Clone()}
	if err != nil {
		return result, err
	}
	if opts.Mode == ModeStrict && result.Report.HasErrors() {
		return result, &ValidationError{Report: result.Report.Clone()}
	}
	return result, nil
}

func ValidateElement(ctx context.Context, element core.Element, opts Options) (Report, error) {
	result, err := ValidateDataSet(ctx, core.DataSet{Elements: []core.Element{element}}, opts)
	return result.Report, err
}

func (s *validationSession) processElements(dataset core.DataSet, prefix Path, depth int, inheritedCharacterSet dicomenc.SpecificCharacterSet, inheritedCharacterSetValid bool) ([]core.Element, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > s.maxDepth {
		return nil, fmt.Errorf("%w: depth exceeds %d", ErrValidationLimit, s.maxDepth)
	}
	characterSet, characterSetValid := resolveDataSetCharacterSet(dataset, inheritedCharacterSet, inheritedCharacterSetValid)
	seen := make(map[core.Tag]struct{}, len(dataset.Elements))
	occurrences := make(map[core.Tag]int, len(dataset.Elements))
	result := make([]core.Element, 0, len(dataset.Elements))
	var previous core.Tag
	previousSet := false
	for _, original := range dataset.Elements {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		if s.builder.shouldStop(s.opts) {
			result = append(result, original)
			continue
		}
		s.visited++
		if s.visited > s.maxElements {
			return nil, fmt.Errorf("%w: element count exceeds %d", ErrValidationLimit, s.maxElements)
		}
		path := appendPath(prefix, PathStep{Tag: original.Tag(), ItemIndex: NoItem})
		occurrence := occurrences[original.Tag()]
		occurrences[original.Tag()] = occurrence + 1
		findingStart := len(s.builder.report.Findings)
		finishOccurrence := func() { s.setFindingOccurrence(findingStart, path, occurrence) }
		element := original
		if _, exists := seen[element.Tag()]; exists {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodeDuplicateElement, Message: "duplicate element in the same data set"})
		}
		seen[element.Tag()] = struct{}{}
		if previousSet && element.Tag().Less(previous) {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodeElementOrder, Severity: SeverityWarning, Message: "element is encoded out of tag order"})
		}
		previous = element.Tag()
		previousSet = true
		if s.builder.shouldStop(s.opts) {
			finishOccurrence()
			result = append(result, element)
			continue
		}

		_, sequenceValue := element.Value.(core.SequenceValue)
		if !sequenceValue {
			hookResult, err := s.runElementHook(HookAfterElement, path, element)
			if err != nil {
				return nil, err
			}
			if hookResult.Element != nil {
				element = cloneElement(*hookResult.Element)
				path[len(path)-1].Tag = element.Tag()
			}
			if hookResult.Filter {
				finishOccurrence()
				continue
			}
			if s.builder.shouldStop(s.opts) {
				finishOccurrence()
				result = append(result, element)
				continue
			}
		}

		s.validateElement(dataset, path, element, characterSet, characterSetValid)
		if s.builder.shouldStop(s.opts) {
			finishOccurrence()
			result = append(result, element)
			continue
		}
		if sequence, ok := element.Value.(core.SequenceValue); ok {
			items := make([]core.DataSet, len(sequence.Items))
			for itemIndex, item := range sequence.Items {
				itemPrefix := appendPath(prefix, PathStep{Tag: element.Tag(), ItemIndex: itemIndex})
				children, childErr := s.processElements(item, itemPrefix, depth+1, characterSet, characterSetValid)
				if childErr != nil {
					return nil, childErr
				}
				items[itemIndex] = item
				items[itemIndex].Elements = children
				if !s.builder.shouldStop(s.opts) {
					if err := s.runDataSetHook(HookItemComplete, itemPrefix, &items[itemIndex]); err != nil {
						return nil, err
					}
				}
			}
			sequence.Items = items
			element.Value = sequence
			if !s.builder.shouldStop(s.opts) {
				sequenceResult, err := s.runElementHook(HookSequenceComplete, path, element)
				if err != nil {
					return nil, err
				}
				if sequenceResult.Element != nil {
					element = cloneElement(*sequenceResult.Element)
					path[len(path)-1].Tag = element.Tag()
				}
				if sequenceResult.Filter {
					finishOccurrence()
					continue
				}
				afterResult, err := s.runElementHook(HookAfterElement, path, element)
				if err != nil {
					return nil, err
				}
				if afterResult.Element != nil {
					element = cloneElement(*afterResult.Element)
					path[len(path)-1].Tag = element.Tag()
				}
				if afterResult.Filter {
					finishOccurrence()
					continue
				}
			}
		}
		finishOccurrence()
		result = append(result, element)
	}
	if !s.builder.shouldStop(s.opts) {
		validated := dataset
		validated.Elements = result
		s.runDataSetRules(validated, prefix)
	}
	return result, nil
}

func (s *validationSession) setFindingOccurrence(start int, path Path, occurrence int) {
	key := path.String()
	for i := start; i < len(s.builder.report.Findings); i++ {
		if s.builder.report.Findings[i].Path.String() == key {
			s.builder.report.Findings[i].occurrence = occurrence
		}
	}
}

func (s *validationSession) runDataSetRules(dataset core.DataSet, path Path) {
	for _, registration := range s.opts.DataSetRules {
		if s.builder.shouldStop(s.opts) {
			return
		}
		findings, panicked := invokeDataSetRule(s.ctx, registration.Rule, DataSetContext{
			DataSet: cloneDataSet(dataset), Path: path.Clone(), Dictionary: s.opts.Dictionary,
			ByteOrder: byteOrder(s.opts.ByteOrder), TransferSyntax: s.opts.TransferSyntax,
		})
		if panicked {
			s.add(Finding{Path: path, Rule: registration.Name, Severity: SeverityWarning, Code: CodeDataSetRule, Message: "data set rule failed without exposing its error"})
			continue
		}
		for _, finding := range findings {
			if s.builder.shouldStop(s.opts) {
				return
			}
			if len(finding.Path) == 0 {
				if finding.Tag != (core.Tag{}) {
					finding.Path = appendPath(path, PathStep{Tag: finding.Tag, ItemIndex: NoItem})
				} else {
					finding.Path = path.Clone()
				}
			} else {
				finding.Path = append(path.Clone(), finding.Path...)
			}
			finding.Rule = registration.Name
			finding.ExpectedVR = nil
			finding.Offset = 0
			finding.OffsetSet = false
			finding.Hook = ""
			if finding.Code == "" {
				finding.Code = CodeDataSetRule
			}
			finding.Message = "data set rule reported a validation finding"
			s.add(finding)
		}
	}
}

func invokeDataSetRule(ctx context.Context, rule DataSetRule, dataset DataSetContext) (findings []Finding, panicked bool) {
	defer func() {
		if recover() != nil {
			findings = nil
			panicked = true
		}
	}()
	return rule.ValidateDataSet(ctx, dataset), false
}

func (s *validationSession) validateElement(dataset core.DataSet, path Path, element core.Element, characterSet dicomenc.SpecificCharacterSet, characterSetValid bool) {
	s.validateDictionary(dataset, path, element)
	s.validateValue(path, element, characterSet, characterSetValid)
	if element.Tag() == core.TagPixelData {
		s.validatePixelData(path, element)
	}
}

func (s *validationSession) validateDictionary(dataset core.DataSet, path Path, element core.Element) {
	if s.opts.Dictionary == nil {
		return
	}
	entry, ok := s.opts.Dictionary.ByTag(element.Tag())
	if !ok {
		return
	}
	expected := []core.VR(nil)
	if s.opts.ResolveVR != nil {
		expected = s.opts.ResolveVR(ElementContext{
			Element: element, DataSet: dataset, Path: path.Clone(), Dictionary: s.opts.Dictionary, ByteOrder: byteOrder(s.opts.ByteOrder),
		}, entry)
	}
	if len(expected) == 0 {
		if spec, found := dictionary.LookupVRSpec(s.opts.Dictionary, element.Tag()); found {
			expected = spec.Values()
		}
	}
	if len(expected) == 0 && entry.VR != "" {
		expected = []core.VR{entry.VR}
	}
	if element.VR() != core.VRUN && len(expected) > 0 && !containsVR(expected, element.VR()) {
		s.add(Finding{
			Path: path, Tag: element.Tag(), VR: element.VR(), ExpectedVR: expected,
			Rule: entry.Keyword, Code: CodeDictionaryVR, Message: "encoded VR does not match the data dictionary",
		})
	}
	if entry.VM == "" {
		return
	}
	vm := valueMultiplicity(element)
	if vm == 0 {
		return
	}
	if !matchesVM(entry.VM, vm) {
		s.add(Finding{
			Path: path, Tag: element.Tag(), VR: element.VR(), Rule: entry.VM,
			Code: CodeValueMultiplicity, Message: "value multiplicity does not match the data dictionary",
		})
	}
}

func (s *validationSession) validateValue(path Path, element core.Element, characterSet dicomenc.SpecificCharacterSet, characterSetValid bool) {
	vr := element.VR()
	if _, err := core.ParseVR(vr.String()); err != nil {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValueKind, Message: "element uses an unknown value representation"})
		return
	}
	if element.Header.HasLength() && element.Header.Length.IsDefined() && element.Header.Length&1 != 0 {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValueLength, Message: "encoded value length is odd"})
	}

	switch value := element.Value.(type) {
	case nil:
		return
	case core.StringValue:
		if !vr.IsStringLike() {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValueKind, Message: "string value is incompatible with the encoded VR"})
			return
		}
		for _, component := range value {
			s.validateTextComponent(path, element, component, false, characterSet, characterSetValid)
		}
	case core.RawValue:
		raw := value.Bytes()
		if vr.IsStringLike() {
			if len(raw) > 0 && len(raw)%2 == 0 && raw[len(raw)-1] != vr.PaddingByte() && (raw[len(raw)-1] == 0 || raw[len(raw)-1] == ' ') {
				s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValuePadding, Message: "value uses the wrong trailing padding byte"})
			}
			for _, component := range splitTextMultiplicity(vr, raw) {
				s.validateTextComponent(path, element, component, true, characterSet, characterSetValid)
			}
			return
		}
		s.validateRawBinary(path, element, raw)
	case core.SequenceValue:
		if vr != core.VRSQ && vr != core.VRUN {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeSequenceStructure, Message: "sequence value requires SQ or implicit-VR UN"})
		}
	case core.FragmentSequence:
		if element.Tag() != core.TagPixelData || (vr != core.VROB && vr != core.VROW) {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeFragmentStructure, Message: "fragment sequence is not valid for this tag and VR"})
		}
		if len(value.OffsetTable)%4 != 0 {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeFragmentStructure, Message: "basic offset table length is not a multiple of four"})
		}
	case core.BulkDataValue:
		if strings.TrimSpace(value.URI) == "" {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValueFormat, Message: "bulk data reference URI is empty"})
		}
	default:
		s.validateTypedValue(path, element)
	}
}

func (s *validationSession) validateTextComponent(path Path, element core.Element, component string, encodedRaw bool, characterSet dicomenc.SpecificCharacterSet, characterSetValid bool) {
	vr := element.VR()
	max := maxTextLength(vr)
	if max > 0 && textLength(vr, component) > max {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValueLength, Message: "text component exceeds the VR length limit"})
	}
	if !validTextRepertoire(vr, component, encodedRaw) || (encodedRaw && vr.UsesSpecificCharacterSet() && !validEncodedText(characterSet, characterSetValid, vr, []byte(component))) {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: CodeValueRepertoire, Message: "text component contains characters outside the VR repertoire"})
	}
	valid, rangeError := validateTextFormat(vr, component)
	if !valid {
		code := CodeValueFormat
		if rangeError {
			code = CodeValueRange
		}
		s.add(Finding{Path: path, Tag: element.Tag(), VR: vr, Code: code, Message: "text component does not satisfy the VR format"})
	}
}

func resolveDataSetCharacterSet(dataset core.DataSet, inherited dicomenc.SpecificCharacterSet, inheritedValid bool) (dicomenc.SpecificCharacterSet, bool) {
	for _, element := range dataset.Elements {
		if element.Tag() != tagSpecificCharacterSet {
			continue
		}
		parsed, err := dicomenc.ParseCharacterSet(element.StringValues()...)
		if err != nil {
			return inherited, false
		}
		return parsed, true
	}
	return inherited, inheritedValid
}

func validEncodedText(characterSet dicomenc.SpecificCharacterSet, characterSetValid bool, vr core.VR, value []byte) bool {
	if !characterSetValid {
		return false
	}
	if bytes.IndexByte(value, 0x1b) >= 0 {
		usesCodeExtensions := false
		for _, name := range characterSet.Names() {
			if strings.HasPrefix(name, "ISO 2022 ") {
				usesCodeExtensions = true
				break
			}
		}
		if !usesCodeExtensions {
			return false
		}
	}
	var err error
	if vr == core.VRPN {
		_, err = characterSet.DecodePersonName(value)
	} else {
		_, err = characterSet.Decode(value)
	}
	return err == nil
}

func (s *validationSession) validateRawBinary(path Path, element core.Element, raw []byte) {
	if element.VR() == core.VRSQ {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodeValueKind, Message: "raw value is incompatible with sequence VR"})
		return
	}
	width := binaryWidth(element.VR())
	if width > 0 && len(raw)%width != 0 {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodeValueLength, Message: "binary value length is not a multiple of the VR width"})
	}
}

func (s *validationSession) validateTypedValue(path Path, element core.Element) {
	compatible := false
	switch element.Value.(type) {
	case core.Uint16Value:
		compatible = element.VR() == core.VRUS
	case core.Int16Value:
		compatible = element.VR() == core.VRSS
	case core.Uint32Value:
		compatible = element.VR() == core.VRUL || element.VR() == core.VROL
	case core.Int32Value:
		compatible = element.VR() == core.VRSL
	case core.Uint64Value:
		compatible = element.VR() == core.VRUV || element.VR() == core.VROV
	case core.Int64Value:
		compatible = element.VR() == core.VRSV
	case core.Float32Value:
		compatible = element.VR() == core.VRFL || element.VR() == core.VROF
	case core.Float64Value:
		compatible = element.VR() == core.VRFD || element.VR() == core.VROD
	case core.TagValue:
		compatible = element.VR() == core.VRAT
	}
	if !compatible {
		s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodeValueKind, Message: "typed value is incompatible with the encoded VR"})
	}
}

func (s *validationSession) validateRequiredUIDs(dataset core.DataSet) error {
	for _, tag := range s.opts.RequiredUIDs {
		value, ok := dataSetString(dataset, tag)
		if !ok || strings.TrimSpace(value) == "" {
			s.add(Finding{Path: Path{{Tag: tag, ItemIndex: NoItem}}, Tag: tag, VR: core.VRUI, Code: CodeRequiredUID, Message: "required UID element is missing or empty"})
			if s.builder.shouldStop(s.opts) {
				break
			}
		}
	}
	return nil
}

func (s *validationSession) validatePixelData(path Path, element core.Element) {
	switch element.Value.(type) {
	case core.FragmentSequence:
		if !element.Header.Length.IsUndefined() && element.Header.HasLength() {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodePixelMetadata, Message: "encapsulated Pixel Data requires undefined length"})
		}
	case core.RawValue, nil:
		if element.Header.HasLength() && element.Header.Length.IsUndefined() {
			s.add(Finding{Path: path, Tag: element.Tag(), VR: element.VR(), Code: CodePixelMetadata, Message: "native Pixel Data requires defined length"})
		}
	}
}

func (s *validationSession) runElementHook(point HookPoint, path Path, element core.Element) (HookResult, error) {
	if s.opts.Hooks == nil {
		clone := cloneElement(element)
		return HookResult{Element: &clone}, nil
	}
	result, err := s.opts.Hooks.run(s.ctx, HookEvent{Point: point, Path: path.Clone(), Element: &element}, s.opts.StopFirst)
	s.addHookResult(result)
	return result, err
}

func (s *validationSession) runDataSetHook(point HookPoint, path Path, dataset *core.DataSet) error {
	if s.opts.Hooks == nil {
		return nil
	}
	result, err := s.opts.Hooks.run(s.ctx, HookEvent{Point: point, Path: path.Clone(), DataSet: dataset}, s.opts.StopFirst)
	s.addHookResult(result)
	return err
}

func (s *validationSession) addHookResult(result HookResult) {
	addHookResultToBuilder(s.builder, result)
}

func addHookResultToBuilder(builder *reportBuilder, result HookResult) {
	for _, finding := range result.Findings {
		builder.add(finding)
	}
	for _, change := range result.Changes {
		builder.change(change)
		code, message := CodeHookTransformed, "hook explicitly transformed an element"
		switch change.Kind {
		case ChangeFiltered:
			code, message = CodeHookFiltered, "hook explicitly filtered an element"
		case ChangeSkipped:
			code, message = CodeHookSkipped, "hook explicitly skipped an element value"
		case ChangeDeferred:
			code, message = CodeHookDeferred, "hook explicitly deferred an element value"
		}
		builder.add(Finding{Path: change.Path, Tag: change.Tag, Hook: change.Hook, Severity: SeverityInfo, Code: code, Message: message})
	}
}

func (s *validationSession) add(finding Finding) { s.builder.add(finding) }

func ValidateFile(ctx context.Context, meta, dataset core.DataSet, syntax transfer.Syntax, opts Options) (Report, error) {
	opts.TransferSyntax = syntax
	metaOpts := opts
	metaOpts.RequiredUIDs = nil
	metaResult, err := ValidateDataSet(ctx, meta, metaOpts)
	if err != nil && !errors.Is(err, ErrValidationFailed) {
		return metaResult.Report, err
	}
	if opts.StopFirst && len(metaResult.Report.Findings) > 0 {
		if opts.Mode == ModeStrict && metaResult.Report.HasErrors() {
			return metaResult.Report, &ValidationError{Report: metaResult.Report.Clone()}
		}
		return metaResult.Report, nil
	}
	dataResult, dataErr := ValidateDataSet(ctx, dataset, opts)
	report := mergeReports(opts, metaResult.Report, dataResult.Report)
	if dataErr != nil && !errors.Is(dataErr, ErrValidationFailed) {
		return report, dataErr
	}
	for _, pair := range []struct{ metaTag, dataTag core.Tag }{
		{core.NewTag(0x0002, 0x0002), core.NewTag(0x0008, 0x0016)},
		{core.NewTag(0x0002, 0x0003), core.NewTag(0x0008, 0x0018)},
	} {
		metaValue, metaOK := dataSetString(metaResult.DataSet, pair.metaTag)
		dataValue, dataOK := dataSetString(dataResult.DataSet, pair.dataTag)
		if !metaOK || !dataOK || core.NormalizeUID(metaValue) != core.NormalizeUID(dataValue) {
			report = appendFindingBounded(report, opts, Finding{Path: Path{{Tag: pair.metaTag, ItemIndex: NoItem}}, Tag: pair.metaTag, VR: core.VRUI, Code: CodeFileMetaMismatch, Message: "File Meta UID does not match the data set"})
		}
	}
	declared, ok := dataSetString(metaResult.DataSet, core.NewTag(0x0002, 0x0010))
	if !ok || core.NormalizeUID(declared) != core.NormalizeUID(syntax.UID) {
		tag := core.NewTag(0x0002, 0x0010)
		report = appendFindingBounded(report, opts, Finding{Path: Path{{Tag: tag, ItemIndex: NoItem}}, Tag: tag, VR: core.VRUI, Code: CodeFileMetaMismatch, Message: "File Meta Transfer Syntax UID does not match the supplied syntax"})
	}
	if opts.Mode == ModeStrict && report.HasErrors() {
		return report, &ValidationError{Report: report.Clone()}
	}
	return report, nil
}

func mergeReports(opts Options, reports ...Report) Report {
	merged, _ := MergeReports(opts, reports...)
	return merged
}

func appendFindingBounded(report Report, opts Options, finding Finding) Report {
	if opts.StopFirst && len(report.Findings) > 0 {
		return report
	}
	max := opts.MaxFindings
	if max == 0 {
		max = DefaultMaxFindings
	}
	if finding.Severity == "" {
		finding.Severity = SeverityError
	}
	if opts.Mode == ModeWarn && finding.Severity == SeverityError {
		finding.Severity = SeverityWarning
	}
	if len(report.Findings)+len(report.Changes) >= max {
		report.Truncated = true
		report.Dropped++
		return report
	}
	finding.Path = finding.Path.Clone()
	finding.ExpectedVR = append([]core.VR(nil), finding.ExpectedVR...)
	report.Findings = append(report.Findings, finding)
	return report
}

func validateTextFormat(vr core.VR, value string) (valid bool, rangeError bool) {
	if value == "" {
		return true, false
	}
	switch vr {
	case core.VRAS:
		return asPattern.MatchString(value), false
	case core.VRCS:
		return csPattern.MatchString(value), false
	case core.VRDA:
		_, err := dcmtime.ParseDA(value)
		return err == nil, err != nil && len(value) == 8
	case core.VRTM:
		_, err := dcmtime.ParseTM(value)
		return err == nil, err != nil
	case core.VRDT:
		_, err := dcmtime.ParseDatetime(value)
		return err == nil, err != nil
	case core.VRDS:
		value = strings.TrimSpace(value)
		if !dsPattern.MatchString(value) {
			return false, false
		}
		parsed, err := strconv.ParseFloat(value, 64)
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed), err != nil
	case core.VRIS:
		value = strings.TrimSpace(value)
		if !isPattern.MatchString(value) {
			return false, false
		}
		_, err := strconv.ParseInt(value, 10, 32)
		return err == nil, err != nil
	case core.VRUI:
		if !uiPattern.MatchString(value) {
			return false, false
		}
		for _, component := range strings.Split(value, ".") {
			if len(component) > 1 && component[0] == '0' {
				return false, false
			}
		}
		return true, false
	case core.VRPN:
		groups := strings.Split(value, "=")
		if len(groups) > 3 {
			return false, false
		}
		for _, group := range groups {
			if len(strings.Split(group, "^")) > 5 {
				return false, false
			}
		}
	}
	return true, false
}

func validTextRepertoire(vr core.VR, value string, encodedRaw bool) bool {
	if encodedRaw && vr.UsesSpecificCharacterSet() {
		for _, b := range []byte(value) {
			if b == 0x1b || (allowsTextLineControls(vr) && (b == '\t' || b == '\n' || b == '\f' || b == '\r')) {
				continue
			}
			if b < 0x20 || b == 0x7f {
				return false
			}
		}
		return true
	}
	if !utf8.ValidString(value) {
		return false
	}
	if vr.UsesSpecificCharacterSet() {
		for _, r := range value {
			if allowsTextLineControls(vr) && (r == '\t' || r == '\n' || r == '\f' || r == '\r') {
				continue
			}
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
		return true
	}
	for _, r := range value {
		if isSingleTextVR(vr) && r == '\\' {
			continue
		}
		if allowsTextLineControls(vr) && (r == '\t' || r == '\n' || r == '\f' || r == '\r') {
			continue
		}
		if r < 0x20 || r > 0x7e || r == '\\' {
			return false
		}
	}
	return true
}

func allowsTextLineControls(vr core.VR) bool {
	switch vr {
	case core.VRLT, core.VRST, core.VRUT:
		return true
	default:
		return false
	}
}

func maxTextLength(vr core.VR) int {
	switch vr {
	case core.VRAE, core.VRCS, core.VRDS, core.VRSH:
		return 16
	case core.VRAS:
		return 4
	case core.VRDA:
		return 8
	case core.VRDT:
		return 26
	case core.VRIS:
		return 12
	case core.VRLO, core.VRPN, core.VRUI:
		return 64
	case core.VRLT:
		return 10240
	case core.VRST:
		return 1024
	case core.VRTM:
		return 16
	default:
		return 0
	}
}

func textLength(vr core.VR, value string) int {
	if vr == core.VRPN {
		max := 0
		for _, group := range strings.Split(value, "=") {
			if n := utf8.RuneCountInString(group); n > max {
				max = n
			}
		}
		return max
	}
	return utf8.RuneCountInString(value)
}

func binaryWidth(vr core.VR) int {
	switch vr {
	case core.VRUS, core.VRSS, core.VROW:
		return 2
	case core.VRAT, core.VRFL, core.VRSL, core.VRUL, core.VROF, core.VROL:
		return 4
	case core.VRFD, core.VRSV, core.VRUV, core.VROD, core.VROV:
		return 8
	default:
		return 0
	}
}

func valueMultiplicity(element core.Element) int {
	switch value := element.Value.(type) {
	case nil:
		return 0
	case core.StringValue:
		return len(value)
	case core.RawValue:
		if len(value) == 0 {
			return 0
		}
		if element.VR().IsStringLike() {
			return len(splitTextMultiplicity(element.VR(), value))
		}
		if width := numericWidth(element.VR()); width > 0 {
			return len(value) / width
		}
		return 1
	case core.Uint16Value:
		return len(value)
	case core.Int16Value:
		return len(value)
	case core.Uint32Value:
		return len(value)
	case core.Int32Value:
		return len(value)
	case core.Uint64Value:
		return len(value)
	case core.Int64Value:
		return len(value)
	case core.Float32Value:
		return len(value)
	case core.Float64Value:
		return len(value)
	case core.TagValue:
		return len(value)
	case core.SequenceValue:
		return 1
	case core.FragmentSequence:
		return 1
	default:
		return 1
	}
}

func numericWidth(vr core.VR) int {
	switch vr {
	case core.VRUS, core.VRSS:
		return 2
	case core.VRAT, core.VRFL, core.VRSL, core.VRUL:
		return 4
	case core.VRFD, core.VRSV, core.VRUV:
		return 8
	default:
		return 0
	}
}

func isSingleTextVR(vr core.VR) bool {
	switch vr {
	case core.VRLT, core.VRST, core.VRUT, core.VRUR:
		return true
	default:
		return false
	}
}

func splitTextMultiplicity(vr core.VR, raw []byte) []string {
	if isSingleTextVR(vr) {
		return []string{string(core.TrimTextValueBytes(vr, raw))}
	}
	return core.SplitTextMultiplicity(vr, string(raw))
}

func matchesVM(spec string, count int) bool {
	parts := strings.Split(spec, "-")
	if len(parts) == 0 || len(parts) > 2 {
		return true
	}
	minimum, err := strconv.Atoi(parts[0])
	if err != nil || minimum <= 0 {
		return true
	}
	if len(parts) == 1 {
		return count == minimum
	}
	maximum := parts[1]
	if maximum == "n" {
		return count >= minimum
	}
	if strings.HasSuffix(maximum, "n") {
		multiple, err := strconv.Atoi(strings.TrimSuffix(maximum, "n"))
		if err != nil || multiple <= 0 {
			return true
		}
		return count >= minimum && count%multiple == 0
	}
	max, err := strconv.Atoi(maximum)
	if err != nil || max < minimum {
		return true
	}
	return count >= minimum && count <= max
}

func containsVR(values []core.VR, want core.VR) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func dataSetString(dataset core.DataSet, tag core.Tag) (string, bool) {
	for i := len(dataset.Elements) - 1; i >= 0; i-- {
		if dataset.Elements[i].Tag() == tag {
			return dataset.Elements[i].StringValue(), true
		}
	}
	return "", false
}

func appendPath(path Path, step PathStep) Path {
	result := make(Path, len(path)+1)
	copy(result, path)
	result[len(path)] = step
	return result
}

func byteOrder(order binary.ByteOrder) binary.ByteOrder {
	if order == nil {
		return binary.LittleEndian
	}
	return order
}

func cloneDataSet(dataset core.DataSet) core.DataSet {
	clone := dataset
	clone.Elements = make([]core.Element, len(dataset.Elements))
	for i, element := range dataset.Elements {
		clone.Elements[i] = cloneElement(element)
	}
	return clone
}

func cloneDataSetWithLimits(ctx context.Context, dataset core.DataSet, maxDepth, maxElements int) (core.DataSet, error) {
	visited := 0
	var clone func(core.DataSet, int) (core.DataSet, error)
	clone = func(source core.DataSet, depth int) (core.DataSet, error) {
		if err := ctx.Err(); err != nil {
			return core.DataSet{}, err
		}
		if depth > maxDepth {
			return core.DataSet{}, fmt.Errorf("%w: depth exceeds %d", ErrValidationLimit, maxDepth)
		}
		if len(source.Elements) > maxElements-visited {
			return core.DataSet{}, fmt.Errorf("%w: element/item count exceeds %d", ErrValidationLimit, maxElements)
		}
		visited += len(source.Elements)
		result := source
		result.Elements = make([]core.Element, len(source.Elements))
		for i, element := range source.Elements {
			if err := ctx.Err(); err != nil {
				return core.DataSet{}, err
			}
			cloned := element
			if sequence, ok := element.Value.(core.SequenceValue); ok {
				if len(sequence.Items) > maxElements-visited {
					return core.DataSet{}, fmt.Errorf("%w: element/item count exceeds %d", ErrValidationLimit, maxElements)
				}
				visited += len(sequence.Items)
				items := make([]core.DataSet, len(sequence.Items))
				for itemIndex, item := range sequence.Items {
					itemClone, err := clone(item, depth+1)
					if err != nil {
						return core.DataSet{}, err
					}
					items[itemIndex] = itemClone
				}
				cloned.Value = core.SequenceValue{Items: items}
			} else {
				cloned = cloneElement(element)
			}
			result.Elements[i] = cloned
		}
		return result, nil
	}
	return clone(dataset, 0)
}

func cloneElement(element core.Element) core.Element {
	clone := element
	switch value := element.Value.(type) {
	case core.RawValue:
		clone.Value = core.RawValue(core.CloneBytes(value.Bytes()))
	case core.StringValue:
		clone.Value = core.StringValue(append([]string(nil), value...))
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
			items[i] = cloneDataSet(item)
		}
		clone.Value = core.SequenceValue{Items: items}
	case core.FragmentSequence:
		fragments := make([][]byte, len(value.Fragments))
		for i, fragment := range value.Fragments {
			fragments[i] = core.CloneBytes(fragment)
		}
		clone.Value = core.FragmentSequence{OffsetTable: core.CloneBytes(value.OffsetTable), Fragments: fragments}
	}
	return clone
}
