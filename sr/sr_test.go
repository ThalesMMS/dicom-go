package sr

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func mustDocumentDataset(t testing.TB, doc *Document) *object.Object {
	t.Helper()
	dataset, err := doc.Dataset()
	if err != nil {
		t.Fatalf("Document.Dataset() error = %v", err)
	}
	return dataset
}

func sampleKOS() *Document {
	return &Document{
		SOPClassUID:    KeyObjectSelectionDocumentStorage,
		SOPInstanceUID: "1.2.3.4.5",
		Title:          CodedEntry{CodeValue: "113000", CodingSchemeDesignator: "DCM", CodeMeaning: "Of Interest"},
		ContentDate:    "20240101",
		ContentTime:    "120000",
		Content: []ContentItem{
			{
				ValueType:        ValueImage,
				RelationshipType: RelationshipContains,
				ConceptName:      CodedEntry{CodeValue: "121079", CodingSchemeDesignator: "DCM", CodeMeaning: "Baseline"},
				Image: ImageReference{
					StudyInstanceUID:  "1.2.3.study",
					SeriesInstanceUID: "1.2.3.series",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.9.9.9",
					Frames:            []int{1, 3},
				},
			},
			{
				ValueType:        ValueText,
				RelationshipType: RelationshipContains,
				ConceptName:      CodedEntry{CodeValue: "121071", CodingSchemeDesignator: "DCM", CodeMeaning: "Finding"},
				Text:             "Nodule in right upper lobe",
			},
			{
				ValueType:        ValueNum,
				RelationshipType: RelationshipContains,
				ConceptName:      CodedEntry{CodeValue: "G-D7FE", CodingSchemeDesignator: "SRT", CodeMeaning: "Diameter"},
				Measurement:      &Measurement{Value: 12.5, Units: CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"}},
			},
		},
	}
}

func TestKOSRoundTrip(t *testing.T) {
	doc := sampleKOS()
	doc.StudyInstanceUID = "1.2.3.study"
	doc.SeriesInstanceUID = "1.2.3.kos.series"
	round, err := ReadDocument(mustDocumentDataset(t, doc))
	if err != nil {
		t.Fatalf("ReadDocument() error = %v", err)
	}
	if round.SOPClassUID != doc.SOPClassUID || round.SOPInstanceUID != doc.SOPInstanceUID {
		t.Fatalf("header mismatch: %#v", round)
	}
	if round.Modality != "KO" {
		t.Fatalf("modality = %q, want KO", round.Modality)
	}
	if round.Title != doc.Title {
		t.Fatalf("title = %#v, want %#v", round.Title, doc.Title)
	}
	if round.ContentDate != "20240101" || round.ContentTime != "120000" {
		t.Fatalf("content date/time = %q/%q", round.ContentDate, round.ContentTime)
	}
	if round.StudyInstanceUID != doc.StudyInstanceUID || round.SeriesInstanceUID != doc.SeriesInstanceUID {
		t.Fatalf("identity = %q/%q, want %q/%q", round.StudyInstanceUID, round.SeriesInstanceUID, doc.StudyInstanceUID, doc.SeriesInstanceUID)
	}
	if got, _ := mustDocumentDataset(t, doc).GetString(core.NewTag(0x0020, 0x000D)); got != doc.StudyInstanceUID {
		t.Fatalf("dataset StudyInstanceUID = %q, want %q", got, doc.StudyInstanceUID)
	}
	if got, _ := mustDocumentDataset(t, doc).GetString(core.NewTag(0x0020, 0x000E)); got != doc.SeriesInstanceUID {
		t.Fatalf("dataset SeriesInstanceUID = %q, want %q", got, doc.SeriesInstanceUID)
	}
	if len(round.Content) != 3 {
		t.Fatalf("content count = %d, want 3", len(round.Content))
	}

	img := round.Content[0]
	if img.ValueType != ValueImage || img.Image.SOPInstanceUID != "1.9.9.9" {
		t.Fatalf("image item = %#v", img)
	}
	if len(img.Image.Frames) != 2 || img.Image.Frames[0] != 1 || img.Image.Frames[1] != 3 {
		t.Fatalf("frames = %v, want [1 3]", img.Image.Frames)
	}
	if img.ConceptName.CodeMeaning != "Baseline" {
		t.Fatalf("image concept = %#v", img.ConceptName)
	}

	text := round.Content[1]
	if text.ValueType != ValueText || text.Text != "Nodule in right upper lobe" {
		t.Fatalf("text item = %#v", text)
	}

	num := round.Content[2]
	if num.ValueType != ValueNum || num.Measurement == nil {
		t.Fatalf("num item = %#v", num)
	}
	if num.Measurement.Value != 12.5 || num.Measurement.Units.CodeValue != "mm" {
		t.Fatalf("measurement = %#v", num.Measurement)
	}
}

func TestDocumentRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name         string
		completion   string
		verification string
	}{
		{name: "invalid completion", completion: "FINAL", verification: verificationUnverified},
		{name: "invalid verification", completion: completionComplete, verification: "ATTESTED"},
		{name: "partial verified", completion: completionPartial, verification: verificationVerified},
		{name: "verified without observer", completion: completionComplete, verification: verificationVerified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &Document{
				SOPClassUID:      BasicTextSRStorage,
				SOPInstanceUID:   "1.2.3.4",
				CompletionFlag:   tt.completion,
				VerificationFlag: tt.verification,
			}
			if _, err := doc.Dataset(); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("Dataset() error = %v, want ErrInvalidDocument", err)
			}
		})
	}
}

