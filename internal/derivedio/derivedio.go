package derivedio

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	TagMediaStorageSOPClassUID    = core.NewTag(0x0002, 0x0002)
	TagMediaStorageSOPInstanceUID = core.NewTag(0x0002, 0x0003)
	TagTransferSyntaxUID          = core.NewTag(0x0002, 0x0010)

	TagSOPClassUID       = core.NewTag(0x0008, 0x0016)
	TagSOPInstanceUID    = core.NewTag(0x0008, 0x0018)
	TagContentDate       = core.NewTag(0x0008, 0x0023)
	TagContentTime       = core.NewTag(0x0008, 0x0033)
	TagModality          = core.NewTag(0x0008, 0x0060)
	TagSeriesDescr       = core.NewTag(0x0008, 0x103E)
	TagRefSOPClassUID    = core.NewTag(0x0008, 0x1150)
	TagRefSOPInstanceUID = core.NewTag(0x0008, 0x1155)
	TagRefFrameNumber    = core.NewTag(0x0008, 0x1160)

	TagStudyInstanceUID    = core.NewTag(0x0020, 0x000D)
	TagSeriesInstanceUID   = core.NewTag(0x0020, 0x000E)
	TagFrameOfReferenceUID = core.NewTag(0x0020, 0x0052)

	TagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	TagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	TagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	TagRows                      = core.NewTag(0x0028, 0x0010)
	TagColumns                   = core.NewTag(0x0028, 0x0011)
	TagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	TagBitsStored                = core.NewTag(0x0028, 0x0101)
	TagHighBit                   = core.NewTag(0x0028, 0x0102)
	TagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
	TagPixelData                 = core.NewTag(0x7FE0, 0x0010)
)

// Object builds a DICOM object from the given elements using the standard dictionary.
func Object(elements ...core.Element) *object.Object {
	return object.FromElements(elements, std.Dictionary)
}

// File creates a DICOM file object with the specified SOP class UID, instance UID,
// and dataset using explicit VR little-endian transfer syntax.
// It returns object.ErrMissingSOPClassUID if sopClassUID is empty,
// object.ErrMissingSOPInstanceUID if sopInstanceUID is empty, or an error if dataset is nil.
func File(sopClassUID string, sopInstanceUID string, dataset *object.Object) (*object.File, error) {
	if strings.TrimSpace(sopClassUID) == "" {
		return nil, object.ErrMissingSOPClassUID
	}
	if strings.TrimSpace(sopInstanceUID) == "" {
		return nil, object.ErrMissingSOPInstanceUID
	}
	if dataset == nil {
		return nil, fmt.Errorf("dicom derived: dataset is required")
	}
	return &object.File{
		Meta: Object(
			UI(TagMediaStorageSOPClassUID, sopClassUID),
			UI(TagMediaStorageSOPInstanceUID, sopInstanceUID),
			UI(TagTransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID),
		),
		Dataset:        dataset,
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, nil
}

// Str constructs a string element with the specified tag, VR, and value.
func Str(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
}

// Strings constructs a DICOM element containing multiple string values.
func Strings(tag core.Tag, vr core.VR, values []string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(append([]string(nil), values...))}
}

// UI builds a DICOM element for unique identifiers.
func UI(tag core.Tag, value string) core.Element {
	return Str(tag, core.VRUI, value)
}

// CS constructs a Code String element.
func CS(tag core.Tag, value string) core.Element {
	return Str(tag, core.VRCS, value)
}

// LO constructs a string element with Long String (LO) value representation.
func LO(tag core.Tag, value string) core.Element {
	return Str(tag, core.VRLO, value)
}

// SH constructs a string element with SH (Short Header) VR.
func SH(tag core.Tag, value string) core.Element {
	return Str(tag, core.VRSH, value)
}

// IS builds a DICOM Integer String (IS) element from the provided integers.
func IS(tag core.Tag, values ...int) core.Element {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.Itoa(value)
	}
	return Strings(tag, core.VRIS, out)
}

// DS constructs a DICOM element with Decimal String VR from the provided float64 values.
func DS(tag core.Tag, values ...float64) core.Element {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = formatDS(value)
	}
	return Strings(tag, core.VRDS, out)
}

