package display

import (
	"errors"
	"testing"
)

func TestNewLUTValid(t *testing.T) {
	lut, err := NewLUT([]int{3, 0, 16}, []uint16{10, 20, 30})
	if err != nil {
		t.Fatalf("NewLUT() error = %v", err)
	}
	if lut.FirstMapped != 0 || lut.BitsPerEntry != 16 || len(lut.Entries) != 3 {
		t.Fatalf("NewLUT() = %#v", lut)
	}
}

func TestNewLUTZeroEntriesMeans65536(t *testing.T) {
	entries := make([]uint16, 1<<16)
	if _, err := NewLUT([]int{0, 0, 16}, entries); err != nil {
		t.Fatalf("NewLUT() with 0 (=65536) entries error = %v", err)
	}
}

func TestNewLUTInvalidDescriptor(t *testing.T) {
	tests := []struct {
		name       string
		descriptor []int
		entries    []uint16
		wantErr    error
	}{
		{"too few values", []int{3, 0}, []uint16{1, 2, 3}, ErrInvalidLUTDescriptor},
		{"too many values", []int{3, 0, 16, 1}, []uint16{1, 2, 3}, ErrInvalidLUTDescriptor},
		{"bad bits per entry", []int{3, 0, 7}, []uint16{1, 2, 3}, ErrInvalidLUTDescriptor},
		{"negative entries", []int{-1, 0, 16}, nil, ErrInvalidLUTDescriptor},
		{"data length mismatch", []int{3, 0, 16}, []uint16{1, 2}, ErrInvalidLUTData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLUT(tc.descriptor, tc.entries)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewLUT() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLUTLookupClamps(t *testing.T) {
	lut, err := NewLUT([]int{3, 10, 16}, []uint16{100, 200, 300})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		in   int
		want uint16
	}{
		{0, 100},  // below FirstMapped -> first entry
		{9, 100},  // just below -> first entry
		{10, 100}, // FirstMapped -> entry 0
		{11, 200}, // entry 1
		{12, 300}, // entry 2 (last)
		{99, 300}, // above range -> last entry
	}
	for _, tc := range tests {
		if got := lut.Lookup(tc.in); got != tc.want {
			t.Fatalf("Lookup(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLUTOutputRange(t *testing.T) {
	lut, err := NewLUT([]int{4, 0, 16}, []uint16{50, 10, 4000, 200})
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := lut.outputRange()
	if lo != 10 || hi != 4000 {
		t.Fatalf("outputRange() = (%d, %d), want (10, 4000)", lo, hi)
	}
}
