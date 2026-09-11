package object_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dicomxml"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func privateCatalog(t testing.TB) dictionary.DataDictionary {
	t.Helper()
	c, err := dictionary.NewPrivateCatalog(dicomtest.PrivateScopeDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	return dictionary.Chain{c, std.Dictionary}
}

func TestPrivateCreatorDuplicateSlotsPersistUntilExplicitRepair(t *testing.T) {
	creator := func(block uint16, id string) core.Element {
		return core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x11, block), VR: core.VRLO}, Value: core.StringValue{id}}
	}
	tag := core.NewTag(0x11, 0x1f01)
	// Construct without a private catalog, then inspect with a catalog supplied
	// later. Object's last-wins map must not turn this conflict into a match.
	o := object.FromElements([]core.Element{creator(0x1f, "OTHER_910"), creator(0x1f, "ACME_910"), core.NewRawElement(tag, core.VRUN, []byte{7, 0})}, std.Dictionary)
	for _, mutate := range []bool{false, true} {
		if mutate {
			o.Put(creator(0x20, "UNRELATED"))
		}
		for _, row := range o.SummarizeElements(object.SummaryOptions{Dictionary: privateCatalog(t)}) {
			if row.Tag == tag && row.Keyword != "" {
				t.Fatal("ambiguous reservation became a catalog match")
			}
		}
		if err := o.ValidatePrivateReservations(); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
			t.Fatalf("lost original conflict: %v", err)
		}
	}
	o.Put(creator(0x1f, "ACME_910"))
	if err := o.ValidatePrivateReservations(); err != nil {
		t.Fatalf("explicit repair did not clear conflict: %v", err)
	}
	for _, row := range o.SummarizeElements(object.SummaryOptions{Dictionary: privateCatalog(t)}) {
		if row.Tag == tag && row.Keyword != "SyntheticUnsignedValues" {
			t.Fatal("repaired reservation not resolved")
		}
	}
}

func TestPrivateCreatorAuthorshipExhaustionIsAtomic(t *testing.T) {
	o := object.New(privateCatalog(t))
	for block := 0x10; block <= 0xff; block++ {
		o.Put(core.NewRawElement(core.NewTag(0x11, uint16(block<<8)), core.VRUN, []byte{1, 0}))
	}
	before, err := dicomtest.SemanticDataSet(o.ToDataSet(), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReservePrivateCreator(0x11, "ACME_910"); !errors.Is(err, dictionary.ErrPrivateBlocksExhausted) {
		t.Fatalf("occupied block reused: %v", err)
	}
	after, err := dicomtest.SemanticDataSet(o.ToDataSet(), binary.LittleEndian)
	if err != nil || dicomtest.DiffSemantic(after, before) != "" {
		t.Fatalf("failed reservation changed object: %v", err)
	}
}

func TestPrivateCreatorTextDoesNotInheritDatasetCharset(t *testing.T) {
	creatorTag := core.NewTag(0x11, 0x10)
	for _, charset := range []string{"UNSUPPORTED", "ISO_IR 192"} {
		o := object.FromElements([]core.Element{
			core.NewRawElement(core.NewTag(8, 5), core.VRCS, []byte(charset)),
			core.NewRawElement(creatorTag, core.VRLO, []byte("ACME")),
		}, std.Dictionary)
		if value, err := o.LookupString(creatorTag); err != nil || value != "ACME" {
			t.Fatalf("creator inherited charset %s: %q %v", charset, value, err)
		}
		o.Put(core.NewRawElement(creatorTag, core.VRLO, []byte("caf\u00e9")))
		if _, err := o.LookupString(creatorTag); err == nil {
			t.Fatal("private creator accepted extended repertoire")
		}
	}
}

func TestPrivateCreatorDiagnosticsThroughValidationRead(t *testing.T) {
	wire := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUN, []byte{7, 0}))
	o, _, err := object.ReadDataSetWithValidation(context.Background(), bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, object.ReadFileOptions{Dictionary: privateCatalog(t)}, validation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	d, truncated := o.ParsedPrivateDiagnostics()
	if len(d) != 1 || truncated || !errors.Is(d[0].Err, dictionary.ErrPrivateCreatorMissing) {
		t.Fatalf("validation read lost private diagnostic: %+v %v", d, truncated)
	}
}
func TestPrivateCreatorObjectInterchangeAndScopedInspection(t *testing.T) {
	dict := privateCatalog(t)
	ds := dicomtest.PrivateScopeDataSet()
	want, err := dicomtest.SemanticDataSet(ds, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	for _, syntax := range []transfer.Syntax{transfer.ImplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRBigEndian} {
		t.Run(syntax.UID, func(t *testing.T) {
			source := object.FromDataSet(ds, dict)
			var wire bytes.Buffer
			if err := object.WriteFile(&wire, &object.File{Dataset: source, TransferSyntax: syntax}); err != nil {
				t.Fatal(err)
			}
			file, err := object.ReadFileWithOptions(bytes.NewReader(wire.Bytes()), object.ReadFileOptions{Dictionary: dict, RejectInvalidPrivateCreators: true})
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for _, format := range []string{"read-file", "json", "xml"} {
				t.Run(format, func(t *testing.T) {
					obj := file.Dataset
					switch format {
					case "json":
						b, err := dicomjson.Marshal(obj, dicomjson.DefaultOptions())
						if err != nil {
							t.Fatal(err)
						}
						obj, err = dicomjson.Unmarshal(b, dict)
						if err != nil {
							t.Fatal(err)
						}
					case "xml":
						b, err := dicomxml.Marshal(obj, dicomxml.Options{})
						if err != nil {
							t.Fatal(err)
						}
						obj, err = dicomxml.Unmarshal(b, dict)
						if err != nil {
							t.Fatal(err)
						}
					}
					got, err := dicomtest.SemanticDataSet(obj.ToDataSet(), obj.ValueByteOrder())
					if err != nil {
						t.Fatal(err)
					}
					if diff := dicomtest.DiffSemantic(got, want); diff != "" {
						t.Fatal(diff)
					}
					e, err := obj.ResolvePrivateEntry(core.NewTag(0x11, 0x1f01))
					if err != nil || e.Keyword != "SyntheticUnsignedValues" {
						t.Fatalf("root inspection: %+v %v", e, err)
					}
					items, ok := obj.GetSequence(core.NewTag(0x11, 0x1f02))
					if !ok || len(items) != 3 {
						t.Fatal("private sequence")
					}
					for i, wantVR := range []core.VR{core.VRLO, core.VRUS} {
						e, err := items[i].ResolvePrivateEntry(core.NewTag(0x11, 0x8001))
						if err != nil || e.VR != wantVR {
							t.Fatalf("item %d: %+v %v", i, e, err)
						}
					}
					if _, err := items[2].PrivateTag(0x11, "ACME_910", 1); !errors.Is(err, dictionary.ErrPrivateCreatorMissing) {
						t.Fatal("empty item inherited creator")
					}
					rows := obj.SummarizeElements(object.SummaryOptions{})
					found := false
					for _, row := range rows {
						if row.Tag == core.NewTag(0x11, 0x1f01) {
							found = row.Keyword == "SyntheticUnsignedValues" && row.VR == core.VRUS
						}
					}
					if !found {
						t.Fatal("creator-aware summary missing")
					}
				})
			}
		})
	}
	// Immutable catalog reuse creates separate parser and object reservation state.
	var wire bytes.Buffer
	if err := object.WriteFile(&wire, &object.File{Dataset: object.FromDataSet(ds, dict), TransferSyntax: transfer.ImplicitVRLittleEndian}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				f, err := object.ReadFileWithOptions(bytes.NewReader(wire.Bytes()), object.ReadFileOptions{Dictionary: dict})
				if err != nil {
					t.Error(err)
					return
				}
				e, err := f.Dataset.ResolvePrivateEntry(core.NewTag(0x11, 0x1f01))
				if err != nil || e.VR != core.VRUS {
					t.Error("concurrent scope leaked")
				}
				f.Close()
			}
		}()
	}
	wg.Wait()
}

