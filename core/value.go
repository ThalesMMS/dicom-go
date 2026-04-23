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

// DataSet is an ordered list of elements used as a sequence item payload.
type DataSet struct {
	Elements []Element
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
