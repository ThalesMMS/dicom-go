package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
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
}

type ReaderOptions struct {
	Dictionary dictionary.DataDictionary
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
	counter             *countingReader
	syntax              transfer.Syntax
	dec                 dicomenc.BasicDecoder
	dict                dictionary.DataDictionary
	maxElementBytes     int64
	inlineThreshold     int64
	maxTotalBytes       int64
	maxSequenceDepth    int
	maxElements         int
	maxFragments        int
	elementCount        int
	fragmentCount       int
	strictReservedBytes bool
	oddLengthPolicy     OddLengthPolicy
	skipPixelData       bool
	deferPixelData      bool
	deferWaveformData   bool
	frameSink           FrameSink
	frameSinkClosed     bool
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

	valueLocations          map[core.Tag]ValueLocation
	allValueLocations       map[core.Tag][]ValueLocation
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
		opts.FrameSink == nil &&
		!opts.SkipPixelData &&
		!opts.DeferPixelData &&
		!opts.DeferWaveformData &&
		opts.InlineValueBytesThreshold == 0 &&
		opts.MaxTotalBytes == 0
	return &Reader{
		counter:             cr,
		syntax:              syntax,
		dec:                 dicomenc.NewBasicDecoder(syntax.ByteOrder),
		dict:                opts.Dictionary,
		maxElementBytes:     opts.MaxElementBytes,
		inlineThreshold:     opts.InlineValueBytesThreshold,
		maxTotalBytes:       opts.MaxTotalBytes,
		maxSequenceDepth:    opts.MaxSequenceDepth,
		maxElements:         opts.MaxElements,
		maxFragments:        opts.MaxFragments,
		strictReservedBytes: opts.StrictReservedBytes,
		oddLengthPolicy:     opts.OddLengthPolicy,
		skipPixelData:       opts.SkipPixelData,
		deferPixelData:      opts.DeferPixelData,
		deferWaveformData:   opts.DeferWaveformData,
		frameSink:           opts.FrameSink,
		seqDelimiters:       make([]seqToken, 0),
		baseOffset:          opts.BaseOffset,
		rootOffset:          rootOffset,
		sourceEnd:           sourceEnd,
		slurpEligible:       slurpEligible,
	}
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

// ValueLocation returns the recorded raw value location for tag when the reader
// skipped or streamed that value during parsing. It returns false for duplicate
// recorded tags because tag-only lookup would be ambiguous.
func (r *Reader) ValueLocation(tag core.Tag) (ValueLocation, bool) {
	if r == nil || r.ambiguousValueLocations[tag] {
		return ValueLocation{}, false
	}
	location, ok := r.valueLocations[tag]
	return location, ok
}

// ValueLocations returns every recorded raw value location for tag in source
// order. The returned slice is a copy and can be mutated by the caller.
func (r *Reader) ValueLocations(tag core.Tag) []ValueLocation {
	if r == nil {
		return nil
	}
	return append([]ValueLocation(nil), r.allValueLocations[tag]...)
}

// CopyElementValueAt streams raw encoded bytes from a previously recorded value
// location without reparsing the data set. The Reader must have been created
// over an io.ReadSeeker.
func (r *Reader) CopyElementValueAt(location ValueLocation, w io.Writer) (int64, error) {
	if r == nil || r.counter == nil {
		return 0, fmt.Errorf("dicom: nil reader")
	}
	if location.Length < 0 {
		return 0, fmt.Errorf("dicom: invalid value location length %d", location.Length)
	}
	if location.ValueOffset < r.baseOffset {
		return 0, fmt.Errorf("dicom: invalid value location offset %d", location.ValueOffset)
	}
	rs, ok := r.counter.r.(io.ReadSeeker)
	if !ok {
		return 0, fmt.Errorf("dicom: underlying reader is not seekable")
	}
	currentSeek, _ := rs.Seek(0, io.SeekCurrent)
	currentPos := r.counter.pos
	defer func() {
		_, _ = rs.Seek(currentSeek, io.SeekStart)
		r.counter.pos = currentPos
	}()

	valueStartSeek := r.rootOffset + (location.ValueOffset - r.baseOffset)
	if _, err := rs.Seek(valueStartSeek, io.SeekStart); err != nil {
		return 0, err
	}
	copied, err := io.CopyN(w, rs, location.Length)
	if err != nil {
		return copied, err
	}
	return copied, nil
}

func (r *Reader) recordValueLocation(header core.ElementHeader, valueOffset, length int64) {
	if r == nil || length < 0 {
		return
	}
	location := ValueLocation{
		Tag:         header.Tag,
		ValueOffset: valueOffset,
		Length:      length,
	}
	for i := len(r.seqDelimiters) - 1; i >= 0; i-- {
		frame := r.seqDelimiters[i]
		if frame.typ != seqTokenTypeItem || frame.baseOffset < 8 {
			continue
		}
		location.ItemOffset = int64(frame.baseOffset - 8)
		location.ItemOffsetSet = true
		break
	}
	if r.allValueLocations == nil {
		r.allValueLocations = map[core.Tag][]ValueLocation{}
	}
	for _, existing := range r.allValueLocations[header.Tag] {
		if existing == location {
			return
		}
	}
	r.allValueLocations[header.Tag] = append(r.allValueLocations[header.Tag], location)
	if r.ambiguousValueLocations[header.Tag] {
		return
	}
	if r.valueLocations == nil {
		r.valueLocations = map[core.Tag]ValueLocation{}
	}
	if _, exists := r.valueLocations[header.Tag]; exists {
		delete(r.valueLocations, header.Tag)
		if r.ambiguousValueLocations == nil {
			r.ambiguousValueLocations = map[core.Tag]bool{}
		}
		r.ambiguousValueLocations[header.Tag] = true
		return
	}
	r.valueLocations[header.Tag] = location
}

// CopyElementValueTo streams the raw encoded bytes of the first occurrence of tag
// from a seekable source.
//
// Lifecycle & concurrency:
//
//   - CopyElementValueTo is not safe for concurrent use with other Reader methods.
//   - The Reader must have been created over an io.ReadSeeker.
//   - When a value location was recorded while parsing, the value is copied by
//     seeking directly to that offset. Otherwise, the Reader is rewound to its
//     rootOffset and reparsed; any prior parsing progress is discarded.
//
// It writes the matching element value to w without materializing it in memory.
// For undefined-length encapsulated Pixel Data, the copied value includes the
// Basic Offset Table, fragment items, and sequence delimitation item, but not the
// Pixel Data element header itself.
func (r *Reader) CopyElementValueTo(tag core.Tag, w io.Writer) (int64, error) {
	if r == nil || r.counter == nil {
		return 0, fmt.Errorf("dicom: nil reader")
	}
	if r.ambiguousValueLocations[tag] {
		return 0, fmt.Errorf("dicom: value location for element %s is ambiguous due to duplicate tags", tag)
	}
	if location, ok := r.ValueLocation(tag); ok {
		return r.CopyElementValueAt(location, w)
	}
	rs, ok := r.counter.r.(io.ReadSeeker)
	if !ok {
		return 0, fmt.Errorf("dicom: underlying reader is not seekable")
	}
	if _, err := rs.Seek(r.rootOffset, io.SeekStart); err != nil {
		return 0, err
	}
	// Reset parser state for a fresh scan.
	r.counter.pos = r.baseOffset
	r.seqDelimiters = r.seqDelimiters[:0]
	r.delimiterCheckPending = false
	r.pixelSequenceOffsetTablePending = false
	r.elementCount = 0
	r.fragmentCount = 0
	r.frameMetadata = frameMetadataState{}
	if r.validationLifecycle != nil {
		r.validationLifecycle.frames = nil
	}
	r.validationSuppressed++
	defer func() { r.validationSuppressed-- }()

	skipPixelData := r.skipPixelData
	if tag == core.TagPixelData {
		r.skipPixelData = true
		defer func() {
			r.skipPixelData = skipPixelData
		}()
	}

	for {
		okTok, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, fmt.Errorf("dicom: element %s not found", tag)
			}
			return 0, err
		}
		if okTok.Kind == TokenStartPixelSequence {
			if okTok.Header.Tag == tag {
				return r.copyPixelSequenceValueTo(okTok.Header, r.Position(), rs, w)
			}
			skipPixelData := r.skipPixelData
			r.skipPixelData = true
			err := r.discardFragmentSequence(okTok.Header)
			r.skipPixelData = skipPixelData
			if err != nil {
				return 0, err
			}
			continue
		}
		if okTok.Kind != TokenElement {
			continue
		}
		elem := okTok.Element
		if elem.Tag() != tag {
			continue
		}
		if elem.Header.Length == core.UndefinedLength {
			return 0, fmt.Errorf("dicom: element %s has undefined length", tag)
		}
		// Value has already been read/consumed by Next() at this point.
		// If it was materialized, we can copy from memory.
		if raw, ok := elem.RawBytes(); ok {
			return io.Copy(w, bytes.NewReader(raw))
		}
		// Otherwise, this element was parsed in skip-large-values mode. To stream it
		// without allocation, re-read it from the seekable source. At this point the
		// reader cursor is positioned immediately after the value.
		valueEnd := r.Position()
		valueStart := valueEnd - int64(elem.Header.Length)
		if valueStart < r.baseOffset {
			return 0, fmt.Errorf("dicom: invalid element %s value position", tag)
		}
		valueStartSeek := r.rootOffset + (valueStart - r.baseOffset)
		valueEndSeek := r.rootOffset + (valueEnd - r.baseOffset)
		if _, err := rs.Seek(valueStartSeek, io.SeekStart); err != nil {
			return 0, err
		}
		copied, err := io.CopyN(w, rs, int64(elem.Header.Length))
		if err != nil {
			return copied, err
		}
		// Restore cursor to the end of the value to keep the reader usable after this call.
		if _, err := rs.Seek(valueEndSeek, io.SeekStart); err != nil {
			return copied, err
		}
		r.counter.pos = valueEnd
		return copied, nil
	}
}

