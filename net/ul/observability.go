package ul

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
)

// ObservabilityOptions configures per-association event correlation. Zero
// values preserve the existing silent transport behavior.
type ObservabilityOptions struct {
	Sink telemetry.Sink
	// Dispatcher may be shared across associations to bound observer workers.
	// Sink and Dispatcher are mutually exclusive; the caller owns a shared
	// Dispatcher and closes it after all associations have stopped.
	Dispatcher             *telemetry.Dispatcher
	EventQueueDepth        int
	associationIDGenerator func() string
	EndpointPolicy         telemetry.EndpointPolicy

	// RawPDUSink is an explicit PHI-bearing P-DATA capture destination. It is
	// disabled unless both byte limits below are positive. Association PDUs,
	// which may contain User Identity credentials, are never captured.
	RawPDUSink                  RawPDUSink
	RawPDUDispatcher            *RawPDUDispatcher
	MaxCapturedPDUBytes         int
	MaxCapturedAssociationBytes int64
	RawPDUQueueDepth            int
}

// RawPDUCapture owns a bounded copy of an encoded P-DATA-TF PDU. Data can
// contain identifiers, query keys, clinical metadata, and pixel data.
type RawPDUCapture struct {
	Time          time.Time
	AssociationID string
	Role          telemetry.AssociationRole
	Direction     telemetry.Direction
	PDUType       PDUType
	OriginalBytes int64
	Data          []byte
	Truncated     bool
}

type RawPDUSink interface {
	CaptureRawPDU(context.Context, RawPDUCapture)
}

type RawPDUSinkFunc func(context.Context, RawPDUCapture)

func (f RawPDUSinkFunc) CaptureRawPDU(ctx context.Context, capture RawPDUCapture) {
	if f != nil {
		f(ctx, capture)
	}
}

type RawPDUDispatcherStats struct {
	Dropped uint64
	Panics  uint64
}

// RawPDUDispatcher bounds PHI-bearing delivery across associations. A server
// should share one dispatcher so a blocked sink consumes one fixed worker.
type RawPDUDispatcher struct {
	sink     RawPDUSink
	captures chan RawPDUCapture
	done     chan struct{}

	mu      sync.RWMutex
	closed  bool
	dropped atomic.Uint64
	panics  atomic.Uint64
}

func NewRawPDUDispatcher(sink RawPDUSink, queueDepth int) *RawPDUDispatcher {
	if sink == nil {
		return nil
	}
	if queueDepth <= 0 {
		queueDepth = 8
	}
	dispatcher := &RawPDUDispatcher{
		sink:     sink,
		captures: make(chan RawPDUCapture, queueDepth),
		done:     make(chan struct{}),
	}
	go dispatcher.run()
	return dispatcher
}

func (d *RawPDUDispatcher) Emit(capture RawPDUCapture) bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return false
	}
	select {
	case d.captures <- capture:
		return true
	default:
		d.dropped.Add(1)
		return false
	}
}

func (d *RawPDUDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.captures)
	}
	d.mu.Unlock()
}

func (d *RawPDUDispatcher) Done() <-chan struct{} {
	if d == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return d.done
}

func (d *RawPDUDispatcher) Stats() RawPDUDispatcherStats {
	if d == nil {
		return RawPDUDispatcherStats{}
	}
	return RawPDUDispatcherStats{Dropped: d.dropped.Load(), Panics: d.panics.Load()}
}

func (d *RawPDUDispatcher) run() {
	defer close(d.done)
	for capture := range d.captures {
		func() {
			defer func() {
				if recover() != nil {
					d.panics.Add(1)
				}
			}()
			d.sink.CaptureRawPDU(context.Background(), capture)
		}()
	}
}

