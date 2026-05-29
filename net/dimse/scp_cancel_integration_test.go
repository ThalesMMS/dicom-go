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

func TestSCPCancelMonitorCorrelatesPresentationContextAndMessageID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{
		{ID: 1, AbstractSyntaxUID: StudyRootFindSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
		{ID: 3, AbstractSyntaxUID: PatientRootFindSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
	})
	monitor := startSCPCancelMonitor(ctx, local, 1, 7, ErrCFindCanceled, false)
	defer monitor.Stop()

	for _, cancelRequest := range []struct {
		pcID      byte
		messageID uint16
	}{
		{pcID: 1, messageID: 8},
		{pcID: 3, messageID: 7},
	} {
		if err := SendCCancelRequest(peer, cancelRequest.pcID, CCancelRequest{MessageIDBeingRespondedTo: cancelRequest.messageID}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-monitor.Context().Done():
			t.Fatalf("C-CANCEL for presentation context %d and message ID %d canceled target context", cancelRequest.pcID, cancelRequest.messageID)
		case <-time.After(20 * time.Millisecond):
		}
	}

	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: 7}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-monitor.Context().Done():
		if err := context.Cause(monitor.Context()); !errors.Is(err, ErrCFindCanceled) {
			t.Fatalf("operation context cause = %v, want ErrCFindCanceled", err)
		}
	case <-ctx.Done():
		t.Fatal("matching C-CANCEL did not cancel target context")
	}
}

func TestSCPCancelMonitorOperationErrorPreservesCancelCauseAfterFailure(t *testing.T) {
	laterErr := errors.New("later association failure")
	monitor := &scpCancelMonitor{
		cancelErr: ErrCFindCanceled,
		canceled:  true,
		err:       laterErr,
	}

	err := monitor.OperationError()
	if !errors.Is(err, ErrCFindCanceled) {
		t.Fatalf("OperationError() = %v, want ErrCFindCanceled", err)
	}
	if !errors.Is(err, laterErr) {
		t.Fatalf("OperationError() = %v, want later association failure", err)
	}
}

func TestServeStudyRootCFindHonorsInterleavedCCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: StudyRootFindSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeStudyRootCFind(ctx, local, 1, CFindHandlerFunc(func(context.Context, CFindRequestContext) ([]*object.Object, error) {
			return []*object.Object{
				studyRootFindMatch("P1", "1.2.3.1"),
				studyRootFindMatch("P2", "1.2.3.2"),
				studyRootFindMatch("P3", "1.2.3.3"),
			}, nil
		}))
	}()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SendCFindRequest(peer, 1, CFindRequest{AffectedSOPClassUID: StudyRootFindSOPClassUID, MessageID: 7}, identifier); err != nil {
		t.Fatal(err)
	}
	first, _, err := ReceiveCFindResponse(peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil || first.Status != StatusPending {
		t.Fatalf("first C-FIND response = %#v, %v; want pending", first, err)
	}
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: 7}); err != nil {
		t.Fatal(err)
	}

	final := first
	for i := 0; i < 4 && final.Status == StatusPending; i++ {
		final, _, err = ReceiveCFindResponse(peer, 1, transfer.ImplicitVRLittleEndian)
		if err != nil {
			t.Fatal(err)
		}
	}
	if final.Status != CFindStatusCancel {
		t.Fatalf("final C-FIND status = 0x%04X, want cancel", final.Status)
	}
	if err := <-serverDone; !errors.Is(err, ErrCFindCanceled) {
		t.Fatalf("ServeStudyRootCFind() error = %v, want ErrCFindCanceled", err)
	}
}

func TestServeStudyRootCMoveHonorsInterleavedCCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: StudyRootMoveSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeStudyRootCMove(ctx, local, 1, CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
			return []CMoveSubOperation{
				{
					AffectedSOPClassUID:    "1.2.3.storage",
					AffectedSOPInstanceUID: "1.2.3.1",
					Store: func(context.Context) CMoveSubOperationResult {
						return CMoveSubOperationResult{Status: StatusSuccess}
					},
				},
				{
					AffectedSOPClassUID:    "1.2.3.storage",
					AffectedSOPInstanceUID: "1.2.3.2",
					Store: func(ctx context.Context) CMoveSubOperationResult {
						<-ctx.Done()
						return CMoveSubOperationResult{Err: ctx.Err()}
					},
				},
			}, nil
		}))
	}()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := CMoveRequest{AffectedSOPClassUID: StudyRootMoveSOPClassUID, MessageID: 8, MoveDestination: "STOREAE"}
	if err := SendCMoveRequest(peer, 1, request); err != nil {
		t.Fatal(err)
	}
	if err := SendDataSet(peer, 1, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	first, err := ReceiveCMoveResponse(peer, 1)
	if err != nil || first.Status != StatusPending {
		t.Fatalf("first C-MOVE response = %#v, %v; want pending", first, err)
	}
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: 8}); err != nil {
		t.Fatal(err)
	}
	final, err := ReceiveCMoveResponse(peer, 1)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != StatusCMoveCancel {
		t.Fatalf("final C-MOVE status = 0x%04X, want cancel", final.Status)
	}
	if final.NumberOfCompletedSuboperationsOrNil == nil || *final.NumberOfCompletedSuboperationsOrNil != 1 {
		t.Fatalf("completed sub-operations = %v, want 1", final.NumberOfCompletedSuboperationsOrNil)
	}
	if err := <-serverDone; !errors.Is(err, ErrCMoveCanceled) {
		t.Fatalf("ServeStudyRootCMove() error = %v, want ErrCMoveCanceled", err)
	}
}

