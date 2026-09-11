package encapsulated

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestAssembleFrameCodestreamsFastPathBorrowsFragments(t *testing.T) {
	fragments := [][]byte{{0xff, 0xd8, 0xff, 0xd9}, {0xff, 0xd8, 0xff, 0xd9}}
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || &frames[0][0] != &fragments[0][0] || &frames[1][0] != &fragments[1][0] {
		t.Fatal("one-fragment-per-frame fast path did not return borrowed views")
	}
}

func TestAssembleFrameCodestreamsJoinsSingleFrameFragments(t *testing.T) {
	fragments := [][]byte{{0xff, 0xd8}, {0xff, 0xda, 0, 2, 1, 2}, {0xff, 0xd9}}
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 2, 0xff, 0xd9}
	if len(frames) != 1 || !bytes.Equal(frames[0], want) {
		t.Fatalf("frames = % x, want % x", frames, want)
	}
	if &frames[0][0] == &fragments[0][0] {
		t.Fatal("joined frame aliases its first source fragment")
	}
}

func TestAssembleFrameCodestreamsUsesBasicOffsetTable(t *testing.T) {
	fragments := [][]byte{{0xff, 0xd8}, {1, 2}, {0xff, 0xd8}, {3, 4}}
	frames, err := collectCodestreams(encapsulatedPixel(fragments, basicOffsets(0, 20)), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{0xff, 0xd8, 1, 2}, {0xff, 0xd8, 3, 4}}
	if len(frames) != len(want) {
		t.Fatalf("frame count = %d, want %d", len(frames), len(want))
	}
	for i := range want {
		if !bytes.Equal(frames[i], want[i]) {
			t.Fatalf("frame %d = % x, want % x", i, frames[i], want[i])
		}
	}
}

func TestAssembleFrameCodestreamsRejectsMalformedBasicOffsetTables(t *testing.T) {
	fragments := [][]byte{{1, 2}, {3, 4}, {5, 6}}
	tests := []struct {
		name   string
		table  []byte
		frames int
	}{
		{name: "wrong entry count", table: basicOffsets(0), frames: 2},
		{name: "first non-zero", table: basicOffsets(10, 20), frames: 2},
		{name: "duplicate", table: basicOffsets(0, 0), frames: 2},
		{name: "decreasing", table: basicOffsets(0, 20, 10), frames: 3},
		{name: "unaligned", table: basicOffsets(0, 11), frames: 2},
		{name: "outside fragments", table: basicOffsets(0, 40), frames: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := collectCodestreams(encapsulatedPixel(fragments, test.table), nil, test.frames)
			if !errors.Is(err, ErrLayout) {
				t.Fatalf("error = %v, want fragment layout error", err)
			}
		})
	}
}

func TestAssembleFrameCodestreamsInfersEmptyBOTFrameBoundariesFromEOI(t *testing.T) {
	fragments := [][]byte{
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 2},
		{3, 4, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 5, 6},
		{7, 0xff, 0xd9, 0},
	}
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 2, 3, 4, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 5, 6, 7, 0xff, 0xd9, 0},
	}
	for i := range want {
		if !bytes.Equal(frames[i], want[i]) {
			t.Fatalf("frame %d = % x, want % x", i, frames[i], want[i])
		}
	}
}

func TestAssembleFrameCodestreamsAllowsMarkerSplitAcrossFragments(t *testing.T) {
	fragments := [][]byte{
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 0xff},
		{2, 3, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 4, 0xff, 0xd9},
	}
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !bytes.Equal(frames[0], []byte{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 0xff, 2, 3, 0xff, 0xd9}) {
		t.Fatalf("frames = % x", frames)
	}
}

func TestAssembleFrameCodestreamsInfersMixedFragmentCounts(t *testing.T) {
	fragments := [][]byte{
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 2, 3},
		{4, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 5, 0xff, 0xd9},
	}
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || &frames[0][0] != &fragments[0][0] || &frames[2][0] != &fragments[3][0] {
		t.Fatalf("mixed frame assembly did not retain one-fragment fast paths: % x", frames)
	}
	if !bytes.Equal(frames[1], []byte{0xff, 0xd8, 0xff, 0xda, 0, 2, 2, 3, 4, 0xff, 0xd9}) {
		t.Fatalf("fragmented frame = % x", frames[1])
	}
}

func TestAssembleFrameCodestreamsRejectsAmbiguousLayout(t *testing.T) {
	_, err := collectCodestreams(encapsulatedPixel([][]byte{{0xff, 0xd8}, {3, 4}, {5, 6}}, nil), nil, 2)
	if !errors.Is(err, ErrLayout) {
		t.Fatalf("error = %v, want fragment layout error", err)
	}
}