func (r *Reader) copyPixelSequenceValueTo(header core.ElementHeader, valueStart int64, rs io.ReadSeeker, w io.Writer) (int64, error) {
	skipPixelData := r.skipPixelData
	r.skipPixelData = true
	err := r.discardFragmentSequence(header)
	r.skipPixelData = skipPixelData
	if err != nil {
		return 0, err
	}

	valueEnd := r.Position()
	if valueEnd < valueStart {
		return 0, fmt.Errorf("dicom: invalid element %s value position", header.Tag)
	}
	valueStartSeek := r.rootOffset + (valueStart - r.baseOffset)
	valueEndSeek := r.rootOffset + (valueEnd - r.baseOffset)
	if _, err := rs.Seek(valueStartSeek, io.SeekStart); err != nil {
		return 0, err
	}
	copied, err := io.CopyN(w, rs, valueEnd-valueStart)
	if err != nil {
		return copied, err
	}
	if _, err := rs.Seek(valueEndSeek, io.SeekStart); err != nil {
		return copied, err
	}
	r.counter.pos = valueEnd
	return copied, nil
}

func (r *Reader) Next() (Token, error) {
	if r.selective != nil {
		return r.nextSelective()
	}
	if r.validationLifecycle != nil {
		for {
			tok, err := r.nextToken(true)
			if errors.Is(err, errLifecycleFiltered) {
				continue
			}
			return tok, err
		}
	}
	if tok, ok, err := r.updateSeqDelimiters(); ok || err != nil {
		return tok, err
	}
	if err := r.checkTotalBytes(); err != nil {
		return Token{}, err
	}
	headerOffset := r.Position()
	header, err := r.readHeader()
	if err != nil {
		if errors.Is(err, io.EOF) && len(r.seqDelimiters) > 0 {
			return Token{}, &ParseError{
				Op:     OpReadTag,
				Offset: r.Position(),
				Err:    io.ErrUnexpectedEOF,
			}
		}
		return Token{}, err
	}
	if tok, ok, err := r.controlToken(header); ok || err != nil {
		tok.Offset = headerOffset
		return tok, err
	}
	if len(r.seqDelimiters) > 0 && r.seqDelimiters[len(r.seqDelimiters)-1].typ == seqTokenTypePixelSequence {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("dicom: unexpected tag %s inside encapsulated Pixel Data", header.Tag),
		}
	}
	if header.VR == core.VRSQ || (header.VR == core.VRUN && header.Length.IsUndefined()) {
		if err := r.pushSequenceTokenWithImplicitVRLittleEndian(seqTokenTypeSequence, header.Length, header.VR == core.VRUN); err != nil {
			return Token{}, err
		}
		r.delimiterCheckPending = true
		return Token{Kind: TokenStartSequence, Header: header, Offset: headerOffset}, nil
	}
	if (header.VR == core.VROB || header.VR == core.VROW) && header.Tag == core.TagPixelData && header.Length.IsUndefined() {
		if err := r.pushSequenceToken(seqTokenTypePixelSequence, header.Length); err != nil {
			return Token{}, err
		}
		r.pixelSequenceOffsetTablePending = true
		r.delimiterCheckPending = true
		return Token{Kind: TokenStartPixelSequence, Header: header, Offset: headerOffset}, nil
	}
	if header.Length.IsUndefined() {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    ErrUnsupportedUndefinedLength,
		}
	}
	tok, err := r.readDefinedValueToken(header)
	tok.Offset = headerOffset
	return tok, err
}

