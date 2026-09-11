package dicomtest

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestSemanticComparisonPreservesIntegerPrecisionAndItemPaths(t *testing.T) {
	want, err := SemanticDataSet(InterchangeDataSet(), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func([]SemanticElement)
		path   string
	}{
		{"uint64 precision", func(v []SemanticElement) {
			for i := range v {
				if v[i].Tag == "77771014" {
					v[i].Values[0] = "9007199254740992"
				}
			}
		}, "77771014/value[0]"},
		{"opaque trailing zero", func(v []SemanticElement) {
			for i := range v {
				if v[i].Tag == "77771018" {
					v[i].Opaque = "00ff01"
				}
			}
		}, "77771018"},
		{"item order", func(v []SemanticElement) {
			for i := range v {
				if v[i].Tag == "77771021" {
					v[i].Items[0], v[i].Items[1] = v[i].Items[1], v[i].Items[0]
				}
			}
		}, "77771021/item[0]"},
		{"empty is present", func(v []SemanticElement) {
			for i := range v {
				if v[i].Tag == "00100030" {
					v[i].Tag = "00100031"
				}
			}
		}, "00100030"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SemanticDataSet(InterchangeDataSet(), binary.LittleEndian)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(got)
			diff := DiffSemantic(got, want)
			if !strings.Contains(diff, tc.path) {
				t.Fatalf("diff=%q, want path %s", diff, tc.path)
			}
		})
	}
}

func TestSemanticComparisonNormalizesOnlyNumericTextAndEncodingMetadata(t *testing.T) {
	tag := core.NewTag(0x7777, 0x1010)
	makeDS := func(v core.Value) core.DataSet {
		return core.DataSet{Elements: []core.Element{{Header: core.ElementHeader{Tag: tag, VR: core.VRDS}, Value: v}}}
	}
	a, _ := SemanticDataSet(makeDS(core.StringValue{"1.2500", "-0"}), binary.LittleEndian)
	b, _ := SemanticDataSet(makeDS(core.RawValue("1.25\\0 ")), binary.BigEndian)
	if diff := DiffSemantic(a, b); diff != "" {
		t.Fatal(diff)
	}
}