func TestPrivateCreatorAuthorshipNeverCollidesOrRemaps(t *testing.T) {
	obj := object.New(privateCatalog(t))
	orphan := core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUN, []byte{9, 0})
	obj.Put(orphan)
	block, err := obj.ReservePrivateCreator(0x11, "ACME_910")
	if err != nil || block != 0x11 {
		t.Fatalf("orphan collision: %x %v", block, err)
	}
	if same, err := obj.ReservePrivateCreator(0x11, " ACME_910 "); err != nil || same != block {
		t.Fatal("duplicate reservation")
	}
	tag, err := obj.PrivateTag(0x11, "ACME_910", 1)
	if err != nil || tag != core.NewTag(0x11, 0x1101) {
		t.Fatal("logical tag")
	}
	obj.Put(core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRUS}, Value: core.Uint16Value{100}})
	if other, err := obj.ReservePrivateCreator(0x11, "OTHER_910"); err != nil || other != 0x12 {
		t.Fatal("second creator")
	}
	if err := obj.ValidatePrivateReservations(); err != nil {
		t.Fatal(err)
	}
	got, _ := obj.GetRaw(orphan.Tag())
	if !bytes.Equal(got, []byte{9, 0}) {
		t.Fatal("orphan moved")
	}
	obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x11, 0x11), VR: core.VRLO}, Value: core.StringValue{"OTHER_910"}})
	if _, err := obj.ResolvePrivateEntry(tag); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal("stale cached reservation")
	}
	if _, err := obj.ReservePrivateCreator(0x11, "NEW"); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal("authored into ambiguous group")
	}
	obj.Remove(core.NewTag(0x11, 0x12))
	if e, err := obj.ResolvePrivateEntry(tag); err != nil || e.VR != core.VRLO {
		t.Fatal("reservation not refreshed after explicit mutation")
	}
}

func TestPrivateCreatorDuplicateSlotSurvivesObjectLastWins(t *testing.T) {
	ds := dicomtest.PrivateScopeDataSet()
	ds.Elements = append([]core.Element{{Header: core.ElementHeader{Tag: core.NewTag(0x11, 0x1f), VR: core.VRLO}, Value: core.StringValue{"OTHER_910"}}}, ds.Elements...)
	obj := object.FromDataSet(ds, privateCatalog(t))
	if _, err := obj.ResolvePrivateEntry(core.NewTag(0x11, 0x1f01)); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal("duplicate slot silently resolved")
	}
	if err := obj.ValidatePrivateReservations(); !errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
		t.Fatal("duplicate conflict not reported")
	}
	items, _ := obj.GetSequence(core.NewTag(0x11, 0x1f02))
	again, _ := obj.GetSequence(core.NewTag(0x11, 0x1f02))
	items[0].Remove(core.NewTag(0x11, 0x80))
	if _, err := items[0].ResolvePrivateEntry(core.NewTag(0x11, 0x8001)); !errors.Is(err, dictionary.ErrPrivateCreatorMissing) {
		t.Fatal("removed child creator")
	}
	if e, err := again[0].ResolvePrivateEntry(core.NewTag(0x11, 0x8001)); err != nil || e.VR != core.VRLO {
		t.Fatal("cached sibling facade mutated")
	}
}
