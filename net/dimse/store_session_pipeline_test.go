package dimse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestEffectiveStoreInvokedDefaultsToSerial(t *testing.T) {
	window := ul.AsynchronousOperationsWindow{MaximumInvoked: 8, MaximumPerformed: 8}
	if got := effectiveStoreInvoked(false, 8, window); got != 1 {
		t.Fatalf("opt-in false = %d, want 1", got)
	}
	if got := effectiveStoreInvoked(true, 0, window); got != 1 {
		t.Fatalf("configured 0 = %d, want 1", got)
	}
	if got := effectiveStoreInvoked(true, 1, window); got != 1 {
		t.Fatalf("configured 1 = %d, want 1", got)
	}
}

func TestEffectiveStoreInvokedUsesMinOfPeerAndLocal(t *testing.T) {
	window := ul.AsynchronousOperationsWindow{MaximumInvoked: 4, MaximumPerformed: 4}
	if got := effectiveStoreInvoked(true, 8, window); got != 4 {
		t.Fatalf("peer 4 local 8 = %d, want 4", got)
	}
	if got := effectiveStoreInvoked(true, 2, window); got != 2 {
		t.Fatalf("peer 4 local 2 = %d, want 2", got)
	}
	unlimited := ul.AsynchronousOperationsWindow{MaximumInvoked: 0, MaximumPerformed: 0}
	if got := effectiveStoreInvoked(true, 3, unlimited); got != 3 {
		t.Fatalf("unlimited peer local 3 = %d, want 3", got)
	}
	omitted := ul.AsynchronousOperationsWindow{MaximumInvoked: 1, MaximumPerformed: 1}
	if got := effectiveStoreInvoked(true, 8, omitted); got != 1 {
		t.Fatalf("peer 1/1 = %d, want serial", got)
	}
}

func TestStoreSessionPipelineOptIn(t *testing.T) {
	if storeSessionPipelineOptIn(StoreSessionOptions{}) {
		t.Fatal("default options must not opt into pipeline")
	}
	if storeSessionPipelineOptIn(StoreSessionOptions{MaxInvokedOperations: 1}) {
		t.Fatal("MaxInvokedOperations=1 must stay serial")
	}
	if !storeSessionPipelineOptIn(StoreSessionOptions{MaxInvokedOperations: 2}) {
		t.Fatal("MaxInvokedOperations=2 must opt in")
	}
}

