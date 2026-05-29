package sr

import (
	"errors"
	"reflect"
	"testing"
)

func TestTemplateMetadataOptInRoundTripPreservesEnhancedCodesAndTemplates(t *testing.T) {
	document := &Document{
		SOPClassUID: ComprehensiveSRStorage, SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.628.20",
		ContentDate: "20260807", ContentTime: "120000",
		Title: CodedEntry{CodeValue: "report", CodingSchemeDesignator: "99TEST", CodeMeaning: "Report"},
		Content: []ContentItem{
			{
				ValueType: ValueCode, RelationshipType: RelationshipContains,
				ConceptName: CodedEntry{CodeValue: "finding", CodingSchemeDesignator: "99TEST", CodeMeaning: "Finding"},
				Code:        CodedEntry{CodingSchemeDesignator: "SCT", CodeMeaning: "Long coded value"},
			},
			{
				ValueType: ValueNum, RelationshipType: RelationshipContains,
				Measurement:           &Measurement{Value: 12.5, Units: CodedEntry{CodeMeaning: "URI unit"}},
				NumericValueQualifier: CodedEntry{CodeValue: "114000", CodingSchemeDesignator: "DCM", CodeMeaning: "Not a number"},
			},
			{
				ValueType: ValueContainer, RelationshipType: RelationshipContains,
				ContinuityOfContent: "SEPARATE",
			},
		},
	}
	metadata := TemplateValidationMetadata{
		CodeContexts: []ContentItemCodeContexts{
			{
				Path: "1", ConceptName: CodeContext{
					CodingSchemeVersion: "2026", ContextIdentifier: "9000", MappingResource: "DCMR",
					ContextGroupVersion: "20260101", ExtensionFlag: "N", ContextUID: "1.2.826.0.1.3680043.9.7433.628.23",
					MappingResourceUID: "1.2.840.10008.8.1.1", MappingResourceName: "DICOM Content Mapping Resource",
				},
				ConceptNameEquivalentCodes: []EquivalentCodeEntry{{
					Code: CodedEntry{CodeValue: "report-alt", CodingSchemeDesignator: "99TEST", CodeMeaning: "Alternate report"},
				}},
			},
			{
				Path: "1.1", Value: CodeContext{
					LongCodeValue: "long-code-value-123", CodingSchemeVersion: "2026",
					ContextIdentifier: "100", MappingResource: "DCMR", ContextGroupVersion: "20260101", ExtensionFlag: "N",
				},
				ValueEquivalentCodes: []EquivalentCodeEntry{{
					Code: CodedEntry{CodeValue: "alt", CodingSchemeDesignator: "99TEST", CodeMeaning: "Alternate"},
				}},
			},
			{
				Path: "1.2", MeasurementUnits: CodeContext{
					URNCodeValue: "urn:example:unit", ContextIdentifier: "101", MappingResource: "DCMR",
					ContextGroupVersion: "20260101", ContextGroupLocalVersion: "20260807",
					ExtensionFlag: "Y", ExtensionCreatorUID: "1.2.826.0.1.3680043.9.7433.628.21",
				},
				NumericValueQualifier: CodeContext{CodingSchemeVersion: "2026"},
				NumericValueQualifierEquivalents: []EquivalentCodeEntry{{
					Code: CodedEntry{CodeValue: "qual-alt", CodingSchemeDesignator: "99TEST", CodeMeaning: "Alternate qualifier"},
				}},
			},
		},
		TemplateIdentifications: []TemplateIdentification{
			{Path: "1", MappingResource: "DCMR", MappingResourceUID: "1.2.840.10008.8.1.1", Identifier: "1000", Version: "20260101"},
			{Path: "1.3", MappingResource: "99TEST", MappingResourceUID: "1.2.826.0.1.3680043.9.7433.628.22", Identifier: "LOCAL", Version: "20260807"},
		},
	}

	dataset, err := document.DatasetWithTemplateMetadata(metadata)
	if err != nil {
		t.Fatalf("DatasetWithTemplateMetadata: %v", err)
	}
	result, err := ReadDocumentWithOptions(dataset, DefaultReadOptions())
	if err != nil {
		t.Fatalf("ReadDocumentWithOptions: %v", err)
	}
	if !reflect.DeepEqual(result.TemplateMetadata, metadata) {
		t.Fatalf("decoded metadata = %#v, want %#v", result.TemplateMetadata, metadata)
	}

	rewritten, err := result.Document.DatasetWithTemplateMetadata(result.TemplateMetadata)
	if err != nil {
		t.Fatalf("rewrite metadata: %v", err)
	}
	roundTrip, err := ReadDocumentWithOptions(rewritten, DefaultReadOptions())
	if err != nil {
		t.Fatalf("read rewritten metadata: %v", err)
	}
	if !reflect.DeepEqual(roundTrip.TemplateMetadata, metadata) {
		t.Fatalf("round-trip metadata = %#v, want %#v", roundTrip.TemplateMetadata, metadata)
	}
}

