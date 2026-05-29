package sr

import (
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestReadDocumentPropagatesStringDecodeErrorFromSequenceCharset(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		strElem(tagValueType, core.VRCS, string(ValueText)),
		core.NewRawElement(tagTextValue, core.VRUT, []byte("report text")),
	}}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0005), core.VRCS, []byte("ISO_IR 999")),
		seqElement(tagContentSequence, item),
	}, std.Dictionary)

	_, err := ReadDocument(obj)
	if err == nil || !strings.Contains(err.Error(), "ISO_IR 999") {
		t.Fatalf("ReadDocument() error = %v, want unsupported charset decode error", err)
	}
}

func TestReadDocumentPropagatesWrongStringAndSequenceVRs(t *testing.T) {
	for _, test := range []struct {
		name string
		obj  *object.Object
	}{
		{
			name: "string value",
			obj: object.FromElements([]core.Element{
				core.NewRawElement(tagSOPClassUID, core.VRUS, []byte{1, 0}),
			}, std.Dictionary),
		},
		{
			name: "sequence value",
			obj: object.FromElements([]core.Element{
				core.NewRawElement(tagContentSequence, core.VRLO, []byte("not a sequence")),
			}, std.Dictionary),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadDocument(test.obj); err == nil {
				t.Fatal("ReadDocument() error = nil, want malformed VR error")
			}
		})
	}
}

func TestReadDocumentPropagatesMalformedBinaryGraphicData(t *testing.T) {
	item := core.DataSet{Elements: []core.Element{
		strElem(tagValueType, core.VRCS, string(ValueSCoord)),
		core.NewRawElement(tagGraphicData, core.VRFL, []byte{1, 2, 3}),
	}}
	obj := object.FromElements([]core.Element{seqElement(tagContentSequence, item)}, std.Dictionary)

	if _, err := ReadDocument(obj); err == nil || !strings.Contains(err.Error(), "multiple of 4") {
		t.Fatalf("ReadDocument() error = %v, want malformed FL length error", err)
	}
}

func TestReadMeasurementReportPropagatesHeaderDecodeError(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagSOPClassUID, core.VRUS, []byte{1, 0}),
	}, std.Dictionary)

	_, err := ReadMeasurementReport(obj)
	if err == nil || errors.Is(err, ErrUnsupportedSRStorage) {
		t.Fatalf("ReadMeasurementReport() error = %v, want underlying header decode error", err)
	}
}
