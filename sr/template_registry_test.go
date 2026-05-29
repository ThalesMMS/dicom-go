package sr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestTemplateRegistryFreezesInputsAndLookups(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "measurement", Version: "1"}
	definition := TemplateDefinition{
		Key: key,
		Rows: []TemplateRow{{
			RuleID:            "group",
			RelationshipTypes: []string{RelationshipContains},
			ValueTypes:        []ValueType{ValueContainer},
		}},
	}
	registry, err := NewTemplateRegistry([]TemplateDefinition{definition}, nil)
	if err != nil {
		t.Fatalf("NewTemplateRegistry: %v", err)
	}

	definition.Rows[0].RuleID = "mutated-input"
	definition.Rows[0].RelationshipTypes[0] = RelationshipInferredFrom
	first, ok := registry.LookupTemplate(key)
	if !ok {
		t.Fatal("LookupTemplate did not find registered template")
	}
	if first.Rows[0].RuleID != "group" || first.Rows[0].RelationshipTypes[0] != RelationshipContains {
		t.Fatalf("registry retained caller-owned input: %+v", first.Rows[0])
	}

	first.Rows[0].RuleID = "mutated-lookup"
	first.Rows[0].RelationshipTypes[0] = RelationshipSelectedFrom
	second, ok := registry.LookupTemplate(key)
	if !ok {
		t.Fatal("second LookupTemplate did not find registered template")
	}
	if second.Rows[0].RuleID != "group" || second.Rows[0].RelationshipTypes[0] != RelationshipContains {
		t.Fatalf("lookup returned registry-owned state: %+v", second.Rows[0])
	}
}

func TestTemplateValidatorChecksRowsCardinalityAndUnknownContent(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "simple", Version: "1"}
	groupConcept := CodeKey{CodeValue: "group", CodingSchemeDesignator: "99TEST"}
	textConcept := CodeKey{CodeValue: "text", CodingSchemeDesignator: "99TEST"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{
		Key: key,
		Rows: []TemplateRow{{
			RuleID:            "group",
			RelationshipTypes: []string{RelationshipContains},
			ReferenceMode:     ReferenceModeByValue,
			ValueTypes:        []ValueType{ValueContainer},
			ConceptNames:      []CodeKey{groupConcept},
			Cardinality:       Cardinality{Min: 1, Max: 1},
			Requirement:       RequirementRequired,
			Children: []TemplateRow{{
				RuleID:            "text",
				RelationshipTypes: []string{RelationshipContains},
				ReferenceMode:     ReferenceModeByValue,
				ValueTypes:        []ValueType{ValueText},
				ConceptNames:      []CodeKey{textConcept},
				Cardinality:       Cardinality{Min: 1, Max: 1},
				Requirement:       RequirementRequired,
			}},
		}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{{
		RelationshipType: RelationshipContains,
		ValueType:        ValueContainer,
		ConceptName:      codedEntry(groupConcept),
		Children: []ContentItem{{
			RelationshipType: RelationshipContains,
			ValueType:        ValueText,
			ConceptName:      codedEntry(textConcept),
			Text:             "not included in diagnostics",
		}},
	}}}

	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatalf("NewTemplateValidator: %v", err)
	}
	report, err := validator.Validate(context.Background(), document, key)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("Validate(valid) = %+v, %v; want clean report", report, err)
	}

	document.Content[0].Children = append(document.Content[0].Children, ContentItem{
		RelationshipType: RelationshipContains,
		ValueType:        ValueText,
		ConceptName:      CodedEntry{CodeValue: "unknown", CodingSchemeDesignator: "99TEST", CodeMeaning: "Patient^Name"},
		Text:             "Patient^Name",
	})
	report, err = validator.Validate(context.Background(), document, key)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("Validate(invalid) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeUnknownContent)
	if strings.Contains(templateReportText(report), "Patient^Name") {
		t.Fatalf("diagnostics leaked content: %+v", report)
	}

	warnValidator, err := NewTemplateValidator(registry, TemplateValidationOptions{Mode: ValidationModeWarn})
	if err != nil {
		t.Fatal(err)
	}
	warnReport, err := warnValidator.Validate(context.Background(), document, key)
	if err != nil {
		t.Fatalf("warn validation returned error: %v", err)
	}
	for _, finding := range warnReport.Findings {
		if finding.Severity != DiagnosticWarning {
			t.Fatalf("warn finding severity = %q, want warning", finding.Severity)
		}
	}
}

