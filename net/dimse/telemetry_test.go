package dimse

import (
	"context"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestDIMSETelemetryUsesAssociationCorrelationAndSafeCommandSummary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	events := make(chan telemetry.Event, 32)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "TELEMETRY_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{VerificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
			Observability: &ul.ObservabilityOptions{Sink: telemetry.SinkFunc(func(_ context.Context, event telemetry.Event) {
				events <- event
			})},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		serverDone <- ServeAssociation(ctx, assoc, AssociationSCPOptions{})
	}()

	client, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "TELEMETRY_SCP",
		CallingAETitle: "TELEMETRY_SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := SendCEchoRequest(client, 1, 17); err != nil {
		t.Fatalf("SendCEchoRequest() error = %v", err)
	}
	status, err := ReceiveCEchoResponse(client, 1, 17)
	if err != nil || status != StatusSuccess {
		t.Fatalf("ReceiveCEchoResponse() = 0x%04X, %v", status, err)
	}
	if err := client.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	var associationID string
	seenRequest := false
	seenResponse := false
	seenClosed := false
	seenOperation := false
	deadline := time.After(time.Second)
	for !seenRequest || !seenResponse || !seenClosed || !seenOperation {
		select {
		case event := <-events:
			if event.AssociationID == "" {
				t.Fatalf("event without association ID: %+v", event)
			}
			if associationID == "" {
				associationID = event.AssociationID
			} else if event.AssociationID != associationID {
				t.Fatalf("event association ID = %q, want %q", event.AssociationID, associationID)
			}
			if event.Kind == telemetry.CommandObserved && event.CommandField == CEchoRQ {
				seenRequest = true
				if event.Direction != telemetry.Inbound || event.StatusSet || event.DataSetPresent {
					t.Fatalf("C-ECHO-RQ event = %+v", event)
				}
			}
			if event.Kind == telemetry.CommandObserved && event.CommandField == CEchoRSP {
				seenResponse = true
				if event.Direction != telemetry.Outbound || !event.StatusSet || event.Status != StatusSuccess || event.StatusCategory != telemetry.StatusCategorySuccess {
					t.Fatalf("C-ECHO-RSP event = %+v", event)
				}
				if event.AbstractSyntaxUID != VerificationSOPClassUID || event.TransferSyntaxUID != ul.ImplicitVRLittleEndian || event.Duration <= 0 {
					t.Fatalf("C-ECHO-RSP syntax/duration = %+v", event)
				}
			}
			seenClosed = seenClosed || event.Kind == telemetry.AssociationClosed
			if event.Kind == telemetry.OperationCompleted && event.CommandField == CEchoRQ {
				seenOperation = true
				if event.Duration <= 0 || event.ErrorClass != "" || event.AbstractSyntaxUID != VerificationSOPClassUID {
					t.Fatalf("C-ECHO operation event = %+v", event)
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for telemetry request=%t response=%t operation=%t closed=%t", seenRequest, seenResponse, seenOperation, seenClosed)
		}
	}
}

func TestCommandStatusCategory(t *testing.T) {
	tests := map[uint16]string{
		0x0000: telemetry.StatusCategorySuccess,
		0xFF00: telemetry.StatusCategoryPending,
		0xFF01: telemetry.StatusCategoryPending,
		0xFE00: telemetry.StatusCategoryCanceled,
		0x0001: telemetry.StatusCategoryWarning,
		0x0107: telemetry.StatusCategoryWarning,
		0x0116: telemetry.StatusCategoryWarning,
		0xB007: telemetry.StatusCategoryWarning,
		0xA700: telemetry.StatusCategoryFailure,
	}
	for status, want := range tests {
		if got := commandStatusCategory(status); got != want {
			t.Errorf("commandStatusCategory(0x%04X) = %q, want %q", status, got, want)
		}
	}
}
