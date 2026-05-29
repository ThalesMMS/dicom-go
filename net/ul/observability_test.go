package ul

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
)

type benchmarkConn struct{}

func (benchmarkConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (benchmarkConn) Write(p []byte) (int, error)      { return len(p), nil }
func (benchmarkConn) Close() error                     { return nil }
func (benchmarkConn) LocalAddr() net.Addr              { return testAddr("local") }
func (benchmarkConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (benchmarkConn) SetDeadline(time.Time) error      { return nil }
func (benchmarkConn) SetReadDeadline(time.Time) error  { return nil }
func (benchmarkConn) SetWriteDeadline(time.Time) error { return nil }

func TestAssociationObservabilityCorrelatesSafePDUEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	events := make(chan telemetry.Event, 32)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "SAFE_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
			Observability: &ObservabilityOptions{
				associationIDGenerator: func() string { return "assoc-safe-1" },
				EventQueueDepth:        16,
				Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
					events <- event
				}),
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		if assoc.ID() != "assoc-safe-1" {
			serverDone <- errors.New("server association ID mismatch")
			return
		}
		if err := assoc.Send(ctx, &PDataTF{Values: []PDataValue{{
			PresentationContextID: 1,
			IsCommand:             true,
			IsLast:                true,
			Data:                  []byte("not captured by default"),
		}}}); err != nil {
			serverDone <- err
			return
		}
		pdu, err := assoc.Receive(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.Send(ctx, &ReleaseRP{})
	}()

	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "SAFE_SCP",
		CallingAETitle: "SAFE_SCU",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Receive(ctx); err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if err := client.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.AssociationID != "assoc-safe-1" {
				t.Fatalf("event AssociationID = %q", event.AssociationID)
			}
			if event.LocalAETitle != "" || event.RemoteAETitle != "" || event.LocalAddress != "" || event.RemoteAddress != "" {
				t.Fatalf("safe-default event leaked endpoint metadata: %+v", event)
			}
			if event.Kind == telemetry.PDUObserved && event.Direction == telemetry.Outbound && event.PDUType == byte(PDUDataTF) {
				if event.PresentationContextID != 1 || event.ActualLength <= int64(PDUHeaderSize) || event.DeclaredLength == 0 {
					t.Fatalf("outbound P-DATA event = %+v", event)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for outbound P-DATA observability event")
		}
	}
}

func TestRawPDUCaptureRequiresExplicitPositiveBounds(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	_, err := Accept(server, AcceptOptions{Observability: &ObservabilityOptions{
		RawPDUSink: RawPDUSinkFunc(func(context.Context, RawPDUCapture) {}),
	}})
	if err == nil {
		t.Fatal("Accept() with unbounded raw PDU sink succeeded")
	}
}

