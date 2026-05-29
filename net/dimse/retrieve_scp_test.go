package dimse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCMoveSCUConversationsWithLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	const qrMoveSOPClass = "1.2.840.10008.5.1.4.1.2.2.2" // Study Root Query/Retrieve Information Model - MOVE

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	got := make(chan *CMoveRequest, 1)

	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{qrMoveSOPClass},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
		if err != nil {
			serverDone <- err
			return
		}

		req, err := ReceiveCMoveRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		got <- req

		// Receive identifier dataset
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}

		// Send a pending response then final success.
		if err := SendCMoveResponse(assoc, pc.ID, CMoveResponse{
			AffectedSOPClassUID:       qrMoveSOPClass,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    0xFF00,
		}); err != nil {
			serverDone <- err
			return
		}
		if err := SendCMoveResponse(assoc, pc.ID, CMoveResponse{
			AffectedSOPClassUID:       qrMoveSOPClass,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    StatusSuccess,
		}); err != nil {
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
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  qrMoveSOPClass,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}

	identifier := object.New(std.Dictionary)
	identifier.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}}) // QueryRetrieveLevel

	rsp, err := SendCMove(ctx, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: qrMoveSOPClass,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}, identifier, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMove() error = %v", err)
	}
	if rsp.Status != StatusSuccess {
		t.Fatalf("final status = 0x%04X, want 0x%04X", rsp.Status, StatusSuccess)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	serverDoneReceived := false
	select {
	case req := <-got:
		if req.MoveDestination != "DESTAE" {
			t.Fatalf("MoveDestination = %q, want %q", req.MoveDestination, "DESTAE")
		}
	case err := <-serverDone:
		serverDoneReceived = true
		if err != nil {
			t.Fatalf("server exited before request was observed: %v", err)
		}
		select {
		case req := <-got:
			if req.MoveDestination != "DESTAE" {
				t.Fatalf("MoveDestination = %q, want %q", req.MoveDestination, "DESTAE")
			}
		default:
			t.Fatalf("server exited without publishing request")
		}
	}

	if !serverDoneReceived {
		if err := <-serverDone; err != nil {
			t.Fatalf("server error = %v", err)
		}
	}
}

