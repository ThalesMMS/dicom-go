// Package telemetry defines the safe operational event seam shared by the UL
// and DIMSE network layers. Events never contain DICOM command values, dataset
// values, raw PDU bytes, errors, credentials, or arbitrary maps.
package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Direction string

const (
	Inbound  Direction = "inbound"
	Outbound Direction = "outbound"
)

type AssociationRole string

const (
	RoleSCU AssociationRole = "scu"
	RoleSCP AssociationRole = "scp"
)

type EventKind string

const (
	PDUObserved            EventKind = "pdu.observed"
	AssociationEstablished EventKind = "association.established"
	AssociationRejected    EventKind = "association.rejected"
	AssociationReleased    EventKind = "association.released"
	AssociationAborted     EventKind = "association.aborted"
	AssociationClosed      EventKind = "association.closed"
	CommandObserved        EventKind = "dimse.command"
	OperationStarted       EventKind = "dimse.operation.started"
	OperationCompleted     EventKind = "dimse.operation.completed"
)

type EndpointMode uint8

const (
	EndpointOmit EndpointMode = iota
	EndpointPlaintext
	EndpointHMACSHA256
)

// EndpointPolicy centralizes the only supported disclosure modes for AE titles
// and network addresses. The zero value omits both. Plaintext requires explicit
// selection; HMAC produces stable correlation without retaining the source.
type EndpointPolicy struct {
	AETitles  EndpointMode
	Addresses EndpointMode
	HMACKey   []byte
}

func (p EndpointPolicy) Validate() error {
	if p.AETitles > EndpointHMACSHA256 {
		return fmt.Errorf("dicom telemetry: invalid AE title endpoint mode %d", p.AETitles)
	}
	if p.Addresses > EndpointHMACSHA256 {
		return fmt.Errorf("dicom telemetry: invalid address endpoint mode %d", p.Addresses)
	}
	if (p.AETitles == EndpointHMACSHA256 || p.Addresses == EndpointHMACSHA256) && len(p.HMACKey) == 0 {
		return fmt.Errorf("dicom telemetry: HMAC endpoint redaction requires a key")
	}
	return nil
}

func (p EndpointPolicy) AETitle(value string) string {
	return p.transform(p.AETitles, "ae", value)
}

func (p EndpointPolicy) Address(value string) string {
	return p.transform(p.Addresses, "address", value)
}

