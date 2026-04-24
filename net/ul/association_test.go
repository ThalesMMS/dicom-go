package ul

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

const verificationSOPClassUID = "1.2.840.10008.1.1"

func closeErr(name string, c io.Closer) error {
	if c == nil {
		return nil
	}
	if err := c.Close(); err != nil {
		return fmt.Errorf("%s Close() failed: %w", name, err)
	}
	return nil
}

func cleanupClose(t *testing.T, name string, c io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := closeErr(name, c); err != nil {
			t.Errorf("%s", err)
		}
	})
}

func assertUserInfo(t *testing.T, items []UserVariableItem, wantMaxPDU uint32, wantImplementationClassUID, wantImplementationVersionName string) {
	t.Helper()

	var foundMaxPDU, foundImplementationClassUID, foundImplementationVersionName bool
	for _, item := range items {
		switch item := item.(type) {
		case MaxLengthItem:
			foundMaxPDU = true
			if item.Value != wantMaxPDU {
				t.Fatalf("MaxLengthItem.Value = %d, want %d", item.Value, wantMaxPDU)
			}
		case ImplementationClassUIDItem:
			foundImplementationClassUID = true
			if item.UID != wantImplementationClassUID {
				t.Fatalf("ImplementationClassUIDItem.UID = %q, want %q", item.UID, wantImplementationClassUID)
			}
		case ImplementationVersionNameItem:
			foundImplementationVersionName = true
			if item.Name != wantImplementationVersionName {
				t.Fatalf("ImplementationVersionNameItem.Name = %q, want %q", item.Name, wantImplementationVersionName)
			}
		}
	}

	if !foundMaxPDU {
		t.Fatalf("UserInfo missing MaxLengthItem")
	}
	if !foundImplementationClassUID {
		t.Fatalf("UserInfo missing ImplementationClassUIDItem")
	}
	if !foundImplementationVersionName {
		t.Fatalf("UserInfo missing ImplementationVersionNameItem")
	}
}

func TestAssociationNegotiationAndRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	acceptOpts := AcceptOptions{
		AETitle:                   "SCP_AE",
		RequireMatchingCalledAE:   true,
		MaxPDU:                    8192,
		Context:                   ctx,
		SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
		SupportedTransferSyntaxes: []string{ExplicitVRLittleEndian},
	}

	if listener.Addr() == nil {
		t.Fatal("listener Addr() = nil")
	}

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		assoc, err := listener.AcceptAssociation(acceptOpts)
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()

		if got, want := assoc.CalledAETitle, "SCP_AE"; got != want {
			result = errors.New("server called AE title mismatch")
			return
		}
		if got, want := assoc.CallingAETitle, "SCU_AE"; got != want {
			result = errors.New("server calling AE title mismatch")
			return
		}
		if got, want := assoc.PeerMaxPDU, uint32(4096); got != want {
			result = errors.New("server peer max PDU mismatch")
			return
		}
		if len(assoc.AcceptedContexts) != 1 || assoc.AcceptedContexts[0].TransferSyntaxUID != ExplicitVRLittleEndian {
			result = errors.New("server accepted context mismatch")
			return
		}

		pdu, err := assoc.ReadPDU()
		if err != nil {
			result = err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			result = errors.New("server expected A-RELEASE-RQ")
			return
		}
		result = assoc.WritePDU(&ReleaseRP{})
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "SCP_AE",
		CallingAETitle: "SCU_AE",
		MaxPDU:         4096,
		Contexts: []PresentationContext{
			{
				AbstractSyntaxUID:  verificationSOPClassUID,
				TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian},
			},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	if got, want := assoc.PeerMaxPDU, uint32(8192); got != want {
		t.Fatalf("client PeerMaxPDU = %d, want %d", got, want)
	}
	if len(assoc.AcceptedContexts) != 1 {
		t.Fatalf("len(client AcceptedContexts) = %d, want 1", len(assoc.AcceptedContexts))
	}
	if got, want := assoc.AcceptedContexts[0], (AcceptedContext{ID: 1, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUID: ExplicitVRLittleEndian}); got != want {
		t.Fatalf("client accepted context = %#v, want %#v", got, want)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestBuildAssociateRQDefaultsAndIDs(t *testing.T) {
	rq := buildAssociateRQ(DialOptions{
		CalledAETitle:  "SCP_AE",
		CallingAETitle: "SCU_AE",
		MaxPDU:         4096,
		Contexts: []PresentationContext{
			{ID: 9, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
			{AbstractSyntaxUID: "1.2.3", TransferSyntaxUIDs: []string{ExplicitVRLittleEndian}},
		},
	})
	if rq.ProtocolVersion != DefaultProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", rq.ProtocolVersion, DefaultProtocolVersion)
	}
	if rq.ApplicationContextName != ApplicationContextName {
		t.Fatalf("ApplicationContextName = %q, want %q", rq.ApplicationContextName, ApplicationContextName)
	}
	if got := []byte{rq.PresentationContexts[0].ID, rq.PresentationContexts[1].ID}; got[0] != 1 || got[1] != 3 {
		t.Fatalf("presentation context IDs = %v, want [1 3]", got)
	}

	if len(rq.UserInfo) != 3 {
		t.Fatalf("len(UserInfo) = %d, want 3", len(rq.UserInfo))
	}
	assertUserInfo(t, rq.UserInfo, 4096, ImplementationClassUID, ImplementationVersionName)
}

func TestBuildAssociateRQImplementationOverrides(t *testing.T) {
	rq := buildAssociateRQ(DialOptions{
		CalledAETitle:             "SCP_AE",
		CallingAETitle:            "SCU_AE",
		ImplementationClassUID:    "1.2.826.0.1",
		ImplementationVersionName: "TESTAPP",
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	assertUserInfo(t, rq.UserInfo, DefaultMaxPDU, "1.2.826.0.1", "TESTAPP")
}

func TestBuildAssociateRQValidation(t *testing.T) {
	err := validateDialOptions(DialOptions{CalledAETitle: "TOO-LONG-CALLED-AE", Contexts: []PresentationContext{
		{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
	}})
	if !errors.Is(err, ErrInvalidAEtitle) {
		t.Fatalf("validateDialOptions(long AE) error = %v, want ErrInvalidAEtitle", err)
	}

	err = validateDialOptions(DialOptions{})
	if !errors.Is(err, ErrMissingPDUField) {
		t.Fatalf("validateDialOptions(no contexts) error = %v, want ErrMissingPDUField", err)
	}
}

func TestDialValidatesOptionsBeforeConnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenConfig.Listen() error = %v", err)
	}
	cleanupClose(t, "listener", ln)

	_, err = Dial(ln.Addr().String(), DialOptions{Context: ctx})
	if !errors.Is(err, ErrMissingPDUField) {
		t.Fatalf("Dial() error = %v, want ErrMissingPDUField", err)
	}

	if tcp, ok := ln.(*net.TCPListener); ok {
		if err := tcp.SetDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
			t.Fatalf("SetDeadline() error = %v", err)
		}
		defer func() { _ = tcp.SetDeadline(time.Time{}) }()
	}
	conn, err := ln.Accept()
	if err == nil {
		if cerr := closeErr("unexpected accepted conn", conn); cerr != nil {
			t.Errorf("%s", cerr)
		}
		t.Fatal("listener accepted a connection for invalid DialOptions")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Accept() error = %v, want timeout proving no connection was opened", err)
	}
}

func TestAssociationRejectReturnsStructuredReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			AETitle:                   "EXPECTED_SCP",
			RequireMatchingCalledAE:   true,
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle:  "WRONG_SCP",
		CallingAETitle: "SCU_AE",
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if !errors.Is(err, ErrAssociationRejected) {
		t.Fatalf("DialContext() error does not wrap ErrAssociationRejected: %v", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: AssociateRJReasonCalledAETitleNotRecognized}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}

	err = <-serverErr
	if !errors.As(err, &reject) {
		t.Fatalf("AcceptAssociation() error = %v, want RejectionError", err)
	}
}

func TestAssociationRejectsProtocolVersionMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		ProtocolVersion: 2,
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceProviderACSE, Reason: AssociateRJReasonProtocolVersionNotSupported}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}

	err = <-serverErr
	if !errors.As(err, &reject) {
		t.Fatalf("AcceptAssociation() error = %v, want RejectionError", err)
	}
}

func TestAssociationRejectsUnsupportedAbstractSyntax(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{"1.2.3.4"},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if got, want := reject.PDU(), (AssociationRJ{Result: AssociateRJResultPermanent, Source: AssociateRJSourceServiceUser, Reason: AssociateRJReasonNoReasonGiven}); got != want {
		t.Fatalf("rejection PDU = %#v, want %#v", got, want)
	}
	if err := <-serverErr; !errors.Is(err, ErrNoAcceptedPresentationContexts) {
		t.Fatalf("AcceptAssociation() error = %v, want ErrNoAcceptedPresentationContexts", err)
	}
}

