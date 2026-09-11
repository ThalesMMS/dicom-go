package dicomtest

import (
	"bytes"
	"encoding/binary"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// PrivateUNNestedPixelRegression is entirely synthetic. The explicit outer UN
// contains implicit little-endian item data, including opaque OW Pixel Data.
// No vendor dictionary, patient file or upstream fixture is incorporated.
func PrivateUNNestedPixelRegression() []byte {
	item := EncodeElements(transfer.ImplicitVRLittleEndian,
		NewStringElement(core.NewTag(0x7777, 0x0010), core.VRLO, "DICOMGO_TEST"),
		core.NewRawElement(core.TagPixelData, core.VROW, []byte{1, 2, 3, 4}),
	)
	return bytes.Join([][]byte{
		ExplicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7777, 0x1010), core.VRUN, 0xffffffff),
		SequenceControlBytes(binary.LittleEndian, core.TagItem, uint32(len(item))),
		item,
		SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
	}, nil)
}

// UpstreamParserRegressionSeeds reproduces structural failure classes only.
func UpstreamParserRegressionSeeds() [][]byte {
	return [][]byte{
		ExplicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7777, 0x1010), core.VROB, 0xffffffff),
		ExplicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7777, 0x1010), core.VROW, 0xffffffff),
		ExplicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7777, 0x1010), core.VROB, 0xfffffffe),
		EncodeElements(transfer.ExplicitVRLittleEndian, NewUShortElement(core.NewTag(0x0028, 0x0010), 1)),
		EncodeElements(transfer.ExplicitVRLittleEndian, core.NewRawElement(core.NewTag(0x0008, 0x0005), core.VROB, []byte("ISO_IR 192"))),
		PrivateUNNestedPixelRegression(),
		[]byte("NOT A DICOM DATASET\n"),
	}
}
