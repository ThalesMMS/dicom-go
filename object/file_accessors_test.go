package object

import (
	"bytes"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"testing"
)

func TestFileCharacterSetNilReceiver(t *testing.T) {
	var file *File

	_, err := file.CharacterSet()
	if err == nil {
		t.Fatal("CharacterSet() on nil file should return an error")
	}

	file = &File{}
	_, err = file.CharacterSet()
	if err == nil {
		t.Fatal("CharacterSet() on file without dataset should return an error")
	}
}
func TestFileAccessorsRouteMetaAndDatasetTags(t *testing.T) {
	file, err := ReadFile(bytes.NewReader(dicomtest.MinimalFile()))
	if err != nil {
		t.Fatal(err)
	}

	metaTag := core.NewTag(0x0002, 0x0010)
	nameTag := core.NewTag(0x0010, 0x0010)

	metaElem, ok := file.Get(metaTag)
	if !ok || metaElem.Tag() != metaTag {
		t.Fatalf("unexpected meta element: tag=%s ok=%v", metaElem.Tag(), ok)
	}
	nameElem, ok := file.Get(nameTag)
	if !ok || nameElem.Tag() != nameTag {
		t.Fatalf("unexpected dataset element: tag=%s ok=%v", nameElem.Tag(), ok)
	}

	if got, ok := file.GetString(metaTag); !ok || got != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q ok=%v", got, ok)
	}
	if got, ok := file.GetString(nameTag); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}

	metaRaw, ok := file.GetRaw(metaTag)
	if !ok || len(metaRaw) == 0 {
		t.Fatalf("unexpected meta raw bytes: %v ok=%v", metaRaw, ok)
	}
	nameRaw, ok := file.GetRaw(nameTag)
	if !ok || len(nameRaw) == 0 {
		t.Fatalf("unexpected dataset raw bytes: %v ok=%v", nameRaw, ok)
	}

	if !file.Has(metaTag) {
		t.Fatal("expected file meta element to be reachable through File.Has")
	}
	if !file.Has(nameTag) {
		t.Fatal("expected dataset element to be reachable through File.Has")
	}

	nameStrings, ok := file.GetStrings(nameTag)
	if !ok || len(nameStrings) != 1 || nameStrings[0] != "TEST^PATIENT" {
		t.Fatalf("unexpected dataset strings: %v ok=%v", nameStrings, ok)
	}

	seqTag := core.NewTag(0x0008, 0x1111)
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewSequenceElement(
			seqTag,
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewPNElement(nameTag, "SEQ^PATIENT"),
				},
			},
		))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	fileWithSequence, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := fileWithSequence.GetSequence(seqTag)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected sequence items: len=%d ok=%v", len(items), ok)
	}
	if got, ok := items[0].GetString(nameTag); !ok || got != "SEQ^PATIENT" {
		t.Fatalf("unexpected nested sequence patient: %q ok=%v", got, ok)
	}
}