func TestRawPDUCaptureIsOptInBoundedAndPDataOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	captures := make(chan RawPDUCapture, 4)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "RAW_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
			Observability: &ObservabilityOptions{
				associationIDGenerator:      func() string { return "raw-assoc" },
				RawPDUSink:                  RawPDUSinkFunc(func(_ context.Context, capture RawPDUCapture) { captures <- capture }),
				MaxCapturedPDUBytes:         12,
				MaxCapturedAssociationBytes: 12,
				RawPDUQueueDepth:            2,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		if err := assoc.Send(ctx, &PDataTF{Values: []PDataValue{{
			PresentationContextID: 1,
			IsCommand:             true,
			IsLast:                true,
			Data:                  []byte("contains clinical payload"),
		}}}); err != nil {
			serverDone <- err
			return
		}
		pdu, err := assoc.Receive(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.Send(ctx, &ReleaseRP{})
	}()

	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "RAW_SCP",
		CallingAETitle: "RAW_SCU",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Receive(ctx); err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if err := client.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	select {
	case capture := <-captures:
		if capture.AssociationID != "raw-assoc" || capture.PDUType != PDUDataTF || capture.Direction != telemetry.Outbound {
			t.Fatalf("capture metadata = %+v", capture)
		}
		if len(capture.Data) != 12 || capture.OriginalBytes <= int64(len(capture.Data)) || !capture.Truncated {
			t.Fatalf("capture bounds = original %d captured %d truncated %t", capture.OriginalBytes, len(capture.Data), capture.Truncated)
		}
		if capture.Data[0] != byte(PDUDataTF) {
			t.Fatalf("capture first byte = 0x%02X, want P-DATA-TF", capture.Data[0])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for raw P-DATA capture")
	}
	select {
	case extra := <-captures:
		t.Fatalf("captured non-P-DATA or exceeded association budget: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRawPDUDispatcherRecoversPanicAndContinues(t *testing.T) {
	delivered := make(chan struct{}, 1)
	var calls atomic.Int32
	dispatcher := NewRawPDUDispatcher(RawPDUSinkFunc(func(context.Context, RawPDUCapture) {
		if calls.Add(1) == 1 {
			panic("raw sink failure")
		}
		delivered <- struct{}{}
	}), 2)
	dispatcher.Emit(RawPDUCapture{AssociationID: "first"})
	dispatcher.Emit(RawPDUCapture{AssociationID: "second"})
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("raw dispatcher stopped after sink panic")
	}
	dispatcher.Close()
	<-dispatcher.Done()
	if got := dispatcher.Stats().Panics; got != 1 {
		t.Fatalf("raw sink panics = %d, want 1", got)
	}
}

func TestSharedDispatcherDropsRemainAttributedToTheEmittingAssociation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	dispatcher := telemetry.NewDispatcher(telemetry.SinkFunc(func(context.Context, telemetry.Event) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
	}), 1)
	first, err := newOperationalState(&ObservabilityOptions{Dispatcher: dispatcher}, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState(first) error = %v", err)
	}
	second, err := newOperationalState(&ObservabilityOptions{Dispatcher: dispatcher}, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState(second) error = %v", err)
	}
	first.emit(telemetry.Event{Kind: telemetry.PDUObserved})
	<-started
	first.emit(telemetry.Event{Kind: telemetry.PDUObserved})
	first.emit(telemetry.Event{Kind: telemetry.PDUObserved})

	if got := first.snapshot().DroppedEvents; got != 1 {
		t.Fatalf("first DroppedEvents = %d, want 1", got)
	}
	if got := second.snapshot().DroppedEvents; got != 0 {
		t.Fatalf("second DroppedEvents = %d, want 0", got)
	}
	if got := dispatcher.Stats().DroppedEvents; got != 1 {
		t.Fatalf("dispatcher DroppedEvents = %d, want 1", got)
	}
	close(release)
	dispatcher.Close()
	<-dispatcher.Done()
}

