package dicomjson

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestMarshalTypedOtherValuesMatchesRawWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		vr    core.VR
		value core.Value
	}{
		{core.VROW, core.Uint16Value{1, 65535}},
		{core.VROL, core.Uint32Value{1, 4294967295}},
		{core.VROV, core.Uint64Value{9007199254740993, 18446744073709551615}},
		{core.VROF, core.Float32Value{1.25, -2.5}},
		{core.VROD, core.Float64Value{1.25, -2.5}},
	} {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			t.Run(tc.vr.String()+"/"+order.String(), func(t *testing.T) {
				tag := core.NewTag(0x7777, 0x1010)
				e := core.Element{Header: core.ElementHeader{Tag: tag, VR: tc.vr}, Value: tc.value}
				obj := object.FromElements([]core.Element{e}, std.Dictionary)
				var raw bytes.Buffer
				if err := binary.Write(&raw, order, tc.value); err != nil {
					t.Fatal(err)
				}
				expected := object.FromElements([]core.Element{core.NewRawElement(tag, tc.vr, raw.Bytes())}, std.Dictionary)
				got, err := Marshal(obj, Options{ByteOrder: order})
				if err != nil {
					t.Fatal(err)
				}
				want, err := Marshal(expected, Options{ByteOrder: order})
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("typed and raw %s differ", tc.vr)
				}
				if _, err := Marshal(obj, Options{ByteOrder: order, Limits: Limits{MaxInlineBinaryBytes: 1}}); !errors.Is(err, ErrMaxInlineBinaryBytesExceeded) {
					t.Fatalf("inline limit: %v", err)
				}
				var after bytes.Buffer
				if err := binary.Write(&after, order, tc.value); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after.Bytes(), raw.Bytes()) {
					t.Fatal("typed source was mutated")
				}
			})
		}
	}
}

func TestMarshalRejectsIncompatibleTypedValuesIncludingEmpty(t *testing.T) {
	for _, value := range []core.Value{core.Uint16Value{1}, core.Uint16Value{}, core.TagValue{core.NewTag(8, 16)}} {
		obj := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x1010), VR: core.VRSS}, Value: value}}, std.Dictionary)
		data, err := MarshalCompact(obj)
		if err == nil || len(data) != 0 {
			t.Fatalf("incompatible %T serialized", value)
		}
	}
}

func TestMarshalRejectsFragmentSequenceWithNumericVR(t *testing.T) {
	obj := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VRUS}, Value: core.FragmentSequence{Fragments: [][]byte{{1, 2}}}}}, std.Dictionary)
	if data, err := MarshalCompact(obj); err == nil || len(data) != 0 {
		t.Fatal("fragment item headers were reinterpreted as numeric samples")
	}
}
