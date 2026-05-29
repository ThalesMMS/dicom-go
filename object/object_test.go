package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

type staticValueProvider []byte

func (p staticValueProvider) CopyValueTo(_ core.Tag, w io.Writer) (int64, error) {
	return io.Copy(w, bytes.NewReader(p))
}

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

func TestPutUpdateClearsDeferredAmbiguityAndReconcilesCount(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	deferred := core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VROB, Length: 4, LengthSet: true},
		Value:  nil,
	}
	obj := New(std.Dictionary)
	obj.setValueProvider(staticValueProvider{1, 2, 3, 4})

	obj.Put(deferred)
	if obj.deferredCount != 1 || obj.ambiguousDeferred[tag] {
		t.Fatalf("initial deferred state = count %d ambiguous %v, want 1/false", obj.deferredCount, obj.ambiguousDeferred[tag])
	}

	obj.Put(core.NewRawElement(tag, core.VROB, []byte{9, 8, 7, 6}))
	if obj.deferredCount != 0 || obj.ambiguousDeferred[tag] {
		t.Fatalf("materialized update state = count %d ambiguous %v, want 0/false", obj.deferredCount, obj.ambiguousDeferred[tag])
	}

	obj.Put(deferred)
	if obj.deferredCount != 1 || obj.ambiguousDeferred[tag] {
		t.Fatalf("second deferred state = count %d ambiguous %v, want 1/false", obj.deferredCount, obj.ambiguousDeferred[tag])
	}
	var got bytes.Buffer
	if _, err := obj.CopyValueTo(tag, &got); err != nil {
		t.Fatalf("CopyValueTo() error = %v", err)
	}
	if !bytes.Equal(got.Bytes(), []byte{1, 2, 3, 4}) {
		t.Fatalf("CopyValueTo() = %v, want provider bytes", got.Bytes())
	}
}

func TestPutResolvesParsedDeferredDuplicateAmbiguity(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	deferred := func(length core.Length) core.Element {
		return core.Element{
			Header: core.ElementHeader{Tag: tag, VR: core.VROB, Length: length, LengthSet: true},
			Value:  nil,
		}
	}
	obj := fromParsedDataSetWithTextOptions(core.DataSet{Elements: []core.Element{
		deferred(4),
		deferred(8),
	}}, std.Dictionary, TextOptions{})
	if !obj.ambiguousDeferred[tag] || obj.deferredCount != 0 {
		t.Fatalf("parsed duplicate state = ambiguous %v count %d, want true/0", obj.ambiguousDeferred[tag], obj.deferredCount)
	}

	obj.Put(core.NewRawElement(tag, core.VROB, []byte{9, 8, 7, 6}))
	if obj.ambiguousDeferred[tag] || obj.deferredCount != 0 {
		t.Fatalf("updated duplicate state = ambiguous %v count %d, want false/0", obj.ambiguousDeferred[tag], obj.deferredCount)
	}
	var got bytes.Buffer
	if _, err := obj.CopyValueTo(tag, &got); err != nil {
		t.Fatalf("CopyValueTo() error = %v", err)
	}
	if !bytes.Equal(got.Bytes(), []byte{9, 8, 7, 6}) {
		t.Fatalf("CopyValueTo() = %v, want updated bytes", got.Bytes())
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

func TestLookupStringsSplitsMultiValueTextAfterCharsetDecode(t *testing.T) {
	charset, err := dicomenc.ParseCharacterSet("ISO 2022 IR 13")
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	first, err := charset.Encode("表")
	if err != nil {
		t.Fatalf("Encode(first) error = %v", err)
	}
	second, err := charset.Encode("裏")
	if err != nil {
		t.Fatalf("Encode(second) error = %v", err)
	}
	if !bytes.Contains(first, []byte{'\\'}) {
		t.Fatalf("encoded first value = % X, want embedded 0x5C byte", first)
	}

	charsetTag := core.NewTag(0x0008, 0x0005)
	loTag := core.NewTag(0x0008, 0x103E)
	pnTag := core.NewTag(0x0010, 0x0010)

	rawLO := append(append(append([]byte(nil), first...), '\\'), second...)
	rawLO = append(rawLO, ' ')

	rawPN := append(append([]byte(nil), first...), []byte("^Taro")...)
	rawPN = append(rawPN, '\\')
	rawPN = append(rawPN, second...)
	rawPN = append(rawPN, []byte("^Jiro ")...)

	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(charsetTag, core.VRCS, "ISO 2022 IR 13"),
		core.NewRawElement(loTag, core.VRLO, rawLO),
		core.NewRawElement(pnTag, core.VRPN, rawPN),
	}, std.Dictionary)

	loValues, err := obj.LookupStrings(loTag)
	if err != nil {
		t.Fatalf("LookupStrings(LO) error = %v", err)
	}
	if len(loValues) != 2 || loValues[0] != "表" || loValues[1] != "裏" {
		t.Fatalf("LookupStrings(LO) = %v, want [表 裏]", loValues)
	}

	pnValues, err := obj.LookupStrings(pnTag)
	if err != nil {
		t.Fatalf("LookupStrings(PN) error = %v", err)
	}
	if len(pnValues) != 2 || pnValues[0] != "表^Taro" || pnValues[1] != "裏^Jiro" {
		t.Fatalf("LookupStrings(PN) = %v, want [表^Taro 裏^Jiro]", pnValues)
	}
}

