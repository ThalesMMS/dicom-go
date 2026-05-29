package ul

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
)

func TestAssociationServerBackpressureBoundsSilentNegotiations(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:            "BOUNDED_SCP",
			NegotiationTimeout: time.Second,
		},
		MaxConcurrentAssociations: 2,
		SaturationPolicy:          SaturationBackpressure,
		Handler:                   func(context.Context, *Association) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()

	clients := make([]net.Conn, 0, 12)
	for i := 0; i < 12; i++ {
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("net.DialTimeout(%d) error = %v", i, err)
		}
		clients = append(clients, conn)
	}
	t.Cleanup(func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
	})

	deadline := time.Now().Add(time.Second)
	for server.Snapshot().Negotiating < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	metrics := server.Snapshot()
	if metrics.Negotiating != 2 || metrics.Active != 0 {
		t.Fatalf("server metrics under silent flood = %+v", metrics)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want context deadline", err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after Shutdown")
	}
}

func TestAssociationServerRejectsSaturationWithProviderLocalLimit(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:            "LIMIT_SCP",
			NegotiationTimeout: time.Second,
		},
		MaxConcurrentAssociations: 1,
		SaturationPolicy:          SaturationReject,
		Handler:                   func(context.Context, *Association) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()

	silent, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("silent net.Dial() error = %v", err)
	}
	defer func() { _ = silent.Close() }()
	deadline := time.Now().Add(time.Second)
	for server.Snapshot().Negotiating != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle: "LIMIT_SCP",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	if got := rejection.PDU(); got != (AssociationRJ{
		Result: AssociateRJResultTransient,
		Source: AssociateRJSourceServiceProviderPresentation,
		Reason: AssociateRJReasonLocalLimitExceeded,
	}) {
		t.Fatalf("saturation rejection = %+v", got)
	}
	if got := server.Snapshot().Rejected; got != 1 {
		t.Fatalf("Rejected = %d, want 1", got)
	}

	_ = silent.Close()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestAssociationServerShutdownAbortsAfterDeadline(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	handlerStarted := make(chan struct{})
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:                   "SHUTDOWN_SCP",
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		},
		MaxConcurrentAssociations: 1,
		Handler: func(ctx context.Context, _ *Association) error {
			close(handlerStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	client, err := Dial(listener.Addr().String(), DialOptions{
		CalledAETitle: "SHUTDOWN_SCP",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	<-handlerStarted
	abortReceived := make(chan error, 1)
	go func() {
		_, err := client.Receive(context.Background())
		abortReceived <- err
	}()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want context deadline", err)
	}
	select {
	case err := <-abortReceived:
		var abortErr *AbortError
		if !errors.As(err, &abortErr) {
			t.Fatalf("client Receive() error = %v, want AbortError", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not receive controlled A-ABORT")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop")
	}
}

func TestAssociationServerRecoversHandlerPanicAndAcceptsNextAssociation(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	var calls atomic.Int32
	secondHandled := make(chan struct{})
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:                   "PANIC_SCP",
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		},
		MaxConcurrentAssociations: 2,
		Handler: func(context.Context, *Association) error {
			if calls.Add(1) == 1 {
				panic("handler failure")
			}
			close(secondHandled)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	dial := func() *Association {
		assoc, err := Dial(listener.Addr().String(), DialOptions{
			CalledAETitle: "PANIC_SCP",
			Contexts: []PresentationContext{{
				AbstractSyntaxUID:  verificationSOPClassUID,
				TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
			}},
		})
		if err != nil {
			t.Fatalf("Dial() error = %v", err)
		}
		return assoc
	}
	first := dial()
	defer func() { _ = first.Close() }()
	deadline := time.Now().Add(time.Second)
	for server.Snapshot().HandlerPanics != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	second := dial()
	defer func() { _ = second.Close() }()
	select {
	case <-secondHandled:
	case <-time.After(time.Second):
		t.Fatal("server did not run handler after panic")
	}
	if got := server.Snapshot().HandlerPanics; got != 1 {
		t.Fatalf("HandlerPanics = %d, want 1", got)
	}
	if got := server.Snapshot().Aborted; got != 1 {
		t.Fatalf("Aborted after handler panic = %d, want 1", got)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestAssociationServerRecoversNegotiationCallbackPanicAndAcceptsNextAssociation(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	handled := make(chan struct{}, 1)
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:                   "PANIC_SCP",
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
			UserIdentityHandler: func(UserIdentityRequest) (UserIdentityResult, error) {
				panic("credential callback value must not escape")
			},
		},
		MaxConcurrentAssociations: 2,
		Handler: func(context.Context, *Association) error {
			handled <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle: "PANIC_SCP",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
		UserIdentity: func() *UserIdentityRequest {
			identity := NewUsernameIdentity("panic-user", false)
			return &identity
		}(),
	})
	if err == nil {
		t.Fatal("DialContext() succeeded after negotiation callback panic")
	}
	deadline := time.Now().Add(time.Second)
	for server.Snapshot().NegotiationPanics != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if metrics := server.Snapshot(); metrics.NegotiationPanics != 1 || metrics.Negotiating != 0 {
		t.Fatalf("metrics after negotiation panic = %+v", metrics)
	}
	second, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		CalledAETitle: "PANIC_SCP",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("second DialContext() error = %v", err)
	}
	defer func() { _ = second.Close() }()
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("server did not handle association after negotiation callback panic")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestAssociationServerRejectsInvalidObservabilityAtConstruction(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()
	_, err = NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{Observability: &ObservabilityOptions{
			EndpointPolicy: telemetry.EndpointPolicy{AETitles: telemetry.EndpointHMACSHA256},
		}},
		Handler: func(context.Context, *Association) error { return nil },
	})
	if err == nil {
		t.Fatal("NewAssociationServer() accepted HMAC observability without a key")
	}
}

func TestAssociationServerShutdownBeforeServeReturnsAndPreventsServe(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Handler: func(context.Context, *Association) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() before Serve error = %v", err)
	}
	if err := server.Serve(context.Background()); err == nil {
		t.Fatal("Serve() succeeded after pre-serve Shutdown")
	}
}

