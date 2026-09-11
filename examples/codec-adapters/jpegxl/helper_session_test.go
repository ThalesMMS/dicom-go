package jpegxladapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestHelperSessionDecodesThroughPrivateProtocol(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func(fragment []byte, meta pixeldata.Metadata) ([]byte, error) {
			if !bytes.Equal(fragment, []byte("jxl")) {
				t.Errorf("fragment = %q, want borrowed payload", fragment)
			}
			if meta.Rows != 1 || meta.Columns != 2 {
				t.Errorf("metadata = %+v", meta)
			}
			return []byte{0x10, 0x20}, nil
		}),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, []byte{0x10, 0x20}) {
		t.Fatalf("pixels = %v, want native 8-bit frame", frame)
	}
}

func TestHelperSessionRejectsSwappedRequestIDsWithoutPublishing(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: faultyPipeDial(faultSwapID),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrHelperProtocol) {
		t.Fatalf("error = %v, want ErrHelperProtocol", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("swapped id presented as malformed: %v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after swapped id", len(frame))
	}
}

func TestHelperSessionTreatsTruncatedReplyAsCrashNotMalformed(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: faultyPipeDial(faultTruncate),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("error = %v, want ErrHelperCrashed", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("truncated reply presented as malformed: %v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after truncation", len(frame))
	}
}

func TestHelperSessionOversizedReplyIsNotPublished(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: faultyPipeDial(faultOversize),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrHelperTooLarge) {
		t.Fatalf("error = %v, want ErrHelperTooLarge", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after oversized reply", len(frame))
	}
}

func TestHelperSessionCancelBeforeAdmitDoesNotStartRequest(t *testing.T) {
	var launches atomic.Int32
	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			launches.Add(1)
			return []byte{1, 2}, nil
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(ctx, []byte("jxl"), gray8Meta())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("cancel presented as malformed: %v", err)
	}
	if launches.Load() != 0 {
		t.Fatalf("launches = %d, want 0", launches.Load())
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after cancel-before-admit", len(frame))
	}
}

func TestHelperSessionTimeoutIsDeadlineExceeded(t *testing.T) {
	started := make(chan struct{})
	session := openTestSession(t, SessionConfig{
		Timeout: 40 * time.Millisecond,
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			close(started)
			time.Sleep(2 * time.Second)
			return []byte{1, 2}, nil
		}),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("timeout presented as malformed: %v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after timeout", len(frame))
	}
}

