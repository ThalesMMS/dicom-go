package encapsulated

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
)

var (
	ErrLayout        = errors.New("dicom: invalid encapsulated frame layout")
	ErrFrameCount    = errors.New("dicom: encapsulated frame count mismatch")
	ErrResourceLimit = errors.New("dicom: encapsulated frame resource limit exceeded")
	ErrSource        = errors.New("dicom: encapsulated fragment source failed")
)

// Format identifies the permitted codestream boundary rules. These formats
// allow multiple Items per frame and EOT only with one Item per frame. RLE,
// Encapsulated Uncompressed, video and other syntaxes are not implied by this API.
type Format uint8

const (
	JPEG Format = iota + 1
	JPEGLS
)

// Limits bounds metadata, aggregate input and each individual frame before
// payload allocation. Zero fields select Defaults; negative counts are invalid.
type Limits struct {
	MaxFrames, MaxFragments, MaxFragmentsPerFrame int
	MaxBytes, MaxFrameBytes                       uint64
}

func Defaults() Limits {
	return Limits{MaxFrames: 100_000, MaxFragments: 100_000, MaxFragmentsPerFrame: 100_000, MaxBytes: 512 << 20, MaxFrameBytes: 512 << 20}
}
func (l Limits) normalized() (Limits, error) {
	if l.MaxFrames < 0 || l.MaxFragments < 0 || l.MaxFragmentsPerFrame < 0 {
		return l, ErrResourceLimit
	}
	d := Defaults()
	if l.MaxFrames == 0 {
		l.MaxFrames = d.MaxFrames
	}
	if l.MaxFragments == 0 {
		l.MaxFragments = d.MaxFragments
	}
	if l.MaxFragmentsPerFrame == 0 {
		l.MaxFragmentsPerFrame = d.MaxFragmentsPerFrame
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxFrameBytes == 0 {
		l.MaxFrameBytes = d.MaxFrameBytes
	}
	return l, nil
}

type frameRange struct {
	start, end int
	length     uint64
}

// Plan stores Item ranges, not concatenated Pixel Data. Its O(Items+Frames)
// metadata is immutable after New. Frame reads allocate at most one compressed
// frame each; retaining multiple returned buffers is the caller's responsibility.
type Plan struct {
	source        Source
	sizes         []uint64
	frames        []frameRange
	inputBytes    uint64
	maxFrameBytes uint64
}

// View is one encoded frame. Borrowed means Data is a read-only source view;
// otherwise the caller owns Data. No buffer is recycled on the next Frame call.
type View struct {
	Data     []byte
	Borrowed bool
}

func (p *Plan) Len() int           { return len(p.frames) }
func (p *Plan) InputBytes() uint64 { return p.inputBytes }

// MaxFrameBytes is the largest compressed frame buffer a read can allocate.
func (p *Plan) MaxFrameBytes() uint64 { return p.maxFrameBytes }

// New validates offset tables and budgets, then determines complete Item ranges.
// Populated BOT/EOT or an unambiguous one-frame/one-Item layout need no payload
// scan (except EOT padding). Otherwise a bounded JPEG marker scanner skips whole
// segment payloads and recognizes EOI across Item boundaries. Pixel decoding
// and validation of the codec's image/scan parameters remain the codec's job.
func New(ctx context.Context, source Source, tables Tables, frames int, format Format, limits Limits) (*Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if format != JPEG && format != JPEGLS {
		return nil, fmt.Errorf("%w: unsupported frame format", ErrLayout)
	}
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if source == nil || frames <= 0 {
		return nil, fmt.Errorf("%w: invalid source or frame count", ErrLayout)
	}
	n := source.FragmentCount()
	if frames > l.MaxFrames || n > l.MaxFragments {
		return nil, ErrResourceLimit
	}
	if n <= 0 {
		return nil, fmt.Errorf("%w: no fragments", ErrLayout)
	}
	if frames > n {
		return nil, fmt.Errorf("%w: %w", ErrLayout, ErrFrameCount)
	}
	p := &Plan{source: source, sizes: make([]uint64, n)}
	starts := make([]uint64, n)
	var itemOffset uint64
	for i := range p.sizes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size, err := source.FragmentSize(i)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSource, err)
		}
		if size == 0 {
			return nil, fmt.Errorf("%w: empty fragment", ErrLayout)
		}
		if size > uint64(math.MaxInt) || size > l.MaxBytes-p.inputBytes {
			return nil, ErrResourceLimit
		}
		p.sizes[i], starts[i] = size, itemOffset
		p.inputBytes += size
		itemOffset, err = nextFragmentItemOffset(itemOffset, int(size))
		if err != nil {
			return nil, err
		}
	}
	if tables.ExtendedPresent != tables.LengthsPresent {
		return nil, fmt.Errorf("%w: EOT and Lengths must both be present", ErrLayout)
	}
	if !tables.ExtendedPresent && (len(tables.Extended) != 0 || len(tables.Lengths) != 0) {
		return nil, fmt.Errorf("%w: EOT presence inconsistent", ErrLayout)
	}
	var frameStarts []int
	if tables.ExtendedPresent {
		if len(tables.Basic) != 0 || n != frames || len(tables.Extended) != frames || len(tables.Lengths) != frames {
			return nil, fmt.Errorf("%w: EOT requires empty BOT and one Item per frame with matching counts", ErrLayout)
		}
		frameStarts, err = mapFrameOffsets(tables.Extended, starts)
	} else if len(tables.Basic) != 0 {
		frameStarts, err = basicFrameStarts(tables.Basic, starts, frames)
	} else if n == frames {
		frameStarts = make([]int, frames)
		for i := range frameStarts {
			frameStarts[i] = i
		}
	} else {
		frameStarts, err = inferFrames(ctx, p, frames, format, l)
	}
	if err != nil {
		return nil, err
	}
	p.frames = make([]frameRange, frames)
	for i, start := range frameStarts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := n
		if i+1 < frames {
			end = frameStarts[i+1]
		}
		if start < 0 || start >= end || end > n {
			return nil, ErrLayout
		}
		if end-start > l.MaxFragmentsPerFrame {
			return nil, ErrResourceLimit
		}
		var size uint64
		for j := start; j < end; j++ {
			if j+1 < end && p.sizes[j]&1 != 0 {
				return nil, fmt.Errorf("%w: odd non-final fragment", ErrLayout)
			}
			if p.sizes[j] > l.MaxFrameBytes-size {
				return nil, ErrResourceLimit
			}
			size += p.sizes[j]
		}
		if size > uint64(math.MaxInt) {
			return nil, ErrResourceLimit
		}
		if tables.ExtendedPresent {
			length := tables.Lengths[i]
			if start != i || length == 0 || length > size || size-length > 1 || (size != length && length&1 == 0) {
				return nil, fmt.Errorf("%w: invalid EOT frame length", ErrLayout)
			}
			if size != length {
				var padding [1]byte
				if err := readExact(ctx, source, start, padding[:], int64(length)); err != nil {
					return nil, err
				}
				if padding[0] != 0 {
					return nil, fmt.Errorf("%w: nonzero EOT padding", ErrLayout)
				}
			}
			size = length
		}
		p.frames[i] = frameRange{start: start, end: end, length: size}
		p.maxFrameBytes = max(p.maxFrameBytes, size)
	}
	return p, nil
}

