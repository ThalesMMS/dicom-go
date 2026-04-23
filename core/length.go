package core

import "fmt"

// Length represents the serialized byte length associated with a DICOM
// element or item. In dicom-go, ElementHeader.Length preserves the encoded
// length seen on the wire, while Element helpers may also derive a calculated
// length from the in-memory Value when needed. UndefinedLength (0xFFFFFFFF)
// denotes delimiter-terminated content such as sequences or encapsulated pixel
// data. As a plain Go value, two UndefinedLength values compare equal.
type Length uint32

const UndefinedLength Length = 0xFFFFFFFF

// IsUndefined reports whether the length is delimiter-terminated.
func (l Length) IsUndefined() bool {
	return l == UndefinedLength
}

// IsDefined reports whether the length is a concrete byte count.
func (l Length) IsDefined() bool {
	return !l.IsUndefined()
}

// Value returns the concrete byte count when the length is defined.
func (l Length) Value() (uint32, bool) {
	if l.IsUndefined() {
		return 0, false
	}
	return uint32(l), true
}

func (l Length) String() string {
	if l.IsUndefined() {
		return "UNDEFINED"
	}
	return fmt.Sprintf("%d", uint32(l))
}