func TestTemplateValidatorPrefersSpecificRequiredRowWhenOrderIsNotSignificant(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "overlap", Version: "1"}
	specific := CodeKey{CodeValue: "specific", CodingSchemeDesignator: "99TEST"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: key, Rows: []TemplateRow{
		{
			RuleID: "generic", ValueTypes: []ValueType{ValueText},
			Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementOptional,
		},
		{
			RuleID: "specific", ValueTypes: []ValueType{ValueText}, ConceptNames: []CodeKey{specific},
			Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
		},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{{ValueType: ValueText, ConceptName: codedEntry(specific)}}}
	report, err := validator.Validate(context.Background(), document, key)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("Validate(overlapping rows) = %+v, %v; want specific required row selected", report, err)
	}
}

func TestTemplateValidatorExpandsTemplateAndContextGroupIncludes(t *testing.T) {
	unitsKey := ContextGroupKey{MappingResource: "TEST", Identifier: "units", Version: "1"}
	lengthUnitsKey := ContextGroupKey{MappingResource: "TEST", Identifier: "length-units", Version: "1"}
	baseKey := TemplateKey{MappingResource: "TEST", Identifier: "base", Version: "1"}
	reportKey := TemplateKey{MappingResource: "TEST", Identifier: "report", Version: "1"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{
		{Key: baseKey, Rows: []TemplateRow{{
			RuleID:            "measurement",
			RelationshipTypes: []string{RelationshipContains},
			ReferenceMode:     ReferenceModeByValue,
			ValueTypes:        []ValueType{ValueNum},
			ContextGroup:      &unitsKey,
			Cardinality:       Cardinality{Min: 1, Max: 1},
			Requirement:       RequirementRequired,
		}}},
		{Key: reportKey, Rows: []TemplateRow{{RuleID: "include-base", Include: &baseKey}}},
	}, []ContextGroupDefinition{
		{Key: lengthUnitsKey, Codes: []CodeKey{{CodeValue: "mm", CodingSchemeDesignator: "UCUM"}}},
		{Key: unitsKey, Includes: []ContextGroupKey{lengthUnitsKey}},
	})
	if err != nil {
		t.Fatalf("NewTemplateRegistry: %v", err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{{
		RelationshipType: RelationshipContains,
		ValueType:        ValueNum,
		Measurement: &Measurement{
			Value: 12, Units: CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"},
		},
	}}}
	report, err := validator.Validate(context.Background(), document, reportKey)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("Validate(included template/CID) = %+v, %v; want clean report", report, err)
	}

	missingReport, err := validator.Validate(context.Background(), &Document{}, reportKey)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("Validate(missing included row) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, missingReport, TemplateCodeCardinality)
	if missingReport.Findings[0].RuleID != "include-base.measurement" {
		t.Fatalf("included cardinality RuleID = %q, want include-base.measurement", missingReport.Findings[0].RuleID)
	}
}

func TestTemplateIncludesPreserveScopeRequirementAndCardinality(t *testing.T) {
	baseKey := TemplateKey{MappingResource: "TEST", Identifier: "include-base", Version: "1"}
	optionalKey := TemplateKey{MappingResource: "TEST", Identifier: "include-optional", Version: "1"}
	conditionalKey := TemplateKey{MappingResource: "TEST", Identifier: "include-conditional", Version: "1"}
	repeatedKey := TemplateKey{MappingResource: "TEST", Identifier: "include-repeated", Version: "1"}
	namespacedKey := TemplateKey{MappingResource: "TEST", Identifier: "include-namespaced", Version: "1"}
	conditionActive := false
	registry, err := NewTemplateRegistry([]TemplateDefinition{
		{Key: baseKey, Rows: []TemplateRow{{
			RuleID: "measurement", RelationshipTypes: []string{RelationshipContains},
			ValueTypes: []ValueType{ValueText}, Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
		}}},
		{Key: optionalKey, Rows: []TemplateRow{{
			RuleID: "optional", Include: &baseKey, Requirement: RequirementOptional,
		}}},
		{Key: conditionalKey, Rows: []TemplateRow{{
			RuleID: "conditional", Include: &baseKey, Requirement: RequirementConditional, ConditionID: "enabled",
			Condition: func(context.Context, TemplateConditionContext) (bool, error) { return conditionActive, nil },
		}}},
		{Key: repeatedKey, Rows: []TemplateRow{{
			RuleID: "repeat", Include: &baseKey, RelationshipTypes: []string{RelationshipInferredFrom},
			Cardinality: Cardinality{Min: 2, Max: 2}, Requirement: RequirementRequired,
		}}},
		{Key: namespacedKey, Rows: []TemplateRow{
			{RuleID: "first", Include: &baseKey},
			{RuleID: "second", Include: &baseKey},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := validator.Validate(context.Background(), &Document{}, optionalKey); err != nil || len(report.Findings) != 0 {
		t.Fatalf("optional include = %+v, %v; want clean report", report, err)
	}
	if report, err := validator.Validate(context.Background(), &Document{}, conditionalKey); err != nil || len(report.Findings) != 0 {
		t.Fatalf("inactive conditional include = %+v, %v; want clean report", report, err)
	}
	conditionActive = true
	conditionalReport, err := validator.Validate(context.Background(), &Document{}, conditionalKey)
	if !errors.Is(err, ErrTemplateValidation) || len(conditionalReport.Findings) != 1 || conditionalReport.Findings[0].RuleID != "conditional.measurement" {
		t.Fatalf("active conditional include = %+v, %v", conditionalReport, err)
	}
	repeatedDocument := &Document{Content: []ContentItem{
		{RelationshipType: RelationshipInferredFrom, ValueType: ValueText},
		{RelationshipType: RelationshipInferredFrom, ValueType: ValueText},
	}}
	if report, err := validator.Validate(context.Background(), repeatedDocument, repeatedKey); err != nil || len(report.Findings) != 0 {
		t.Fatalf("repeated include = %+v, %v; want clean report", report, err)
	}
	repeatedDocument.Content = repeatedDocument.Content[:1]
	repeatedReport, err := validator.Validate(context.Background(), repeatedDocument, repeatedKey)
	if !errors.Is(err, ErrTemplateValidation) || len(repeatedReport.Findings) != 1 || repeatedReport.Findings[0].RuleID != "repeat.measurement" {
		t.Fatalf("short repeated include = %+v, %v", repeatedReport, err)
	}
	namespacedReport, err := validator.Validate(context.Background(), &Document{}, namespacedKey)
	if !errors.Is(err, ErrTemplateValidation) || len(namespacedReport.Findings) != 2 {
		t.Fatalf("namespaced includes = %+v, %v", namespacedReport, err)
	}
	gotRuleIDs := map[string]bool{}
	for _, finding := range namespacedReport.Findings {
		gotRuleIDs[finding.RuleID] = true
	}
	if !gotRuleIDs["first.measurement"] || !gotRuleIDs["second.measurement"] {
		t.Fatalf("namespaced include RuleIDs = %#v", gotRuleIDs)
	}
}

func TestTemplateIncludeCombinesConditionalRequirements(t *testing.T) {
	includedKey := TemplateKey{MappingResource: "TEST", Identifier: "conditional-row", Version: "1"}
	includingKey := TemplateKey{MappingResource: "TEST", Identifier: "conditional-include", Version: "1"}
	includeActive := true
	rowActive := true
	registry, err := NewTemplateRegistry([]TemplateDefinition{
		{Key: includedKey, Rows: []TemplateRow{{
			RuleID: "measurement", Requirement: RequirementConditional, ConditionID: "row-enabled",
			Condition: func(context.Context, TemplateConditionContext) (bool, error) { return rowActive, nil },
		}}},
		{Key: includingKey, Rows: []TemplateRow{{
			RuleID: "include", Include: &includedKey, Requirement: RequirementConditional, ConditionID: "include-enabled",
			Condition: func(context.Context, TemplateConditionContext) (bool, error) { return includeActive, nil },
		}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := registry.compiledTemplates[includingKey].Rows
	if len(rows) != 1 {
		t.Fatalf("compiled rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Requirement != RequirementConditional || row.ConditionID != "include-enabled+row-enabled" {
		t.Fatalf("combined requirement = %v %q", row.Requirement, row.ConditionID)
	}
	active, err := row.Condition(context.Background(), TemplateConditionContext{})
	if err != nil || !active {
		t.Fatalf("combined condition with both predicates active = %t, %v", active, err)
	}
	includeActive = false
	active, err = row.Condition(context.Background(), TemplateConditionContext{})
	if err != nil || active {
		t.Fatalf("combined condition with inactive include = %t, %v", active, err)
	}
	includeActive = true
	rowActive = false
	active, err = row.Condition(context.Background(), TemplateConditionContext{})
	if err != nil || active {
		t.Fatalf("combined condition with inactive row = %t, %v", active, err)
	}
}

func TestTemplateValidatorUsesAllCodeFormsAndValidatesExtensionMetadata(t *testing.T) {
	templateKey := TemplateKey{MappingResource: "TEST", Identifier: "coded", Version: "1"}
	groupKey := ContextGroupKey{MappingResource: "TEST", Identifier: "100", Version: "20260101"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: templateKey, Rows: []TemplateRow{{
		RuleID:            "coded-value",
		RelationshipTypes: []string{RelationshipContains},
		ValueTypes:        []ValueType{ValueCode},
		ContextGroup:      &groupKey,
		Cardinality:       Cardinality{Min: 1, Max: 1},
		Requirement:       RequirementRequired,
	}}}}, []ContextGroupDefinition{{
		Key: groupKey, Extensible: true,
		Codes: []CodeKey{{LongCodeValue: "long-standard-code", CodingSchemeDesignator: "SCT", CodingSchemeVersion: "2026"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{{
		RelationshipType: RelationshipContains,
		ValueType:        ValueCode,
		Code:             CodedEntry{CodingSchemeDesignator: "SCT", CodeMeaning: "Patient^Name"},
	}}}
	standardMetadata := TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{{
		Path: "1.1",
		Value: CodeContext{
			LongCodeValue: "long-standard-code", CodingSchemeVersion: "2026",
			ContextIdentifier: "100", MappingResource: "TEST", ContextGroupVersion: "20260101", ExtensionFlag: "N",
		},
	}}}
	report, err := validator.ValidateWithMetadata(context.Background(), document, templateKey, standardMetadata)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("ValidateWithMetadata(standard long code) = %+v, %v; want clean report", report, err)
	}

	document.Content[0].Code = CodedEntry{CodingSchemeDesignator: "99LOCAL"}
	extensionMetadata := TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{{
		Path: "1.1",
		Value: CodeContext{
			URNCodeValue: "urn:oid:1.2.3.4", ContextIdentifier: "100", MappingResource: "TEST",
			ContextGroupVersion: "20260101", ExtensionFlag: "Y", ContextGroupLocalVersion: "20260807",
			ExtensionCreatorUID: "1.2.826.0.1.3680043.9.999",
		},
	}}}
	report, err = validator.ValidateWithMetadata(context.Background(), document, templateKey, extensionMetadata)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("ValidateWithMetadata(valid extension) = %+v, %v; want clean report", report, err)
	}

	extensionMetadata.CodeContexts[0].Value.ContextGroupLocalVersion = ""
	report, err = validator.ValidateWithMetadata(context.Background(), document, templateKey, extensionMetadata)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("ValidateWithMetadata(incomplete extension) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeCodeContext)
}

func TestTemplateValidatorRejectsAmbiguousOrUnboundedMetadata(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "metadata", Version: "1"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: key}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	duplicate := TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{{Path: "1"}, {Path: "1"}}}
	report, err := validator.ValidateWithMetadata(context.Background(), &Document{}, key, duplicate)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("ValidateWithMetadata(duplicate) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeCodeContext)

	limited, err := NewTemplateValidator(registry, TemplateValidationOptions{MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	metadata := TemplateValidationMetadata{TemplateIdentifications: []TemplateIdentification{{Path: "1"}, {Path: "1.1"}}}
	report, err = limited.ValidateWithMetadata(context.Background(), &Document{}, key, metadata)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("ValidateWithMetadata(unbounded) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeResourceLimit)
}

func TestTemplateRegistryRejectsAmbiguousCodeIdentity(t *testing.T) {
	_, err := NewTemplateRegistry(nil, []ContextGroupDefinition{{
		Key: ContextGroupKey{MappingResource: "TEST", Identifier: "1", Version: "1"},
		Codes: []CodeKey{{
			CodeValue: "short", LongCodeValue: "long", CodingSchemeDesignator: "99TEST",
		}},
	}})
	if !errors.Is(err, ErrInvalidTemplateRegistry) {
		t.Fatalf("NewTemplateRegistry error = %v, want ambiguous CodeKey rejection", err)
	}
}

func TestTemplateValidatorChecksConditionsReferenceModeAndSignificantOrder(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "ordered", Version: "1"}
	conceptA := CodeKey{CodeValue: "a", CodingSchemeDesignator: "99TEST"}
	conceptB := CodeKey{CodeValue: "b", CodingSchemeDesignator: "99TEST"}
	conditionCalled := false
	registry, err := NewTemplateRegistry([]TemplateDefinition{{
		Key: key, OrderSignificant: true,
		Rows: []TemplateRow{
			{
				RuleID: "first", RelationshipTypes: []string{RelationshipContains}, ValueTypes: []ValueType{ValueText},
				ConceptNames: []CodeKey{conceptA}, Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
			},
			{
				RuleID: "second", RelationshipTypes: []string{RelationshipContains}, ValueTypes: []ValueType{ValueText},
				ConceptNames: []CodeKey{conceptB}, Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
			},
			{
				RuleID: "conditional-reference", ReferenceMode: ReferenceModeByReference,
				RelationshipTypes: []string{RelationshipInferredFrom}, Cardinality: Cardinality{Min: 1, Max: 1},
				Requirement: RequirementConditional, ConditionID: "needs-reference",
				Condition: func(ctx context.Context, input TemplateConditionContext) (bool, error) {
					conditionCalled = true
					return input.SOPClassUID == ComprehensiveSRStorage, ctx.Err()
				},
			},
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{
		SOPClassUID: ComprehensiveSRStorage,
		Content: []ContentItem{
			{RelationshipType: RelationshipContains, ValueType: ValueText, ConceptName: codedEntry(conceptB)},
			{RelationshipType: RelationshipContains, ValueType: ValueText, ConceptName: codedEntry(conceptA)},
			{RelationshipType: RelationshipInferredFrom, ValueType: ValueImage},
		},
	}
	report, err := validator.Validate(context.Background(), document, key)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("Validate error = %v, want ErrTemplateValidation", err)
	}
	if !conditionCalled {
		t.Fatal("conditional callback was not evaluated")
	}
	assertTemplateFinding(t, report, TemplateCodeOrder)
	assertTemplateFinding(t, report, TemplateCodeReferenceMode)
}

func TestTemplateValidatorReportsUnsupportedConditionWithoutCallingUntrustedText(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "condition", Version: "1"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: key, Rows: []TemplateRow{{
		RuleID: "safe-rule", Requirement: RequirementConditional, ConditionID: "Patient^Name",
	}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := validator.Validate(context.Background(), &Document{}, key)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("Validate error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeConditionUnsupported)
	if strings.Contains(templateReportText(report), "Patient^Name") {
		t.Fatalf("condition identifier leaked into diagnostics: %+v", report)
	}
}

func TestTemplateValidatorAppliesByReferenceRowConstraintsToTarget(t *testing.T) {
	templateKey := TemplateKey{MappingResource: "TEST", Identifier: "by-reference", Version: "1"}
	groupKey := ContextGroupKey{MappingResource: "TEST", Identifier: "codes", Version: "1"}
	targetConcept := CodeKey{CodeValue: "target", CodingSchemeDesignator: "99TEST"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: templateKey, Rows: []TemplateRow{
		{
			RuleID: "target", RelationshipTypes: []string{RelationshipContains}, ReferenceMode: ReferenceModeByValue,
			ValueTypes: []ValueType{ValueCode}, ConceptNames: []CodeKey{targetConcept},
			Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
		},
		{
			RuleID: "reference", RelationshipTypes: []string{RelationshipInferredFrom}, ReferenceMode: ReferenceModeByReference,
			ValueTypes: []ValueType{ValueCode}, ConceptNames: []CodeKey{targetConcept}, ContextGroup: &groupKey,
			Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
		},
	}}}, []ContextGroupDefinition{{
		Key: groupKey, Codes: []CodeKey{{CodeValue: "member", CodingSchemeDesignator: "99TEST"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{
		{
			RelationshipType: RelationshipContains, ValueType: ValueCode, ConceptName: codedEntry(targetConcept),
			Code: CodedEntry{CodeValue: "member", CodingSchemeDesignator: "99TEST"},
		},
		{
			RelationshipType:                RelationshipInferredFrom,
			ReferencedContentItemIdentifier: ContentItemIdentifier{1, 1},
		},
	}}
	report, err := validator.Validate(context.Background(), document, templateKey)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("Validate(valid by-reference target) = %+v, %v; want clean report", report, err)
	}

	document.Content[0].ValueType = ValueText
	report, err = validator.Validate(context.Background(), document, templateKey)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("Validate(incompatible target) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, ReferenceCodeIncompatible)

	document.Content[1].ReferencedContentItemIdentifier = ContentItemIdentifier{1, 99}
	report, err = validator.Validate(context.Background(), document, templateKey)
	assertTemplateFinding(t, report, ReferenceCodeDangling)
}

func TestTemplateValidatorRecoversConditionPanicWithoutLeakingValue(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "panic", Version: "1"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: key, Rows: []TemplateRow{{
		RuleID: "condition", Requirement: RequirementConditional, ConditionID: "safe-condition",
		Condition: func(context.Context, TemplateConditionContext) (bool, error) {
			panic("Patient^Name")
		},
	}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := validator.Validate(context.Background(), &Document{}, key)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("Validate(panicking condition) error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeConditionFailed)
	if strings.Contains(templateReportText(report), "Patient^Name") {
		t.Fatalf("panic value leaked into diagnostics: %+v", report)
	}
}

func TestTemplateValidatorEnforcesTraversalStepAndFindingBudgets(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "limits", Version: "1"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{
		Key: key,
		Rows: []TemplateRow{
			{RuleID: "first", ValueTypes: []ValueType{ValueCode}, Requirement: RequirementOptional},
			{RuleID: "second", ValueTypes: []ValueType{ValueText}, Requirement: RequirementOptional},
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{{
		ValueType: ValueContainer,
		Children:  []ContentItem{{ValueType: ValueText}, {ValueType: ValueText}},
	}}}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	report, err := validator.Validate(context.Background(), document, key)
	if !errors.Is(err, ErrTemplateValidation) || !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("bounded traversal = %+v, %v; want template and resource errors", report, err)
	}
	assertTemplateFinding(t, report, TemplateCodeResourceLimit)

	warnValidator, err := NewTemplateValidator(registry, TemplateValidationOptions{Mode: ValidationModeWarn, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	warnReport, err := warnValidator.Validate(context.Background(), document, key)
	if !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("warn bounded traversal = %+v, %v; want fatal resource error", warnReport, err)
	}
	if len(warnReport.Findings) == 0 || warnReport.Findings[0].Severity != DiagnosticError {
		t.Fatalf("warn bounded report = %+v, want error severity", warnReport)
	}

	findingValidator, err := NewTemplateValidator(registry, TemplateValidationOptions{MaxFindings: 1})
	if err != nil {
		t.Fatal(err)
	}
	report, err = findingValidator.Validate(context.Background(), document, key)
	if !errors.Is(err, ErrTemplateValidation) || !report.Truncated || len(report.Findings) != 1 {
		t.Fatalf("bounded findings = %+v, %v; want one finding and truncation", report, err)
	}

	stepValidator, err := NewTemplateValidator(registry, TemplateValidationOptions{MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	report, err = stepValidator.Validate(context.Background(), &Document{Content: []ContentItem{{ValueType: ValueText}}}, key)
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("step-limited validation error = %v, want ErrTemplateValidation", err)
	}
	assertTemplateFinding(t, report, TemplateCodeResourceLimit)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = stepValidator.Validate(cancelled, document, key)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validation error = %v, want context.Canceled", err)
	}
}

func TestTemplateRegistryBoundsIncludeExpansionDepth(t *testing.T) {
	templates := make([]TemplateDefinition, 34)
	for index := range templates {
		key := TemplateKey{MappingResource: "TEST", Identifier: fmt.Sprintf("depth-%d", index), Version: "1"}
		templates[index] = TemplateDefinition{Key: key}
		if index > 0 {
			include := templates[index-1].Key
			templates[index].Rows = []TemplateRow{{RuleID: fmt.Sprintf("include-%d", index), Include: &include}}
		}
	}
	_, err := NewTemplateRegistry(templates, nil)
	if !errors.Is(err, ErrInvalidTemplateRegistry) {
		t.Fatalf("NewTemplateRegistry(deep includes) error = %v, want ErrInvalidTemplateRegistry", err)
	}
}

func TestTemplateRegistryAndValidatorAreSafeForConcurrentUse(t *testing.T) {
	key := TemplateKey{MappingResource: "TEST", Identifier: "concurrent", Version: "1"}
	registry, err := NewTemplateRegistry([]TemplateDefinition{{Key: key, Rows: []TemplateRow{{
		RuleID: "text", ValueTypes: []ValueType{ValueText}, Cardinality: Cardinality{Min: 1, Max: 1}, Requirement: RequirementRequired,
	}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewTemplateValidator(registry, TemplateValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := &Document{Content: []ContentItem{{ValueType: ValueText}}}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 32)
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, ok := registry.LookupTemplate(key); !ok {
				errorsFound <- errors.New("lookup failed")
				return
			}
			report, err := validator.Validate(context.Background(), document, key)
			if err != nil || len(report.Findings) != 0 {
				errorsFound <- fmt.Errorf("validation = %+v, %v", report, err)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func codedEntry(key CodeKey) CodedEntry {
	return CodedEntry{CodeValue: key.CodeValue, CodingSchemeDesignator: key.CodingSchemeDesignator}
}

func assertTemplateFinding(t *testing.T, report ValidationReport, code string) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("report = %+v, want finding %q", report, code)
}

func templateReportText(report ValidationReport) string {
	var builder strings.Builder
	for _, finding := range report.Findings {
		builder.WriteString(finding.RuleID)
		builder.WriteString(finding.Code)
		builder.WriteString(finding.Message)
	}
	return builder.String()
}

func TestTemplateRegistryRejectsInvalidDefinitionGraphsWithoutEchoingKeys(t *testing.T) {
	templateA := TemplateKey{MappingResource: "TEST", Identifier: "Patient^Name", Version: "1"}
	templateB := TemplateKey{MappingResource: "TEST", Identifier: "included", Version: "1"}
	groupA := ContextGroupKey{MappingResource: "TEST", Identifier: "private-group", Version: "1"}
	groupB := ContextGroupKey{MappingResource: "TEST", Identifier: "included-group", Version: "1"}
	tests := []struct {
		name      string
		templates []TemplateDefinition
		groups    []ContextGroupDefinition
	}{
		{
			name: "duplicate template key",
			templates: []TemplateDefinition{
				{Key: templateA}, {Key: templateA},
			},
		},
		{
			name: "duplicate rule id",
			templates: []TemplateDefinition{{Key: templateA, Rows: []TemplateRow{
				{RuleID: "same"}, {RuleID: "same"},
			}}},
		},
		{
			name: "missing template include",
			templates: []TemplateDefinition{{Key: templateA, Rows: []TemplateRow{
				{RuleID: "include", Include: &templateB},
			}}},
		},
		{
			name: "cyclic template includes",
			templates: []TemplateDefinition{
				{Key: templateA, Rows: []TemplateRow{{RuleID: "a-to-b", Include: &templateB}}},
				{Key: templateB, Rows: []TemplateRow{{RuleID: "b-to-a", Include: &templateA}}},
			},
		},
		{
			name:   "missing context group include",
			groups: []ContextGroupDefinition{{Key: groupA, Includes: []ContextGroupKey{groupB}}},
		},
		{
			name: "cyclic context group includes",
			groups: []ContextGroupDefinition{
				{Key: groupA, Includes: []ContextGroupKey{groupB}},
				{Key: groupB, Includes: []ContextGroupKey{groupA}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTemplateRegistry(test.templates, test.groups)
			if !errors.Is(err, ErrInvalidTemplateRegistry) {
				t.Fatalf("NewTemplateRegistry error = %v, want ErrInvalidTemplateRegistry", err)
			}
			if strings.Contains(err.Error(), "Patient^Name") || strings.Contains(err.Error(), "private-group") {
				t.Fatalf("registry error leaked caller-controlled key: %q", err)
			}
		})
	}
}

func TestTemplateRegistryFreezesContextGroupsAndFunctionalUpdates(t *testing.T) {
	baseKey := ContextGroupKey{MappingResource: "TEST", Identifier: "units", Version: "20260101"}
	base := ContextGroupDefinition{
		Key:        baseKey,
		Extensible: false,
		Codes: []CodeKey{{
			CodeValue: "mm", CodingSchemeDesignator: "UCUM",
		}},
		Provenance: DefinitionProvenance{Source: "fixture://units", Checksum: "sha256:abc"},
	}
	registry, err := NewTemplateRegistry(nil, []ContextGroupDefinition{base})
	if err != nil {
		t.Fatalf("NewTemplateRegistry: %v", err)
	}
	base.Codes[0].CodeValue = "cm"

	lookup, ok := registry.LookupContextGroup(baseKey)
	if !ok || len(lookup.Codes) != 1 || lookup.Codes[0].CodeValue != "mm" {
		t.Fatalf("LookupContextGroup = %+v, %v; want frozen mm definition", lookup, ok)
	}
	lookup.Codes[0].CodeValue = "km"
	again, _ := registry.LookupContextGroup(baseKey)
	if again.Codes[0].CodeValue != "mm" {
		t.Fatalf("context lookup returned registry-owned state: %+v", again.Codes)
	}

	template := TemplateDefinition{Key: TemplateKey{MappingResource: "TEST", Identifier: "report", Version: "1"}}
	updated, err := registry.WithTemplate(template)
	if err != nil {
		t.Fatalf("WithTemplate: %v", err)
	}
	if _, ok := registry.LookupTemplate(template.Key); ok {
		t.Fatal("WithTemplate mutated the source registry")
	}
	if _, ok := updated.LookupTemplate(template.Key); !ok {
		t.Fatal("WithTemplate result does not contain the new template")
	}
}
