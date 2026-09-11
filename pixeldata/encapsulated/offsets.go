package encapsulated

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	tagExtendedOffsetTable        = core.NewTag(0x7FE0, 0x0001)
	tagExtendedOffsetTableLengths = core.NewTag(0x7FE0, 0x0002)
)

// Tables retains the distinction between an absent and an empty EOT. Offsets
// are measured from the first Item Tag after the BOT, including Item headers.
type Tables struct {
	Basic                           []byte
	Extended, Lengths               []uint64
	ExtendedPresent, LengthsPresent bool
}

// FromFragments plans already parsed, materialized Pixel Data without copying
// its payload. The object supplies only the optional EOT attributes.
func FromFragments(ctx context.Context, sequence core.FragmentSequence, obj *object.Object, frames int, format Format, limits Limits) (*Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if frames <= 0 {
		return nil, ErrLayout
	}
	if frames > l.MaxFrames || len(sequence.Fragments) > l.MaxFragments {
		return nil, ErrResourceLimit
	}
	tables, err := ReadTables(obj, sequence.OffsetTable, frames)
	if err != nil {
		return nil, err
	}
	return New(ctx, Memory(sequence.Fragments), tables, frames, format, l)
}

// ReadTables reads the existing object model; it does not reparse a dataset.
func ReadTables(obj *object.Object, basic []byte, frames int) (Tables, error) {
	var t Tables
	var err error
	t.Basic = basic
	t.Extended, t.ExtendedPresent, err = uint64OffsetTable(obj, tagExtendedOffsetTable, frames)
	if err != nil {
		return Tables{}, err
	}
	t.Lengths, t.LengthsPresent, err = uint64OffsetTable(obj, tagExtendedOffsetTableLengths, frames)
	return t, err
}

func nextFragmentItemOffset(current uint64, fragmentLength int) (uint64, error) {
	if fragmentLength < 0 {
		return 0, fmt.Errorf("%w: negative fragment length", ErrLayout)
	}
	padded := uint64(fragmentLength)
	if padded&1 != 0 {
		padded++
	}
	if current > math.MaxUint64-8 || padded > math.MaxUint64-current-8 {
		return 0, fmt.Errorf("%w: fragment item offset overflow", ErrResourceLimit)
	}
	return current + 8 + padded, nil
}

func basicFrameStarts(table []byte, fragmentStarts []uint64, numberOfFrames int) ([]int, error) {
	if numberOfFrames > math.MaxInt/4 || len(table) != numberOfFrames*4 {
		return nil, fmt.Errorf("%w: Basic Offset Table length=%d frames=%d", ErrLayout, len(table), numberOfFrames)
	}
	offsets := make([]uint64, numberOfFrames)
	for i := range offsets {
		offsets[i] = uint64(binary.LittleEndian.Uint32(table[i*4:]))
	}
	return mapFrameOffsets(offsets, fragmentStarts)
}

func mapFrameOffsets(offsets, fragmentStarts []uint64) ([]int, error) {
	if err := validateOffsetOrder(offsets); err != nil {
		return nil, err
	}
	frameStarts := make([]int, len(offsets))
	for i, offset := range offsets {
		fragmentIndex := sort.Search(len(fragmentStarts), func(j int) bool { return fragmentStarts[j] >= offset })
		if fragmentIndex == len(fragmentStarts) || fragmentStarts[fragmentIndex] != offset {
			return nil, fmt.Errorf("%w: frame offset %d is not aligned to a fragment", ErrLayout, offset)
		}
		frameStarts[i] = fragmentIndex
	}
	return frameStarts, nil
}

func validateOffsetOrder(offsets []uint64) error {
	if len(offsets) == 0 || offsets[0] != 0 {
		return fmt.Errorf("%w: first frame offset is not zero", ErrLayout)
	}
	for i, offset := range offsets {
		if i > 0 && offset <= offsets[i-1] {
			return fmt.Errorf("%w: frame offsets are not strictly increasing at index %d", ErrLayout, i)
		}
	}
	return nil
}

func uint64OffsetTable(obj *object.Object, tag core.Tag, expectedEntries int) ([]uint64, bool, error) {
	if obj == nil {
		return nil, false, nil
	}
	element, ok := obj.Get(tag)
	if !ok {
		return nil, false, nil
	}
	if element.Header.VR != core.VROV {
		return nil, true, fmt.Errorf("%w: EOT attribute must use OV", ErrLayout)
	}
	switch value := element.Value.(type) {
	case core.RawValue:
		if expectedEntries > math.MaxInt/8 || len(value) != expectedEntries*8 {
			return nil, true, fmt.Errorf("%w: %s length=%d frames=%d", ErrLayout, tag, len(value), expectedEntries)
		}
		entries := make([]uint64, len(value)/8)
		for i := range entries {
			entries[i] = binary.LittleEndian.Uint64(value[i*8:])
		}
		return entries, true, nil
	case core.Uint64Value:
		if len(value) != expectedEntries {
			return nil, true, fmt.Errorf("%w: %s entries=%d frames=%d", ErrLayout, tag, len(value), expectedEntries)
		}
		return append([]uint64(nil), value...), true, nil
	default:
		return nil, true, fmt.Errorf("%w: %s has unsupported value type %T", ErrLayout, tag, element.Value)
	}
}
