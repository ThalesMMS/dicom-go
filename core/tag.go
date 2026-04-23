package core

import (
	"fmt"
	"strconv"
	"strings"
)

type GroupNumber = uint16
type ElementNumber = uint16

// Tag identifies a DICOM data element by its group and element numbers.
type Tag struct {
	Group   GroupNumber
	Element ElementNumber
}

var (
	TagPixelData                = NewTag(0x7FE0, 0x0010)
	TagItem                     = NewTag(0xFFFE, 0xE000)
	TagItemDelimitationItem     = NewTag(0xFFFE, 0xE00D)
	TagSequenceDelimitationItem = NewTag(0xFFFE, 0xE0DD)
)

func NewTag(group, element uint16) Tag {
	return Tag{Group: group, Element: element}
}

func (t Tag) String() string {
	return fmt.Sprintf("(%04X,%04X)", t.Group, t.Element)
}

func (t Tag) HexString() string {
	return fmt.Sprintf("%04X%04X", t.Group, t.Element)
}

// Compare orders tags by group number first and element number second.
// It returns -1 when t sorts before other, +1 when it sorts after other,
// and 0 when both tags are equal.
func (t Tag) Compare(other Tag) int {
	switch {
	case t.Group < other.Group:
		return -1
	case t.Group > other.Group:
		return 1
	case t.Element < other.Element:
		return -1
	case t.Element > other.Element:
		return 1
	default:
		return 0
	}
}

// Less reports whether t sorts before other using group-first ordering.
func (t Tag) Less(other Tag) bool {
	return t.Compare(other) < 0
}

func (t Tag) IsPrivate() bool {
	return t.Group%2 == 1
}

func (t Tag) IsGroupLength() bool {
	return t.Element == 0x0000
}

func (t Tag) IsSequenceDelimiting() bool {
	return t.Group == 0xFFFE
}

func (t Tag) IsItem() bool {
	return t == TagItem
}

func (t Tag) IsItemDelimitationItem() bool {
	return t == TagItemDelimitationItem
}

func (t Tag) IsSequenceDelimitationItem() bool {
	return t == TagSequenceDelimitationItem
}

func ParseTag(s string) (Tag, error) {
	s = strings.TrimSpace(s)
	if len(s) == 8 && !strings.ContainsRune(s, ',') {
		value, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return Tag{}, fmt.Errorf("dicom: invalid tag %q: %w", s, err)
		}
		return Tag{
			Group:   uint16(value >> 16),
			Element: uint16(value),
		}, nil
	}

	hasOpen := strings.HasPrefix(s, "(")
	hasClose := strings.HasSuffix(s, ")")
	if hasOpen != hasClose {
		return Tag{}, fmt.Errorf("dicom: invalid tag %q: unbalanced parentheses", s)
	}
	if hasOpen {
		s = strings.TrimPrefix(s, "(")
		s = strings.TrimSuffix(s, ")")
	}
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return Tag{}, fmt.Errorf("dicom: invalid tag %q", s)
	}
	g, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 16, 16)
	if err != nil {
		return Tag{}, fmt.Errorf("dicom: invalid tag group %q: %w", parts[0], err)
	}
	e, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 16, 16)
	if err != nil {
		return Tag{}, fmt.Errorf("dicom: invalid tag element %q: %w", parts[1], err)
	}
	return Tag{Group: uint16(g), Element: uint16(e)}, nil
}