func (r *Reader) nextToken(runLifecycle bool) (Token, error) {
	if tok, ok, err := r.updateSeqDelimiters(); ok || err != nil {
		return r.finishLifecycleToken(tok, err, runLifecycle)
	}
	if err := r.checkTotalBytes(); err != nil {
		return Token{}, err
	}
	headerOffset := r.Position()
	header, err := r.readHeader()
	if err != nil {
		if errors.Is(err, io.EOF) && len(r.seqDelimiters) > 0 {
			return Token{}, &ParseError{
				Op:     OpReadTag,
				Offset: r.Position(),
				Err:    io.ErrUnexpectedEOF,
			}
		}
		return Token{}, err
	}
	if tok, ok, err := r.controlToken(header); ok || err != nil {
		tok.Offset = headerOffset
		return r.finishLifecycleToken(tok, err, runLifecycle)
	}
	if len(r.seqDelimiters) > 0 && r.seqDelimiters[len(r.seqDelimiters)-1].typ == seqTokenTypePixelSequence {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("dicom: unexpected tag %s inside encapsulated Pixel Data", header.Tag),
		}
	}
	headerResult, err := r.handleLifecycleHeader(header, headerOffset, runLifecycle)
	if err != nil {
		return Token{}, err
	}
	if headerResult.SkipValue || headerResult.DeferValue {
		if header.VR == core.VRSQ || header.Length.IsUndefined() {
			return Token{}, fmt.Errorf("%w: header hook can only skip or defer a defined-length primitive", validation.ErrHookAction)
		}
		tok, err := r.readLifecycleSkippedValueToken(header, headerResult.DeferValue)
		tok.Offset = headerOffset
		return r.finishLifecycleToken(tok, err, runLifecycle)
	}
	if header.VR == core.VRSQ || (header.VR == core.VRUN && header.Length.IsUndefined()) {
		if err := r.pushSequenceTokenWithImplicitVRLittleEndian(seqTokenTypeSequence, header.Length, header.VR == core.VRUN); err != nil {
			return Token{}, err
		}
		r.delimiterCheckPending = true
		return r.finishLifecycleToken(Token{Kind: TokenStartSequence, Header: header, Offset: headerOffset}, nil, runLifecycle)
	}
	if (header.VR == core.VROB || header.VR == core.VROW) && header.Tag == core.TagPixelData && header.Length.IsUndefined() {
		if err := r.pushSequenceToken(seqTokenTypePixelSequence, header.Length); err != nil {
			return Token{}, err
		}
		r.pixelSequenceOffsetTablePending = true
		r.delimiterCheckPending = true
		return r.finishLifecycleToken(Token{Kind: TokenStartPixelSequence, Header: header, Offset: headerOffset}, nil, runLifecycle)
	}
	if header.Length.IsUndefined() {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    ErrUnsupportedUndefinedLength,
		}
	}
	tok, err := r.readDefinedValueToken(header)
	tok.Offset = headerOffset
	return r.finishLifecycleToken(tok, err, runLifecycle)
}

// ReadAll returns non-SQ elements in encounter order. Sequence/item boundaries
// are ignored, so SQ nesting is lost; encapsulated Pixel Data is preserved as a
// FragmentSequence. Prefer ReadDataSet when callers need full structure.
func (r *Reader) ReadAll() (elements []core.Element, err error) {
	defer func() {
		err = r.closeFrameSink(err)
	}()
	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			return elements, nil
		}
		if err != nil {
			return nil, err
		}
		switch tok.Kind {
		case TokenElement:
			elements = append(elements, tok.Element)
		case TokenStartPixelSequence:
			if r.skipPixelData || r.deferPixelData {
				valueStart := r.Position()
				if err := r.discardFragmentSequence(tok.Header); err != nil {
					return nil, err
				}
				r.recordValueLocation(tok.Header, valueStart, r.Position()-valueStart)
				elements = append(elements, core.Element{
					Header: tok.Header,
					Value:  nil,
				})
				continue
			}
			fragments, err := r.collectFragmentSequence(tok.Header)
			if err != nil {
				return nil, err
			}
			elements = append(elements, core.Element{
				Header: tok.Header,
				Value:  fragments,
			})
		}
	}
}

// ReadDataSet materializes the token stream into an ordered in-memory data set,
// preserving nested SQ items as core.SequenceValue. This is the preferred API
// for callers that need full sequence structure; ReadAll only returns primitive
// element tokens and therefore discards sequence nesting.
func (r *Reader) ReadDataSet() (dataset core.DataSet, err error) {
	defer func() {
		err = r.closeFrameSink(err)
	}()
	if r.validationLifecycle == nil {
		elements, err := r.collectElementsDefault(false)
		if err != nil {
			return core.DataSet{}, err
		}
		return core.DataSet{Elements: elements}, nil
	}
	elements, err := r.collectElementsValidated(false)
	if err != nil {
		return core.DataSet{}, err
	}
	dataset = core.DataSet{Elements: elements}
	result, validationErr := r.validationLifecycle.operation.ValidateParsedDataSet(dataset)
	return result.DataSet, validationErr
}

func (r *Reader) collectElementsDefault(inItem bool) ([]core.Element, error) {
	if inItem && r.inlineThreshold > 0 {
		inlineThreshold := r.inlineThreshold
		r.inlineThreshold = 0
		defer func() {
			r.inlineThreshold = inlineThreshold
		}()
	}

	var elements []core.Element
	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			if inItem {
				return nil, &ParseError{
					Op:     OpReadValue,
					Offset: r.Position(),
					Err:    io.ErrUnexpectedEOF,
				}
			}
			return elements, nil
		}
		if err != nil {
			return nil, err
		}

		switch tok.Kind {
		case TokenElement:
			elements = append(elements, tok.Element)
		case TokenStartSequence:
			seq, err := r.collectSequenceDefault(tok.Header)
			if err != nil {
				return nil, err
			}
			elements = append(elements, core.Element{
				Header: tok.Header,
				Value:  seq,
			})
		case TokenStartPixelSequence:
			if r.skipPixelData || r.deferPixelData {
				valueStart := r.Position()
				if err := r.discardFragmentSequence(tok.Header); err != nil {
					return nil, err
				}
				r.recordValueLocation(tok.Header, valueStart, r.Position()-valueStart)
				elements = append(elements, core.Element{
					Header: tok.Header,
					Value:  nil,
				})
				continue
			}
			fragments, err := r.collectFragmentSequence(tok.Header)
			if err != nil {
				return nil, err
			}
			elements = append(elements, core.Element{
				Header: tok.Header,
				Value:  fragments,
			})
		case TokenEndItem:
			if !inItem {
				return nil, unexpectedCollectorToken(tok, r.Position(), "top-level dataset")
			}
			return elements, nil
		case TokenStartItem:
			return nil, unexpectedCollectorToken(tok, r.Position(), "dataset")
		case TokenEndSequence:
			return nil, unexpectedCollectorToken(tok, r.Position(), "dataset")
		default:
			return nil, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Err:    fmt.Errorf("dicom: unsupported token kind %d while collecting dataset", tok.Kind),
			}
		}
	}
}

