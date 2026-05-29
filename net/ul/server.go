package ul

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
)

const (
	DefaultServerMaxConcurrentAssociations = 64
	DefaultServerNegotiationTimeout        = 10 * time.Second
	AssociateRJReasonLocalLimitExceeded    = byte(2)
)

type SaturationPolicy uint8

const (
	SaturationBackpressure SaturationPolicy = iota
	SaturationReject
)

type AssociationHandler func(context.Context, *Association) error

// AssociationServerOptions configures a bounded UL accept/negotiation loop.
// MaxConcurrentAssociations includes sockets in negotiation and established
// handlers. Zero selects a safe default rather than an unbounded goroutine per
// connection.
type AssociationServerOptions struct {
	Accept                    AcceptOptions
	MaxConcurrentAssociations int
	SaturationPolicy          SaturationPolicy
	Handler                   AssociationHandler
}

type AssociationServerMetrics struct {
	Negotiating       int64
	Active            int64
	Accepted          uint64
	Rejected          uint64
	NegotiationPanics uint64
	HandlerErrors     uint64
	HandlerPanics     uint64
	Released          uint64
	Aborted           uint64
	Closed            uint64
	DroppedEvents     uint64
	SinkPanics        uint64
	RawDropped        uint64
	RawSinkPanics     uint64
}

type AssociationServer struct {
	listener *Listener
	options  AssociationServerOptions
	slots    chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	serveMu sync.Mutex
	serving bool
	stopped bool

	mu          sync.Mutex
	connections map[*serverConnection]struct{}
	wg          sync.WaitGroup
	stopOnce    sync.Once
	finishOnce  sync.Once

	ownedDispatcher    *telemetry.Dispatcher
	ownedRawDispatcher *RawPDUDispatcher

	negotiating       atomic.Int64
	active            atomic.Int64
	accepted          atomic.Uint64
	rejected          atomic.Uint64
	negotiationPanics atomic.Uint64
	handlerErrors     atomic.Uint64
	handlerPanics     atomic.Uint64
	released          atomic.Uint64
	aborted           atomic.Uint64
	closed            atomic.Uint64
}

type serverConnection struct {
	conn  net.Conn
	assoc *Association
}

