package core

import (
	"bytes"
)

type ValueKind uint8

const (
	ValueRaw ValueKind = iota
	ValueStrings
	ValueSequence
	ValueFragments
	ValueBulkData
	ValueUint16
	ValueInt16
	ValueUint32
	ValueInt32
	ValueUint64
	ValueInt64
	ValueFloat32
	ValueFloat64
	ValueTag
)

// Value is the common contract for element payloads held by the core model.
// Kind returns the payload discriminant and EncodedLength returns the
// serialized byte count when that count is defined in-line.
type Value interface {
	Kind() ValueKind
	EncodedLength() (Length, bool)
}

// RawValue preserves the original bytes of a primitive element payload.
// Text trims trailing space and NUL padding, while Strings splits the trimmed
// text by '\' and trims trailing padding from each component.
type RawValue []byte

func (RawValue) Kind() ValueKind { return ValueRaw }
func (v RawValue) EncodedLength() (Length, bool) {
	return Length(len(v)), true
}
func (v RawValue) Bytes() []byte { return []byte(v) }

func (v RawValue) Text() string {
	return TrimTextValue(VRUnknown, string(v))
}

func (v RawValue) Strings() []string {
	return SplitTextMultiplicity(VRUnknown, string(v))
}

// StringValue stores a primitive string value already split into its DICOM
// value multiplicity components. Its encoded form joins components with '\'.
type StringValue []string

func (StringValue) Kind() ValueKind { return ValueStrings }
func (v StringValue) EncodedLength() (Length, bool) {
	total := 0
	for i := range v {
		total += len(v[i])
	}
	if len(v) > 0 {
		total += len(v) - 1
	}
	if total%2 == 1 {
		total++
	}
	return Length(total), true
}

// The typed numeric values below represent DICOM binary-number multiplicity.
// Their serialized byte order is selected by the transfer syntax in parser.Writer.
// Textual number VRs such as DS and IS continue to use StringValue.
type Uint16Value []uint16

func (Uint16Value) Kind() ValueKind { return ValueUint16 }
func (v Uint16Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 2)
}

type Int16Value []int16

func (Int16Value) Kind() ValueKind { return ValueInt16 }
func (v Int16Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 2)
}

type Uint32Value []uint32

func (Uint32Value) Kind() ValueKind { return ValueUint32 }
func (v Uint32Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 4)
}

type Int32Value []int32

func (Int32Value) Kind() ValueKind { return ValueInt32 }
func (v Int32Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 4)
}

type Uint64Value []uint64

func (Uint64Value) Kind() ValueKind { return ValueUint64 }
func (v Uint64Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 8)
}

type Int64Value []int64

func (Int64Value) Kind() ValueKind { return ValueInt64 }
func (v Int64Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 8)
}

type Float32Value []float32

func (Float32Value) Kind() ValueKind { return ValueFloat32 }
func (v Float32Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 4)
}

type Float64Value []float64

func (Float64Value) Kind() ValueKind { return ValueFloat64 }
func (v Float64Value) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 8)
}

// TagValue stores one or more AT values. Each tag is encoded as its 16-bit
// group followed by its 16-bit element in the transfer syntax byte order.
type TagValue []Tag

func (TagValue) Kind() ValueKind { return ValueTag }
func (v TagValue) EncodedLength() (Length, bool) {
	return fixedWidthValueLength(len(v), 4)
}

func fixedWidthValueLength(count, width int) (Length, bool) {
	if count < 0 || width <= 0 {
		return 0, false
	}
	total := uint64(count) * uint64(width)
	if total >= uint64(UndefinedLength) {
		return 0, false
	}
	return Length(total), true
}

// DataSet is an ordered list of elements used as a sequence item payload.
type DataSet struct {
	Elements []Element
	// ItemOffset is the absolute encoded offset of the Item Data Element tag
	// when this data set was parsed from a sequence. It is metadata only and is
	// not used when encoding the data set again.
	ItemOffset    int64
	ItemOffsetSet bool
}

// SequenceValue represents a nested sequence of data sets and always uses
// undefined length in the core model.
type SequenceValue struct {
	Items []DataSet
}

func (SequenceValue) Kind() ValueKind { return ValueSequence }
func (SequenceValue) EncodedLength() (Length, bool) {
	return UndefinedLength, false
}

// FragmentSequence represents encapsulated pixel data as a basic offset table
// plus one or more encoded fragments. It always uses undefined length in the
// core model.
type FragmentSequence struct {
	OffsetTable []byte
	Fragments   [][]byte
}

func (FragmentSequence) Kind() ValueKind { return ValueFragments }
func (FragmentSequence) EncodedLength() (Length, bool) {
	return UndefinedLength, false
}

// BulkDataValue preserves an unresolved BulkDataURI reference from DICOM JSON.
// The URI is kept as metadata only and does not imply that payload bytes exist
// in memory.
type BulkDataValue struct {
	URI string
}

func (BulkDataValue) Kind() ValueKind { return ValueBulkData }
func (BulkDataValue) EncodedLength() (Length, bool) {
	return UndefinedLength, false
}

func CloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return bytes.Clone(b)
}
