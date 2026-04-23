package object

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

func TestFromDataSetToDataSetPreservesOrder(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1111)
	ds := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "DOE^JOHN"),
			dicomtest.NewSequenceElement(
				seqTag,
				core.DataSet{
					Elements: []core.Element{
						dicomtest.NewStringElement(core.NewTag(0x0008, 0x1150), core.VRUI, dicomtest.TestSOPClassUID),
					},
				},
			),
			dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "PATIENT-001"),
		},
	}

	obj := FromDataSet(ds, std.Dictionary)
	if obj.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", obj.Len())
	}

	got := obj.ToDataSet()
	if diff := dicomtest.DiffDataSet(got, ds); diff != "" {
		t.Fatalf("dataset mismatch:\n%s", diff)
	}
}

func TestHasReportsPresentAndAbsentTags(t *testing.T) {
	presentTag := core.NewTag(0x0010, 0x0010)
	absentTag := core.NewTag(0x0010, 0x0020)
	obj := FromElements([]core.Element{
		dicomtest.NewPNElement(presentTag, "DOE^JOHN"),
	}, std.Dictionary)

	if !obj.Has(presentTag) {
		t.Fatalf("Has(%s) = false, want true", presentTag)
	}
	if obj.Has(absentTag) {
		t.Fatalf("Has(%s) = true, want false", absentTag)
	}
}

func TestFromDataSetDuplicateTagsUseLastWinsAndLastPosition(t *testing.T) {
	nameTag := core.NewTag(0x0010, 0x0010)
	idTag := core.NewTag(0x0010, 0x0020)
	ds := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(nameTag, "FIRST^PATIENT"),
			dicomtest.NewStringElement(idTag, core.VRLO, "PATIENT-001"),
			dicomtest.NewPNElement(nameTag, "LAST^PATIENT"),
		},
	}

	obj := FromDataSet(ds, std.Dictionary)

	if obj.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", obj.Len())
	}
	if !obj.Has(nameTag) || !obj.Has(idTag) {
		t.Fatalf("Has() did not report retained tags")
	}
	if got, ok := obj.GetString(nameTag); !ok || got != "LAST^PATIENT" {
		t.Fatalf("GetString() = %q, %v, want LAST^PATIENT, true", got, ok)
	}

	want := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewStringElement(idTag, core.VRLO, "PATIENT-001"),
			dicomtest.NewPNElement(nameTag, "LAST^PATIENT"),
		},
	}
	if diff := dicomtest.DiffDataSet(obj.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch:\n%s", diff)
	}
}

func TestGetRaw(t *testing.T) {
	rawTag := core.NewTag(0x7FE0, 0x0010)
	stringTag := core.NewTag(0x0008, 0x0008)
	obj := FromElements([]core.Element{
		dicomtest.NewOBElement(rawTag, []byte{0x01, 0x02, 0x03}),
		{
			Header: core.ElementHeader{Tag: stringTag, VR: core.VRCS},
			Value:  core.StringValue{"ORIGINAL", "PRIMARY"},
		},
	}, std.Dictionary)

	raw, ok := obj.GetRaw(rawTag)
	if !ok {
		t.Fatalf("GetRaw(%s) = ok false, want true", rawTag)
	}
	raw[0] = 0xFF
	originalRaw, _ := obj.GetRaw(rawTag)
	if originalRaw[0] != 0x01 {
		t.Fatalf("GetRaw should return a cloned slice, got %v", originalRaw)
	}
	if _, ok := obj.GetRaw(stringTag); ok {
		t.Fatalf("GetRaw(%s) = ok true, want false", stringTag)
	}
	if _, ok := obj.GetRaw(core.NewTag(0x0010, 0x0020)); ok {
		t.Fatal("GetRaw(missing) = ok true, want false")
	}
}

