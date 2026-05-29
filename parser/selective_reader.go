package parser

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

var (
	ErrSelectiveReaderOptions  = errors.New("dicom: invalid selective reader options")
	ErrSelectiveReaderCallback = errors.New("dicom: selective reader callback failed")
	ErrSelectiveDisposition    = errors.New("dicom: invalid selective reader disposition")
)

// SelectiveDisposition controls how a SelectiveReader handles the value whose
// header was just read.
type SelectiveDisposition uint8

const (
	// SelectiveMaterialize preserves ordinary Reader behavior for the element.
	SelectiveMaterialize SelectiveDisposition = iota
	// SelectiveSkip consumes a defined-length primitive without allocating its
	// value. The returned TokenElement has a nil Value.
	SelectiveSkip
	// SelectiveStop emits TokenStop without consuming any value bytes.
	SelectiveStop
)

// SelectiveReaderSelector decides how to handle one data element after its
// header has been decoded and before its value or sequence body is consumed.
// Path and Offset identify the header without exposing its value.
type SelectiveReaderSelector func(context.Context, validation.Path, core.ElementHeader, int64) (SelectiveDisposition, error)

// SelectiveReaderOptions configures NewSelectiveReader.
type SelectiveReaderOptions struct {
	Select SelectiveReaderSelector
}

type selectiveReaderFrame struct {
	tag       core.Tag
	itemIndex int
	pixel     bool
}

type selectiveReaderState struct {
	ctx                   context.Context
	selectf               SelectiveReaderSelector
	frames                []selectiveReaderFrame
	stopped               bool
	skippingPixelSequence bool
}

type selectiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *selectiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type selectiveContextReadSeeker struct {
	ctx    context.Context
	source io.ReadSeeker
}

