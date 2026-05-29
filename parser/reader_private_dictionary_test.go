package parser

import (
	"bytes"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadDataSetImplicitVRPrivateTagUsesDictionaryChain(t *testing.T) {
	patientNameTag := core.NewTag(0x0010, 0x0010)
	privateTag := core.NewTag(0x0011, 0x1001)
	stream := bytes.Join([][]byte{
		definedElementBytes(transfer.ImplicitVRLittleEndian, patientNameTag, core.VRPN, 10, []byte("STD^NAME  ")),
		definedElementBytes(transfer.ImplicitVRLittleEndian, privateTag, core.VRLO, 8, []byte("PRIVATE ")),
	}, nil)
	privateDict := &multiCountingDictionary{
		entries: map[core.Tag]core.VR{
			privateTag: core.VRLO,
		},
	}
	dict := dictionary.Chain{privateDict, std.Dictionary}

	reader := NewReader(bytes.NewReader(stream), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: dict})
	dataSet, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}

	byTag := make(map[core.Tag]core.Element)
	for _, elem := range dataSet.Elements {
		byTag[elem.Tag()] = elem
	}
	if got := byTag[patientNameTag].VR(); got != core.VRPN {
		t.Fatalf("standard fallback VR = %s, want %s", got, core.VRPN)
	}
	privateElem := byTag[privateTag]
	if got := privateElem.VR(); got != core.VRLO {
		t.Fatalf("private overlay VR = %s, want %s", got, core.VRLO)
	}
	raw, ok := privateElem.RawBytes()
	if !ok || !bytes.Equal(raw, []byte("PRIVATE ")) {
		t.Fatalf("private raw = % X ok=%v, want PRIVATE", raw, ok)
	}
}
