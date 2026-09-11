package encapsulated

import (
	"context"
	"fmt"
)

const (
	scanSOI = iota
	scanSOICode
	scanMarker
	scanMarkerCode
	scanLengthHigh
	scanLengthLow
	scanPayload
	scanEntropy
	scanEntropyCode
	scanEnd
)

// markerScanner identifies framing, not coding parameters. In particular APP,
// COM, tables and scan headers are opaque length-delimited payloads: embedded
// FF D9, FF D8 and DICOM delimiter bytes there have no framing significance.
type markerScanner struct {
	state, length, remaining int
	marker                   byte
	format                   Format
	resumeEntropy            bool
}

// ValidateFrame checks that a buffer contains exactly one structurally framed
// JPEG/JPEG-LS codestream, with at most one zero pad after EOI. It does not
// validate coding parameters or entropy. Decoders that otherwise ignore bytes
// after EOI can use it even when an offset table already supplied the bounds.
func ValidateFrame(ctx context.Context, data []byte, format Format) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if format != JPEG && format != JPEGLS {
		return ErrLayout
	}
	s := markerScanner{format: format}
	for i, b := range data {
		if i%(32<<10) == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		end, err := s.feed(b)
		if err != nil {
			return err
		}
		if end {
			remaining := data[i+1:]
			if len(remaining) > 1 || len(remaining) == 1 && remaining[0] != 0 {
				return fmt.Errorf("%w: trailing bytes after EOI", ErrLayout)
			}
			return ctx.Err()
		}
	}
	return fmt.Errorf("%w: missing EOI", ErrLayout)
}

func (s *markerScanner) feed(b byte) (bool, error) {
	switch s.state {
	case scanSOI:
		if b != 0xff {
			return false, fmt.Errorf("%w: missing SOI", ErrLayout)
		}
		s.state = scanSOICode
	case scanSOICode:
		if b != 0xd8 {
			return false, fmt.Errorf("%w: missing SOI", ErrLayout)
		}
		s.state = scanMarker
	case scanMarker:
		if b != 0xff {
			return false, fmt.Errorf("%w: expected marker", ErrLayout)
		}
		s.state = scanMarkerCode
	case scanMarkerCode:
		if b == 0xff {
			return false, nil
		}
		return s.beginMarker(b, false)
	case scanLengthHigh:
		s.length = int(b) << 8
		s.state = scanLengthLow
	case scanLengthLow:
		s.length |= int(b)
		if s.length < 2 {
			return false, fmt.Errorf("%w: invalid marker length", ErrLayout)
		}
		s.remaining = s.length - 2
		s.state = scanPayload
		if s.remaining == 0 {
			s.endSegment()
		}
	case scanPayload:
		s.remaining--
		if s.remaining == 0 {
			s.endSegment()
		}
	case scanEntropy:
		if b == 0xff {
			s.state = scanEntropyCode
		}
	case scanEntropyCode:
		if b == 0xff {
			return false, nil
		}
		if b == 0 || s.format == JPEGLS && b < 0x80 {
			s.state = scanEntropy
			return false, nil
		}
		return s.beginMarker(b, true)
	case scanEnd:
		return false, fmt.Errorf("%w: data after EOI", ErrLayout)
	}
	return false, nil
}

func (s *markerScanner) beginMarker(b byte, entropy bool) (bool, error) {
	if b == 0 || b == 0xd8 {
		return false, fmt.Errorf("%w: invalid/nested marker", ErrLayout)
	}
	if b == 0xd9 {
		s.state = scanEnd
		return true, nil
	}
	if b == 0x01 || b >= 0xd0 && b <= 0xd7 {
		if !entropy {
			return false, fmt.Errorf("%w: standalone marker outside scan", ErrLayout)
		}
		s.state = scanEntropy
		return false, nil
	}
	s.marker = b
	s.resumeEntropy = entropy && b == 0xdc // DNL resumes the interrupted scan.
	s.state = scanLengthHigh
	return false, nil
}
func (s *markerScanner) endSegment() {
	s.state = scanMarker
	if s.marker == 0xda || s.resumeEntropy {
		s.state = scanEntropy
	}
}

func inferFrames(ctx context.Context, p *Plan, frames int, format Format, limits Limits) ([]int, error) {
	starts := make([]int, 0, frames)
	starts = append(starts, 0)
	scanner := markerScanner{format: format}
	buffer := make([]byte, 32<<10)
	completed := 0
	var frameBytes uint64
	for i, size := range p.sizes {
		if i-starts[len(starts)-1]+1 > limits.MaxFragmentsPerFrame || size > limits.MaxFrameBytes-frameBytes {
			return nil, ErrResourceLimit
		}
		frameBytes += size
		end := false
		var eoiOffset uint64
		for offset := uint64(0); offset < size; {
			chunk := min(uint64(len(buffer)), size-offset)
			data := buffer[:int(chunk)]
			if err := readExact(ctx, p.source, i, data, int64(offset)); err != nil {
				return nil, err
			}
			for j, b := range data {
				position := offset + uint64(j)
				if end {
					if b != 0 || position != eoiOffset+1 || position+1 != size {
						return nil, fmt.Errorf("%w: frame ends inside an Item or invalid padding", ErrLayout)
					}
					continue
				}
				var err error
				end, err = scanner.feed(b)
				if err != nil {
					return nil, err
				}
				if end {
					eoiOffset = position
				}
			}
			offset += chunk
		}
		if !end {
			if size&1 != 0 {
				return nil, fmt.Errorf("%w: odd non-final fragment", ErrLayout)
			}
			continue
		}
		completed++
		if completed > frames || completed == frames && i+1 < len(p.sizes) {
			return nil, fmt.Errorf("%w: %w", ErrLayout, ErrFrameCount)
		}
		if i+1 < len(p.sizes) {
			starts = append(starts, i+1)
			scanner = markerScanner{format: format}
			frameBytes = 0
		}
	}
	if completed != frames || len(starts) != frames {
		return nil, fmt.Errorf("%w: %w: incomplete codestream boundaries", ErrLayout, ErrFrameCount)
	}
	return starts, nil
}
