package dicomtest

import (
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

// PrivateScopeDataSet is authored synthetic metadata with no patient attributes.
// Every item claims its own blocks. Creator strings are test identities only.
func PrivateScopeDataSet() core.DataSet {
	e := func(group, tag uint16, vr core.VR, v core.Value) core.Element {
		return core.Element{Header: core.ElementHeader{Tag: core.NewTag(group, tag), VR: vr}, Value: v}
	}
	return core.DataSet{Elements: []core.Element{
		e(8, 0x16, core.VRUI, core.StringValue{"1.2.840.10008.5.1.4.1.1.7"}),
		e(8, 0x18, core.VRUI, core.StringValue{"1.2.826.0.1.3680043.10.543.910.1"}),
		e(0x11, 0x1f, core.VRLO, core.StringValue{"ACME_910"}),
		e(0x11, 0x1f01, core.VRUS, core.Uint16Value{7, 65535}),
		e(0x11, 0x1f02, core.VRSQ, core.SequenceValue{Items: []core.DataSet{
			{Elements: []core.Element{e(0x11, 0x80, core.VRLO, core.StringValue{"OTHER_910"}), e(0x11, 0x8001, core.VRLO, core.StringValue{"SYNTHETIC"})}},
			{Elements: []core.Element{e(0x11, 0x80, core.VRLO, core.StringValue{"ACME_910"}), e(0x11, 0x8001, core.VRUS, core.Uint16Value{99, 256})}},
			{},
		}}),
		e(0x13, 0x42, core.VRLO, core.StringValue{"ACME_910"}),
		e(0x13, 0x4201, core.VRFD, core.Float64Value{1.25, -2.5}),
	}}
}

func PrivateScopeDefinitions() []dictionary.PrivateEntry {
	return []dictionary.PrivateEntry{
		{Creator: "ACME_910", Group: 0x11, Offset: 1, VR: core.VRUS, Keyword: "SyntheticUnsignedValues", Name: "Synthetic unsigned values", VM: "1-n"},
		{Creator: "ACME_910", Group: 0x11, Offset: 2, VR: core.VRSQ, Keyword: "SyntheticPrivateSequence", Name: "Synthetic private sequence", VM: "1"},
		{Creator: "OTHER_910", Group: 0x11, Offset: 1, VR: core.VRLO, Keyword: "SyntheticLabel", Name: "Synthetic label", VM: "1"},
		{Creator: "ACME_910", Group: 0x13, Offset: 1, VR: core.VRFD, Keyword: "SyntheticDoubleValues", Name: "Synthetic double values", VM: "1-n"},
	}
}