func (r *Reader) collectSequenceDefault(header core.ElementHeader) (core.SequenceValue, error) {
	var items []core.DataSet
	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			return core.SequenceValue{}, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    io.ErrUnexpectedEOF,
			}
		}
		if err != nil {
			return core.SequenceValue{}, err
		}

		switch tok.Kind {
		case TokenStartItem:
			elements, err := r.collectElementsDefault(true)
			if err != nil {
				return core.SequenceValue{}, err
			}
			items = append(items, core.DataSet{Elements: elements, ItemOffset: tok.Offset, ItemOffsetSet: true})
		case TokenEndSequence:
			return core.SequenceValue{Items: items}, nil
		default:
			return core.SequenceValue{}, unexpectedCollectorToken(tok, r.Position(), fmt.Sprintf("sequence %s", header.Tag))
		}
	}
}

func (r *Reader) collectElementsValidated(inItem bool) ([]core.Element, error) {
	if inItem && r.inlineThreshold > 0 {
		inlineThreshold := r.inlineThreshold
		r.inlineThreshold = 0
		defer func() {
			r.inlineThreshold = inlineThreshold
		}()
	}

	var elements []core.Element
	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			if inItem {
				return nil, &ParseError{
					Op:     OpReadValue,
					Offset: r.Position(),
					Err:    io.ErrUnexpectedEOF,
				}
			}
			return elements, nil
		}
		if err != nil {
			return nil, err
		}

		switch tok.Kind {
		case TokenElement:
			elements = append(elements, tok.Element)
		case TokenStartSequence:
			path := r.lifecycleElementPath(tok.Header.Tag)
			seq, err := r.collectSequenceValidated(tok.Header, path)
			if err != nil {
				return nil, err
			}
			element := core.Element{
				Header: tok.Header,
				Value:  seq,
			}
			element, filtered, err := r.handleCompletedElement(path, tok.Offset, validation.HookSequenceComplete, element)
			if err != nil {
				return nil, err
			}
			if !filtered {
				elements = append(elements, element)
			}
		case TokenStartPixelSequence:
			path := r.lifecycleElementPath(tok.Header.Tag)
			if r.skipPixelData || r.deferPixelData {
				valueStart := r.Position()
				if err := r.discardFragmentSequence(tok.Header); err != nil {
					return nil, err
				}
				r.recordValueLocation(tok.Header, valueStart, r.Position()-valueStart)
				element := core.Element{
					Header: tok.Header,
					Value:  nil,
				}
				element, filtered, hookErr := r.handleCompletedElement(path, tok.Offset, "", element)
				if hookErr != nil {
					return nil, hookErr
				}
				if !filtered {
					elements = append(elements, element)
				}
				continue
			}
			fragments, err := r.collectFragmentSequence(tok.Header)
			if err != nil {
				return nil, err
			}
			element := core.Element{
				Header: tok.Header,
				Value:  fragments,
			}
			element, filtered, hookErr := r.handleCompletedElement(path, tok.Offset, "", element)
			if hookErr != nil {
				return nil, hookErr
			}
			if !filtered {
				elements = append(elements, element)
			}
		case TokenEndItem:
			if !inItem {
				return nil, unexpectedCollectorToken(tok, r.Position(), "top-level dataset")
			}
			return elements, nil
		case TokenStartItem:
			return nil, unexpectedCollectorToken(tok, r.Position(), "dataset")
		case TokenEndSequence:
			return nil, unexpectedCollectorToken(tok, r.Position(), "dataset")
		default:
			return nil, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Err:    fmt.Errorf("dicom: unsupported token kind %d while collecting dataset", tok.Kind),
			}
		}
	}
}

func (r *Reader) collectSequenceValidated(header core.ElementHeader, sequencePath validation.Path) (core.SequenceValue, error) {
	var items []core.DataSet
	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			return core.SequenceValue{}, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    io.ErrUnexpectedEOF,
			}
		}
		if err != nil {
			return core.SequenceValue{}, err
		}

		switch tok.Kind {
		case TokenStartItem:
			elements, err := r.collectElementsValidated(true)
			if err != nil {
				return core.SequenceValue{}, err
			}
			item := core.DataSet{Elements: elements, ItemOffset: tok.Offset, ItemOffsetSet: true}
			if r.validationLifecycle != nil && r.validationSuppressed == 0 {
				itemPath := sequencePath.Clone()
				itemPath[len(itemPath)-1].ItemIndex = len(items)
				if _, err := r.validationLifecycle.operation.Handle(validation.HookEvent{Point: validation.HookItemComplete, Path: itemPath, DataSet: &item, Offset: tok.Offset, OffsetSet: true}); err != nil {
					return core.SequenceValue{}, err
				}
			}
			items = append(items, item)
		case TokenEndSequence:
			return core.SequenceValue{Items: items}, nil
		default:
			return core.SequenceValue{}, unexpectedCollectorToken(tok, r.Position(), fmt.Sprintf("sequence %s", header.Tag))
		}
	}
}

func (r *Reader) collectFragmentSequence(header core.ElementHeader) (core.FragmentSequence, error) {
	var value core.FragmentSequence
	var sawOffsetTable bool

	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			return core.FragmentSequence{}, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    io.ErrUnexpectedEOF,
			}
		}
		if err != nil {
			return core.FragmentSequence{}, err
		}

		switch tok.Kind {
		case TokenElement:
			raw, ok := tok.Element.RawBytes()
			if !ok {
				return core.FragmentSequence{}, unexpectedCollectorToken(tok, r.Position(), fmt.Sprintf("pixel sequence %s", header.Tag))
			}
			if !sawOffsetTable {
				value.OffsetTable = core.CloneBytes(raw)
				sawOffsetTable = true
				continue
			}
			value.Fragments = append(value.Fragments, core.CloneBytes(raw))
		case TokenEndSequence:
			if !sawOffsetTable {
				return core.FragmentSequence{}, &ParseError{
					Op:     OpReadValue,
					Offset: r.Position(),
					Tag:    header.Tag,
					VR:     header.VR,
					Length: header.Length,
					Err:    ErrMissingBasicOffsetTable,
				}
			}
			return value, nil
		default:
			return core.FragmentSequence{}, unexpectedCollectorToken(tok, r.Position(), fmt.Sprintf("pixel sequence %s", header.Tag))
		}
	}
}

