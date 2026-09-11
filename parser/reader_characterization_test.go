package parser

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReaderSyntaxAndMalformedCharacterization(t *testing.T) {
	patientName := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "STATE^MACHINE")
	for _, syntax := range []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	} {
		t.Run(syntax.UID, func(t *testing.T) {
			encoded := dicomtest.EncodeElement(patientName, syntax)
			reader := NewReader(bytes.NewReader(encoded), syntax, ReaderOptions{Dictionary: std.Dictionary})
			dataset, err := reader.ReadDataSet()
			if err != nil || len(dataset.Elements) != 1 || dataset.Elements[0].StringValue() != "STATE^MACHINE" {
				t.Fatalf("dataset = %#v, err = %v", dataset, err)
			}
			if reader.Position() != int64(len(encoded)) {
				t.Fatalf("position = %d, want %d", reader.Position(), len(encoded))
			}

			truncated := NewReader(bytes.NewReader(encoded[:len(encoded)-1]), syntax, ReaderOptions{Dictionary: std.Dictionary})
			if _, err := truncated.ReadDataSet(); err == nil {
				t.Fatal("truncated value was accepted")
			}
		})
	}

	inner := dicomtest.EncodeElement(patientName, transfer.ExplicitVRLittleEndian)
	sequenceTag := core.NewTag(0x0008, 0x1111)
	encoded := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, sequenceTag, core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(inner))),
		inner,
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)
	dataset, err := NewReader(bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary}).ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	sequence, ok := dataset.Elements[0].Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != 1 || sequence.Items[0].Elements[0].StringValue() != "STATE^MACHINE" {
		t.Fatalf("undefined-length sequence = %#v", dataset.Elements[0].Value)
	}
}
