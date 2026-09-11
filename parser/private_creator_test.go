package parser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func privateTestDictionary(t testing.TB) dictionary.DataDictionary {
	t.Helper()
	c, err := dictionary.NewPrivateCatalog([]dictionary.PrivateEntry{{Creator: "ACME", Group: 0x11, Offset: 1, VR: core.VRUS}, {Creator: "OTHER", Group: 0x11, Offset: 1, VR: core.VRLO}, {Creator: "ACME", Group: 0x11, Offset: 2, VR: core.VRSQ}})
	if err != nil {
		t.Fatal(err)
	}
	return dictionary.Chain{c, std.Dictionary}
}

func TestPrivateCreatorBudgetsReplayAndDiagnosticBounds(t *testing.T) {
	dict := privateTestDictionary(t)
	wire := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian,
		creatorElement(0x10, "ACME"), creatorElement(0x11, "OTHER"),
		core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUS, []byte{7, 0}),
		core.NewRawElement(core.NewTag(0x11, 0x1201), core.VRUN, []byte{8, 0}),
		core.NewRawElement(core.NewTag(0x11, 0x1301), core.VRUN, []byte{9, 0}))
	for _, opts := range []ReaderOptions{
		{Dictionary: dict, MaxPrivateCreators: -1},
		{Dictionary: dict, MaxPrivateDiagnostics: -1},
		{Dictionary: dict, MaxPrivateCreators: 1},
	} {
		r := NewReader(bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, opts)
		if _, err := r.ReadDataSet(); !errors.Is(err, dictionary.ErrPrivateResourceLimit) {
			t.Fatalf("budget accepted: %v", err)
		}
	}
	r := NewReader(bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: dict, MaxPrivateDiagnostics: 1, MaxElementBytes: 8192})
	if _, err := r.ReadDataSet(); err != nil {
		t.Fatal(err)
	}
	d, truncated := r.PrivateDiagnostics()
	if len(d) != 1 || !truncated {
		t.Fatalf("unbounded report: %+v %v", d, truncated)
	}
	d[0].Err = nil
	d, _ = r.PrivateDiagnostics()
	if d[0].Err == nil {
		t.Fatal("report aliases reader")
	}
	// No recorded skipped location: replay reparses creators from the start.
	var copied bytes.Buffer
	if _, err := r.CopyElementValueTo(core.NewTag(0x11, 0x1001), &copied); err != nil || !bytes.Equal(copied.Bytes(), []byte{7, 0}) {
		t.Fatalf("replay: %x %v", copied.Bytes(), err)
	}
	if creator, err := r.privateScope().Creator(core.NewTag(0x11, 0x1001)); err != nil || creator != "ACME" {
		t.Fatalf("replay duplicated reservation: %q %v", creator, err)
	}
	for {
		if _, err := r.Next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrivateCreatorHooksCannotInvalidateStreamingBindings(t *testing.T) {
	wire := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, creatorElement(0x10, "ACME"), core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUS, []byte{7, 0}))
	for _, action := range []string{"observe", "skip", "defer", "filter", "replace"} {
		t.Run(action, func(t *testing.T) {
			chain, err := validation.NewHookChain(validation.HookRegistration{
				Name: "creator", Points: []validation.HookPoint{validation.HookElementHeaderRead, validation.HookAfterElement},
				Hook: validation.HookFunc(func(_ context.Context, e validation.HookEvent) (validation.HookDecision, error) {
					if e.Header == nil || !dictionary.IsPrivateCreatorTag(e.Header.Tag) {
						return validation.HookDecision{}, nil
					}
					if e.Point == validation.HookElementHeaderRead {
						return validation.HookDecision{SkipValue: action == "skip", DeferValue: action == "defer"}, nil
					}
					if action == "replace" {
						replacement := creatorElement(0x10, "OTHER")
						return validation.HookDecision{Element: &replacement}, nil
					}
					return validation.HookDecision{Filter: action == "filter"}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewReaderWithValidation(context.Background(), bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: privateTestDictionary(t)}, validation.Options{Hooks: chain})
			if err != nil {
				t.Fatal(err)
			}
			ds, err := r.ReadDataSet()
			if action == "observe" {
				if err != nil || len(ds.Elements) != 2 || ds.Elements[1].VR() != core.VRUS {
					t.Fatalf("read-only hook changed parse: %v", err)
				}
			} else if !errors.Is(err, dictionary.ErrPrivateCreator) {
				t.Fatalf("creator %s allowed: %v", action, err)
			}
		})
	}
}

func TestPrivateCreatorWriterAlwaysUsesDefaultRepertoire(t *testing.T) {
	charset, err := dicomenc.ParseCharacterSet("ISO_IR 192")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	w := NewWriterWithOptions(&output, transfer.ExplicitVRLittleEndian, WriterOptions{CharacterSet: charset})
	if err := w.WriteElement(creatorElement(0x10, "caf\u00e9")); err == nil {
		t.Fatal("encoded Unicode private creator using dataset UTF-8")
	}
	output.Reset()
	if err := w.WriteElement(creatorElement(0x10, "ACME")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes()[8:], []byte("ACME")) {
		t.Fatalf("creator encoding: %x", output.Bytes())
	}
}

func TestPrivateCreatorInvalidSequenceVRAndSelectiveSkip(t *testing.T) {
	bad := core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x11, 0x10), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}}
	wire := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, bad)
	for _, strict := range []bool{false, true} {
		r := NewReader(bytes.NewReader(wire), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: privateTestDictionary(t), RejectInvalidPrivateCreators: strict})
		_, err := r.ReadDataSet()
		if strict && !errors.Is(err, dictionary.ErrPrivateCreator) || !strict && err != nil {
			t.Fatalf("invalid SQ creator strict=%v: %v", strict, err)
		}
		d, _ := r.PrivateDiagnostics()
		if len(d) != 1 || !errors.Is(d[0].Err, dictionary.ErrPrivateCreator) || d[0].ItemOffsetSet {
			t.Fatalf("invalid creator recorded in wrong scope: %+v", d)
		}
	}
	wire = dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, creatorElement(0x10, "ACME"), core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUS, []byte{7, 0}))
	for _, skip := range []bool{false, true} {
		r, err := NewSelectiveReader(context.Background(), bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: privateTestDictionary(t)}, SelectiveReaderOptions{Select: func(_ context.Context, _ validation.Path, header core.ElementHeader, _ int64) (SelectiveDisposition, error) {
			if skip && dictionary.IsPrivateCreatorTag(header.Tag) {
				return SelectiveSkip, nil
			}
			return SelectiveMaterialize, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		ds, err := r.ReadDataSet()
		if skip {
			if !errors.Is(err, dictionary.ErrPrivateCreator) {
				t.Fatalf("selective skip allowed: %v", err)
			}
		} else if err != nil || len(ds.Elements) != 2 || ds.Elements[1].VR() != core.VRUS {
			t.Fatalf("selective scope resolution: %v", err)
		}
	}
}

func FuzzPrivateCreatorScopes(f *testing.F) {
	dict := privateTestDictionary(f)
	f.Add(dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, creatorElement(0x10, "ACME"), core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUS, []byte{7, 0})))
	f.Add(dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, creatorElement(0x10, "A\\B")))
	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > 8192 {
			t.Skip()
		}
		r := NewReader(bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: dict, MaxPrivateCreators: 8, MaxPrivateDiagnostics: 4, MaxElements: 64, MaxSequenceDepth: 8, MaxElementBytes: 8192})
		_, _ = r.ReadDataSet()
		d, _ := r.PrivateDiagnostics()
		if len(d) > 4 {
			t.Fatal("unbounded private report")
		}
	})
}
func creatorElement(block uint16, id string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x11, block), VR: core.VRLO}, Value: core.StringValue{id}}
}
func TestPrivateCreatorParsingScopesAndExplicitVR(t *testing.T) {
	inner := func(id string, vr core.VR) core.DataSet {
		return core.DataSet{Elements: []core.Element{creatorElement(0x80, id), core.NewRawElement(core.NewTag(0x11, 0x8001), vr, []byte{65, 0})}}
	}
	elements := []core.Element{creatorElement(0x1f, "ACME"), core.NewRawElement(core.NewTag(0x11, 0x1f01), core.VRUS, []byte{7, 0}),
		{Header: core.ElementHeader{Tag: core.NewTag(0x11, 0x1f02), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{inner("OTHER", core.VRLO), inner("ACME", core.VRUS), {Elements: []core.Element{core.NewRawElement(core.NewTag(0x11, 0x1f01), core.VRUN, []byte{9, 0})}}}}}}
	for _, syntax := range []transfer.Syntax{transfer.ImplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRBigEndian} {
		wire := dicomtest.EncodeElements(syntax, elements...)
		r := NewReader(bytes.NewReader(wire), syntax, ReaderOptions{Dictionary: privateTestDictionary(t)})
		ds, err := r.ReadDataSet()
		if err != nil {
			t.Fatal(err)
		}
		if ds.Elements[0].VR() != core.VRLO || ds.Elements[1].VR() != core.VRUS {
			t.Fatal("root reservation")
		}
		seq := ds.Elements[2].Value.(core.SequenceValue)
		for i, want := range []core.VR{core.VRLO, core.VRUS, core.VRUN} {
			if seq.Items[i].Elements[len(seq.Items[i].Elements)-1].VR() != want {
				t.Fatalf("item %d inherited or confused reservation", i)
			}
		}
		if !syntax.ExplicitVR {
			d, truncated := r.PrivateDiagnostics()
			if len(d) != 1 || truncated || !d[0].ItemOffsetSet || !errors.Is(d[0].Err, dictionary.ErrPrivateCreatorMissing) {
				t.Fatalf("diagnostics: %+v", d)
			}
		}
	}
	// An explicit valid VR remains authoritative even against a catalog US entry.
	wire := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, creatorElement(0x10, "ACME"), core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRLO, []byte("TEXT")))
	r := NewReader(bytes.NewReader(wire), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: privateTestDictionary(t)})
	ds, err := r.ReadDataSet()
	if err != nil || ds.Elements[1].VR() != core.VRLO {
		t.Fatalf("explicit VR replaced: %v", err)
	}
}
func TestPrivateCreatorMalformedReservationsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		creators []core.Element
	}{
		{"missing", nil}, {"unknown", []core.Element{creatorElement(0x10, "UNKNOWN")}},
		{"invalid", []core.Element{creatorElement(0x10, "A\\B")}},
		{"duplicate slot", []core.Element{creatorElement(0x10, "ACME"), creatorElement(0x10, "OTHER")}},
		{"duplicate creator", []core.Element{creatorElement(0x10, "ACME"), creatorElement(0x11, "ACME")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			elements := append(tc.creators, core.NewRawElement(core.NewTag(0x11, 0x1001), core.VRUN, []byte{7, 0}))
			wire := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, elements...)
			r := NewReader(bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: privateTestDictionary(t), MaxPrivateDiagnostics: 1})
			ds, err := r.ReadDataSet()
			if err != nil {
				t.Fatal(err)
			}
			el := ds.Elements[len(ds.Elements)-1]
			if el.VR() != core.VRUN {
				t.Fatal("guessed private VR")
			}
			raw, _ := el.RawBytes()
			if !bytes.Equal(raw, []byte{7, 0}) {
				t.Fatal("raw changed")
			}
			d, _ := r.PrivateDiagnostics()
			if len(d) != 1 {
				t.Fatal("missing diagnostic")
			}
			if tc.name == "invalid" || tc.name == "duplicate slot" || tc.name == "duplicate creator" {
				r := NewReader(bytes.NewReader(wire), transfer.ImplicitVRLittleEndian, ReaderOptions{Dictionary: privateTestDictionary(t), RejectInvalidPrivateCreators: true})
				if _, err := r.ReadDataSet(); err == nil {
					t.Fatal("strict policy ignored")
				}
			}
		})
	}
}
