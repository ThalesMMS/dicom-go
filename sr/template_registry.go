package sr

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
)

var ErrInvalidTemplateRegistry = errors.New("dicom/sr: invalid template registry")

var ErrTemplateValidation = errors.New("dicom/sr: template validation failed")

const (
	TemplateRegistryCodeInvalidDefinition = "sr.template_registry.invalid_definition"
	TemplateRegistryCodeDuplicate         = "sr.template_registry.duplicate"
	TemplateRegistryCodeMissingInclude    = "sr.template_registry.missing_include"
	TemplateRegistryCodeIncludeCycle      = "sr.template_registry.include_cycle"
	TemplateRegistryCodeResourceLimit     = "sr.template_registry.resource_limit"
)

const (
	TemplateCodeTemplateNotFound       = "sr.template.not_found"
	TemplateCodeCardinality            = "sr.template.cardinality"
	TemplateCodeUnknownContent         = "sr.template.unknown_content"
	TemplateCodeOrder                  = "sr.template.order"
	TemplateCodeConditionUnsupported   = "sr.template.condition_unsupported"
	TemplateCodeConditionFailed        = "sr.template.condition_failed"
	TemplateCodeReferenceMode          = "sr.template.reference_mode"
	TemplateCodeContextGroup           = "sr.template.context_group"
	TemplateCodeCodeContext            = "sr.template.code_context"
	TemplateCodeTemplateIdentification = "sr.template.identification"
	TemplateCodeResourceLimit          = "sr.template.resource_limit"
)

const (
	maxRegistryDefinitions  = 4096
	maxRegistryRows         = 100_000
	maxRegistryCodes        = 250_000
	maxRegistryIncludes     = 100_000
	maxRegistryString       = 2048
	maxTemplateIncludeDepth = 32
	maxTemplateRowDepth     = 64
)

// TemplateRegistryError is a typed, PHI-free registry construction error.
type TemplateRegistryError struct{ Code string }

func (err *TemplateRegistryError) Error() string {
	if err == nil || err.Code == "" {
		return ErrInvalidTemplateRegistry.Error()
	}
	return fmt.Sprintf("%s: %s", ErrInvalidTemplateRegistry, err.Code)
}

func (err *TemplateRegistryError) Unwrap() error { return ErrInvalidTemplateRegistry }

func (err *TemplateRegistryError) Is(target error) bool {
	return target == ErrResourceLimitExceeded && err != nil && err.Code == TemplateRegistryCodeResourceLimit
}

func newTemplateRegistryError(code string) error { return &TemplateRegistryError{Code: code} }

// TemplateKey identifies one version of a template managed by a mapping
// resource. Identifier does not include a human-readable template name.
type TemplateKey struct {
	MappingResource string
	Identifier      string
	Version         string
}

// ContextGroupKey identifies one version of a context group managed by a
// mapping resource.
type ContextGroupKey struct {
	MappingResource string
	Identifier      string
	Version         string
}

// CodeKey is the identity of a coded concept. Code Meaning is intentionally
// absent because it is display text and may be replaced by a synonym.
type CodeKey struct {
	CodeValue              string
	LongCodeValue          string
	URNCodeValue           string
	CodingSchemeDesignator string
	CodingSchemeVersion    string
}

// CodeContext carries Enhanced Code Sequence metadata that is not modeled by
// CodedEntry. It is comparable and can therefore be used safely in frozen
// indexes. Exactly one of LongCodeValue and URNCodeValue may be present; the
// ordinary Code Value remains in the associated CodedEntry.
type CodeContext struct {
	CodingSchemeVersion      string
	ContextIdentifier        string
	MappingResource          string
	ContextGroupVersion      string
	ContextGroupLocalVersion string
	ExtensionFlag            string
	ExtensionCreatorUID      string
	ContextUID               string
	MappingResourceUID       string
	MappingResourceName      string
	LongCodeValue            string
	URNCodeValue             string
}

// EquivalentCodeEntry preserves one item of Equivalent Code Sequence without
// adding non-comparable fields to CodedEntry.
type EquivalentCodeEntry struct {
	Code    CodedEntry
	Context CodeContext
}

// ContentItemCodeContexts associates non-textual code metadata with a stable,
// value-free Content Item path. Equivalent-code slices are caller-owned.
type ContentItemCodeContexts struct {
	Path                             string
	ConceptName                      CodeContext
	ConceptNameEquivalentCodes       []EquivalentCodeEntry
	Value                            CodeContext
	ValueEquivalentCodes             []EquivalentCodeEntry
	MeasurementUnits                 CodeContext
	MeasurementUnitsEquivalentCodes  []EquivalentCodeEntry
	NumericValueQualifier            CodeContext
	NumericValueQualifierEquivalents []EquivalentCodeEntry
}

// TemplateIdentification carries the encoded Content Template Sequence
// identity for a CONTAINER without adding fields to Document or ContentItem.
type TemplateIdentification struct {
	Path               string
	MappingResource    string
	MappingResourceUID string
	Identifier         string
	Version            string
}

// DefinitionProvenance records the verifiable source of a generated or
// application-provided definition without placing it in diagnostics.
type DefinitionProvenance struct {
	Source   string
	Checksum string
}

// TemplateDefinition is an immutable-after-registration description of the
// rows that constrain the children of an SR CONTAINER.
type TemplateDefinition struct {
	Key              TemplateKey
	Extensible       bool
	OrderSignificant bool
	Rows             []TemplateRow
}

// TemplateRow describes one template row. Additional constraints are added to
// this value as part of the public template validation API.
type TemplateRow struct {
	RuleID            string
	RelationshipTypes []string
	ReferenceMode     ReferenceMode
	ValueTypes        []ValueType
	ConceptNames      []CodeKey
	ContextGroup      *ContextGroupKey
	Cardinality       Cardinality
	Requirement       Requirement
	ConditionID       string
	Condition         TemplateCondition
	Children          []TemplateRow
	Include           *TemplateKey
}

// ReferenceMode constrains whether a template row is represented by-value or
// by-reference. The zero value is treated as either for compatibility with
// definitions that do not constrain relationship mode.
type ReferenceMode uint8

const (
	ReferenceModeByValue ReferenceMode = iota + 1
	ReferenceModeByReference
	ReferenceModeEither
)

// Cardinality is the permitted number of matching Content Items. Max == 0 is
// unbounded.
type Cardinality struct {
	Min int
	Max int
}

// Requirement controls whether a template row must be present. A conditional
// row is required only when its condition evaluates true.
type Requirement uint8

