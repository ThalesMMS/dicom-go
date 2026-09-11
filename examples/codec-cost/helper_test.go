package codeccost

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestReadHelperReplyRejectsOversizedPayloadBeforeAllocating(t *testing.T) {
	var header [12]byte
	copy(header[:4], helperMagic)
	binary.LittleEndian.PutUint32(header[4:], 0)
	binary.LittleEndian.PutUint32(header[8:], maxHelperPayloadBytes+1)
	_, err := readHelperReply(bytes.NewReader(header[:]))
	if err == nil {
		t.Fatal("oversized reply accepted")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v, want payload limit", err)
	}
}

func TestServeHelperRejectsOversizedRequestBeforeAllocating(t *testing.T) {
	var header [24]byte
	copy(header[:4], helperMagic)
	binary.LittleEndian.PutUint32(header[20:], maxHelperPayloadBytes+1)
	err := ServeHelper(bytes.NewReader(header[:]), ioDiscardWriter{}, func([]byte, pixeldata.Metadata) ([]byte, error) {
		t.Fatal("decode invoked for oversized request")
		return nil, nil
	})
	if err == nil {
		t.Fatal("oversized request accepted")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v, want payload limit", err)
	}
}

type ioDiscardWriter struct{}

func (ioDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestWriteHelperRequestRejectsOversizedFragment(t *testing.T) {
	err := writeHelperRequest(ioDiscardWriter{}, DecodeRequest{
		Fragment: make([]byte, maxHelperPayloadBytes+1),
		Metadata: gray8x2Metadata(),
	})
	if err == nil {
		t.Fatal("oversized fragment accepted")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v, want payload limit", err)
	}
}

func TestWriteHelperReplyRejectsOversizedPixelsBeforeWriting(t *testing.T) {
	var buf bytes.Buffer
	err := writeHelperReply(&buf, 0, make([]byte, maxHelperPayloadBytes+1))
	if err == nil {
		t.Fatal("oversized reply accepted")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v, want payload limit", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes before rejecting oversized reply", buf.Len())
	}
}

func TestServeHelperRejectsGeometryAboveUint16(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset int
	}{
		{"rows", 4},
		{"columns", 8},
		{"samples", 12},
		{"bits", 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var header [24]byte
			copy(header[:4], helperMagic)
			binary.LittleEndian.PutUint32(header[tc.offset:], math.MaxUint16+1)
			binary.LittleEndian.PutUint32(header[20:], 1)
			decodeCalled := false
			err := ServeHelper(bytes.NewReader(append(header[:], 0x00)), ioDiscardWriter{}, func([]byte, pixeldata.Metadata) ([]byte, error) {
				decodeCalled = true
				return []byte{1}, nil
			})
			if err == nil {
				t.Fatal("geometry above uint16 accepted")
			}
			if decodeCalled {
				t.Fatal("decode ran after truncating geometry to uint16")
			}
		})
	}
}

func TestHelperDecodeHonorsContextDuringExchange(t *testing.T) {
	reader := newBlockingReadCloser()
	t.Cleanup(func() { _ = reader.Close() })
	backend := &HelperBackend{
		Name:   "hung-helper",
		pid:    1,
		writer: discardCloser{},
		reader: reader,
	}
	req := DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeCancel,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := backend.Decode(ctx, req)
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Decode ignored ctx during helper write/read")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode() error = %v, want context deadline or cancel", err)
	}
	_, err = backend.Decode(context.Background(), req)
	if err == nil {
		t.Fatal("Decode reused helper after interrupted exchange")
	}
}

func TestHelperCloseInterruptsActiveExchangeWithoutDeadline(t *testing.T) {
	reader := newBlockingReadCloser()
	t.Cleanup(func() { _ = reader.Close() })
	closerCalled := make(chan struct{})
	backend := &HelperBackend{
		Name:   "hung-helper",
		pid:    1,
		writer: discardCloser{},
		reader: reader,
		closer: closeFunc(func() error {
			close(closerCalled)
			return nil
		}),
	}
	req := DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeWarm,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	}
	decodeDone := make(chan error, 1)
	go func() {
		_, err := backend.Decode(context.Background(), req)
		decodeDone <- err
	}()
	select {
	case <-reader.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Decode never started helper I/O")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- backend.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked behind hung helper exchange")
	}
	select {
	case <-closerCalled:
	case <-time.After(time.Second):
		t.Fatal("Close did not invoke closer after marking the helper closed")
	}

	select {
	case err := <-decodeDone:
		if err == nil {
			t.Fatal("Decode succeeded after Close aborted the exchange")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Decode still hung after Close")
	}

	if _, err := backend.Decode(context.Background(), req); err == nil {
		t.Fatal("Decode accepted work after Close")
	}
}

func TestSetHelperIODeadlineClearsWhenContextHasNoDeadline(t *testing.T) {
	rec := &deadlineRecorder{}
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	setHelperIODeadline(rec, rec, ctx)
	if rec.write.IsZero() || rec.read.IsZero() {
		t.Fatal("context deadline was not applied to reused streams")
	}
	setHelperIODeadline(rec, rec, context.Background())
	if !rec.write.IsZero() || !rec.read.IsZero() {
		t.Fatalf("stale deadlines retained without ctx deadline: write=%v read=%v", rec.write, rec.read)
	}
}

type deadlineRecorder struct {
	write time.Time
	read  time.Time
}

func (d *deadlineRecorder) Write(p []byte) (int, error) { return len(p), nil }

func (d *deadlineRecorder) Read([]byte) (int, error) { return 0, io.EOF }

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.write = t
	return nil
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.read = t
	return nil
}

func TestHelperDecodeDoesNotReuseAfterExchangeError(t *testing.T) {
	var bad [12]byte
	copy(bad[:4], "XXXX")
	var good bytes.Buffer
	if err := writeHelperReply(&good, 0, []byte{0, 255}); err != nil {
		t.Fatal(err)
	}
	backend := &HelperBackend{
		Name:   "desynced-helper",
		pid:    1,
		writer: discardCloser{},
		reader: io.MultiReader(bytes.NewReader(bad[:]), &good),
	}
	req := DecodeRequest{
		Codec:    "jpegxl",
		Cohort:   "tiny-gray",
		Mode:     ModeWarm,
		Fragment: []byte("encoded"),
		Metadata: gray8x2Metadata(),
	}
	if _, err := backend.Decode(context.Background(), req); err == nil {
		t.Fatal("corrupt reply succeeded")
	}
	if _, err := backend.Decode(context.Background(), req); err == nil {
		t.Fatal("Decode reused helper after a desynchronized reply")
	}
}

type discardCloser struct{}

func (discardCloser) Write(p []byte) (int, error) { return len(p), nil }

func (discardCloser) Close() error { return nil }

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

type blockingReadCloser struct {
	enterOnce sync.Once
	closeOnce sync.Once
	entered   chan struct{}
	closed    chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		entered: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	b.enterOnce.Do(func() { close(b.entered) })
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReadCloser) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}
