package encapsulated

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Stream assembles already parsed Items and synchronously delivers complete
// encoded JPEG/JPEG-LS frames. Install it as object.ReadFileOptions.EncapsulatedSink.
// Retention is controlled separately by DeferPixelData on the reader options.
// A successful callback is provisional: only a successful complete file read
// means all frames, tables and trailing dataset bytes were valid.
type Stream struct {
	ctx                                context.Context
	cancel                             context.CancelFunc
	sink                               parser.EncodedFrameSink
	limits                             Limits
	once                               sync.Once
	eotObject                          *object.Object
	tables                             Tables
	metadata                           parser.FrameMetadata
	syntax                             transfer.Syntax
	format                             Format
	started, ended, bot                bool
	offsets                            []uint64
	itemOffset, inputBytes, frameBytes uint64
	fragments, delivered               int
	parts                              [][]byte
	scanner                            markerScanner
}

// NewStream takes ownership of sink finalization after success. The parser or
// high-level object reader closes it once, including early errors. It never
// closes the input. Cancellation is checked between bounded reads and deliveries;
// interrupt a blocked input with a transport deadline or by closing owned input.
// Close may race with delivery when the downstream sink supports that contract.
func NewStream(ctx context.Context, sink parser.EncodedFrameSink, limits Limits) (*Stream, error) {
	if ctx == nil || sink == nil {
		return nil, fmt.Errorf("%w: nil stream context or sink", ErrLayout)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	ownedContext, cancel := context.WithCancel(ctx)
	return &Stream{ctx: ownedContext, cancel: cancel, sink: sink, limits: l, eotObject: object.FromElements(nil, nil)}, nil
}
func (s *Stream) Context() context.Context { return s.ctx }
func (s *Stream) Close() (err error) {
	s.once.Do(func() { s.cancel(); err = s.sink.Close() })
	return err
}

func (s *Stream) CheckExtendedTable(h core.ElementHeader) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.started || (h.Tag != tagExtendedOffsetTable && h.Tag != tagExtendedOffsetTableLengths) || h.VR != core.VROV || h.Length.IsUndefined() || h.Length == 0 || h.Length%8 != 0 {
		return fmt.Errorf("%w: invalid or late EOT attribute", ErrLayout)
	}
	if uint64(h.Length)/8 > uint64(s.limits.MaxFrames) {
		return ErrResourceLimit
	}
	if _, ok := s.eotObject.Get(h.Tag); ok {
		return fmt.Errorf("%w: duplicate EOT attribute", ErrLayout)
	}
	return nil
}
func (s *Stream) ExtendedTable(e core.Element) error {
	if err := s.CheckExtendedTable(e.Header); err != nil {
		return err
	}
	s.eotObject.Put(e)
	return nil
}
func (s *Stream) StartPixelData(metadata parser.FrameMetadata, syntax transfer.Syntax) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.started {
		return ErrLayout
	}
	if metadata.NumberOfFrames <= 0 {
		return ErrFrameCount
	}
	if metadata.NumberOfFrames > s.limits.MaxFrames || uint64(metadata.NumberOfFrames) > math.MaxInt32 {
		return ErrResourceLimit
	}
	switch syntax.UID {
	case transfer.JPEGBaseline.UID, transfer.JPEGExtended.UID,
		transfer.JPEGLosslessNonHierarchical.UID, transfer.JPEGLosslessSV1.UID:
		s.format = JPEG
	case transfer.JPEGLSLossless.UID, transfer.JPEGLSNearLossless.UID:
		s.format = JPEGLS
	default:
		return fmt.Errorf("%w: unsupported streaming transfer syntax", ErrLayout)
	}
	t, err := ReadTables(s.eotObject, nil, metadata.NumberOfFrames)
	if err != nil {
		return err
	}
	if t.ExtendedPresent != t.LengthsPresent {
		return fmt.Errorf("%w: incomplete EOT pair", ErrLayout)
	}
	if t.ExtendedPresent {
		if err := validateOffsetOrder(t.Extended); err != nil {
			return err
		}
		for _, length := range t.Lengths {
			if length == 0 {
				return ErrLayout
			}
			if length > s.limits.MaxFrameBytes {
				return ErrResourceLimit
			}
		}
		s.offsets = t.Extended
	}
	s.tables, s.metadata, s.syntax = t, metadata, syntax
	s.scanner = markerScanner{format: s.format}
	s.started = true
	return nil
}