type operationalState struct {
	id             string
	role           telemetry.AssociationRole
	started        time.Time
	events         *telemetry.Dispatcher
	ownedEvents    bool
	monitoring     bool
	endpointPolicy telemetry.EndpointPolicy
	localAETitle   string
	remoteAETitle  string
	localAddress   string
	remoteAddress  string

	rawDispatcher      *RawPDUDispatcher
	ownedRawDispatcher bool
	maxRawPDUBytes     int
	rawBytesRemaining  int64

	pdusInbound         atomic.Uint64
	pdusOutbound        atomic.Uint64
	bytesInbound        atomic.Uint64
	bytesOutbound       atomic.Uint64
	malformedPDUs       atomic.Uint64
	truncatedPDUs       atomic.Uint64
	timeouts            atomic.Uint64
	protocolErrors      atomic.Uint64
	droppedEvents       atomic.Uint64
	rawCapturedBytes    atomic.Uint64
	rawDropped          atomic.Uint64
	established         atomic.Uint64
	rejected            atomic.Uint64
	released            atomic.Uint64
	aborted             atomic.Uint64
	closedAssociations  atomic.Uint64
	commandsInbound     atomic.Uint64
	commandsOutbound    atomic.Uint64
	successResponses    atomic.Uint64
	pendingResponses    atomic.Uint64
	warningResponses    atomic.Uint64
	failureResponses    atomic.Uint64
	canceledResponses   atomic.Uint64
	operationsStarted   atomic.Uint64
	operationsCompleted atomic.Uint64
	operationErrors     atomic.Uint64
	canceledOperations  atomic.Uint64
	activeOperations    atomic.Int64

	establishedOnce sync.Once
	rejectedOnce    sync.Once
	releasedOnce    sync.Once
	abortedOnce     sync.Once
	closedOnce      sync.Once
	mu              sync.RWMutex
	closed          bool
}

func newOperationalState(options *ObservabilityOptions, role telemetry.AssociationRole) (*operationalState, error) {
	if options == nil {
		options = &ObservabilityOptions{}
	}
	if err := validateObservabilityOptions(options); err != nil {
		return nil, err
	}
	id := ""
	if options.associationIDGenerator != nil {
		id = options.associationIDGenerator()
	}
	if id == "" {
		var err error
		id, err = newAssociationID()
		if err != nil {
			return nil, err
		}
	}
	depth := options.EventQueueDepth
	if depth == 0 {
		depth = 64
	}
	policy := options.EndpointPolicy
	policy.HMACKey = append([]byte(nil), policy.HMACKey...)
	events := options.Dispatcher
	ownedEvents := false
	if events == nil {
		events = telemetry.NewDispatcher(options.Sink, depth)
		ownedEvents = events != nil
	}
	rawDispatcher := options.RawPDUDispatcher
	ownedRawDispatcher := false
	if rawDispatcher == nil && options.RawPDUSink != nil {
		rawDispatcher = NewRawPDUDispatcher(options.RawPDUSink, options.RawPDUQueueDepth)
		ownedRawDispatcher = true
	}
	state := &operationalState{
		id:                 id,
		role:               role,
		started:            time.Now(),
		events:             events,
		ownedEvents:        ownedEvents,
		monitoring:         events != nil || rawDispatcher != nil,
		endpointPolicy:     policy,
		rawDispatcher:      rawDispatcher,
		ownedRawDispatcher: ownedRawDispatcher,
		maxRawPDUBytes:     options.MaxCapturedPDUBytes,
		rawBytesRemaining:  options.MaxCapturedAssociationBytes,
	}
	return state, nil
}

func validateObservabilityOptions(options *ObservabilityOptions) error {
	if options == nil {
		return nil
	}
	if options.EventQueueDepth < 0 {
		return fmt.Errorf("dicom ul: observability event queue depth must not be negative")
	}
	if options.Sink != nil && options.Dispatcher != nil {
		return fmt.Errorf("dicom ul: observability Sink and Dispatcher are mutually exclusive")
	}
	if options.RawPDUSink != nil && options.RawPDUDispatcher != nil {
		return fmt.Errorf("dicom ul: raw PDU sink and dispatcher are mutually exclusive")
	}
	if err := options.EndpointPolicy.Validate(); err != nil {
		return err
	}
	if options.RawPDUQueueDepth < 0 {
		return fmt.Errorf("dicom ul: raw PDU queue depth must not be negative")
	}
	if (options.RawPDUSink != nil || options.RawPDUDispatcher != nil) && (options.MaxCapturedPDUBytes <= 0 || options.MaxCapturedAssociationBytes <= 0) {
		return fmt.Errorf("dicom ul: raw PDU capture requires positive per-PDU and per-association byte limits")
	}
	return nil
}