func (r *Reader) discardFragmentSequence(header core.ElementHeader) error {
	for {
		tok, err := r.Next()
		if errors.Is(err, io.EOF) {
			return &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    io.ErrUnexpectedEOF,
			}
		}
		if err != nil {
			return err
		}

		switch tok.Kind {
		case TokenElement:
			continue
		case TokenEndSequence:
			return nil
		default:
			return unexpectedCollectorToken(tok, r.Position(), fmt.Sprintf("pixel sequence %s", header.Tag))
		}
	}
}

func unexpectedCollectorToken(tok Token, offset int64, context string) error {
	return &ParseError{
		Op:     OpReadValue,
		Offset: offset,
		Tag:    tok.Header.Tag,
		VR:     tok.Header.VR,
		Length: tok.Header.Length,
		Err:    fmt.Errorf("dicom: unexpected %s while collecting %s", tok.Kind, context),
	}
}

func (r *Reader) readHeader() (core.ElementHeader, error) {
	r.lastReservedNonZero = false
	headerOffset := r.Position()
	decoder := r.currentDecoder()
	tag, err := decoder.ReadTag(r.counter)
	if err != nil {
		err = normalizeReadError(headerOffset, r.Position(), err)
		if r.Position() == headerOffset && errors.Is(err, io.EOF) {
			return core.ElementHeader{}, io.EOF
		}
		return core.ElementHeader{}, &ParseError{
			Op:     OpReadTag,
			Offset: headerOffset,
			Err:    err,
		}
	}
	if tag.IsSequenceDelimiting() {
		length, err := r.readLongLength(tag, core.VRUN)
		if err != nil {
			return core.ElementHeader{}, err
		}
		return core.ElementHeader{Tag: tag, VR: core.VRUN, Length: length, LengthSet: true}, nil
	}
	if r.syntax.ExplicitVR && !r.implicitVRLittleEndianActive() {
		return r.readExplicitHeader(tag)
	}
	return r.readImplicitHeader(tag)
}

func (r *Reader) readExplicitHeader(tag core.Tag) (core.ElementHeader, error) {
	var vr core.VR
	var length core.Length

	var vrBytes [2]byte
	vrOffset := r.Position()
	if _, err := io.ReadFull(r.counter, vrBytes[:]); err != nil {
		return core.ElementHeader{}, &ParseError{
			Op:     OpReadVR,
			Offset: vrOffset,
			Tag:    tag,
			Err:    normalizeReadError(vrOffset, r.Position(), err),
		}
	}
	parsedVR, err := core.ParseVR(string(vrBytes[:]))
	if err != nil {
		return core.ElementHeader{}, &ParseError{
			Op:     OpReadVR,
			Offset: vrOffset,
			Tag:    tag,
			Err:    err,
		}
	}
	vr = parsedVR
	if vr.UsesLongExplicitLength() {
		var reserved [2]byte
		reservedOffset := r.Position()
		if _, err := io.ReadFull(r.counter, reserved[:]); err != nil {
			return core.ElementHeader{}, &ParseError{
				Op:     OpReadReserved,
				Offset: reservedOffset,
				Tag:    tag,
				VR:     vr,
				Err:    normalizeReadError(reservedOffset, r.Position(), err),
			}
		}
		if r.strictReservedBytes && reserved != [2]byte{} {
			return core.ElementHeader{}, &ParseError{
				Op:     OpValidateReserved,
				Offset: reservedOffset,
				Tag:    tag,
				VR:     vr,
				Err:    fmt.Errorf("%w: got %02X%02X", ErrNonZeroReservedBytes, reserved[0], reserved[1]),
			}
		}
		if reserved != [2]byte{} {
			r.lastReservedNonZero = true
			r.lastReservedOffset = reservedOffset
		}
		length, err = r.readLongLength(tag, vr)
		if err != nil {
			return core.ElementHeader{}, err
		}
	} else {
		lengthOffset := r.Position()
		u16, err := r.currentDecoder().ReadU16(r.counter)
		if err != nil {
			return core.ElementHeader{}, &ParseError{
				Op:     OpReadLength,
				Offset: lengthOffset,
				Tag:    tag,
				VR:     vr,
				Err:    normalizeReadError(lengthOffset, r.Position(), err),
			}
		}
		length = core.Length(u16)
	}
	return core.ElementHeader{Tag: tag, VR: vr, Length: length, LengthSet: true}, nil
}

func (r *Reader) readImplicitHeader(tag core.Tag) (core.ElementHeader, error) {
	vr := r.lookupImplicitVR(tag)
	length, err := r.readLongLength(tag, vr)
	if err != nil {
		return core.ElementHeader{}, err
	}
	return core.ElementHeader{Tag: tag, VR: vr, Length: length, LengthSet: true}, nil
}

func (r *Reader) readLongLength(tag core.Tag, vr core.VR) (core.Length, error) {
	lengthOffset := r.Position()
	u32, err := r.currentDecoder().ReadU32(r.counter)
	if err != nil {
		return 0, &ParseError{
			Op:     OpReadLength,
			Offset: lengthOffset,
			Tag:    tag,
			VR:     vr,
			Err:    normalizeReadError(lengthOffset, r.Position(), err),
		}
	}
	return core.Length(u32), nil
}

// lookupImplicitVR resolves a tag's VR for implicit-VR transfer syntaxes in
// this order:
//  1. Hard-coded special cases for Pixel Data and Overlay Data, both of which
//     are treated as OW for parser safety.
//  2. Dictionary lookup via dictionary.LookupVR.
//  3. Fallback to UN when no dictionary entry is available.
//
// Private tags are typically absent from the standard dictionary, so they
// usually resolve to UN unless callers provide a custom dictionary. A defined-
// length UN value is preserved as raw bytes. An undefined-length UN value is
// parsed through the sequence item/delimiter grammar with a
// scoped Implicit VR Little Endian override while preserving VR UN in the
// in-memory header. Full private dictionary support is intentionally out of
// scope.
func (r *Reader) lookupImplicitVR(tag core.Tag) core.VR {
	if tag == core.TagPixelData {
		return core.VROW
	}
	if tag.Element == 0x3000 && tag.Group>>8 == 0x60 && tag.Group&1 == 0 {
		return core.VROW
	}
	return dictionary.LookupVR(r.dict, tag)
}