func TestCMoveWithProgressSurfacesReceiveError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	const qrMoveSOPClass = "1.2.840.10008.5.1.4.1.2.2.2"

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{qrMoveSOPClass},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}

		pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
		if err != nil {
			_ = assoc.Close()
			serverDone <- err
			return
		}
		if _, err := ReceiveCMoveRequest(assoc, pc.ID); err != nil {
			_ = assoc.Close()
			serverDone <- err
			return
		}
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			_ = assoc.Close()
			serverDone <- err
			return
		}
		serverDone <- assoc.Close()
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  qrMoveSOPClass,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = assoc.Close() }()

	pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}

	identifier := object.New(std.Dictionary)
	identifier.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}})

	progress, err := SendCMoveWithProgress(ctx, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: qrMoveSOPClass,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}, identifier, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMoveWithProgress() error = %v", err)
	}
	got, ok := <-progress
	if !ok {
		t.Fatalf("progress channel closed without terminal error")
	}
	if got.Err == nil || !got.Final {
		t.Fatalf("progress=%+v, want final receive error", got)
	}
	if _, ok := <-progress; ok {
		t.Fatalf("progress channel remained open after terminal error")
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestCMoveWithProgressOptionsTimesOutBetweenResponsesAndAborts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	const qrMoveSOPClass = "1.2.840.10008.5.1.4.1.2.2.2"

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{qrMoveSOPClass},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := ReceiveCMoveRequest(assoc, pc.ID); err != nil {
			serverDone <- err
			return
		}
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		if _, err := assoc.ReadPDU(); err == nil {
			serverDone <- errors.New("server expected A-ABORT-RQ")
			return
		} else {
			var abortErr *ul.AbortError
			if !errors.As(err, &abortErr) {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  qrMoveSOPClass,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = assoc.Close() }()

	pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}

	identifier := object.New(std.Dictionary)
	identifier.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}})

	progress, err := SendCMoveWithProgressWithOptions(OperationOptions{
		Context:         ctx,
		ResponseTimeout: 25 * time.Millisecond,
		ErrorPolicy:     OperationErrorPolicyAbort,
	}, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: qrMoveSOPClass,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}, identifier, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMoveWithProgressWithOptions() error = %v", err)
	}

	got, ok := <-progress
	if !ok {
		t.Fatalf("progress channel closed without terminal error")
	}
	if got.Err == nil || !got.Final {
		t.Fatalf("progress=%+v, want final timeout error", got)
	}
	if !errors.Is(got.Err, ErrOperationTimeout) {
		t.Fatalf("progress error = %v, want ErrOperationTimeout", got.Err)
	}
	if !errors.Is(got.Err, ErrAssociationStateUncertain) {
		t.Fatalf("progress error = %v, want ErrAssociationStateUncertain", got.Err)
	}
	if _, ok := <-progress; ok {
		t.Fatalf("progress channel remained open after terminal error")
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestCMoveWithProgressPopulatesResponseIdentifier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	const qrMoveSOPClass = "1.2.840.10008.5.1.4.1.2.2.2"

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{qrMoveSOPClass},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := ReceiveCMoveRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		rspIdentifier := object.New(std.Dictionary)
		rspIdentifier.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}})
		if err := SendCMoveResponse(assoc, pc.ID, CMoveResponse{
			AffectedSOPClassUID:       qrMoveSOPClass,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    0xB000,
			Identifier:                rspIdentifier,
		}); err != nil {
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
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  qrMoveSOPClass,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}

	identifier := object.New(std.Dictionary)
	identifier.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}})

	progress, err := SendCMoveWithProgress(ctx, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: qrMoveSOPClass,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}, identifier, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMoveWithProgress() error = %v", err)
	}
	got, ok := <-progress
	if !ok {
		t.Fatalf("progress channel closed without response")
	}
	if got.Err != nil || !got.Final || got.Identifier == nil || got.Response == nil || got.Response.Identifier == nil {
		t.Fatalf("progress=%+v, want final response with identifier", got)
	}
	if _, ok := <-progress; ok {
		t.Fatalf("progress channel remained open after final response")
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestCMoveWithProgressCancellationReleasesOperationWhenConsumerStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opCtx, cancelOp := context.WithCancel(ctx)
	defer cancelOp()

	const qrMoveSOPClass = "1.2.840.10008.5.1.4.1.2.2.2"

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{qrMoveSOPClass},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := ReceiveCMoveRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		for _, status := range []uint16{StatusPending, StatusPending, StatusSuccess} {
			if err := SendCMoveResponse(assoc, pc.ID, CMoveResponse{
				AffectedSOPClassUID:       qrMoveSOPClass,
				MessageIDBeingRespondedTo: req.MessageID,
				Status:                    status,
			}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  qrMoveSOPClass,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = assoc.Close() }()

	pc, err := AcceptedContextForSOPClass(assoc, qrMoveSOPClass)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifier := object.New(std.Dictionary)
	identifier.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}})

	progress, err := SendCMoveWithProgressWithOptions(OperationOptions{
		Context:     opCtx,
		ErrorPolicy: OperationErrorPolicyLeaveOpen,
	}, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: qrMoveSOPClass,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}, identifier, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMoveWithProgressWithOptions() error = %v", err)
	}
	if event := <-progress; event.Err != nil || event.Final {
		t.Fatalf("first progress event = %+v, want pending response", event)
	}
	cancelOp()
	waitForAssociationOperationRelease(t, assoc)

	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestServeStudyRootCMoveReportsSubOperationCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootMoveSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootMoveSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		err = ServeStudyRootCMove(ctx, assoc, pc.ID, CMoveHandlerFunc(func(_ context.Context, req CMoveRequestContext) ([]CMoveSubOperation, error) {
			if req.Request.MoveDestination != "STOREAE" {
				t.Errorf("MoveDestination = %q, want STOREAE", req.Request.MoveDestination)
			}
			if req.QueryRetrieveLevel != QueryRetrieveLevelStudy {
				t.Errorf("QueryRetrieveLevel = %q, want STUDY", req.QueryRetrieveLevel)
			}
			if req.Identifier == nil || req.PresentationContextID != pc.ID || req.IdentifierSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
				t.Errorf("request context = %#v", req)
			}
			return []CMoveSubOperation{
				{
					AffectedSOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
					AffectedSOPInstanceUID: "1.2.3.4.1",
					Store: func(context.Context) CMoveSubOperationResult {
						return CMoveSubOperationResult{Status: StatusSuccess}
					},
				},
				{
					AffectedSOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
					AffectedSOPInstanceUID: "1.2.3.4.2",
					Store: func(context.Context) CMoveSubOperationResult {
						return CMoveSubOperationResult{Status: StatusCMoveUnableToProcess, Err: errors.New("store failed")}
					},
				},
			}, nil
		}))
		if err != nil {
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
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts:       []ul.PresentationContext{StudyRootMovePresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootMoveSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}

	progress, err := SendCMoveWithProgress(ctx, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: StudyRootMoveSOPClassUID,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "STOREAE",
	}, object.FromElements(identifierElems, std.Dictionary), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMoveWithProgress() error = %v", err)
	}

	var responses []*CMoveResponse
	for event := range progress {
		if event.Err != nil {
			t.Fatalf("progress error = %v", event.Err)
		}
		responses = append(responses, event.Response)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want pending and final", len(responses))
	}
	assertCMoveCounts(t, "pending", responses[0], StatusPending, 1, 1, 0, 0)
	assertCMoveCounts(t, "final", responses[1], StatusCMoveSubOperationsCompleteOneOrMoreFailures, 0, 1, 1, 0)
	if ClassifyCMoveStatus(responses[1].Status) != CMoveStatusWarning {
		t.Fatalf("final status class = %v, want warning", ClassifyCMoveStatus(responses[1].Status))
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestServeStudyRootCMoveCanReturnDestinationUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runStudyRootCMoveStatusTest(t, ctx, CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
		return nil, NewCMoveSCPError(StatusCMoveMoveDestinationUnknown, "move destination unknown", nil)
	}))
	if status != StatusCMoveMoveDestinationUnknown {
		t.Fatalf("final status = 0x%04X, want destination unknown 0x%04X", status, StatusCMoveMoveDestinationUnknown)
	}
	var statusErr *CMoveSCPError
	if !errors.As(serverErr, &statusErr) || statusErr.Status != StatusCMoveMoveDestinationUnknown {
		t.Fatalf("server error = %v, want destination unknown status error", serverErr)
	}
}

