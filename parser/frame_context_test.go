package parser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The hook observes the send's cancellation select, not a scheduling delay.
type observedFrameContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *observedFrameContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestFrameChannelSinkContextAbandonedConsumer(t *testing.T) {
	for _, capacity := range []int{0, 1} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &observedFrameContext{Context: base, entered: make(chan struct{})}
			ch := make(chan Frame, capacity)
			if capacity > 0 {
				ch <- Frame{Index: -1}
			}
			sink := NewFrameChannelSinkContext(ctx, ch)
			done := make(chan error, 1)
			go func() { done <- sink.HandleFrame(Frame{Index: 1}) }()
			finished := false
			t.Cleanup(func() {
				cancel()
				if !finished {
					// Drain only after the proof failed, to release the legacy negative control.
					for !finished {
						select {
						case <-done:
							finished = true
						case <-ch:
						case <-time.After(time.Second):
							t.Error("sender leaked")
							return
						}
					}
				}
				_ = sink.Close()
			})
			select {
			case <-ctx.entered:
			case <-time.After(time.Second):
				t.Fatal("send never observed cancellation")
			}
			cancel()
			select {
			case err := <-done:
				finished = true
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("send cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled send remained blocked without a consumer")
			}
		})
	}
}

func TestFrameChannelSinkContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, c := range []context.Context{ctx, deadline} {
		ch := make(chan Frame, 1) // A ready send must not hide prior cancellation.
		sink := NewFrameChannelSinkContext(c, ch)
		if err := sink.HandleFrame(Frame{}); !errors.Is(err, c.Err()) || !errors.Is(err, ErrFrameSink) {
			t.Fatalf("cancellation chain: %v", err)
		}
		if len(ch) != 0 {
			t.Fatal("cancelled call delivered a frame")
		}
		_ = sink.Close()
		_ = sink.Close()
		if _, ok := <-ch; ok {
			t.Fatal("channel not closed")
		}
	}
}

func TestFrameChannelSinkContextSlowConsumerOwnsBuffers(t *testing.T) {
	for _, capacity := range []int{0, 2} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan Frame, capacity)
			sink := NewFrameChannelSinkContext(ctx, ch)
			done := make(chan error, 1)
			ack := make(chan struct{})
			go func() {
				defer sink.Close()
				for i := 0; i < 4; i++ {
					if err := sink.HandleFrame(Frame{Index: i, Data: []byte{byte(i)}}); err != nil {
						done <- err
						return
					}
					select {
					case <-ack:
					case <-ctx.Done():
						done <- ctx.Err()
						return
					}
				}
				done <- nil
			}()
			var retained [][]byte
			for i := 0; i < 4; i++ {
				select {
				case frame := <-ch:
					if frame.Index != i {
						t.Fatal("frame order changed")
					}
					retained = append(retained, frame.Data)
					frame.Data[0] += 10 // Ownership transfers to the consumer.
				case <-time.After(time.Second):
					t.Fatal("consumer stalled")
				}
				select {
				case ack <- struct{}{}:
				case <-time.After(time.Second):
					t.Fatal("producer stalled")
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("producer did not finish")
			}
			_ = sink.Close() // Races safely with the producer's deferred close.
			if _, ok := <-ch; ok {
				t.Fatal("channel not finalized")
			}
			for i, data := range retained {
				if data[0] != byte(i+10) {
					t.Fatal("retained frame buffer overwritten")
				}
			}
		})
	}
}

func TestFrameChannelSinkContextConcurrentFinalization(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedFrameContext{Context: base, entered: make(chan struct{})}
	ch := make(chan Frame)
	sink := NewFrameChannelSinkContext(ctx, ch)
	done := make(chan error, 1)
	go func() { done <- sink.HandleFrame(Frame{}) }()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		t.Fatal("send did not start")
	}
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); _ = sink.Close() }()
	}
	closed := make(chan struct{})
	go func() { workers.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("finalizers stalled")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrFrameSink) {
			t.Fatalf("closed send: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sender did not terminate")
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
	if err := sink.HandleFrame(Frame{}); !errors.Is(err, ErrFrameSink) {
		t.Fatalf("send after close: %v", err)
	}
}

func TestFrameChannelSinkContextInvalidInputs(t *testing.T) {
	var nilSink *contextFrameChannelSink
	for _, sink := range []FrameSink{nilSink, NewFrameChannelSinkContext(nil, make(chan Frame)), NewFrameChannelSinkContext(context.Background(), nil)} {
		if err := sink.HandleFrame(Frame{}); !errors.Is(err, ErrFrameSink) {
			t.Fatalf("invalid input: %v", err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
