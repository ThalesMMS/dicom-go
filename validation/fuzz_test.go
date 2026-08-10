package validation_test

import (
	"context"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/validation"
)

func FuzzValidateVRStringsBounded(f *testing.F) {
	f.Add("UI", []byte("1.2.840.10008.1\x00"))
	f.Add("DS", []byte(" 1.25E+2 "))
	f.Add("PN", []byte("DOE^JANE=YAMADA^HANA"))
	f.Add("DT", []byte("20260228123045.123456-0300"))
	f.Fuzz(func(t *testing.T, vrCode string, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		vr, err := core.ParseVR(vrCode)
		if err != nil {
			return
		}
		report, err := validation.ValidateElement(context.Background(), core.NewRawElement(core.NewTag(0x0011, 0x1001), vr, data), validation.Options{MaxFindings: 8})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Findings)+len(report.Changes) > 8 {
			t.Fatalf("report exceeded bound: %#v", report)
		}
	})
}

func FuzzValidateMultiplicityDelimitersBounded(f *testing.F) {
	f.Add("1", "one")
	f.Add("2-2n", "one\\two\\three\\four")
	f.Add("3-n", "one\\two")
	f.Add("0-0n", "zero")
	f.Fuzz(func(t *testing.T, vm, encoded string) {
		if len(vm) > 32 || len(encoded) > 4096 {
			t.Skip()
		}
		tag := core.NewTag(0x0011, 0x1002)
		dict := fuzzDictionary{entry: dictionary.Entry{Tag: tag, VR: core.VRLO, VM: vm}}
		report, err := validation.ValidateElement(context.Background(), core.NewRawElement(tag, core.VRLO, []byte(encoded)), validation.Options{Dictionary: dict, MaxFindings: 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Findings)+len(report.Changes) > 4 {
			t.Fatalf("report exceeded bound: %#v", report)
		}
	})
}

func FuzzValidateNestedSequencesBounded(f *testing.F) {
	f.Add(uint8(0), uint8(1))
	f.Add(uint8(4), uint8(3))
	f.Add(uint8(12), uint8(2))
	f.Fuzz(func(t *testing.T, requestedDepth, duplicates uint8) {
		depth := int(requestedDepth % 12)
		duplicateCount := int(duplicates%8) + 1
		tag := core.NewTag(0x0011, 0x1010)
		leaf := core.DataSet{}
		for i := 0; i < duplicateCount; i++ {
			leaf.Elements = append(leaf.Elements, core.NewRawElement(tag, core.VRLO, []byte("value")))
		}
		for i := 0; i < depth; i++ {
			sequenceTag := core.NewTag(0x0008, uint16(0x1100+i))
			leaf = core.DataSet{Elements: []core.Element{{
				Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ},
				Value:  core.SequenceValue{Items: []core.DataSet{leaf}},
			}}}
		}
		result, err := validation.ValidateDataSet(context.Background(), leaf, validation.Options{MaxFindings: 3, MaxDepth: 8, MaxElements: 128})
		if err != nil && depth <= 8 {
			t.Fatal(err)
		}
		if len(result.Report.Findings)+len(result.Report.Changes) > 3 {
			t.Fatalf("report exceeded bound: %#v", result.Report)
		}
	})
}

type fuzzDictionary struct{ entry dictionary.Entry }

func (d fuzzDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	return d.entry, tag == d.entry.Tag
}

func (d fuzzDictionary) ByKeyword(string) (dictionary.Entry, bool) { return dictionary.Entry{}, false }
