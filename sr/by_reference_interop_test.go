package sr_test

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/sr"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	tagContentSequence                 = core.NewTag(0x0040, 0xA730)
	tagRelationshipType                = core.NewTag(0x0040, 0xA010)
	tagValueType                       = core.NewTag(0x0040, 0xA040)
	tagConceptNameCodeSequence         = core.NewTag(0x0040, 0xA043)
	tagReferencedContentItemIdentifier = core.NewTag(0x0040, 0xDB73)
)

func TestPydicomByReferenceFixtureInterop(t *testing.T) {
	path := os.Getenv("DICOM_GO_PYDICOM_SR_FIXTURE")
	if path == "" {
		t.Skip("set DICOM_GO_PYDICOM_SR_FIXTURE to pydicom test-SR.dcm")
	}

	file, err := object.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile pydicom SR fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close pydicom SR fixture: %v", err)
		}
	})

	document, err := sr.ReadDocument(file.Dataset)
	if err != nil {
		t.Fatalf("ReadDocument pydicom SR fixture: %v", err)
	}
	options := sr.DefaultReferenceOptions()
	options.Mode = sr.ValidationModeWarn
	index, report, err := sr.ResolveReferences(document, options)
	if err != nil {
		t.Fatalf("ResolveReferences warn: %v", err)
	}
	for _, finding := range report.Findings {
		if finding.Severity != sr.DiagnosticWarning {
			t.Fatalf("warn-mode finding severity = %q, want %q", finding.Severity, sr.DiagnosticWarning)
		}
	}

	edges, err := index.Edges()
	if err != nil {
		t.Fatalf("reference edges: %v", err)
	}
	for _, want := range []sr.ContentItemIdentifier{{1, 3, 2}, {1, 2, 2, 1}} {
		if !hasReferenceTarget(edges, want) {
			t.Errorf("reference target %v not found in %#v", want, referenceTargets(edges))
		}
	}
}

func TestWriteByReferenceFixtureForExternalInterop(t *testing.T) {
	output := os.Getenv("DICOM_GO_SR_INTEROP_OUTPUT")
	if output == "" {
		t.Skip("set DICOM_GO_SR_INTEROP_OUTPUT to write a synthetic Comprehensive SR fixture")
	}

	document := syntheticByReferenceDocument()
	index, report, err := sr.ResolveReferences(document, sr.DefaultReferenceOptions())
	if err != nil {
		t.Fatalf("ResolveReferences synthetic fixture: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("synthetic fixture findings = %#v, want none", report.Findings)
	}
	edges, err := index.Edges()
	if err != nil {
		t.Fatalf("synthetic reference edges: %v", err)
	}
	if len(edges) != 2 ||
		!hasReferenceEdge(edges, sr.ContentItemIdentifier{1}, sr.ContentItemIdentifier{1, 1}, sr.ContentItemIdentifier{1, 3}) ||
		!hasReferenceEdge(edges, sr.ContentItemIdentifier{1, 3}, sr.ContentItemIdentifier{1, 3, 1}, sr.ContentItemIdentifier{1, 2}) {
		t.Fatalf("synthetic reference edges = %#v, want forward 1 -> 1.3 and backward 1.3 -> 1.2", edges)
	}

	dataset, err := document.Dataset()
	if err != nil {
		t.Fatalf("Dataset synthetic fixture: %v", err)
	}
	assertReferenceSlotsContainOnlyReferenceMacros(t, dataset)

	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, &object.File{
		Dataset:        dataset,
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}); err != nil {
		t.Fatalf("WriteFile synthetic fixture: %v", err)
	}

	written, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("ReadFile serialized synthetic fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := written.Close(); err != nil {
			t.Errorf("close serialized synthetic fixture: %v", err)
		}
	})
	assertReferenceSlotsContainOnlyReferenceMacros(t, written.Dataset)

	roundTrip, err := sr.ReadDocument(written.Dataset)
	if err != nil {
		t.Fatalf("ReadDocument serialized synthetic fixture: %v", err)
	}
	if _, roundTripReport, err := sr.ResolveReferences(roundTrip, sr.DefaultReferenceOptions()); err != nil || len(roundTripReport.Findings) != 0 {
		t.Fatalf("ResolveReferences serialized fixture = report %#v, error %v", roundTripReport, err)
	}

	if err := os.WriteFile(output, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write synthetic SR interoperability fixture: %v", err)
	}
	if python := os.Getenv("DICOM_GO_PYDICOM_PYTHON"); python != "" {
		const script = `
import pydicom
import sys

dataset = pydicom.dcmread(sys.argv[1])
forward = dataset.ContentSequence[0]
backward = dataset.ContentSequence[2].ContentSequence[0]
assert list(forward.ReferencedContentItemIdentifier) == [1, 3]
assert list(backward.ReferencedContentItemIdentifier) == [1, 2]
for slot in (forward, backward):
    assert "ValueType" not in slot
    assert "ConceptNameCodeSequence" not in slot
`
		command := exec.Command(python, "-c", script, output)
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("pydicom validation failed: %v (%s)", err, bytes.TrimSpace(result))
		}
	}
}