func NewAssociationServer(listener *Listener, options AssociationServerOptions) (*AssociationServer, error) {
	if listener == nil || listener.listener == nil {
		return nil, fmt.Errorf("dicom ul: nil association server listener")
	}
	if options.Handler == nil {
		return nil, fmt.Errorf("dicom ul: nil association handler")
	}
	if options.MaxConcurrentAssociations < 0 {
		return nil, fmt.Errorf("dicom ul: maximum concurrent associations must not be negative")
	}
	if options.SaturationPolicy != SaturationBackpressure && options.SaturationPolicy != SaturationReject {
		return nil, fmt.Errorf("dicom ul: invalid saturation policy %d", options.SaturationPolicy)
	}
	if options.MaxConcurrentAssociations == 0 {
		options.MaxConcurrentAssociations = DefaultServerMaxConcurrentAssociations
	}
	options.Accept = normalizeAcceptOptions(options.Accept)
	if options.Accept.NegotiationTimeout == 0 {
		options.Accept.NegotiationTimeout = DefaultServerNegotiationTimeout
	}
	if err := validateOperationalTimeouts(
		options.Accept.NegotiationTimeout,
		options.Accept.IdleTimeout,
		options.Accept.ReadProgressTimeout,
		options.Accept.WriteProgressTimeout,
		options.Accept.ReleaseTimeout,
	); err != nil {
		return nil, err
	}
	if err := validateObservabilityOptions(options.Accept.Observability); err != nil {
		return nil, err
	}

	var ownedDispatcher *telemetry.Dispatcher
	var ownedRawDispatcher *RawPDUDispatcher
	if observability := options.Accept.Observability; observability != nil && observability.Sink != nil && observability.Dispatcher == nil {
		copyOptions := *observability
		depth := copyOptions.EventQueueDepth
		if depth == 0 {
			depth = 64
		}
		ownedDispatcher = telemetry.NewDispatcher(copyOptions.Sink, depth)
		copyOptions.Sink = nil
		copyOptions.Dispatcher = ownedDispatcher
		options.Accept.Observability = &copyOptions
	}
	if observability := options.Accept.Observability; observability != nil && observability.RawPDUSink != nil && observability.RawPDUDispatcher == nil {
		copyOptions := *observability
		ownedRawDispatcher = NewRawPDUDispatcher(copyOptions.RawPDUSink, copyOptions.RawPDUQueueDepth)
		copyOptions.RawPDUSink = nil
		copyOptions.RawPDUDispatcher = ownedRawDispatcher
		options.Accept.Observability = &copyOptions
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &AssociationServer{
		listener:           listener,
		options:            options,
		slots:              make(chan struct{}, options.MaxConcurrentAssociations),
		ctx:                ctx,
		cancel:             cancel,
		done:               make(chan struct{}),
		connections:        make(map[*serverConnection]struct{}),
		ownedDispatcher:    ownedDispatcher,
		ownedRawDispatcher: ownedRawDispatcher,
	}, nil
}

func (s *AssociationServer) Serve(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("dicom ul: nil association server")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.serveMu.Lock()
	if s.serving {
		s.serveMu.Unlock()
		return fmt.Errorf("dicom ul: association server already serving")
	}
	if s.stopped {
		s.serveMu.Unlock()
		return fmt.Errorf("dicom ul: association server is stopped")
	}
	runtimeCtx, runtimeCancel := mergeServerContexts(ctx, s.options.Accept.Context)
	s.cancel()
	s.ctx = runtimeCtx
	s.cancel = runtimeCancel
	s.serving = true
	s.serveMu.Unlock()

	serveExited := make(chan struct{})
	defer close(serveExited)
	go func() {
		select {
		case <-s.ctx.Done():
			s.forceStop()
		case <-serveExited:
		}
	}()

	acceptErr := s.acceptLoop()
	gracefulClose := errors.Is(acceptErr, net.ErrClosed) && s.isStopped()
	if acceptErr != nil && !gracefulClose && !errors.Is(acceptErr, context.Canceled) {
		s.forceStop()
	}
	s.wg.Wait()
	s.finish()
	if gracefulClose || errors.Is(acceptErr, context.Canceled) {
		return nil
	}
	return acceptErr
}

func (s *AssociationServer) acceptLoop() error {
	for {
		if s.options.SaturationPolicy == SaturationBackpressure {
			select {
			case s.slots <- struct{}{}:
			case <-s.ctx.Done():
				return s.ctx.Err()
			}
			conn, err := s.listener.Accept()
			if err != nil {
				s.releaseSlot()
				return err
			}
			s.startConnection(conn)
			continue
		}

		conn, err := s.listener.Accept()
		if err != nil {
			return err
		}
		select {
		case s.slots <- struct{}{}:
			s.startConnection(conn)
		default:
			s.rejectSaturated(conn)
		}
	}
}

func (s *AssociationServer) startConnection(conn net.Conn) {
	entry := &serverConnection{conn: conn}
	s.mu.Lock()
	s.connections[entry] = struct{}{}
	s.mu.Unlock()
	s.wg.Add(1)
	go s.serveConnection(entry)
}

func (s *AssociationServer) serveConnection(entry *serverConnection) {
	defer s.wg.Done()
	defer s.releaseSlot()
	defer func() {
		s.mu.Lock()
		delete(s.connections, entry)
		s.mu.Unlock()
		_ = entry.conn.Close()
	}()

	s.negotiating.Add(1)
	acceptOptions := s.options.Accept
	acceptOptions.Context = s.ctx
	originalRejected := acceptOptions.OnAssociationRejected
	acceptOptions.OnAssociationRejected = func() {
		s.rejected.Add(1)
		if originalRejected != nil {
			originalRejected()
		}
	}
	assoc, err, panicked := func() (*Association, error, bool) {
		defer s.negotiating.Add(-1)
		return acceptAssociationSafely(entry.conn, acceptOptions)
	}()
	if panicked {
		s.negotiationPanics.Add(1)
		return
	}
	if err != nil {
		return
	}
	s.mu.Lock()
	entry.assoc = assoc
	s.mu.Unlock()
	s.active.Add(1)
	s.accepted.Add(1)
	defer s.active.Add(-1)
	defer func() {
		_ = assoc.Close()
		lifecycle := assoc.TelemetrySnapshot()
		switch {
		case lifecycle.Aborted > 0:
			s.aborted.Add(1)
		case lifecycle.Released > 0:
			s.released.Add(1)
		default:
			s.closed.Add(1)
		}
	}()

	handlerErr, panicked := callAssociationHandlerSafely(s.options.Handler, s.ctx, assoc)
	if panicked {
		s.handlerPanics.Add(1)
		abortCtx, cancelAbort := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = assoc.AbortWithContext(abortCtx, AbortReasonNotSpecified)
		cancelAbort()
		return
	}
	if handlerErr != nil && !errors.Is(handlerErr, context.Canceled) {
		s.handlerErrors.Add(1)
	}
}

func (s *AssociationServer) rejectSaturated(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	options := s.options.Accept
	ctx, cancel := contextWithOptionalTimeout(s.ctx, options.NegotiationTimeout)
	defer cancel()
	options.Context = ctx

	state, err := newOperationalState(options.Observability, telemetry.RoleSCP)
	if err != nil {
		return
	}
	defer state.close()
	state.setConnection(conn)
	if options.TLSConfig != nil {
		conn, err = serverTLSConn(ctx, conn, options.TLSConfig)
		if err != nil {
			return
		}
	}
	pdu, err := readAssociationPDUObserved(ctx, conn, options.MaxPDU, state)
	if err != nil {
		return
	}
	if _, ok := pdu.(*AssociationRQ); !ok {
		return
	}
	rj := &AssociationRJ{
		Result: AssociateRJResultTransient,
		Source: AssociateRJSourceServiceProviderPresentation,
		Reason: AssociateRJReasonLocalLimitExceeded,
	}
	// Count the server decision before exposing the rejection on the wire so a
	// client that receives the RJ can immediately observe a consistent snapshot.
	s.rejected.Add(1)
	_ = writeAssociationPDUObserved(ctx, conn, rj, state)
}

func (s *AssociationServer) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.serveMu.Lock()
	s.stopped = true
	serving := s.serving
	s.serveMu.Unlock()
	s.stopOnce.Do(func() { _ = s.listener.Close() })
	if !serving {
		s.finish()
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.forceStop()
		return ctx.Err()
	}
}

