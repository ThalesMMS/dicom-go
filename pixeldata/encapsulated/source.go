// Package encapsulated resolves DICOM JPEG and JPEG-LS Items into frames.
// It consumes already parsed Items; it does not parse datasets or scan for
// DICOM Sequence Delimitation bytes inside opaque payloads.
package encapsulated

import (
	"context"
	"fmt"
	"io"
	"math"
)

// Source provides stable fragment sizes and random reads. Implementations must
// keep the source alive and unchanged for the lifetime of a Plan and its views.
// Reads must honor ctx; a blocking underlying ReaderAt cannot be interrupted.
// Concurrent use requires a concurrency-safe source. The Plan never closes it.
type Source interface {
	FragmentCount() int
	FragmentSize(index int) (uint64, error)
	ReadFragmentAt(ctx context.Context, index int, p []byte, offset int64) (int, error)
}

// Borrower optionally exposes immutable memory. The returned bytes may not be
// mutated by decoders or callers; they remain owned by the source.
type Borrower interface {
	BorrowFragment(index int) ([]byte, bool)
}

type memorySource struct{ fragments [][]byte }

// Memory borrows already materialized fragments. An odd-length final fragment
// may omit its as-yet-unencoded DICOM pad; Item offsets include that pad. Odd
// fragments within a fragmented frame are rejected. No input bytes are copied.
func Memory(fragments [][]byte) Source { return &memorySource{fragments: fragments} }

func (s *memorySource) FragmentCount() int { return len(s.fragments) }
func (s *memorySource) FragmentSize(i int) (uint64, error) {
	if i < 0 || i >= len(s.fragments) {
		return 0, fmt.Errorf("%w: fragment index", ErrLayout)
	}
	return uint64(len(s.fragments[i])), nil
}
func (s *memorySource) BorrowFragment(i int) ([]byte, bool) {
	if i < 0 || i >= len(s.fragments) {
		return nil, false
	}
	return s.fragments[i], true
}
func (s *memorySource) ReadFragmentAt(ctx context.Context, i int, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if i < 0 || i >= len(s.fragments) || offset < 0 {
		return 0, ErrLayout
	}
	data := s.fragments[i]
	if offset >= int64(len(data)) {
		return 0, io.EOF
	}
	n := copy(p, data[offset:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// ItemRange locates an already parsed Item Value in an io.ReaderAt source.
// Offset excludes the eight-byte Item header; Length includes on-wire padding.
type ItemRange struct {
	Offset int64
	Length uint64
}

type readerAtSource struct {
	reader io.ReaderAt
	items  []ItemRange
}

// ReaderAt creates a deferred source without reading any payload. Ranges must
// come from the caller's existing parser/index and describe even, nonempty
// on-wire Item Values in source order. The ranges slice is borrowed and must
// remain unchanged. Size/count budgets are enforced by New before index allocation.
func ReaderAt(reader io.ReaderAt, items []ItemRange) (Source, error) {
	if reader == nil {
		return nil, fmt.Errorf("%w: nil ReaderAt", ErrLayout)
	}
	var previousEnd int64
	for i, item := range items {
		if item.Offset < 0 || item.Length < 2 || item.Length&1 != 0 || item.Length > math.MaxUint32-1 || uint64(item.Offset) > math.MaxInt64-item.Length {
			return nil, fmt.Errorf("%w: invalid Item range %d", ErrLayout, i)
		}
		if i > 0 && item.Offset < previousEnd {
			return nil, fmt.Errorf("%w: overlapping Item ranges", ErrLayout)
		}
		previousEnd = item.Offset + int64(item.Length)
	}
	return &readerAtSource{reader: reader, items: items[:len(items):len(items)]}, nil
}
func (s *readerAtSource) FragmentCount() int { return len(s.items) }
func (s *readerAtSource) FragmentSize(i int) (uint64, error) {
	if i < 0 || i >= len(s.items) {
		return 0, ErrLayout
	}
	return s.items[i].Length, nil
}
func (s *readerAtSource) ReadFragmentAt(ctx context.Context, i int, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if i < 0 || i >= len(s.items) || offset < 0 {
		return 0, ErrLayout
	}
	item := s.items[i]
	if uint64(offset) >= item.Length {
		return 0, io.EOF
	}
	available := item.Length - uint64(offset)
	want := len(p)
	if uint64(want) > available {
		p = p[:int(available)]
	}
	n, err := s.reader.ReadAt(p, item.Offset+offset)
	if err == nil && n < want {
		err = io.EOF
	}
	return n, err
}
