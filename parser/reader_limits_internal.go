package parser

import (
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
)

func (r *Reader) checkElementCountLimit(header core.ElementHeader) error {
	if r.maxElements <= 0 || r.elementCount < r.maxElements {
		return nil
	}
	return &ParseError{
		Op:     OpCheckElementCount,
		Offset: r.Position(),
		Tag:    header.Tag,
		VR:     header.VR,
		Length: header.Length,
		Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxElementsExceeded, r.elementCount+1, r.maxElements),
	}
}

func (r *Reader) checkFragmentCountLimit(header core.ElementHeader) error {
	if r.pixelSequenceOffsetTablePending || r.maxFragments <= 0 || r.fragmentCount < r.maxFragments {
		return nil
	}
	return &ParseError{
		Op:     OpCheckFragmentCount,
		Offset: r.Position(),
		Tag:    header.Tag,
		VR:     header.VR,
		Length: header.Length,
		Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxFragmentsExceeded, r.fragmentCount+1, r.maxFragments),
	}
}

func (r *Reader) checkSequenceDepthLimit(length core.Length) error {
	if r.maxSequenceDepth <= 0 || len(r.seqDelimiters)+1 <= r.maxSequenceDepth {
		return nil
	}
	return &ParseError{
		Op:     OpCheckDepth,
		Offset: r.Position(),
		Length: length,
		Err:    fmt.Errorf("%w: got %d, limit %d", ErrMaxDepthExceeded, len(r.seqDelimiters)+1, r.maxSequenceDepth),
	}
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
	// an error (including EOF). A declared value is required even when no payload
	// byte was available, so every EOF here is truncation.
	if _, err := io.CopyN(io.Discard, r.counter, n); err != nil {
		return &ParseError{
			Op:     OpReadValue,
			Offset: offset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    requiredReadError(err),
		}
	}
	return nil
}

func (r *Reader) checkElementByteLimit(header core.ElementHeader) error {
	limit := r.maxElementBytes
	limitErr := ErrMaxElementBytesExceeded
	if isPixelDataValueTag(header.Tag) && r.maxPixelDataBytes > 0 {
		limit = r.maxPixelDataBytes
		limitErr = ErrMaxPixelDataBytesExceeded
	} else if header.Tag == tagVectorGridData && r.maxVectorGridBytes > 0 {
		limit = r.maxVectorGridBytes
		limitErr = ErrMaxDeformableVectorGridBytesExceeded
	}
	if limit <= 0 || int64(header.Length) <= limit {
		return nil
	}
	return &ParseError{
		Op: OpReadValue, Offset: r.Position(), Tag: header.Tag, VR: header.VR, Length: header.Length,
		Err: fmt.Errorf("%w: value length exceeds configured limit", limitErr),
	}
}

func (r *Reader) reservePixelDataBytes(header core.ElementHeader) error {
	if r.maxPixelDataBytes <= 0 {
		return nil
	}
	length := int64(header.Length)
	if length < 0 || r.pixelDataBytes > r.maxPixelDataBytes || length > r.maxPixelDataBytes-r.pixelDataBytes {
		return &ParseError{
			Op: OpReadValue, Offset: r.Position(), Tag: core.TagPixelData, VR: header.VR, Length: header.Length,
			Err: fmt.Errorf("%w: encapsulated value exceeds configured limit", ErrMaxPixelDataBytesExceeded),
		}
	}
	r.pixelDataBytes += length
	return nil
}

func isPixelDataValueTag(tag core.Tag) bool {
	return tag == core.TagPixelData || tag == tagFloatPixelData || tag == tagDoubleFloatPixelData
}
