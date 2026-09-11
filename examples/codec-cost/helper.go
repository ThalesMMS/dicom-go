package codeccost

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const helperMagic = "CST1"

// maxHelperPayloadBytes caps CST1 allocations. It matches jpegxladapter's
// defaultMaxHelperPayloadBytes (64 MiB) without importing the production helper.
const maxHelperPayloadBytes = 64 << 20

// HelperBackend talks to one persistent decoder over a private stream.
type HelperBackend struct {
	Name     string
	pid      int
	writer   io.Writer
	reader   io.Reader
	closer   io.Closer
	mu       sync.Mutex // closed, unusable, and stream handles
	exchange sync.Mutex // serializes request/response I/O
	closed   bool
	unusable bool
}

// Decode sends one length-prefixed request to the reused helper.
func (b *HelperBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
	if b == nil {
		return FrameResult{}, fmt.Errorf("codeccost: nil helper")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := FrameResult{
		Codec:     req.Codec,
		Backend:   b.Name,
		Cohort:    req.Cohort,
		Mode:      req.Mode,
		HelperPID: b.pid,
		Launches:  0,
	}
	clock := NewClock(nil)
	clock.Start()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	if err := ctx.Err(); err != nil {
		result.Cancelled = true
		clock.Step(StagePreflight)
		result.StageRecord = clock.Record()
		return result, err
	}
	clock.Step(StagePreflight)
	if req.WaitAdmission != nil {
		if err := req.WaitAdmission(ctx); err != nil {
			clock.Step(StageAdmission)
			result.Cancelled = true
			result.StageRecord = clock.Record()
			return result, err
		}
	}
	clock.Step(StageAdmission)
	clock.Step(StagePrepare)

	b.exchange.Lock()
	defer b.exchange.Unlock()

	writer, reader, err := b.startExchange(ctx)
	if err != nil {
		if err == ctx.Err() {
			result.Cancelled = true
			result.StageRecord = clock.Record()
		}
		return result, err
	}

	type exchangeResult struct {
		pixels []byte
		err    error
		wrote  bool
	}
	done := make(chan exchangeResult, 1)
	go func() {
		if err := writeHelperRequest(writer, req); err != nil {
			done <- exchangeResult{err: err}
			return
		}
		pixels, err := readHelperReply(reader)
		done <- exchangeResult{pixels: pixels, err: err, wrote: true}
	}()

	var ex exchangeResult
	select {
	case ex = <-done:
	case <-ctx.Done():
		b.abortExchange()
		clock.Step(StageLaunch)
		clock.Step(StageExecute)
		result.Cancelled = true
		result.StageRecord = clock.Record()
		return result, ctx.Err()
	}
	if ex.err != nil && !ex.wrote {
		b.markUnusable()
		clock.Step(StageLaunch)
		return result, ex.err
	}
	clock.Step(StageLaunch)
	if ex.err != nil {
		b.markUnusable()
		clock.Step(StageExecute)
		return result, ex.err
	}
	pixels := ex.pixels
	clock.Step(StageExecute)
	clock.Step(StageConvert)
	result.IOBytes = int64(len(req.Fragment) + len(pixels))
	result.IOOps = 2
	result.Pixels = pixels
	sum := sha256.Sum256(pixels)
	result.PixelSHA256 = hex.EncodeToString(sum[:])
	clock.Step(StageDeliver)
	result.StageRecord = clock.Record()
	result.Elapsed = result.StageRecord.FirstFrame
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	if memAfter.TotalAlloc > memBefore.TotalAlloc {
		result.AllocBytes = memAfter.TotalAlloc - memBefore.TotalAlloc
	}
	result.HeapInuseBytes = memAfter.HeapInuse
	result.ProcessRSSBytes, _ = peekProcessRSS(os.Getpid())
	result.SubprocessRSSBytes, _ = peekProcessRSS(b.pid)
	return result, nil
}

func (b *HelperBackend) startExchange(ctx context.Context) (io.Writer, io.Reader, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, fmt.Errorf("codeccost: helper closed")
	}
	if b.unusable {
		return nil, nil, fmt.Errorf("codeccost: helper stream unusable")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	setHelperIODeadline(b.writer, b.reader, ctx)
	return b.writer, b.reader, nil
}

func (b *HelperBackend) markUnusable() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unusable = true
}

func (b *HelperBackend) abortExchange() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unusable = true
	b.abortLocked()
}

func (b *HelperBackend) abortLocked() {
	if c, ok := b.writer.(io.Closer); ok {
		_ = c.Close()
	}
	if c, ok := b.reader.(io.Closer); ok {
		_ = c.Close()
	}
}

type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

func setHelperIODeadline(w io.Writer, r io.Reader, ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	if wd, ok := w.(writeDeadliner); ok {
		_ = wd.SetWriteDeadline(deadline)
	}
	if rd, ok := r.(readDeadliner); ok {
		_ = rd.SetReadDeadline(deadline)
	}
}

// Close asks the helper to exit. It is idempotent.
func (b *HelperBackend) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.unusable = true
	b.abortLocked()
	closer := b.closer
	b.closer = nil
	b.mu.Unlock()
	if closer != nil {
		return closer.Close()
	}
	return nil
}

