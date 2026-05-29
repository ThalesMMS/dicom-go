package dimse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestFind_ContextCancellationBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, errCh := Find(ctx, nil, 1, CFindRequest{}, object.New(nil), transfer.ImplicitVRLittleEndian)
	// Drain to ensure goroutine (if any) finishes; should close quickly.
	for range results {
	}
	if err := <-errCh; err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestFind_ContextCancellationDuringReceive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverReady := make(chan struct{})
	serverStop := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		cmd, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := ParseCFindRequest(cmd); err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}
		close(serverReady)
		<-serverStop
		serverDone <- nil
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CallingAETitle: "FINDSCU",
		CalledAETitle:  "FINDSCP",
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	identifier := object.FromElements(identifierElems, std.Dictionary)

	findCtx, findCancel := context.WithCancel(ctx)
	results, errCh := Find(findCtx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifier, transfer.ImplicitVRLittleEndian)

	<-serverReady
	time.Sleep(25 * time.Millisecond)
	findCancel()

	for range results {
	}
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Find() error = %v, want context.Canceled", err)
	}
	close(serverStop)
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestFindRejectsConcurrentOperationOnSameAssociation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverReady := make(chan struct{})
	serverStop := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		cmd, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := ParseCFindRequest(cmd); err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}
		close(serverReady)
		<-serverStop
		serverDone <- nil
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CallingAETitle: "FINDSCU",
		CalledAETitle:  "FINDSCP",
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	identifier := object.FromElements(identifierElems, std.Dictionary)

	firstCtx, firstCancel := context.WithCancel(ctx)
	firstResults, firstErrs := Find(firstCtx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifier, transfer.ImplicitVRLittleEndian)

	<-serverReady

	secondResults, secondErrs := Find(ctx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           2,
	}, identifier, transfer.ImplicitVRLittleEndian)
	for range secondResults {
	}
	if err := <-secondErrs; !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("second Find() error = %v, want ErrOperationInProgress", err)
	}

	firstCancel()
	for range firstResults {
	}
	if err := <-firstErrs; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Find() error = %v, want context.Canceled", err)
	}
	close(serverStop)
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}
