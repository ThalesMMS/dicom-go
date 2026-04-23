package dicomtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	ImplementationClassUID    = "1.2.826.0.1.3680043.10.543.1"
	ImplementationVersionName = "DICOMGO_M0"
)

var (
	tagFileMetaInformationGroupLength = core.NewTag(0x0002, 0x0000)
	tagFileMetaInformationVersion     = core.NewTag(0x0002, 0x0001)
	tagMediaStorageSOPClassUID        = core.NewTag(0x0002, 0x0002)
	tagMediaStorageSOPInstanceUID     = core.NewTag(0x0002, 0x0003)
	tagTransferSyntaxUID              = core.NewTag(0x0002, 0x0010)
	tagImplementationClassUID         = core.NewTag(0x0002, 0x0012)
	tagImplementationVersionName      = core.NewTag(0x0002, 0x0013)

	tagSOPClassUID               = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID            = core.NewTag(0x0008, 0x0018)
	tagModality                  = core.NewTag(0x0008, 0x0060)
	tagPatientName               = core.NewTag(0x0010, 0x0010)
	tagPatientID                 = core.NewTag(0x0010, 0x0020)
	tagStudyInstanceUID          = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID         = core.NewTag(0x0020, 0x000E)
	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
	tagPixelData                 = core.NewTag(0x7FE0, 0x0010)
)

func EncodeElement(elem core.Element, syntax transfer.Syntax) []byte {
	order := byteOrderFor(syntax)
	value, headerLength := encodeElementValue(elem, syntax)

	var buf bytes.Buffer
	buf.Write(encodeUint16(elem.Tag().Group, order))
	buf.Write(encodeUint16(elem.Tag().Element, order))

	if syntax.ExplicitVR {
		buf.WriteString(elem.VR().String())
		if elem.VR().UsesLongExplicitLength() {
			buf.Write([]byte{0x00, 0x00})
			buf.Write(encodeUint32(headerLength, order))
		} else {
			if headerLength > 0xFFFF {
				panic(fmt.Sprintf("dicomtest: explicit VR %s payload too large for 16-bit length: %d", elem.VR(), headerLength))
			}
			buf.Write(encodeUint16(uint16(headerLength), order))
		}
	} else {
		buf.Write(encodeUint32(headerLength, order))
	}

	buf.Write(value)
	return buf.Bytes()
}

func EncodeElements(syntax transfer.Syntax, elems ...core.Element) []byte {
	var buf bytes.Buffer
	for _, elem := range elems {
		buf.Write(EncodeElement(elem, syntax))
	}
	return buf.Bytes()
}

func SequenceControlBytes(order binary.ByteOrder, tag core.Tag, length uint32) []byte {
	return implicitHeaderBytes(order, tag, length)
}

func ExplicitLongHeaderBytes(order binary.ByteOrder, tag core.Tag, vr core.VR, length uint32) []byte {
	var buf bytes.Buffer
	writeTag(&buf, normalizeByteOrder(order), tag)
	buf.WriteString(vr.String())
	buf.Write([]byte{0x00, 0x00})
	buf.Write(encodeUint32(length, order))
	return buf.Bytes()
}

func SequenceHeaderBytes(syntax transfer.Syntax, tag core.Tag, length uint32) []byte {
	if syntax.ExplicitVR {
		return ExplicitLongHeaderBytes(syntax.ByteOrder, tag, core.VRSQ, length)
	}
	return implicitHeaderBytes(syntax.ByteOrder, tag, length)
}

func ExplicitElement(elem core.Element) []byte {
	return EncodeElement(elem, transfer.ExplicitVRLittleEndian)
}

func ImplicitElement(elem core.Element) []byte {
	return EncodeElement(elem, transfer.ImplicitVRLittleEndian)
}

func BigEndianElement(elem core.Element) []byte {
	return EncodeElement(elem, transfer.ExplicitVRBigEndian)
}

func StringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return NewStringElement(tag, vr, value)
}

func StringsElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue(values),
	}
}

func BytesElement(tag core.Tag, vr core.VR, value []byte) core.Element {
	if vr == core.VROB {
		return NewOBElement(tag, value)
	}
	return core.NewRawElement(tag, vr, value)
}

func Uint16Element(tag core.Tag, vr core.VR, order binary.ByteOrder, values ...uint16) core.Element {
	order = normalizeByteOrder(order)
	var data []byte
	for _, value := range values {
		data = append(data, encodeUint16(value, order)...)
	}
	return BytesElement(tag, vr, data)
}

func Uint32Element(tag core.Tag, vr core.VR, order binary.ByteOrder, values ...uint32) core.Element {
	order = normalizeByteOrder(order)
	var data []byte
	for _, value := range values {
		data = append(data, encodeUint32(value, order)...)
	}
	return BytesElement(tag, vr, data)
}

func MinimalDataSet() []core.Element {
	return MinimalDataset()
}

func MinimalPart10File(syntax transfer.Syntax) ([]byte, error) {
	return Part10File(syntax, MinimalDataset()...)
}

func Part10File(syntax transfer.Syntax, dataset ...core.Element) ([]byte, error) {
	builder := NewFileMetaBuilder().WithTransferSyntax(syntax.UID)
	if sopClassUID := findElementString(dataset, tagSOPClassUID); sopClassUID != "" {
		builder.WithSOPClass(sopClassUID)
	}
	if sopInstanceUID := findElementString(dataset, tagSOPInstanceUID); sopInstanceUID != "" {
		builder.WithSOPInstance(sopInstanceUID)
	}
	return NewFileBuilder().WithMeta(builder).AddElements(dataset...).Build()
}

