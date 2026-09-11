package dictionary_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

func TestPrivateCatalogIdentityImmutabilityAndOverlayPrecedence(t *testing.T) {
	entries := []dictionary.PrivateEntry{{Creator: " ACME ", Group: 0x0011, Offset: 1, VR: core.VRUS, Keyword: "AcmeValue"}, {Creator: "Other", Group: 0x0011, Offset: 1, VR: core.VRLO}}
	c, err := dictionary.NewPrivateCatalog(entries)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].VR = core.VRFD
	snapshot := c.Entries()
	snapshot[0].VR = core.VRFD
	for _, block := range []uint16{0x10, 0x1f, 0x80, 0xff} {
		tag := core.NewTag(0x0011, block<<8|1)
		e, ok := dictionary.LookupScopedEntry(c, tag, "ACME")
		if !ok || e.Tag != tag || e.VR != core.VRUS {
			t.Fatalf("block %x: %+v", block, e)
		}
		other, ok := dictionary.LookupScopedEntry(c, tag, "Other")
		if !ok || other.VR != core.VRLO {
			t.Fatal("creator confused")
		}
		if _, ok := dictionary.LookupScopedEntry(c, tag, "acme"); ok {
			t.Fatal("case folded")
		}
	}
	tag := core.NewTag(0x0011, 0x1f01)
	overlay := stubDictionary{entry: dictionary.Entry{Tag: tag, VR: core.VRUN}}
	for _, tc := range []struct {
		dict dictionary.DataDictionary
		want core.VR
	}{{dictionary.Chain{overlay, c}, core.VRUN}, {dictionary.Chain{c, overlay}, core.VRUS}} {
		e, ok := dictionary.LookupScopedEntry(tc.dict, tag, "ACME")
		if !ok || e.VR != tc.want {
			t.Fatalf("overlay precedence: %+v", e)
		}
	}
	if dictionary.HasPrivateDictionary(dictionary.Chain{overlay, dictionary.Empty{}}) {
		t.Fatal("legacy Chain activated private resolution")
	}
	legacy := dictionary.Chain{overlay, dictionary.Empty{}}
	if dictionary.HasPrivateDictionary(&legacy) {
		t.Fatal("legacy Chain pointer activated private resolution")
	}
	scoped := dictionary.Chain{c, overlay}
	if e, ok := dictionary.LookupScopedEntry(&scoped, tag, "ACME"); !ok || e.VR != core.VRUS || !dictionary.HasPrivateDictionary(&scoped) {
		t.Fatal("Chain pointer lost scoped precedence")
	}
	if _, ok := c.ByTag(tag); ok {
		t.Fatal("unscoped private lookup")
	}
	if _, ok := c.ByKeyword("AcmeValue"); ok {
		t.Fatal("unbound keyword lookup")
	}
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				e, ok := c.ByPrivate("ACME", 0x11, 1)
				if !ok || e.VR != core.VRUS {
					t.Error("shared catalog changed")
				}
			}
		}()
	}
	wg.Wait()
}

func TestPrivateCreatorValidationAndReservationConflicts(t *testing.T) {
	for _, value := range []string{"", " ", "A\\B", "A\x00", "A\tB", "A\x1bB", "\u00e9", "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLM"} {
		if _, err := dictionary.PrivateCreatorID(value); !errors.Is(err, dictionary.ErrPrivateCreator) {
			t.Fatalf("invalid creator accepted")
		}
	}
	for _, group := range []uint16{0, 1, 3, 5, 7, 0x10, 0xffff} {
		if dictionary.IsPrivateGroup(group) {
			t.Fatalf("forbidden group %x", group)
		}
	}
	makeCreator := func(group, block uint16, id string) core.Element {
		return core.Element{Header: core.ElementHeader{Tag: core.NewTag(group, block), VR: core.VRLO}, Value: core.StringValue{id}}
	}
	s, _ := dictionary.NewPrivateReservations(3)
	for _, e := range []core.Element{makeCreator(0x11, 0x1f, "ACME"), makeCreator(0x13, 0x1f, "ACME")} {
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	if id, err := s.Creator(core.NewTag(0x11, 0x1f10)); err != nil || id != "ACME" {
		t.Fatalf("reservation: %s %v", id, err)
	}
	if err := s.Observe(makeCreator(0x11, 0x20, "ACME")); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal(err)
	}
	if _, err := s.Creator(core.NewTag(0x11, 0x1f10)); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal("ambiguous group resolved")
	}
	if id, err := s.Creator(core.NewTag(0x13, 0x1f10)); err != nil || id != "ACME" {
		t.Fatal("other group contaminated")
	}
	if err := s.Observe(makeCreator(0x15, 0x10, "NEW")); !errors.Is(err, dictionary.ErrPrivateResourceLimit) {
		t.Fatal("reservation budget")
	}
	empty, _ := dictionary.NewPrivateReservations(0)
	if _, err := empty.Creator(core.NewTag(0x11, 0x1f10)); !errors.Is(err, dictionary.ErrPrivateCreatorMissing) {
		t.Fatal("scope inherited")
	}
	if err := empty.Observe(makeCreator(0x11, 0x10, "A")); err != nil {
		t.Fatal(err)
	}
	if err := empty.Observe(makeCreator(0x11, 0x10, "B")); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal("slot conflict")
	}
}