func formatDS(value float64) string {
	const maxLength = 16

	formatted := strconv.FormatFloat(value, 'g', -1, 64)
	if len(formatted) <= maxLength {
		return formatted
	}
	for precision := maxLength; precision > 0; precision-- {
		formatted = strconv.FormatFloat(value, 'g', precision, 64)
		if len(formatted) <= maxLength {
			if _, err := strconv.ParseFloat(formatted, 64); err != nil {
				continue
			}
			return formatted
		}
	}
	// Scientific notation with seven significant digits fits even with a sign
	// and a three-digit float64 exponent. The loop above normally finds a more
	// precise representation; this is the bounded fallback for finite values.
	return strconv.FormatFloat(value, 'e', 6, 64)
}

// US constructs an Unsigned Short element using little-endian binary values.
func US(tag core.Tag, values ...uint16) core.Element {
	raw := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(raw[i*2:], value)
	}
	return Raw(tag, core.VRUS, raw)
}

// UL constructs an Unsigned Long element using little-endian binary values.
func UL(tag core.Tag, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], value)
	}
	return Raw(tag, core.VRUL, raw)
}

// SL constructs a Signed Long element using little-endian binary values.
func SL(tag core.Tag, values ...int32) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], uint32(value))
	}
	return Raw(tag, core.VRSL, raw)
}

// FL constructs a Floating Point Single element using little-endian binary values.
func FL(tag core.Tag, values ...float64) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(float32(value)))
	}
	return Raw(tag, core.VRFL, raw)
}

// FD constructs a Floating Point Double element using little-endian binary values.
func FD(tag core.Tag, values ...float64) core.Element {
	raw := make([]byte, len(values)*8)
	for i, value := range values {
		binary.LittleEndian.PutUint64(raw[i*8:], math.Float64bits(value))
	}
	return Raw(tag, core.VRFD, raw)
}

// Raw constructs a DICOM element with the specified tag, value representation, and raw binary data.
func Raw(tag core.Tag, vr core.VR, data []byte) core.Element {
	return core.NewRawElement(tag, vr, data)
}

// Seq creates a DICOM sequence element with the specified tag and items.
func Seq(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.SequenceValue{Items: items},
	}
}

// DataSet returns a DataSet containing the provided elements.
func DataSet(elements ...core.Element) core.DataSet {
	return core.DataSet{Elements: elements}
}

// CleanString retrieves the string value for a tag and returns it with leading and trailing whitespace and null bytes removed. Returns an empty string if the tag is missing.
func CleanString(obj *object.Object, tag core.Tag) string {
	value, ok := obj.GetString(tag)
	if !ok {
		return ""
	}
	return strings.TrimFunc(value, func(r rune) bool {
		return r == 0 || unicode.IsSpace(r)
	})
}

