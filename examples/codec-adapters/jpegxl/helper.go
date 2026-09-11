package jpegxladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const (
	defaultHelperTimeout        = 30 * time.Second
	defaultHelperRestartBudget  = 2
	helperResidentOverheadBytes = 8 << 20
)

// DialFunc starts one helper connection. Tests inject a fake; production
// dials the packaged jpegxl-helper executable.
type DialFunc func() (HelperConn, error)

// AdmissionFunc charges estimated helper/IPC bytes against a caller budget.
// The returned release function must be idempotent.
type AdmissionFunc func(ctx context.Context, bytes int64) (func(), error)

// SessionConfig configures a session-owned JPEG XL helper pool.
type SessionConfig struct {
	Dial            DialFunc
	Executable      string
	MaxRestarts     int
	Timeout         time.Duration
	MaxPayloadBytes uint32
	Fallback        Decoder
	Admit           AdmissionFunc
}

// HelperConn is one private, versioned helper stream. It carries no DICOM identifiers.
type HelperConn struct {
	Reader io.Reader
	Writer io.Writer
	Closer io.Closer
	PID    int
}

// Session owns a limited JPEG XL helper worker for one application session.
type Session struct {
	cfg      SessionConfig
	mu       sync.Mutex
	worker   chan struct{}
	conn     HelperConn
	connGen  uint64
	ready    bool
	closed   bool
	restarts int
	nextID   uint64
}

type sessionDecoder struct {
	session *Session
}

// OpenSession starts one helper worker, handshakes the versioned protocol, and
// returns a session-owned decoder. Failure to start is typed unavailable.
func OpenSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg = normalizeSessionConfig(cfg)
	runCtx, cancel := boundDecoderContext(ctx, cfg.Timeout)
	defer cancel()
	session := &Session{
		cfg:    cfg,
		worker: make(chan struct{}, 1),
	}
	session.worker <- struct{}{}
	if err := session.ensureReady(runCtx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func normalizeSessionConfig(cfg SessionConfig) SessionConfig {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultHelperTimeout
	}
	if cfg.MaxRestarts < 0 {
		cfg.MaxRestarts = 0
	}
	if cfg.MaxPayloadBytes == 0 {
		cfg.MaxPayloadBytes = defaultMaxHelperPayloadBytes
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = defaultHelperRestartBudget
	}
	if cfg.Dial == nil {
		cfg.Dial = processDial(cfg.Executable)
	}
	return cfg
}

// Close asks the helper to exit and reaps the process. It is idempotent.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closer := s.takeConnLocked()
	s.mu.Unlock()
	return closeHelperConn(closer)
}

// Decoder returns a Decoder that honors DecodeFrameContext.
func (s *Session) Decoder() Decoder {
	if s == nil {
		return sessionDecoder{}
	}
	return sessionDecoder{session: s}
}

func (d sessionDecoder) DecodeFrame(fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return d.DecodeFrameContext(context.Background(), fragment, metadata)
}

func (d sessionDecoder) DecodeFrameContext(ctx context.Context, fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	if d.session == nil {
		return nil, ErrHelperUnavailable
	}
	return d.session.decode(ctx, fragment, metadata)
}

func (s *Session) decode(ctx context.Context, fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}

	want := decodedFrameSize(metadata)
	if want <= 0 {
		return nil, fmt.Errorf("%w: decoded frame bytes=%d", ErrUnsupportedMetadata, want)
	}
	if want > int64(s.cfg.MaxPayloadBytes) || uint64(len(fragment)) > uint64(s.cfg.MaxPayloadBytes) {
		return s.fallback(ctx, fragment, metadata,
			fmt.Errorf("%w: encoded=%d decoded=%d", ErrHelperTooLarge, len(fragment), want))
	}

	release, err := s.admit(ctx, fragment, metadata)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return s.fallback(ctx, fragment, metadata, err)
	}
	defer release()

	runCtx, cancel := boundDecoderContext(ctx, s.cfg.Timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return nil, err
	}

	pixels, err := s.decodeHelper(runCtx, fragment, metadata, uint32(want))
	if err == nil {
		return pixels, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if errors.Is(err, ErrMalformedCodestream) ||
		errors.Is(err, ErrUnsupportedMetadata) ||
		errors.Is(err, ErrImageSizeMismatch) ||
		errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		return nil, err
	}
	return s.fallback(ctx, fragment, metadata, err)
}