const (
	RequirementRequired Requirement = iota + 1
	RequirementConditional
	RequirementOptional
)

// TemplateConditionContext intentionally contains only structural and coded
// identity data; it excludes SR text, names, UIDs, dates and code meanings.
type TemplateConditionContext struct {
	SOPClassUID     string
	ParentPath      string
	ParentValueType ValueType
	ChildCount      int
}

// TemplateCondition is a caller-supplied, context-aware predicate for one
// conditional template row.
type TemplateCondition func(context.Context, TemplateConditionContext) (bool, error)

// TemplateRegistry is a frozen registry. Its maps are never mutated after
// construction, so lookups are safe to use concurrently.
type TemplateRegistry struct {
	templates           map[TemplateKey]TemplateDefinition
	contextGroups       map[ContextGroupKey]ContextGroupDefinition
	compiledTemplates   map[TemplateKey]TemplateDefinition
	contextGroupMembers map[ContextGroupKey]map[CodeKey]struct{}
}

// TemplateValidationOptions bounds one validation run and controls whether
// violations are returned as errors or warnings.
type TemplateValidationOptions struct {
	Mode        ValidationMode
	MaxDepth    int
	MaxItems    int
	MaxSteps    int
	MaxFindings int
}

// DefaultTemplateValidationOptions returns bounded strict defaults.
func DefaultTemplateValidationOptions() TemplateValidationOptions {
	return TemplateValidationOptions{
		Mode:        ValidationModeStrict,
		MaxDepth:    64,
		MaxItems:    100_000,
		MaxSteps:    1_000_000,
		MaxFindings: 1_024,
	}
}

// TemplateValidationMetadata supplies encoded metadata that is intentionally
// kept outside the legacy Document and ContentItem structs.
type TemplateValidationMetadata struct {
	CodeContexts            []ContentItemCodeContexts
	TemplateIdentifications []TemplateIdentification
}

// TemplateValidator is an immutable, opt-in validator. A validator and its
// registry may be shared by concurrent goroutines.
type TemplateValidator struct {
	registry *TemplateRegistry
	options  TemplateValidationOptions
}

// TemplateValidationError is a typed, PHI-free validation error.
type TemplateValidationError struct{ Code string }

func (err *TemplateValidationError) Error() string {
	if err == nil || err.Code == "" {
		return ErrTemplateValidation.Error()
	}
	return fmt.Sprintf("%s: %s", ErrTemplateValidation, err.Code)
}

func (err *TemplateValidationError) Unwrap() error { return ErrTemplateValidation }

func (err *TemplateValidationError) Is(target error) bool {
	return target == ErrResourceLimitExceeded && err != nil && err.Code == TemplateCodeResourceLimit
}

// NewTemplateValidator constructs a bounded validator over a frozen registry.
func NewTemplateValidator(registry *TemplateRegistry, options TemplateValidationOptions) (*TemplateValidator, error) {
	if registry == nil {
		return nil, &TemplateValidationError{Code: TemplateCodeTemplateNotFound}
	}
	options = normalizeTemplateValidationOptions(options)
	if options.Mode != ValidationModeStrict && options.Mode != ValidationModeWarn {
		return nil, &TemplateValidationError{Code: TemplateCodeResourceLimit}
	}
	if options.MaxDepth < 1 || options.MaxItems < 1 || options.MaxSteps < 1 || options.MaxFindings < 1 {
		return nil, &TemplateValidationError{Code: TemplateCodeResourceLimit}
	}
	return &TemplateValidator{registry: registry, options: options}, nil
}

func normalizeTemplateValidationOptions(options TemplateValidationOptions) TemplateValidationOptions {
	defaults := DefaultTemplateValidationOptions()
	if options.Mode == 0 {
		options.Mode = defaults.Mode
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = defaults.MaxDepth
	}
	if options.MaxItems == 0 {
		options.MaxItems = defaults.MaxItems
	}
	if options.MaxSteps == 0 {
		options.MaxSteps = defaults.MaxSteps
	}
	if options.MaxFindings == 0 {
		options.MaxFindings = defaults.MaxFindings
	}
	return options
}

// Validate checks document against key without additional encoded metadata.
func (validator *TemplateValidator) Validate(ctx context.Context, document *Document, key TemplateKey) (ValidationReport, error) {
	return validator.ValidateWithMetadata(ctx, document, key, TemplateValidationMetadata{})
}