func TestLookupStringsUnsupportedSpecificCharacterSet(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(charsetTag, core.VRCS, "UNKNOWN"),
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
		dicomtest.NewStringElement(charsetTag, core.VRCS, "UNKNOWN"),
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

func TestLookupStringsDecodesPersonNameComponentGroups(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	rawName := append([]byte("Jos\xe9^Silva="), []byte("山田^太郎")...)
	rawName = append(rawName, '=')
	rawName = append(rawName, 0xD6, 0xD0, 0xCE, 0xC4)
	obj := FromElements([]core.Element{
		core.NewRawElement(charsetTag, core.VRCS, []byte("ISO_IR 100\\ISO_IR 192\\GBK")),
		core.NewRawElement(nameTag, core.VRPN, rawName),
	}, std.Dictionary)

	values, err := obj.LookupStrings(nameTag)
	if err != nil {
		t.Fatalf("LookupStrings() error = %v", err)
	}
	if len(values) != 1 || values[0] != "José^Silva=山田^太郎=中文" {
		t.Fatalf("LookupStrings() = %v", values)
	}
	name, ok := obj.GetPersonName(nameTag)
	if !ok || name.ToDICOMString() != "José^Silva=山田^太郎=中文" {
		t.Fatalf("GetPersonName() = (%q, %v)", name.ToDICOMString(), ok)
	}
}

func TestLookupStringsDecodesISO2022PersonNameComponentGroups(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	japanese := []byte{
		0x1B, 0x24, 0x42, 0x3B, 0x33, 0x45, 0x44, 0x1B, 0x28, 0x42,
		'^',
		0x1B, 0x24, 0x42, 0x42, 0x40, 0x4F, 0x3A, 0x1B, 0x28, 0x42,
	}
	korean := []byte{0xC8, 0xAB, 0xB1, 0xE6, 0xB5, 0xBF}
	rawName := append([]byte("Jos\xe9^Silva="), japanese...)
	rawName = append(rawName, '=')
	rawName = append(rawName, korean...)
	obj := FromElements([]core.Element{
		core.NewRawElement(charsetTag, core.VRCS, []byte("ISO_IR 100\\ISO 2022 IR 87\\ISO 2022 IR 149")),
		core.NewRawElement(nameTag, core.VRPN, rawName),
	}, std.Dictionary)

	values, err := obj.LookupStrings(nameTag)
	if err != nil {
		t.Fatalf("LookupStrings() error = %v", err)
	}
	if len(values) != 1 || values[0] != "José^Silva=山田^太郎=홍길동" {
		t.Fatalf("LookupStrings() = %v", values)
	}
	name, ok := obj.GetPersonName(nameTag)
	if !ok || name.ToDICOMString() != "José^Silva=山田^太郎=홍길동" {
		t.Fatalf("GetPersonName() = (%q, %v)", name.ToDICOMString(), ok)
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

func TestObjectSetTextOptionsInvalidatesCharsetCache(t *testing.T) {
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 6"),
	}, std.Dictionary)

	charset, err := obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() error = %v", err)
	}
	if charset.Name() != "ISO_IR 6" {
		t.Fatalf("CharacterSet() = %q, want ISO_IR 6", charset.Name())
	}

	obj.elements[tagSpecificCharacterSet] = dicomtest.NewStringElement(tagSpecificCharacterSet, core.VRCS, "ISO_IR 100")
	charset, err = obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() error = %v", err)
	}
	if charset.Name() != "ISO_IR 6" {
		t.Fatalf("CharacterSet() after element mutation without SetTextOptions = %q, want cached ISO_IR 6", charset.Name())
	}

	obj.SetTextOptions(TextOptions{})
	charset, err = obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() error = %v", err)
	}
	if charset.Name() != "ISO_IR 100" {
		t.Fatalf("CharacterSet() after SetTextOptions = %q, want ISO_IR 100", charset.Name())
	}
}

