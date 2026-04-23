package core

import (
	"bytes"
	"testing"
)

func TestNewRawElementSetsHeaderLengthAndClones(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	tag := NewTag(0x7FE0, 0x0010)
	elem := NewRawElement(tag, VROB, data)

	if got := elem.Tag(); got != tag {
		t.Fatalf("Tag() = %v, want %v", got, tag)
	}
	if got := elem.VR(); got != VROB {
		t.Fatalf("VR() = %v, want %v", got, VROB)
	}
	if got := elem.EncodedLength(); got != Length(len(data)) {
		t.Fatalf("EncodedLength() = %v, want %d", got, len(data))
	}
	if !elem.Header.HasLength() {
		t.Fatalf("NewRawElement should mark header length as explicitly set")
	}
	if got := elem.Length(); got != Length(len(data)) {
		t.Fatalf("Length() = %v, want %d", got, len(data))
	}

	data[0] = 0xFF
	raw, ok := elem.RawBytes()
	if !ok {
		t.Fatalf("RawBytes() returned ok=false")
	}
	if !bytes.Equal(raw, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("raw bytes = %v, want original cloned payload", raw)
	}
}

func TestElementRawBytesForNonRawValue(t *testing.T) {
	elem := Element{Value: StringValue{"TEST"}}
	if raw, ok := elem.RawBytes(); ok || raw != nil {
		t.Fatalf("RawBytes() = (%v, %v), want (nil, false)", raw, ok)
	}
}

func TestElementLengthFallback(t *testing.T) {
	stringElem := Element{
		Header: ElementHeader{Tag: NewTag(0x0010, 0x0010), VR: VRPN},
		Value:  StringValue{"ABC"},
	}
	if got := stringElem.Length(); got != 4 {
		t.Fatalf("StringValue fallback length = %v, want 4", got)
	}

	defined := Element{
		Header: ElementHeader{Tag: NewTag(0x0010, 0x0020), VR: VRLO, Length: 7, LengthSet: true},
		Value:  StringValue{"ABC"},
	}
	if got := defined.Length(); got != 7 {
		t.Fatalf("defined header length should win, got %v", got)
	}

	zeroLength := Element{
		Header: ElementHeader{Tag: NewTag(0x0010, 0x0030), VR: VRLO, Length: 0, LengthSet: true},
		Value:  StringValue{"ABC"},
	}
	if got := zeroLength.Length(); got != 0 {
		t.Fatalf("explicit zero header length should win, got %v", got)
	}

	sequence := Element{
		Header: ElementHeader{Tag: NewTag(0x0008, 0x1111), VR: VRSQ},
		Value:  SequenceValue{},
	}
	if got := sequence.Length(); got != UndefinedLength {
		t.Fatalf("sequence length = %v, want %v", got, UndefinedLength)
	}
}

func TestElementCalculatedLength(t *testing.T) {
	elem := Element{
		Header: ElementHeader{Tag: NewTag(0x0010, 0x0020), VR: VRLO},
		Value:  StringValue{"AB", "CD"},
	}

	length, ok := elem.CalculatedLength()
	if !ok || length != 6 {
		t.Fatalf("CalculatedLength() = (%v, %v), want (6, true)", length, ok)
	}

	sequence := Element{
		Header: ElementHeader{Tag: NewTag(0x0008, 0x1111), VR: VRSQ},
		Value:  SequenceValue{},
	}
	if length, ok := sequence.CalculatedLength(); ok || length != UndefinedLength {
		t.Fatalf("sequence CalculatedLength() = (%v, %v), want (%v, false)", length, ok, UndefinedLength)
	}
}

func TestElementStringAccessors(t *testing.T) {
	raw := Element{Value: RawValue([]byte("ONE\\TWO \x00"))}
	if got := raw.StringValue(); got != "ONE\\TWO" {
		t.Fatalf("raw StringValue() = %q", got)
	}
	if got := raw.StringValues(); len(got) != 2 || got[0] != "ONE" || got[1] != "TWO" {
		t.Fatalf("raw StringValues() = %v", got)
	}

	strings := Element{Value: StringValue{"ONE", "TWO"}}
	if got := strings.StringValue(); got != "ONE" {
		t.Fatalf("StringValue() = %q, want ONE", got)
	}
	if got := strings.StringValues(); len(got) != 2 || got[0] != "ONE" || got[1] != "TWO" {
		t.Fatalf("StringValues() = %v", got)
	}

	single := Element{Value: StringValue{"ONE"}}
	if got := single.StringValue(); got != "ONE" {
		t.Fatalf("single StringValue() = %q, want ONE", got)
	}
	if got := single.StringValues(); len(got) != 1 || got[0] != "ONE" {
		t.Fatalf("single StringValues() = %v", got)
	}
}