func (p EndpointPolicy) transform(mode EndpointMode, domain, value string) string {
	if value == "" || mode == EndpointOmit {
		return ""
	}
	if mode == EndpointPlaintext {
		return value
	}
	if mode != EndpointHMACSHA256 || len(p.HMACKey) == 0 {
		return ""
	}
	hash := hmac.New(sha256.New, p.HMACKey)
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	return "hmac-sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// Event is intentionally a closed collection of non-clinical fields. Endpoint
// fields remain empty unless the caller explicitly supplies a redaction policy.
type Event struct {
	Time                  time.Time
	Elapsed               time.Duration
	Kind                  EventKind
	AssociationID         string
	Role                  AssociationRole
	Direction             Direction
	PDUType               byte
	DeclaredLength        uint32
	ActualLength          int64
	PresentationContextID byte
	AbstractSyntaxUID     string
	TransferSyntaxUID     string
	LocalAETitle          string
	RemoteAETitle         string
	LocalAddress          string
	RemoteAddress         string
	Malformed             bool
	Truncated             bool
	ErrorClass            string
	CommandField          uint16
	DataSetPresent        bool
	Status                uint16
	StatusSet             bool
	StatusCategory        string
	Duration              time.Duration
	RejectionResult       byte
	RejectionSource       byte
	RejectionReason       byte
}

// CommandObservation is the PHI-free DIMSE subset accepted from the command
// layer. It intentionally has no Message ID, SOP Instance UID, identifier, or
// dataset value fields.
type CommandObservation struct {
	Direction             Direction
	PresentationContextID byte
	AbstractSyntaxUID     string
	TransferSyntaxUID     string
	CommandField          uint16
	DataSetPresent        bool
	Status                uint16
	StatusSet             bool
	StatusCategory        string
	Duration              time.Duration
}

type OperationObservation struct {
	Completed             bool
	PresentationContextID byte
	CommandField          uint16
	Duration              time.Duration
	ErrorClass            string
}

const (
	StatusCategorySuccess  = "success"
	StatusCategoryPending  = "pending"
	StatusCategoryWarning  = "warning"
	StatusCategoryFailure  = "failure"
	StatusCategoryCanceled = "canceled"
)

const (
	ErrorClassTimeout            = "timeout"
	ErrorClassProtocol           = "protocol"
	ErrorClassCanceled           = "canceled"
	ErrorClassDIMSEStatus        = "dimse_status"
	ErrorClassHandlerOrTransport = "handler_or_transport"
	ErrorClassOther              = "other"
)

type Sink interface {
	EmitNetworkEvent(context.Context, Event)
}

type SinkFunc func(context.Context, Event)

func (f SinkFunc) EmitNetworkEvent(ctx context.Context, event Event) {
	if f != nil {
		f(ctx, event)
	}
}

type DispatcherStats struct {
	DroppedEvents uint64
	SinkPanics    uint64
}

// AssociationMetrics is an exact atomic snapshot independent of best-effort
// event delivery.
type AssociationMetrics struct {
	PDUsInbound         uint64
	PDUsOutbound        uint64
	BytesInbound        uint64
	BytesOutbound       uint64
	MalformedPDUs       uint64
	TruncatedPDUs       uint64
	RawCapturedBytes    uint64
	RawDropped          uint64
	RawSinkPanics       uint64
	DroppedEvents       uint64
	SinkPanics          uint64
	CommandsInbound     uint64
	CommandsOutbound    uint64
	SuccessResponses    uint64
	PendingResponses    uint64
	WarningResponses    uint64
	FailureResponses    uint64
	CanceledResponses   uint64
	Timeouts            uint64
	ProtocolErrors      uint64
	Established         uint64
	Rejected            uint64
	Released            uint64
	Aborted             uint64
	Closed              uint64
	OperationsStarted   uint64
	OperationsCompleted uint64
	OperationErrors     uint64
	CanceledOperations  uint64
	ActiveOperations    int64
}

// Dispatcher isolates the network path from a slow or panicking Sink. Delivery
// is ordered per dispatcher and best effort; a full queue drops new events.
type Dispatcher struct {
	sink   Sink
	events chan Event

	mu     sync.RWMutex
	closed bool
	done   chan struct{}

	dropped atomic.Uint64
	panics  atomic.Uint64
}

func NewDispatcher(sink Sink, queueDepth int) *Dispatcher {
	if sink == nil {
		return nil
	}
	if queueDepth <= 0 {
		queueDepth = 64
	}
	d := &Dispatcher{
		sink:   sink,
		events: make(chan Event, queueDepth),
		done:   make(chan struct{}),
	}
	go d.run()
	return d
}

// Emit enqueues an event and reports whether it was accepted. A false result
// means the dispatcher is nil, closed, or full; a full queue is also reflected
// in Stats.
func (d *Dispatcher) Emit(event Event) bool {
	if d == nil {
		return false
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return false
	}
	select {
	case d.events <- event:
		return true
	default:
		d.dropped.Add(1)
		return false
	}
}

// EmitNetworkEvent lets one Dispatcher be shared by many associations without
// creating one sink goroutine per connection.
func (d *Dispatcher) EmitNetworkEvent(_ context.Context, event Event) {
	_ = d.Emit(event)
}

// Close starts a non-blocking ordered drain. It deliberately does not wait for
// a potentially stuck sink; callers can use Done when bounded waiting matters.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.events)
	}
	d.mu.Unlock()
}

func (d *Dispatcher) Done() <-chan struct{} {
	if d == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return d.done
}

func (d *Dispatcher) Stats() DispatcherStats {
	if d == nil {
		return DispatcherStats{}
	}
	return DispatcherStats{DroppedEvents: d.dropped.Load(), SinkPanics: d.panics.Load()}
}

func (d *Dispatcher) run() {
	defer close(d.done)
	for event := range d.events {
		func() {
			defer func() {
				if recover() != nil {
					d.panics.Add(1)
				}
			}()
			d.sink.EmitNetworkEvent(context.Background(), event)
		}()
	}
}
