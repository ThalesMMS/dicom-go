package parser

import (
	"errors"
	"io"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrUnsupportedUndefinedLength = errors.New("dicom: undefined length values are not supported by this parser")
var ErrNonZeroReservedBytes = errors.New("dicom: explicit VR long-form reserved bytes must be zero")
var ErrUnexpectedSequenceControlTag = errors.New("dicom: unexpected sequence control tag")
var ErrUnexpectedDelimiterLength = errors.New("dicom: delimiter length must be zero")
var ErrMissingBasicOffsetTable = errors.New("dicom: encapsulated Pixel Data is missing the Basic Offset Table item")

var (
	tagFloatPixelData       = core.NewTag(0x7FE0, 0x0008)
	tagDoubleFloatPixelData = core.NewTag(0x7FE0, 0x0009)
	tagWaveformData         = core.NewTag(0x5400, 0x1010)
	tagVectorGridData       = core.NewTag(0x0064, 0x0009)
)

const (
	definedValueReadChunkSize   = 32 << 10
	smallValueArenaLimit        = 256
	smallValueArenaInitialBytes = 256
	smallValueArenaBlockBytes   = 4 << 10
)

type OddLengthPolicy uint8

const (
	RejectOddLength OddLengthPolicy = iota
	AcceptOddLength
)

type seqTokenType uint8

const (
	seqTokenTypeSequence seqTokenType = iota
	seqTokenTypePixelSequence
	seqTokenTypeItem
)

type seqToken struct {
	typ                    seqTokenType
	length                 core.Length
	baseOffset             uint64
	implicitVRLittleEndian bool
	privateScope           *dictionary.PrivateReservations
}

type ReaderOptions struct {
	Dictionary dictionary.DataDictionary
	// Creator resolution is enabled only when Dictionary includes a PrivateDataDictionary.
	// Limits default to 4096 reservations per dataset/item and 128 diagnostics.
	MaxPrivateCreators           int
	MaxPrivateDiagnostics        int
	RejectInvalidPrivateCreators bool
	// MaxElementBytes limits a single defined-length element value allocation in
	// bytes. Use it to reject suspiciously large values before allocating
	// memory. A zero value means unlimited.
	MaxElementBytes int64
	// InlineValueBytesThreshold controls when the reader will materialize a
	// defined-length primitive value into memory.
	//
	// If InlineValueBytesThreshold is > 0 and a primitive element has a defined
	// length strictly greater than this threshold, the reader will not allocate a
	// value buffer and will instead return an element token with a nil Value.
	//
	// A zero value preserves the historical behavior of always materializing
	// defined-length primitive values (subject to MaxElementBytes).
	InlineValueBytesThreshold int64
	// MaxPixelDataBytes limits native Pixel Data values and the cumulative bytes
	// of an encapsulated Pixel Data value, including its Basic Offset Table. When
	// set, this limit replaces MaxElementBytes for Pixel Data value tags. A zero
	// value leaves Pixel Data subject to MaxElementBytes.
	MaxPixelDataBytes int64
	// MaxDeformableVectorGridBytes limits each Vector Grid Data (0064,0009)
	// value before allocation, including values nested in sequences. A zero
	// value leaves the element subject to MaxElementBytes.
	MaxDeformableVectorGridBytes int64
	// MaxTotalBytes limits the total number of bytes consumed from the source.
	// Use it to bound parser work on untrusted input. A zero value means
	// unlimited.
	MaxTotalBytes int64
	BaseOffset    int64
	// MaxSequenceDepth limits the combined nesting depth of sequence and item
	// frames tracked in seqDelimiters. The count includes both sequence and
	// item starts. Use it to guard against maliciously deep nesting. A zero
	// value means unlimited.
	MaxSequenceDepth int
	// MaxElements limits the number of primitive elements returned by the
	// reader. Use it to bound work on very large or malformed data sets. A zero
	// value means unlimited.
	MaxElements int
	// MaxFragments limits encapsulated Pixel Data fragments, excluding the
	// Basic Offset Table. Use it to guard against pathological fragment counts.
	// A zero value means unlimited.
	MaxFragments        int
	StrictReservedBytes bool
	OddLengthPolicy     OddLengthPolicy
	// SkipPixelData consumes integer, Float, and Double Float Pixel Data payload
	// bytes without materializing them. The returned element keeps its header and
	// has a nil Value.
	SkipPixelData bool
	// DeferPixelData consumes integer, Float, and Double Float Pixel Data payload
	// bytes without materializing them, while preserving enough stream position
	// information for object-level APIs to replay the original value from a
	// seekable source.
	DeferPixelData bool
	// DeferWaveformData consumes Waveform Data (5400,1010) payload bytes without
	// materializing them, including occurrences nested inside Waveform Sequence
	// items. Recorded ValueLocations preserve occurrence order and the nearest
	// enclosing item offset so multiplex groups can be bound without relying on
	// tag order alone. The source must be seekable for callers to replay them.
	DeferWaveformData bool
	// FrameSink receives native Pixel Data frames as they are read. When set,
	// native Pixel Data is split and emitted without materializing the complete
	// Pixel Data value.
	FrameSink FrameSink
	// EncapsulatedSink consumes top-level encapsulated Items for bounded assembly.
	// Without DeferPixelData the result contains core.DiscardedValue.
	EncapsulatedSink EncapsulatedSink
}

// ValueLocation identifies raw encoded value bytes in the reader's seekable
// source. Offsets use the same absolute coordinate space as Reader.Position.
type ValueLocation struct {
	Tag         core.Tag
	ValueOffset int64
	Length      int64
	// ItemOffset identifies the nearest enclosing sequence Item tag when the
	// value was nested in a sequence. It lets deferred callers bind repeated
	// tags to their parsed item instead of relying on global occurrence order.
	ItemOffset    int64
	ItemOffsetSet bool
}

type Reader struct {
	privateRoot                               *dictionary.PrivateReservations
	privateInitError                          error
	maxPrivateCreators, maxPrivateDiagnostics int
	rejectInvalidPrivateCreators              bool
	privateDiagnostics                        []PrivateDiagnostic
	privateDiagnosticsTruncated               bool
	counter                                   *countingReader
	syntax                                    transfer.Syntax
	dec                                       dicomenc.BasicDecoder
	dict                                      dictionary.DataDictionary
	maxElementBytes                           int64
	maxPixelDataBytes                         int64
	maxVectorGridBytes                        int64
	inlineThreshold                           int64
	maxTotalBytes                             int64
	maxSequenceDepth                          int
	maxElements                               int
	maxFragments                              int
	elementCount                              int
	fragmentCount                             int
	pixelDataBytes                            int64
	strictReservedBytes                       bool
	oddLengthPolicy                           OddLengthPolicy
	skipPixelData                             bool
	deferPixelData                            bool
	deferWaveformData                         bool
	frameSink                                 FrameSink
	frameSinkClosed                           bool
	encapsulatedSink                          EncapsulatedSink
	encapsulatedSinkClosed                    bool
	encodedPixelActive, encodedPixelSeen      bool
	// stopBeforePixelData is used only by the transfer syntax probe. It returns
	// the Pixel Data header as a terminal token without consuming or inspecting
	// payload bytes. Ordinary Reader APIs leave it false.
	stopBeforePixelData bool
	frameMetadata       frameMetadataState
	// seqDelimiters tracks the combined stack of open sequence and item frames
	// used for depth enforcement and defined-length boundary closure.
	seqDelimiters                   []seqToken
	delimiterCheckPending           bool
	pixelSequenceOffsetTablePending bool

	baseOffset int64
	rootOffset int64
	// sourceEnd is the stream-coordinate position of the physical end of a
	// seekable source, or 0 when the source size is unknown. A defined value
	// length that fits before sourceEnd cannot be a forged huge VL, so it is
	// safe to allocate in a single step.
	sourceEnd int64
	// slurpEligible marks a full-materialization pass over a sized source,
	// where the remaining dataset region may be read into a single buffer and
	// defined-length values returned as sub-slices of it. Like the small-value
	// arena, aliased values share backing memory and are treated as immutable;
	// the dataset buffer stays reachable while any aliased value is alive.
	slurpEligible bool

	valueLocations    map[core.Tag]ValueLocation
	allValueLocations map[core.Tag][]ValueLocation
	// valueLocationGeneration is zero during the initial ordered parse. An
	// explicit rewind advances it and enables seenValueLocations so replayed
	// locations are merged without charging the common first pass for hashing.
	valueLocationGeneration uint64
	seenValueLocations      map[ValueLocation]struct{}
	ambiguousValueLocations map[core.Tag]bool
	// smallValueArena is the current append-only block for primitive values up
	// to smallValueArenaLimit. Returned RawValue slices occupy disjoint regions;
	// a full block is abandoned, never overwritten, so earlier tokens remain
	// valid for their normal lifetime.
	smallValueArena []byte

	validationLifecycle  *readerValidationLifecycle
	selective            *selectiveReaderState
	validationSuppressed int
	lastReservedOffset   int64
	lastReservedNonZero  bool
}

func NewReader(r io.Reader, syntax transfer.Syntax, opts ReaderOptions) *Reader {
	cr := &countingReader{r: r, pos: opts.BaseOffset, maxTotalBytes: opts.MaxTotalBytes}
	rootOffset := int64(0)
	sourceEnd := int64(0)
	if seeker, ok := r.(io.Seeker); ok {
		if pos, err := seeker.Seek(0, io.SeekCurrent); err == nil {
			rootOffset = pos
			if size, ok := sourcePhysicalSize(r); ok && size >= pos {
				sourceEnd = opts.BaseOffset + (size - pos)
			}
		}
	}
	slurpEligible := sourceEnd > 0 &&
		opts.FrameSink == nil && opts.EncapsulatedSink == nil &&
		!opts.SkipPixelData &&
		!opts.DeferPixelData &&
		!opts.DeferWaveformData &&
		opts.InlineValueBytesThreshold == 0 &&
		opts.MaxElementBytes == 0 &&
		opts.MaxPixelDataBytes == 0 &&
		opts.MaxDeformableVectorGridBytes == 0 &&
		opts.MaxTotalBytes == 0
	reader := &Reader{
		maxPrivateCreators:           opts.MaxPrivateCreators,
		maxPrivateDiagnostics:        opts.MaxPrivateDiagnostics,
		rejectInvalidPrivateCreators: opts.RejectInvalidPrivateCreators,
		counter:                      cr,
		syntax:                       syntax,
		dec:                          dicomenc.NewBasicDecoder(syntax.ByteOrder),
		dict:                         opts.Dictionary,
		maxElementBytes:              opts.MaxElementBytes,
		maxPixelDataBytes:            opts.MaxPixelDataBytes,
		maxVectorGridBytes:           opts.MaxDeformableVectorGridBytes,
		inlineThreshold:              opts.InlineValueBytesThreshold,
		maxTotalBytes:                opts.MaxTotalBytes,
		maxSequenceDepth:             opts.MaxSequenceDepth,
		maxElements:                  opts.MaxElements,
		maxFragments:                 opts.MaxFragments,
		strictReservedBytes:          opts.StrictReservedBytes,
		oddLengthPolicy:              opts.OddLengthPolicy,
		skipPixelData:                opts.SkipPixelData,
		deferPixelData:               opts.DeferPixelData,
		deferWaveformData:            opts.DeferWaveformData,
		frameSink:                    opts.FrameSink,
		encapsulatedSink:             opts.EncapsulatedSink,
		seqDelimiters:                make([]seqToken, 0),
		baseOffset:                   opts.BaseOffset,
		rootOffset:                   rootOffset,
		sourceEnd:                    sourceEnd,
		slurpEligible:                slurpEligible,
	}
	reader.resetPrivateScopes()
	return reader
}

// sliceSource serves the parser from an in-memory copy of the dataset region
// and can hand out zero-copy sub-slices for defined-length values.
type sliceSource struct {
	buf []byte
	off int
	// err is surfaced once buf is exhausted, preserving the original read
	// error of a source that ended early; nil means a clean io.EOF.
	err error
}

func (s *sliceSource) Read(p []byte) (int, error) {
	if s.off >= len(s.buf) {
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.EOF
	}
	n := copy(p, s.buf[s.off:])
	s.off += n
	return n, nil
}

// take returns the next n bytes as an alias of the underlying buffer, or
// false without consuming anything when fewer than n bytes remain.
func (s *sliceSource) take(n int) ([]byte, bool) {
	if n < 0 || n > len(s.buf)-s.off {
		return nil, false
	}
	data := s.buf[s.off : s.off+n : s.off+n]
	s.off += n
	return data, true
}

// ensureSlurp reads the remaining dataset region of a sized source into a
// single buffer and swaps it in as the reader's byte source. It runs at most
// once; on a short read the partial buffer is used and the original error is
// surfaced at the position the source actually ended, so downstream error
// behavior matches a plain streaming read.
func (r *Reader) ensureSlurp() {
	if !r.slurpEligible {
		return
	}
	r.slurpEligible = false
	remaining := r.sourceEnd - r.Position()
	if remaining <= 0 || remaining > int64(^uint(0)>>1) {
		return
	}
	buf := make([]byte, int(remaining))
	n, err := io.ReadFull(r.counter.r, buf)
	src := &sliceSource{buf: buf[:n]}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		src.err = err
	}
	r.counter.r = src
}

