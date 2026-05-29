package audit

import (
	"context"
	"time"
)

type EventKind string

const (
	AssociationAccepted EventKind = "association.accepted"
	AssociationRejected EventKind = "association.rejected"
	AssociationAborted  EventKind = "association.aborted"
	CommandReceived     EventKind = "command.received"
	ResponseSent        EventKind = "response.sent"
	OperationSucceeded  EventKind = "operation.succeeded"
	OperationFailed     EventKind = "operation.failed"
)

type Event struct {
	Time          time.Time
	Kind          EventKind
	Service       string
	LocalAETitle  string
	RemoteAETitle string
	RemoteAddr    string
	SOPClassUID   string
	Command       string
	Status        uint16
	StatusSet     bool
	ErrorClass    string
}

type Sink interface {
	EmitAuditEvent(context.Context, Event)
}

type SinkFunc func(context.Context, Event)

func (f SinkFunc) EmitAuditEvent(ctx context.Context, event Event) {
	if f != nil {
		f(ctx, event)
	}
}

func Emit(ctx context.Context, sink Sink, event Event) {
	if sink == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	sink.EmitAuditEvent(ctx, event)
}