func (s *operationalState) emit(event telemetry.Event) {
	if s == nil || s.events == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	event.Elapsed = event.Time.Sub(s.started)
	event.AssociationID = s.id
	event.Role = s.role
	s.mu.RLock()
	event.LocalAETitle = s.localAETitle
	event.RemoteAETitle = s.remoteAETitle
	event.LocalAddress = s.localAddress
	event.RemoteAddress = s.remoteAddress
	s.mu.RUnlock()
	if !s.events.Emit(event) {
		s.droppedEvents.Add(1)
	}
}

func (s *operationalState) setConnection(conn net.Conn) {
	if s == nil || conn == nil {
		return
	}
	localAddress := ""
	remoteAddress := ""
	if address := conn.LocalAddr(); address != nil {
		localAddress = address.String()
	}
	if address := conn.RemoteAddr(); address != nil {
		remoteAddress = address.String()
	}
	s.mu.Lock()
	s.localAddress = s.endpointPolicy.Address(localAddress)
	s.remoteAddress = s.endpointPolicy.Address(remoteAddress)
	s.mu.Unlock()
}

func (s *operationalState) setAETitles(called, calling string) {
	if s == nil {
		return
	}
	local := calling
	remote := called
	if s.role == telemetry.RoleSCP {
		local = called
		remote = calling
	}
	s.mu.Lock()
	s.localAETitle = s.endpointPolicy.AETitle(local)
	s.remoteAETitle = s.endpointPolicy.AETitle(remote)
	s.mu.Unlock()
}

func (s *operationalState) close() {
	if s == nil {
		return
	}
	s.closedOnce.Do(func() {
		s.closedAssociations.Add(1)
		s.emit(telemetry.Event{Kind: telemetry.AssociationClosed})
	})
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.ownedEvents && s.events != nil {
			s.events.Close()
		}
		if s.ownedRawDispatcher && s.rawDispatcher != nil {
			s.rawDispatcher.Close()
		}
	}
	s.mu.Unlock()
}

func (s *operationalState) markEstablished() {
	if s == nil {
		return
	}
	s.establishedOnce.Do(func() {
		s.established.Add(1)
		s.emit(telemetry.Event{Kind: telemetry.AssociationEstablished})
	})
}

func (s *operationalState) markRejected(rejection *AssociationRJ) {
	if s == nil {
		return
	}
	s.rejectedOnce.Do(func() {
		s.rejected.Add(1)
		event := telemetry.Event{Kind: telemetry.AssociationRejected}
		if rejection != nil {
			event.RejectionResult = rejection.Result
			event.RejectionSource = rejection.Source
			event.RejectionReason = rejection.Reason
		}
		s.emit(event)
	})
}

func (s *operationalState) markReleased() {
	if s == nil {
		return
	}
	s.releasedOnce.Do(func() {
		s.released.Add(1)
		s.emit(telemetry.Event{Kind: telemetry.AssociationReleased})
	})
}

func (s *operationalState) markAborted() {
	if s == nil {
		return
	}
	s.abortedOnce.Do(func() {
		s.aborted.Add(1)
		s.emit(telemetry.Event{Kind: telemetry.AssociationAborted})
	})
}