func TestAssociationServerPropagatesServeContextValues(t *testing.T) {
	type contextKey struct{}
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	observed := make(chan any, 1)
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:                   "CONTEXT_SCP",
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		},
		Handler: func(ctx context.Context, assoc *Association) error {
			observed <- ctx.Value(contextKey{})
			if assoc.Context.Value(contextKey{}) != "serve-value" {
				return errors.New("association context lost Serve value")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	serveCtx := context.WithValue(context.Background(), contextKey{}, "serve-value")
	go func() { serveDone <- server.Serve(serveCtx) }()
	client, err := Dial(listener.Addr().String(), DialOptions{
		CalledAETitle: "CONTEXT_SCP",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	if got := <-observed; got != "serve-value" {
		t.Fatalf("handler context value = %v", got)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestAssociationServerCountsNegotiationRejections(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Accept: AcceptOptions{
			AETitle:                   "EXPECTED_SCP",
			RequireMatchingCalledAE:   true,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		},
		Handler: func(context.Context, *Association) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	_, err = Dial(listener.Addr().String(), DialOptions{
		CalledAETitle: "WRONG_SCP",
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	var rejection *RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("Dial() error = %v, want RejectionError", err)
	}
	deadline := time.Now().Add(time.Second)
	for server.Snapshot().Rejected != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := server.Snapshot().Rejected; got != 1 {
		t.Fatalf("Rejected = %d, want 1", got)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestAssociationServerReportsUnexpectedExternalListenerClose(t *testing.T) {
	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server, err := NewAssociationServer(listener, AssociationServerOptions{
		Handler: func(context.Context, *Association) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAssociationServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	if err := listener.Close(); err != nil {
		t.Fatalf("Listener.Close() error = %v", err)
	}
	select {
	case err := <-serveDone:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve() error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after external listener close")
	}
}