func TestAssembleFrameCodestreamsRejectsAmbiguousEOIBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		fragments [][]byte
		frames    int
	}{
		{
			name:      "too few EOI markers",
			fragments: [][]byte{{0xff, 0xd8, 0xff, 0xda, 0, 2, 1, 2}, {3, 4}, {0xff, 0xd9}},
			frames:    2,
		},
		{
			name: "too many EOI markers",
			fragments: [][]byte{
				{0xff, 0xd8, 0xff, 0xd9},
				{0xff, 0xd8, 0xff, 0xd9},
				{0xff, 0xd8, 0xff, 0xd9},
			},
			frames: 2,
		},
		{
			name:      "EOI not terminal",
			fragments: [][]byte{{0xff, 0xd8}, {0xff, 0xd9, 1, 2}, {0xff, 0xd8, 0xff, 0xd9}},
			frames:    2,
		},
		{
			name:      "more than one padding byte",
			fragments: [][]byte{{0xff, 0xd8}, {0xff, 0xd9, 0, 0}, {0xff, 0xd8, 0xff, 0xd9}},
			frames:    2,
		},
		{
			name:      "non-zero padding",
			fragments: [][]byte{{0xff, 0xd8}, {0xff, 0xd9, 1}, {0xff, 0xd8, 0xff, 0xd9}},
			frames:    2,
		},
		{
			name:      "invalid header before split EOI",
			fragments: [][]byte{{0xff, 0xd8, 1, 0xff}, {0xd9, 0}, {0xff, 0xd8, 0xff, 0xd9}},
			frames:    2,
		},
		{
			name:      "next frame lacks SOI",
			fragments: [][]byte{{0xff, 0xd8}, {0xff, 0xd9}, {1, 2}, {0xff, 0xd9}},
			frames:    2,
		},
		{
			name:      "first frame lacks SOI",
			fragments: [][]byte{{1, 2}, {0xff, 0xd9}, {0xff, 0xd8, 0xff, 0xd9}},
			frames:    2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := collectCodestreams(encapsulatedPixel(test.fragments, nil), nil, test.frames)
			if !errors.Is(err, ErrLayout) {
				t.Fatalf("error = %v, want fragment layout error", err)
			}
		})
	}
}

func TestAssembleFrameCodestreamsRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name   string
		pixel  pixeldata.PixelData
		frames int
	}{
		{name: "not encapsulated", pixel: pixeldata.PixelData{}, frames: 1},
		{name: "zero frames", pixel: encapsulatedPixel([][]byte{{1, 2}}, nil), frames: 0},
		{name: "no fragments", pixel: encapsulatedPixel(nil, nil), frames: 1},
		{name: "empty fragment", pixel: encapsulatedPixel([][]byte{{}}, nil), frames: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := collectCodestreams(test.pixel, nil, test.frames)
			if !errors.Is(err, ErrLayout) {
				t.Fatalf("error = %v, want fragment layout error", err)
			}
		})
	}
}

func TestAssembleFrameCodestreamsUsesExtendedOffsetTable(t *testing.T) {
	fragments := [][]byte{{1, 2, 3, 0}, {4, 5}}
	obj := object.New(nil)
	obj.Put(core.NewRawElement(tagExtendedOffsetTable, core.VROV, uint64Table(0, 12)))
	obj.Put(core.NewRawElement(tagExtendedOffsetTableLengths, core.VROV, uint64Table(3, 2)))
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), obj, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !bytes.Equal(frames[0], []byte{1, 2, 3}) || !bytes.Equal(frames[1], []byte{4, 5}) {
		t.Fatalf("frames = % x", frames)
	}
}

func TestAssembleFrameCodestreamsAcceptsTypedExtendedOffsetTables(t *testing.T) {
	fragments := [][]byte{{1, 2}, {3, 4}}
	obj := object.New(nil)
	obj.Put(core.Element{Header: core.ElementHeader{Tag: tagExtendedOffsetTable, VR: core.VROV}, Value: core.Uint64Value{0, 10}})
	obj.Put(core.Element{Header: core.ElementHeader{Tag: tagExtendedOffsetTableLengths, VR: core.VROV}, Value: core.Uint64Value{2, 2}})
	frames, err := collectCodestreams(encapsulatedPixel(fragments, nil), obj, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || &frames[0][0] != &fragments[0][0] {
		t.Fatal("typed Extended Offset Table did not preserve one-fragment views")
	}
}

func TestAssembleFrameCodestreamsRejectsMalformedExtendedOffsetTables(t *testing.T) {
	fragments := [][]byte{{1, 2, 3, 0}, {4, 5}}
	tests := []struct {
		name    string
		bot     []byte
		offsets []uint64
		lengths []uint64
		putEOT  bool
		putLens bool
	}{
		{name: "missing lengths", offsets: []uint64{0, 12}, putEOT: true},
		{name: "missing offsets", lengths: []uint64{3, 2}, putLens: true},
		{name: "basic table conflict", bot: basicOffsets(0, 12), offsets: []uint64{0, 12}, lengths: []uint64{3, 2}, putEOT: true, putLens: true},
		{name: "wrong count", offsets: []uint64{0}, lengths: []uint64{3}, putEOT: true, putLens: true},
		{name: "unaligned", offsets: []uint64{0, 11}, lengths: []uint64{3, 2}, putEOT: true, putLens: true},
		{name: "length exceeds fragment", offsets: []uint64{0, 12}, lengths: []uint64{5, 2}, putEOT: true, putLens: true},
		{name: "length omits too much", offsets: []uint64{0, 12}, lengths: []uint64{2, 2}, putEOT: true, putLens: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			obj := object.New(nil)
			if test.putEOT {
				obj.Put(core.NewRawElement(tagExtendedOffsetTable, core.VROV, uint64Table(test.offsets...)))
			}
			if test.putLens {
				obj.Put(core.NewRawElement(tagExtendedOffsetTableLengths, core.VROV, uint64Table(test.lengths...)))
			}
			_, err := collectCodestreams(encapsulatedPixel(fragments, test.bot), obj, 2)
			if !errors.Is(err, ErrLayout) {
				t.Fatalf("error = %v, want fragment layout error", err)
			}
		})
	}
}