func TestAcceptRejectsUnexpectedPDU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, server := net.Pipe()
	cleanupClose(t, "client pipe", client)

	writeErr := make(chan error, 1)
	go func() {
		if err := WritePDU(client, &ReleaseRQ{}); err != nil {
			writeErr <- err
			return
		}
		pdu, err := ReadPDU(client, DefaultMaxPDU)
		if err != nil {
			writeErr <- err
			return
		}
		if _, ok := pdu.(*ReleaseRP); !ok {
			writeErr <- fmt.Errorf("unexpected response PDU = %T, want *ReleaseRP", pdu)
			return
		}
		writeErr <- nil
	}()

	_, err := Accept(server, AcceptOptions{Context: ctx})
	if !errors.Is(err, ErrUnexpectedPDU) {
		t.Fatalf("Accept() error = %v, want ErrUnexpectedPDU", err)
	}
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("client WritePDU() error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("client goroutine did not finish after unexpected PDU response")
	}
}

func TestAssociationAbort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverErr <- result }()

		assoc, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server association", assoc); result == nil && err != nil {
				result = err
			}
		}()
		_, result = assoc.Receive(ctx)
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if err := assoc.Abort(AbortReasonNotSpecified); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}

	err = <-serverErr
	var abortErr *AbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("server Receive() error = %v, want AbortError", err)
	}
	if abortErr.Source != AbortSourceServiceUser || abortErr.Reason != AbortReasonNotSpecified {
		t.Fatalf("abort = source %d reason %d, want %d/%d", abortErr.Source, abortErr.Reason, AbortSourceServiceUser, AbortReasonNotSpecified)
	}
}

func TestReleaseAbortsAndClosesOnUnexpectedPDU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, server := net.Pipe()
	cleanupClose(t, "server pipe", server)

	serverErr := make(chan error, 1)
	go func() {
		pdu, err := ReadPDU(server, DefaultMaxPDU)
		if err != nil {
			serverErr <- err
			return
		}
		if _, ok := pdu.(*ReleaseRQ); !ok {
			serverErr <- fmt.Errorf("server got %T, want *ReleaseRQ", pdu)
			return
		}
		err = WritePDU(server, &PDataTF{Values: []PDataValue{{
			PresentationContextID: 1,
			IsCommand:             true,
			IsLast:                true,
			Data:                  []byte{1},
		}}})
		if err != nil {
			serverErr <- err
			return
		}
		pdu, err = ReadPDU(server, DefaultMaxPDU)
		if err != nil {
			serverErr <- err
			return
		}
		abort, ok := pdu.(*AbortRQ)
		if !ok {
			serverErr <- fmt.Errorf("server got %T, want *AbortRQ", pdu)
			return
		}
		if abort.Source != AbortSourceServiceUser || abort.Reason != AbortReasonUnexpectedPDU {
			serverErr <- fmt.Errorf("abort = source %d reason %d, want %d/%d", abort.Source, abort.Reason, AbortSourceServiceUser, AbortReasonUnexpectedPDU)
			return
		}
		serverErr <- nil
	}()

	assoc := &Association{Conn: client, MaxPDU: DefaultMaxPDU, Context: ctx}
	err := assoc.Release(ctx)
	if !errors.Is(err, ErrUnexpectedPDU) {
		t.Fatalf("Release() error = %v, want ErrUnexpectedPDU", err)
	}
	if assoc.Conn != nil {
		t.Fatal("Release() left association connection open after unexpected PDU")
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestDialContextAssociationTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenConfig.Listen() error = %v", err)
	}
	cleanupClose(t, "listener", ln)

	serverDone := make(chan error, 1)
	go func() {
		var result error
		defer func() { serverDone <- result }()

		conn, err := ln.Accept()
		if err != nil {
			result = err
			return
		}
		defer func() {
			if err := closeErr("server conn", conn); result == nil && err != nil {
				result = err
			}
		}()
		_, result = io.Copy(io.Discard, conn)
	}()

	_, err = DialContext(ctx, ln.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("DialContext() error = %v, want ErrAssociationTimeout", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("server goroutine did not finish after Dial timeout")
	}
}

func TestAcceptAssociationTimeout(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, err = listener.AcceptAssociation(AcceptOptions{Context: ctx})
	if !errors.Is(err, ErrAssociationTimeout) {
		t.Fatalf("AcceptAssociation() error = %v, want ErrAssociationTimeout", err)
	}
}