func TestKOSRejectsIncompleteEvidenceReferences(t *testing.T) {
	tests := []struct {
		name  string
		clear func(*ImageReference)
	}{
		{name: "study UID", clear: func(ref *ImageReference) { ref.StudyInstanceUID = "" }},
		{name: "series UID", clear: func(ref *ImageReference) { ref.SeriesInstanceUID = "" }},
		{name: "SOP class UID", clear: func(ref *ImageReference) { ref.SOPClassUID = "" }},
		{name: "SOP instance UID", clear: func(ref *ImageReference) { ref.SOPInstanceUID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := sampleKOS()
			tt.clear(&doc.Content[0].Image)
			if _, err := doc.Dataset(); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("Dataset() error = %v, want ErrInvalidDocument", err)
			}
		})
	}
}

func TestKeyObjectSelectionHelpersBuildAndExtractFindings(t *testing.T) {
	refs := []ImageReference{
		{StudyInstanceUID: "1.2.study", SeriesInstanceUID: "1.2.series.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.inst.1", Frames: []int{2}},
		{StudyInstanceUID: "1.2.study", SeriesInstanceUID: "1.2.series.2", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.inst.2"},
	}
	doc := NewKeyObjectSelection("1.2.kos.doc", refs, map[string]string{
		"1.2.inst.1": "8 mm nodule, RUL",
	})
	if doc.SOPClassUID != KeyObjectSelectionDocumentStorage || doc.Modality != "KO" || doc.Title != KOSTitleOfInterest {
		t.Fatalf("KOS header = %#v", doc)
	}

	images := KeyObjectSelectionImages(doc)
	if len(images) != 2 || images[0].SOPInstanceUID != "1.2.inst.1" {
		t.Fatalf("KeyObjectSelectionImages() = %#v", images)
	}
	if len(images[0].Frames) != 1 || images[0].Frames[0] != 2 {
		t.Fatalf("frames = %#v, want [2]", images[0].Frames)
	}
	if images[0].StudyInstanceUID != "1.2.study" || images[0].SeriesInstanceUID != "1.2.series.1" {
		t.Fatalf("hierarchy = %q/%q, want source study/series", images[0].StudyInstanceUID, images[0].SeriesInstanceUID)
	}

	findings := KeyObjectSelectionFindings(doc)
	if findings["1.2.inst.1"] != "8 mm nodule, RUL" {
		t.Fatalf("KeyObjectSelectionFindings() = %#v", findings)
	}
	if _, ok := findings["1.2.inst.2"]; ok {
		t.Fatalf("unexpected empty finding for second image: %#v", findings)
	}
}

func TestDocumentEmitsMandatorySRAttributes(t *testing.T) {
	doc := &Document{
		SOPClassUID:    BasicTextSRStorage,
		SOPInstanceUID: "1.2.3.sr",
		ContentDate:    "20260709",
		ContentTime:    "120000",
		Content: []ContentItem{{
			ValueType:        ValueContainer,
			RelationshipType: RelationshipContains,
		}},
	}
	obj := mustDocumentDataset(t, doc)
	for tag, want := range map[core.Tag]string{
		tagSeriesNumber:        "1",
		tagInstanceNumber:      "1",
		tagCompletionFlag:      "PARTIAL",
		tagVerificationFlag:    "UNVERIFIED",
		tagContinuityOfContent: "SEPARATE",
	} {
		if got, ok := obj.GetString(tag); !ok || got != want {
			t.Fatalf("%v = %q, ok=%v; want %q", tag, got, ok, want)
		}
	}
	items, ok := obj.GetSequence(tagContentSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("ContentSequence = %d items, ok=%v; want 1", len(items), ok)
	}
	if got, ok := items[0].GetString(tagContinuityOfContent); !ok || got != "SEPARATE" {
		t.Fatalf("child ContinuityOfContent = %q, ok=%v; want SEPARATE", got, ok)
	}

	round, err := ReadDocument(obj)
	if err != nil {
		t.Fatal(err)
	}
	if round.SeriesNumber != "1" || round.InstanceNumber != "1" || round.CompletionFlag != "PARTIAL" || round.VerificationFlag != "UNVERIFIED" || round.ContinuityOfContent != "SEPARATE" {
		t.Fatalf("mandatory SR attributes not preserved: %#v", round)
	}
	if len(round.Content) != 1 || round.Content[0].ContinuityOfContent != "SEPARATE" {
		t.Fatalf("container continuity not preserved: %#v", round.Content)
	}
}

func TestDocumentDefaultsMandatoryContentDateAndTime(t *testing.T) {
	obj := mustDocumentDataset(t, &Document{
		SOPClassUID:    BasicTextSRStorage,
		SOPInstanceUID: "1.2.3.sr.default-time",
	})
	if got, ok := obj.GetString(tagContentDate); !ok || got == "" {
		t.Fatalf("ContentDate = %q, ok=%v; want generated Type 1 value", got, ok)
	}
	if got, ok := obj.GetString(tagContentTime); !ok || got == "" {
		t.Fatalf("ContentTime = %q, ok=%v; want generated Type 1 value", got, ok)
	}
}

func TestKOSGroupsCurrentRequestedProcedureEvidenceByStudyAndSeries(t *testing.T) {
	doc := NewKeyObjectSelection("1.2.kos", []ImageReference{
		{StudyInstanceUID: "1.2.study", SeriesInstanceUID: "1.2.series.1", SOPClassUID: "1.2.ct", SOPInstanceUID: "1.2.image.1"},
		{StudyInstanceUID: "1.2.study", SeriesInstanceUID: "1.2.series.2", SOPClassUID: "1.2.ct", SOPInstanceUID: "1.2.image.2"},
		{StudyInstanceUID: "1.2.study", SeriesInstanceUID: "1.2.series.1", SOPClassUID: "1.2.ct", SOPInstanceUID: "1.2.image.3"},
	}, nil)

	studies, ok := mustDocumentDataset(t, doc).GetSequence(tagCurrentRequestedProcedureEvidence)
	if !ok || len(studies) != 1 {
		t.Fatalf("CurrentRequestedProcedureEvidenceSequence = %d studies, ok=%v; want 1", len(studies), ok)
	}
	series, ok := studies[0].GetSequence(tagReferencedSeriesSequence)
	if !ok || len(series) != 2 {
		t.Fatalf("ReferencedSeriesSequence = %d items, ok=%v; want 2", len(series), ok)
	}
	wantInstances := map[string]int{"1.2.series.1": 2, "1.2.series.2": 1}
	for _, seriesItem := range series {
		seriesUID, _ := seriesItem.GetString(tagSeriesInstanceUID)
		instances, present := seriesItem.GetSequence(tagRefSOPSequence)
		if !present || len(instances) != wantInstances[seriesUID] {
			t.Fatalf("series %q instances = %d, present=%v; want %d", seriesUID, len(instances), present, wantInstances[seriesUID])
		}
	}

	round, err := ReadDocument(mustDocumentDataset(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	refs := KeyObjectSelectionImages(round)
	if len(refs) != 3 || refs[2].StudyInstanceUID != "1.2.study" || refs[2].SeriesInstanceUID != "1.2.series.1" {
		t.Fatalf("round-trip evidence hierarchy = %#v", refs)
	}
}

func TestNilNUMEmitsEmptyMeasuredValueAndUnknownQualifier(t *testing.T) {
	doc := &Document{
		SOPClassUID:    BasicTextSRStorage,
		SOPInstanceUID: "1.2.3.sr",
		Content: []ContentItem{{
			ValueType:        ValueNum,
			RelationshipType: RelationshipContains,
		}},
	}
	items, ok := mustDocumentDataset(t, doc).GetSequence(tagContentSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("ContentSequence = %d items, ok=%v; want 1", len(items), ok)
	}
	measured, ok := items[0].GetSequence(tagMeasuredValueSeq)
	if !ok || len(measured) != 0 {
		t.Fatalf("MeasuredValueSequence = %d items, ok=%v; want present and empty", len(measured), ok)
	}
	qualifier, ok := readCode(items[0], tagNumericValueQualifierCodeSeq)
	if !ok || qualifier.CodeValue != "114010" || qualifier.CodingSchemeDesignator != "DCM" {
		t.Fatalf("NumericValueQualifierCodeSequence = %#v, ok=%v; want DCM 114010", qualifier, ok)
	}

	round, err := ReadDocument(mustDocumentDataset(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(round.Content) != 1 || round.Content[0].Measurement != nil || round.Content[0].NumericValueQualifier != qualifier {
		t.Fatalf("NUM round-trip = %#v", round.Content)
	}
}

func TestNestedContainerRoundTrip(t *testing.T) {
	doc := &Document{
		SOPClassUID:    BasicTextSRStorage,
		SOPInstanceUID: "1.1.1",
		Title:          CodedEntry{CodeValue: "11528-7", CodingSchemeDesignator: "LN", CodeMeaning: "Radiology Report"},
		Content: []ContentItem{
			{
				ValueType:        ValueContainer,
				RelationshipType: RelationshipContains,
				ConceptName:      CodedEntry{CodeValue: "121070", CodingSchemeDesignator: "DCM", CodeMeaning: "Findings"},
				Children: []ContentItem{
					{ValueType: ValueText, RelationshipType: RelationshipContains, Text: "Normal study"},
				},
			},
		},
	}
	round, err := ReadDocument(mustDocumentDataset(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if round.Modality != "SR" {
		t.Fatalf("modality = %q, want SR", round.Modality)
	}
	if len(round.Content) != 1 || round.Content[0].ValueType != ValueContainer {
		t.Fatalf("root content = %#v", round.Content)
	}
	children := round.Content[0].Children
	if len(children) != 1 || children[0].Text != "Normal study" {
		t.Fatalf("nested children = %#v", children)
	}
}

func TestReadDocumentNil(t *testing.T) {
	if _, err := ReadDocument(nil); err != ErrNoDataset {
		t.Fatalf("error = %v, want ErrNoDataset", err)
	}
}

func TestReadDocumentRejectsExcessiveContentTreeDepth(t *testing.T) {
	item := object.New(nil)
	for i := 1; i < maxContentTreeDepth+1; i++ {
		item = object.FromElements([]core.Element{seqElement(tagContentSequence, item.ToDataSet())}, nil)
	}
	obj := object.FromElements([]core.Element{seqElement(tagContentSequence, item.ToDataSet())}, nil)

	_, err := ReadDocument(obj)
	if !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("ReadDocument() error = %v, want ErrResourceLimitExceeded", err)
	}
}