func (s *Session) admit(ctx context.Context, fragment []byte, metadata pixeldata.Metadata) (func(), error) {
	if s.cfg.Admit == nil {
		return func() {}, nil
	}
	bytes := int64(len(fragment)) + decodedFrameSize(metadata)
	if bytes < 0 {
		bytes = 0
	}
	release, err := s.cfg.Admit(ctx, bytes)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return func() {}, nil
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

func (s *Session) fallback(ctx context.Context, fragment []byte, metadata pixeldata.Metadata, cause error) ([]byte, error) {
	if s.cfg.Fallback == nil {
		return nil, cause
	}
	if ctxDec, ok := s.cfg.Fallback.(ContextDecoder); ok {
		pixels, err := ctxDec.DecodeFrameContext(ctx, fragment, metadata)
		if err != nil {
			return nil, err
		}
		return pixels, nil
	}
	return s.cfg.Fallback.DecodeFrame(fragment, metadata)
}

func (s *Session) decodeHelper(ctx context.Context, fragment []byte, metadata pixeldata.Metadata, want uint32) ([]byte, error) {
	if err := s.acquireWorker(ctx); err != nil {
		return nil, err
	}
	defer s.releaseWorker()

	if err := s.ensureReady(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrHelperUnavailable
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !s.ready {
		s.mu.Unlock()
		return nil, ErrHelperUnavailable
	}

	s.nextID++
	id := s.nextID
	req := helperRequest{
		Opcode:        helperOpcodeDecode,
		RequestID:     id,
		Rows:          uint32(metadata.Rows),
		Columns:       uint32(metadata.Columns),
		Samples:       uint32(metadata.SamplesPerPixel),
		BitsAllocated: uint32(metadata.BitsAllocated),
		Payload:       fragment,
	}
	writer := s.conn.Writer
	reader := s.conn.Reader
	s.mu.Unlock()

	if err := writeHelperRequest(writer, req); err != nil {
		s.resetConn()
		return nil, fmt.Errorf("%w: write request %d: %w", ErrHelperCrashed, id, err)
	}

	reply, err := waitHelperReply(ctx, reader, want)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrHelperUnavailable
	}
	if err != nil {
		closer := s.takeConnLocked()
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.restarts++
		}
		s.mu.Unlock()
		_ = closeHelperConn(closer)
		return nil, err
	}
	if reply.RequestID != id {
		closer := s.takeConnLocked()
		s.restarts++
		s.mu.Unlock()
		_ = closeHelperConn(closer)
		return nil, fmt.Errorf("%w: reply id %d want %d", ErrHelperProtocol, reply.RequestID, id)
	}
	if reply.Status != helperStatusOK {
		s.mu.Unlock()
		return nil, helperStatusError(reply.Status)
	}
	if int64(len(reply.Pixels)) != decodedFrameSize(metadata) {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: helper pixels=%d expected=%d", pixeldata.ErrPixelDataSizeMismatch, len(reply.Pixels), decodedFrameSize(metadata))
	}
	if uint32(reply.Rows) != uint32(metadata.Rows) || uint32(reply.Columns) != uint32(metadata.Columns) || reply.Samples != uint32(metadata.SamplesPerPixel) {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: helper size=%dx%d samples=%d", ErrImageSizeMismatch, reply.Columns, reply.Rows, reply.Samples)
	}
	s.mu.Unlock()
	return append([]byte(nil), reply.Pixels...), nil
}

func (s *Session) acquireWorker(ctx context.Context) error {
	if s.worker == nil {
		return ErrHelperUnavailable
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.worker:
		if err := ctx.Err(); err != nil {
			s.releaseWorker()
			return err
		}
		return nil
	}
}

func (s *Session) releaseWorker() {
	if s.worker == nil {
		return
	}
	select {
	case s.worker <- struct{}{}:
	default:
	}
}