func TestServeStudyRootCMoveCanReturnCancelStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runStudyRootCMoveStatusTest(t, ctx, CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
		return nil, ErrCMoveCanceled
	}))
	if status != StatusCMoveCancel {
		t.Fatalf("final status = 0x%04X, want cancel 0x%04X", status, StatusCMoveCancel)
	}
	if !errors.Is(serverErr, ErrCMoveCanceled) {
		t.Fatalf("server error = %v, want ErrCMoveCanceled", serverErr)
	}
}

func TestStudyRootCMoveLevelAcceptsImage(t *testing.T) {
	identifierElems, err := BuildStudyRootImageFindKeys(map[string]string{
		"StudyInstanceUID":  "1.2.3",
		"SeriesInstanceUID": "1.2.3.4",
		"SOPInstanceUID":    "1.2.3.4.5",
	})
	if err != nil {
		t.Fatalf("BuildStudyRootImageFindKeys() error = %v", err)
	}
	level, err := studyRootCMoveLevel(object.FromElements(identifierElems, std.Dictionary))
	if err != nil {
		t.Fatalf("studyRootCMoveLevel() error = %v", err)
	}
	if level != QueryRetrieveLevelImage {
		t.Fatalf("level = %q, want IMAGE", level)
	}
}

func TestValidateCMoveSubOperationsRejectsCountOverflow(t *testing.T) {
	ops := make([]CMoveSubOperation, maxCMoveSubOperations+1)
	err := validateCMoveSubOperations(ops)
	if err == nil || !strings.Contains(err.Error(), "too many C-MOVE sub-operations") {
		t.Fatalf("validateCMoveSubOperations() error = %v, want count overflow", err)
	}
}

func assertCMoveCounts(t *testing.T, label string, rsp *CMoveResponse, status uint16, remaining, completed, failed, warning uint16) {
	t.Helper()

	if rsp == nil {
		t.Fatalf("%s response is nil", label)
	}
	if rsp.Status != status {
		t.Fatalf("%s status = 0x%04X, want 0x%04X", label, rsp.Status, status)
	}
	if status != StatusPending && status != StatusCMoveCancel && rsp.NumberOfRemainingSuboperationsOrNil != nil {
		t.Fatalf("%s remaining = %d, want attribute absent for final status 0x%04X", label, *rsp.NumberOfRemainingSuboperationsOrNil, status)
	}
	if got := cMoveCountValue(rsp.NumberOfRemainingSuboperationsOrNil); got != remaining {
		t.Fatalf("%s remaining = %d, want %d", label, got, remaining)
	}
	if got := cMoveCountValue(rsp.NumberOfCompletedSuboperationsOrNil); got != completed {
		t.Fatalf("%s completed = %d, want %d", label, got, completed)
	}
	if got := cMoveCountValue(rsp.NumberOfFailedSuboperationsOrNil); got != failed {
		t.Fatalf("%s failed = %d, want %d", label, got, failed)
	}
	if got := cMoveCountValue(rsp.NumberOfWarningSuboperationsOrNil); got != warning {
		t.Fatalf("%s warning = %d, want %d", label, got, warning)
	}
}

func cMoveCountValue(v *uint16) uint16 {
	if v == nil {
		return 0
	}
	return *v
}

func runStudyRootCMoveStatusTest(t *testing.T, ctx context.Context, handler CMoveHandler) (uint16, error) {
	t.Helper()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootMoveSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootMoveSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServeStudyRootCMove(ctx, assoc, pc.ID, handler)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "MOVESCP",
		CallingAETitle: "MOVESCU",
		Contexts:       []ul.PresentationContext{StudyRootMovePresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootMoveSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	rsp, err := SendCMove(ctx, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: StudyRootMoveSOPClassUID,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "STOREAE",
	}, object.FromElements(identifierElems, std.Dictionary), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMove() error = %v", err)
	}
	return rsp.Status, <-serverDone
}