func (s *AssociationServer) forceStop() {
	if s == nil {
		return
	}
	s.serveMu.Lock()
	s.stopped = true
	s.serveMu.Unlock()
	s.stopOnce.Do(func() { _ = s.listener.Close() })
	type stopTarget struct {
		conn  net.Conn
		assoc *Association
	}
	s.mu.Lock()
	connections := make([]stopTarget, 0, len(s.connections))
	for entry := range s.connections {
		connections = append(connections, stopTarget{conn: entry.conn, assoc: entry.assoc})
	}
	s.mu.Unlock()
	abortCtx, cancelAbort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelAbort()
	for _, entry := range connections {
		if entry.assoc != nil {
			_ = entry.assoc.AbortWithContext(abortCtx, AbortReasonNotSpecified)
			entry.assoc.operational.markAborted()
		}
		_ = entry.conn.Close()
	}
	s.cancel()
}

func (s *AssociationServer) isStopped() bool {
	s.serveMu.Lock()
	defer s.serveMu.Unlock()
	return s.stopped
}

func acceptAssociationSafely(conn net.Conn, options AcceptOptions) (assoc *Association, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			assoc = nil
			err = nil
			panicked = true
		}
	}()
	assoc, err = Accept(conn, options)
	return assoc, err, false
}

func callAssociationHandlerSafely(handler AssociationHandler, ctx context.Context, assoc *Association) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			err = nil
			panicked = true
		}
	}()
	return handler(ctx, assoc), false
}

type serverValueContext struct {
	context.Context
	primary   context.Context
	secondary context.Context
}

func (c serverValueContext) Deadline() (time.Time, bool) {
	primaryDeadline, primaryOK := c.primary.Deadline()
	secondaryDeadline, secondaryOK := c.secondary.Deadline()
	switch {
	case primaryOK && secondaryOK && secondaryDeadline.Before(primaryDeadline):
		return secondaryDeadline, true
	case primaryOK:
		return primaryDeadline, true
	default:
		return secondaryDeadline, secondaryOK
	}
}

func (c serverValueContext) Value(key any) any {
	if value := c.primary.Value(key); value != nil {
		return value
	}
	return c.secondary.Value(key)
}

func mergeServerContexts(primary, secondary context.Context) (context.Context, context.CancelFunc) {
	if primary == nil {
		primary = context.Background()
	}
	if secondary == nil {
		secondary = context.Background()
	}
	base, cancelCause := context.WithCancelCause(context.Background())
	stopPrimary := context.AfterFunc(primary, func() { cancelCause(context.Cause(primary)) })
	stopSecondary := context.AfterFunc(secondary, func() { cancelCause(context.Cause(secondary)) })
	merged := serverValueContext{Context: base, primary: primary, secondary: secondary}
	return merged, func() {
		stopPrimary()
		stopSecondary()
		cancelCause(context.Canceled)
	}
}

func (s *AssociationServer) finish() {
	s.finishOnce.Do(func() {
		s.cancel()
		if s.ownedDispatcher != nil {
			s.ownedDispatcher.Close()
		}
		if s.ownedRawDispatcher != nil {
			s.ownedRawDispatcher.Close()
		}
		close(s.done)
	})
}

func (s *AssociationServer) releaseSlot() {
	select {
	case <-s.slots:
	default:
	}
}

func (s *AssociationServer) Snapshot() AssociationServerMetrics {
	if s == nil {
		return AssociationServerMetrics{}
	}
	metrics := AssociationServerMetrics{
		Negotiating:       s.negotiating.Load(),
		Active:            s.active.Load(),
		Accepted:          s.accepted.Load(),
		Rejected:          s.rejected.Load(),
		NegotiationPanics: s.negotiationPanics.Load(),
		HandlerErrors:     s.handlerErrors.Load(),
		HandlerPanics:     s.handlerPanics.Load(),
		Released:          s.released.Load(),
		Aborted:           s.aborted.Load(),
		Closed:            s.closed.Load(),
	}
	if s.ownedDispatcher != nil {
		dispatcher := s.ownedDispatcher.Stats()
		metrics.DroppedEvents = dispatcher.DroppedEvents
		metrics.SinkPanics = dispatcher.SinkPanics
	}
	if s.ownedRawDispatcher != nil {
		dispatcher := s.ownedRawDispatcher.Stats()
		metrics.RawDropped = dispatcher.Dropped
		metrics.RawSinkPanics = dispatcher.Panics
	}
	return metrics
}
