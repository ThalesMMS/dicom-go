package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/audit"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.port != 11112 || opts.aeTitle != "ECHOSCP" || opts.single {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsCustomValues(t *testing.T) {
	opts, err := parseArgs([]string{"-port", "104", "-ae-title", "SCP", "-single"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.port != 104 || opts.aeTitle != "SCP" || !opts.single {
		t.Fatalf("parseArgs() = %#v", opts)
	}
}

func TestParseArgsQueueOptions(t *testing.T) {
	opts, err := parseArgs([]string{
		"-max-associations", "4",
		"-max-active-operations", "2",
		"-queue-depth", "8",
		"-enqueue-timeout", "25ms",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.maxAssociations != 4 || opts.maxActiveOperations != 2 || opts.queueDepth != 8 || opts.enqueueTimeout != 25*time.Millisecond {
		t.Fatalf("queue options = %#v", opts)
	}
}

func TestParseArgsUsageError(t *testing.T) {
	_, err := parseArgs([]string{"extra"}, &bytes.Buffer{})
	if !errors.Is(err, errUsage) {
		t.Fatalf("parseArgs(extra) error = %v, want errUsage", err)
	}

	_, err = parseArgs([]string{"-h"}, &bytes.Buffer{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

type recordingAuditSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (s *recordingAuditSink) EmitAuditEvent(_ context.Context, event audit.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *recordingAuditSink) snapshot() []audit.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.Event(nil), s.events...)
}

func TestHandleAssociationEmitsAuditEventsForCEcho(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	sink := &recordingAuditSink{}
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "ECHOSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dimse.VerificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		var stdout bytes.Buffer
		serverDone <- handleAssociation(assoc, &stdout, sink)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dimse.VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		t.Fatal("AcceptedVerificationContext() = false")
	}
	response, err := dimse.SendCEcho(assoc, pc.ID, 7)
	if err != nil {
		t.Fatalf("SendCEcho() error = %v", err)
	}
	if response.Status != dimse.StatusSuccess {
		t.Fatalf("C-ECHO status = 0x%04X, want success", response.Status)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	events := sink.snapshot()
	wantKinds := []audit.EventKind{
		audit.AssociationAccepted,
		audit.CommandReceived,
		audit.ResponseSent,
		audit.OperationSucceeded,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("audit event count = %d, want %d: %#v", len(events), len(wantKinds), events)
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("event[%d].Kind = %q, want %q", i, events[i].Kind, want)
		}
		if events[i].LocalAETitle != "ECHOSCP" || events[i].RemoteAETitle != "ECHOSCU" {
			t.Fatalf("event[%d] AE titles = %q/%q", i, events[i].LocalAETitle, events[i].RemoteAETitle)
		}
		if events[i].RemoteAddr == "" {
			t.Fatalf("event[%d].RemoteAddr is empty", i)
		}
	}
	if events[1].Command != "C-ECHO-RQ" || events[1].SOPClassUID != dimse.VerificationSOPClassUID {
		t.Fatalf("command event = %#v", events[1])
	}
	if !events[2].StatusSet || events[2].Status != dimse.StatusSuccess {
		t.Fatalf("response event status = set %v value 0x%04X", events[2].StatusSet, events[2].Status)
	}
}

func TestHandleAssociationEmitsAuditFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	sink := &recordingAuditSink{}
	assoc := &ul.Association{
		Conn:           server,
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		Context:        ctx,
	}
	var stdout bytes.Buffer
	err := handleAssociation(assoc, &stdout, sink)
	if err == nil {
		t.Fatal("handleAssociation() error = nil, want missing presentation context")
	}

	events := sink.snapshot()
	if len(events) != 2 {
		t.Fatalf("audit event count = %d, want 2: %#v", len(events), events)
	}
	if events[0].Kind != audit.AssociationAccepted {
		t.Fatalf("event[0].Kind = %q, want %q", events[0].Kind, audit.AssociationAccepted)
	}
	if events[1].Kind != audit.OperationFailed || events[1].ErrorClass != "presentation-context" {
		t.Fatalf("failure event = %#v", events[1])
	}
	if events[1].Command != "C-ECHO-RQ" {
		t.Fatalf("failure command = %q, want C-ECHO-RQ", events[1].Command)
	}
}

func TestRejectAssociationForQueueBackpressureAborts(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	assoc := &ul.Association{Conn: server, MaxPDU: ul.DefaultMaxPDU}
	var stderr bytes.Buffer

	done := make(chan struct{})
	go func() {
		defer close(done)
		rejectAssociationForQueueBackpressure(assoc, &stderr, dimse.ErrServiceQueueFull)
	}()

	pdu, err := ul.ReadPDU(client, ul.DefaultMaxPDU)
	if err != nil {
		t.Fatalf("ReadPDU() error = %v", err)
	}
	abort, ok := pdu.(*ul.AbortRQ)
	if !ok {
		t.Fatalf("PDU = %T, want *ul.AbortRQ", pdu)
	}
	if abort.Source != ul.AbortSourceServiceUser || abort.Reason != ul.AbortReasonNotSpecified {
		t.Fatalf("abort = source %d reason %d", abort.Source, abort.Reason)
	}
	<-done
	if got := stderr.String(); !strings.Contains(got, "service queue full") {
		t.Fatalf("stderr = %q, want service queue full diagnostic", got)
	}
}
