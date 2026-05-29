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

var errLifecycleFiltered = errors.New("dicom: lifecycle hook filtered element")

type readerLifecycleFrame struct {
	tag       core.Tag
	itemIndex int
	pixel     bool
}

type readerValidationLifecycle struct {
	operation *validation.Operation
	frames    []readerLifecycleFrame
	nodes     int
	maxNodes  int
}

// NewReaderWithValidation creates a Reader with one frozen, opt-in validation
// operation. NewReader remains the unchanged default path and never runs hooks
// or validation rules. Header and decoded-element hooks are available while
// iterating tokens; complete item/sequence/dataset phases and dataset rules
// require ReadDataSet, which owns the materialized hierarchy.
func NewReaderWithValidation(ctx context.Context, source io.Reader, syntax transfer.Syntax, readerOpts ReaderOptions, validationOpts validation.Options) (*Reader, error) {
	maxElements := validationOpts.MaxElements
	if maxElements == 0 {
		maxElements = validation.DefaultMaxElements
	}
	maxDepth := validationOpts.MaxDepth
	if maxDepth == 0 {
		maxDepth = validation.DefaultMaxDepth
	}
	maxInt := int(^uint(0) >> 1)
	parserMaxDepth := maxInt
	if maxDepth <= (maxInt-1)/2 {
		// The parser counts sequence and item frames separately. One additional
		// frame permits encapsulated Pixel Data at the deepest valid item level.
		parserMaxDepth = maxDepth*2 + 1
	}
	if readerOpts.MaxSequenceDepth == 0 || readerOpts.MaxSequenceDepth > parserMaxDepth {
		readerOpts.MaxSequenceDepth = parserMaxDepth
	}
	if validationOpts.Dictionary == nil {
		validationOpts.Dictionary = readerOpts.Dictionary
	}
	if validationOpts.ByteOrder == nil {
		validationOpts.ByteOrder = syntax.ByteOrder
	}
	validationOpts.TransferSyntax = syntax
	operation, err := validation.NewOperation(ctx, validationOpts)
	if err != nil {
		return nil, err
	}
	reader := NewReader(source, syntax, readerOpts)
	// A header hook may request defer after earlier values have been read. Keep
	// the original seekable source instead of replacing it with the slurp arena.
	reader.slurpEligible = false
	reader.validationLifecycle = &readerValidationLifecycle{operation: operation, maxNodes: maxElements}
	return reader, nil
}

// ValidationReport returns a detached snapshot of the operation report.
func (r *Reader) ValidationReport() validation.Report {
	if r == nil || r.validationLifecycle == nil {
		return validation.Report{}
	}
	return r.validationLifecycle.operation.Report()
}

func (r *Reader) lifecycleEnabled(requested bool) bool {
	return requested && r != nil && r.validationLifecycle != nil && r.validationSuppressed == 0
}

func (r *Reader) handleLifecycleHeader(header core.ElementHeader, offset int64, requested bool) (validation.HookResult, error) {
	if !r.lifecycleEnabled(requested) {
		return validation.HookResult{}, nil
	}
	path := r.lifecycleElementPath(header.Tag)
	if r.lastReservedNonZero {
		r.validationLifecycle.operation.AddFinding(validation.Finding{
			Path: path, Tag: header.Tag, VR: header.VR, Code: validation.CodeReservedBytes,
			Offset: r.lastReservedOffset, OffsetSet: true, Message: "explicit VR reserved bytes are non-zero",
		})
		r.lastReservedNonZero = false
	}
	return r.validationLifecycle.operation.Handle(validation.HookEvent{
		Point: validation.HookElementHeaderRead, Path: path, Header: &header,
		Offset: offset, OffsetSet: true,
	})
}

func (r *Reader) finishLifecycleToken(tok Token, err error, requested bool) (Token, error) {
	if err != nil {
		return tok, err
	}
	if requested && r != nil && r.validationLifecycle != nil && r.validationSuppressed > 0 {
		// Replay scans must not emit hooks or consume validation budgets, but they
		// still rebuild the structural path for parsing that continues afterward.
		r.advanceLifecyclePath(tok)
		return tok, nil
	}
	if !r.lifecycleEnabled(requested) {
		return tok, nil
	}
	if r.lifecycleTokenConsumesNode(tok) {
		r.validationLifecycle.nodes++
		if r.validationLifecycle.maxNodes > 0 && r.validationLifecycle.nodes > r.validationLifecycle.maxNodes {
			return Token{}, &ParseError{
				Op: OpCheckElementCount, Offset: tok.Offset, Tag: tok.Header.Tag, VR: tok.Header.VR, Length: tok.Header.Length,
				Err: fmt.Errorf("%w: got %d element/item nodes, limit %d", ErrMaxElementsExceeded, r.validationLifecycle.nodes, r.validationLifecycle.maxNodes),
			}
		}
	}
	if tok.Kind == TokenElement && !tok.Header.Tag.IsSequenceDelimiting() {
		path := r.lifecycleElementPath(tok.Element.Tag())
		result, hookErr := r.validationLifecycle.operation.Handle(validation.HookEvent{
			Point: validation.HookAfterElement, Path: path, Header: &tok.Header, Element: &tok.Element,
			Offset: tok.Offset, OffsetSet: true,
		})
		if hookErr != nil {
			return Token{}, hookErr
		}
		if result.Filter {
			return Token{}, errLifecycleFiltered
		}
		if result.Element != nil {
			if err := validateDeferredLifecycleReplacement(tok.Element, *result.Element); err != nil {
				return Token{}, err
			}
			tok.Element = *result.Element
			tok.Header = tok.Element.Header
		}
	}
	r.advanceLifecyclePath(tok)
	return tok, nil
}