func (s *operationalState) observeCommand(assoc *Association, observation telemetry.CommandObservation) {
	if !s.enabled() {
		return
	}
	observation.Direction = safeDirection(observation.Direction)
	if observation.Direction == telemetry.Inbound {
		s.commandsInbound.Add(1)
	} else if observation.Direction == telemetry.Outbound {
		s.commandsOutbound.Add(1)
	}
	if observation.StatusSet {
		observation.StatusCategory = safeStatusCategory(observation.StatusCategory)
		switch observation.StatusCategory {
		case telemetry.StatusCategorySuccess:
			s.successResponses.Add(1)
		case telemetry.StatusCategoryPending:
			s.pendingResponses.Add(1)
		case telemetry.StatusCategoryWarning:
			s.warningResponses.Add(1)
		case telemetry.StatusCategoryFailure:
			s.failureResponses.Add(1)
		case telemetry.StatusCategoryCanceled:
			s.canceledResponses.Add(1)
		}
	} else {
		observation.StatusCategory = ""
	}
	// Never trust caller-supplied syntax strings. Only negotiated presentation
	// context metadata is eligible for the safe event.
	observation.AbstractSyntaxUID = ""
	observation.TransferSyntaxUID = ""
	if assoc != nil {
		for _, accepted := range assoc.AcceptedContexts {
			if accepted.ID == observation.PresentationContextID {
				observation.AbstractSyntaxUID = accepted.AbstractSyntaxUID
				observation.TransferSyntaxUID = accepted.TransferSyntaxUID
				break
			}
		}
	}
	s.emit(telemetry.Event{
		Kind:                  telemetry.CommandObserved,
		Direction:             observation.Direction,
		PresentationContextID: observation.PresentationContextID,
		AbstractSyntaxUID:     observation.AbstractSyntaxUID,
		TransferSyntaxUID:     observation.TransferSyntaxUID,
		CommandField:          observation.CommandField,
		DataSetPresent:        observation.DataSetPresent,
		Status:                observation.Status,
		StatusSet:             observation.StatusSet,
		StatusCategory:        observation.StatusCategory,
		Duration:              observation.Duration,
	})
}

func (s *operationalState) observeOperation(assoc *Association, observation telemetry.OperationObservation) {
	if !s.enabled() {
		return
	}
	kind := telemetry.OperationStarted
	observation.ErrorClass = safeOperationErrorClass(observation.ErrorClass)
	if observation.Completed {
		kind = telemetry.OperationCompleted
		s.operationsCompleted.Add(1)
		s.activeOperations.Add(-1)
		if observation.ErrorClass != "" {
			s.operationErrors.Add(1)
			switch observation.ErrorClass {
			case telemetry.ErrorClassTimeout:
				s.timeouts.Add(1)
			case telemetry.ErrorClassProtocol:
				s.protocolErrors.Add(1)
			case telemetry.ErrorClassCanceled:
				s.canceledOperations.Add(1)
			}
		}
	} else {
		s.operationsStarted.Add(1)
		s.activeOperations.Add(1)
	}
	event := telemetry.Event{
		Kind:                  kind,
		PresentationContextID: observation.PresentationContextID,
		CommandField:          observation.CommandField,
		Duration:              observation.Duration,
		ErrorClass:            observation.ErrorClass,
	}
	if assoc != nil {
		for _, accepted := range assoc.AcceptedContexts {
			if accepted.ID == observation.PresentationContextID {
				event.AbstractSyntaxUID = accepted.AbstractSyntaxUID
				event.TransferSyntaxUID = accepted.TransferSyntaxUID
				break
			}
		}
	}
	s.emit(event)
}

func safeDirection(direction telemetry.Direction) telemetry.Direction {
	switch direction {
	case telemetry.Inbound, telemetry.Outbound:
		return direction
	default:
		return ""
	}
}

func safeStatusCategory(category string) string {
	switch category {
	case telemetry.StatusCategorySuccess,
		telemetry.StatusCategoryPending,
		telemetry.StatusCategoryWarning,
		telemetry.StatusCategoryFailure,
		telemetry.StatusCategoryCanceled:
		return category
	default:
		return telemetry.StatusCategoryFailure
	}
}

func safeOperationErrorClass(errorClass string) string {
	switch errorClass {
	case "",
		telemetry.ErrorClassTimeout,
		telemetry.ErrorClassProtocol,
		telemetry.ErrorClassCanceled,
		telemetry.ErrorClassDIMSEStatus,
		telemetry.ErrorClassHandlerOrTransport:
		return errorClass
	default:
		return telemetry.ErrorClassOther
	}
}