func writeHelperRequest(w io.Writer, req DecodeRequest) error {
	if uint64(len(req.Fragment)) > uint64(maxHelperPayloadBytes) {
		return fmt.Errorf("codeccost: helper payload %d exceeds limit %d", len(req.Fragment), maxHelperPayloadBytes)
	}
	if err := writeHelperHeader(w, uint32(req.Metadata.Rows), uint32(req.Metadata.Columns), uint32(req.Metadata.SamplesPerPixel), uint32(req.Metadata.BitsAllocated), uint32(len(req.Fragment))); err != nil {
		return err
	}
	_, err := w.Write(req.Fragment)
	return err
}

func writeHelperShutdown(w io.Writer) error {
	return writeHelperHeader(w, 0, 0, 0, 0, 0)
}

func writeHelperHeader(w io.Writer, rows, cols, samples, bits, payload uint32) error {
	var header [24]byte
	copy(header[:4], helperMagic)
	binary.LittleEndian.PutUint32(header[4:], rows)
	binary.LittleEndian.PutUint32(header[8:], cols)
	binary.LittleEndian.PutUint32(header[12:], samples)
	binary.LittleEndian.PutUint32(header[16:], bits)
	binary.LittleEndian.PutUint32(header[20:], payload)
	_, err := w.Write(header[:])
	return err
}

func readHelperReply(r io.Reader) ([]byte, error) {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	if string(header[:4]) != helperMagic {
		return nil, fmt.Errorf("codeccost: helper magic mismatch")
	}
	status := binary.LittleEndian.Uint32(header[4:])
	n := binary.LittleEndian.Uint32(header[8:])
	if n > maxHelperPayloadBytes {
		return nil, fmt.Errorf("codeccost: helper payload %d exceeds limit %d", n, maxHelperPayloadBytes)
	}
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	if status != 0 {
		return nil, fmt.Errorf("codeccost: helper status %d", status)
	}
	return payload, nil
}

func writeHelperReply(w io.Writer, status uint32, pixels []byte) error {
	if uint64(len(pixels)) > uint64(maxHelperPayloadBytes) {
		return fmt.Errorf("codeccost: helper payload %d exceeds limit %d", len(pixels), maxHelperPayloadBytes)
	}
	var header [12]byte
	copy(header[:4], helperMagic)
	binary.LittleEndian.PutUint32(header[4:], status)
	binary.LittleEndian.PutUint32(header[8:], uint32(len(pixels)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(pixels) == 0 {
		return nil
	}
	_, err := w.Write(pixels)
	return err
}

// ServeHelper reads requests until a zero-length payload. decode must not retain fragment.
func ServeHelper(r io.Reader, w io.Writer, decode func([]byte, pixeldata.Metadata) ([]byte, error)) error {
	for {
		var header [24]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if string(header[:4]) != helperMagic {
			return fmt.Errorf("codeccost: helper request magic mismatch")
		}
		rows := binary.LittleEndian.Uint32(header[4:])
		cols := binary.LittleEndian.Uint32(header[8:])
		samples := binary.LittleEndian.Uint32(header[12:])
		bits := binary.LittleEndian.Uint32(header[16:])
		n := binary.LittleEndian.Uint32(header[20:])
		if n == 0 {
			return nil
		}
		if n > maxHelperPayloadBytes {
			return fmt.Errorf("codeccost: helper payload %d exceeds limit %d", n, maxHelperPayloadBytes)
		}
		if rows > math.MaxUint16 || cols > math.MaxUint16 || samples > math.MaxUint16 || bits > math.MaxUint16 {
			return fmt.Errorf("codeccost: helper geometry exceeds uint16")
		}
		fragment := make([]byte, n)
		if _, err := io.ReadFull(r, fragment); err != nil {
			return err
		}
		pixels, err := decode(fragment, pixeldata.Metadata{
			Rows:            uint16(rows),
			Columns:         uint16(cols),
			SamplesPerPixel: uint16(samples),
			BitsAllocated:   uint16(bits),
		})
		status := uint32(0)
		if err != nil {
			status = 1
			pixels = nil
		}
		if err := writeHelperReply(w, status, pixels); err != nil {
			return err
		}
	}
}

type helperCloser struct {
	reqW *io.PipeWriter
	repW *io.PipeWriter
	done <-chan error
}

func (c helperCloser) Close() error {
	_ = writeHelperShutdown(c.reqW)
	_ = c.reqW.Close()
	err := <-c.done
	_ = c.repW.Close()
	return err
}

// StartTestHelper serves the helper protocol in-process for unit tests.
func StartTestHelper(t testing.TB, decode func([]byte, pixeldata.Metadata) ([]byte, error)) *HelperBackend {
	t.Helper()
	reqR, reqW := io.Pipe()
	repR, repW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeHelper(reqR, repW, decode)
		_ = repW.Close()
	}()
	backend := &HelperBackend{
		Name:   "jpegxl-helper-proto",
		pid:    os.Getpid(),
		writer: reqW,
		reader: repR,
		closer: helperCloser{reqW: reqW, repW: repW, done: done},
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}