// CleanUID retrieves a UID from obj using tag and returns it with surrounding whitespace trimmed, or an empty string if the tag is missing.
func CleanUID(obj *object.Object, tag core.Tag) string {
	value, ok := obj.GetUID(tag)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// Int retrieves an integer value from a DICOM object by tag.
// If the tag is not found or an error occurs, it returns 0.
func Int(obj *object.Object, tag core.Tag) int {
	values := Ints(obj, tag)
	if len(values) == 0 {
		return 0
	}
	return int(values[0])
}

// Ints retrieves integer values from either IS strings or the standard binary
// integer VRs. It returns nil when the element is missing or malformed.
func Ints(obj *object.Object, tag core.Tag) []int64 {
	values, err := LookupInts(obj, tag)
	if err != nil {
		return nil
	}
	return values
}

// LookupInts retrieves integer values from IS strings or standard binary
// integer VRs and reports missing, malformed, or unsupported values.
func LookupInts(obj *object.Object, tag core.Tag) ([]int64, error) {
	if obj == nil {
		return nil, fmt.Errorf("dicom derived: nil object for %s", tag)
	}
	element, ok := obj.Get(tag)
	if !ok {
		return nil, fmt.Errorf("dicom derived: missing element %s", tag)
	}
	if element.VR() == core.VRIS {
		values, err := obj.GetInts(tag)
		if err != nil {
			return nil, err
		}
		return values, nil
	}
	raw, ok := element.RawBytes()
	if !ok {
		return nil, fmt.Errorf("dicom derived: element %s with VR %s is not raw integer data", tag, element.VR())
	}
	order := obj.ValueByteOrder()
	var width int
	switch element.VR() {
	case core.VRUS, core.VRSS:
		width = 2
	case core.VRUL, core.VRSL:
		width = 4
	case core.VRUV, core.VRSV:
		width = 8
	default:
		return nil, fmt.Errorf("dicom derived: element %s has unsupported integer VR %s", tag, element.VR())
	}
	if len(raw)%width != 0 {
		return nil, fmt.Errorf("dicom derived: element %s has %d bytes, want a multiple of %d for VR %s", tag, len(raw), width, element.VR())
	}
	out := make([]int64, len(raw)/width)
	for i := range out {
		offset := i * width
		switch element.VR() {
		case core.VRUS:
			out[i] = int64(order.Uint16(raw[offset:]))
		case core.VRSS:
			out[i] = int64(int16(order.Uint16(raw[offset:])))
		case core.VRUL:
			out[i] = int64(order.Uint32(raw[offset:]))
		case core.VRSL:
			out[i] = int64(int32(order.Uint32(raw[offset:])))
		case core.VRUV:
			value := order.Uint64(raw[offset:])
			if value > math.MaxInt64 {
				return nil, fmt.Errorf("dicom derived: element %s unsigned value overflows int64", tag)
			}
			out[i] = int64(value)
		case core.VRSV:
			out[i] = int64(order.Uint64(raw[offset:]))
		}
	}
	return out, nil
}

// Floats retrieves values from DS strings or FL/FD binary elements. It returns
// nil if the tag is missing or cannot be parsed.
func Floats(obj *object.Object, tag core.Tag) []float64 {
	values, err := LookupFloats(obj, tag)
	if err != nil {
		return nil
	}
	return values
}

// LookupFloats retrieves values from DS strings or FL/FD binary elements and
// reports missing, malformed, or unsupported values.
func LookupFloats(obj *object.Object, tag core.Tag) ([]float64, error) {
	if obj == nil {
		return nil, fmt.Errorf("dicom derived: nil object for %s", tag)
	}
	element, ok := obj.Get(tag)
	if !ok {
		return nil, fmt.Errorf("dicom derived: missing element %s", tag)
	}
	if element.VR() == core.VRDS {
		values, err := obj.GetFloats(tag)
		if err != nil {
			return nil, err
		}
		return values, nil
	}
	raw, ok := element.RawBytes()
	if !ok {
		return nil, fmt.Errorf("dicom derived: element %s with VR %s is not raw floating-point data", tag, element.VR())
	}
	order := obj.ValueByteOrder()
	switch element.VR() {
	case core.VRFL:
		if len(raw)%4 != 0 {
			return nil, fmt.Errorf("dicom derived: element %s has %d bytes, want a multiple of 4 for VR FL", tag, len(raw))
		}
		out := make([]float64, len(raw)/4)
		for i := range out {
			out[i] = float64(math.Float32frombits(order.Uint32(raw[i*4:])))
		}
		return out, nil
	case core.VRFD:
		if len(raw)%8 != 0 {
			return nil, fmt.Errorf("dicom derived: element %s has %d bytes, want a multiple of 8 for VR FD", tag, len(raw))
		}
		out := make([]float64, len(raw)/8)
		for i := range out {
			out[i] = math.Float64frombits(order.Uint64(raw[i*8:]))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("dicom derived: element %s has unsupported floating-point VR %s", tag, element.VR())
	}
}

// Sequence retrieves a sequence element from the object by tag. It returns nil if the element is missing, otherwise returns the sequence items.
func Sequence(obj *object.Object, tag core.Tag) []*object.Object {
	items, ok := obj.GetSequence(tag)
	if !ok {
		return nil
	}
	return items
}

// Uint16Bytes encodes the uint16 values into a byte slice using little-endian byte order.
func Uint16Bytes(values []uint16) []byte {
	out := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(out[i*2:], value)
	}
	return out
}

// Uint16s converts a byte slice to uint16 values using little-endian byte order.
func Uint16s(data []byte) []uint16 {
	out := make([]uint16, len(data)/2)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return out
}

// SamePosition reports whether a and b are within 0.001 of each other.
func SamePosition(a, b float64) bool {
	return math.Abs(a-b) <= 1e-3
}
