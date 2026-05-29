package sr

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestByReferenceRoundTripAndNavigation(t *testing.T) {
	document := validByReferenceDocument()
	dataset, err := document.Dataset()
	if err != nil {
		t.Fatalf("Dataset: %v", err)
	}
	items, ok := dataset.GetSequence(tagContentSequence)
	if !ok || len(items) != 3 {
		t.Fatalf("Content Sequence = %#v, want three items", items)
	}
	if items[0].Has(tagValueType) || items[0].Has(tagConceptNameCodeSeq) {
		t.Fatal("by-reference item contains a by-value macro")
	}
	referenceElement, ok := items[0].Get(tagReferencedContentItemIdentifier)
	if !ok || referenceElement.VR() != core.VRUL {
		t.Fatalf("Referenced Content Item Identifier = %#v, want UL", referenceElement)
	}
	identifier, ok := referenceElement.Value.(core.Uint32Value)
	if !ok || len(identifier) != 2 || identifier[0] != 1 || identifier[1] != 3 {
		t.Fatalf("Referenced Content Item Identifier = %#v, want [1 3]", referenceElement.Value)
	}

	read, err := ReadDocument(dataset)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if got := read.Content[0].ReferencedContentItemIdentifier; !got.equal(ContentItemIdentifier{1, 3}) {
		t.Fatalf("forward reference = %v, want 1.3", got)
	}
	if got := read.Content[2].Children[0].ReferencedContentItemIdentifier; !got.equal(ContentItemIdentifier{1, 2}) {
		t.Fatalf("backward reference = %v, want 1.2", got)
	}

	index, report, err := ResolveReferences(read, DefaultReferenceOptions())
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("ResolveReferences = report %#v, error %v", report, err)
	}
	edges, err := index.Edges()
	if err != nil || len(edges) != 2 {
		t.Fatalf("Edges = %#v, %v; want two", edges, err)
	}
	rootTargets, err := index.TargetsFrom(ContentItemIdentifier{1})
	if err != nil || len(rootTargets) != 1 || !rootTargets[0].Target.equal(ContentItemIdentifier{1, 3}) {
		t.Fatalf("TargetsFrom(root) = %#v, %v", rootTargets, err)
	}
	backReferences, err := index.ReferencesTo(ContentItemIdentifier{1, 2})
	if err != nil || len(backReferences) != 1 || !backReferences[0].Source.equal(ContentItemIdentifier{1, 3}) {
		t.Fatalf("ReferencesTo(1.2) = %#v, %v", backReferences, err)
	}
	target, err := index.TargetFor(ContentItemIdentifier{1, 1})
	if err != nil || !target.equal(ContentItemIdentifier{1, 3}) {
		t.Fatalf("TargetFor(1.1) = %v, %v", target, err)
	}

	// Mutating a returned path must not alter the immutable index.
	target[1] = 99
	target, err = index.TargetFor(ContentItemIdentifier{1, 1})
	if err != nil || !target.equal(ContentItemIdentifier{1, 3}) {
		t.Fatalf("TargetFor after caller mutation = %v, %v", target, err)
	}
}

func TestByReferencePathUsesTransferSyntaxByteOrder(t *testing.T) {
	for _, syntax := range []transfer.Syntax{
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	} {
		t.Run(syntax.UID, func(t *testing.T) {
			dataset, err := validByReferenceDocument().Dataset()
			if err != nil {
				t.Fatalf("Dataset: %v", err)
			}
			var encoded bytes.Buffer
			if err := object.WriteDataSet(&encoded, dataset, syntax); err != nil {
				t.Fatalf("WriteDataSet: %v", err)
			}
			decoded, err := object.ReadDataSet(bytes.NewReader(encoded.Bytes()), syntax)
			if err != nil {
				t.Fatalf("ReadDataSet: %v", err)
			}
			document, err := ReadDocument(decoded)
			if err != nil {
				t.Fatalf("ReadDocument: %v", err)
			}
			if got := document.Content[0].ReferencedContentItemIdentifier; !got.equal(ContentItemIdentifier{1, 3}) {
				t.Fatalf("reference = %v, want 1.3", got)
			}
		})
	}
}