func (r *Reader) controlToken(header core.ElementHeader) (Token, bool, error) {
	if !header.Tag.IsSequenceDelimiting() {
		return Token{}, false, nil
	}

	switch {
	case header.Tag.IsItem():
		if len(r.seqDelimiters) > 0 && r.seqDelimiters[len(r.seqDelimiters)-1].typ == seqTokenTypePixelSequence {
			if header.Length.IsUndefined() {
				return Token{}, true, &ParseError{
					Op:     OpReadValue,
					Offset: r.Position(),
					Tag:    header.Tag,
					VR:     header.VR,
					Length: header.Length,
					Err:    ErrUnsupportedUndefinedLength,
				}
			}
			tok, err := r.readPixelSequenceItemToken(header)
			return tok, true, err
		}
		if err := r.validateItemStart(header); err != nil {
			return Token{}, true, err
		}
		if err := r.pushSequenceToken(seqTokenTypeItem, header.Length); err != nil {
			return Token{}, true, err
		}
		r.delimiterCheckPending = true
		return Token{Kind: TokenStartItem, Header: header}, true, nil
	case header.Tag.IsItemDelimitationItem():
		if err := validateDelimiterLength(header, r.Position()); err != nil {
			return Token{}, true, err
		}
		if err := r.popUndefinedLengthSequenceToken(header, seqTokenTypeItem, ErrUnexpectedItemDelimiter); err != nil {
			return Token{}, true, err
		}
		r.delimiterCheckPending = true
		return Token{Kind: TokenEndItem, Header: header}, true, nil
	case header.Tag.IsSequenceDelimitationItem():
		if err := validateDelimiterLength(header, r.Position()); err != nil {
			return Token{}, true, err
		}
		if len(r.seqDelimiters) > 0 && r.seqDelimiters[len(r.seqDelimiters)-1].typ == seqTokenTypePixelSequence {
			if err := r.popUndefinedLengthSequenceToken(header, seqTokenTypePixelSequence, ErrUnexpectedSequenceDelimiter); err != nil {
				return Token{}, true, err
			}
			r.pixelSequenceOffsetTablePending = false
			r.delimiterCheckPending = true
			return Token{Kind: TokenEndSequence, Header: header}, true, nil
		}
		if err := r.popUndefinedLengthSequenceToken(header, seqTokenTypeSequence, ErrUnexpectedSequenceDelimiter); err != nil {
			return Token{}, true, err
		}
		r.delimiterCheckPending = true
		return Token{Kind: TokenEndSequence, Header: header}, true, nil
	default:
		return Token{}, true, &ParseError{
			Op:     OpReadTag,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("%w: %s", ErrUnexpectedSequenceControlTag, header.Tag),
		}
	}
}

func (r *Reader) readPixelSequenceItemToken(header core.ElementHeader) (Token, error) {
	if !r.pixelSequenceOffsetTablePending && r.maxFragments > 0 && r.fragmentCount >= r.maxFragments {
		return Token{}, &ParseError{
			Op:     OpCheckFragmentCount,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxFragmentsExceeded, r.fragmentCount+1, r.maxFragments),
		}
	}

	if r.skipPixelData || r.deferPixelData {
		valueOffset := r.Position()
		var err error
		if r.selective != nil && r.selective.skippingPixelSequence {
			if err = r.validateDefinedValueLength(header); err == nil {
				err = r.selectiveSkipN(valueOffset, header, int64(header.Length))
			}
		} else {
			err = r.skipN(valueOffset, header, int64(header.Length))
		}
		if err != nil {
			return Token{}, err
		}
		if r.pixelSequenceOffsetTablePending {
			r.pixelSequenceOffsetTablePending = false
		} else {
			r.fragmentCount++
		}
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}

	// Fragment items must stay materialized so collectFragmentSequence can
	// distinguish the Basic Offset Table from encoded fragments.
	inlineThreshold := r.inlineThreshold
	r.inlineThreshold = 0
	tok, err := r.readDefinedValueToken(header)
	r.inlineThreshold = inlineThreshold
	if err != nil {
		return Token{}, err
	}
	if r.pixelSequenceOffsetTablePending {
		r.pixelSequenceOffsetTablePending = false
		return tok, nil
	}
	r.fragmentCount++
	return tok, nil
}

func (r *Reader) updateSeqDelimiters() (Token, bool, error) {
	if !r.delimiterCheckPending {
		return Token{}, false, nil
	}
	if len(r.seqDelimiters) == 0 {
		r.delimiterCheckPending = false
		return Token{}, false, nil
	}

	last := r.seqDelimiters[len(r.seqDelimiters)-1]
	if last.length.IsUndefined() {
		r.delimiterCheckPending = false
		return Token{}, false, nil
	}

	endOffset := last.baseOffset + uint64(last.length)
	currentOffset := r.positionU64()
	switch {
	case currentOffset == endOffset:
		r.seqDelimiters = r.seqDelimiters[:len(r.seqDelimiters)-1]
		switch last.typ {
		case seqTokenTypeSequence:
			return Token{Kind: TokenEndSequence}, true, nil
		case seqTokenTypePixelSequence:
			r.pixelSequenceOffsetTablePending = false
			return Token{Kind: TokenEndSequence}, true, nil
		case seqTokenTypeItem:
			return Token{Kind: TokenEndItem}, true, nil
		default:
			return Token{}, true, &ParseError{
				Op:     OpReadValue,
				Offset: int64(currentOffset),
				Err:    fmt.Errorf("dicom: unknown sequence token type %d", last.typ),
			}
		}
	case currentOffset > endOffset:
		return Token{}, true, &ParseError{
			Op:     OpReadValue,
			Offset: int64(currentOffset),
			Length: last.length,
			Err:    fmt.Errorf("dicom: read past %s boundary: expected end at offset %d, got %d", last.typ, endOffset, currentOffset),
		}
	default:
		r.delimiterCheckPending = false
		return Token{}, false, nil
	}
}

func (r *Reader) validateItemStart(header core.ElementHeader) error {
	if len(r.seqDelimiters) == 0 {
		return &ParseError{
			Op:     OpReadTag,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("%w: %s", ErrUnexpectedSequenceControlTag, header.Tag),
		}
	}
	parent := r.seqDelimiters[len(r.seqDelimiters)-1]
	if parent.typ != seqTokenTypeSequence && parent.typ != seqTokenTypePixelSequence {
		return &ParseError{
			Op:     OpReadTag,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("%w: %s", ErrUnexpectedSequenceControlTag, header.Tag),
		}
	}
	return nil
}

func (r *Reader) pushSequenceToken(typ seqTokenType, length core.Length) error {
	return r.pushSequenceTokenWithImplicitVRLittleEndian(typ, length, false)
}

