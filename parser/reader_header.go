package parser

import (
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
)

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
	var header core.ElementHeader
	if r.syntax.ExplicitVR && !r.implicitVRLittleEndianActive() {
		header, err = r.readExplicitHeader(tag)
	} else {
		header, err = r.readImplicitHeader(tag)
	}
	// A malformed SQ/UN creator may never enter the primitive-value path.
	// Record its invalid reservation before entering any child item scope.
	if err == nil && header.VR != core.VRLO {
		err = r.capturePrivateReservation(core.Element{Header: header})
	}
	return header, err
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
			Err:    requiredReadError(err),
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
				Err:    requiredReadError(err),
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
				Err:    requiredReadError(err),
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
			Err:    requiredReadError(err),
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
// in-memory header. An opt-in PrivateDataDictionary resolves creator-relative
// definitions against this dataset/item's reservations, without inheritance.
func (r *Reader) lookupImplicitVR(tag core.Tag) core.VR {
	if tag == core.TagPixelData {
		return core.VROW
	}
	if tag.Element == 0x3000 && tag.Group>>8 == 0x60 && tag.Group&1 == 0 {
		return core.VROW
	}
	if r.privateRoot != nil && tag.IsPrivate() {
		return r.lookupPrivateVR(tag)
	}
	return dictionary.LookupVR(r.dict, tag)
}