func TestHelperSessionCancelInFlightDoesNotDropQueuedIdentity(t *testing.T) {
	var current atomic.Int64
	release := make(chan struct{})
	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			id := current.Add(1)
			if id == 1 {
				<-release
			}
			return []byte{byte(id), 0}, nil
		}),
	})
	decoder := session.Decoder().(ContextDecoder)
	inFlightCtx, cancelInFlight := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := decoder.DecodeFrameContext(inFlightCtx, []byte("a"), gray8Meta())
		firstErr <- err
	}()
	waitHelper(t, time.Second, func() bool { return current.Load() == 1 })

	queuedID := make(chan uint64, 1)
	secondErr := make(chan error, 1)
	go func() {
		frame, err := decoder.DecodeFrameContext(context.Background(), []byte("b"), gray8Meta())
		if err == nil && !bytes.Equal(frame, []byte{2, 0}) {
			secondErr <- errors.New("queued request lost its identity")
			return
		}
		if err == nil {
			queuedID <- 2
		}
		secondErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelInFlight()
	close(release)

	select {
	case err := <-firstErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight decode did not return after cancel")
	}
	select {
	case err := <-secondErr:
		if err != nil {
			t.Fatalf("queued request error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued request did not complete")
	}
	select {
	case id := <-queuedID:
		if id != 2 {
			t.Fatalf("queued identity = %d, want 2", id)
		}
	default:
	}
}

func TestHelperSessionCancelWhileQueuedDoesNotWaitForInFlightDecode(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	defer func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	session := openTestSession(t, SessionConfig{
		Timeout: 5 * time.Second,
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-unblock
			return []byte{1, 2}, nil
		}),
	})
	decoder := session.Decoder().(ContextDecoder)

	firstErr := make(chan error, 1)
	go func() {
		_, err := decoder.DecodeFrameContext(context.Background(), []byte("a"), gray8Meta())
		firstErr <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight decode did not start")
	}

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedErr := make(chan error, 1)
	go func() {
		_, err := decoder.DecodeFrameContext(queuedCtx, []byte("b"), gray8Meta())
		queuedErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	cancelQueued()
	select {
	case err := <-queuedErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("queued cancel waited %s for in-flight decode", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("queued cancel did not return while another decode owned the worker")
	}
	select {
	case <-unblock:
	default:
		close(unblock)
	}
	select {
	case <-firstErr:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight decode did not finish after unblock")
	}
}

func TestOpenSessionHandshakeHonorsCallerCancel(t *testing.T) {
	var closes atomic.Int32
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := OpenSession(ctx, SessionConfig{
			Timeout: 30 * time.Second,
			Dial:    hangHelloDial(&closes, started),
		})
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handshake dial did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("OpenSession() error = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrMalformedCodestream) {
			t.Fatalf("handshake cancel presented as malformed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenSession handshake ignored caller cancel")
	}
	if closes.Load() == 0 {
		t.Fatal("handshake cancel did not close/reap the helper connection")
	}
}

func TestOpenSessionHandshakeTimesOutHangingHelper(t *testing.T) {
	var closes atomic.Int32
	started := make(chan struct{})
	start := time.Now()
	_, err := OpenSession(context.Background(), SessionConfig{
		Timeout: 40 * time.Millisecond,
		Dial:    hangHelloDial(&closes, started),
	})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("OpenSession() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("handshake hung for %s without observing Timeout", elapsed)
	}
	if closes.Load() == 0 {
		t.Fatal("handshake timeout did not close/reap the helper connection")
	}
}

func TestHelperSessionRejectsOversizedEncodedFragmentBeforeAdmit(t *testing.T) {
	var admitted atomic.Int32
	session := openTestSession(t, SessionConfig{
		MaxPayloadBytes: 4,
		Admit: func(context.Context, int64) (func(), error) {
			admitted.Add(1)
			return func() {}, nil
		},
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			t.Error("helper used for oversized encoded fragment")
			return nil, errors.New("helper used for oversized encoded fragment")
		}),
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("too-big!"), gray8Meta())
	if !errors.Is(err, ErrHelperTooLarge) {
		t.Fatalf("error = %v, want ErrHelperTooLarge", err)
	}
	if admitted.Load() != 0 {
		t.Fatalf("admission calls = %d, want 0 before oversized fragment is rejected", admitted.Load())
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after oversized fragment", len(frame))
	}
}

func TestHelperSessionLocalOversizeFallsBackLikeHelperTooLarge(t *testing.T) {
	cases := []struct {
		name     string
		maxBytes uint32
		fragment []byte
	}{
		{name: "encoded", maxBytes: 4, fragment: []byte("too-big!")},
		{name: "decoded", maxBytes: 1, fragment: []byte("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var helper, fallback, admitted atomic.Int32
			session := openTestSession(t, SessionConfig{
				MaxPayloadBytes: tc.maxBytes,
				Admit: func(context.Context, int64) (func(), error) {
					admitted.Add(1)
					return func() {}, nil
				},
				Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
					helper.Add(1)
					return nil, errors.New("helper used for oversized frame")
				}),
				Fallback: &scriptedDecoder{output: []byte{0x10, 0x20}, calls: &fallback},
			})
			frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), tc.fragment, gray8Meta())
			if err != nil {
				t.Fatal(err)
			}
			if helper.Load() != 0 {
				t.Fatalf("helper calls = %d, want 0", helper.Load())
			}
			if admitted.Load() != 0 {
				t.Fatalf("admission calls = %d, want 0", admitted.Load())
			}
			if fallback.Load() != 1 {
				t.Fatalf("fallback calls = %d, want 1 after local oversize", fallback.Load())
			}
			if !bytes.Equal(frame, []byte{0x10, 0x20}) {
				t.Fatalf("fallback pixels = %v", frame)
			}
		})
	}
}

