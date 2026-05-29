package dimse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestVerifyCEchoWithLocalSCP(t *testing.T) {
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
		assoc, pc, err := acceptVerificationAssociation(ctx, listener)
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

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
		serverDone <- respondToRelease(assoc)
	}()

	startedBefore := time.Now().UTC()
	result, err := VerifyCEcho(ctx, CEchoVerificationOptions{
		Address:        listener.Addr().String(),
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		MessageID:      77,
	})
	if err != nil {
		t.Fatalf("VerifyCEcho() error = %v", err)
	}
	if result.Status != StatusSuccess {
		t.Fatalf("status = 0x%04X, want 0x%04X", result.Status, StatusSuccess)
	}
	if result.MessageID != 77 {
		t.Fatalf("MessageID = %d, want 77", result.MessageID)
	}
	if result.PresentationContext.AbstractSyntaxUID != VerificationSOPClassUID {
		t.Fatalf("presentation context abstract syntax = %q", result.PresentationContext.AbstractSyntaxUID)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration = %s, want positive duration", result.Duration)
	}
	if result.StartedAt.Before(startedBefore) {
		t.Fatalf("StartedAt = %s, want after %s", result.StartedAt, startedBefore)
	}
	if got := <-requestID; got != result.MessageID {
		t.Fatalf("request MessageID = %d, result MessageID = %d", got, result.MessageID)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestPositiveElapsed(t *testing.T) {
	started := time.Unix(100, 0)
	tests := []struct {
		name     string
		finished time.Time
		want     time.Duration
	}{
		{name: "positive", finished: started.Add(25 * time.Millisecond), want: 25 * time.Millisecond},
		{name: "below clock resolution", finished: started, want: time.Nanosecond},
		{name: "clock regression", finished: started.Add(-time.Second), want: time.Nanosecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := positiveElapsed(started, tt.finished); got != tt.want {
				t.Fatalf("positiveElapsed() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestVerifyCEchoDialTimeoutDoesNotBoundDIMSEExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	dialTimeout := 150 * time.Millisecond
	respondAfter := time.Now().Add(dialTimeout + 75*time.Millisecond)

	serverDone := make(chan error, 1)
	go func() {
		assoc, pc, err := acceptVerificationAssociation(ctx, listener)
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		messageID, err := ReceiveCEcho(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if wait := time.Until(respondAfter); wait > 0 {
			time.Sleep(wait)
		}
		if err := SendCEchoResponse(assoc, pc.ID, messageID, StatusSuccess); err != nil {
			serverDone <- err
			return
		}
		serverDone <- respondToRelease(assoc)
	}()

	result, err := VerifyCEcho(ctx, CEchoVerificationOptions{
		Address:        listener.Addr().String(),
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		DialTimeout:    dialTimeout,
		MessageID:      78,
	})
	serverErr := <-serverDone
	if err != nil {
		t.Fatalf("VerifyCEcho() error = %v", err)
	}
	if serverErr != nil {
		t.Fatalf("server error = %v", serverErr)
	}
	if result.Status != StatusSuccess {
		t.Fatalf("status = 0x%04X, want 0x%04X", result.Status, StatusSuccess)
	}
	if result.MessageID != 78 {
		t.Fatalf("MessageID = %d, want 78", result.MessageID)
	}
}

func TestVerifyCEchoWrapsNonSuccessStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, pc, err := acceptVerificationAssociation(ctx, listener)
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		messageID, err := ReceiveCEcho(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if err := SendCEchoResponse(assoc, pc.ID, messageID, 0x0122); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	_, err = VerifyCEcho(ctx, CEchoVerificationOptions{
		Address:        listener.Addr().String(),
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
	})
	if !errors.Is(err, ErrCEchoRequest) {
		t.Fatalf("VerifyCEcho() error = %v, want ErrCEchoRequest", err)
	}
	if !strings.Contains(err.Error(), "status 0x0122") {
		t.Fatalf("VerifyCEcho() error = %q, want status context", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestVerifyCEchoWrapsAssociationRejectionForRejectedPresentationContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "ECHOSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{VerificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ExplicitVRLittleEndian},
		})
		serverDone <- err
	}()

	_, err = VerifyCEcho(ctx, CEchoVerificationOptions{
		Address:        listener.Addr().String(),
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
	})
	if !errors.Is(err, ErrCEchoAssociation) {
		t.Fatalf("VerifyCEcho() error = %v, want ErrCEchoAssociation", err)
	}
	if !strings.Contains(err.Error(), "association rejected") {
		t.Fatalf("VerifyCEcho() error = %q, want association rejected context", err)
	}
	if serverErr := <-serverDone; !errors.Is(serverErr, ul.ErrNoAcceptedPresentationContexts) {
		t.Fatalf("server error = %v, want ErrNoAcceptedPresentationContexts", serverErr)
	}
}

func TestVerifyCEchoWrapsReleaseFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, pc, err := acceptVerificationAssociation(ctx, listener)
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		messageID, err := ReceiveCEcho(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if err := SendCEchoResponse(assoc, pc.ID, messageID, StatusSuccess); err != nil {
			serverDone <- err
			return
		}
		serverDone <- assoc.Close()
	}()

	_, err = VerifyCEcho(ctx, CEchoVerificationOptions{
		Address:        listener.Addr().String(),
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		ReleaseTimeout: time.Second,
	})
	if !errors.Is(err, ErrCEchoRelease) {
		t.Fatalf("VerifyCEcho() error = %v, want ErrCEchoRelease", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func acceptVerificationAssociation(ctx context.Context, listener *ul.Listener) (*ul.Association, ul.AcceptedContext, error) {
	assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
		AETitle:                   "ECHOSCP",
		Context:                   ctx,
		SupportedAbstractSyntaxes: []string{VerificationSOPClassUID},
		SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
	})
	if err != nil {
		return nil, ul.AcceptedContext{}, err
	}
	pc, ok := AcceptedVerificationContext(assoc)
	if !ok {
		_ = assoc.Close()
		return nil, ul.AcceptedContext{}, errors.New("no accepted Verification SOP Class presentation context")
	}
	return assoc, pc, nil
}

func respondToRelease(assoc *ul.Association) error {
	pdu, err := assoc.ReadPDU()
	if err != nil {
		return err
	}
	if _, ok := pdu.(*ul.ReleaseRQ); !ok {
		return errors.New("server expected A-RELEASE-RQ")
	}
	return assoc.WritePDU(&ul.ReleaseRP{})
}