func TestReferenceIndexRejectsStructuralMutation(t *testing.T) {
	document := validByReferenceDocument()
	index, _, err := ResolveReferences(document, DefaultReferenceOptions())
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	document.Content[1], document.Content[2] = document.Content[2], document.Content[1]
	if _, err := index.Edges(); !errors.Is(err, ErrStaleReferenceIndex) {
		t.Fatalf("Edges after reorder error = %v, want ErrStaleReferenceIndex", err)
	}
	if _, _, err := ResolveReferences(document, DefaultReferenceOptions()); !errors.Is(err, ErrReferenceResolution) {
		t.Fatalf("ResolveReferences after reorder error = %v, want explicit rebind failure", err)
	}
}

func TestReferenceResolutionDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		document *Document
		code     string
	}{
		{
			name: "invalid path",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{RelationshipType: RelationshipHasObsContext, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 0}},
			}),
			code: ReferenceCodeInvalidPath,
		},
		{
			name: "dangling",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{RelationshipType: RelationshipHasObsContext, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 9}},
				textContentItem("target"),
			}),
			code: ReferenceCodeDangling,
		},
		{
			name: "self",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{ValueType: ValueText, RelationshipType: RelationshipContains, Children: []ContentItem{{
					RelationshipType: RelationshipHasProperties, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 1},
				}}},
			}),
			code: ReferenceCodeSelf,
		},
		{
			name: "ancestor",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{ValueType: ValueContainer, RelationshipType: RelationshipContains, Children: []ContentItem{
					{ValueType: ValueText, RelationshipType: RelationshipContains, Children: []ContentItem{{
						RelationshipType: RelationshipHasProperties, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 1},
					}}},
				}},
			}),
			code: ReferenceCodeAncestor,
		},
		{
			name: "cycle",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{ValueType: ValueText, RelationshipType: RelationshipContains, Children: []ContentItem{{
					RelationshipType: RelationshipHasProperties, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2},
				}}},
				{ValueType: ValueText, RelationshipType: RelationshipContains, Children: []ContentItem{{
					RelationshipType: RelationshipHasProperties, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 1},
				}}},
			}),
			code: ReferenceCodeCycle,
		},
		{
			name: "SOP profile",
			document: referenceDocument(EnhancedSRStorage, []ContentItem{
				{RelationshipType: RelationshipHasObsContext, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2}},
				textContentItem("target"),
			}),
			code: ReferenceCodeForbiddenProfile,
		},
		{
			name: "relationship",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{RelationshipType: RelationshipContains, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2}},
				textContentItem("target"),
			}),
			code: ReferenceCodeForbiddenRelation,
		},
		{
			name: "target value type",
			document: referenceDocument(ComprehensiveSRStorage, []ContentItem{
				{ValueType: ValueSCoord, RelationshipType: RelationshipContains, Children: []ContentItem{{
					RelationshipType: RelationshipSelectedFrom, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2},
				}}},
				textContentItem("target"),
			}),
			code: ReferenceCodeIncompatible,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, report, err := ResolveReferences(test.document, DefaultReferenceOptions())
			if !errors.Is(err, ErrReferenceResolution) {
				t.Fatalf("ResolveReferences error = %v, want ErrReferenceResolution", err)
			}
			assertReferenceFinding(t, report, test.code)

			options := DefaultReferenceOptions()
			options.Mode = ValidationModeWarn
			_, warnReport, warnErr := ResolveReferences(test.document, options)
			if warnErr != nil {
				t.Fatalf("warn ResolveReferences error = %v", warnErr)
			}
			assertReferenceFinding(t, warnReport, test.code)
			for _, finding := range warnReport.Findings {
				if finding.Severity != DiagnosticWarning {
					t.Fatalf("warn finding severity = %q", finding.Severity)
				}
			}
		})
	}
}

func TestDocumentWriterRejectsMixedByReferenceAndByValueMacros(t *testing.T) {
	document := referenceDocument(ComprehensiveSRStorage, []ContentItem{
		{
			ValueType: ValueText, Text: "must not be encoded", RelationshipType: RelationshipHasObsContext,
			ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2},
		},
		textContentItem("target"),
	})
	if _, err := document.Dataset(); !errors.Is(err, ErrReferenceResolution) || !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("Dataset error = %v, want ErrInvalidDocument and ErrReferenceResolution", err)
	}
}

