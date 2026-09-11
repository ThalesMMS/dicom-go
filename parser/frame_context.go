package parser

import (
	"context"
	"fmt"
	"sync"
)

// NewEncodedFrameChannelSinkContext uses the same cancellation/backpressure
// lifecycle as NewFrameChannelSinkContext, with distinct encoded-frame events.
// Queue capacity is chosen by the caller; retained bytes are at most capacity
// times the configured maximum encoded frame size (plus active delivery).
func NewEncodedFrameChannelSinkContext(ctx context.Context, ch chan EncodedFrame) EncodedFrameSink {
	return &encodedFrameChannelSink{contextChannelSink[EncodedFrame]{ctx: ctx, ch: ch, done: make(chan struct{})}}
}

type encodedFrameChannelSink struct {
	contextChannelSink[EncodedFrame]
}

func (s *encodedFrameChannelSink) HandleEncodedFrame(frame EncodedFrame) error {
	if s == nil {
		return fmt.Errorf("%w: nil encoded frame sink", ErrFrameSink)
	}
	return s.handle(frame)
}
func (s *encodedFrameChannelSink) Close() error {
	if s == nil {
		return nil
	}
	return s.contextChannelSink.Close()
}

// NewFrameChannelSinkContext delivers frames with backpressure until ctx is
// cancelled or the producer finalizes the sink. ctx must be non-nil. Consumers
// cancel ctx when abandoning delivery; only the sink closes ch. A frame racing
// with cancellation may still be delivered. Already-cancelled calls do not send.
//
// Close interrupts blocked sends, waits for active sends and closes ch once.
// Concurrent HandleFrame and Close calls are safe, but the parser Reader itself
// is not concurrent. This sink does not interrupt a blocked underlying io.Reader;
// configure transport deadlines or close an owned, interruptible input separately.
func NewFrameChannelSinkContext(ctx context.Context, ch chan Frame) FrameSink {
	return &contextFrameChannelSink{contextChannelSink[Frame]{ctx: ctx, ch: ch, done: make(chan struct{})}}
}

type contextFrameChannelSink struct{ contextChannelSink[Frame] }

func (s *contextFrameChannelSink) HandleFrame(frame Frame) error {
	if s == nil {
		return fmt.Errorf("%w: nil frame sink", ErrFrameSink)
	}
	return s.handle(frame)
}
func (s *contextFrameChannelSink) Close() error {
	if s == nil {
		return nil
	}
	return s.contextChannelSink.Close()
}

type contextChannelSink[T any] struct {
	ctx   context.Context
	ch    chan T
	done  chan struct{}
	once  sync.Once
	sends sync.RWMutex
}

func (s *contextChannelSink[T]) handle(frame T) error {
	if s == nil || s.ctx == nil || s.ch == nil {
		return fmt.Errorf("%w: nil frame context or channel", ErrFrameSink)
	}
	s.sends.RLock()
	defer s.sends.RUnlock()
	if err := s.ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrFrameSink, err)
	}
	// Checking done while holding the read lock also rejects calls after Close.
	select {
	case <-s.done:
		return fmt.Errorf("%w: frame channel finalized", ErrFrameSink)
	default:
	}
	select {
	case <-s.ctx.Done():
		return fmt.Errorf("%w: %w", ErrFrameSink, s.ctx.Err())
	case <-s.done:
		return fmt.Errorf("%w: frame channel finalized", ErrFrameSink)
	case s.ch <- frame:
		return nil
	}
}

func (s *contextChannelSink[T]) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		close(s.done) // Wake senders before waiting for their read locks.
		s.sends.Lock()
		defer s.sends.Unlock()
		if s.ch != nil {
			close(s.ch)
		}
	})
	return nil
}