func (r *Reader) pushSequenceTokenWithImplicitVRLittleEndian(typ seqTokenType, length core.Length, force bool) error {
	if r.maxSequenceDepth > 0 && len(r.seqDelimiters)+1 > r.maxSequenceDepth {
		return &ParseError{
			Op:     OpCheckDepth,
			Offset: r.Position(),
			Length: length,
			Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxDepthExceeded, len(r.seqDelimiters)+1, r.maxSequenceDepth),
		}
	}
	implicitVRLittleEndian := r.implicitVRLittleEndianActive()
	if force {
		implicitVRLittleEndian = true
	}
	r.seqDelimiters = append(r.seqDelimiters, seqToken{
		typ:                    typ,
		length:                 length,
		baseOffset:             r.positionU64(),
		implicitVRLittleEndian: implicitVRLittleEndian,
	})
	return nil
}

func (r *Reader) implicitVRLittleEndianActive() bool {
	if len(r.seqDelimiters) == 0 {
		return false
	}
	return r.seqDelimiters[len(r.seqDelimiters)-1].implicitVRLittleEndian
}

func (r *Reader) currentDecoder() dicomenc.BasicDecoder {
	if r.implicitVRLittleEndianActive() {
		return dicomenc.NewBasicDecoder(binary.LittleEndian)
	}
	return r.dec
}

func (r *Reader) popUndefinedLengthSequenceToken(header core.ElementHeader, want seqTokenType, errType error) error {
	if len(r.seqDelimiters) == 0 {
		return &ParseError{
			Op:     OpReadTag,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    errType,
		}
	}
	last := r.seqDelimiters[len(r.seqDelimiters)-1]
	if last.typ != want || last.length.IsDefined() {
		return &ParseError{
			Op:     OpReadTag,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    errType,
		}
	}
	r.seqDelimiters = r.seqDelimiters[:len(r.seqDelimiters)-1]
	return nil
}

func (r *Reader) positionU64() uint64 {
	pos := r.Position()
	if pos < 0 {
		return 0
	}
	return uint64(pos)
}

func validateDelimiterLength(header core.ElementHeader, offset int64) error {
	if header.Length == 0 {
		return nil
	}
	return &ParseError{
		Op:     OpReadLength,
		Offset: offset,
		Tag:    header.Tag,
		VR:     header.VR,
		Length: header.Length,
		Err:    fmt.Errorf("%w for %s", ErrUnexpectedDelimiterLength, header.Tag),
	}
}

func normalizeReadError(start, end int64, err error) error {
	if err == nil {
		return nil
	}
	if end > start && errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (r *Reader) checkTotalBytes() error {
	if r.maxTotalBytes <= 0 || r.Position() < r.maxTotalBytes {
		return nil
	}
	return &ParseError{
		Op:     OpCheckTotalBytes,
		Offset: r.Position(),
		Err:    fmt.Errorf("%w: read %d bytes, limit %d", ErrMaxTotalBytesExceeded, r.Position(), r.maxTotalBytes),
	}
}

func (r *Reader) validateDefinedValueLength(header core.ElementHeader) error {
	if header.Length&1 == 0 || r.oddLengthPolicy == AcceptOddLength {
		return nil
	}
	return &ParseError{
		Op:     OpReadValue,
		Offset: r.Position(),
		Tag:    header.Tag,
		VR:     header.VR,
		Length: header.Length,
		Err:    fmt.Errorf("%w: got %d", ErrOddElementLength, header.Length),
	}
}

func (r *Reader) skipN(offset int64, header core.ElementHeader, n int64) error {
	if n == 0 {
		return nil
	}
	if n < 0 {
		return &ParseError{
			Op:     OpReadValue,
			Offset: offset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("cannot skip negative length %d", n),
		}
	}
	// io.CopyN returns nil when exactly n bytes were copied; otherwise it returns
	// an error (including EOF). We normalize EOF into UnexpectedEOF if we've
	// advanced at all.
	if _, err := io.CopyN(io.Discard, r.counter, n); err != nil {
		return &ParseError{
			Op:     OpReadValue,
			Offset: offset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    normalizeReadError(offset, r.Position(), err),
		}
	}
	return nil
}

func (r *Reader) readDefinedValueToken(header core.ElementHeader) (Token, error) {
	if err := r.validateDefinedValueLength(header); err != nil {
		return Token{}, err
	}
	if r.maxElements > 0 && r.elementCount >= r.maxElements {
		return Token{}, &ParseError{
			Op:     OpCheckElementCount,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxElementsExceeded, r.elementCount+1, r.maxElements),
		}
	}
	if r.stopBeforePixelData && header.Tag == core.TagPixelData {
		r.elementCount++
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	if r.maxElementBytes > 0 && int64(header.Length) > r.maxElementBytes {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: r.Position(),
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    fmt.Errorf("%w: element %s length %d exceeds limit %d", ErrMaxElementBytesExceeded, header.Tag, header.Length, r.maxElementBytes),
		}
	}
	valueOffset := r.Position()
	if r.frameSink != nil && !r.frameSinkClosed && header.Tag == core.TagPixelData && !r.syntax.Encapsulated {
		return r.readNativePixelDataFramesToken(header, valueOffset)
	}
	if (r.skipPixelData || r.deferPixelData) && isPixelDataValueTag(header.Tag) {
		if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
			return Token{}, err
		}
		r.recordValueLocation(header, valueOffset, int64(header.Length))
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	if r.deferWaveformData && header.Tag == tagWaveformData {
		if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
			return Token{}, err
		}
		r.recordValueLocation(header, valueOffset, int64(header.Length))
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	if r.inlineThreshold > 0 && int64(header.Length) > r.inlineThreshold && header.VR != core.VRUT {
		// Large defined-length primitive.
		//
		// We only support skipping/streaming raw bytes for VRs where consumers can
		// reasonably interpret the bytes without additional parsing/decoding.
		//
		// This includes the typical "byte blob" VRs and native (defined-length)
		// Pixel Data.
		switch header.VR {
		case core.VROB, core.VROW, core.VROF, core.VROD, core.VRUN:
			// ok
		default:
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: valueOffset,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    fmt.Errorf("dicom: refusing to skip/stream large defined-length value for VR %s", header.VR),
			}
		}
		if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
			return Token{}, err
		}
		r.recordValueLocation(header, valueOffset, int64(header.Length))
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	data, err := r.readDefinedValueBytes(header, valueOffset)
	if err != nil {
		return Token{}, err
	}
	if err := r.captureFrameMetadata(header, data); err != nil {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    err,
		}
	}
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{
		Kind:   TokenElement,
		Header: header,
		Element: core.Element{
			Header: header,
			Value:  core.RawValue(data),
		},
	}, nil
}