func (r *Reader) handleCompletedElement(path validation.Path, offset int64, completionPoint validation.HookPoint, element core.Element) (core.Element, bool, error) {
	if r == nil || r.validationLifecycle == nil || r.validationSuppressed != 0 {
		return element, false, nil
	}
	if completionPoint != "" {
		result, err := r.validationLifecycle.operation.Handle(validation.HookEvent{
			Point: completionPoint, Path: path, Header: &element.Header, Element: &element,
			Offset: offset, OffsetSet: true,
		})
		if err != nil {
			return core.Element{}, false, err
		}
		if result.Filter {
			return element, true, nil
		}
		if result.Element != nil {
			if err := validateDeferredLifecycleReplacement(element, *result.Element); err != nil {
				return core.Element{}, false, err
			}
			element = *result.Element
			path = path.Clone()
			path[len(path)-1].Tag = element.Tag()
		}
	}
	result, err := r.validationLifecycle.operation.Handle(validation.HookEvent{
		Point: validation.HookAfterElement, Path: path, Header: &element.Header, Element: &element,
		Offset: offset, OffsetSet: true,
	})
	if err != nil {
		return core.Element{}, false, err
	}
	if result.Filter {
		return element, true, nil
	}
	if result.Element != nil {
		if err := validateDeferredLifecycleReplacement(element, *result.Element); err != nil {
			return core.Element{}, false, err
		}
		element = *result.Element
	}
	return element, false, nil
}

func (r *Reader) lifecycleTokenConsumesNode(tok Token) bool {
	switch tok.Kind {
	case TokenStartSequence, TokenStartPixelSequence:
		return true
	case TokenElement:
		return !r.lifecycleInsidePixelSequence()
	case TokenStartItem:
		return !r.lifecycleInsidePixelSequence()
	default:
		return false
	}
}

func (r *Reader) lifecycleInsidePixelSequence() bool {
	if r == nil || r.validationLifecycle == nil || len(r.validationLifecycle.frames) == 0 {
		return false
	}
	return r.validationLifecycle.frames[len(r.validationLifecycle.frames)-1].pixel
}

func (r *Reader) lifecycleElementPath(tag core.Tag) validation.Path {
	if r == nil || r.validationLifecycle == nil {
		return nil
	}
	path := make(validation.Path, 0, len(r.validationLifecycle.frames)+1)
	for _, frame := range r.validationLifecycle.frames {
		if frame.pixel || frame.itemIndex < 0 {
			continue
		}
		path = append(path, validation.PathStep{Tag: frame.tag, ItemIndex: frame.itemIndex})
	}
	return append(path, validation.PathStep{Tag: tag, ItemIndex: validation.NoItem})
}

func (r *Reader) advanceLifecyclePath(tok Token) {
	frames := &r.validationLifecycle.frames
	switch tok.Kind {
	case TokenStartSequence:
		*frames = append(*frames, readerLifecycleFrame{tag: tok.Header.Tag, itemIndex: -1})
	case TokenStartPixelSequence:
		*frames = append(*frames, readerLifecycleFrame{tag: tok.Header.Tag, itemIndex: -1, pixel: true})
	case TokenStartItem:
		if len(*frames) > 0 && !(*frames)[len(*frames)-1].pixel {
			(*frames)[len(*frames)-1].itemIndex++
		}
	case TokenEndItem:
		if len(*frames) > 0 && !(*frames)[len(*frames)-1].pixel {
			// Keep the completed index available until the next item starts; no
			// ordinary element can occur between an item end and that boundary.
		}
	case TokenEndSequence:
		if len(*frames) > 0 {
			*frames = (*frames)[:len(*frames)-1]
		}
	}
}

func (r *Reader) readLifecycleSkippedValueToken(header core.ElementHeader, deferValue bool) (Token, error) {
	if err := r.validateDefinedValueLength(header); err != nil {
		return Token{}, err
	}
	if r.maxElements > 0 && r.elementCount >= r.maxElements {
		return Token{}, &ParseError{
			Op: OpCheckElementCount, Offset: r.Position(), Tag: header.Tag, VR: header.VR, Length: header.Length,
			Err: fmt.Errorf("%w: got %d, limit %d", ErrMaxElementsExceeded, r.elementCount+1, r.maxElements),
		}
	}
	if r.maxElementBytes > 0 && int64(header.Length) > r.maxElementBytes {
		return Token{}, &ParseError{
			Op: OpReadValue, Offset: r.Position(), Tag: header.Tag, VR: header.VR, Length: header.Length,
			Err: fmt.Errorf("%w: element %s length %d exceeds limit %d", ErrMaxElementBytesExceeded, header.Tag, header.Length, r.maxElementBytes),
		}
	}
	if deferValue {
		for _, frame := range r.validationLifecycle.frames {
			if !frame.pixel && frame.itemIndex >= 0 {
				return Token{}, fmt.Errorf("%w: defer hook does not support values nested in sequence items", validation.ErrHookAction)
			}
		}
		if _, ok := r.counter.r.(io.ReadSeeker); !ok {
			return Token{}, fmt.Errorf("%w: defer hook requires a seekable source", validation.ErrHookAction)
		}
	}
	valueOffset := r.Position()
	if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
		return Token{}, err
	}
	if deferValue {
		r.recordValueLocation(header, valueOffset, int64(header.Length))
	}
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{Kind: TokenElement, Header: header, Element: core.Element{Header: header}}, nil
}

func validateDeferredLifecycleReplacement(original, replacement core.Element) error {
	if original.Value != nil || replacement.Value != nil || original.Header == replacement.Header {
		return nil
	}
	return fmt.Errorf("%w: a skipped or deferred element replacement without a value must preserve its header", validation.ErrHookAction)
}