func (s *Stream) Item(length uint32, basic bool, value io.Reader) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if !s.started || s.ended {
		return ErrLayout
	}
	if basic {
		if s.bot || uint64(length)/4 > uint64(s.limits.MaxFrames) || uint64(length) > uint64(math.MaxInt) {
			return ErrResourceLimit
		}
		if length != 0 && (uint64(length) != uint64(s.metadata.NumberOfFrames)*4 || s.tables.ExtendedPresent) {
			return ErrLayout
		}
		if uint64(length) > s.limits.MaxBytes-s.inputBytes {
			return ErrResourceLimit
		}
		data := make([]byte, int(length))
		if err := s.read(value, data); err != nil {
			return err
		}
		if length != 0 {
			s.offsets = make([]uint64, s.metadata.NumberOfFrames)
			for i := range s.offsets {
				s.offsets[i] = uint64(binary.LittleEndian.Uint32(data[i*4:]))
			}
			if err := validateOffsetOrder(s.offsets); err != nil {
				return err
			}
		}
		s.bot = true
		s.inputBytes += uint64(length)
		return nil
	}
	if !s.bot || s.delivered >= s.metadata.NumberOfFrames {
		return fmt.Errorf("%w: unexpected fragment", ErrFrameCount)
	}
	if length < 2 || length&1 != 0 || length == math.MaxUint32 {
		return ErrLayout
	}
	if s.fragments >= s.limits.MaxFragments || len(s.parts) >= s.limits.MaxFragmentsPerFrame || uint64(length) > uint64(math.MaxInt) ||
		uint64(length) > s.limits.MaxFrameBytes-s.frameBytes || uint64(length) > s.limits.MaxBytes-s.inputBytes {
		return ErrResourceLimit
	}
	if len(s.parts) == 0 && len(s.offsets) != 0 && s.offsets[s.delivered] != s.itemOffset {
		return fmt.Errorf("%w: frame offset not at Item boundary", ErrLayout)
	}
	if s.tables.ExtendedPresent && len(s.parts) != 0 {
		return fmt.Errorf("%w: EOT frame spans Items", ErrLayout)
	}
	data := make([]byte, int(length))
	if err := s.read(value, data); err != nil {
		return err
	}
	s.fragments++
	s.inputBytes += uint64(length)
	s.frameBytes += uint64(length)
	nextOffset, err := nextFragmentItemOffset(s.itemOffset, len(data))
	if err != nil {
		return err
	}
	end, eoiLength := false, 0
	for i, b := range data {
		if i%(32<<10) == 0 {
			if err := s.ctx.Err(); err != nil {
				return err
			}
		}
		if end {
			if i != len(data)-1 || i != eoiLength || b != 0 {
				return fmt.Errorf("%w: frame ends inside Item", ErrLayout)
			}
			continue
		}
		end, err = s.scanner.feed(b)
		if err != nil {
			return err
		}
		if end {
			eoiLength = i + 1
		}
	}
	if len(s.offsets) != 0 && s.delivered+1 < len(s.offsets) {
		nextFrame := s.offsets[s.delivered+1]
		if nextOffset > nextFrame || end && nextOffset != nextFrame || !end && nextOffset == nextFrame {
			return fmt.Errorf("%w: BOT contradicts codestream boundary", ErrLayout)
		}
	}
	if s.tables.ExtendedPresent {
		if !end || s.tables.Lengths[s.delivered] != uint64(eoiLength) {
			return fmt.Errorf("%w: EOT length contradicts codestream boundary", ErrLayout)
		}
	}
	s.itemOffset = nextOffset
	s.parts = append(s.parts, data)
	if !end {
		return nil
	}
	// Reuse the same bounded Item-range/ownership implementation as Decode.
	plan, err := New(s.ctx, Memory(s.parts), Tables{Basic: []byte{0, 0, 0, 0}}, 1, s.format, s.limits)
	if err != nil {
		return err
	}
	view, err := plan.Frame(s.ctx, 0)
	if err != nil {
		return err
	}
	// Even a borrowed Plan view belongs to this stream's newly allocated Item.
	// Transfer it to the receiver and release every assembler reference.
	frame := parser.EncodedFrame{Index: s.delivered, Data: view.Data, Metadata: s.metadata, TransferSyntax: s.syntax}
	s.parts = nil
	s.frameBytes = 0
	s.scanner = markerScanner{format: s.format}
	if err := s.sink.HandleEncodedFrame(frame); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.delivered++
	return nil
}

func (s *Stream) read(r io.Reader, data []byte) error {
	for len(data) > 0 {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		n := min(len(data), 32<<10)
		if _, err := io.ReadFull(r, data[:n]); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("%w: %w", ErrSource, err)
		}
		data = data[n:]
	}
	return s.ctx.Err()
}
func (s *Stream) EndPixelData() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if !s.started || s.ended || !s.bot || len(s.parts) != 0 || s.delivered != s.metadata.NumberOfFrames {
		return ErrFrameCount
	}
	s.ended = true
	return nil
}