func TestPermissiveReaderPreservesMixedMacroDiagnostic(t *testing.T) {
	mixed := object.FromElements([]core.Element{
		strElem(tagSOPClassUID, core.VRUI, ComprehensiveSRStorage),
		strElem(tagValueType, core.VRCS, string(ValueContainer)),
		seqElement(tagContentSequence,
			core.DataSet{Elements: []core.Element{
				strElem(tagRelationshipType, core.VRCS, RelationshipHasObsContext),
				contentItemIdentifierElement(ContentItemIdentifier{1, 2}),
				strElem(tagValueType, core.VRCS, string(ValueText)),
				strElem(tagTextValue, core.VRUT, "SECRET^PATIENT"),
			}},
			core.DataSet{Elements: []core.Element{
				strElem(tagRelationshipType, core.VRCS, RelationshipContains),
				strElem(tagValueType, core.VRCS, string(ValueText)),
				strElem(tagTextValue, core.VRUT, "target"),
			}},
		),
	}, nil)
	document, err := ReadDocument(mixed)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if !document.Content[0].Encoding.ByValueMacroPresent {
		t.Fatal("reader lost by-value macro provenance on by-reference item")
	}
	options := DefaultReferenceOptions()
	options.Mode = ValidationModeWarn
	_, report, err := ResolveReferences(document, options)
	if err != nil {
		t.Fatalf("ResolveReferences warn: %v", err)
	}
	assertReferenceFinding(t, report, ReferenceCodeByValueMacro)
	for _, finding := range report.Findings {
		if strings.Contains(finding.Message, "SECRET") {
			t.Fatalf("finding leaked value: %#v", finding)
		}
	}
}

func TestPermissiveReaderPreservesAndBoundsChildrenOnMalformedReference(t *testing.T) {
	child := func(value string) core.DataSet {
		return core.DataSet{Elements: []core.Element{
			strElem(tagRelationshipType, core.VRCS, RelationshipContains),
			strElem(tagValueType, core.VRCS, string(ValueText)),
			strElem(tagTextValue, core.VRUT, value),
		}}
	}
	malformed := object.FromElements([]core.Element{
		strElem(tagSOPClassUID, core.VRUI, ComprehensiveSRStorage),
		strElem(tagValueType, core.VRCS, string(ValueContainer)),
		seqElement(tagContentSequence, core.DataSet{Elements: []core.Element{
			strElem(tagRelationshipType, core.VRCS, RelationshipHasObsContext),
			contentItemIdentifierElement(ContentItemIdentifier{1, 2}),
			seqElement(tagContentSequence, child("first"), child("second")),
		}}),
	}, nil)

	options := DefaultReadOptions()
	options.MaxItems = 1
	if _, err := ReadDocumentWithOptions(malformed, options); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("bounded read error = %v, want ErrResourceLimitExceeded", err)
	}

	document, err := ReadDocument(malformed)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if len(document.Content) != 1 || len(document.Content[0].Children) != 2 || !document.Content[0].Encoding.ByValueMacroPresent {
		t.Fatalf("decoded malformed reference = %#v, want preserved children and mixed-macro provenance", document.Content)
	}
	referenceOptions := DefaultReferenceOptions()
	referenceOptions.Mode = ValidationModeWarn
	_, report, err := ResolveReferences(document, referenceOptions)
	if err != nil {
		t.Fatalf("ResolveReferences warn: %v", err)
	}
	assertReferenceFinding(t, report, ReferenceCodeByValueMacro)
	if _, err := document.Dataset(); !errors.Is(err, ErrReferenceResolution) {
		t.Fatalf("Dataset error = %v, want ErrReferenceResolution", err)
	}
}

func TestKnownContentItemPreservesUnknownExtensionElement(t *testing.T) {
	extensionTag := core.NewTag(0x0041, 0x1010)
	document := referenceDocument(ComprehensiveSRStorage, []ContentItem{{
		ValueType: ValueText, RelationshipType: RelationshipContains, Text: "value",
		ValueElements: []core.Element{core.NewRawElement(extensionTag, core.VROB, []byte{1, 2, 3, 4})},
	}})
	dataset, err := document.Dataset()
	if err != nil {
		t.Fatalf("Dataset: %v", err)
	}
	read, err := ReadDocument(dataset)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if len(read.Content) != 1 || len(read.Content[0].ValueElements) != 1 || read.Content[0].ValueElements[0].Tag() != extensionTag {
		t.Fatalf("preserved extension elements = %#v", read.Content)
	}
	rewritten, err := read.Dataset()
	if err != nil {
		t.Fatalf("rewritten Dataset: %v", err)
	}
	items, ok := rewritten.GetSequence(tagContentSequence)
	if !ok || len(items) != 1 || !items[0].Has(extensionTag) {
		t.Fatal("rewritten item lost unknown extension element")
	}
}