// ValidateWithMetadata checks document against key and verifies supplied code
// and template identification metadata. No validation is performed unless this
// method is called explicitly.
func (validator *TemplateValidator) ValidateWithMetadata(ctx context.Context, document *Document, key TemplateKey, metadata TemplateValidationMetadata) (ValidationReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := ValidationReport{}
	if validator == nil || validator.registry == nil || document == nil {
		addTemplateFinding(&report, templateOptions(validator), TemplateCodeTemplateNotFound, ContentItemIdentifier{1}, "")
		return validator.finish(report)
	}
	definition, ok := validator.registry.compiledTemplates[key]
	if !ok {
		addTemplateFinding(&report, validator.options, TemplateCodeTemplateNotFound, ContentItemIdentifier{1}, "")
		return validator.finish(report)
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if _, ok := templateMetadataItemCount(metadata, validator.options.MaxItems); !ok {
		addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, ContentItemIdentifier{1}, "")
		return validator.finish(report)
	}

	codeContexts := make(map[string]ContentItemCodeContexts, len(metadata.CodeContexts))
	for _, itemContexts := range metadata.CodeContexts {
		if itemContexts.Path == "" || len(itemContexts.Path) > maxRegistryString ||
			!validCodeContextShape(itemContexts.ConceptName) || !validCodeContextShape(itemContexts.Value) ||
			!validCodeContextShape(itemContexts.MeasurementUnits) || !validCodeContextShape(itemContexts.NumericValueQualifier) ||
			!validEquivalentCodeEntries(itemContexts.ConceptNameEquivalentCodes) ||
			!validEquivalentCodeEntries(itemContexts.ValueEquivalentCodes) ||
			!validEquivalentCodeEntries(itemContexts.MeasurementUnitsEquivalentCodes) ||
			!validEquivalentCodeEntries(itemContexts.NumericValueQualifierEquivalents) {
			addTemplateFinding(&report, validator.options, TemplateCodeCodeContext, ContentItemIdentifier{1}, "")
			continue
		}
		if _, duplicate := codeContexts[itemContexts.Path]; duplicate {
			addTemplateFinding(&report, validator.options, TemplateCodeCodeContext, ContentItemIdentifier{1}, "")
			continue
		}
		codeContexts[itemContexts.Path] = itemContexts
	}
	identifications := make(map[string]TemplateIdentification, len(metadata.TemplateIdentifications))
	for _, identification := range metadata.TemplateIdentifications {
		if !validTemplateIdentification(identification) {
			addTemplateFinding(&report, validator.options, TemplateCodeTemplateIdentification, ContentItemIdentifier{1}, "")
			continue
		}
		if _, duplicate := identifications[identification.Path]; duplicate {
			addTemplateFinding(&report, validator.options, TemplateCodeTemplateIdentification, ContentItemIdentifier{1}, "")
			continue
		}
		identifications[identification.Path] = identification
	}
	if identification, provided := identifications[ContentItemIdentifier{1}.String()]; provided {
		if identification.MappingResource != key.MappingResource || identification.Identifier != key.Identifier ||
			(identification.Version != "" && identification.Version != key.Version) {
			addTemplateFinding(&report, validator.options, TemplateCodeTemplateIdentification, ContentItemIdentifier{1}, "")
		}
	}
	targets, targetErr := indexTemplateTargets(document, validator.options)
	if targetErr != nil {
		addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, ContentItemIdentifier{1}, "")
		return validator.finish(report)
	}

	root := ContentItemIdentifier{1}
	stack := []templateValidationFrame{{
		path: root, valueType: ValueContainer, children: document.Content,
		rows: definition.Rows, orderSignificant: definition.OrderSignificant,
	}}
	items := 0
	steps := 0
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return report.clone(), err
		}
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if len(frame.path)-1 > validator.options.MaxDepth {
			addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, frame.path, "")
			break
		}
		counts := make([]int, len(frame.rows))
		lastRow := -1
		for childIndex := range frame.children {
			items++
			if items > validator.options.MaxItems {
				addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, frame.path, "")
				break
			}
			child := &frame.children[childIndex]
			childPath := appendPath(frame.path, uint32(childIndex+1))
			matched := -1
			matchedScore := -1 << 30
			mismatchCode := ""
			mismatchRule := ""
			start := 0
			if frame.orderSignificant && lastRow >= 0 {
				start = lastRow
			}
			for rowIndex := start; rowIndex < len(frame.rows); rowIndex++ {
				steps++
				if steps > validator.options.MaxSteps {
					addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, childPath, "")
					break
				}
				matches, reason := templateRowMatches(frame.rows[rowIndex], *child, codeContexts[childPath.String()], validator.registry, targets, codeContexts, validator.options)
				if reason != "" && mismatchCode == "" {
					mismatchCode = reason
					mismatchRule = frame.rows[rowIndex].RuleID
				}
				if matches {
					if frame.orderSignificant {
						matched = rowIndex
						break
					}
					score := templateRowSelectionScore(frame.rows[rowIndex], counts[rowIndex])
					if matched < 0 || score > matchedScore {
						matched = rowIndex
						matchedScore = score
					}
				}
			}
			if steps > validator.options.MaxSteps {
				break
			}
			if matched < 0 && frame.orderSignificant && start > 0 {
				for rowIndex := 0; rowIndex < start; rowIndex++ {
					steps++
					if steps > validator.options.MaxSteps {
						addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, childPath, "")
						break
					}
					matches, _ := templateRowMatches(frame.rows[rowIndex], *child, codeContexts[childPath.String()], validator.registry, targets, codeContexts, validator.options)
					if matches {
						matched = rowIndex
						break
					}
				}
			}
			if matched < 0 {
				if mismatchCode != "" {
					addTemplateFinding(&report, validator.options, mismatchCode, childPath, mismatchRule)
				} else {
					addTemplateFindingSeverity(&report, validator.options, TemplateCodeUnknownContent, childPath, "", definition.Extensible)
				}
				if len(child.Children) > 0 {
					stack = append(stack, templateValidationFrame{path: childPath, valueType: child.ValueType, children: child.Children})
				}
				continue
			}
			if frame.orderSignificant && matched < lastRow {
				addTemplateFinding(&report, validator.options, TemplateCodeOrder, childPath, frame.rows[matched].RuleID)
			}
			if matched > lastRow {
				lastRow = matched
			}
			counts[matched]++
			if len(child.Children) > 0 {
				stack = append(stack, templateValidationFrame{
					path: childPath, valueType: child.ValueType, children: child.Children,
					rows: frame.rows[matched].Children, orderSignificant: frame.orderSignificant,
				})
			}
		}
		for rowIndex, row := range frame.rows {
			steps++
			if steps > validator.options.MaxSteps {
				addTemplateFinding(&report, validator.options, TemplateCodeResourceLimit, frame.path, row.RuleID)
				break
			}
			required := row.Requirement == RequirementRequired
			if row.Requirement == RequirementConditional {
				if row.Condition == nil {
					addTemplateFinding(&report, validator.options, TemplateCodeConditionUnsupported, frame.path, row.RuleID)
				} else {
					active, err := callTemplateCondition(row.Condition, ctx, TemplateConditionContext{
						SOPClassUID: document.SOPClassUID, ParentPath: frame.path.String(),
						ParentValueType: frame.valueType, ChildCount: len(frame.children),
					})
					if err != nil {
						addTemplateFinding(&report, validator.options, TemplateCodeConditionFailed, frame.path, row.RuleID)
					} else {
						required = active
					}
				}
			}
			minimum := row.Cardinality.Min
			if required && minimum < 1 {
				minimum = 1
			}
			if !required && counts[rowIndex] == 0 {
				continue
			}
			if counts[rowIndex] < minimum || (row.Cardinality.Max > 0 && counts[rowIndex] > row.Cardinality.Max) {
				addTemplateFinding(&report, validator.options, TemplateCodeCardinality, frame.path, row.RuleID)
			}
		}
		if steps > validator.options.MaxSteps || items > validator.options.MaxItems {
			break
		}
	}
	return validator.finish(report)
}

type templateValidationFrame struct {
	path             ContentItemIdentifier
	valueType        ValueType
	children         []ContentItem
	rows             []TemplateRow
	orderSignificant bool
}

func indexTemplateTargets(document *Document, options TemplateValidationOptions) (map[string]*ContentItem, error) {
	rootItem := &ContentItem{ValueType: ValueContainer, ConceptName: document.Title, Children: document.Content}
	root := ContentItemIdentifier{1}
	targets := map[string]*ContentItem{identifierKey(root): rootItem}
	walker := newReferenceWalker(document.Content, root, 1)
	count := 0
	for {
		entry, ok := walker.next()
		if !ok {
			break
		}
		count++
		if count > options.MaxItems || entry.depth > options.MaxDepth {
			return nil, ErrResourceLimitExceeded
		}
		if len(entry.item.ReferencedContentItemIdentifier) == 0 {
			targets[identifierKey(entry.path)] = entry.item
		}
		walker.push(entry.item.Children, entry.path, entry.depth+1)
	}
	return targets, nil
}

