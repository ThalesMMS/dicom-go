package sr

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestReadDocumentInheritsSpecificCharacterSetInCodeSequence(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0005), core.VRCS, []byte("ISO_IR 192")),
		dicomtest.NewSequenceElement(tagConceptNameCodeSeq, core.DataSet{Elements: []core.Element{
			dicomtest.NewStringElement(tagCodeValue, core.VRSH, "99TEST"),
			dicomtest.NewStringElement(tagCodingScheme, core.VRSH, "99LOCAL"),
			core.NewRawElement(tagCodeMeaning, core.VRLO, []byte("Relatório clínico")),
		}}),
	}, nil)

	doc, err := ReadDocument(obj)
	if err != nil {
		t.Fatalf("ReadDocument() error = %v", err)
	}
	if doc.Title.CodeMeaning != "Relatório clínico" {
		t.Fatalf("title CodeMeaning = %q, want Relatório clínico", doc.Title.CodeMeaning)
	}
}