func TestStoreSessionDefaultMaxInvokedStaysSerialAgainstPipelinedPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := writeStoreFixtures(t, 2)
	peak := &atomic.Int32{}
	address, serverDone := startCountingStoreSCP(t, ctx, 4, 2, peak, 0, 20*time.Millisecond)

	session, err := NewStoreSession(address, StoreSessionOptions{
		DialOptions: ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.StoreBatch(ctx, pathStoreSources(paths))
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Succeeded != 2 || result.Associations != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("default session peak in-flight = %d, want 1 (serial)", got)
	}
	if err := waitPipelineServer(serverDone); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSessionPipelinesWhenWindowAndOptInAllow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := writeStoreFixtures(t, 3)
	peak := &atomic.Int32{}
	address, serverDone := startCountingStoreSCP(t, ctx, 4, 3, peak, 2, 0)

	session, err := NewStoreSession(address, StoreSessionOptions{
		DialOptions:          ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		MaxInvokedOperations: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.StoreBatch(ctx, pathStoreSources(paths))
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Succeeded != 3 || result.Associations != 1 || !result.Complete {
		t.Fatalf("result = %#v", result)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak in-flight = %d, want at least 2", got)
	}
	if err := waitPipelineServer(serverDone); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSessionPipelineAssignsOutOfOrderResponses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := writeStoreFixtures(t, 2)
	address, serverDone := startOutOfOrderStoreSCP(t, ctx, 2)

	session, err := NewStoreSession(address, StoreSessionOptions{
		DialOptions:          ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		MaxInvokedOperations: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.StoreBatch(ctx, pathStoreSources(paths))
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Succeeded != 2 || len(result.Items) != 2 {
		t.Fatalf("result = %#v", result)
	}
	for i, item := range result.Items {
		if item.SourceIndex != i || item.Outcome != StoreOutcomeSuccess || !item.StatusSet {
			t.Fatalf("item %d = %#v", i, item)
		}
	}
	if err := waitPipelineServer(serverDone); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSessionPipelineCancelAbortsInFlightAndCancelsRemainder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := writeStoreFixtures(t, 3)
	started := make(chan struct{})
	address, serverDone := startHoldFirstStoreSCP(t, ctx, started)

	session, err := NewStoreSession(address, StoreSessionOptions{
		DialOptions:          ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		MaxInvokedOperations: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	batchCtx, batchCancel := context.WithCancel(ctx)
	go func() {
		<-started
		batchCancel()
	}()
	result, err := session.StoreBatch(batchCtx, pathStoreSources(paths))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StoreBatch() error = %v, want context.Canceled", err)
	}
	var unknown, canceled int
	for _, item := range result.Items {
		switch item.Outcome {
		case StoreOutcomeUnknown:
			unknown++
		case StoreOutcomeCanceled:
			canceled++
		}
	}
	if unknown == 0 || canceled == 0 {
		t.Fatalf("cancel outcomes = %#v, want some unknown in-flight and canceled remainder", result.Items)
	}
	_ = waitPipelineServer(serverDone)
}

func TestStoreSessionPipelineHonorsInFlightByteBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := writeStoreFixtures(t, 2)
	info, err := os.Stat(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	peak := &atomic.Int32{}
	address, serverDone := startCountingStoreSCP(t, ctx, 4, 2, peak, 0, 40*time.Millisecond)

	session, err := NewStoreSession(address, StoreSessionOptions{
		DialOptions:          ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "STORESCU"},
		MaxInvokedOperations: 4,
		MaxInFlightBytes:     info.Size(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.StoreBatch(ctx, pathStoreSources(paths))
	if err != nil {
		t.Fatalf("StoreBatch() error = %v", err)
	}
	if result.Succeeded != 2 {
		t.Fatalf("result = %#v", result)
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("byte-budget peak in-flight = %d, want 1", got)
	}
	if err := waitPipelineServer(serverDone); err != nil {
		t.Fatal(err)
	}
}

func writeStoreFixtures(t *testing.T, count int) []string {
	t.Helper()
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := make([]string, count)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("source-%d.dcm", i))
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func pathStoreSources(paths []string) []StoreSource {
	sources := make([]StoreSource, len(paths))
	for i, path := range paths {
		sources[i] = NewPathStoreSource(path)
	}
	return sources
}

func startCountingStoreSCP(t *testing.T, ctx context.Context, window, expected int, peak *atomic.Int32, holdUntil int, delay time.Duration) (string, chan error) {
	t.Helper()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	var inFlight atomic.Int32
	releaseHold := make(chan struct{})
	var releaseOnce sync.Once
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
			AsynchronousOperationsWindow: &ul.AsynchronousOperationsWindow{
				MaximumInvoked: uint16(window), MaximumPerformed: uint16(window),
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		session, err := NewAsyncSession(assoc, AsyncSessionOptions{})
		if err != nil {
			_ = assoc.Close()
			serverDone <- err
			return
		}
		defer func() { _ = session.Close() }()
		session.Handle(CStoreRQ, func(handlerCtx context.Context, async *AsyncSession, message AsyncMessage) error {
			current := inFlight.Add(1)
			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-handlerCtx.Done():
					timer.Stop()
					inFlight.Add(-1)
					return handlerCtx.Err()
				}
			}
			if holdUntil > 1 {
				if int(peak.Load()) >= holdUntil {
					releaseOnce.Do(func() { close(releaseHold) })
				}
				select {
				case <-releaseHold:
				case <-handlerCtx.Done():
					inFlight.Add(-1)
					return handlerCtx.Err()
				}
			}
			inFlight.Add(-1)
			request, err := ParseCStoreRequest(message.Command)
			if err != nil {
				return err
			}
			return async.Respond(handlerCtx, message, (CStoreResponse{
				AffectedSOPClassUID:    request.AffectedSOPClassUID,
				AffectedSOPInstanceUID: request.AffectedSOPInstanceUID,
				Status:                 StatusSuccess,
			}).CommandSet(), nil)
		})
		<-session.Done()
		_ = expected
		serverDone <- session.Err()
	}()
	return listener.Addr().String(), serverDone
}

func startOutOfOrderStoreSCP(t *testing.T, ctx context.Context, expected int) (string, chan error) {
	t.Helper()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
			AsynchronousOperationsWindow: &ul.AsynchronousOperationsWindow{
				MaximumInvoked: 4, MaximumPerformed: 4,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		session, err := NewAsyncSession(assoc, AsyncSessionOptions{})
		if err != nil {
			_ = assoc.Close()
			serverDone <- err
			return
		}
		defer func() { _ = session.Close() }()
		type held struct {
			ctx     context.Context
			session *AsyncSession
			message AsyncMessage
		}
		arrived := make(chan held, expected)
		session.Handle(CStoreRQ, func(handlerCtx context.Context, async *AsyncSession, message AsyncMessage) error {
			select {
			case arrived <- held{handlerCtx, async, message}:
			case <-handlerCtx.Done():
				return handlerCtx.Err()
			}
			<-handlerCtx.Done()
			return handlerCtx.Err()
		})
		go func() {
			heldItems := make([]held, 0, expected)
			for len(heldItems) < expected {
				select {
				case item := <-arrived:
					heldItems = append(heldItems, item)
				case <-ctx.Done():
					return
				case <-session.Done():
					return
				}
			}
			for i := len(heldItems) - 1; i >= 0; i-- {
				item := heldItems[i]
				request, err := ParseCStoreRequest(item.message.Command)
				if err != nil {
					return
				}
				_ = item.session.Respond(item.ctx, item.message, (CStoreResponse{
					AffectedSOPClassUID:    request.AffectedSOPClassUID,
					AffectedSOPInstanceUID: request.AffectedSOPInstanceUID,
					Status:                 StatusSuccess,
				}).CommandSet(), nil)
			}
		}()
		<-session.Done()
		serverDone <- session.Err()
	}()
	return listener.Addr().String(), serverDone
}

func startHoldFirstStoreSCP(t *testing.T, ctx context.Context, started chan struct{}) (string, chan error) {
	t.Helper()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	var once sync.Once
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
			AsynchronousOperationsWindow: &ul.AsynchronousOperationsWindow{
				MaximumInvoked: 4, MaximumPerformed: 4,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		session, err := NewAsyncSession(assoc, AsyncSessionOptions{})
		if err != nil {
			_ = assoc.Close()
			serverDone <- err
			return
		}
		defer func() { _ = session.Close() }()
		session.Handle(CStoreRQ, func(handlerCtx context.Context, async *AsyncSession, message AsyncMessage) error {
			once.Do(func() { close(started) })
			<-handlerCtx.Done()
			return handlerCtx.Err()
		})
		<-session.Done()
		serverDone <- session.Err()
	}()
	return listener.Addr().String(), serverDone
}

func waitPipelineServer(done chan error) error {
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ul.ErrAssociationAborted) || errors.Is(err, ErrAssociationReleased) {
			return nil
		}
		if errors.Is(err, ErrAsyncSessionClosed) {
			return nil
		}
		return err
	case <-time.After(3 * time.Second):
		return errors.New("SCP did not finish")
	}
}