func callTemplateCondition(condition TemplateCondition, ctx context.Context, input TemplateConditionContext) (active bool, err error) {
	defer func() {
		if recover() != nil {
			active = false
			err = errors.New("dicom/sr: template condition failed")
		}
	}()
	return condition(ctx, input)
}

func templateOptions(validator *TemplateValidator) TemplateValidationOptions {
	if validator == nil {
		return DefaultTemplateValidationOptions()
	}
	return validator.options
}

func (validator *TemplateValidator) finish(report ValidationReport) (ValidationReport, error) {
	report = report.clone()
	for _, finding := range report.Findings {
		if finding.Code == TemplateCodeResourceLimit {
			return report, &TemplateValidationError{Code: TemplateCodeResourceLimit}
		}
	}
	if validator != nil && validator.options.Mode == ValidationModeStrict && report.HasErrors() {
		return report, &TemplateValidationError{Code: report.Findings[0].Code}
	}
	return report, nil
}

func addTemplateFinding(report *ValidationReport, options TemplateValidationOptions, code string, path ContentItemIdentifier, ruleID string) {
	addTemplateFindingSeverity(report, options, code, path, ruleID, false)
}

func addTemplateFindingSeverity(report *ValidationReport, options TemplateValidationOptions, code string, path ContentItemIdentifier, ruleID string, allowedExtension bool) {
	if len(report.Findings) >= options.MaxFindings {
		report.Truncated = true
		return
	}
	severity := DiagnosticError
	if code != TemplateCodeResourceLimit && (options.Mode == ValidationModeWarn || allowedExtension) {
		severity = DiagnosticWarning
	}
	report.Findings = append(report.Findings, DiagnosticFinding{
		Path: path.Clone(), RuleID: ruleID, Code: code, Severity: severity, Message: templateFindingMessage(code),
	})
}

func templateFindingMessage(code string) string {
	switch code {
	case TemplateCodeTemplateNotFound:
		return "template is not registered"
	case TemplateCodeCardinality:
		return "template row cardinality is not satisfied"
	case TemplateCodeUnknownContent:
		return "content item is not described by the template"
	case TemplateCodeOrder:
		return "content item order does not satisfy the template"
	case TemplateCodeConditionUnsupported:
		return "template condition is not available"
	case TemplateCodeConditionFailed:
		return "template condition evaluation failed"
	case TemplateCodeReferenceMode:
		return "content item relationship mode does not satisfy the template"
	case TemplateCodeContextGroup:
		return "coded value does not satisfy the context group"
	case TemplateCodeCodeContext:
		return "encoded code context metadata is invalid"
	case TemplateCodeTemplateIdentification:
		return "encoded template identification does not match validation template"
	default:
		return "template validation resource limit exceeded"
	}
}