func TestHelperSessionStopsRestartingAfterBudget(t *testing.T) {
	var dials atomic.Int32
	crashDial := faultyPipeDial(faultCrash)
	session := openTestSession(t, SessionConfig{
		MaxRestarts: 1,
		Dial: func() (HelperConn, error) {
			dials.Add(1)
			return crashDial()
		},
	})
	if dials.Load() != 1 {
		t.Fatalf("open dials = %d, want 1", dials.Load())
	}

	decoder := session.Decoder().(ContextDecoder)
	if _, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta()); !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("first decode error = %v, want ErrHelperCrashed", err)
	}
	if _, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta()); !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("restart decode error = %v, want ErrHelperCrashed", err)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials after one restart = %d, want 2", dials.Load())
	}
	if _, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta()); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("exhausted budget error = %v, want ErrHelperUnavailable", err)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials after exhausted budget = %d, want 2", dials.Load())
	}
}

func TestHelperSessionCallerCancelDoesNotConsumeRestartBudget(t *testing.T) {
	var decodeCalls atomic.Int32
	hang := make(chan struct{})
	started := make(chan struct{}, 4)
	t.Cleanup(func() {
		select {
		case <-hang:
		default:
			close(hang)
		}
	})

	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			n := decodeCalls.Add(1)
			if n <= 3 {
				started <- struct{}{}
				<-hang
				return nil, errors.New("hung helper interrupted")
			}
			return []byte{0x10, 0x20}, nil
		}),
	})
	decoder := session.Decoder().(ContextDecoder)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, err := decoder.DecodeFrameContext(ctx, []byte("jxl"), gray8Meta())
			errCh <- err
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("cancel %d: helper decode did not start", i+1)
		}
		cancel()
		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel %d error = %v, want context.Canceled", i+1, err)
			}
			if errors.Is(err, ErrHelperUnavailable) {
				t.Fatalf("cancel %d exhausted helper restart budget: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("cancel %d did not return", i+1)
		}
	}

	frame, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("decode after 3 caller cancels = %v, helper should stay available", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, []byte{0x10, 0x20}) {
		t.Fatalf("pixels = %v", frame)
	}
}

func TestHelperSessionCloseIsIdempotent(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			return []byte{1, 2}, nil
		}),
	})
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	_, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("decode after close = %v, want ErrHelperUnavailable", err)
	}
}

func TestHelperSessionCloseInterruptsInFlightDecode(t *testing.T) {
	started := make(chan struct{})
	session := openTestSession(t, SessionConfig{
		Timeout: 5 * time.Second,
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			time.Sleep(24 * time.Hour)
			return []byte{1, 2}, nil
		}),
	})
	decoder := session.Decoder().(ContextDecoder)
	errCh := make(chan error, 1)
	var frame []byte
	go func() {
		var err error
		frame, err = decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight decode did not start")
	}
	start := time.Now()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Close waited %s for in-flight decode", elapsed)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("in-flight decode succeeded after Close")
		}
		if errors.Is(err, ErrMalformedCodestream) {
			t.Fatalf("close presented as malformed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight decode did not return after Close")
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after Close interrupted decode", len(frame))
	}
}