func TestCommandObservationUsesNegotiatedSyntaxAndCountsPending(t *testing.T) {
	events := make(chan telemetry.Event, 2)
	state, err := newOperationalState(&ObservabilityOptions{Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
		events <- event
	})}, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState() error = %v", err)
	}
	assoc := &Association{
		operational: state,
		AcceptedContexts: []AcceptedContext{{
			ID:                3,
			AbstractSyntaxUID: verificationSOPClassUID,
			TransferSyntaxUID: ImplicitVRLittleEndian,
		}},
	}
	assoc.RecordCommandObservation(telemetry.CommandObservation{
		Direction:             telemetry.Inbound,
		PresentationContextID: 3,
		AbstractSyntaxUID:     "9.9.9",
		TransferSyntaxUID:     "8.8.8",
		CommandField:          0x8020,
		Status:                0xFF00,
		StatusSet:             true,
		StatusCategory:        telemetry.StatusCategoryPending,
	})
	select {
	case event := <-events:
		if event.AbstractSyntaxUID != verificationSOPClassUID || event.TransferSyntaxUID != ImplicitVRLittleEndian {
			t.Fatalf("command syntax = %q/%q", event.AbstractSyntaxUID, event.TransferSyntaxUID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for command event")
	}
	if got := assoc.TelemetrySnapshot().PendingResponses; got != 1 {
		t.Fatalf("PendingResponses = %d, want 1", got)
	}
}

func TestOperationErrorClassesUpdateExactCounters(t *testing.T) {
	state, err := newOperationalState(&ObservabilityOptions{Sink: telemetry.SinkFunc(func(context.Context, telemetry.Event) {})}, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState() error = %v", err)
	}
	assoc := &Association{operational: state}
	for _, errorClass := range []string{"timeout", "protocol", "canceled"} {
		assoc.RecordOperationObservation(telemetry.OperationObservation{})
		assoc.RecordOperationObservation(telemetry.OperationObservation{Completed: true, ErrorClass: errorClass})
	}
	metrics := assoc.TelemetrySnapshot()
	if metrics.OperationsStarted != 3 || metrics.OperationsCompleted != 3 || metrics.OperationErrors != 3 || metrics.ActiveOperations != 0 {
		t.Fatalf("operation metrics = %+v", metrics)
	}
	if metrics.Timeouts != 1 || metrics.ProtocolErrors != 1 || metrics.CanceledOperations != 1 {
		t.Fatalf("operation error class metrics = %+v", metrics)
	}
}

func TestObservationEnumsDoNotForwardArbitraryStrings(t *testing.T) {
	events := make(chan telemetry.Event, 4)
	state, err := newOperationalState(&ObservabilityOptions{Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
		events <- event
	})}, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState() error = %v", err)
	}
	defer state.close()
	assoc := &Association{operational: state}
	assoc.RecordCommandObservation(telemetry.CommandObservation{
		Direction:         telemetry.Direction("patient-derived-direction"),
		AbstractSyntaxUID: "patient-derived-abstract-syntax",
		TransferSyntaxUID: "patient-derived-transfer-syntax",
		StatusSet:         true,
		StatusCategory:    "patient-derived-category",
	})
	assoc.RecordCommandObservation(telemetry.CommandObservation{
		StatusSet:      false,
		StatusCategory: "patient-derived-without-status",
	})
	assoc.RecordOperationObservation(telemetry.OperationObservation{})
	assoc.RecordOperationObservation(telemetry.OperationObservation{Completed: true, ErrorClass: "patient-derived-error"})

	seenCommands := 0
	seenOperation := false
	deadline := time.After(time.Second)
	for seenCommands < 2 || !seenOperation {
		select {
		case event := <-events:
			switch event.Kind {
			case telemetry.CommandObserved:
				seenCommands++
				if event.Direction != "" && event.Direction != telemetry.Inbound && event.Direction != telemetry.Outbound {
					t.Fatalf("Direction = %q, want a closed safe value", event.Direction)
				}
				if event.AbstractSyntaxUID != "" || event.TransferSyntaxUID != "" {
					t.Fatalf("unnegotiated syntax leaked into event: %+v", event)
				}
				if event.StatusSet && event.StatusCategory != telemetry.StatusCategoryFailure {
					t.Fatalf("StatusCategory = %q, want sanitized failure", event.StatusCategory)
				}
				if !event.StatusSet && event.StatusCategory != "" {
					t.Fatalf("StatusCategory without status = %q, want empty", event.StatusCategory)
				}
			case telemetry.OperationCompleted:
				seenOperation = true
				if event.ErrorClass != telemetry.ErrorClassOther {
					t.Fatalf("ErrorClass = %q, want sanitized other", event.ErrorClass)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for sanitized observation events")
		}
	}
}

func TestInboundAssociationRejectionEmitsSCULifecycleEvent(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	events := make(chan telemetry.Event, 4)
	state, err := newOperationalState(&ObservabilityOptions{
		associationIDGenerator: func() string { return "rejected-scu" },
		Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
			events <- event
		}),
	}, telemetry.RoleSCU)
	if err != nil {
		t.Fatalf("newOperationalState() error = %v", err)
	}
	rj := &AssociationRJ{Result: AssociateRJResultTransient, Source: AssociateRJSourceServiceProviderPresentation, Reason: AssociateRJReasonLocalLimitExceeded}
	writeDone := make(chan error, 1)
	go func() { writeDone <- writeAssociationPDU(context.Background(), server, rj) }()
	if _, err := readAssociationPDUObserved(context.Background(), client, DefaultMaxPDU, state); err != nil {
		t.Fatalf("readAssociationPDUObserved() error = %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("writeAssociationPDU() error = %v", err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind != telemetry.AssociationRejected {
				continue
			}
			if event.AssociationID != "rejected-scu" || event.Role != telemetry.RoleSCU || event.RejectionResult != rj.Result || event.RejectionSource != rj.Source || event.RejectionReason != rj.Reason {
				t.Fatalf("rejection event = %+v", event)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for SCU rejection lifecycle event")
		}
	}
}

