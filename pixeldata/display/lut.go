package display

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidLUTDescriptor reports a LUT Descriptor (0028,3002) that does
	// not hold exactly three values, declares an unsupported bits-per-entry, or
	// declares a negative entry count.
	ErrInvalidLUTDescriptor = errors.New("dicom/display: invalid LUT descriptor")
	// ErrInvalidLUTData reports LUT Data (0028,3006) whose length does not match
	// the entry count declared by the LUT Descriptor.
	ErrInvalidLUTData = errors.New("dicom/display: LUT data length does not match descriptor")
)

// LUT models a DICOM lookup table built from a LUT Descriptor (0028,3002) and
// LUT Data (0028,3006). It backs both the Modality LUT (PS3.3 C.11.1) and the
// VOI LUT (PS3.3 C.11.2) transforms.
//
// A LUT Descriptor holds three values: the number of entries, the first stored
// pixel value mapped, and the number of bits per entry. Per the standard a
// number-of-entries value of 0 means 2^16 (65536) entries.
type LUT struct {
	// FirstMapped is the input value mapped by entry 0 (LUT Descriptor value 2).
	// Inputs at or below FirstMapped map to the first entry; inputs at or above
	// FirstMapped+len(Entries)-1 map to the last entry.
	FirstMapped int
	// BitsPerEntry is LUT Descriptor value 3; DICOM permits 8 or 16.
	BitsPerEntry int
	// Entries holds the output values, one per declared entry.
	Entries []uint16
}

// NewLUT validates descriptor and entries and returns a LUT. descriptor must
// hold exactly [numberOfEntries, firstMapped, bitsPerEntry]; firstMapped is
// taken verbatim (callers sign-interpret it before calling, per the relevant
// Pixel Representation). A numberOfEntries of 0 is treated as 65536.
func NewLUT(descriptor []int, entries []uint16) (*LUT, error) {
	if len(descriptor) != 3 {
		return nil, fmt.Errorf("%w: got %d descriptor values, want 3", ErrInvalidLUTDescriptor, len(descriptor))
	}
	numberOfEntries := descriptor[0]
	if numberOfEntries == 0 {
		numberOfEntries = 1 << 16
	}
	if numberOfEntries < 0 {
		return nil, fmt.Errorf("%w: number of entries %d is negative", ErrInvalidLUTDescriptor, numberOfEntries)
	}
	bits := descriptor[2]
	if bits != 8 && bits != 16 {
		return nil, fmt.Errorf("%w: bits per entry %d, want 8 or 16", ErrInvalidLUTDescriptor, bits)
	}
	if len(entries) != numberOfEntries {
		return nil, fmt.Errorf("%w: got %d entries, want %d", ErrInvalidLUTData, len(entries), numberOfEntries)
	}
	return &LUT{
		FirstMapped:  descriptor[1],
		BitsPerEntry: bits,
		Entries:      entries,
	}, nil
}

// Lookup maps value through the LUT, clamping to the first/last entry for
// inputs outside the mapped range.
func (l *LUT) Lookup(value int) uint16 {
	return l.lookupInt64(int64(value))
}

func (l *LUT) lookupInt64(value int64) uint16 {
	if len(l.Entries) == 0 {
		return 0
	}
	idx := value - int64(l.FirstMapped)
	if idx < 0 {
		idx = 0
	}
	if idx >= int64(len(l.Entries)) {
		idx = int64(len(l.Entries) - 1)
	}
	return l.Entries[idx]
}

// outputRange returns the smallest and largest entry values, used to scale a
// VOI LUT result into the 8-bit display range.
func (l *LUT) outputRange() (minEntry, maxEntry uint16) {
	if len(l.Entries) == 0 {
		return 0, 0
	}
	minEntry, maxEntry = l.Entries[0], l.Entries[0]
	for _, e := range l.Entries[1:] {
		if e < minEntry {
			minEntry = e
		}
		if e > maxEntry {
			maxEntry = e
		}
	}
	return minEntry, maxEntry
}