// Frame reads only the requested range. One in-memory Item is returned as a
// borrowed, capacity-limited view; multiple Items or deferred data use one owned
// buffer. Cancellation/source errors return no partial frame.
func (p *Plan) Frame(ctx context.Context, index int) (View, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	if p == nil || index < 0 || index >= len(p.frames) {
		return View{}, ErrLayout
	}
	f := p.frames[index]
	if f.end == f.start+1 {
		if source, ok := p.source.(Borrower); ok {
			if data, ok := source.BorrowFragment(f.start); ok {
				if uint64(len(data)) != p.sizes[f.start] {
					return View{}, fmt.Errorf("%w: source size changed", ErrSource)
				}
				return View{Data: data[:int(f.length):int(f.length)], Borrowed: true}, nil
			}
		}
	}
	data := make([]byte, int(f.length))
	pos := 0
	for i := f.start; i < f.end; i++ {
		size := p.sizes[i]
		if size > uint64(len(data)-pos) {
			size = uint64(len(data) - pos)
		}
		for offset := uint64(0); offset < size; {
			chunk := min(uint64(64<<10), size-offset)
			if err := readExact(ctx, p.source, i, data[pos:pos+int(chunk)], int64(offset)); err != nil {
				return View{}, err
			}
			pos += int(chunk)
			offset += chunk
		}
	}
	return View{Data: data}, nil
}

func readExact(ctx context.Context, source Source, index int, data []byte, offset int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := source.ReadFragmentAt(ctx, index, data, offset)
	if n < 0 || n > len(data) {
		return fmt.Errorf("%w: invalid read count", ErrSource)
	}
	if n == len(data) && (err == nil || errors.Is(err, io.EOF)) {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("%w: fragment %d: %w", ErrSource, index, err)
}