func TestInboundNegotiationAbortEmitsSCULifecycleEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, serverDone := terminalNegotiationPeer(t, &AbortRQ{
		Source: AbortSourceServiceProvider,
		Reason: AbortReasonNotSpecified,
	})
	events := make(chan telemetry.Event, 8)
	_, err := DialContext(ctx, addr, DialOptions{
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
		Observability: &ObservabilityOptions{
			associationIDGenerator: func() string { return "aborted-scu" },
			Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
				events <- event
			}),
		},
	})
	var abortErr *AbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("DialContext() error = %v, want AbortError", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind != telemetry.AssociationAborted {
				continue
			}
			if event.AssociationID != "aborted-scu" || event.Role != telemetry.RoleSCU {
				t.Fatalf("abort event = %+v", event)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for SCU abort lifecycle event")
		}
	}
}

func TestDialObservabilityCoversNegotiationWithStableAssociationID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "TRACE_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		pdu, err := assoc.Receive(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.Send(ctx, &ReleaseRP{})
	}()

	events := make(chan telemetry.Event, 16)
	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "TRACE_SCP",
		CallingAETitle: "TRACE_SCU",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
		Observability: &ObservabilityOptions{
			associationIDGenerator: func() string { return "client-assoc" },
			Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
				events <- event
			}),
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if client.ID() != "client-assoc" {
		t.Fatalf("client ID() = %q", client.ID())
	}
	if err := client.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	want := map[struct {
		direction telemetry.Direction
		pduType   byte
	}]bool{
		{telemetry.Outbound, byte(PDUAssociateRQ)}: false,
		{telemetry.Inbound, byte(PDUAssociateAC)}:  false,
	}
	deadline := time.After(time.Second)
	for {
		all := true
		for _, seen := range want {
			all = all && seen
		}
		if all {
			return
		}
		select {
		case event := <-events:
			if event.AssociationID != "client-assoc" || event.Role != telemetry.RoleSCU {
				t.Fatalf("negotiation event correlation = %+v", event)
			}
			key := struct {
				direction telemetry.Direction
				pduType   byte
			}{event.Direction, event.PDUType}
			if _, ok := want[key]; ok {
				want[key] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for negotiation events: %+v", want)
		}
	}
}

func TestAssociationsExposeStableIDWithoutEnablingTelemetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	type serverResult struct {
		id      string
		metrics telemetry.AssociationMetrics
		err     error
	}
	serverDone := make(chan serverResult, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "METRIC_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- serverResult{err: err}
			return
		}
		defer func() { _ = assoc.Close() }()
		if _, err := assoc.Receive(ctx); err != nil {
			serverDone <- serverResult{err: err}
			return
		}
		if err := assoc.Send(ctx, &ReleaseRP{}); err != nil {
			serverDone <- serverResult{err: err}
			return
		}
		serverDone <- serverResult{id: assoc.ID(), metrics: assoc.TelemetrySnapshot()}
	}()

	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "METRIC_SCP",
		CallingAETitle: "METRIC_SCU",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if client.ID() == "" {
		t.Fatal("client ID() is empty without a telemetry sink")
	}
	if err := client.Send(ctx, &ReleaseRQ{}); err != nil {
		t.Fatalf("Send(ReleaseRQ) error = %v", err)
	}
	result := <-serverDone
	if result.err != nil {
		t.Fatalf("server error = %v", result.err)
	}
	if result.id == "" {
		t.Fatal("server ID() is empty without a telemetry sink")
	}
	if result.metrics.PDUsInbound != 0 || result.metrics.PDUsOutbound != 0 || result.metrics.BytesInbound != 0 || result.metrics.BytesOutbound != 0 {
		t.Fatalf("telemetry-off wire metrics = %+v, want zero", result.metrics)
	}
	if result.metrics.Established != 1 || result.metrics.Released != 1 {
		t.Fatalf("always-on lifecycle metrics = %+v", result.metrics)
	}
}