func findElementString(elements []core.Element, tag core.Tag) string {
	for _, elem := range elements {
		if elem.Tag() == tag {
			return elem.StringValue()
		}
	}
	return ""
}

func encodeElementValue(elem core.Element, syntax transfer.Syntax) ([]byte, uint32) {
	if elem.Value == nil {
		return nil, 0
	}

	switch value := elem.Value.(type) {
	case core.RawValue:
		encoded := padToEven(value.Bytes(), padByteForVR(elem.VR()))
		return encoded, uint32(len(encoded))
	case core.StringValue:
		encoded := encodeStringsForVR(elem.VR(), []string(value))
		return encoded, uint32(len(encoded))
	case core.SequenceValue:
		encoded := encodeSequenceValue(value, syntax)
		// Test fixtures always encode in-memory sequences as undefined-length SQ.
		return encoded, uint32(core.UndefinedLength)
	case core.FragmentSequence:
		encoded := encodeFragmentSequenceValue(value, syntax)
		return encoded, uint32(core.UndefinedLength)
	default:
		panic(fmt.Sprintf("dicomtest: unsupported value kind %T", elem.Value))
	}
}

func encodeSequenceValue(value core.SequenceValue, syntax transfer.Syntax) []byte {
	var buf bytes.Buffer
	for _, item := range value.Items {
		itemValue := EncodeElements(syntax, item.Elements...)
		buf.Write(encodeUint16(core.TagItem.Group, syntax.ByteOrder))
		buf.Write(encodeUint16(core.TagItem.Element, syntax.ByteOrder))
		buf.Write(encodeUint32(uint32(len(itemValue)), syntax.ByteOrder))
		buf.Write(itemValue)
	}
	buf.Write(encodeUint16(core.TagSequenceDelimitationItem.Group, syntax.ByteOrder))
	buf.Write(encodeUint16(core.TagSequenceDelimitationItem.Element, syntax.ByteOrder))
	buf.Write(encodeUint32(0, syntax.ByteOrder))
	return buf.Bytes()
}

func encodeFragmentSequenceValue(value core.FragmentSequence, syntax transfer.Syntax) []byte {
	var buf bytes.Buffer
	writeFragmentItem(&buf, syntax, value.OffsetTable)
	for _, fragment := range value.Fragments {
		writeFragmentItem(&buf, syntax, fragment)
	}
	buf.Write(encodeUint16(core.TagSequenceDelimitationItem.Group, syntax.ByteOrder))
	buf.Write(encodeUint16(core.TagSequenceDelimitationItem.Element, syntax.ByteOrder))
	buf.Write(encodeUint32(0, syntax.ByteOrder))
	return buf.Bytes()
}

func writeFragmentItem(buf *bytes.Buffer, syntax transfer.Syntax, data []byte) {
	padded := core.VROB.PadToEvenLength(data)
	buf.Write(encodeUint16(core.TagItem.Group, syntax.ByteOrder))
	buf.Write(encodeUint16(core.TagItem.Element, syntax.ByteOrder))
	buf.Write(encodeUint32(uint32(len(padded)), syntax.ByteOrder))
	buf.Write(padded)
}

func encodeString(s string) []byte {
	return padToEven([]byte(s), ' ')
}

func encodeStringsForVR(vr core.VR, ss []string) []byte {
	joined := joinStrings(ss)
	if vr == core.VRUI {
		return padToEven([]byte(joined), 0x00)
	}
	if vr.IsStringLike() {
		return encodeString(joined)
	}
	return padToEven([]byte(joined), padByteForVR(vr))
}

func joinStrings(ss []string) string {
	return strings.Join(ss, "\\")
}

func encodeUint16(v uint16, order binary.ByteOrder) []byte {
	order = normalizeByteOrder(order)
	var data [2]byte
	order.PutUint16(data[:], v)
	return data[:]
}

func encodeUint32(v uint32, order binary.ByteOrder) []byte {
	order = normalizeByteOrder(order)
	var data [4]byte
	order.PutUint32(data[:], v)
	return data[:]
}

func padToEven(data []byte, padByte byte) []byte {
	padded := core.CloneBytes(data)
	if len(padded) == dicomenc.EvenLength(len(padded)) {
		return padded
	}
	return append(padded, padByte)
}

func padByteForVR(vr core.VR) byte {
	if vr == core.VRUI {
		return 0x00
	}
	if vr.IsStringLike() {
		return ' '
	}
	return 0x00
}

func byteOrderFor(syntax transfer.Syntax) binary.ByteOrder {
	return normalizeByteOrder(syntax.ByteOrder)
}

func normalizeByteOrder(order binary.ByteOrder) binary.ByteOrder {
	if order == nil {
		return binary.LittleEndian
	}
	return order
}

func implicitHeaderBytes(order binary.ByteOrder, tag core.Tag, length uint32) []byte {
	var buf bytes.Buffer
	writeTag(&buf, normalizeByteOrder(order), tag)
	buf.Write(encodeUint32(length, order))
	return buf.Bytes()
}

func writeTag(buf *bytes.Buffer, order binary.ByteOrder, tag core.Tag) {
	buf.Write(encodeUint16(tag.Group, order))
	buf.Write(encodeUint16(tag.Element, order))
}
