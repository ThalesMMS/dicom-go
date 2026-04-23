package parser

import (
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrUnsupportedUndefinedLength = errors.New("dicom: undefined length values are not implemented in the scaffold parser")
var ErrNonZeroReservedBytes = errors.New("dicom: explicit VR long-form reserved bytes must be zero")
var ErrUnexpectedSequenceControlTag = errors.New("dicom: unexpected sequence control tag")
var ErrUnexpectedDelimiterLength = errors.New("dicom: delimiter length must be zero")
var ErrMissingBasicOffsetTable = errors.New("dicom: encapsulated Pixel Data is missing the Basic Offset Table item")

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
	typ        seqTokenType
	length     core.Length
	baseOffset uint64
}

type ReaderOptions struct {
	Dictionary dictionary.DataDictionary
	// MaxElementBytes limits a single defined-length element value allocation in
	// bytes. Use it to reject suspiciously large values before allocating
	// memory. A zero value means unlimited.
	MaxElementBytes int64
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
}

type Reader struct {
	counter             *countingReader
	syntax              transfer.Syntax
	dec                 dicomenc.BasicDecoder
	dict                dictionary.DataDictionary
	maxElementBytes     int64
	maxTotalBytes       int64
	maxSequenceDepth    int
	maxElements         int
	maxFragments        int
	elementCount        int
	fragmentCount       int
	strictReservedBytes bool
	oddLengthPolicy     OddLengthPolicy
	// seqDelimiters tracks the combined stack of open sequence and item frames
	// used for depth enforcement and defined-length boundary closure.
	seqDelimiters                   []seqToken
	delimiterCheckPending           bool
	pixelSequenceOffsetTablePending bool
}

func NewReader(r io.Reader, syntax transfer.Syntax, opts ReaderOptions) *Reader {
	cr := &countingReader{r: r, pos: opts.BaseOffset, maxTotalBytes: opts.MaxTotalBytes}
	return &Reader{
		counter:             cr,
		syntax:              syntax,
		dec:                 dicomenc.NewBasicDecoder(syntax.ByteOrder),
		dict:                opts.Dictionary,
		maxElementBytes:     opts.MaxElementBytes,
		maxTotalBytes:       opts.MaxTotalBytes,
		maxSequenceDepth:    opts.MaxSequenceDepth,
		maxElements:         opts.MaxElements,
		maxFragments:        opts.MaxFragments,
		strictReservedBytes: opts.StrictReservedBytes,
		oddLengthPolicy:     opts.OddLengthPolicy,
		seqDelimiters:       make([]seqToken, 0),
	}
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

func (r *Reader) Next() (Token, error) {
	if tok, ok, err := r.updateSeqDelimiters(); ok || err != nil {
		return tok, err
	}
	if err := r.checkTotalBytes(); err != nil {
		return Token{}, err
	}
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
	if header.VR == core.VRSQ {
		if err := r.pushSequenceToken(seqTokenTypeSequence, header.Length); err != nil {
			return Token{}, err
		}
		r.delimiterCheckPending = true
		return Token{Kind: TokenStartSequence, Header: header}, nil
	}
	if (header.VR == core.VROB || header.VR == core.VROW) && header.Tag == core.TagPixelData && header.Length.IsUndefined() {
		if err := r.pushSequenceToken(seqTokenTypePixelSequence, header.Length); err != nil {
			return Token{}, err
		}
		r.pixelSequenceOffsetTablePending = true
		r.delimiterCheckPending = true
		return Token{Kind: TokenStartPixelSequence, Header: header}, nil
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
	return r.readDefinedValueToken(header)
}

// ReadAll returns non-SQ elements in encounter order. Sequence/item boundaries
// are ignored, so SQ nesting is lost; encapsulated Pixel Data is preserved as a
// FragmentSequence. Prefer ReadDataSet when callers need full structure.
func (r *Reader) ReadAll() ([]core.Element, error) {
	var elements []core.Element
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
func (r *Reader) ReadDataSet() (core.DataSet, error) {
	elements, err := r.collectElements(false)
	if err != nil {
		return core.DataSet{}, err
	}
	return core.DataSet{Elements: elements}, nil
}

func (r *Reader) collectElements(inItem bool) ([]core.Element, error) {
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
			seq, err := r.collectSequence(tok.Header)
			if err != nil {
				return nil, err
			}
			elements = append(elements, core.Element{
				Header: tok.Header,
				Value:  seq,
			})
		case TokenStartPixelSequence:
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

func (r *Reader) collectSequence(header core.ElementHeader) (core.SequenceValue, error) {
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
			elements, err := r.collectElements(true)
			if err != nil {
				return core.SequenceValue{}, err
			}
			items = append(items, core.DataSet{Elements: elements})
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
	headerOffset := r.Position()
	tag, err := r.dec.ReadTag(r.counter)
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
	if r.syntax.ExplicitVR {
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
		length, err = r.readLongLength(tag, vr)
		if err != nil {
			return core.ElementHeader{}, err
		}
	} else {
		lengthOffset := r.Position()
		u16, err := r.dec.ReadU16(r.counter)
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
	u32, err := r.dec.ReadU32(r.counter)
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
// length UN value is preserved as raw bytes; an undefined-length UN value is
// rejected with ErrUnsupportedUndefinedLength. Full private dictionary support
// is intentionally out of scope.
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

	tok, err := r.readDefinedValueToken(header)
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
	if r.maxSequenceDepth > 0 && len(r.seqDelimiters)+1 > r.maxSequenceDepth {
		return &ParseError{
			Op:     OpCheckDepth,
			Offset: r.Position(),
			Length: length,
			Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxDepthExceeded, len(r.seqDelimiters)+1, r.maxSequenceDepth),
		}
	}
	r.seqDelimiters = append(r.seqDelimiters, seqToken{
		typ:        typ,
		length:     length,
		baseOffset: r.positionU64(),
	})
	return nil
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

func (r *Reader) readDefinedValueToken(header core.ElementHeader) (Token, error) {
	if err := r.validateDefinedValueLength(header); err != nil {
		return Token{}, err
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
	data := make([]byte, int(header.Length))
	valueOffset := r.Position()
	if _, err := io.ReadFull(r.counter, data); err != nil {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    normalizeReadError(valueOffset, r.Position(), err),
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