func TestHelperSessionCloseDoesNotHoldMutexDuringCloserIO(t *testing.T) {
	started := make(chan struct{})
	gate := make(chan struct{})
	session := openTestSession(t, SessionConfig{
		Dial: func() (HelperConn, error) {
			conn, err := pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
				return []byte{1, 2}, nil
			})()
			if err != nil {
				return HelperConn{}, err
			}
			conn.Closer = gatedCloser{started: started, gate: gate, inner: conn.Closer}
			return conn, nil
		},
	})
	t.Cleanup(func() { closeOnce(gate) })
	decoder := session.Decoder().(ContextDecoder)

	closeErr := make(chan error, 1)
	go func() { closeErr <- session.Close() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not enter closer I/O")
	}

	decodeErr := make(chan error, 1)
	go func() {
		_, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
		decodeErr <- err
	}()
	select {
	case err := <-decodeErr:
		if !errors.Is(err, ErrHelperUnavailable) {
			t.Fatalf("decode during Close I/O = %v, want ErrHelperUnavailable", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("decode blocked on Close I/O holding mutex")
	}

	close(gate)
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after closer I/O")
	}
}

func TestHelperSessionDecodeTimeoutDoesNotHoldMutexDuringShutdown(t *testing.T) {
	started := make(chan struct{})
	gate := make(chan struct{})
	hang := make(chan struct{})
	session := openTestSession(t, SessionConfig{
		Timeout: 40 * time.Millisecond,
		Dial: func() (HelperConn, error) {
			conn, err := pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
				<-hang
				return []byte{1, 2}, nil
			})()
			if err != nil {
				return HelperConn{}, err
			}
			conn.Closer = gatedCloser{started: started, gate: gate, inner: conn.Closer}
			return conn, nil
		},
	})
	t.Cleanup(func() {
		closeOnce(hang)
		closeOnce(gate)
	})

	decodeErr := make(chan error, 1)
	go func() {
		_, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
		decodeErr <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("decode timeout did not enter closer I/O")
	}

	start := time.Now()
	closeErr := make(chan error, 1)
	go func() { closeErr <- session.Close() }()
	select {
	case err := <-closeErr:
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("Close waited %s for decode shutdown I/O holding mutex", elapsed)
		}
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on decode shutdown I/O holding mutex")
	}

	close(gate)
	select {
	case err := <-decodeErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("decode error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("decode did not return after shutdown I/O")
	}
}

func TestHelperSessionCloseDoesNotWaitOnHungRestartHandshake(t *testing.T) {
	var dials atomic.Int32
	hangStarted := make(chan struct{})
	var closes atomic.Int32
	crashDial := faultyPipeDial(faultCrash)
	session := openTestSession(t, SessionConfig{
		Timeout:     5 * time.Second,
		MaxRestarts: 2,
		Dial: func() (HelperConn, error) {
			if dials.Add(1) == 1 {
				return crashDial()
			}
			return hangHelloDial(&closes, hangStarted)()
		},
	})
	decoder := session.Decoder().(ContextDecoder)
	if _, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta()); !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("first decode error = %v, want ErrHelperCrashed", err)
	}

	decodeErr := make(chan error, 1)
	go func() {
		_, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
		decodeErr <- err
	}()
	select {
	case <-hangStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("restart handshake did not start")
	}

	start := time.Now()
	closeErr := make(chan error, 1)
	go func() { closeErr <- session.Close() }()
	select {
	case err := <-closeErr:
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("Close waited %s for handshake I/O holding mutex", elapsed)
		}
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on handshake I/O holding mutex")
	}
	if closes.Load() == 0 {
		t.Fatal("Close did not reap the in-flight handshake connection")
	}

	select {
	case err := <-decodeErr:
		if err == nil {
			t.Fatal("restart decode succeeded after Close during handshake")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart decode did not return after Close")
	}
}