func TestAssociationReleasedMetricRequiresCompletedHandshake(t *testing.T) {
	state, err := newOperationalState(nil, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState() error = %v", err)
	}
	assoc := &Association{operational: state}
	assoc.observeLifecyclePDU(telemetry.Inbound, &ReleaseRQ{})
	if got := assoc.TelemetrySnapshot().Released; got != 0 {
		t.Fatalf("Released after request only = %d, want 0", got)
	}
	assoc.observeLifecyclePDU(telemetry.Outbound, &ReleaseRP{})
	if got := assoc.TelemetrySnapshot().Released; got != 1 {
		t.Fatalf("Released after completed handshake = %d, want 1", got)
	}

	collisionState, err := newOperationalState(nil, telemetry.RoleSCU)
	if err != nil {
		t.Fatalf("newOperationalState(collision) error = %v", err)
	}
	collision := &Association{operational: collisionState}
	collision.observeLifecyclePDU(telemetry.Outbound, &ReleaseRQ{})
	collision.observeLifecyclePDU(telemetry.Inbound, &ReleaseRQ{})
	collision.observeLifecyclePDU(telemetry.Outbound, &ReleaseRP{})
	if got := collision.TelemetrySnapshot().Released; got != 0 {
		t.Fatalf("Released before collision peer response = %d, want 0", got)
	}
	collision.observeLifecyclePDU(telemetry.Inbound, &ReleaseRP{})
	if got := collision.TelemetrySnapshot().Released; got != 1 {
		t.Fatalf("Released after collision completed = %d, want 1", got)
	}
}

func TestTruncatedNegotiationEmitsMetadataBeforeAcceptReturns(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	events := make(chan telemetry.Event, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	acceptDone := make(chan error, 1)
	go func() {
		_, err := Accept(server, AcceptOptions{
			Context: ctx,
			Observability: &ObservabilityOptions{
				associationIDGenerator: func() string { return "truncated-assoc" },
				Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
					events <- event
				}),
			},
		})
		acceptDone <- err
	}()

	raw := make([]byte, int(PDUHeaderSize)+3)
	raw[0] = byte(PDUAssociateRQ)
	binary.BigEndian.PutUint32(raw[2:6], 100)
	if _, err := client.Write(raw); err != nil {
		t.Fatalf("client.Write() error = %v", err)
	}
	_ = client.Close()
	if err := <-acceptDone; err == nil {
		t.Fatal("Accept() succeeded for truncated A-ASSOCIATE-RQ")
	}

	select {
	case event := <-events:
		if event.AssociationID != "truncated-assoc" || event.PDUType != byte(PDUAssociateRQ) || event.Direction != telemetry.Inbound {
			t.Fatalf("truncated event correlation = %+v", event)
		}
		if !event.Truncated || event.Malformed || event.ErrorClass != "truncated_pdu" || event.DeclaredLength != 100 || event.ActualLength != int64(len(raw)) {
			t.Fatalf("truncated event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for truncated negotiation event")
	}
}

func TestEmptyEOFIsConnectionCloseNotMalformedOrTruncated(t *testing.T) {
	events := make(chan telemetry.Event, 2)
	state, err := newOperationalState(&ObservabilityOptions{Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
		events <- event
	})}, telemetry.RoleSCP)
	if err != nil {
		t.Fatalf("newOperationalState() error = %v", err)
	}
	assoc := &Association{
		Conn:        &deadlineTrackingConn{readErr: io.EOF},
		MaxPDU:      DefaultMaxPDU,
		Context:     context.Background(),
		operational: state,
	}
	if _, err := assoc.Receive(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Receive() error = %v, want io.EOF", err)
	}
	select {
	case event := <-events:
		if event.ErrorClass != "connection_closed" || event.Malformed || event.Truncated {
			t.Fatalf("empty EOF event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connection close event")
	}
	metrics := assoc.TelemetrySnapshot()
	if metrics.MalformedPDUs != 0 || metrics.TruncatedPDUs != 0 {
		t.Fatalf("empty EOF metrics = %+v", metrics)
	}
}