func TestTemplateMetadataWritesEquivalentCodesWithoutPrimaryContext(t *testing.T) {
	document := &Document{
		SOPClassUID: ComprehensiveSRStorage, ContentDate: "20260807", ContentTime: "120000",
		Title: CodedEntry{CodeValue: "report", CodingSchemeDesignator: "99TEST", CodeMeaning: "Report"},
	}
	metadata := TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{{
		Path: "1", ConceptNameEquivalentCodes: []EquivalentCodeEntry{{
			Code: CodedEntry{CodeValue: "alternate", CodingSchemeDesignator: "99TEST", CodeMeaning: "Alternate"},
		}},
	}}}
	dataset, err := document.DatasetWithTemplateMetadata(metadata)
	if err != nil {
		t.Fatalf("DatasetWithTemplateMetadata: %v", err)
	}
	result, err := ReadDocumentWithOptions(dataset, DefaultReadOptions())
	if err != nil {
		t.Fatalf("ReadDocumentWithOptions: %v", err)
	}
	if !reflect.DeepEqual(result.TemplateMetadata, metadata) {
		t.Fatalf("metadata = %#v, want %#v", result.TemplateMetadata, metadata)
	}
}

func TestReadDocumentBoundsEquivalentCodeMetadata(t *testing.T) {
	document := &Document{
		SOPClassUID: ComprehensiveSRStorage, ContentDate: "20260807", ContentTime: "120000",
		Title: CodedEntry{CodeValue: "report", CodingSchemeDesignator: "99TEST", CodeMeaning: "Report"},
	}
	metadata := TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{{
		Path: "1", ConceptNameEquivalentCodes: []EquivalentCodeEntry{
			{Code: CodedEntry{CodeValue: "alt-1", CodingSchemeDesignator: "99TEST", CodeMeaning: "Alternate one"}},
			{Code: CodedEntry{CodeValue: "alt-2", CodingSchemeDesignator: "99TEST", CodeMeaning: "Alternate two"}},
		},
	}}}
	dataset, err := document.DatasetWithTemplateMetadata(metadata)
	if err != nil {
		t.Fatalf("DatasetWithTemplateMetadata: %v", err)
	}
	options := DefaultReadOptions()
	options.MaxItems = 1
	if _, err := ReadDocumentWithOptions(dataset, options); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("ReadDocumentWithOptions error = %v, want ErrResourceLimitExceeded", err)
	}
}

func TestReadDocumentBoundsAggregateTemplateMetadata(t *testing.T) {
	document := &Document{
		SOPClassUID: ComprehensiveSRStorage, ContentDate: "20260807", ContentTime: "120000",
		Title: CodedEntry{CodeValue: "report", CodingSchemeDesignator: "99TEST", CodeMeaning: "Report"},
	}
	metadata := TemplateValidationMetadata{
		CodeContexts: []ContentItemCodeContexts{{Path: "1", ConceptName: CodeContext{CodingSchemeVersion: "1"}}},
		TemplateIdentifications: []TemplateIdentification{{
			Path: "1", MappingResource: "99TEST", Identifier: "ROOT",
		}},
	}
	dataset, err := document.DatasetWithTemplateMetadata(metadata)
	if err != nil {
		t.Fatalf("DatasetWithTemplateMetadata: %v", err)
	}
	options := DefaultReadOptions()
	options.MaxItems = 1
	if _, err := ReadDocumentWithOptions(dataset, options); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("ReadDocumentWithOptions error = %v, want aggregate metadata resource limit", err)
	}
}

func TestTemplateMetadataWriterRejectsDuplicateAndUnknownPaths(t *testing.T) {
	document := &Document{
		SOPClassUID: ComprehensiveSRStorage, ContentDate: "20260807", ContentTime: "120000",
		Title: CodedEntry{CodeValue: "report", CodingSchemeDesignator: "99TEST", CodeMeaning: "Report"},
	}
	context := ContentItemCodeContexts{Path: "1", ConceptName: CodeContext{CodingSchemeVersion: "1"}}
	_, err := document.DatasetWithTemplateMetadata(TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{context, context}})
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("duplicate metadata error = %v, want ErrTemplateValidation", err)
	}

	context.Path = "1.9"
	_, err = document.DatasetWithTemplateMetadata(TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{context}})
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("unknown path error = %v, want ErrTemplateValidation", err)
	}

	context.Path = "1"
	context.ConceptName = CodeContext{
		ContextIdentifier: "100", MappingResource: "DCMR", ContextGroupVersion: "20260101", ExtensionFlag: "Y",
	}
	_, err = document.DatasetWithTemplateMetadata(TemplateValidationMetadata{CodeContexts: []ContentItemCodeContexts{context}})
	if !errors.Is(err, ErrTemplateValidation) {
		t.Fatalf("incomplete extension metadata error = %v, want ErrTemplateValidation", err)
	}
}

func TestCodeKeyAllowsURNWithoutCodingSchemeAndEnforcesLongForm(t *testing.T) {
	if !validCodeKey(CodeKey{URNCodeValue: "urn:example:code"}) {
		t.Fatal("URN code without Coding Scheme Designator should be valid")
	}
	if validCodeKey(CodeKey{CodeValue: "12345678901234567", CodingSchemeDesignator: "99TEST"}) {
		t.Fatal("Code Value longer than 16 characters should require Long Code Value")
	}
	if validCodeKey(CodeKey{LongCodeValue: "short", CodingSchemeDesignator: "99TEST"}) {
		t.Fatal("Long Code Value should not encode a short non-URN code")
	}
	if !validCodeKey(CodeKey{CodeValue: "ééééééééé", CodingSchemeDesignator: "99TEST"}) {
		t.Fatal("Code Value length should count Unicode characters, not UTF-8 bytes")
	}
	if validCodeKey(CodeKey{LongCodeValue: "ééééééééé", CodingSchemeDesignator: "99TEST"}) {
		t.Fatal("Long Code Value should require more than 16 Unicode characters")
	}
}
