package parser

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
)

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
	if err := r.reservePixelDataBytes(header); err != nil {
		return Token{}, err
	}
	if err := r.checkFragmentCountLimit(header); err != nil {
		return Token{}, err
	}

	if r.encodedPixelActive {
		return r.streamEncodedItem(header)
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
	if err := r.checkSequenceDepthLimit(length); err != nil {
		return err
	}
	implicitVRLittleEndian := r.implicitVRLittleEndianActive()
	if force {
		implicitVRLittleEndian = true
	}
	var privateScope *dictionary.PrivateReservations
	if typ == seqTokenTypeItem && r.privateRoot != nil {
		var err error
		privateScope, err = dictionary.NewPrivateReservations(r.maxPrivateCreators)
		if err != nil {
			return err
		}
	}
	r.seqDelimiters = append(r.seqDelimiters, seqToken{
		privateScope:           privateScope,
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

// EOF is normal only before the first byte of a new element's tag. Once a
// header field or declared value is required, even a zero-byte EOF is truncation.
func requiredReadError(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