func TestHelperSessionDropConnDoesNotCloseReplacementWhenClosersCompareEqual(t *testing.T) {
	// io.Closer interface equality is not a generation: value-type closers with
	// the same fields compare equal even when they belong to different connections.
	var closes atomic.Int32
	shared := countingCloser{n: &closes}
	session := &Session{
		conn:    HelperConn{Closer: shared, PID: 2},
		connGen: 2,
		ready:   true,
	}

	session.dropConn(1, true)

	if closes.Load() != 0 {
		t.Fatal("stale dropConn closed the replacement connection")
	}
	if !session.ready {
		t.Fatal("stale dropConn cleared the replacement ready state")
	}
	if session.conn.PID != 2 {
		t.Fatal("stale dropConn took the replacement connection")
	}
	if session.restarts != 0 {
		t.Fatalf("stale dropConn consumed restart budget: %d", session.restarts)
	}
}

func TestHelperSessionDropConnClosesCurrentGeneration(t *testing.T) {
	var closes atomic.Int32
	session := &Session{
		conn:    HelperConn{Closer: countingCloser{n: &closes}, PID: 1},
		connGen: 1,
		ready:   true,
	}

	session.dropConn(1, true)

	if closes.Load() != 1 {
		t.Fatalf("closes = %d, want 1", closes.Load())
	}
	if session.ready {
		t.Fatal("current-generation drop left the session ready")
	}
	if session.conn.PID != 0 || session.conn.Closer != nil {
		t.Fatal("current-generation drop left the connection installed")
	}
	if session.restarts != 1 {
		t.Fatalf("restarts = %d, want 1", session.restarts)
	}
}

func TestHelperSessionFailedHandshakeDoesNotDropReplacementWhenClosersCompareEqual(t *testing.T) {
	var closes atomic.Int32
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session := &Session{
		cfg: normalizeSessionConfig(SessionConfig{
			Timeout: 30 * time.Second,
			Dial:    hangHelloDialSharedCloser(&closes, started),
		}),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- session.ensureReady(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handshake dial did not start")
	}
	waitHelper(t, time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.connGen != 0
	})

	replacementR, replacementW := io.Pipe()
	t.Cleanup(func() {
		_ = replacementW.Close()
		_ = replacementR.Close()
	})
	session.mu.Lock()
	session.conn = HelperConn{
		Reader: replacementR,
		Writer: replacementW,
		Closer: countingCloser{n: &closes},
		PID:    2,
	}
	session.connGen++
	session.mu.Unlock()
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ensureReady() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensureReady did not return after cancel")
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if closes.Load() != 0 {
		t.Fatal("stale handshake drop closed the replacement connection")
	}
	if session.conn.PID != 2 {
		t.Fatal("stale handshake drop took the replacement connection")
	}
	if session.restarts != 0 {
		t.Fatalf("caller-canceled handshake consumed restart budget: %d", session.restarts)
	}
}

func TestHelperSessionReadyHandshakeDoesNotDropReplacementWhenGenerationMoved(t *testing.T) {
	var closes atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	session := &Session{
		cfg: normalizeSessionConfig(SessionConfig{
			Timeout: 30 * time.Second,
			Dial:    gatedHelloDialSharedCloser(&closes, started, release),
		}),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- session.ensureReady(context.Background()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handshake dial did not start")
	}
	waitHelper(t, time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.connGen != 0
	})

	replacementR, replacementW := io.Pipe()
	t.Cleanup(func() {
		_ = replacementW.Close()
		_ = replacementR.Close()
	})
	session.mu.Lock()
	session.conn = HelperConn{
		Reader: replacementR,
		Writer: replacementW,
		Closer: countingCloser{n: &closes},
		PID:    2,
	}
	session.connGen++
	session.mu.Unlock()
	close(release)

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrHelperUnavailable) {
			t.Fatalf("ensureReady() error = %v, want ErrHelperUnavailable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensureReady did not return after stale handshake completed")
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.ready {
		t.Fatal("stale handshake marked the replacement connection ready")
	}
	if closes.Load() != 0 {
		t.Fatal("stale handshake close dropped the replacement connection")
	}
	if session.conn.PID != 2 {
		t.Fatal("stale handshake took the replacement connection")
	}
}

