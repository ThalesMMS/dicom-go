package dicomtest

import (
	"github.com/ThalesMMS/dicom-go/core"
	"math"
)

// InterchangeDataSet contains only deterministic synthetic non-PHI values.
func InterchangeDataSet() core.DataSet {
	e := func(group, tag uint16, vr core.VR, v core.Value) core.Element {
		return core.Element{Header: core.ElementHeader{Tag: core.NewTag(group, tag), VR: vr}, Value: v}
	}
	return core.DataSet{Elements: []core.Element{
		e(0x0008, 0x0005, core.VRCS, core.StringValue{"ISO_IR 192"}),
		e(0x0008, 0x0016, core.VRUI, core.StringValue{"1.2.840.10008.5.1.4.1.1.7"}),
		e(0x0008, 0x0018, core.VRUI, core.StringValue{"1.2.826.0.1.3680043.10.543.901.1"}),
		e(0x0010, 0x0010, core.VRPN, core.StringValue{"SYNTHETIC^MÜLLER=合成^例=ごうせい^れい"}),
		e(0x0010, 0x0020, core.VRLO, core.StringValue{"NO-PHI-901"}),
		e(0x0010, 0x0030, core.VRDA, core.StringValue{}),
		e(0x0028, 0x0103, core.VRUS, core.Uint16Value{1}),
		e(0x0028, 0x0106, core.VRSS, core.Int16Value{-32768}),
		e(0x7777, 0x0010, core.VRLO, core.StringValue{"SYNTHETIC_CREATOR"}),
		e(0x7777, 0x1010, core.VRUS, core.Uint16Value{0, 65535}),
		e(0x7777, 0x1011, core.VRSL, core.Int32Value{math.MinInt32, math.MaxInt32}),
		e(0x7777, 0x1012, core.VRUL, core.Uint32Value{0, math.MaxUint32}),
		e(0x7777, 0x1013, core.VRSV, core.Int64Value{math.MinInt64, -9007199254740993, math.MaxInt64}),
		e(0x7777, 0x1014, core.VRUV, core.Uint64Value{9007199254740993, math.MaxUint64}),
		e(0x7777, 0x1015, core.VRFL, core.Float32Value{1.25, -2.5}),
		e(0x7777, 0x1016, core.VRFD, core.Float64Value{1.25, -2.5, math.SmallestNonzeroFloat64}),
		e(0x7777, 0x1017, core.VRAT, core.TagValue{core.NewTag(0x0010, 0x0010), core.NewTag(0x7777, 0x1014)}),
		e(0x7777, 0x1018, core.VROB, core.RawValue{0, 255, 1, 0}),
		e(0x7777, 0x1019, core.VRUN, core.RawValue{0, 1, 0, 2}),
		e(0x7777, 0x1020, core.VRLO, core.RawValue("RAW\\\\LAST ")),
		e(0x7777, 0x1023, core.VRPN, core.StringValue{"SYNTHETIC^ONE=合成^例", "", "SECOND^SYNTHETIC"}),
		e(0x7777, 0x1024, core.VRSS, core.Int16Value{-32768, 32767}),
		e(0x7777, 0x1025, core.VRDS, core.StringValue{"1.2500", "-0", "2.5e+1"}),
		e(0x7777, 0x1026, core.VRIS, core.StringValue{"000012", "-7"}),
		e(0x7777, 0x1021, core.VRSQ, core.SequenceValue{Items: []core.DataSet{
			{Elements: []core.Element{
				e(0x7777, 0x0010, core.VRLO, core.StringValue{"SYNTHETIC_CREATOR"}),
				e(0x7777, 0x1014, core.VRUV, core.Uint64Value{math.MaxUint64}),
			}},
			{},
			{Elements: []core.Element{
				e(0x7777, 0x0010, core.VRLO, core.StringValue{"SYNTHETIC_CREATOR"}),
				e(0x7777, 0x1022, core.VRSQ, core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
					e(0x0010, 0x0020, core.VRLO, core.StringValue{"NESTED"}),
				}}}}),
			}},
		}}),
	}}
}
