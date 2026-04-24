package dimse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestCEchoSCUConversationsWithLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	requestID := make(chan uint16, 1)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "ECHOSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{VerificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, ok := AcceptedVerificationContext(assoc)
		if !ok {
			serverDone <- errors.New("no accepted Verification SOP Class presentation context")
			return
		}
		messageID, err := ReceiveCEcho(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		requestID <- messageID
		if err := SendCEchoResponse(assoc, pc.ID, messageID, StatusSuccess); err != nil {
			serverDone <- err
			return
		}
		pdu, err := assoc.ReadPDU()
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ul.ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	pc, ok := AcceptedVerificationContext(assoc)
	if !ok {
		t.Fatal("AcceptedVerificationContext() = false")
	}
	response, err := SendCEcho(assoc, pc.ID, 42)
	if err != nil {
		t.Fatalf("SendCEcho() error = %v", err)
	}
	if response.Status != StatusSuccess {
		t.Fatalf("status = 0x%04X, want 0x%04X", response.Status, StatusSuccess)
	}
	if response.MessageIDBeingRespondedTo != 42 {
		t.Fatalf("MessageIDBeingRespondedTo = %d, want 42", response.MessageIDBeingRespondedTo)
	}
	if got := <-requestID; got != response.MessageIDBeingRespondedTo {
		t.Fatalf("request MessageID = %d, response MessageIDBeingRespondedTo = %d", got, response.MessageIDBeingRespondedTo)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func closeOrFail(t testing.TB, name string, c interface{ Close() error }) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatalf("failed to close %s: %v", name, err)
	}
}