func (r *selectiveContextReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func (r *selectiveContextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Seek(offset, whence)
}

// NewSelectiveReader creates an opt-in Reader whose selector can materialize,
// skip, or stop at each data element header. NewReader remains the default fast
// path and does not allocate selective-reader state.
func NewSelectiveReader(ctx context.Context, source io.Reader, syntax transfer.Syntax, readerOpts ReaderOptions, opts SelectiveReaderOptions) (*Reader, error) {
	if source == nil || opts.Select == nil {
		return nil, ErrSelectiveReaderOptions
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader := NewReader(source, syntax, readerOpts)
	// Selection must happen before any value bytes are consumed. Retaining the
	// original source also enables seek-only skips on seekable inputs.
	reader.slurpEligible = false
	reader.selective = &selectiveReaderState{ctx: ctx, selectf: opts.Select}
	if readSeeker, ok := source.(io.ReadSeeker); ok {
		reader.counter.r = &selectiveContextReadSeeker{ctx: ctx, source: readSeeker}
	} else {
		reader.counter.r = &selectiveContextReader{ctx: ctx, reader: source}
	}
	return reader, nil
}

func (r *Reader) nextSelective() (Token, error) {
	state := r.selective
	if state.stopped {
		return Token{}, io.EOF
	}
	if err := state.ctx.Err(); err != nil {
		return Token{}, err
	}
	if tok, ok, err := r.updateSeqDelimiters(); ok || err != nil {
		if err == nil {
			r.advanceSelectivePath(tok)
		}
		return tok, err
	}
	if err := r.checkTotalBytes(); err != nil {
		return Token{}, err
	}
	headerOffset := r.Position()
	header, err := r.readHeader()
	if err != nil {
		if errors.Is(err, io.EOF) && len(r.seqDelimiters) > 0 {
			return Token{}, &ParseError{Op: OpReadTag, Offset: r.Position(), Err: io.ErrUnexpectedEOF}
		}
		return Token{}, err
	}
	if err := state.ctx.Err(); err != nil {
		return Token{}, err
	}
	if tok, ok, err := r.controlToken(header); ok || err != nil {
		tok.Offset = headerOffset
		if err == nil {
			r.advanceSelectivePath(tok)
		}
		return tok, err
	}
	if len(r.seqDelimiters) > 0 && r.seqDelimiters[len(r.seqDelimiters)-1].typ == seqTokenTypePixelSequence {
		return Token{}, &ParseError{
			Op: OpReadValue, Offset: r.Position(), Tag: header.Tag, VR: header.VR, Length: header.Length,
			Err: fmt.Errorf("dicom: unexpected tag %s inside encapsulated Pixel Data", header.Tag),
		}
	}

	disposition, err := r.callSelectiveReaderSelector(header, headerOffset)
	if err != nil {
		return Token{}, err
	}
	switch disposition {
	case SelectiveStop:
		state.stopped = true
		return Token{Kind: TokenStop, Header: header, Offset: headerOffset}, nil
	case SelectiveSkip:
		if isEncapsulatedPixelDataHeader(header) {
			tok, err := r.readSelectiveSkippedPixelSequenceToken(header, headerOffset)
			tok.Offset = headerOffset
			return tok, err
		}
		if header.VR == core.VRSQ || header.Length.IsUndefined() {
			return Token{}, r.selectiveDispositionError(header, headerOffset)
		}
		tok, err := r.readSelectiveSkippedValueToken(header)
		tok.Offset = headerOffset
		return tok, err
	case SelectiveMaterialize:
		// Continue through the ordinary materialization path below.
	default:
		return Token{}, r.selectiveDispositionError(header, headerOffset)
	}

	if header.VR == core.VRSQ || (header.VR == core.VRUN && header.Length.IsUndefined()) {
		if err := r.pushSequenceTokenWithImplicitVRLittleEndian(seqTokenTypeSequence, header.Length, header.VR == core.VRUN); err != nil {
			return Token{}, err
		}
		r.delimiterCheckPending = true
		tok := Token{Kind: TokenStartSequence, Header: header, Offset: headerOffset}
		r.advanceSelectivePath(tok)
		return tok, nil
	}
	if (header.VR == core.VROB || header.VR == core.VROW) && header.Tag == core.TagPixelData && header.Length.IsUndefined() {
		if err := r.pushSequenceToken(seqTokenTypePixelSequence, header.Length); err != nil {
			return Token{}, err
		}
		r.pixelSequenceOffsetTablePending = true
		r.delimiterCheckPending = true
		tok := Token{Kind: TokenStartPixelSequence, Header: header, Offset: headerOffset}
		r.advanceSelectivePath(tok)
		return tok, nil
	}
	if header.Length.IsUndefined() {
		return Token{}, &ParseError{
			Op: OpReadValue, Offset: r.Position(), Tag: header.Tag, VR: header.VR, Length: header.Length,
			Err: ErrUnsupportedUndefinedLength,
		}
	}
	tok, err := r.readDefinedValueToken(header)
	tok.Offset = headerOffset
	return tok, err
}

func isEncapsulatedPixelDataHeader(header core.ElementHeader) bool {
	return header.Tag == core.TagPixelData &&
		(header.VR == core.VROB || header.VR == core.VROW) &&
		header.Length.IsUndefined()
}

func (r *Reader) readSelectiveSkippedPixelSequenceToken(header core.ElementHeader, headerOffset int64) (tok Token, err error) {
	if err := r.pushSequenceToken(seqTokenTypePixelSequence, header.Length); err != nil {
		return Token{}, err
	}
	r.pixelSequenceOffsetTablePending = true
	r.delimiterCheckPending = true
	r.advanceSelectivePath(Token{Kind: TokenStartPixelSequence, Header: header, Offset: headerOffset})

	state := r.selective
	previousSkipPixelData := r.skipPixelData
	state.skippingPixelSequence = true
	r.skipPixelData = true
	defer func() {
		state.skippingPixelSequence = false
		r.skipPixelData = previousSkipPixelData
	}()
	if err := r.discardFragmentSequence(header); err != nil {
		return Token{}, err
	}
	if err := state.ctx.Err(); err != nil {
		return Token{}, err
	}
	return Token{Kind: TokenElement, Header: header, Element: core.Element{Header: header}}, nil
}

func (r *Reader) callSelectiveReaderSelector(header core.ElementHeader, offset int64) (disposition SelectiveDisposition, err error) {
	state := r.selective
	path := r.selectiveElementPath(header.Tag)
	defer func() {
		if recover() != nil {
			disposition = SelectiveMaterialize
			if ctxErr := state.ctx.Err(); ctxErr != nil {
				err = ctxErr
			} else {
				err = &ParseError{Op: OpReadValue, Offset: offset, Tag: header.Tag, VR: header.VR, Length: header.Length, Err: ErrSelectiveReaderCallback}
			}
		}
	}()
	disposition, callbackErr := state.selectf(state.ctx, path, header, offset)
	if ctxErr := state.ctx.Err(); ctxErr != nil {
		return SelectiveMaterialize, ctxErr
	}
	if callbackErr != nil {
		return SelectiveMaterialize, &ParseError{Op: OpReadValue, Offset: offset, Tag: header.Tag, VR: header.VR, Length: header.Length, Err: ErrSelectiveReaderCallback}
	}
	return disposition, nil
}

func (r *Reader) selectiveDispositionError(header core.ElementHeader, offset int64) error {
	return &ParseError{
		Op: OpReadValue, Offset: offset, Tag: header.Tag, VR: header.VR, Length: header.Length,
		Err: ErrSelectiveDisposition,
	}
}

func (r *Reader) selectiveElementPath(tag core.Tag) validation.Path {
	state := r.selective
	path := make(validation.Path, 0, len(state.frames)+1)
	for _, frame := range state.frames {
		if frame.pixel || frame.itemIndex < 0 {
			continue
		}
		path = append(path, validation.PathStep{Tag: frame.tag, ItemIndex: frame.itemIndex})
	}
	return append(path, validation.PathStep{Tag: tag, ItemIndex: validation.NoItem})
}

func (r *Reader) advanceSelectivePath(tok Token) {
	frames := &r.selective.frames
	switch tok.Kind {
	case TokenStartSequence:
		*frames = append(*frames, selectiveReaderFrame{tag: tok.Header.Tag, itemIndex: -1})
	case TokenStartPixelSequence:
		*frames = append(*frames, selectiveReaderFrame{tag: tok.Header.Tag, itemIndex: -1, pixel: true})
	case TokenStartItem:
		if len(*frames) > 0 && !(*frames)[len(*frames)-1].pixel {
			(*frames)[len(*frames)-1].itemIndex++
		}
	case TokenEndSequence:
		if len(*frames) > 0 {
			*frames = (*frames)[:len(*frames)-1]
		}
	}
}

func (r *Reader) readSelectiveSkippedValueToken(header core.ElementHeader) (Token, error) {
	if err := r.validateDefinedValueLength(header); err != nil {
		return Token{}, err
	}
	if r.maxElements > 0 && r.elementCount >= r.maxElements {
		return Token{}, &ParseError{
			Op: OpCheckElementCount, Offset: r.Position(), Tag: header.Tag, VR: header.VR, Length: header.Length,
			Err: fmt.Errorf("%w: got %d, limit %d", ErrMaxElementsExceeded, r.elementCount+1, r.maxElements),
		}
	}
	valueOffset := r.Position()
	if err := r.selectiveSkipN(valueOffset, header, int64(header.Length)); err != nil {
		return Token{}, err
	}
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{Kind: TokenElement, Header: header, Element: core.Element{Header: header}}, nil
}

func (r *Reader) selectiveSkipN(offset int64, header core.ElementHeader, n int64) error {
	if err := r.selective.ctx.Err(); err != nil {
		return err
	}
	seeker, ok := r.counter.r.(io.Seeker)
	if !ok {
		return r.skipN(offset, header, n)
	}
	if n == 0 {
		return nil
	}

	available, limitErr, err := r.selectiveSeekAvailable(seeker, n)
	if err != nil {
		return &ParseError{Op: OpReadValue, Offset: offset, Tag: header.Tag, VR: header.VR, Length: header.Length, Err: err}
	}
	if available > 0 {
		if _, err := seeker.Seek(available, io.SeekCurrent); err != nil {
			return &ParseError{Op: OpReadValue, Offset: offset, Tag: header.Tag, VR: header.VR, Length: header.Length, Err: err}
		}
		r.counter.pos += available
	}
	if limitErr != nil {
		return &ParseError{
			Op: OpReadValue, Offset: offset, Tag: header.Tag, VR: header.VR, Length: header.Length,
			Err: normalizeReadError(offset, r.Position(), limitErr),
		}
	}
	return nil
}

func (r *Reader) selectiveSeekAvailable(seeker io.Seeker, n int64) (int64, error, error) {
	available := n
	var limitErr error
	if r.maxTotalBytes > 0 {
		totalRemaining := r.maxTotalBytes - r.Position()
		if totalRemaining < 0 {
			totalRemaining = 0
		}
		if available > totalRemaining {
			available = totalRemaining
			limitErr = ErrMaxTotalBytesExceeded
		}
	}

	sourceRemaining := int64(-1)
	if r.sourceEnd > 0 {
		sourceRemaining = r.sourceEnd - r.Position()
	} else {
		current, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, nil, err
		}
		end, err := seeker.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, nil, err
		}
		if _, err := seeker.Seek(current, io.SeekStart); err != nil {
			return 0, nil, err
		}
		sourceRemaining = end - current
	}
	if sourceRemaining < 0 {
		sourceRemaining = 0
	}
	if available > sourceRemaining {
		available = sourceRemaining
		if limitErr == nil || sourceRemaining < r.maxTotalBytes-r.Position() {
			limitErr = io.ErrUnexpectedEOF
		}
	}
	return available, limitErr, nil
}