func TestServeStudyRootCMoveCancelGraceBoundsNonCooperativeStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = withSCPControls(ctx, SCPControls{CancelGrace: 20 * time.Millisecond})
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: StudyRootMoveSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	defer close(releaseStore)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeStudyRootCMove(ctx, local, 1, CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
			return []CMoveSubOperation{{
				AffectedSOPClassUID:    "1.2.3.storage",
				AffectedSOPInstanceUID: "1.2.3.1",
				Store: func(context.Context) CMoveSubOperationResult {
					close(storeStarted)
					<-releaseStore
					return CMoveSubOperationResult{Status: StatusSuccess}
				},
			}}, nil
		}))
	}()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := CMoveRequest{AffectedSOPClassUID: StudyRootMoveSOPClassUID, MessageID: 10, MoveDestination: "STOREAE"}
	if err := SendCMoveRequest(peer, 1, request); err != nil {
		t.Fatal(err)
	}
	if err := SendDataSet(peer, 1, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	select {
	case <-storeStarted:
	case <-ctx.Done():
		t.Fatal("C-MOVE Store callback did not start")
	}
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: 10}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if !errors.Is(err, ErrCMoveCanceled) || !errors.Is(err, ErrCancelGraceExceeded) {
			t.Fatalf("ServeStudyRootCMove() error = %v, want cancel grace error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeStudyRootCMove() exceeded cancel grace bound")
	}
}

func TestServeStudyRootCGetHonorsCancelWhileWaitingForCStoreResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{
		{ID: 1, AbstractSyntaxUID: StudyRootGetSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
		{ID: 3, AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
	})
	roles := []ul.RoleSelectionItem{{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true}}
	peer.AcceptedRoleSelections = append([]ul.RoleSelectionItem(nil), roles...)
	local.AcceptedRoleSelections = append([]ul.RoleSelectionItem(nil), roles...)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeStudyRootCGet(ctx, local, 1, CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			return []CGetSubOperation{
				cGetSubOperation("1.2.3.4.1"),
				{
					AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
					AffectedSOPInstanceUID: "1.2.3.4.2",
					LoadDataSet: func(ctx context.Context) (*object.Object, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					},
				},
			}, nil
		}))
	}()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := CGetRequest{AffectedSOPClassUID: StudyRootGetSOPClassUID, MessageID: 9, Priority: PriorityMedium}
	if err := SendCGetRequest(peer, 1, request); err != nil {
		t.Fatal(err)
	}
	if err := SendDataSet(peer, 1, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	storeReq, err := ReceiveCStoreRequest(peer, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReceiveDataSet(peer, 3, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: 9}); err != nil {
		t.Fatal(err)
	}
	if err := SendCStoreResponse(peer, 3, CStoreResponse{
		AffectedSOPClassUID:       storeReq.AffectedSOPClassUID,
		MessageIDBeingRespondedTo: storeReq.MessageID,
		AffectedSOPInstanceUID:    storeReq.AffectedSOPInstanceUID,
		Status:                    StatusSuccess,
	}); err != nil {
		t.Fatal(err)
	}
	command, err := receiveCommandSetWithContext(ctx, peer, 1)
	if err != nil {
		t.Fatal(err)
	}
	final, err := ParseCGetResponse(command)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != StatusCGetCancel {
		t.Fatalf("final C-GET status = 0x%04X, want cancel", final.Status)
	}
	if final.NumberOfCompletedSuboperationsOrNil == nil || *final.NumberOfCompletedSuboperationsOrNil != 1 {
		t.Fatalf("completed sub-operations = %v, want 1", final.NumberOfCompletedSuboperationsOrNil)
	}
	if err := <-serverDone; !errors.Is(err, ErrCGetCanceled) {
		t.Fatalf("ServeStudyRootCGet() error = %v, want ErrCGetCanceled", err)
	}
}

func TestServeStudyRootCGetCancelGraceBoundsNonCooperativeLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = withSCPControls(ctx, SCPControls{CancelGrace: 20 * time.Millisecond})
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{
		{ID: 1, AbstractSyntaxUID: StudyRootGetSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
		{ID: 3, AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
	})
	roles := []ul.RoleSelectionItem{{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true}}
	peer.AcceptedRoleSelections = append([]ul.RoleSelectionItem(nil), roles...)
	local.AcceptedRoleSelections = append([]ul.RoleSelectionItem(nil), roles...)
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	defer close(releaseLoad)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeStudyRootCGet(ctx, local, 1, CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			return []CGetSubOperation{{
				AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
				AffectedSOPInstanceUID: "1.2.3.4.1",
				LoadDataSet: func(context.Context) (*object.Object, error) {
					close(loadStarted)
					<-releaseLoad
					return nil, nil
				},
			}}, nil
		}))
	}()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := CGetRequest{AffectedSOPClassUID: StudyRootGetSOPClassUID, MessageID: 11, Priority: PriorityMedium}
	if err := SendCGetRequest(peer, 1, request); err != nil {
		t.Fatal(err)
	}
	if err := SendDataSet(peer, 1, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	select {
	case <-loadStarted:
	case <-ctx.Done():
		t.Fatal("C-GET LoadDataSet callback did not start")
	}
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: 11}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if !errors.Is(err, ErrCGetCanceled) || !errors.Is(err, ErrCancelGraceExceeded) {
			t.Fatalf("ServeStudyRootCGet() error = %v, want cancel grace error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeStudyRootCGet() exceeded cancel grace bound")
	}
}