func (s *operationalState) enabled() bool {
	return s != nil && s.monitoring
}

func (s *operationalState) observePDU(direction telemetry.Direction, pdu PDU, actualLength int64, encoded []byte) {
	s.observePDUWithByteCount(direction, pdu, actualLength, actualLength, encoded)
}

func (s *operationalState) observePDUWithByteCount(direction telemetry.Direction, pdu PDU, actualLength, byteCount int64, encoded []byte) {
	if !s.enabled() {
		return
	}
	if byteCount > 0 {
		if direction == telemetry.Inbound {
			s.bytesInbound.Add(uint64(byteCount))
		} else {
			s.bytesOutbound.Add(uint64(byteCount))
		}
	}
	if pdu != nil {
		s.observeAssociationMetadata(pdu)
		if direction == telemetry.Inbound {
			s.pdusInbound.Add(1)
		} else {
			s.pdusOutbound.Add(1)
		}
	}
	event := telemetry.Event{
		Kind:           telemetry.PDUObserved,
		Direction:      direction,
		PDUType:        byte(PDUTypeOf(pdu)),
		ActualLength:   actualLength,
		DeclaredLength: declaredPDUSize(actualLength),
	}
	switch pdu.(type) {
	case *UnknownPDU, UnknownPDU:
		event.ErrorClass = "unsupported_pdu"
		s.protocolErrors.Add(1)
	}
	if data, ok := pdu.(*PDataTF); ok && len(data.Values) > 0 {
		event.PresentationContextID = commonPresentationContextID(data.Values)
	}
	s.emit(event)
	s.captureRawPDU(direction, pdu, actualLength, encoded)
}

func (s *operationalState) observeAssociationMetadata(pdu PDU) {
	switch value := pdu.(type) {
	case *AssociationRQ:
		s.setAETitles(value.CalledAETitle, value.CallingAETitle)
	case AssociationRQ:
		s.setAETitles(value.CalledAETitle, value.CallingAETitle)
	case *AssociationAC:
		s.setAETitles(value.CalledAETitle, value.CallingAETitle)
	case AssociationAC:
		s.setAETitles(value.CalledAETitle, value.CallingAETitle)
	}
}

func (s *operationalState) observePDUError(direction telemetry.Direction, encodedPrefix []byte, actualLength int64, err error) {
	s.observePDUErrorWithByteCount(direction, encodedPrefix, actualLength, actualLength, err)
}

func (s *operationalState) observePDUErrorWithByteCount(direction telemetry.Direction, encodedPrefix []byte, actualLength, byteCount int64, err error) {
	if !s.enabled() {
		return
	}
	if byteCount > 0 {
		if direction == telemetry.Inbound {
			s.bytesInbound.Add(uint64(byteCount))
		} else {
			s.bytesOutbound.Add(uint64(byteCount))
		}
	}
	event := telemetry.Event{
		Kind:         telemetry.PDUObserved,
		Direction:    direction,
		ActualLength: actualLength,
	}
	if len(encodedPrefix) > 0 {
		event.PDUType = encodedPrefix[0]
	}
	if len(encodedPrefix) >= int(PDUHeaderSize) {
		event.DeclaredLength = binary.BigEndian.Uint32(encodedPrefix[2:PDUHeaderSize])
	}
	switch {
	case direction == telemetry.Outbound && (errors.Is(err, ErrAssociationTimeout) || errors.Is(err, context.DeadlineExceeded)):
		event.ErrorClass = "write_timeout"
		s.timeouts.Add(1)
	case direction == telemetry.Outbound && errors.Is(err, context.Canceled):
		event.ErrorClass = "write_canceled"
	case direction == telemetry.Outbound:
		event.ErrorClass = "transport_write"
	case (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) && actualLength == 0:
		event.ErrorClass = "connection_closed"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		event.Truncated = true
		event.ErrorClass = "truncated_pdu"
		s.truncatedPDUs.Add(1)
	case errors.Is(err, ErrPDUTooLarge):
		event.Malformed = true
		event.ErrorClass = "oversized_pdu"
		s.malformedPDUs.Add(1)
		s.protocolErrors.Add(1)
	case errors.Is(err, ErrAssociationTimeout), errors.Is(err, context.DeadlineExceeded):
		event.ErrorClass = "read_timeout"
		s.timeouts.Add(1)
	case errors.Is(err, context.Canceled):
		event.ErrorClass = "read_canceled"
	case isMalformedPDUError(err):
		event.Malformed = true
		event.ErrorClass = "malformed_pdu"
		s.malformedPDUs.Add(1)
		s.protocolErrors.Add(1)
	default:
		event.ErrorClass = "transport_read"
	}
	s.emit(event)
}

