package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
)

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
func (r *Reader) CopyElementValueAt(location ValueLocation, w io.Writer) (copied int64, err error) {
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
	currentSeek, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fmt.Errorf("dicom: snapshot replay position: %w", err)
	}
	currentPos := r.counter.pos
	defer func() {
		if _, restoreErr := rs.Seek(currentSeek, io.SeekStart); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("dicom: restore replay position: %w", restoreErr))
		}
		r.counter.pos = currentPos
	}()

	valueStartSeek := r.rootOffset + (location.ValueOffset - r.baseOffset)
	if _, err := rs.Seek(valueStartSeek, io.SeekStart); err != nil {
		return 0, err
	}
	copied, err = io.CopyN(w, rs, location.Length)
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
	if r.valueLocationGeneration > 0 {
		if _, exists := r.seenValueLocations[location]; exists {
			return
		}
		r.seenValueLocations[location] = struct{}{}
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

func (r *Reader) beginValueLocationGeneration() {
	if r == nil {
		return
	}
	r.valueLocationGeneration++
	if r.valueLocationGeneration == 0 {
		// Keep zero reserved for the allocation-free initial generation.
		r.valueLocationGeneration = 1
	}
	if r.seenValueLocations != nil {
		return
	}
	locationCount := 0
	for _, locations := range r.allValueLocations {
		locationCount += len(locations)
	}
	r.seenValueLocations = make(map[ValueLocation]struct{}, locationCount)
	for _, locations := range r.allValueLocations {
		for _, location := range locations {
			r.seenValueLocations[location] = struct{}{}
		}
	}
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
	r.beginValueLocationGeneration()
	// Reset parser state for a fresh scan.
	r.counter.pos = r.baseOffset
	r.seqDelimiters = r.seqDelimiters[:0]
	r.resetPrivateScopes()
	r.delimiterCheckPending = false
	r.pixelSequenceOffsetTablePending = false
	r.elementCount = 0
	r.fragmentCount = 0
	r.pixelDataBytes = 0
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
