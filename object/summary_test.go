package object

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

func TestSummarizeElementsUsesDictionaryAndValuePreviews(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	unknownPublicTag := core.NewTag(0x7776, 0x0010)
	privateTag := core.NewTag(0x0011, 0x1010)
	bulkTag := core.NewTag(0x0040, 0xA730)
	deferredTag := core.NewTag(0x0008, 0x1030)

	obj := FromElements([]core.Element{
		dicomtest.NewPNElement(tags.PatientName, "DOE^JANE"),
		dicomtest.NewStringElement(privateTag, core.VRLO, "PRIVATE"),
		core.NewRawElement(unknownPublicTag, core.VROB, []byte{0x01, 0x02, 0x03}),
		dicomtest.NewSequenceElement(
			sequenceTag,
			core.DataSet{Elements: []core.Element{dicomtest.NewStringElement(tags.PatientID, core.VRLO, "P001")}},
		),
		{
			Header: core.ElementHeader{Tag: tags.PixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
			Value:  core.FragmentSequence{Fragments: [][]byte{{0x01}, {0x02}}},
		},
		{
			Header: core.ElementHeader{Tag: bulkTag, VR: core.VROB},
			Value:  core.BulkDataValue{URI: "bulk://pixel/1"},
		},
		{
			Header: core.ElementHeader{Tag: deferredTag, VR: core.VRLO, Length: 64, LengthSet: true},
		},
	}, std.Dictionary)

	rows := SummarizeElements(obj, SummaryOptions{Source: "dataset"})

	if len(rows) != 7 {
		t.Fatalf("SummarizeElements() length = %d, want 7: %#v", len(rows), rows)
	}
	for i := 1; i < len(rows); i++ {
		if !rows[i-1].Tag.Less(rows[i].Tag) {
			t.Fatalf("rows not sorted at %d: %s before %s", i, rows[i-1].Tag, rows[i].Tag)
		}
	}

	patient := requireSummaryRow(t, rows, tags.PatientName)
	if patient.Source != "dataset" || patient.Keyword != "PatientName" || patient.Name != "Patient Name" {
		t.Fatalf("patient row dictionary/source = %#v", patient)
	}
	if patient.VR != core.VRPN || patient.VRString() != "PN" {
		t.Fatalf("patient VR = %s/%q, want PN", patient.VR, patient.VRString())
	}
	if patient.TagString() != "(0010,0010)" || patient.LengthString() != "8" {
		t.Fatalf("patient formatted tag/length = %q/%q", patient.TagString(), patient.LengthString())
	}
	if patient.Value != "DOE^JANE" || patient.Private {
		t.Fatalf("patient value/private = %q/%v", patient.Value, patient.Private)
	}

	seq := requireSummaryRow(t, rows, sequenceTag)
	if seq.Value != "1 item(s)" || seq.Length != core.UndefinedLength || seq.LengthString() != "UNDEFINED" {
		t.Fatalf("sequence summary = %#v", seq)
	}

	unknown := requireSummaryRow(t, rows, unknownPublicTag)
	if unknown.Keyword != "" || unknown.Name != "" || unknown.Value != "<3 bytes>" || unknown.Private {
		t.Fatalf("unknown public summary = %#v", unknown)
	}

	private := requireSummaryRow(t, rows, privateTag)
	if !private.Private || private.Keyword != "" || private.Value != "PRIVATE" {
		t.Fatalf("private summary = %#v", private)
	}

	bulk := requireSummaryRow(t, rows, bulkTag)
	if bulk.Value != "bulk://pixel/1" {
		t.Fatalf("bulk value = %q, want bulk URI", bulk.Value)
	}

	pixel := requireSummaryRow(t, rows, tags.PixelData)
	if pixel.Keyword != "PixelData" || pixel.Value != "2 fragment(s)" {
		t.Fatalf("pixel summary = %#v", pixel)
	}

	deferred := requireSummaryRow(t, rows, deferredTag)
	if deferred.Value != "<value skipped>" {
		t.Fatalf("deferred value = %q, want skipped marker", deferred.Value)
	}
}

func TestSummarizeElementsOptions(t *testing.T) {
	privateTag := core.NewTag(0x0011, 0x1010)
	customTag := core.NewTag(0x7776, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(tags.PatientName, core.VRPN, "ABCDEFGHI"),
		dicomtest.NewStringElement(tags.StudyDescription, core.VRLO, "αβγδε"),
		dicomtest.NewStringElement(privateTag, core.VRLO, "PRIVATE"),
		core.NewRawElement(tags.PixelData, core.VROB, []byte{0x00, 0x01}),
		dicomtest.NewStringElement(customTag, core.VRLO, "CUSTOM"),
	}, std.Dictionary)

	rows := obj.SummarizeElements(SummaryOptions{
		Dictionary:     summaryDictionary{tag: customTag},
		MaxValueLength: 4,
		SkipPrivate:    true,
		SkipPixelData:  true,
	})

	if len(rows) != 3 {
		t.Fatalf("filtered summaries length = %d, want 3: %#v", len(rows), rows)
	}
	if findSummaryRow(rows, privateTag) != nil {
		t.Fatalf("private tag was not skipped: %#v", rows)
	}
	if findSummaryRow(rows, tags.PixelData) != nil {
		t.Fatalf("pixel data was not skipped: %#v", rows)
	}

	patient := requireSummaryRow(t, rows, tags.PatientName)
	if patient.Value != "ABCD..." {
		t.Fatalf("truncated patient value = %q, want ABCD...", patient.Value)
	}
	study := requireSummaryRow(t, rows, tags.StudyDescription)
	if study.Value != "αβγδ..." {
		t.Fatalf("unicode truncated value = %q, want αβγδ...", study.Value)
	}
	custom := requireSummaryRow(t, rows, customTag)
	if custom.Keyword != "CustomTag" || custom.Name != "Custom Tag" {
		t.Fatalf("custom dictionary summary = %#v", custom)
	}
}

func TestSummarizeElementsNilObject(t *testing.T) {
	if rows := SummarizeElements(nil, SummaryOptions{}); rows != nil {
		t.Fatalf("SummarizeElements(nil) = %#v, want nil", rows)
	}
	var obj *Object
	if rows := obj.SummarizeElements(SummaryOptions{}); rows != nil {
		t.Fatalf("nil.SummarizeElements() = %#v, want nil", rows)
	}
}

type summaryDictionary struct {
	tag core.Tag
}

func (d summaryDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if tag != d.tag {
		return std.Dictionary.ByTag(tag)
	}
	return dictionary.Entry{
		Tag:     tag,
		VR:      core.VRLO,
		Keyword: "CustomTag",
		Name:    "Custom Tag",
		VM:      "1",
	}, true
}

func (d summaryDictionary) ByKeyword(keyword string) (dictionary.Entry, bool) {
	return std.Dictionary.ByKeyword(keyword)
}

func requireSummaryRow(t *testing.T, rows []ElementSummary, tag core.Tag) ElementSummary {
	t.Helper()
	row := findSummaryRow(rows, tag)
	if row == nil {
		t.Fatalf("missing summary row for %s in %#v", tag, rows)
	}
	return *row
}

func findSummaryRow(rows []ElementSummary, tag core.Tag) *ElementSummary {
	for i := range rows {
		if rows[i].Tag == tag {
			return &rows[i]
		}
	}
	return nil
}