func TestHelperSessionCloseAndDecodeRace(t *testing.T) {
	session := openTestSession(t, SessionConfig{
		Timeout: time.Second,
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			time.Sleep(5 * time.Millisecond)
			return []byte{1, 2}, nil
		}),
	})
	decoder := session.Decoder().(ContextDecoder)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		_ = session.Close()
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close/decode race did not finish")
	}
}

func TestHelperSessionFallsBackOnProtocolHelperError(t *testing.T) {
	var fallback atomic.Int32
	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			return nil, errors.New("transient helper resource failure")
		}),
		Fallback: &scriptedDecoder{output: []byte{0x55, 0x66}, calls: &fallback},
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Load() != 1 {
		t.Fatalf("fallback calls = %d, want 1 after non-terminal helper status", fallback.Load())
	}
	if !bytes.Equal(frame, []byte{0x55, 0x66}) {
		t.Fatalf("fallback pixels = %v", frame)
	}
}

func TestHelperSessionDoesNotFallBackOnMalformed(t *testing.T) {
	var fallback atomic.Int32
	session := openTestSession(t, SessionConfig{
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			return nil, ErrMalformedCodestream
		}),
		Fallback: &scriptedDecoder{output: []byte{0x11, 0x22}, calls: &fallback},
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if !errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("error = %v, want ErrMalformedCodestream", err)
	}
	if fallback.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0 for malformed codestream", fallback.Load())
	}
	if len(frame) != 0 {
		t.Fatalf("published %d bytes after malformed helper decode", len(frame))
	}
}

func TestHelperSessionFallsBackToCLIOnCrash(t *testing.T) {
	var fallback atomic.Int32
	session := openTestSession(t, SessionConfig{
		Dial: faultyPipeDial(faultCrash),
		Fallback: &scriptedDecoder{
			output: []byte{0x33, 0x44},
			calls:  &fallback,
		},
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Load() != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.Load())
	}
	if !bytes.Equal(frame, []byte{0x33, 0x44}) {
		t.Fatalf("fallback pixels = %v", frame)
	}
}

func TestHelperSessionAdmissionDeniedFallsBackExplicitly(t *testing.T) {
	var fallback atomic.Int32
	session := openTestSession(t, SessionConfig{
		Admit: func(context.Context, int64) (func(), error) {
			return nil, ErrHelperUnavailable
		},
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			t.Error("helper used after admission denial")
			return nil, errors.New("helper used after admission denial")
		}),
		Fallback: &scriptedDecoder{output: []byte{9, 8}, calls: &fallback},
	})
	frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Load() != 1 {
		t.Fatalf("fallback calls = %d, want 1 after admission denial", fallback.Load())
	}
	if !bytes.Equal(frame, []byte{9, 8}) {
		t.Fatalf("pixels = %v", frame)
	}
}

func TestHelperSessionAdmitContextErrorDoesNotStartFallback(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fallback, helper atomic.Int32
			session := openTestSession(t, SessionConfig{
				Admit: func(context.Context, int64) (func(), error) {
					return nil, tc.err
				},
				Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
					helper.Add(1)
					return nil, errors.New("helper used after admit context error")
				}),
				Fallback: &decodeFrameOnly{output: []byte{9, 8}, calls: &fallback},
			})
			frame, err := session.Decoder().(ContextDecoder).DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta())
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if helper.Load() != 0 {
				t.Fatalf("helper calls = %d, want 0", helper.Load())
			}
			if fallback.Load() != 0 {
				t.Fatalf("fallback calls = %d, want 0 when admit returns %v", fallback.Load(), tc.err)
			}
			if len(frame) != 0 {
				t.Fatalf("published %d bytes after admit %v", len(frame), tc.err)
			}
		})
	}
}