func isPixelDataValueTag(tag core.Tag) bool {
	return tag == core.TagPixelData || tag == tagFloatPixelData || tag == tagDoubleFloatPixelData
}

func (r *Reader) readDefinedValueBytes(header core.ElementHeader, valueOffset int64) ([]byte, error) {
	length := int64(header.Length)
	if length == 0 {
		return []byte{}, nil
	}
	if r.slurpEligible {
		r.ensureSlurp()
	}
	maxInt := int64(^uint(0) >> 1)
	if src, ok := r.counter.r.(*sliceSource); ok && length <= maxInt {
		if data, ok := src.take(int(length)); ok {
			r.counter.pos += int64(len(data))
			return data, nil
		}
		// Fewer bytes remain than the declared length; fall through so the
		// streaming paths produce the same truncation errors as any source.
	}
	if length <= definedValueReadChunkSize {
		data := r.allocateDefinedValueBytes(int(length))
		if _, err := io.ReadFull(r.counter, data); err != nil {
			return nil, r.valueReadError(valueOffset, header, err)
		}
		return data, nil
	}
	if length <= maxInt {
		// A configured limit has already validated length in
		// readDefinedValueToken, and a length that fits before the physical end
		// of a sized source cannot be a forged huge VL. Either bound lets the
		// final buffer be allocated exactly once. Unsized streams keep the
		// incremental growth below as their allocation guard.
		if r.maxElementBytes > 0 || (r.sourceEnd > 0 && length <= r.sourceEnd-r.Position()) {
			data := r.allocateDefinedValueBytes(int(length))
			if _, err := io.ReadFull(r.counter, data); err != nil {
				return nil, r.valueReadError(valueOffset, header, err)
			}
			return data, nil
		}
	}

	var out bytes.Buffer
	out.Grow(definedValueReadChunkSize)
	remaining := length
	for remaining > 0 {
		want := int64(definedValueReadChunkSize)
		if remaining < want {
			want = remaining
		}
		out.Grow(int(want))
		// Read into the buffer's free capacity instead of copying through a
		// separate scratch slice. Write only advances the buffer length here.
		dst := out.AvailableBuffer()[:int(want)]
		n, err := io.ReadFull(r.counter, dst)
		if n > 0 {
			out.Write(dst[:n])
			remaining -= int64(n)
		}
		if err != nil {
			return nil, r.valueReadError(valueOffset, header, err)
		}
	}
	return out.Bytes(), nil
}

func (r *Reader) allocateDefinedValueBytes(length int) []byte {
	if length <= 0 {
		return []byte{}
	}
	if length > smallValueArenaLimit {
		return make([]byte, length)
	}
	if cap(r.smallValueArena)-len(r.smallValueArena) < length {
		blockSize := smallValueArenaBlockBytes
		if cap(r.smallValueArena) == 0 {
			blockSize = smallValueArenaInitialBytes
		}
		r.smallValueArena = make([]byte, 0, blockSize)
	}
	start := len(r.smallValueArena)
	r.smallValueArena = r.smallValueArena[:start+length]
	return r.smallValueArena[start : start+length : start+length]
}

func (r *Reader) valueReadError(valueOffset int64, header core.ElementHeader, err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	} else {
		err = normalizeReadError(valueOffset, r.Position(), err)
	}
	return &ParseError{
		Op:     OpReadValue,
		Offset: valueOffset,
		Tag:    header.Tag,
		VR:     header.VR,
		Length: header.Length,
		Err:    err,
	}
}

func (r *Reader) readNativePixelDataFramesToken(header core.ElementHeader, valueOffset int64) (Token, error) {
	metadata, err := r.frameMetadata.complete()
	if err != nil {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    err,
		}
	}

	frameSize := metadata.FrameSize()
	totalSize := metadata.TotalSize()
	maxInt := int64(^uint(0) >> 1)
	if frameSize <= 0 || totalSize <= 0 || frameSize > maxInt || totalSize > maxInt {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err: fmt.Errorf(
				"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d number_of_frames=%d",
				ErrInvalidFrameMetadata,
				metadata.Rows,
				metadata.Columns,
				metadata.SamplesPerPixel,
				metadata.BitsAllocated,
				metadata.NumberOfFrames,
			),
		}
	}
	encodedSize := int64(header.Length)
	hasPadding := encodedSize == totalSize+1 && totalSize%2 == 1
	if encodedSize != totalSize && !hasPadding {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err: fmt.Errorf(
				"%w: expected %d bytes for %d frame(s), got %d",
				ErrFrameDataSizeMismatch,
				totalSize,
				metadata.NumberOfFrames,
				header.Length,
			),
		}
	}

	frameSizeInt := int(frameSize)
	for i := 0; i < metadata.NumberOfFrames; i++ {
		data := make([]byte, frameSizeInt)
		if _, err := io.ReadFull(r.counter, data); err != nil {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: valueOffset + int64(i)*frameSize,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    normalizeReadError(valueOffset, r.Position(), err),
			}
		}
		if err := r.frameSink.HandleFrame(Frame{
			Index:          i,
			Data:           data,
			Metadata:       metadata,
			TransferSyntax: r.syntax,
		}); err != nil {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    fmt.Errorf("%w: frame %d: %w", ErrFrameSink, i, err),
			}
		}
	}
	if hasPadding {
		var pad [1]byte
		padOffset := valueOffset + totalSize
		if _, err := io.ReadFull(r.counter, pad[:]); err != nil {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: padOffset,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    normalizeReadError(padOffset, r.Position(), err),
			}
		}
		if pad[0] != 0 {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: padOffset,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err: fmt.Errorf(
					"%w: expected zero padding byte after %d byte payload, got 0x%02X",
					ErrFrameDataSizeMismatch,
					totalSize,
					pad[0],
				),
			}
		}
	}

	r.recordValueLocation(header, valueOffset, int64(header.Length))
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{
		Kind:   TokenElement,
		Header: header,
		Element: core.Element{
			Header: header,
			Value:  nil,
		},
	}, nil
}

func (r *Reader) closeFrameSink(readErr error) error {
	if r == nil || r.frameSink == nil || r.frameSinkClosed {
		return readErr
	}
	r.frameSinkClosed = true
	closeErr := r.frameSink.Close()
	if readErr == nil {
		return closeErr
	}
	if closeErr == nil {
		return readErr
	}
	return errors.Join(readErr, closeErr)
}

func (t seqTokenType) String() string {
	switch t {
	case seqTokenTypeSequence:
		return "sequence"
	case seqTokenTypePixelSequence:
		return "pixel sequence"
	case seqTokenTypeItem:
		return "item"
	default:
		return "unknown"
	}
}
