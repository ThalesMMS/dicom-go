package curated_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/deid"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/curated"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func catalog(t testing.TB) *curated.Catalog {
	t.Helper()
	c, err := curated.New()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func creator(group, block uint16, id string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: core.NewTag(group, block), VR: core.VRLO}, Value: core.StringValue{id}}
}
func sourceValue(vr core.VR) []byte {
	switch vr {
	case core.VRSL:
		return []byte{123, 0, 0, 0}
	case core.VRSS:
		return []byte{123, 0}
	case core.VRFL:
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, math.Float32bits(1.25))
		return b
	case core.VRDS:
		return []byte("1.25")
	case core.VROB:
		return []byte{1, 2, 3, 4}
	default:
		return []byte("SYNTHETIC ")
	}
}
func readImplicit(t testing.TB, elements []core.Element, dict dictionary.DataDictionary) *object.Object {
	t.Helper()
	source := object.FromElements(elements, std.Dictionary)
	defer source.Close()
	var b bytes.Buffer
	if err := object.WriteDataSet(&b, source, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	obj, err := object.ReadDataSetWithOptions(bytes.NewReader(b.Bytes()), transfer.ImplicitVRLittleEndian, object.ReadFileOptions{Dictionary: dict})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { obj.Close() })
	return obj
}

func TestCuratedCatalogProvenanceAndAllVariableBlocks(t *testing.T) {
	c := catalog(t)
	entries := c.Entries()
	if len(entries) != 21 {
		t.Fatal("curated scope changed")
	}
	for _, r := range entries {
		d := r.Definition
		if r.Origin.Commit != "ac002900cab167509881e5b837cdef5dcb07cd37" || r.Origin.Verification != "reviewed-upstream" || r.Origin.SourceLine <= 0 || len(r.Origin.SourceSHA256) != 64 || len(r.Origin.DefinitionSHA256) != 64 || r.Origin.License != "BSD-3-Clause" || r.Origin.Restrictions == "" {
			t.Fatal("incomplete audit origin")
		}
		for _, block := range []uint16{0x10, 0x1f, 0x80, 0xff} {
			tag := core.NewTag(d.Group, block<<8|uint16(d.Offset))
			elements := []core.Element{creator(d.Group, block, d.Creator), core.NewRawElement(tag, d.VR, sourceValue(d.VR))}
			o := readImplicit(t, elements, dictionary.Chain{c, std.Dictionary})
			e, ok := o.Get(tag)
			if !ok || e.Header.VR != d.VR {
				t.Fatalf("block %x lost VR", block)
			}
			entry, err := o.ResolvePrivateEntry(tag)
			if err != nil || entry.Keyword != d.Keyword {
				t.Fatalf("scoped lookup: %v", err)
			}
			plain := readImplicit(t, elements, std.Dictionary)
			unknown, ok := plain.Get(tag)
			if !ok || unknown.Header.VR != core.VRUN {
				t.Fatal("default parser gained private definitions")
			}
			if _, ok := std.Dictionary.ByTag(tag); ok {
				t.Fatal("standard dictionary changed")
			}
		}
		got, ok := c.Lookup(" "+d.Creator+" ", d.Group, d.Offset)
		if !ok || got != r {
			t.Fatal("audit lookup differs")
		}
	}
	if !strings.Contains(curated.LicenseNotice(), "OFFIS") || !strings.Contains(curated.LicenseNotice(), "Redistribution and use") || !strings.Contains(curated.LicenseNotice(), "AS IS") {
		t.Fatal("license notice incomplete")
	}
}

func TestCuratedCatalogScopeOverlayAndUnknownFallback(t *testing.T) {
	c := catalog(t)
	tag := core.NewTag(0x19, 0x1f02)
	local, err := dictionary.NewPrivateCatalog([]dictionary.PrivateEntry{{Creator: "GEMS_ACQU_01", Group: 0x19, Offset: 2, VR: core.VRLO, VM: "1", Keyword: "LocalReview"}})
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := dictionary.LookupScopedEntry(dictionary.Chain{local, c, std.Dictionary}, tag, "GEMS_ACQU_01"); !ok || e.VR != core.VRLO || e.Keyword != "LocalReview" {
		t.Fatal("overlay lost")
	}
	for _, owner := range []string{"UNKNOWN_CREATOR", "gems_acqu_01"} {
		o := readImplicit(t, []core.Element{creator(0x19, 0x1f, owner), core.NewRawElement(tag, core.VRSL, []byte{123, 0, 0, 0})}, dictionary.Chain{c, std.Dictionary})
		e, _ := o.Get(tag)
		if e.Header.VR != core.VRUN {
			t.Fatal("creator inferred")
		}
	}
	seq := core.NewTag(8, 0x1110)
	obj := readImplicit(t, []core.Element{
		creator(0x19, 0x1f, "GEMS_ACQU_01"),
		{Header: core.ElementHeader{Tag: seq, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{
			{Elements: []core.Element{core.NewRawElement(tag, core.VRSL, []byte{1, 0, 0, 0})}},
			{Elements: []core.Element{creator(0x19, 0x80, "GEMS_ACQU_01"), core.NewRawElement(core.NewTag(0x19, 0x8002), core.VRSL, []byte{2, 0, 0, 0})}},
		}}},
	}, dictionary.Chain{c, std.Dictionary})
	items, ok := obj.GetSequence(seq)
	if !ok || len(items) != 2 {
		t.Fatal("sequence lost")
	}
	a, _ := items[0].Get(tag)
	b, _ := items[1].Get(core.NewTag(0x19, 0x8002))
	if a.Header.VR != core.VRUN || b.Header.VR != core.VRSL {
		t.Fatal("private reservations crossed item scope")
	}
}

func TestCuratedCatalogKnownDoesNotAuthorizeRetention(t *testing.T) {
	c := catalog(t)
	tag := core.NewTag(0x29, 0x4210)
	o := object.FromElements([]core.Element{creator(0x29, 0x42, "SIEMENS CSA HEADER"), core.NewRawElement(tag, core.VROB, []byte("SYNTHETIC PRIVATE PAYLOAD"))}, dictionary.Chain{c, std.Dictionary})
	defer o.Close()
	if _, err := o.ResolvePrivateEntry(tag); err != nil {
		t.Fatal(err)
	}
	plan, err := deid.PlanBasicProfile(context.Background(), o, deid.DefaultBasicProfileOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if o.Has(tag) || o.Has(core.NewTag(0x29, 0x42)) {
		t.Fatal("known private definition retained by deid")
	}
}

func TestCuratedCatalogImmutableConcurrentSnapshots(t *testing.T) {
	c := catalog(t)
	want := c.Entries()[0]
	snapshot := c.Entries()
	snapshot[0].Definition.VR = core.VRUN
	snapshot[0].Origin.License = "changed"
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				got, ok := c.Lookup(want.Definition.Creator, want.Definition.Group, want.Definition.Offset)
				if !ok || got != want {
					t.Error("mutable catalog")
				}
			}
		}()
	}
	wg.Wait()
	if _, ok := c.ByTag(core.NewTag(0x19, 0x1002)); ok {
		t.Fatal("absolute lookup bypass")
	}
	if _, ok := c.ByKeyword(want.Definition.Keyword); ok {
		t.Fatal("global keyword lookup")
	}
}

func BenchmarkCuratedLookup(b *testing.B) {
	c := catalog(b)
	b.ReportAllocs()
	for range b.N {
		if _, ok := c.ByPrivate("GEMS_ACQU_01", 0x19, 2); !ok {
			b.Fatal("missing")
		}
	}
}