func TestHelperSessionRespectsAdmissionBudgetAcrossConcurrentRequests(t *testing.T) {
	var peak atomic.Int64
	var current atomic.Int64
	session := openTestSession(t, SessionConfig{
		Admit: func(_ context.Context, bytes int64) (func(), error) {
			n := current.Add(bytes)
			for {
				prev := peak.Load()
				if n <= prev || peak.CompareAndSwap(prev, n) {
					break
				}
			}
			return func() { current.Add(-bytes) }, nil
		},
		Dial: pipeDial(func([]byte, pixeldata.Metadata) ([]byte, error) {
			time.Sleep(20 * time.Millisecond)
			return []byte{1, 2}, nil
		}),
	})
	decoder := session.Decoder().(ContextDecoder)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := decoder.DecodeFrameContext(context.Background(), []byte("jxl"), gray8Meta()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if current.Load() != 0 {
		t.Fatalf("leaked admission bytes = %d", current.Load())
	}
	if peak.Load() <= 0 {
		t.Fatal("admission was never charged")
	}
}

type decodeFrameOnly struct {
	output []byte
	calls  *atomic.Int32
}

func (d *decodeFrameOnly) DecodeFrame(_ []byte, _ pixeldata.Metadata) ([]byte, error) {
	if d.calls != nil {
		d.calls.Add(1)
	}
	return append([]byte(nil), d.output...), nil
}

type scriptedDecoder struct {
	output []byte
	calls  *atomic.Int32
}

func (d *scriptedDecoder) DecodeFrame(fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return d.DecodeFrameContext(context.Background(), fragment, metadata)
}

func (d *scriptedDecoder) DecodeFrameContext(ctx context.Context, _ []byte, _ pixeldata.Metadata) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.calls != nil {
		d.calls.Add(1)
	}
	return append([]byte(nil), d.output...), nil
}