func TestGetStrings(t *testing.T) {
	singleTag := core.NewTag(0x0010, 0x0010)
	multiTag := core.NewTag(0x0008, 0x0008)
	rawTag := core.NewTag(0x7FE0, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewPNElement(singleTag, "DOE^JOHN"),
		{
			Header: core.ElementHeader{Tag: multiTag, VR: core.VRCS},
			Value:  core.StringValue{"ORIGINAL", "PRIMARY"},
		},
		dicomtest.NewOBElement(rawTag, []byte{0x01, 0x02, 0x03}),
	}, std.Dictionary)

	single, ok := obj.GetStrings(singleTag)
	if !ok {
		t.Fatalf("GetStrings(%s) = ok false, want true", singleTag)
	}
	if len(single) != 1 || single[0] != "DOE^JOHN" {
		t.Fatalf("GetStrings(%s) = %v, want [DOE^JOHN]", singleTag, single)
	}

	strings, ok := obj.GetStrings(multiTag)
	if !ok {
		t.Fatalf("GetStrings(%s) = ok false, want true", multiTag)
	}
	strings[0] = "MUTATED"
	originalStrings, _ := obj.GetStrings(multiTag)
	if originalStrings[0] != "ORIGINAL" {
		t.Fatalf("GetStrings should return a cloned slice, got %v", originalStrings)
	}
	if _, ok := obj.GetStrings(core.NewTag(0x0010, 0x0020)); ok {
		t.Fatal("GetStrings(missing) = ok true, want false")
	}
}

func TestLookupStringsDecodesSpecificCharacterSet(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(charsetTag, core.VRCS, "ISO_IR 100"),
		core.NewRawElement(nameTag, core.VRPN, []byte("Jos\xe9^Silva ")),
	}, std.Dictionary)

	values, err := obj.LookupStrings(nameTag)
	if err != nil {
		t.Fatalf("LookupStrings() error = %v", err)
	}
	if len(values) != 1 || values[0] != "José^Silva" {
		t.Fatalf("LookupStrings() = %v, want [José^Silva]", values)
	}
}

func TestLookupStringsUnsupportedSpecificCharacterSet(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(charsetTag, core.VRCS, "ISO_IR 192"),
		core.NewRawElement(nameTag, core.VRPN, []byte("Jos\xe9^Silva")),
	}, std.Dictionary)

	_, err := obj.LookupStrings(nameTag)
	if !errors.Is(err, dicomenc.ErrUnsupportedCharacterSet) {
		t.Fatalf("LookupStrings() error = %v, want ErrUnsupportedCharacterSet", err)
	}
	if _, ok := obj.GetStrings(nameTag); ok {
		t.Fatal("GetStrings() should fail for unsupported charset in strict mode")
	}
}

func TestLookupStringsFallbackSpecificCharacterSet(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(charsetTag, core.VRCS, "ISO_IR 192"),
		core.NewRawElement(nameTag, core.VRPN, []byte("Jos\xe9^Silva")),
	}, std.Dictionary)
	obj.SetTextOptions(TextOptions{
		AllowUnsupportedCharsetFallback: true,
		FallbackCharacterSet:            dicomenc.ISOIR100,
	})

	values, err := obj.LookupStrings(nameTag)
	if err != nil {
		t.Fatalf("LookupStrings() error = %v", err)
	}
	if len(values) != 1 || values[0] != "José^Silva" {
		t.Fatalf("LookupStrings() = %v, want [José^Silva]", values)
	}
}

func TestCharacterSet(t *testing.T) {
	obj := FromElements(nil, std.Dictionary)

	charset, err := obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() error = %v", err)
	}
	if charset.Name() != "ISO_IR 6" {
		t.Fatalf("CharacterSet() = %q, want ISO_IR 6", charset.Name())
	}

	obj.Put(dicomtest.NewStringElement(core.NewTag(0x0008, 0x0005), core.VRCS, "ISO_IR 100"))
	charset, err = obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() error = %v", err)
	}
	if charset.Name() != "ISO_IR 100" {
		t.Fatalf("CharacterSet() = %q, want ISO_IR 100", charset.Name())
	}
}

func TestGetPersonNameFullAndPartial(t *testing.T) {
	nameTag := core.NewTag(0x0010, 0x0010)
	partialTag := core.NewTag(0x0010, 0x1001)
	obj := FromElements([]core.Element{
		dicomtest.NewPNElement(nameTag, "Doe^John^Quincy^Dr.^Jr."),
		dicomtest.NewPNElement(partialTag, "Doe^Jane"),
	}, std.Dictionary)

	full, ok := obj.GetPersonName(nameTag)
	if !ok {
		t.Fatalf("GetPersonName(%s) = ok false, want true", nameTag)
	}
	if full.String() != "Dr. John Quincy Doe Jr." {
		t.Fatalf("GetPersonName(%s) = %q, want %q", nameTag, full.String(), "Dr. John Quincy Doe Jr.")
	}

	partial, ok := obj.GetPersonName(partialTag)
	if !ok {
		t.Fatalf("GetPersonName(%s) = ok false, want true", partialTag)
	}
	if partial.FamilyName != "Doe" || partial.GivenName != "Jane" || partial.MiddleName != "" {
		t.Fatalf("GetPersonName(%s) = %+v", partialTag, partial)
	}
}