func syntheticByReferenceDocument() *sr.Document {
	return &sr.Document{
		SOPClassUID:       sr.ComprehensiveSRStorage,
		SOPInstanceUID:    "1.2.826.0.1.3680043.10.543.628.1",
		StudyInstanceUID:  "1.2.826.0.1.3680043.10.543.628.2",
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.543.628.3",
		Modality:          "SR",
		SeriesNumber:      "1",
		InstanceNumber:    "1",
		Title: sr.CodedEntry{
			CodeValue:              "18748-4",
			CodingSchemeDesignator: "LN",
			CodeMeaning:            "Diagnostic imaging report",
		},
		Content: []sr.ContentItem{
			{
				RelationshipType:                sr.RelationshipHasObsContext,
				ReferencedContentItemIdentifier: sr.ContentItemIdentifier{1, 3},
			},
			textItem("backward reference target"),
			{
				ValueType:        sr.ValueText,
				RelationshipType: sr.RelationshipContains,
				ConceptName: sr.CodedEntry{
					CodeValue:              "FWD",
					CodingSchemeDesignator: "99DICOMGO",
					CodeMeaning:            "Forward reference target",
				},
				Text: "synthetic forward reference target",
				Children: []sr.ContentItem{{
					RelationshipType:                sr.RelationshipHasProperties,
					ReferencedContentItemIdentifier: sr.ContentItemIdentifier{1, 2},
				}},
			},
		},
	}
}

func textItem(text string) sr.ContentItem {
	return sr.ContentItem{
		ValueType:        sr.ValueText,
		RelationshipType: sr.RelationshipContains,
		ConceptName: sr.CodedEntry{
			CodeValue:              "TXT",
			CodingSchemeDesignator: "99DICOMGO",
			CodeMeaning:            "Synthetic text",
		},
		Text: text,
	}
}

func assertReferenceSlotsContainOnlyReferenceMacros(t *testing.T, dataset *object.Object) {
	t.Helper()
	items, ok := dataset.GetSequence(tagContentSequence)
	if !ok || len(items) != 3 {
		t.Fatalf("top-level Content Sequence has %d items, want 3", len(items))
	}
	assertReferenceSlot(t, items[0], sr.ContentItemIdentifier{1, 3})

	children, ok := items[2].GetSequence(tagContentSequence)
	if !ok || len(children) != 1 {
		t.Fatalf("nested Content Sequence has %d items, want 1", len(children))
	}
	assertReferenceSlot(t, children[0], sr.ContentItemIdentifier{1, 2})
}

func assertReferenceSlot(t *testing.T, slot *object.Object, want sr.ContentItemIdentifier) {
	t.Helper()
	if slot == nil {
		t.Fatal("by-reference slot is nil")
	}
	if !slot.Has(tagRelationshipType) || !slot.Has(tagReferencedContentItemIdentifier) {
		t.Fatal("by-reference slot lacks Relationship Type or Referenced Content Item Identifier")
	}
	if slot.Has(tagValueType) || slot.Has(tagConceptNameCodeSequence) {
		t.Fatal("by-reference slot contains Value Type or Concept Name Code Sequence")
	}
	element, _ := slot.Get(tagReferencedContentItemIdentifier)
	got, ok := referenceIdentifierValue(slot, element)
	if !ok || !reflect.DeepEqual(got, []uint32(want)) {
		t.Fatalf("Referenced Content Item Identifier = %#v, want %v", element.Value, want)
	}
}

func referenceIdentifierValue(slot *object.Object, element core.Element) ([]uint32, bool) {
	if element.VR() != core.VRUL {
		return nil, false
	}
	if values, ok := element.Value.(core.Uint32Value); ok {
		return []uint32(values), true
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw)%4 != 0 {
		return nil, false
	}
	values := make([]uint32, len(raw)/4)
	for index := range values {
		values[index] = slot.ValueByteOrder().Uint32(raw[index*4:])
	}
	return values, true
}

func hasReferenceTarget(edges []sr.ReferenceEdge, want sr.ContentItemIdentifier) bool {
	for _, edge := range edges {
		if reflect.DeepEqual(edge.Target, want) {
			return true
		}
	}
	return false
}

func hasReferenceEdge(edges []sr.ReferenceEdge, source, slot, target sr.ContentItemIdentifier) bool {
	for _, edge := range edges {
		if reflect.DeepEqual(edge.Source, source) && reflect.DeepEqual(edge.Slot, slot) && reflect.DeepEqual(edge.Target, target) {
			return true
		}
	}
	return false
}

func referenceTargets(edges []sr.ReferenceEdge) []sr.ContentItemIdentifier {
	targets := make([]sr.ContentItemIdentifier, len(edges))
	for index, edge := range edges {
		targets[index] = edge.Target
	}
	return targets
}