func TestReferenceResolutionLimitsAndBoundedReport(t *testing.T) {
	document := referenceDocument(ComprehensiveSRStorage, []ContentItem{
		{RelationshipType: RelationshipHasObsContext, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 9}},
		{RelationshipType: RelationshipHasObsContext, ReferencedContentItemIdentifier: ContentItemIdentifier{1, 8}},
	})
	options := DefaultReferenceOptions()
	options.Mode = ValidationModeWarn
	options.MaxFindings = 1
	_, report, err := ResolveReferences(document, options)
	if err != nil {
		t.Fatalf("ResolveReferences warn error = %v", err)
	}
	if len(report.Findings) != 1 || !report.Truncated {
		t.Fatalf("report = %#v, want one finding and truncated", report)
	}

	options = DefaultReferenceOptions()
	options.MaxItems = 1
	index, report, err := ResolveReferences(document, options)
	if !errors.Is(err, ErrReferenceResolution) || !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("MaxItems error = %v, want ErrReferenceResolution and ErrResourceLimitExceeded", err)
	}
	if index != nil {
		t.Fatalf("MaxItems index = %#v, want nil partial index", index)
	}
	assertReferenceFinding(t, report, ReferenceCodeResourceLimit)

	options.Mode = ValidationModeWarn
	index, report, err = ResolveReferences(document, options)
	if !errors.Is(err, ErrResourceLimitExceeded) || index != nil {
		t.Fatalf("warn MaxItems = index %#v, error %v; want fatal resource error", index, err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Severity != DiagnosticError {
		t.Fatalf("warn MaxItems report = %#v, want one error finding", report)
	}
}

func TestReaderPreservesEmptyReferencedContentItemIdentifierAsInvalidReference(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		strElem(tagSOPClassUID, core.VRUI, ComprehensiveSRStorage),
		strElem(tagValueType, core.VRCS, string(ValueContainer)),
		seqElement(tagContentSequence, core.DataSet{Elements: []core.Element{
			strElem(tagRelationshipType, core.VRCS, RelationshipHasObsContext),
			{Header: core.ElementHeader{Tag: tagReferencedContentItemIdentifier, VR: core.VRUL}, Value: core.Uint32Value{}},
		}}),
	}, nil)
	document, err := ReadDocument(dataset)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if len(document.Content) != 1 || !document.Content[0].Encoding.EmptyReferenceMacroPresent {
		t.Fatalf("decoded content = %#v, want preserved empty reference macro", document.Content)
	}
	options := DefaultReferenceOptions()
	options.Mode = ValidationModeWarn
	_, report, err := ResolveReferences(document, options)
	if err != nil {
		t.Fatalf("ResolveReferences warn: %v", err)
	}
	assertReferenceFinding(t, report, ReferenceCodeInvalidPath)
	if _, err := document.Dataset(); !errors.Is(err, ErrReferenceResolution) {
		t.Fatalf("Dataset error = %v, want ErrReferenceResolution", err)
	}
}

func TestUnknownValueTypePreservesKnownMacroPayload(t *testing.T) {
	text := strElem(tagTextValue, core.VRUT, "future payload")
	continuity := strElem(tagContinuityOfContent, core.VRCS, "SEPARATE")
	dataset := object.FromElements([]core.Element{
		strElem(tagSOPClassUID, core.VRUI, ComprehensiveSRStorage),
		strElem(tagValueType, core.VRCS, string(ValueContainer)),
		seqElement(tagContentSequence, core.DataSet{Elements: []core.Element{
			strElem(tagRelationshipType, core.VRCS, RelationshipContains),
			strElem(tagValueType, core.VRCS, "FUTURE"),
			text,
			continuity,
		}}),
	}, nil)
	document, err := ReadDocument(dataset)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if len(document.Content) != 1 || len(document.Content[0].ValueElements) != 2 ||
		document.Content[0].ValueElements[0].Tag() != tagTextValue ||
		document.Content[0].ValueElements[1].Tag() != tagContinuityOfContent {
		t.Fatalf("unknown content = %#v, want preserved Text Value and Continuity of Content", document.Content)
	}
	rewritten, err := document.Dataset()
	if err != nil {
		t.Fatalf("Dataset: %v", err)
	}
	items, ok := rewritten.GetSequence(tagContentSequence)
	if !ok || len(items) != 1 || !items[0].Has(tagTextValue) || !items[0].Has(tagContinuityOfContent) {
		t.Fatal("rewritten unknown content lost known macro payload")
	}
}