func isMalformedPDUError(err error) bool {
	return errors.Is(err, ErrInvalidPDU) ||
		errors.Is(err, ErrInvalidPDUSize) ||
		errors.Is(err, ErrLengthOverflow) ||
		errors.Is(err, ErrUnsupportedPDU) ||
		errors.Is(err, ErrInvalidPDUField) ||
		errors.Is(err, ErrMissingPDUField) ||
		errors.Is(err, ErrInvalidPDUItem) ||
		errors.Is(err, ErrInvalidUserItem) ||
		errors.Is(err, ErrInvalidPCID)
}

func (s *operationalState) captureRawPDU(direction telemetry.Direction, pdu PDU, actualLength int64, encoded []byte) {
	if s == nil || s.rawDispatcher == nil || PDUTypeOf(pdu) != PDUDataTF || len(encoded) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.rawBytesRemaining <= 0 {
		return
	}
	limit := int64(s.maxRawPDUBytes)
	if s.rawBytesRemaining < limit {
		limit = s.rawBytesRemaining
	}
	if int64(len(encoded)) < limit {
		limit = int64(len(encoded))
	}
	if limit <= 0 {
		return
	}
	data := append([]byte(nil), encoded[:limit]...)
	s.rawBytesRemaining -= int64(len(data))
	capture := RawPDUCapture{
		Time:          time.Now(),
		AssociationID: s.id,
		Role:          s.role,
		Direction:     direction,
		PDUType:       PDUDataTF,
		OriginalBytes: actualLength,
		Data:          data,
		Truncated:     actualLength > int64(len(data)),
	}
	if s.rawDispatcher.Emit(capture) {
		s.rawCapturedBytes.Add(uint64(len(data)))
	} else {
		s.rawDropped.Add(1)
	}
}

func (s *operationalState) snapshot() telemetry.AssociationMetrics {
	if s == nil {
		return telemetry.AssociationMetrics{}
	}
	var sinkPanics uint64
	if s.ownedEvents {
		sinkPanics = s.events.Stats().SinkPanics
	}
	var rawSinkPanics uint64
	if s.ownedRawDispatcher {
		rawSinkPanics = s.rawDispatcher.Stats().Panics
	}
	return telemetry.AssociationMetrics{
		PDUsInbound:         s.pdusInbound.Load(),
		PDUsOutbound:        s.pdusOutbound.Load(),
		BytesInbound:        s.bytesInbound.Load(),
		BytesOutbound:       s.bytesOutbound.Load(),
		MalformedPDUs:       s.malformedPDUs.Load(),
		TruncatedPDUs:       s.truncatedPDUs.Load(),
		RawCapturedBytes:    s.rawCapturedBytes.Load(),
		RawDropped:          s.rawDropped.Load(),
		RawSinkPanics:       rawSinkPanics,
		DroppedEvents:       s.droppedEvents.Load(),
		SinkPanics:          sinkPanics,
		Timeouts:            s.timeouts.Load(),
		ProtocolErrors:      s.protocolErrors.Load(),
		Established:         s.established.Load(),
		Rejected:            s.rejected.Load(),
		Released:            s.released.Load(),
		Aborted:             s.aborted.Load(),
		Closed:              s.closedAssociations.Load(),
		CommandsInbound:     s.commandsInbound.Load(),
		CommandsOutbound:    s.commandsOutbound.Load(),
		SuccessResponses:    s.successResponses.Load(),
		PendingResponses:    s.pendingResponses.Load(),
		WarningResponses:    s.warningResponses.Load(),
		FailureResponses:    s.failureResponses.Load(),
		CanceledResponses:   s.canceledResponses.Load(),
		OperationsStarted:   s.operationsStarted.Load(),
		OperationsCompleted: s.operationsCompleted.Load(),
		OperationErrors:     s.operationErrors.Load(),
		CanceledOperations:  s.canceledOperations.Load(),
		ActiveOperations:    s.activeOperations.Load(),
	}
}