func (s *Session) ensureReady(ctx context.Context) error {
	s.mu.Lock()
	if s.ready {
		s.mu.Unlock()
		return nil
	}
	if s.closed {
		s.mu.Unlock()
		return ErrHelperUnavailable
	}
	if s.restarts > s.cfg.MaxRestarts {
		s.mu.Unlock()
		return ErrHelperUnavailable
	}
	if s.cfg.Dial == nil {
		s.mu.Unlock()
		return ErrHelperUnavailable
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	dial := s.cfg.Dial
	s.mu.Unlock()

	conn, err := dial()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHelperUnavailable, err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = closeHelperConn(conn.Closer)
		return ErrHelperUnavailable
	}
	if s.ready {
		s.mu.Unlock()
		_ = closeHelperConn(conn.Closer)
		return nil
	}
	s.conn = conn
	s.connGen++
	gen := s.connGen
	writer := conn.Writer
	reader := conn.Reader
	s.mu.Unlock()

	if err := writeHelperRequest(writer, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
		s.dropConn(gen, true)
		return fmt.Errorf("%w: hello: %w", ErrHelperUnavailable, err)
	}
	reply, err := waitHelperReply(ctx, reader, 0)
	if err != nil {
		restart := !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
		s.dropConn(gen, restart)
		if !restart {
			return err
		}
		return fmt.Errorf("%w: hello: %w", ErrHelperUnavailable, err)
	}
	if reply.RequestID != 1 || reply.Status != helperStatusOK {
		s.dropConn(gen, true)
		return fmt.Errorf("%w: hello handshake", ErrHelperProtocol)
	}

	s.mu.Lock()
	if s.closed || s.connGen != gen {
		var closer io.Closer
		if s.connGen == gen {
			closer = s.takeConnLocked()
		}
		s.mu.Unlock()
		_ = closeHelperConn(closer)
		return ErrHelperUnavailable
	}
	s.ready = true
	s.mu.Unlock()
	return nil
}

func waitHelperReply(ctx context.Context, r io.Reader, maxPixels uint32) (helperReply, error) {
	type result struct {
		reply helperReply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reply, err := readHelperReply(r, maxPixels)
		done <- result{reply: reply, err: err}
	}()
	select {
	case <-ctx.Done():
		return helperReply{}, ctx.Err()
	case got := <-done:
		if err := ctx.Err(); err != nil {
			return helperReply{}, err
		}
		return got.reply, got.err
	}
}

func (s *Session) resetConn() {
	s.mu.Lock()
	closer := s.takeConnLocked()
	s.restarts++
	s.mu.Unlock()
	_ = closeHelperConn(closer)
}

func (s *Session) dropConn(gen uint64, consumeRestart bool) {
	s.mu.Lock()
	var closer io.Closer
	if gen != 0 && s.connGen == gen {
		closer = s.takeConnLocked()
		if consumeRestart {
			s.restarts++
		}
	}
	s.mu.Unlock()
	_ = closeHelperConn(closer)
}

func (s *Session) takeConnLocked() io.Closer {
	closer := s.conn.Closer
	s.conn = HelperConn{}
	s.ready = false
	s.connGen++
	return closer
}

func closeHelperConn(closer io.Closer) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func helperStatusError(status uint32) error {
	switch status {
	case helperStatusMalformed:
		return ErrMalformedCodestream
	case helperStatusUnsupported:
		return ErrUnsupportedMetadata
	case helperStatusSizeMismatch:
		return ErrImageSizeMismatch
	case helperStatusTooLarge:
		return ErrHelperTooLarge
	default:
		return fmt.Errorf("%w: status %d", ErrHelperProtocol, status)
	}
}

func helperReplyStatus(err error) uint32 {
	switch {
	case err == nil:
		return helperStatusOK
	case errors.Is(err, ErrMalformedCodestream):
		return helperStatusMalformed
	case errors.Is(err, ErrUnsupportedMetadata):
		return helperStatusUnsupported
	case errors.Is(err, ErrImageSizeMismatch),
		errors.Is(err, pixeldata.ErrPixelDataSizeMismatch):
		return helperStatusSizeMismatch
	case errors.Is(err, ErrHelperTooLarge):
		return helperStatusTooLarge
	default:
		return helperStatusProtocol
	}
}

func processDial(executable string) DialFunc {
	return func() (HelperConn, error) {
		path, err := resolveHelperExecutable(executable)
		if err != nil {
			return HelperConn{}, err
		}
		return startHelperProcess(path)
	}
}

func resolveHelperExecutable(explicit string) (string, error) {
	candidates := []string{explicit, os.Getenv("DICOM_GO_JPEGXL_HELPER")}
	candidates = append(candidates, bundledHelperCandidates()...)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", ErrHelperUnavailable
}

func bundledHelperCandidates() []string {
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Dir(executable)
	name := HelperExecutableName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "codec", name),
		filepath.Clean(filepath.Join(dir, "..", "Resources", "codec", name)),
	}
}

var _ ContextDecoder = sessionDecoder{}
