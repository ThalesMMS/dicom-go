package roi

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestNewRasterMaskFromBufferValidatesDimensionsAndLength(t *testing.T) {
	tests := []struct {
		name    string
		columns int
		rows    int
		buffer  []byte
		wantErr error
	}{
		{name: "zero columns", columns: 0, rows: 2, buffer: make([]byte, 0), wantErr: ErrInvalidRasterMaskDimensions},
		{name: "zero rows", columns: 2, rows: 0, buffer: make([]byte, 0), wantErr: ErrInvalidRasterMaskDimensions},
		{name: "negative columns", columns: -1, rows: 2, buffer: make([]byte, 0), wantErr: ErrInvalidRasterMaskDimensions},
		{name: "negative rows", columns: 2, rows: -1, buffer: make([]byte, 0), wantErr: ErrInvalidRasterMaskDimensions},
		{name: "multiplication overflow", columns: math.MaxInt, rows: 2, buffer: nil, wantErr: ErrRasterMaskDimensionOverflow},
		{name: "pixel limit", columns: 1, rows: RasterMaskPixelLimit + 1, buffer: nil, wantErr: ErrRasterMaskTooLarge},
		{name: "short buffer", columns: 2, rows: 2, buffer: make([]byte, 3), wantErr: ErrRasterMaskBufferLength},
		{name: "long buffer", columns: 2, rows: 2, buffer: make([]byte, 5), wantErr: ErrRasterMaskBufferLength},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mask, err := NewRasterMaskFromBuffer(test.columns, test.rows, test.buffer)
			if mask != nil {
				t.Fatalf("mask = %#v, want nil on invalid input", mask)
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.wantErr)
			}
		})
	}
}

func TestNewRasterMaskFromBufferBuildsRunMaskWithoutChangingValidOperations(t *testing.T) {
	buffer := []byte{
		0, 1, 1, 0,
		1, 1, 0, 0,
		0, 0, 0, 1,
	}
	mask, err := NewRasterMaskFromBuffer(4, 3, buffer)
	if err != nil {
		t.Fatalf("NewRasterMaskFromBuffer() error = %v", err)
	}
	want := [][]MaskRun{
		{{Start: 1, End: 3}},
		{{Start: 0, End: 2}},
		{{Start: 3, End: 4}},
	}
	for y := range want {
		if got := mask.Runs(y); !reflect.DeepEqual(got, want[y]) {
			t.Fatalf("row %d runs = %#v, want %#v", y, got, want[y])
		}
	}
	if got := mask.Count(); got != 5 {
		t.Fatalf("mask count = %d, want 5", got)
	}
	mask.Set(0, 0, true)
	mask.ClearRun(1, 0, 1)
	if !mask.Get(0, 0) || mask.Get(0, 1) {
		t.Fatal("valid mask lost normal Set/Clear behavior")
	}
	alias, err := NewRasterMaskFromBytes(4, 3, buffer)
	if err != nil || alias.Count() != 5 {
		t.Fatalf("NewRasterMaskFromBytes() = mask count %d, error %v; want count 5 and no error", alias.Count(), err)
	}
}