func TestMalformedAndOversizedPDUEmitMetadataBeforeClose(t *testing.T) {
	tests := []struct {
		name          string
		wire          []byte
		maxPDU        uint32
		wantError     error
		wantClass     string
		wantMalformed bool
		wantDeclared  uint32
	}{
		{
			name:          "malformed release length",
			wire:          []byte{byte(PDUReleaseRQ), 0, 0, 0, 0, 5, 0, 0, 0, 0, 0},
			maxPDU:        DefaultMaxPDU,
			wantError:     ErrInvalidPDUField,
			wantClass:     "malformed_pdu",
			wantMalformed: true,
			wantDeclared:  5,
		},
		{
			name:          "oversized P-DATA",
			wire:          []byte{byte(PDUDataTF), 0, 0, 0, 0, 17},
			maxPDU:        16,
			wantError:     ErrPDUTooLarge,
			wantClass:     "oversized_pdu",
			wantMalformed: true,
			wantDeclared:  17,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan telemetry.Event, 2)
			state, err := newOperationalState(&ObservabilityOptions{
				Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
					events <- event
				}),
			}, telemetry.RoleSCP)
			if err != nil {
				t.Fatalf("newOperationalState() error = %v", err)
			}
			defer state.close()
			assoc := &Association{
				Conn:        &deadlineTrackingConn{readData: bytes.NewReader(tt.wire)},
				MaxPDU:      tt.maxPDU,
				Context:     context.Background(),
				operational: state,
			}

			if _, err := assoc.Receive(context.Background()); !errors.Is(err, tt.wantError) {
				t.Fatalf("Receive() error = %v, want %v", err, tt.wantError)
			}
			select {
			case event := <-events:
				if event.ErrorClass != tt.wantClass || event.Malformed != tt.wantMalformed || event.Truncated {
					t.Fatalf("PDU error event = %+v", event)
				}
				if event.PDUType != tt.wire[0] || event.DeclaredLength != tt.wantDeclared || event.ActualLength != int64(len(tt.wire)) {
					t.Fatalf("PDU length event = %+v", event)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for PDU error event")
			}
		})
	}
}

func TestEndpointMetadataRequiresExplicitRedactionPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "HASH_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err == nil {
			err = assoc.Close()
		}
		serverDone <- err
	}()

	events := make(chan telemetry.Event, 8)
	client, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "HASH_SCP",
		CallingAETitle: "HASH_SCU",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
		Observability: &ObservabilityOptions{
			EndpointPolicy: telemetry.EndpointPolicy{
				AETitles:  telemetry.EndpointHMACSHA256,
				Addresses: telemetry.EndpointHMACSHA256,
				HMACKey:   []byte("test-only-key"),
			},
			Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) { events <- event }),
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.PDUType != byte(PDUAssociateRQ) {
				continue
			}
			for name, value := range map[string]string{
				"local AE": event.LocalAETitle, "remote AE": event.RemoteAETitle,
				"local address": event.LocalAddress, "remote address": event.RemoteAddress,
			} {
				if value == "" || !strings.HasPrefix(value, "hmac-sha256:") {
					t.Fatalf("%s = %q, want HMAC redaction", name, value)
				}
			}
			if strings.Contains(fmt.Sprint(event), "HASH_SCU") || strings.Contains(fmt.Sprint(event), "HASH_SCP") || strings.Contains(fmt.Sprint(event), "127.0.0.1") {
				t.Fatalf("redacted event leaked endpoint metadata: %+v", event)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for redacted negotiation event")
		}
	}
}

func BenchmarkAssociationPDUTracing(b *testing.B) {
	pdu := &PDataTF{Values: []PDataValue{{
		PresentationContextID: 1,
		IsLast:                true,
		Data:                  make([]byte, 1024),
	}}}
	tests := []struct {
		name    string
		options *ObservabilityOptions
	}{
		{name: "off"},
		{name: "header", options: &ObservabilityOptions{
			Sink:            telemetry.SinkFunc(func(context.Context, telemetry.Event) {}),
			EventQueueDepth: 1024,
		}},
		{name: "payload", options: &ObservabilityOptions{
			RawPDUSink:                  RawPDUSinkFunc(func(context.Context, RawPDUCapture) {}),
			MaxCapturedPDUBytes:         2048,
			MaxCapturedAssociationBytes: math.MaxInt64,
			RawPDUQueueDepth:            1024,
		}},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			state, err := newOperationalState(test.options, telemetry.RoleSCU)
			if err != nil {
				b.Fatalf("newOperationalState() error = %v", err)
			}
			b.Cleanup(state.close)
			assoc := &Association{Conn: benchmarkConn{}, Context: context.Background(), operational: state}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := assoc.Send(context.Background(), pdu); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
