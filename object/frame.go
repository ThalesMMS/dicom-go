package object

import (
	"context"
	"errors"
	"sync"

	"github.com/ThalesMMS/dicom-go/parser"
)

type Frame = parser.Frame
type FrameMetadata = parser.FrameMetadata
type FrameSink = parser.FrameSink
type FrameSinkFunc = parser.FrameSinkFunc

func NewFrameChannelSink(ch chan Frame) FrameSink {
	return parser.NewFrameChannelSink(ch)
}

// NewFrameChannelSinkContext is the cancelable parser frame-channel adapter.
// Consumers cancel ctx rather than closing ch. See parser.NewFrameChannelSinkContext
// for buffer ownership, concurrent finalization and underlying I/O limitations.
func NewFrameChannelSinkContext(ctx context.Context, ch chan Frame) FrameSink {
	return parser.NewFrameChannelSinkContext(ctx, ch)
}

// A high-level read owns finalization even when it fails before constructing a
// parser. Nested read helpers share this guard; only the first Close reports its
// error, so the error already returned by the parser is not joined a second time.
type ownedFrameSink struct {
	FrameSink
	once sync.Once
}

func (s *ownedFrameSink) Close() (err error) {
	s.once.Do(func() { err = s.FrameSink.Close() })
	return err
}

func finalizeFrameSink(opts *ReadFileOptions) func(*error) {
	finishEncoded := finalizeEncodedSink(opts)
	if opts.FrameSink == nil {
		return finishEncoded
	}
	if _, ok := opts.FrameSink.(*ownedFrameSink); !ok {
		opts.FrameSink = &ownedFrameSink{FrameSink: opts.FrameSink}
	}
	sink := opts.FrameSink
	return func(err *error) {
		finishEncoded(err)
		if closeErr := sink.Close(); closeErr != nil {
			*err = errors.Join(*err, closeErr)
		}
	}
}

type ownedEncapsulatedSink struct {
	parser.EncapsulatedSink
	once sync.Once
}

func (s *ownedEncapsulatedSink) Close() (err error) {
	s.once.Do(func() { err = s.EncapsulatedSink.Close() })
	return err
}
func finalizeEncodedSink(opts *ReadFileOptions) func(*error) {
	if opts.EncapsulatedSink == nil {
		return func(*error) {}
	}
	if _, ok := opts.EncapsulatedSink.(*ownedEncapsulatedSink); !ok {
		opts.EncapsulatedSink = &ownedEncapsulatedSink{EncapsulatedSink: opts.EncapsulatedSink}
	}
	sink := opts.EncapsulatedSink
	return func(err *error) { *err = errors.Join(*err, sink.Close()) }
}
