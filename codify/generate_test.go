package codify

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"go/format"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

func fixture() *object.Object {
	var elements []core.Element
	add := func(vr core.VR, value core.Value) {
		elements = append(elements, core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, uint16(len(elements)+0x1000)), VR: vr}, Value: value})
	}
	add(core.VRLO, core.StringValue{"CANARY^é\\\"\n\t\x00😀", "", "multiple"})
	add(core.VRUS, core.Uint16Value{0, 65535})
	add(core.VRSS, core.Int16Value{-32768, 32767})
	add(core.VRUL, core.Uint32Value{0, math.MaxUint32})
	add(core.VRSL, core.Int32Value{math.MinInt32, math.MaxInt32})
	add(core.VRUV, core.Uint64Value{0, 1<<53 + 1, math.MaxUint64})
	add(core.VRSV, core.Int64Value{math.MinInt64, math.MaxInt64})
	add(core.VRFL, core.Float32Value{math.Float32frombits(0x80000000), math.SmallestNonzeroFloat32, math.MaxFloat32})
	add(core.VRFD, core.Float64Value{math.Float64frombits(0x8000000000000000), math.SmallestNonzeroFloat64, math.MaxFloat64})
	add(core.VRAT, core.TagValue{core.NewTag(0xffff, 0xffff), core.NewTag(8, 0x18)})
	add(core.VRDS, core.StringValue{"+1.2300E+02", "-0"})
	add(core.VRIS, core.StringValue{"+000123"})
	add(core.VRUN, core.RawValue{0, 255, 128, 17})
	add(core.VRLO, core.RawValue("RAW_CANARY\x00"))
	add(core.VRUL, core.RawValue{0xff, 0xff, 0xff, 0xff})
	add(core.VRLO, core.StringValue{})
	add(core.VRSQ, core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("NESTED_CANARY"))}}, {}}})
	elements = append(elements, core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1100), VR: core.VRLO, LengthSet: true}, Value: nil})
	return object.FromElements(elements, std.Dictionary)
}

func runGenerated(t *testing.T, source []byte, harness string) []byte {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	mod := "module github.com/ThalesMMS/dicom-go/codify/generatedfixture\n\ngo 1.22\n\nrequire github.com/ThalesMMS/dicom-go v0.0.0\nreplace github.com/ThalesMMS/dicom-go => " + strconv.Quote(filepath.ToSlash(root)) + "\n"
	for name, data := range map[string][]byte{"go.mod": []byte(mod), "generated.go": source, "harness_test.go": []byte(harness)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-v", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated source did not compile/reconstruct: %v\n%s", err, out)
	}
	return out
}

func TestFaithfulGeneratedObjectCompilesAndMatchesSemanticOracle(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		obj := fixture()
		obj.SetValueByteOrder(order)
		result, err := Generate(context.Background(), obj, Options{Mode: Faithful, InlineBinary: true, Package: "repro"})
		if err != nil {
			t.Fatal(err)
		}
		again, err := Generate(context.Background(), obj, Options{Mode: Faithful, InlineBinary: true, Package: "repro"})
		if err != nil || !bytes.Equal(result.Source, again.Source) {
			t.Fatal("nondeterministic output")
		}
		formatted, err := format.Source(result.Source)
		if err != nil || !bytes.Equal(formatted, result.Source) {
			t.Fatal("not gofmt")
		}
		want, err := dicomtest.SemanticDataSet(obj.ToDataSet(), order)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		harness := `package repro
import("encoding/json";"testing";"bytes";"github.com/ThalesMMS/dicom-go/internal/dicomtest")
func TestReconstruction(t *testing.T) { ds:=BuildObject(); got,err:=dicomtest.SemanticDataSet(ds.ToDataSet(),ds.ValueByteOrder()); if err!=nil { t.Fatal(err) }; data,err:=json.Marshal(got); if err!=nil { t.Fatal(err) }; if !bytes.Equal(data,[]byte(` + strconv.Quote(string(encoded)) + `)) { t.Fatal("semantic reconstruction differs") } }
`
		runGenerated(t, result.Source, harness)
	}
}

func TestFaithfulFloatBitsFragmentsAndBulkURI(t *testing.T) {
	obj := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1001), VR: core.VRFL}, Value: core.Float32Value{math.Float32frombits(0x7fa12345), math.Float32frombits(0xff800000)}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1002), VR: core.VRFD}, Value: core.Float64Value{math.Float64frombits(0x7ff123456789abcd)}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x7fe0, 0x0010), VR: core.VROB}, Value: core.FragmentSequence{OffsetTable: []byte{0, 0, 0, 0}, Fragments: [][]byte{{0xff, 0xd8, 0, 0xff}, {1, 2, 3, 4}}}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1003), VR: core.VRUR}, Value: core.BulkDataValue{URI: "https://never-fetch.invalid/CANARY"}},
	}, std.Dictionary)
	result, err := Generate(context.Background(), obj, Options{Mode: Faithful, InlineBinary: true})
	if err != nil {
		t.Fatal(err)
	}
	runGenerated(t, result.Source, `package main
import("testing";"math";"reflect";"github.com/ThalesMMS/dicom-go/core")
func TestReconstruction(t *testing.T) { ds:=BuildObject(); a,_:=ds.Get(core.NewTag(0x7777,0x1001)); f:=a.Value.(core.Float32Value); if math.Float32bits(f[0])!=0x7fa12345 || math.Float32bits(f[1])!=0xff800000 { t.Fatal("float32 bits") }; b,_:=ds.Get(core.NewTag(0x7777,0x1002)); if math.Float64bits(b.Value.(core.Float64Value)[0])!=0x7ff123456789abcd { t.Fatal("float64 bits") }; p,_:=ds.Get(core.NewTag(0x7fe0,0x0010)); want:=core.FragmentSequence{OffsetTable:[]byte{0,0,0,0},Fragments:[][]byte{{0xff,0xd8,0,0xff},{1,2,3,4}}}; if !reflect.DeepEqual(p.Value,want) { t.Fatal("fragments") }; u,_:=ds.Get(core.NewTag(0x7777,0x1003)); if u.Value.(core.BulkDataValue).URI!="https://never-fetch.invalid/CANARY" { t.Fatal("bulk URI") } }
`)
}