func TestComprehensiveReferenceRelationshipMatrix(t *testing.T) {
	tests := []struct {
		name         string
		sopClassUID  string
		source       ValueType
		relationship string
		target       ValueType
		want         bool
	}{
		{"container observation container", ComprehensiveSRStorage, ValueContainer, RelationshipHasObsContext, ValueContainer, true},
		{"text observation container", ComprehensiveSRStorage, ValueText, RelationshipHasObsContext, ValueContainer, false},
		{"acquisition composite target", ComprehensiveSRStorage, ValueContainer, RelationshipHasAcqContext, ValueComposite, false},
		{"3D property target", Comprehensive3DSRStorage, ValueText, RelationshipHasProperties, ValueSCoord3D, true},
		{"3D inferred target", Comprehensive3DSRStorage, ValueNum, RelationshipInferredFrom, ValueSCoord3D, true},
		{"3D temporal selection", Comprehensive3DSRStorage, ValueTCoord, RelationshipSelectedFrom, ValueSCoord3D, true},
		{"3D spatial source", Comprehensive3DSRStorage, ValueSCoord3D, RelationshipSelectedFrom, ValueImage, false},
		{"2D temporal cannot select 3D", ComprehensiveSRStorage, ValueTCoord, RelationshipSelectedFrom, ValueSCoord3D, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := referenceRelationshipAllowed(test.sopClassUID, test.source, test.relationship, test.target); got != test.want {
				t.Fatalf("referenceRelationshipAllowed = %v, want %v", got, test.want)
			}
		})
	}
}

func TestReferenceErrorPreservesSourceAndMissingRelationshipCode(t *testing.T) {
	document := referenceDocument(ComprehensiveSRStorage, []ContentItem{
		{ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2}},
		textContentItem("target"),
	})
	_, report, err := ResolveReferences(document, DefaultReferenceOptions())
	var referenceErr *ReferenceError
	if !errors.As(err, &referenceErr) {
		t.Fatalf("ResolveReferences error = %v, want ReferenceError", err)
	}
	if referenceErr.Code != ReferenceCodeMissingRelation || !referenceErr.Source.equal(ContentItemIdentifier{1}) {
		t.Fatalf("ReferenceError = %#v, want missing relationship from root", referenceErr)
	}
	assertReferenceFinding(t, report, ReferenceCodeMissingRelation)
}

func TestReferenceResolutionHonorsCustomPathComponentLimit(t *testing.T) {
	targetPath := ContentItemIdentifier{1, 2}
	target := ContentItem{ValueType: ValueContainer, RelationshipType: RelationshipContains}
	cursor := &target
	for range 64 {
		targetPath = append(targetPath, 1)
		cursor.Children = []ContentItem{{ValueType: ValueContainer, RelationshipType: RelationshipContains}}
		cursor = &cursor.Children[0]
	}
	document := referenceDocument(ComprehensiveSRStorage, []ContentItem{
		{RelationshipType: RelationshipHasObsContext, ReferencedContentItemIdentifier: targetPath},
		target,
	})
	options := DefaultReferenceOptions()
	options.MaxDepth = 70
	options.MaxPathComponents = 70
	_, report, err := ResolveReferences(document, options)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("ResolveReferences(custom path limit) = report %#v, error %v", report, err)
	}
}

func TestReferenceCycleDiagnosticIsDeterministicAcrossDisjointCycles(t *testing.T) {
	container := func(target ContentItemIdentifier) ContentItem {
		return ContentItem{
			ValueType: ValueContainer, RelationshipType: RelationshipContains,
			Children: []ContentItem{{
				RelationshipType:                RelationshipHasObsContext,
				ReferencedContentItemIdentifier: target,
			}},
		}
	}
	document := referenceDocument(ComprehensiveSRStorage, []ContentItem{
		container(ContentItemIdentifier{1, 2}),
		container(ContentItemIdentifier{1, 1}),
		container(ContentItemIdentifier{1, 4}),
		container(ContentItemIdentifier{1, 3}),
	})
	wantPath := ContentItemIdentifier{1, 2, 1}
	wantTarget := ContentItemIdentifier{1, 1}
	for attempt := 0; attempt < 100; attempt++ {
		_, report, err := ResolveReferences(document, DefaultReferenceOptions())
		if !errors.Is(err, ErrReferenceResolution) {
			t.Fatalf("attempt %d: error = %v, want ErrReferenceResolution", attempt, err)
		}
		if len(report.Findings) != 1 || report.Findings[0].Code != ReferenceCodeCycle ||
			!report.Findings[0].Path.equal(wantPath) || !report.Findings[0].Target.equal(wantTarget) {
			t.Fatalf("attempt %d: report = %#v, want deterministic first cycle", attempt, report)
		}
	}
}

