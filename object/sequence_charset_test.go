package object

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

func TestGetSequenceInheritsSpecificCharacterSetRecursively(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nestedSequenceTag := core.NewTag(0x0008, 0x1115)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		core.NewRawElement(tagSpecificCharacterSet, core.VRCS, []byte("ISO_IR 192")),
		dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{
			core.NewRawElement(nameTag, core.VRPN, []byte("René^José")),
			dicomtest.NewSequenceElement(nestedSequenceTag, core.DataSet{Elements: []core.Element{
				core.NewRawElement(nameTag, core.VRPN, []byte("Ângela^Müller")),
			}}),
		}}),
	}, nil)

	items, ok := obj.GetSequence(sequenceTag)
	if !ok || len(items) != 1 {
		t.Fatalf("GetSequence() = %d items, %v; want one item", len(items), ok)
	}
	if got, err := items[0].LookupString(nameTag); err != nil || got != "René^José" {
		t.Fatalf("inherited item name = %q, %v; want René^José", got, err)
	}
	items[0].SetTextOptions(TextOptions{})
	if charset, err := items[0].CharacterSet(); err != nil || charset.Name() != "ISO_IR 192" {
		t.Fatalf("charset after SetTextOptions = %q, %v; want ISO_IR 192", charset.Name(), err)
	}

	nested, ok := items[0].GetSequence(nestedSequenceTag)
	if !ok || len(nested) != 1 {
		t.Fatalf("nested GetSequence() = %d items, %v; want one item", len(nested), ok)
	}
	if got, err := nested[0].LookupString(nameTag); err != nil || got != "Ângela^Müller" {
		t.Fatalf("nested inherited name = %q, %v; want Ângela^Müller", got, err)
	}
}

func TestGetSequenceItemSpecificCharacterSetOverridesAndCanReturnToParent(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		core.NewRawElement(tagSpecificCharacterSet, core.VRCS, []byte("ISO_IR 192")),
		dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{
			core.NewRawElement(tagSpecificCharacterSet, core.VRCS, []byte("ISO_IR 100")),
			core.NewRawElement(nameTag, core.VRPN, []byte("Jos\xe9^Silva")),
		}}),
	}, nil)

	items, _ := obj.GetSequence(sequenceTag)
	if got, err := items[0].LookupString(nameTag); err != nil || got != "José^Silva" {
		t.Fatalf("item override name = %q, %v; want José^Silva", got, err)
	}
	if !items[0].Remove(tagSpecificCharacterSet) {
		t.Fatal("Remove(item charset) = false, want true")
	}
	charset, err := items[0].CharacterSet()
	if err != nil || charset.Name() != "ISO_IR 192" {
		t.Fatalf("charset after removing override = %q, %v; want inherited ISO_IR 192", charset.Name(), err)
	}
}

func TestGetSequenceInheritsUnsupportedCharsetFallback(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElementsWithTextOptions([]core.Element{
		core.NewRawElement(tagSpecificCharacterSet, core.VRCS, []byte("UNKNOWN")),
		dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{
			core.NewRawElement(nameTag, core.VRPN, []byte("Jos\xe9^Silva")),
		}}),
	}, nil, TextOptions{
		AllowUnsupportedCharsetFallback: true,
		FallbackCharacterSet:            dicomenc.ISOIR100,
	})

	items, _ := obj.GetSequence(sequenceTag)
	if got, err := items[0].LookupString(nameTag); err != nil || got != "José^Silva" {
		t.Fatalf("fallback item name = %q, %v; want José^Silva", got, err)
	}
}