func TestStructuralCanariesNeverAppearAndOutputCompiles(t *testing.T) {
	obj := fixture()
	before := obj.Elements()
	obj.Put(core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("USUAL_CANARY")))
	obj.Put(core.NewRawElement(core.NewTag(0x7777, 0x0010), core.VRLO, []byte("PRIVATE_CREATOR_CANARY")))
	obj.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1200), VR: core.VRUR}, Value: core.BulkDataValue{URI: "https://URI_CANARY.invalid"}})
	result, err := Generate(context.Background(), obj, Options{})
	if err != nil {
		t.Fatal(err)
	}
	report, _ := json.Marshal(result.Report)
	if bytes.Contains(result.Source, []byte("CANARY")) || bytes.Contains(report, []byte("CANARY")) {
		t.Fatal("structural output disclosed a value")
	}
	if len(result.Report.Changes) != result.Report.Elements {
		t.Fatal("substitutions not reported")
	}
	if !reflect.DeepEqual(before[0], obj.Elements()[0]) {
		t.Fatal("source mutated")
	}
	runGenerated(t, result.Source, `package main
import("testing";"github.com/ThalesMMS/dicom-go/core")
func TestReconstruction(t *testing.T){ ds:=BuildObject(); e,_:=ds.Get(core.NewTag(0x7777,0x1005)); for _,v:=range e.Value.(core.Uint64Value){if v!=0 {t.Fatal("numeric placeholder")}}; if !ds.HasDiscardedValues(){t.Fatal("missing explicit binary placeholder")} }
`)
}

func TestGeneratorRejectsLimitsNamesCancellationAndUnavailableValues(t *testing.T) {
	ctx := context.Background()
	for _, o := range []Options{{Package: "x;CANARY"}, {Package: "for"}, {Function: "_"}, {Function: "main"}, {Function: "object"}, {Function: "byte"}, {Function: "len"}, {Function: "true"}, {Function: "a-b"}, {InlineBinary: true}, {Mode: "unknown"}, {Limits: Limits{MaxDepth: -1}}, {Limits: Limits{MaxDepth: 65}}} {
		if _, err := Generate(ctx, fixture(), o); !errors.Is(err, ErrOptions) || strings.Contains(err.Error(), "CANARY") {
			t.Fatalf("options: %v", err)
		}
	}
	for _, o := range []Options{{Limits: Limits{MaxElements: 1}}, {Limits: Limits{MaxValues: 1}}, {Limits: Limits{MaxOutputBytes: 100}}, {Mode: Faithful, InlineBinary: true, Limits: Limits{MaxBinaryBytes: 1}}, {Mode: Faithful, Limits: Limits{MaxValueBytes: 1}}} {
		if result, err := Generate(ctx, fixture(), o); !errors.Is(err, ErrLimit) || len(result.Source) != 0 {
			t.Fatalf("limit: %v", err)
		}
	}
	deep := object.New(std.Dictionary)
	for i := 0; i < 5; i++ {
		deep = object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.NewTag(8, 0x1115), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{deep.ToDataSet()}}}}, std.Dictionary)
	}
	if _, err := Generate(ctx, deep, Options{Limits: Limits{MaxDepth: 2}}); !errors.Is(err, ErrLimit) {
		t.Fatalf("depth: %v", err)
	}
	items := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.NewTag(8, 0x1115), VR: core.VRSQ}, Value: core.SequenceValue{Items: make([]core.DataSet, 100)}}}, std.Dictionary)
	if _, err := Generate(ctx, items, Options{Limits: Limits{MaxItems: 10}}); !errors.Is(err, ErrLimit) {
		t.Fatalf("empty items: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Generate(canceled, fixture(), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	missing := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.NewTag(8, 0x1030), VR: core.VRLO, Length: 10, LengthSet: true}}}, std.Dictionary)
	if _, err := Generate(ctx, missing, Options{Mode: Faithful}); !errors.Is(err, ErrValue) {
		t.Fatalf("unavailable: %v", err)
	}
	large := object.FromElements([]core.Element{core.NewRawElement(core.NewTag(0x7fe0, 0x0010), core.VROB, make([]byte, 2<<20))}, std.Dictionary)
	if _, err := Generate(ctx, large, Options{Mode: Faithful, InlineBinary: true}); !errors.Is(err, ErrLimit) {
		t.Fatalf("large binary: %v", err)
	}
	result, err := Generate(ctx, large, Options{})
	if err != nil || len(result.Source) > 4096 {
		t.Fatalf("large placeholder: %v", err)
	}
}