func TestGetPersonNamesMultiValued(t *testing.T) {
	nameTag := core.NewTag(0x0010, 0x1001)
	obj := FromElements([]core.Element{
		{
			Header: core.ElementHeader{Tag: nameTag, VR: core.VRPN},
			Value:  core.StringValue{"Doe^John", "Smith^Jane^^Dr."},
		},
	}, std.Dictionary)

	names, ok := obj.GetPersonNames(nameTag)
	if !ok {
		t.Fatalf("GetPersonNames(%s) = ok false, want true", nameTag)
	}
	if len(names) != 2 {
		t.Fatalf("GetPersonNames(%s) returned %d names, want 2", nameTag, len(names))
	}
	if names[0].String() != "John Doe" || names[1].String() != "Dr. Jane Smith" {
		t.Fatalf("GetPersonNames(%s) = %v", nameTag, names)
	}
}

func TestTypedNumericAndUIDAccessors(t *testing.T) {
	nameTag := core.NewTag(0x0010, 0x0010)
	uidTag := core.NewTag(0x0008, 0x0018)
	uidsTag := core.NewTag(0x0008, 0x1155)
	intTag := core.NewTag(0x0020, 0x0013)
	intsTag := core.NewTag(0x0018, 0x1088)
	floatTag := core.NewTag(0x0018, 0x0088)
	floatsTag := core.NewTag(0x0018, 0x6020)
	obj := FromElements([]core.Element{
		dicomtest.NewPNElement(nameTag, " Doe ^ John ^^ Dr. "),
		core.NewRawElement(uidTag, core.VRUI, []byte("1.2.3\x00")),
		core.NewRawElement(uidsTag, core.VRUI, []byte("1.2.3\\4.5.6\x00")),
		dicomtest.NewStringElement(intTag, core.VRIS, "42"),
		core.Element{Header: core.ElementHeader{Tag: intsTag, VR: core.VRIS}, Value: core.StringValue{"7", "-3"}},
		dicomtest.NewStringElement(floatTag, core.VRDS, "1.25"),
		core.Element{Header: core.ElementHeader{Tag: floatsTag, VR: core.VRDS}, Value: core.StringValue{"1.25", "-0.5"}},
	}, std.Dictionary)

	if got, ok := obj.GetPersonName(nameTag); !ok || got.String() != "Dr. John Doe" {
		t.Fatalf("GetPersonName() = (%v, %v), want (Dr. John Doe, true)", got, ok)
	}

	uid, ok := obj.GetUID(uidTag)
	if !ok || uid != "1.2.3" {
		t.Fatalf("GetUID() = (%q, %v), want (1.2.3, true)", uid, ok)
	}
	uids, ok := obj.GetUIDs(uidsTag)
	if !ok || len(uids) != 2 || uids[0] != "1.2.3" || uids[1] != "4.5.6" {
		t.Fatalf("GetUIDs() = (%v, %v), want ([1.2.3 4.5.6], true)", uids, ok)
	}

	intValue, err := obj.GetInt(intTag)
	if err != nil || intValue != 42 {
		t.Fatalf("GetInt() = (%d, %v), want (42, nil)", intValue, err)
	}
	intValues, err := obj.GetInts(intsTag)
	if err != nil || len(intValues) != 2 || intValues[0] != 7 || intValues[1] != -3 {
		t.Fatalf("GetInts() = (%v, %v), want ([7 -3], nil)", intValues, err)
	}

	floatValue, err := obj.GetFloat(floatTag)
	if err != nil || floatValue != 1.25 {
		t.Fatalf("GetFloat() = (%v, %v), want (1.25, nil)", floatValue, err)
	}
	floatValues, err := obj.GetFloats(floatsTag)
	if err != nil || len(floatValues) != 2 || floatValues[0] != 1.25 || floatValues[1] != -0.5 {
		t.Fatalf("GetFloats() = (%v, %v), want ([1.25 -0.5], nil)", floatValues, err)
	}
}