func openTestSession(t *testing.T, cfg SessionConfig) *Session {
	t.Helper()
	if cfg.Timeout == 0 {
		cfg.Timeout = time.Second
	}
	session, err := OpenSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type helperFault int

const (
	faultNone helperFault = iota
	faultSwapID
	faultTruncate
	faultOversize
	faultCrash
	faultHang
)

func hangHelloDial(closes *atomic.Int32, started chan struct{}) DialFunc {
	return func() (HelperConn, error) {
		reqR, reqW := io.Pipe()
		repR, repW := io.Pipe()
		go func() {
			select {
			case <-started:
			default:
				close(started)
			}
			_, _ = readHelperRequest(reqR, defaultMaxHelperPayloadBytes)
			_, _ = io.Copy(io.Discard, reqR)
		}()
		return HelperConn{
			Reader: repR,
			Writer: reqW,
			Closer: countingCloser{
				n: closes,
				inner: pipeHelperCloser{
					reqW: reqW,
					repW: repW,
				},
			},
			PID: 1,
		}, nil
	}
}

func hangHelloDialSharedCloser(closes *atomic.Int32, started chan struct{}) DialFunc {
	return gatedHelloDialSharedCloser(closes, started, nil)
}

func gatedHelloDialSharedCloser(closes *atomic.Int32, started, release chan struct{}) DialFunc {
	return func() (HelperConn, error) {
		reqR, reqW := io.Pipe()
		repR, repW := io.Pipe()
		go func() {
			select {
			case <-started:
			default:
				close(started)
			}
			req, err := readHelperRequest(reqR, defaultMaxHelperPayloadBytes)
			if err != nil {
				_ = repW.Close()
				return
			}
			if release == nil {
				_, _ = io.Copy(io.Discard, reqR)
				_ = repW.Close()
				return
			}
			<-release
			_ = writeHelperReply(repW, helperReply{RequestID: req.RequestID, Status: helperStatusOK})
		}()
		return HelperConn{
			Reader: repR,
			Writer: reqW,
			Closer: countingCloser{n: closes},
			PID:    1,
		}, nil
	}
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

type gatedCloser struct {
	started chan struct{}
	gate    chan struct{}
	inner   io.Closer
}

func (c gatedCloser) Close() error {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	if c.gate != nil {
		<-c.gate
	}
	if c.inner != nil {
		return c.inner.Close()
	}
	return nil
}

type countingCloser struct {
	n     *atomic.Int32
	inner io.Closer
}

func (c countingCloser) Close() error {
	if c.n != nil {
		c.n.Add(1)
	}
	if c.inner != nil {
		return c.inner.Close()
	}
	return nil
}

func pipeDial(decode func([]byte, pixeldata.Metadata) ([]byte, error)) DialFunc {
	return faultyPipeDialWithDecode(faultNone, decode)
}

func faultyPipeDial(fault helperFault) DialFunc {
	return faultyPipeDialWithDecode(fault, func([]byte, pixeldata.Metadata) ([]byte, error) {
		return []byte{1, 2}, nil
	})
}

func faultyPipeDialWithDecode(fault helperFault, decode func([]byte, pixeldata.Metadata) ([]byte, error)) DialFunc {
	return func() (HelperConn, error) {
		reqR, reqW := io.Pipe()
		repR, repW := io.Pipe()
		go serveFaultyHelper(reqR, repW, fault, decode)
		return HelperConn{
			Reader: repR,
			Writer: reqW,
			Closer: pipeHelperCloser{reqW: reqW, repW: repW},
			PID:    1,
		}, nil
	}
}

type pipeHelperCloser struct {
	reqW *io.PipeWriter
	repW *io.PipeWriter
}

func (c pipeHelperCloser) Close() error {
	_ = c.reqW.Close()
	_ = c.repW.Close()
	return nil
}

func waitHelper(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for helper condition")
}

func serveFaultyHelper(r io.Reader, w io.Writer, fault helperFault, decode func([]byte, pixeldata.Metadata) ([]byte, error)) {
	defer func() {
		if closer, ok := w.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	for {
		req, err := readHelperRequest(r, defaultMaxHelperPayloadBytes)
		if err != nil {
			return
		}
		switch req.Opcode {
		case helperOpcodeHello:
			_ = writeHelperReply(w, helperReply{RequestID: req.RequestID, Status: helperStatusOK})
		case helperOpcodeShutdown:
			return
		case helperOpcodeDecode:
			switch fault {
			case faultCrash:
				return
			case faultTruncate:
				_, _ = w.Write([]byte("JXL"))
				return
			case faultOversize:
				var header [helperReplyHeaderSize]byte
				copy(header[:4], helperMagic)
				putUint32(header[4:], HelperProtocolVersion)
				putUint64(header[8:], req.RequestID)
				putUint32(header[32:], 1<<20)
				_, _ = w.Write(header[:])
				return
			case faultSwapID:
				pixels, _ := decode(req.Payload, pixeldata.Metadata{
					Rows: uint16(req.Rows), Columns: uint16(req.Columns),
					SamplesPerPixel: uint16(req.Samples), BitsAllocated: uint16(req.BitsAllocated),
				})
				_ = writeHelperReply(w, helperReply{
					RequestID: req.RequestID + 1,
					Status:    helperStatusOK,
					Rows:      req.Rows,
					Columns:   req.Columns,
					Samples:   req.Samples,
					Pixels:    pixels,
				})
			case faultHang:
				select {}
			default:
				pixels, err := decode(req.Payload, pixeldata.Metadata{
					Rows: uint16(req.Rows), Columns: uint16(req.Columns),
					SamplesPerPixel: uint16(req.Samples), BitsAllocated: uint16(req.BitsAllocated),
				})
				status := helperReplyStatus(err)
				if err != nil {
					pixels = nil
				}
				_ = writeHelperReply(w, helperReply{
					RequestID: req.RequestID,
					Status:    status,
					Rows:      req.Rows,
					Columns:   req.Columns,
					Samples:   req.Samples,
					Pixels:    pixels,
				})
			}
		}
	}
}
