package object

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
)

func TestRemoveDeletesElement(t *testing.T) {
	a := core.NewTag(0x0010, 0x0010)
	b := core.NewTag(0x0010, 0x0020)
	c := core.NewTag(0x0010, 0x0040)
	obj := FromElements([]core.Element{
		core.NewRawElement(a, core.VRPN, []byte("DOE^JOHN")),
		core.NewRawElement(b, core.VRLO, []byte("PATIENT-001")),
		core.NewRawElement(c, core.VRCS, []byte("M ")),
	}, std.Dictionary)

	if !obj.Remove(b) {
		t.Fatal("Remove(present) = false, want true")
	}
	if obj.Has(b) {
		t.Error("element still present after Remove")
	}
	if obj.Len() != 2 {
		t.Errorf("Len() = %d after Remove, want 2", obj.Len())
	}

	// Removing again is a no-op returning false.
	if obj.Remove(b) {
		t.Error("Remove(absent) = true, want false")
	}

	// Ordering of the survivors is preserved, with the removed tag gone.
	want := []core.Tag{a, c}
	got := obj.Elements()
	if len(got) != len(want) {
		t.Fatalf("Elements() len = %d, want %d", len(got), len(want))
	}
	for i, tag := range want {
		if got[i].Tag() != tag {
			t.Errorf("Elements()[%d] = %s, want %s", i, got[i].Tag(), tag)
		}
	}
}

func TestRemoveNilObject(t *testing.T) {
	var obj *Object
	if obj.Remove(core.NewTag(0x0010, 0x0010)) {
		t.Error("Remove on nil object = true, want false")
	}
}

func TestRemoveSpecificCharacterSetInvalidatesCache(t *testing.T) {
	charsetTag := core.NewTag(0x0008, 0x0005)
	nameTag := core.NewTag(0x0010, 0x0010)
	obj := FromElements([]core.Element{
		core.NewRawElement(charsetTag, core.VRCS, []byte("ISO_IR 192")),
		core.NewRawElement(nameTag, core.VRPN, []byte("DOE^JOHN")),
	}, std.Dictionary)

	// Prime the character-set cache with the UTF-8 override.
	primed, err := obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() error = %v", err)
	}
	if primed.Name() == dicomenc.DefaultCharacterSet.Name() {
		t.Fatalf("primed charset = %q, want the ISO_IR 192 override", primed.Name())
	}
	if !obj.Remove(charsetTag) {
		t.Fatal("Remove(charset) = false, want true")
	}
	cs, err := obj.CharacterSet()
	if err != nil {
		t.Fatalf("CharacterSet() after remove error = %v", err)
	}
	// With the override removed, the invalidated cache falls back to the default.
	if cs.Name() != dicomenc.DefaultCharacterSet.Name() {
		t.Errorf("CharacterSet() after remove = %q, want default %q", cs.Name(), dicomenc.DefaultCharacterSet.Name())
	}
}

func TestOrderCompactionPreservesUpdateRemoveAndReinsertOrder(t *testing.T) {
	const count = orderIndexThreshold + 200
	tags := make([]core.Tag, count)
	elements := make([]core.Element, count)
	for i := range tags {
		tags[i] = core.NewTag(0x0011, uint16(0x1000+i))
		elements[i] = core.NewRawElement(tags[i], core.VRLO, []byte("INITIAL"))
	}
	obj := FromElements(elements, std.Dictionary)

	// Updating the first half moves it after the untouched second half. Removing
	// that second half creates enough stale slots to exercise compaction.
	for i := 0; i < count/2; i++ {
		obj.Put(core.NewRawElement(tags[i], core.VRLO, []byte("UPDATED")))
	}
	for i := count / 2; i < count; i++ {
		if !obj.Remove(tags[i]) {
			t.Fatalf("Remove(%s) = false, want true", tags[i])
		}
	}
	if len(obj.order) > len(obj.elements)*2 {
		t.Fatalf("remove-heavy order length = %d for %d live elements; want proportional compaction", len(obj.order), len(obj.elements))
	}
	if !obj.Remove(tags[0]) {
		t.Fatal("Remove(first updated tag) = false, want true")
	}
	obj.Put(core.NewRawElement(tags[0], core.VRLO, []byte("REINSERTED")))

	got := obj.Elements()
	if len(got) != count/2 {
		t.Fatalf("Elements() len = %d, want %d", len(got), count/2)
	}
	for i := 0; i < count/2-1; i++ {
		if got[i].Tag() != tags[i+1] {
			t.Fatalf("Elements()[%d] = %s, want %s", i, got[i].Tag(), tags[i+1])
		}
	}
	if got[len(got)-1].Tag() != tags[0] {
		t.Fatalf("last tag = %s, want reinserted %s", got[len(got)-1].Tag(), tags[0])
	}
}

func TestGetTimeDispatchesByVR(t *testing.T) {
	daTag := core.NewTag(0x0008, 0x0020) // Study Date
	tmTag := core.NewTag(0x0008, 0x0030) // Study Time
	dtTag := core.NewTag(0x0008, 0x002A) // Acquisition DateTime
	obj := FromElements([]core.Element{
		core.NewRawElement(daTag, core.VRDA, []byte("20240115")),
		core.NewRawElement(tmTag, core.VRTM, []byte("143025")),
		core.NewRawElement(dtTag, core.VRDT, []byte("20240115143025")),
	}, std.Dictionary)

	da, err := obj.GetTime(daTag)
	if err != nil {
		t.Fatalf("GetTime(DA) error = %v", err)
	}
	if da.Year() != 2024 || da.Month() != 1 || da.Day() != 15 {
		t.Errorf("GetTime(DA) = %v, want 2024-01-15", da)
	}

	tm, err := obj.GetTime(tmTag)
	if err != nil {
		t.Fatalf("GetTime(TM) error = %v", err)
	}
	if tm.Hour() != 14 || tm.Minute() != 30 || tm.Second() != 25 {
		t.Errorf("GetTime(TM) = %v, want 14:30:25", tm)
	}

	dt, err := obj.GetTime(dtTag)
	if err != nil {
		t.Fatalf("GetTime(DT) error = %v", err)
	}
	if dt.Year() != 2024 || dt.Hour() != 14 || dt.Second() != 25 {
		t.Errorf("GetTime(DT) = %v, want 2024-01-15 14:30:25", dt)
	}
}

func TestGetTimeErrors(t *testing.T) {
	obj := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("PATIENT-001")),
	}, std.Dictionary)

	// Missing element.
	if _, err := obj.GetTime(core.NewTag(0x0008, 0x0020)); err == nil {
		t.Error("GetTime(missing) error = nil, want error")
	}
	// Non-temporal VR.
	if _, err := obj.GetTime(core.NewTag(0x0010, 0x0020)); err == nil {
		t.Error("GetTime(non-temporal VR) error = nil, want error")
	}
	// Nil object.
	var nilObj *Object
	if _, err := nilObj.GetTime(core.NewTag(0x0008, 0x0020)); err == nil {
		t.Error("GetTime on nil object error = nil, want error")
	}
}