func TestTypedNumericAccessorsInvalidInputs(t *testing.T) {
	intTag := core.NewTag(0x0020, 0x0013)
	floatTag := core.NewTag(0x0018, 0x0088)
	wrongVRTag := core.NewTag(0x0010, 0x0020)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(intTag, core.VRIS, "ABC"),
		dicomtest.NewStringElement(floatTag, core.VRDS, "ABC"),
		dicomtest.NewStringElement(wrongVRTag, core.VRLO, "42"),
	}, std.Dictionary)

	if _, err := obj.GetInt(intTag); err == nil {
		t.Fatal("GetInt() should fail for invalid IS input")
	}
	if _, err := obj.GetFloat(floatTag); err == nil {
		t.Fatal("GetFloat() should fail for invalid DS input")
	}
	if _, err := obj.GetInts(wrongVRTag); err == nil {
		t.Fatal("GetInts() should fail for wrong VR")
	}
}

func TestGetSequence(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1111)
	nestedSeqTag := core.NewTag(0x0008, 0x1115)
	stringTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewSequenceElement(
			seqTag,
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewSequenceElement(
						nestedSeqTag,
						core.DataSet{
							Elements: []core.Element{
								dicomtest.NewPNElement(stringTag, "NEST^ONE"),
							},
						},
					),
				},
			},
			core.DataSet{
				Elements: []core.Element{
					dicomtest.NewPNElement(stringTag, "NEST^TWO"),
				},
			},
		),
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0020), "NOT-A-SEQUENCE"),
	}, std.Dictionary)

	items, ok := obj.GetSequence(seqTag)
	if !ok {
		t.Fatalf("GetSequence(%s) = ok false, want true", seqTag)
	}
	if len(items) != 2 {
		t.Fatalf("GetSequence(%s) returned %d items, want 2", seqTag, len(items))
	}
	if _, ok := items[0].GetString(stringTag); ok {
		t.Fatalf("first nested item unexpectedly exposed %s directly", stringTag)
	}
	if got, ok := items[1].GetString(stringTag); !ok || got != "NEST^TWO" {
		t.Fatalf("second nested patient = %q, %v, want NEST^TWO, true", got, ok)
	}

	nestedItems, ok := items[0].GetSequence(nestedSeqTag)
	if !ok {
		t.Fatalf("GetSequence(%s) on nested item = ok false, want true", nestedSeqTag)
	}
	if len(nestedItems) != 1 {
		t.Fatalf("nested GetSequence(%s) returned %d items, want 1", nestedSeqTag, len(nestedItems))
	}
	if got, ok := nestedItems[0].GetString(stringTag); !ok || got != "NEST^ONE" {
		t.Fatalf("nested chained patient = %q, %v, want NEST^ONE, true", got, ok)
	}

	if _, ok := obj.GetSequence(core.NewTag(0x0010, 0x0020)); ok {
		t.Fatal("GetSequence(non-sequence) = ok true, want false")
	}
	if _, ok := obj.GetSequence(core.NewTag(0x0010, 0x0030)); ok {
		t.Fatal("GetSequence(missing) = ok true, want false")
	}
}

func TestNilObjectAccessors(t *testing.T) {
	var obj *Object

	if obj.Has(core.NewTag(0x0010, 0x0010)) {
		t.Fatal("Has() on nil object = true, want false")
	}
	if obj.Len() != 0 {
		t.Fatalf("Len() on nil object = %d, want 0", obj.Len())
	}
	if got := obj.ToDataSet(); len(got.Elements) != 0 {
		t.Fatalf("ToDataSet() on nil object returned %d elements, want 0", len(got.Elements))
	}
	if _, ok := obj.GetRaw(core.NewTag(0x7FE0, 0x0010)); ok {
		t.Fatal("GetRaw() on nil object = ok true, want false")
	}
	if _, ok := obj.GetStrings(core.NewTag(0x0010, 0x0010)); ok {
		t.Fatal("GetStrings() on nil object = ok true, want false")
	}
	if _, ok := obj.GetSequence(core.NewTag(0x0008, 0x1111)); ok {
		t.Fatal("GetSequence() on nil object = ok true, want false")
	}
}
