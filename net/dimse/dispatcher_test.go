package dimse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestDispatcherRoutesPersistentHandlersAndUnexpectedCommands(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var routedPC byte
	var routedMessageID uint16
	dispatcher.Handle(CEchoRQ, func(_ context.Context, _ *ul.Association, pcID byte, command *object.Object) error {
		messageID, err := CommandUint16(command, MessageID)
		if err != nil {
			return err
		}
		routedPC = pcID
		routedMessageID = messageID
		return nil
	})

	sendErr := sendCommandAsync(peer, 1, CEchoRequest{MessageID: 7}.CommandSet())
	if err := dispatcher.Next(ctx); err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("send C-ECHO-RQ: %v", err)
	}
	if routedPC != 1 || routedMessageID != 7 {
		t.Fatalf("handler routed pc/message = %d/%d, want 1/7", routedPC, routedMessageID)
	}

	sendErr = sendCommandAsync(peer, 1, CEchoResponse{MessageIDBeingRespondedTo: 8, Status: StatusSuccess}.CommandSet())
	err := dispatcher.Next(ctx)
	if !errors.Is(err, ErrUnexpectedCommand) {
		t.Fatalf("Next() error = %v, want ErrUnexpectedCommand", err)
	}
	var unexpected *UnexpectedCommandError
	if !errors.As(err, &unexpected) {
		t.Fatalf("Next() error = %T, want *UnexpectedCommandError", err)
	}
	if unexpected.CommandField != CEchoRSP || unexpected.MessageID != 8 {
		t.Fatalf("UnexpectedCommandError = %#v, want field C-ECHO-RSP and message 8", unexpected)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("send C-ECHO-RSP: %v", err)
	}
}