func TestLookupTemporalValues(t *testing.T) {
	dateTag := core.NewTag(0x0008, 0x0020)
	timeTag := core.NewTag(0x0008, 0x0030)
	dateTimeTag := core.NewTag(0x0008, 0x002A)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(dateTag, core.VRDA, "202405"),
		dicomtest.NewStringElement(timeTag, core.VRTM, "143015.123"),
		dicomtest.NewStringElement(dateTimeTag, core.VRDT, "20240528143015.123456-0300"),
	}, std.Dictionary)

	date, err := obj.LookupDate(dateTag)
	if err != nil {
		t.Fatal(err)
	}
	if date.Precision != dcmtime.PrecisionMonth || date.DCM() != "202405" {
		t.Fatalf("LookupDate() = precision %s DCM %q, want MONTH 202405", date.Precision, date.DCM())
	}

	tm, err := obj.LookupTime(timeTag)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Precision != dcmtime.PrecisionMS3 || tm.DCM() != "143015.123" {
		t.Fatalf("LookupTime() = precision %s DCM %q, want MS3 143015.123", tm.Precision, tm.DCM())
	}

	dt, err := obj.LookupDateTime(dateTimeTag)
	if err != nil {
		t.Fatal(err)
	}
	if dt.Precision != dcmtime.PrecisionFull || dt.NoOffset || dt.DCM() != "20240528143015.123456-0300" {
		t.Fatalf("LookupDateTime() = precision %s noOffset %v DCM %q", dt.Precision, dt.NoOffset, dt.DCM())
	}
}

func TestLookupTemporalValuesRejectWrongVR(t *testing.T) {
	tag := core.NewTag(0x0008, 0x0020)
	obj := FromElements([]core.Element{
		dicomtest.NewStringElement(tag, core.VRLO, "20240528"),
	}, std.Dictionary)

	if _, err := obj.LookupDate(tag); err == nil {
		t.Fatal("LookupDate() error = nil, want wrong-VR error")
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

func TestGetSequenceCachedItemsRemainIndependentAcrossCalls(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nameTag := core.NewTag(0x0010, 0x0010)
	idTag := core.NewTag(0x0010, 0x0020)
	obj := FromElements([]core.Element{
		dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{
			dicomtest.NewPNElement(nameTag, "ORIGINAL^PATIENT"),
			dicomtest.NewStringElement(idTag, core.VRLO, "PATIENT-001"),
		}}),
	}, std.Dictionary)

	first, _ := obj.GetSequence(sequenceTag)
	second, _ := obj.GetSequence(sequenceTag)
	if first[0] == second[0] {
		t.Fatal("repeated GetSequence() returned the same mutable item pointer")
	}
	first[0].Put(dicomtest.NewPNElement(nameTag, "CHANGED^PATIENT"))
	if !first[0].Remove(idTag) {
		t.Fatal("Remove() on first facade = false, want true")
	}

	if got, _ := first[0].GetString(nameTag); got != "CHANGED^PATIENT" {
		t.Fatalf("mutated first item name = %q, want CHANGED^PATIENT", got)
	}
	if got, _ := second[0].GetString(nameTag); got != "ORIGINAL^PATIENT" {
		t.Fatalf("second item name = %q, want ORIGINAL^PATIENT", got)
	}
	if !second[0].Has(idTag) {
		t.Fatal("mutation of first facade removed Patient ID from second facade")
	}
	fresh, _ := obj.GetSequence(sequenceTag)
	if got, _ := fresh[0].GetString(nameTag); got != "ORIGINAL^PATIENT" || !fresh[0].Has(idTag) {
		t.Fatalf("fresh item = name %q hasID %v, want original/true", got, fresh[0].Has(idTag))
	}
}

func TestGetSequenceCacheInvalidatesWhenSequenceOrTextConfigurationChanges(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nameTag := core.NewTag(0x0010, 0x0010)
	sequence := func(name string) core.Element {
		return dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{
			dicomtest.NewPNElement(nameTag, name),
		}})
	}
	obj := FromElements([]core.Element{sequence("FIRST^PATIENT")}, std.Dictionary)

	obj.GetSequence(sequenceTag)
	if len(obj.sequenceCache) != 1 {
		t.Fatalf("sequence cache size = %d, want 1", len(obj.sequenceCache))
	}
	obj.Put(sequence("SECOND^PATIENT"))
	if _, cached := obj.sequenceCache[sequenceTag]; cached {
		t.Fatal("Put(sequence) did not invalidate cached items")
	}
	items, _ := obj.GetSequence(sequenceTag)
	if got, _ := items[0].GetString(nameTag); got != "SECOND^PATIENT" {
		t.Fatalf("item after Put = %q, want SECOND^PATIENT", got)
	}

	obj.SetTextOptions(TextOptions{})
	if len(obj.sequenceCache) != 0 {
		t.Fatalf("SetTextOptions() left %d cached sequences, want 0", len(obj.sequenceCache))
	}
	obj.GetSequence(sequenceTag)
	obj.SetValueByteOrder(binary.BigEndian)
	if len(obj.sequenceCache) != 0 {
		t.Fatalf("SetValueByteOrder() left %d cached sequences, want 0", len(obj.sequenceCache))
	}
}