func declaredPDUSize(actualLength int64) uint32 {
	if actualLength <= int64(PDUHeaderSize) {
		return 0
	}
	length := actualLength - int64(PDUHeaderSize)
	if length > int64(^uint32(0)) {
		return 0
	}
	return uint32(length)
}

func commonPresentationContextID(values []PDataValue) byte {
	if len(values) == 0 {
		return 0
	}
	id := values[0].PresentationContextID
	for _, value := range values[1:] {
		if value.PresentationContextID != id {
			return 0
		}
	}
	return id
}

type countingWriter struct {
	w            io.Writer
	n            int64
	captureLimit int
	captured     []byte
}

type countingReader struct {
	r      io.Reader
	n      int64
	prefix []byte
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	remaining := int(PDUHeaderSize) - len(r.prefix)
	if remaining > n {
		remaining = n
	}
	if remaining > 0 {
		r.prefix = append(r.prefix, p[:remaining]...)
	}
	return n, err
}

func readAssociationPDUObserved(ctx context.Context, conn net.Conn, maxPDU uint32, state *operationalState) (PDU, error) {
	if !state.enabled() {
		return readAssociationPDU(ctx, conn, maxPDU)
	}
	reader := &countingReader{r: conn}
	var pdu PDU
	err := withConnReadDeadline(ctx, conn, func() error {
		var readErr error
		pdu, readErr = ReadPDU(reader, maxPDU)
		return readErr
	})
	if err != nil {
		state.observePDUError(telemetry.Inbound, reader.prefix, reader.n, err)
	} else {
		state.observePDU(telemetry.Inbound, pdu, reader.n, nil)
		switch terminal := pdu.(type) {
		case *AssociationRJ:
			state.markRejected(terminal)
		case *AbortRQ:
			state.markAborted()
		}
	}
	return pdu, err
}

func writeAssociationPDUObserved(ctx context.Context, conn net.Conn, pdu PDU, state *operationalState) error {
	if !state.enabled() {
		err := writeAssociationPDU(ctx, conn, pdu)
		if err == nil {
			switch terminal := pdu.(type) {
			case *AssociationRJ:
				state.markRejected(terminal)
			case AssociationRJ:
				state.markRejected(&terminal)
			case *AbortRQ, AbortRQ:
				state.markAborted()
			}
		}
		return err
	}
	writer := &countingWriter{w: conn, captureLimit: int(PDUHeaderSize)}
	err := withConnWriteDeadline(ctx, conn, func() error { return WritePDU(writer, pdu) })
	if err != nil {
		state.observePDUError(telemetry.Outbound, writer.captured, writer.n, err)
	} else {
		state.observePDU(telemetry.Outbound, pdu, writer.n, nil)
		switch terminal := pdu.(type) {
		case *AssociationRJ:
			state.markRejected(terminal)
		case AssociationRJ:
			state.markRejected(&terminal)
		case *AbortRQ, AbortRQ:
			state.markAborted()
		}
	}
	return err
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	remaining := w.captureLimit - len(w.captured)
	if remaining > n {
		remaining = n
	}
	if remaining > 0 {
		w.captured = append(w.captured, p[:remaining]...)
	}
	return n, err
}