// sourcePhysicalSize reports the total size of a source whose physical end is
// knowable without disturbing its read position. It covers regular files and
// in-memory readers that expose a Size method, such as bytes.Reader and
// io.SectionReader.
func sourcePhysicalSize(r io.Reader) (int64, bool) {
	switch src := r.(type) {
	case *os.File:
		info, err := src.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return 0, false
		}
		return info.Size(), true
	case interface{ Size() int64 }:
		return src.Size(), true
	}
	return 0, false
}

type countingReader struct {
	r             io.Reader
	pos           int64
	maxTotalBytes int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.maxTotalBytes > 0 {
		remaining := r.maxTotalBytes - r.pos
		if remaining <= 0 {
			return 0, ErrMaxTotalBytesExceeded
		}
		if int64(len(p)) > remaining {
			p = p[:remaining]
			n, err := r.r.Read(p)
			r.pos += int64(n)
			if err != nil {
				return n, err
			}
			if int64(n) == remaining {
				return n, ErrMaxTotalBytesExceeded
			}
			return n, nil
		}
	}
	n, err := r.r.Read(p)
	r.pos += int64(n)
	return n, err
}

func (r *countingReader) Position() int64 {
	if r == nil {
		return 0
	}
	return r.pos
}

func (r *Reader) Position() int64 {
	if r == nil {
		return 0
	}
	return r.counter.Position()
}