func TestNilObjectAccessors(t *testing.T) {
	var obj *Object
	patientName := dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "IGNORED^PATIENT")
	obj.Put(patientName)
	obj.put(patientName, true)

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

// TestObjectMustGetReturnsElementOrError verifies MustGet returns the element
// when present, and a descriptive error when the tag is absent.
func TestObjectMustGetReturnsElementOrError(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		dicomtest.NewPNElement(tag, "FOUND^PATIENT"),
	}, std.Dictionary)

	elem, err := obj.MustGet(tag)
	if err != nil {
		t.Fatalf("MustGet present tag: %v", err)
	}
	if elem.Tag() != tag {
		t.Fatalf("MustGet returned tag %s, want %s", elem.Tag(), tag)
	}

	_, err = obj.MustGet(core.NewTag(0x0010, 0x0020))
	requireMissingTagError(t, err, core.NewTag(0x0010, 0x0020))
}

// TestObjectSortedElementsReturnsSortedOrder verifies SortedElements returns
// elements sorted by ascending tag regardless of insertion order.
func TestObjectSortedElementsReturnsSortedOrder(t *testing.T) {
	tag1 := core.NewTag(0x0020, 0x000D) // group 0020
	tag2 := core.NewTag(0x0010, 0x0010) // group 0010
	tag3 := core.NewTag(0x0008, 0x0018) // group 0008

	obj := New(std.Dictionary)
	obj.Put(dicomtest.NewStringElement(tag1, core.VRUI, dicomtest.TestStudyInstanceUID))
	obj.Put(dicomtest.NewPNElement(tag2, "TEST^PATIENT"))
	obj.Put(dicomtest.NewStringElement(tag3, core.VRUI, dicomtest.TestSOPInstanceUID))

	sorted := obj.SortedElements()
	if len(sorted) != 3 {
		t.Fatalf("SortedElements count = %d, want 3", len(sorted))
	}
	for i := 1; i < len(sorted); i++ {
		prev := sorted[i-1].Tag()
		curr := sorted[i].Tag()
		if !prev.Less(curr) {
			t.Fatalf("SortedElements: element %d (%s) not less than element %d (%s)", i-1, prev, i, curr)
		}
	}
	if sorted[0].Tag() != tag3 {
		t.Fatalf("first sorted tag = %s, want %s", sorted[0].Tag(), tag3)
	}
}

// TestObjectLookupStringReturnsMissingError verifies the error path of
// LookupString when a tag is absent from the object.
func TestObjectLookupStringReturnsMissingError(t *testing.T) {
	tag := core.NewTag(0x0010, 0x0010)
	obj := New(std.Dictionary)
	_, err := obj.LookupString(tag)
	requireMissingTagError(t, err, tag)
}

func requireMissingTagError(t *testing.T, err error, tag core.Tag) {
	t.Helper()

	if err == nil {
		t.Fatalf("missing tag %s: expected error, got nil", tag)
	}
	if !strings.Contains(err.Error(), "missing element") || !strings.Contains(err.Error(), tag.String()) {
		t.Fatalf("missing tag error = %q, want missing element message containing %s", err, tag)
	}
}

// TestObjectElementsPreservesInsertionOrder verifies that Elements() returns
// elements in insertion order, not sorted order.
func TestObjectElementsPreservesInsertionOrder(t *testing.T) {
	tag1 := core.NewTag(0x0020, 0x000D)
	tag2 := core.NewTag(0x0010, 0x0010)

	obj := New(std.Dictionary)
	obj.Put(dicomtest.NewStringElement(tag1, core.VRUI, dicomtest.TestStudyInstanceUID))
	obj.Put(dicomtest.NewPNElement(tag2, "TEST^PATIENT"))

	elems := obj.Elements()
	if len(elems) != 2 {
		t.Fatalf("Elements() count = %d, want 2", len(elems))
	}
	if elems[0].Tag() != tag1 {
		t.Fatalf("Elements()[0] = %s, want %s (inserted first)", elems[0].Tag(), tag1)
	}
	if elems[1].Tag() != tag2 {
		t.Fatalf("Elements()[1] = %s, want %s (inserted second)", elems[1].Tag(), tag2)
	}
}

// TestObjectGetUIDsReturnsFalseForEmptyUID verifies that GetUIDs returns
// false when all values are empty/whitespace after trimming.
func TestObjectGetUIDsReturnsFalseForEmptyUID(t *testing.T) {
	tag := core.NewTag(0x0008, 0x0018)
	obj := FromElements([]core.Element{
		core.NewRawElement(tag, core.VRUI, []byte("\x00")),
	}, std.Dictionary)

	if _, ok := obj.GetUIDs(tag); ok {
		t.Fatal("GetUIDs() on all-null UID: expected false")
	}
}