func TestAssembleFrameCodestreamsRejectsExtendedOffsetsForFragmentedFrames(t *testing.T) {
	obj := object.New(nil)
	obj.Put(core.NewRawElement(tagExtendedOffsetTable, core.VROV, uint64Table(0, 20)))
	obj.Put(core.NewRawElement(tagExtendedOffsetTableLengths, core.VROV, uint64Table(4, 2)))
	_, err := collectCodestreams(
		encapsulatedPixel([][]byte{{1, 2}, {3, 4}, {5, 6}}, nil),
		obj,
		2,
	)
	if !errors.Is(err, ErrLayout) {
		t.Fatalf("error = %v, want fragment layout error", err)
	}
}

func TestAssembleFrameCodestreamsRejectsNonZeroEOTPadding(t *testing.T) {
	obj := object.New(nil)
	obj.Put(core.NewRawElement(tagExtendedOffsetTable, core.VROV, uint64Table(0)))
	obj.Put(core.NewRawElement(tagExtendedOffsetTableLengths, core.VROV, uint64Table(3)))
	_, err := collectCodestreams(encapsulatedPixel([][]byte{{1, 2, 3, 9}}, nil), obj, 1)
	if !errors.Is(err, ErrLayout) {
		t.Fatalf("error = %v, want fragment layout error", err)
	}
}

func TestAssembleFrameCodestreamsEnforcesLimits(t *testing.T) {
	pixel := encapsulatedPixel([][]byte{{1, 2}, {3, 4}}, nil)
	tests := []struct {
		name   string
		frames int
		limits Limits
	}{
		{name: "frames", frames: 2, limits: Limits{MaxFrames: 1, MaxFragments: 2, MaxBytes: 4}},
		{name: "fragments", frames: 2, limits: Limits{MaxFrames: 2, MaxFragments: 1, MaxBytes: 4}},
		{name: "bytes", frames: 2, limits: Limits{MaxFrames: 2, MaxFragments: 2, MaxBytes: 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := collectCodestreamsWithLimits(pixel, nil, test.frames, test.limits)
			if !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("error = %v, want resource limit error", err)
			}
		})
	}
}

func TestNextFragmentItemOffsetRejectsOverflow(t *testing.T) {
	_, err := nextFragmentItemOffset(math.MaxUint64-4, 2)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("error = %v, want resource limit error", err)
	}
}

func encapsulatedPixel(fragments [][]byte, basicOffsetTable []byte) pixeldata.PixelData {
	return pixeldata.PixelData{
		Encapsulated: true,
		Sequence: core.FragmentSequence{
			OffsetTable: basicOffsetTable,
			Fragments:   fragments,
		},
	}
}

func basicOffsets(offsets ...uint32) []byte {
	table := make([]byte, len(offsets)*4)
	for i, offset := range offsets {
		binary.LittleEndian.PutUint32(table[i*4:], offset)
	}
	return table
}

func uint64Table(values ...uint64) []byte {
	table := make([]byte, len(values)*8)
	for i, value := range values {
		binary.LittleEndian.PutUint64(table[i*8:], value)
	}
	return table
}

// Preserve the original JPEG-LS offset regression cases against the extracted
// implementation. Production decoders read one Plan frame at a time.
func collectCodestreams(pixel pixeldata.PixelData, obj *object.Object, frames int) ([][]byte, error) {
	return collectCodestreamsWithLimits(pixel, obj, frames, Limits{})
}
func collectCodestreamsWithLimits(pixel pixeldata.PixelData, obj *object.Object, frames int, limits Limits) ([][]byte, error) {
	if !pixel.Encapsulated {
		return nil, ErrLayout
	}
	p, err := FromFragments(context.Background(), pixel.Sequence, obj, frames, JPEGLS, limits)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, p.Len())
	for i := range out {
		v, err := p.Frame(context.Background(), i)
		if err != nil {
			return nil, err
		}
		out[i] = v.Data
	}
	return out, nil
}