func TestReadDocumentWithOptionsPreservesWarnedReferences(t *testing.T) {
	malformed := object.FromElements([]core.Element{
		strElem(tagSOPClassUID, core.VRUI, ComprehensiveSRStorage),
		strElem(tagValueType, core.VRCS, string(ValueContainer)),
		seqElement(tagContentSequence,
			core.DataSet{Elements: []core.Element{
				strElem(tagRelationshipType, core.VRCS, RelationshipHasObsContext),
				contentItemIdentifierElement(ContentItemIdentifier{1, 9}),
			}},
		),
	}, nil)
	options := DefaultReadOptions()
	options.ResolveReferences = true
	result, err := ReadDocumentWithOptions(malformed, options)
	if err != nil {
		t.Fatalf("warn ReadDocumentWithOptions error = %v", err)
	}
	if result.Document == nil || result.References == nil {
		t.Fatalf("warn result = %#v, want preserved document and index", result)
	}
	assertReferenceFinding(t, result.Report, ReferenceCodeDangling)

	options.ReferenceMode = ValidationModeStrict
	strict, err := ReadDocumentWithOptions(malformed, options)
	if !errors.Is(err, ErrReferenceResolution) || strict.Document == nil {
		t.Fatalf("strict result = %#v, error %v; want preserved document and typed error", strict, err)
	}

	phi := "SECRET^PATIENT"
	for _, finding := range result.Report.Findings {
		if strings.Contains(finding.Message, phi) || strings.Contains(finding.Code, phi) {
			t.Fatalf("diagnostic leaked PHI canary: %#v", finding)
		}
	}
}

func TestReadDocumentWithOptionsEnforcesItemAndPathLimits(t *testing.T) {
	dataset, err := validByReferenceDocument().Dataset()
	if err != nil {
		t.Fatalf("Dataset: %v", err)
	}
	options := DefaultReadOptions()
	options.MaxItems = 1
	if _, err := ReadDocumentWithOptions(dataset, options); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("MaxItems error = %v, want ErrResourceLimitExceeded", err)
	}

	options = DefaultReadOptions()
	options.MaxPathComponents = 1
	if _, err := ReadDocumentWithOptions(dataset, options); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("MaxPathComponents error = %v, want ErrResourceLimitExceeded", err)
	}
}

func validByReferenceDocument() *Document {
	return referenceDocument(ComprehensiveSRStorage, []ContentItem{
		{
			RelationshipType:                RelationshipHasObsContext,
			ReferencedContentItemIdentifier: ContentItemIdentifier{1, 3},
		},
		textContentItem("backward target"),
		{
			ValueType: ValueText, RelationshipType: RelationshipContains,
			ConceptName: CodedEntry{CodeValue: "B", CodingSchemeDesignator: "99TEST", CodeMeaning: "Source"},
			Text:        "forward target",
			Children: []ContentItem{{
				RelationshipType:                RelationshipHasProperties,
				ReferencedContentItemIdentifier: ContentItemIdentifier{1, 2},
			}},
		},
	})
}

func referenceDocument(sopClassUID string, content []ContentItem) *Document {
	return &Document{
		SOPClassUID: sopClassUID, SOPInstanceUID: "1.2.826.0.1.3680043.10.543.1",
		Title:   CodedEntry{CodeValue: "R", CodingSchemeDesignator: "99TEST", CodeMeaning: "Report"},
		Content: content,
	}
}

func textContentItem(text string) ContentItem {
	return ContentItem{
		ValueType: ValueText, RelationshipType: RelationshipContains,
		ConceptName: CodedEntry{CodeValue: "T", CodingSchemeDesignator: "99TEST", CodeMeaning: "Text"},
		Text:        text,
	}
}

func assertReferenceFinding(t *testing.T, report ValidationReport, code string) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("report %#v does not contain %q", report, code)
}
