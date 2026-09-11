package parser

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// EncodedFrame contains a complete codec-framed codestream, never just an Item.
// Data is owned by the receiver and is not reused. It has not been pixel-decoded.
type EncodedFrame struct {
	Index          int
	Data           []byte
	Metadata       FrameMetadata
	TransferSyntax transfer.Syntax
}

type EncodedFrameSink interface {
	HandleEncodedFrame(EncodedFrame) error
	Close() error
}
type EncodedFrameSinkFunc func(EncodedFrame) error

func (f EncodedFrameSinkFunc) HandleEncodedFrame(frame EncodedFrame) error {
	if f == nil {
		return nil
	}
	return f(frame)
}
func (EncodedFrameSinkFunc) Close() error { return nil }

// EncapsulatedSink is the parser-to-assembler contract for top-level Pixel Data.
// Items are NOT frames. Use pixeldata/encapsulated.NewStream for JPEG/JPEG-LS
// frame assembly. Callbacks are synchronous, with no parser-created goroutines.
// The Item reader is borrowed, limited to its value, valid only during the call
// and must be consumed completely. The parser owns sink finalization, not input
// closure. Direct Next callers must call Reader.Close when abandoning parsing.
type EncapsulatedSink interface {
	Context() context.Context
	CheckExtendedTable(core.ElementHeader) error
	ExtendedTable(core.Element) error
	StartPixelData(FrameMetadata, transfer.Syntax) error
	Item(length uint32, basic bool, value io.Reader) error
	EndPixelData() error
	Close() error
}

// Close finalizes reader-owned sinks exactly once. It does not close the input.
// Callers using Next must finish an active Next call before calling Close.
func (r *Reader) Close() error {
	return r.closeSinks(nil)
}

func (r *Reader) closeSinks(err error) error {
	err = r.closeFrameSink(err)
	if r != nil && r.encapsulatedSink != nil && !r.encapsulatedSinkClosed {
		r.encapsulatedSinkClosed = true
		err = errors.Join(err, r.encapsulatedSink.Close())
	}
	return err
}

func (r *Reader) encodedContextError() error {
	if r.encapsulatedSink != nil {
		if ctx := r.encapsulatedSink.Context(); ctx != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: nil encoded stream context", ErrFrameSink)
	}
	return nil
}

func isExtendedTable(tag core.Tag) bool {
	return tag == core.NewTag(0x7fe0, 1) || tag == core.NewTag(0x7fe0, 2)
}

func (r *Reader) streamEncodedPixelData(header core.ElementHeader, headerOffset int64) (Token, error) {
	if len(r.seqDelimiters) != 1 || r.skipPixelData || r.encapsulatedSinkClosed || r.encodedPixelSeen {
		return Token{}, fmt.Errorf("%w: encoded streaming requires a single top-level Pixel Data value and no skip policy", ErrFrameSink)
	}
	r.encodedPixelSeen = true
	metadata, err := r.frameMetadata.complete()
	if err != nil {
		return Token{}, err
	}
	if err := r.encapsulatedSink.StartPixelData(metadata, r.syntax); err != nil {
		return Token{}, err
	}
	start := r.Position()
	r.encodedPixelActive = true
	defer func() { r.encodedPixelActive = false }()
	for {
		tok, err := r.Next()
		if err != nil {
			return Token{}, err
		}
		if tok.Kind == TokenEndSequence {
			if r.pixelSequenceOffsetTablePending {
				return Token{}, ErrMissingBasicOffsetTable
			}
			if err := r.encapsulatedSink.EndPixelData(); err != nil {
				return Token{}, err
			}
			var value core.Value = core.DiscardedValue{}
			if r.deferPixelData {
				r.recordValueLocation(header, start, r.Position()-start)
				value = nil
			}
			return Token{Kind: TokenElement, Header: header, Offset: headerOffset, Element: core.Element{Header: header, Value: value}}, nil
		}
		if tok.Kind != TokenElement || !tok.Header.Tag.IsItem() {
			return Token{}, ErrUnexpectedSequenceControlTag
		}
	}
}

func (r *Reader) streamEncodedItem(header core.ElementHeader) (Token, error) {
	if err := r.validateDefinedValueLength(header); err != nil {
		return Token{}, err
	}
	if err := r.checkElementCountLimit(header); err != nil {
		return Token{}, err
	}
	if err := r.checkElementByteLimit(header); err != nil {
		return Token{}, err
	}
	limited := &io.LimitedReader{R: r.counter, N: int64(header.Length)}
	if err := r.encapsulatedSink.Item(uint32(header.Length), r.pixelSequenceOffsetTablePending, limited); err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrFrameSink, err)
	}
	if limited.N != 0 {
		return Token{}, fmt.Errorf("%w: Item callback did not consume value", ErrFrameSink)
	}
	if r.pixelSequenceOffsetTablePending {
		r.pixelSequenceOffsetTablePending = false
	} else {
		r.fragmentCount++
	}
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{Kind: TokenElement, Header: header}, nil
}