// NewTemplateRegistry freezes template and context-group definitions into a
// registry. Context groups are accepted here so both namespaces can be
// validated and frozen atomically.
func NewTemplateRegistry(templates []TemplateDefinition, contextGroups []ContextGroupDefinition) (*TemplateRegistry, error) {
	if len(templates)+len(contextGroups) > maxRegistryDefinitions {
		return nil, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
	}
	registry := &TemplateRegistry{
		templates:           make(map[TemplateKey]TemplateDefinition, len(templates)),
		contextGroups:       make(map[ContextGroupKey]ContextGroupDefinition, len(contextGroups)),
		compiledTemplates:   make(map[TemplateKey]TemplateDefinition, len(templates)),
		contextGroupMembers: make(map[ContextGroupKey]map[CodeKey]struct{}, len(contextGroups)),
	}
	rawRowCount := 0
	for _, definition := range templates {
		if !validTemplateKey(definition.Key) {
			return nil, newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		if _, exists := registry.templates[definition.Key]; exists {
			return nil, newTemplateRegistryError(TemplateRegistryCodeDuplicate)
		}
		if err := validateTemplateRows(definition.Rows, &rawRowCount); err != nil {
			return nil, err
		}
		registry.templates[definition.Key] = cloneTemplateDefinition(definition)
	}
	rawCodeCount := 0
	for _, definition := range contextGroups {
		if !validContextGroupDefinition(definition) {
			return nil, newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		if _, exists := registry.contextGroups[definition.Key]; exists {
			return nil, newTemplateRegistryError(TemplateRegistryCodeDuplicate)
		}
		seenCodes := make(map[CodeKey]struct{}, len(definition.Codes))
		for _, code := range definition.Codes {
			rawCodeCount++
			if rawCodeCount > maxRegistryCodes {
				return nil, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
			}
			if !validCodeKey(code) {
				return nil, newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
			}
			if _, duplicate := seenCodes[code]; duplicate {
				return nil, newTemplateRegistryError(TemplateRegistryCodeDuplicate)
			}
			seenCodes[code] = struct{}{}
		}
		registry.contextGroups[definition.Key] = cloneContextGroupDefinition(definition)
	}
	if err := validateTemplateIncludes(registry.templates); err != nil {
		return nil, err
	}
	if err := validateContextGroupIncludes(registry.contextGroups); err != nil {
		return nil, err
	}
	if err := validateTemplateContextGroups(registry.templates, registry.contextGroups); err != nil {
		return nil, err
	}
	compiledRowCount := 0
	for key, definition := range registry.templates {
		rows, err := registry.expandTemplateRows(definition.Rows, 0, 0, "", &compiledRowCount)
		if err != nil {
			return nil, err
		}
		if err := validateCompiledRuleIDs(rows); err != nil {
			return nil, err
		}
		compiled := cloneTemplateDefinition(definition)
		compiled.Rows = rows
		registry.compiledTemplates[key] = compiled
	}
	compiledMemberCount := 0
	for key := range registry.contextGroups {
		members, err := registry.expandContextGroup(key, &compiledMemberCount)
		if err != nil {
			return nil, err
		}
		registry.contextGroupMembers[key] = members
	}
	return registry, nil
}

// LookupTemplate returns a caller-owned copy of the requested definition.
func (registry *TemplateRegistry) LookupTemplate(key TemplateKey) (TemplateDefinition, bool) {
	if registry == nil {
		return TemplateDefinition{}, false
	}
	definition, ok := registry.templates[key]
	if !ok {
		return TemplateDefinition{}, false
	}
	return cloneTemplateDefinition(definition), true
}

// LookupContextGroup returns a caller-owned copy of the requested definition.
func (registry *TemplateRegistry) LookupContextGroup(key ContextGroupKey) (ContextGroupDefinition, bool) {
	if registry == nil {
		return ContextGroupDefinition{}, false
	}
	definition, ok := registry.contextGroups[key]
	if !ok {
		return ContextGroupDefinition{}, false
	}
	return cloneContextGroupDefinition(definition), true
}

// WithTemplate returns a new registry containing definition. The receiver is
// unchanged and duplicate keys remain errors.
func (registry *TemplateRegistry) WithTemplate(definition TemplateDefinition) (*TemplateRegistry, error) {
	templates, groups := registryDefinitions(registry)
	templates = append(templates, definition)
	return NewTemplateRegistry(templates, groups)
}

// WithContextGroup returns a new registry containing definition. The receiver
// is unchanged and duplicate keys remain errors.
func (registry *TemplateRegistry) WithContextGroup(definition ContextGroupDefinition) (*TemplateRegistry, error) {
	templates, groups := registryDefinitions(registry)
	groups = append(groups, definition)
	return NewTemplateRegistry(templates, groups)
}

func registryDefinitions(registry *TemplateRegistry) ([]TemplateDefinition, []ContextGroupDefinition) {
	if registry == nil {
		return nil, nil
	}
	templates := make([]TemplateDefinition, 0, len(registry.templates))
	for _, definition := range registry.templates {
		templates = append(templates, cloneTemplateDefinition(definition))
	}
	groups := make([]ContextGroupDefinition, 0, len(registry.contextGroups))
	for _, definition := range registry.contextGroups {
		groups = append(groups, cloneContextGroupDefinition(definition))
	}
	return templates, groups
}

func cloneTemplateDefinition(definition TemplateDefinition) TemplateDefinition {
	definition.Rows = cloneTemplateRows(definition.Rows)
	return definition
}

func cloneTemplateRows(rows []TemplateRow) []TemplateRow {
	out := make([]TemplateRow, len(rows))
	for index := range rows {
		out[index] = rows[index]
		out[index].RelationshipTypes = append([]string(nil), rows[index].RelationshipTypes...)
		out[index].ValueTypes = append([]ValueType(nil), rows[index].ValueTypes...)
		out[index].ConceptNames = append([]CodeKey(nil), rows[index].ConceptNames...)
		out[index].Children = cloneTemplateRows(rows[index].Children)
		if rows[index].ContextGroup != nil {
			contextGroup := *rows[index].ContextGroup
			out[index].ContextGroup = &contextGroup
		}
		if rows[index].Include != nil {
			include := *rows[index].Include
			out[index].Include = &include
		}
	}
	return out
}

// ContextGroupDefinition is one immutable, versioned set of coded concepts.
// Includes are expanded and checked when the registry is constructed.
type ContextGroupDefinition struct {
	Key        ContextGroupKey
	Extensible bool
	Codes      []CodeKey
	Includes   []ContextGroupKey
	Provenance DefinitionProvenance
}

func cloneContextGroupDefinition(definition ContextGroupDefinition) ContextGroupDefinition {
	definition.Codes = append([]CodeKey(nil), definition.Codes...)
	definition.Includes = append([]ContextGroupKey(nil), definition.Includes...)
	return definition
}

func validTemplateKey(key TemplateKey) bool {
	return validRegistryPart(key.MappingResource) && validRegistryPart(key.Identifier) && validRegistryPart(key.Version)
}

func validContextGroupKey(key ContextGroupKey) bool {
	return validRegistryPart(key.MappingResource) && validRegistryPart(key.Identifier) && validRegistryPart(key.Version)
}

func validRegistryPart(value string) bool { return value != "" && len(value) <= maxRegistryString }

func validCodeKey(code CodeKey) bool {
	valueCount := 0
	for _, value := range []string{code.CodeValue, code.LongCodeValue, code.URNCodeValue} {
		if value != "" {
			valueCount++
			if !utf8.ValidString(value) || len(value) > maxRegistryString {
				return false
			}
		}
	}
	if valueCount != 1 || len(code.CodingSchemeVersion) > maxRegistryString {
		return false
	}
	if code.CodeValue != "" && utf8.RuneCountInString(code.CodeValue) > 16 {
		return false
	}
	if code.LongCodeValue != "" && utf8.RuneCountInString(code.LongCodeValue) <= 16 {
		return false
	}
	if code.URNCodeValue != "" {
		if code.CodingSchemeDesignator == "" {
			return code.CodingSchemeVersion == ""
		}
		return validRegistryPart(code.CodingSchemeDesignator)
	}
	return validRegistryPart(code.CodingSchemeDesignator)
}

func validContextGroupDefinition(definition ContextGroupDefinition) bool {
	return validContextGroupKey(definition.Key) && len(definition.Provenance.Source) <= maxRegistryString &&
		len(definition.Provenance.Checksum) <= maxRegistryString && len(definition.Codes) <= maxRegistryCodes &&
		len(definition.Includes) <= maxRegistryIncludes &&
		((definition.Provenance.Source == "") == (definition.Provenance.Checksum == ""))
}

func validateTemplateRows(rows []TemplateRow, totalRows *int) error {
	seen := map[string]struct{}{}
	type rowEntry struct {
		row   TemplateRow
		depth int
	}
	stack := make([]rowEntry, 0, len(rows))
	for _, row := range rows {
		stack = append(stack, rowEntry{row: row, depth: 1})
	}
	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		row := entry.row
		*totalRows++
		if *totalRows > maxRegistryRows || entry.depth > maxTemplateRowDepth {
			return newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
		}
		if !validRuleID(row.RuleID) {
			return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		if _, duplicate := seen[row.RuleID]; duplicate {
			return newTemplateRegistryError(TemplateRegistryCodeDuplicate)
		}
		seen[row.RuleID] = struct{}{}
		if row.Include != nil && len(row.Children) != 0 {
			return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		if row.ReferenceMode > ReferenceModeEither || row.Requirement > RequirementOptional || row.Cardinality.Min < 0 ||
			row.Cardinality.Max < 0 || (row.Cardinality.Max > 0 && row.Cardinality.Max < row.Cardinality.Min) {
			return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		if len(row.RelationshipTypes) > 64 || len(row.ValueTypes) > 64 || len(row.ConceptNames) > 4096 {
			return newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
		}
		for _, relationship := range row.RelationshipTypes {
			if relationship == "" || len(relationship) > 64 {
				return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
			}
		}
		if row.Requirement == RequirementConditional && !validRegistryPart(row.ConditionID) {
			return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		for _, concept := range row.ConceptNames {
			if !validCodeKey(concept) {
				return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
			}
		}
		for _, child := range row.Children {
			stack = append(stack, rowEntry{row: child, depth: entry.depth + 1})
		}
	}
	return nil
}

func validRuleID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func templateRowMatches(
	row TemplateRow,
	item ContentItem,
	contexts ContentItemCodeContexts,
	registry *TemplateRegistry,
	targets map[string]*ContentItem,
	allContexts map[string]ContentItemCodeContexts,
	options TemplateValidationOptions,
) (bool, string) {
	byReference := len(item.ReferencedContentItemIdentifier) != 0
	if len(row.RelationshipTypes) > 0 && !containsString(row.RelationshipTypes, item.RelationshipType) {
		return false, ""
	}
	if byReference {
		if row.ReferenceMode == ReferenceModeByValue {
			return false, TemplateCodeReferenceMode
		}
		targetPath := item.ReferencedContentItemIdentifier
		if !targetPath.valid(options.MaxDepth + 1) {
			return false, ReferenceCodeInvalidPath
		}
		target, ok := targets[identifierKey(targetPath)]
		if !ok {
			return false, ReferenceCodeDangling
		}
		matches, reason := templateValueMatches(row, *target, allContexts[targetPath.String()], registry)
		if !matches && reason == "" {
			reason = ReferenceCodeIncompatible
		}
		return matches, reason
	}
	if row.ReferenceMode == ReferenceModeByReference {
		return false, TemplateCodeReferenceMode
	}
	return templateValueMatches(row, item, contexts, registry)
}

func templateValueMatches(row TemplateRow, item ContentItem, contexts ContentItemCodeContexts, registry *TemplateRegistry) (bool, string) {
	if len(row.ValueTypes) > 0 && !containsValueType(row.ValueTypes, item.ValueType) {
		return false, ""
	}
	if len(row.ConceptNames) > 0 {
		concept := codeKeyForEntry(item.ConceptName, contexts.ConceptName)
		if !containsCodeKey(row.ConceptNames, concept) {
			return false, ""
		}
	}
	if row.ContextGroup != nil {
		var code CodeKey
		var codeContext CodeContext
		switch item.ValueType {
		case ValueCode:
			codeContext = contexts.Value
			code = codeKeyForEntry(item.Code, codeContext)
		case ValueNum:
			if item.Measurement != nil {
				codeContext = contexts.MeasurementUnits
				code = codeKeyForEntry(item.Measurement.Units, codeContext)
			}
		}
		matches, reason := registry.contextGroupContains(*row.ContextGroup, code, codeContext)
		if !matches {
			return false, reason
		}
	}
	return true, ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func templateRowSelectionScore(row TemplateRow, currentCount int) int {
	score := 0
	switch row.Requirement {
	case RequirementRequired:
		score += 10_000
	case RequirementConditional:
		score += 5_000
	}
	if currentCount < row.Cardinality.Min {
		score += 2_000
	}
	if len(row.ConceptNames) > 0 {
		score += 1_000 - min(len(row.ConceptNames), 999)
	}
	if row.ContextGroup != nil {
		score += 500
	}
	if len(row.ValueTypes) > 0 {
		score += 200 - min(len(row.ValueTypes), 199)
	}
	if len(row.RelationshipTypes) > 0 {
		score += 100 - min(len(row.RelationshipTypes), 99)
	}
	if row.ReferenceMode != 0 && row.ReferenceMode != ReferenceModeEither {
		score += 50
	}
	if row.Cardinality.Max > 0 && currentCount >= row.Cardinality.Max {
		score -= 1_000_000
	}
	return score
}

func containsValueType(values []ValueType, want ValueType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsCodeKey(values []CodeKey, want CodeKey) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func codeKeyForEntry(entry CodedEntry, context CodeContext) CodeKey {
	return CodeKey{
		CodeValue: entry.CodeValue, LongCodeValue: context.LongCodeValue, URNCodeValue: context.URNCodeValue,
		CodingSchemeDesignator: entry.CodingSchemeDesignator, CodingSchemeVersion: context.CodingSchemeVersion,
	}
}

func (registry *TemplateRegistry) contextGroupContains(key ContextGroupKey, code CodeKey, codeContext CodeContext) (bool, string) {
	definition, defined := registry.contextGroups[key]
	members, ok := registry.contextGroupMembers[key]
	if !defined || !ok || !validCodeKey(code) {
		return false, TemplateCodeCodeContext
	}
	if !validEncodedCodeContext(key, codeContext) {
		return false, TemplateCodeCodeContext
	}
	_, found := members[code]
	if found {
		if codeContext.ExtensionFlag == "Y" {
			return false, TemplateCodeCodeContext
		}
		return true, ""
	}
	if !definition.Extensible {
		return false, TemplateCodeContextGroup
	}
	if codeContext.ExtensionFlag != "Y" || codeContext.ContextGroupLocalVersion == "" || codeContext.ExtensionCreatorUID == "" {
		return false, TemplateCodeCodeContext
	}
	return true, ""
}

func validEncodedCodeContext(key ContextGroupKey, codeContext CodeContext) bool {
	if codeContext.LongCodeValue != "" && codeContext.URNCodeValue != "" {
		return false
	}
	groupMetadataPresent := codeContext.ContextIdentifier != "" || codeContext.MappingResource != "" ||
		codeContext.ContextGroupVersion != "" || codeContext.ContextGroupLocalVersion != "" ||
		codeContext.ExtensionFlag != "" || codeContext.ExtensionCreatorUID != ""
	if groupMetadataPresent {
		if codeContext.ContextIdentifier == "" || codeContext.MappingResource == "" || codeContext.ContextGroupVersion == "" {
			return false
		}
		if codeContext.ContextIdentifier != key.Identifier || codeContext.MappingResource != key.MappingResource ||
			codeContext.ContextGroupVersion != key.Version {
			return false
		}
	}
	if codeContext.ExtensionFlag != "" && codeContext.ExtensionFlag != "Y" && codeContext.ExtensionFlag != "N" {
		return false
	}
	if codeContext.ExtensionFlag == "Y" {
		return codeContext.ContextGroupLocalVersion != "" && codeContext.ExtensionCreatorUID != ""
	}
	return codeContext.ContextGroupLocalVersion == "" && codeContext.ExtensionCreatorUID == ""
}

func validCodeContextShape(codeContext CodeContext) bool {
	for _, value := range []string{
		codeContext.CodingSchemeVersion, codeContext.ContextIdentifier, codeContext.MappingResource,
		codeContext.ContextGroupVersion, codeContext.ContextGroupLocalVersion, codeContext.ExtensionFlag,
		codeContext.ExtensionCreatorUID, codeContext.ContextUID, codeContext.MappingResourceUID,
		codeContext.MappingResourceName, codeContext.LongCodeValue, codeContext.URNCodeValue,
	} {
		if len(value) > maxRegistryString {
			return false
		}
	}
	if codeContext.LongCodeValue != "" && codeContext.URNCodeValue != "" {
		return false
	}
	groupMetadataPresent := codeContext.ContextIdentifier != "" || codeContext.MappingResource != "" ||
		codeContext.ContextGroupVersion != "" || codeContext.ContextGroupLocalVersion != "" ||
		codeContext.ExtensionFlag != "" || codeContext.ExtensionCreatorUID != ""
	if groupMetadataPresent && (codeContext.ContextIdentifier == "" || codeContext.MappingResource == "" || codeContext.ContextGroupVersion == "") {
		return false
	}
	if codeContext.ExtensionFlag != "" && codeContext.ExtensionFlag != "Y" && codeContext.ExtensionFlag != "N" {
		return false
	}
	if codeContext.ExtensionFlag == "Y" {
		return codeContext.ContextGroupLocalVersion != "" && codeContext.ExtensionCreatorUID != ""
	}
	return codeContext.ContextGroupLocalVersion == "" && codeContext.ExtensionCreatorUID == ""
}

func validTemplateIdentification(identification TemplateIdentification) bool {
	if identification.Path == "" || len(identification.Path) > maxRegistryString {
		return false
	}
	for _, value := range []string{
		identification.MappingResource, identification.MappingResourceUID, identification.Identifier, identification.Version,
	} {
		if len(value) > maxRegistryString {
			return false
		}
	}
	return true
}

func validateTemplateIncludes(templates map[TemplateKey]TemplateDefinition) error {
	adjacency := make(map[TemplateKey][]TemplateKey, len(templates))
	indegree := make(map[TemplateKey]int, len(templates))
	for key := range templates {
		indegree[key] = 0
	}
	includeCount := 0
	for key, definition := range templates {
		stack := append([]TemplateRow(nil), definition.Rows...)
		for len(stack) > 0 {
			row := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, row.Children...)
			if row.Include == nil {
				continue
			}
			includeCount++
			if includeCount > maxRegistryIncludes {
				return newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
			}
			if _, exists := templates[*row.Include]; !exists {
				return newTemplateRegistryError(TemplateRegistryCodeMissingInclude)
			}
			adjacency[key] = append(adjacency[key], *row.Include)
			indegree[*row.Include]++
		}
	}
	if graphHasCycle(adjacency, indegree) {
		return newTemplateRegistryError(TemplateRegistryCodeIncludeCycle)
	}
	return nil
}

func validateContextGroupIncludes(groups map[ContextGroupKey]ContextGroupDefinition) error {
	adjacency := make(map[ContextGroupKey][]ContextGroupKey, len(groups))
	indegree := make(map[ContextGroupKey]int, len(groups))
	for key := range groups {
		indegree[key] = 0
	}
	includeCount := 0
	for key, definition := range groups {
		for _, include := range definition.Includes {
			includeCount++
			if includeCount > maxRegistryIncludes {
				return newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
			}
			if _, exists := groups[include]; !exists {
				return newTemplateRegistryError(TemplateRegistryCodeMissingInclude)
			}
			adjacency[key] = append(adjacency[key], include)
			indegree[include]++
		}
	}
	if graphHasCycle(adjacency, indegree) {
		return newTemplateRegistryError(TemplateRegistryCodeIncludeCycle)
	}
	return nil
}

func validateTemplateContextGroups(templates map[TemplateKey]TemplateDefinition, groups map[ContextGroupKey]ContextGroupDefinition) error {
	for _, definition := range templates {
		stack := append([]TemplateRow(nil), definition.Rows...)
		for len(stack) > 0 {
			row := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, row.Children...)
			if row.ContextGroup != nil {
				if _, exists := groups[*row.ContextGroup]; !exists {
					return newTemplateRegistryError(TemplateRegistryCodeMissingInclude)
				}
			}
		}
	}
	return nil
}

func (registry *TemplateRegistry) expandTemplateRows(rows []TemplateRow, includeDepth, rowDepth int, scope string, rowCount *int) ([]TemplateRow, error) {
	if includeDepth > maxTemplateIncludeDepth || rowDepth > maxTemplateRowDepth {
		return nil, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
	}
	out := make([]TemplateRow, 0, len(rows))
	for _, row := range rows {
		*rowCount++
		if *rowCount > maxRegistryRows {
			return nil, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
		}
		if row.Include != nil {
			included := registry.templates[*row.Include]
			includeScope := scopedTemplateRuleID(scope, row.RuleID)
			expanded, err := registry.expandTemplateRows(included.Rows, includeDepth+1, rowDepth, includeScope, rowCount)
			if err != nil {
				return nil, err
			}
			expanded, err = applyTemplateIncludeConstraints(row, expanded)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded...)
			continue
		}
		cloned := cloneTemplateRows([]TemplateRow{row})[0]
		cloned.Children = nil
		cloned.RuleID = scopedTemplateRuleID(scope, cloned.RuleID)
		if !validRuleID(cloned.RuleID) {
			return nil, newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		children, err := registry.expandTemplateRows(row.Children, includeDepth, rowDepth+1, scope, rowCount)
		if err != nil {
			return nil, err
		}
		cloned.Children = children
		out = append(out, cloned)
	}
	return out, nil
}

func scopedTemplateRuleID(scope, ruleID string) string {
	if scope == "" {
		return ruleID
	}
	return scope + "." + ruleID
}

func applyTemplateIncludeConstraints(include TemplateRow, rows []TemplateRow) ([]TemplateRow, error) {
	for index := range rows {
		if len(include.RelationshipTypes) > 0 {
			rows[index].RelationshipTypes = append([]string(nil), include.RelationshipTypes...)
		}
		cardinality, err := composeTemplateCardinality(include.Cardinality, rows[index].Cardinality)
		if err != nil {
			return nil, err
		}
		rows[index].Cardinality = cardinality
		applyTemplateIncludeRequirement(include, &rows[index])
	}
	return rows, nil
}

func composeTemplateCardinality(include, row Cardinality) (Cardinality, error) {
	if include == (Cardinality{}) {
		return row, nil
	}
	minimum, ok := multiplyTemplateCardinality(include.Min, row.Min)
	if !ok {
		return Cardinality{}, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
	}
	maximum := 0
	if include.Max > 0 && row.Max > 0 {
		maximum, ok = multiplyTemplateCardinality(include.Max, row.Max)
		if !ok {
			return Cardinality{}, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
		}
	}
	return Cardinality{Min: minimum, Max: maximum}, nil
}

func multiplyTemplateCardinality(left, right int) (int, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	maximum := int(^uint(0) >> 1)
	if left > maximum/right {
		return 0, false
	}
	return left * right, true
}

func applyTemplateIncludeRequirement(include TemplateRow, row *TemplateRow) {
	switch include.Requirement {
	case RequirementOptional:
		row.Requirement = RequirementOptional
		row.ConditionID = ""
		row.Condition = nil
	case RequirementConditional:
		if row.Requirement == RequirementOptional {
			return
		}
		if row.Requirement == RequirementConditional {
			row.ConditionID = include.ConditionID + "+" + row.ConditionID
			row.Condition = combinedTemplateCondition(include.Condition, row.Condition)
			return
		}
		row.Requirement = RequirementConditional
		row.ConditionID = include.ConditionID
		row.Condition = include.Condition
	}
}

func combinedTemplateCondition(left, right TemplateCondition) TemplateCondition {
	return func(ctx context.Context, input TemplateConditionContext) (bool, error) {
		leftActive, err := callTemplateCondition(left, ctx, input)
		if err != nil || !leftActive {
			return leftActive, err
		}
		return callTemplateCondition(right, ctx, input)
	}
}

func validateCompiledRuleIDs(rows []TemplateRow) error {
	seen := map[string]struct{}{}
	stack := append([]TemplateRow(nil), rows...)
	for len(stack) > 0 {
		row := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !validRuleID(row.RuleID) {
			return newTemplateRegistryError(TemplateRegistryCodeInvalidDefinition)
		}
		if _, duplicate := seen[row.RuleID]; duplicate {
			return newTemplateRegistryError(TemplateRegistryCodeDuplicate)
		}
		seen[row.RuleID] = struct{}{}
		stack = append(stack, row.Children...)
	}
	return nil
}

func (registry *TemplateRegistry) expandContextGroup(root ContextGroupKey, totalMembers *int) (map[CodeKey]struct{}, error) {
	members := map[CodeKey]struct{}{}
	seen := map[ContextGroupKey]struct{}{}
	stack := []ContextGroupKey{root}
	steps := 0
	for len(stack) > 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, visited := seen[key]; visited {
			continue
		}
		seen[key] = struct{}{}
		steps++
		if steps > maxRegistryDefinitions {
			return nil, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
		}
		definition := registry.contextGroups[key]
		for _, code := range definition.Codes {
			if _, exists := members[code]; exists {
				continue
			}
			members[code] = struct{}{}
			*totalMembers++
			if *totalMembers > maxRegistryCodes {
				return nil, newTemplateRegistryError(TemplateRegistryCodeResourceLimit)
			}
		}
		stack = append(stack, definition.Includes...)
	}
	return members, nil
}

func graphHasCycle[K comparable](adjacency map[K][]K, indegree map[K]int) bool {
	queue := make([]K, 0, len(indegree))
	for key, degree := range indegree {
		if degree == 0 {
			queue = append(queue, key)
		}
	}
	visited := 0
	for len(queue) > 0 {
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		visited++
		for _, target := range adjacency[key] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	return visited != len(indegree)
}

// CurrentMeasurementReportTemplate describes exactly the private structural
// shape emitted by WriteMeasurementReport today. It deliberately does not
// claim conformance to any DICOM TID.
func CurrentMeasurementReportTemplate() TemplateDefinition {
	return TemplateDefinition{
		Key: TemplateKey{
			MappingResource: "DICOMGO",
			Identifier:      "CURRENT_MEASUREMENT_REPORT",
			Version:         "1",
		},
		OrderSignificant: true,
		Rows: []TemplateRow{{
			RuleID:            "measurement-group",
			RelationshipTypes: []string{RelationshipContains},
			ReferenceMode:     ReferenceModeByValue,
			ValueTypes:        []ValueType{ValueContainer},
			ConceptNames: []CodeKey{{
				CodeValue: "125007", CodingSchemeDesignator: "DCM",
			}},
			Cardinality: Cardinality{Min: 1},
			Requirement: RequirementOptional,
			Children: []TemplateRow{
				{
					RuleID: "tracking-identifier", RelationshipTypes: []string{RelationshipContains},
					ReferenceMode: ReferenceModeByValue, ValueTypes: []ValueType{ValueText},
					ConceptNames: []CodeKey{{CodeValue: "112039", CodingSchemeDesignator: "DCM"}},
					Cardinality:  Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
				},
				{
					RuleID: "tracking-unique-identifier", RelationshipTypes: []string{RelationshipHasProperties},
					ReferenceMode: ReferenceModeByValue, ValueTypes: []ValueType{ValueUIDRef},
					ConceptNames: []CodeKey{{CodeValue: "112040", CodingSchemeDesignator: "DCM"}},
					Cardinality:  Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
				},
				{
					RuleID: "referenced-segment", RelationshipTypes: []string{RelationshipInferredFrom},
					ReferenceMode: ReferenceModeByValue, ValueTypes: []ValueType{ValueImage},
					ConceptNames: []CodeKey{{CodeValue: "121191", CodingSchemeDesignator: "DCM"}},
					Cardinality:  Cardinality{Min: 1, Max: 1}, Requirement: RequirementOptional,
				},
				{
					RuleID: "measurement", RelationshipTypes: []string{RelationshipContains},
					ReferenceMode: ReferenceModeByValue, ValueTypes: []ValueType{ValueNum},
					Cardinality: Cardinality{Min: 1}, Requirement: RequirementOptional,
					Children: []TemplateRow{
						{
							RuleID: "measurement-image", RelationshipTypes: []string{RelationshipInferredFrom},
							ReferenceMode: ReferenceModeByValue, ValueTypes: []ValueType{ValueImage},
							Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementOptional,
						},
						{
							RuleID: "measurement-spatial", RelationshipTypes: []string{RelationshipInferredFrom},
							ReferenceMode: ReferenceModeByValue, ValueTypes: []ValueType{ValueSCoord3D},
							Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementOptional,
						},
					},
				},
			},
		}},
	}
}