func TestDispatcherHandleOnceUsesResponseMessageIDAndRemovesHandler(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                3,
		AbstractSyntaxUID: cGetTestStorageSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var calls []string
	dispatcher.Handle(CStoreRSP, func(_ context.Context, _ *ul.Association, _ byte, command *object.Object) error {
		messageID, err := CommandUint16(command, MessageIDBeingRespondedTo)
		if err != nil {
			return err
		}
		calls = append(calls, fmt.Sprintf("persistent:%d", messageID))
		return nil
	})
	dispatcher.HandleOnce(CStoreRSP, 21, func(_ context.Context, _ *ul.Association, _ byte, command *object.Object) error {
		messageID, err := CommandUint16(command, MessageIDBeingRespondedTo)
		if err != nil {
			return err
		}
		calls = append(calls, fmt.Sprintf("once:%d", messageID))
		return nil
	})

	for i := 0; i < 2; i++ {
		sendErr := sendCommandAsync(peer, 3, CStoreResponse{
			AffectedSOPClassUID:       cGetTestStorageSOPClassUID,
			MessageIDBeingRespondedTo: 21,
			AffectedSOPInstanceUID:    "1.2.3",
			Status:                    StatusSuccess,
		}.CommandSet())
		if err := dispatcher.Next(ctx); err != nil {
			t.Fatalf("Next() #%d error = %v", i+1, err)
		}
		if err := <-sendErr; err != nil {
			t.Fatalf("send C-STORE-RSP #%d: %v", i+1, err)
		}
	}

	want := []string{"once:21", "persistent:21"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDispatcherRoutesInterleavedCGetStoreAndFinalResponse(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{
		{ID: 1, AbstractSyntaxUID: StudyRootGetSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
		{ID: 3, AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
	})
	dispatcher := NewDispatcher(local)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var calls []string
	dispatcher.Handle(CStoreRQ, func(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object) error {
		req, err := ParseCStoreRequest(command)
		if err != nil {
			return err
		}
		dataset, err := receiveDataSetWithContext(ctx, assoc, pcID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			return err
		}
		if dataset == nil {
			return errors.New("nil C-STORE dataset")
		}
		calls = append(calls, fmt.Sprintf("store:%d", req.MessageID))
		return SendCStoreResponse(assoc, pcID, CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    StatusSuccess,
		})
	})
	dispatcher.HandleOnce(CGetRSP, 1, func(_ context.Context, _ *ul.Association, _ byte, command *object.Object) error {
		rsp, err := ParseCGetResponse(command)
		if err != nil {
			return err
		}
		calls = append(calls, fmt.Sprintf("get:%d", rsp.MessageIDBeingRespondedTo))
		return nil
	})

	peerDone := make(chan error, 1)
	go func() {
		for _, messageID := range []uint16{21, 22} {
			if err := SendCStoreRequest(peer, 3, CStoreRequest{
				AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
				MessageID:              messageID,
				AffectedSOPInstanceUID: fmt.Sprintf("1.2.3.%d", messageID),
			}); err != nil {
				peerDone <- err
				return
			}
			if err := SendDataSet(peer, 3, cGetStoredObject(), transfer.ImplicitVRLittleEndian); err != nil {
				peerDone <- err
				return
			}
			rsp, err := ReceiveCStoreResponse(peer, 3)
			if err != nil {
				peerDone <- err
				return
			}
			if rsp.MessageIDBeingRespondedTo != messageID || rsp.Status != StatusSuccess {
				peerDone <- fmt.Errorf("C-STORE-RSP = %#v, want message %d success", rsp, messageID)
				return
			}
		}
		peerDone <- SendCGetResponse(peer, 1, CGetResponse{
			AffectedSOPClassUID:       StudyRootGetSOPClassUID,
			MessageIDBeingRespondedTo: 1,
			Status:                    StatusSuccess,
		})
	}()

	for i := 0; i < 3; i++ {
		if err := dispatcher.Next(ctx); err != nil {
			t.Fatalf("Next() #%d error = %v", i+1, err)
		}
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer error = %v", err)
	}
	want := []string{"store:21", "store:22", "get:1"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}

	sendErr := sendCommandAsync(peer, 1, CGetResponse{
		AffectedSOPClassUID:       StudyRootGetSOPClassUID,
		MessageIDBeingRespondedTo: 1,
		Status:                    StatusSuccess,
	}.CommandSet())
	err := dispatcher.Next(ctx)
	if !errors.Is(err, ErrUnexpectedCommand) {
		t.Fatalf("Next() after one-shot removal error = %v, want ErrUnexpectedCommand", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("send duplicate C-GET-RSP: %v", err)
	}
}

func TestDispatcherClassifiesContextErrors(t *testing.T) {
	_, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dispatcher.Next(canceled); !errors.Is(err, ErrOperationCanceled) {
		t.Fatalf("Next(canceled) error = %v, want ErrOperationCanceled", err)
	}

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancelExpired()
	if err := dispatcher.Next(expired); !errors.Is(err, ErrOperationTimeout) {
		t.Fatalf("Next(expired) error = %v, want ErrOperationTimeout", err)
	}
}

func TestDispatcherHandlesAssociationRelease(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	releaseDone := make(chan error, 1)
	go func() {
		if err := peer.Send(ctx, &ul.ReleaseRQ{}); err != nil {
			releaseDone <- err
			return
		}
		pdu, err := peer.Receive(ctx)
		if err != nil {
			releaseDone <- err
			return
		}
		if _, ok := pdu.(*ul.ReleaseRP); !ok {
			releaseDone <- fmt.Errorf("release response PDU = %T, want *ul.ReleaseRP", pdu)
			return
		}
		releaseDone <- nil
	}()

	if err := dispatcher.Next(ctx); !errors.Is(err, ErrAssociationReleased) {
		t.Fatalf("Next() error = %v, want ErrAssociationReleased", err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("release peer error = %v", err)
	}
}

func TestDispatcherReleaseResponseHonorsContextDeadline(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- peer.Send(context.Background(), &ul.ReleaseRQ{})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Next(ctx)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrAssociationReleased) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Next() error = %v, want association release wrapping context deadline", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = peer.Close()
		err := <-done
		t.Fatalf("Next() did not honor context deadline before peer close; error after close = %v", err)
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("send release request: %v", err)
	}
}

func TestDispatcherRunStopsOnAssociationRelease(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	releaseDone := make(chan error, 1)
	go func() {
		if err := peer.Send(ctx, &ul.ReleaseRQ{}); err != nil {
			releaseDone <- err
			return
		}
		pdu, err := peer.Receive(ctx)
		if err != nil {
			releaseDone <- err
			return
		}
		if _, ok := pdu.(*ul.ReleaseRP); !ok {
			releaseDone <- fmt.Errorf("release response PDU = %T, want *ul.ReleaseRP", pdu)
			return
		}
		releaseDone <- nil
	}()

	if err := dispatcher.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v, want nil for clean release", err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("release peer error = %v", err)
	}
}

func TestDispatcherRunReturnsReleaseResponseWriteFailure(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	dispatcher := NewDispatcher(local)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- peer.Send(ctx, &ul.ReleaseRQ{})
		_ = peer.Close()
	}()

	err := dispatcher.Run(ctx)
	if err == nil || !errors.Is(err, ErrAssociationReleased) || !strings.Contains(err.Error(), "release response failed") {
		t.Fatalf("Run() error = %v, want release response write failure", err)
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("send release request: %v", err)
	}
}

func testPipeAssociations(t *testing.T, contexts []ul.AcceptedContext) (*ul.Association, *ul.Association) {
	t.Helper()
	peerConn, localConn := net.Pipe()
	cloneContexts := func() []ul.AcceptedContext {
		return append([]ul.AcceptedContext(nil), contexts...)
	}
	peer := &ul.Association{
		Conn:             peerConn,
		MaxPDU:           ul.DefaultMaxPDU,
		PeerMaxPDU:       ul.DefaultMaxPDU,
		AcceptedContexts: cloneContexts(),
	}
	local := &ul.Association{
		Conn:             localConn,
		MaxPDU:           ul.DefaultMaxPDU,
		PeerMaxPDU:       ul.DefaultMaxPDU,
		AcceptedContexts: cloneContexts(),
	}
	t.Cleanup(func() {
		_ = peer.Close()
		_ = local.Close()
	})
	return peer, local
}

func sendCommandAsync(assoc *ul.Association, pcID byte, elements []core.Element) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- SendCommandSet(assoc, pcID, elements)
	}()
	return errCh
}
