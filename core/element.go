package core

import "bytes"

// ElementHeader stores the metadata encoded alongside a DICOM element value.
type ElementHeader struct {
	// Tag identifies the element within the data set.
	Tag Tag
	// VR is the value representation associated with the element.
	VR VR
	// Length is the encoded value length carried by the element header.
	Length Length
	// LengthSet reports whether Length was explicitly provided by the header.
	LengthSet bool
}

// Element is an in-memory DICOM data element composed of a header and value.
type Element struct {
	Header ElementHeader
	Value  Value
}

func NewRawElement(tag Tag, vr VR, data []byte) Element {
	return Element{
		Header: ElementHeader{Tag: tag, VR: vr, Length: Length(len(data)), LengthSet: true},
		Value:  RawValue(CloneBytes(data)),
	}
}

func (e Element) Tag() Tag { return e.Header.Tag }
func (e Element) VR() VR   { return e.Header.VR }

// HasLength reports whether the header explicitly carries an encoded length.
func (h ElementHeader) HasLength() bool {
	return h.LengthSet
}

// EncodedLength returns the raw length value preserved in the element header.
// Use Header.HasLength to distinguish an explicit zero from an unset length.
func (e Element) EncodedLength() Length {
	return e.Header.Length
}

// CalculatedLength returns the serialized byte length derived from the current
// in-memory value. Sequence and fragment values report false because they use
// undefined length in the core model.
func (e Element) CalculatedLength() (Length, bool) {
	if e.Value == nil {
		return 0, false
	}
	return e.Value.EncodedLength()
}

// Length returns the effective element length using this fallback chain:
// an explicit header length when present, calculated length from the in-memory
// value when the header has no encoded length, and UndefinedLength when the
// value has no defined serialized size.
func (e Element) Length() Length {
	if e.Header.HasLength() {
		return e.Header.Length
	}
	if length, ok := e.CalculatedLength(); ok {
		return length
	}
	return UndefinedLength
}

func (e Element) RawBytes() ([]byte, bool) {
	raw, ok := e.Value.(RawValue)
	if !ok {
		return nil, false
	}
	return raw.Bytes(), true
}

func (e Element) StringValue() string {
	if raw, ok := e.Value.(RawValue); ok {
		return TrimTextValue(e.VR(), string(raw))
	}
	if strings, ok := e.Value.(StringValue); ok {
		if len(strings) == 0 {
			return ""
		}
		return TrimTextValue(e.VR(), strings[0])
	}
	return ""
}

func (e Element) StringValues() []string {
	if raw, ok := e.Value.(RawValue); ok {
		parts := bytes.Split(raw, []byte{'\\'})
		if len(parts) == 1 && len(TrimTextValueBytes(e.VR(), parts[0])) == 0 {
			return nil
		}
		values := make([]string, len(parts))
		for i := range parts {
			values[i] = string(TrimTextValueBytes(e.VR(), parts[i]))
		}
		return values
	}
	if strings, ok := e.Value.(StringValue); ok {
		if len(strings) == 0 {
			return nil
		}
		values := make([]string, len(strings))
		for i := range strings {
			values[i] = TrimTextValue(e.VR(), strings[i])
		}
		return values
	}
	return nil
}
