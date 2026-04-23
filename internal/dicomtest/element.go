package dicomtest

import "github.com/ThalesMMS/dicom-go/core"

func NewStringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue{value},
	}
}

func NewUIElement(tag core.Tag, value string) core.Element {
	return NewStringElement(tag, core.VRUI, value)
}

func NewPNElement(tag core.Tag, value string) core.Element {
	return NewStringElement(tag, core.VRPN, value)
}

func NewDAElement(tag core.Tag, value string) core.Element {
	return NewStringElement(tag, core.VRDA, value)
}

func NewUShortElement(tag core.Tag, value uint16) core.Element {
	return Uint16Element(tag, core.VRUS, nil, value)
}

func NewOBElement(tag core.Tag, data []byte) core.Element {
	return core.NewRawElement(tag, core.VROB, data)
}

func NewSequenceElement(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{
			Tag:       tag,
			VR:        core.VRSQ,
			Length:    core.UndefinedLength,
			LengthSet: true,
		},
		Value: core.SequenceValue{Items: append([]core.DataSet(nil), items...)},
	}
}

func NewFragmentSequenceElement(tag core.Tag, offsetTable []byte, fragments ...[]byte) core.Element {
	clonedFragments := make([][]byte, len(fragments))
	for i := range fragments {
		clonedFragments[i] = core.CloneBytes(fragments[i])
	}
	return core.Element{
		Header: core.ElementHeader{
			Tag:       tag,
			VR:        core.VROB,
			Length:    core.UndefinedLength,
			LengthSet: true,
		},
		Value: core.FragmentSequence{
			OffsetTable: core.CloneBytes(offsetTable),
			Fragments:   clonedFragments,
		},
	}
}
