package dimse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestServeAssociationHandlesCEchoAndCFindOnSameAssociation(t *testing.T) {
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
			AETitle: "QRSCP",
			Context: ctx,
			SupportedAbstractSyntaxes: []string{
				VerificationSOPClassUID,
				StudyRootFindSOPClassUID,
			},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)
		serverDone <- ServeAssociation(ctx, assoc, AssociationSCPOptions{
			CFindHandler: CFindHandlerFunc(func(_ context.Context, req CFindRequestContext) ([]*object.Object, error) {
				if req.QueryRetrieveLevel != QueryRetrieveLevelStudy {
					t.Errorf("QueryRetrieveLevel = %q, want STUDY", req.QueryRetrieveLevel)
				}
				return []*object.Object{studyRootFindMatch("PID1", "1.2.3.study")}, nil
			}),
		})
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "QRSCP",
		CallingAETitle: "QRSCU",
		Contexts: []ul.PresentationContext{
			{
				AbstractSyntaxUID:  VerificationSOPClassUID,
				TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
			},
			StudyRootFindPresentationContext(),
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	verifyPC, ok := AcceptedVerificationContext(assoc)
	if !ok {
		t.Fatal("verification presentation context not accepted")
	}
	echoRsp, err := SendCEcho(assoc, verifyPC.ID, 7)
	if err != nil {
		t.Fatalf("SendCEcho() error = %v", err)
	}
	if echoRsp.Status != StatusSuccess {
		t.Fatalf("C-ECHO status = 0x%04X, want success", echoRsp.Status)
	}

	findPC, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	if err := SendCFindRequest(assoc, findPC.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           8,
	}, identifierElems); err != nil {
		t.Fatalf("SendCFindRequest() error = %v", err)
	}
	first, match, err := ReceiveCFindResponse(assoc, findPC.ID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveCFindResponse(pending) error = %v", err)
	}
	if first.Status != StatusPending {
		t.Fatalf("pending status = 0x%04X, want 0x%04X", first.Status, StatusPending)
	}
	if got, ok := match.GetString(core.NewTag(0x0010, 0x0020)); !ok || got != "PID1" {
		t.Fatalf("match PatientID = %q ok=%v", got, ok)
	}
	final, _, err := ReceiveCFindResponse(assoc, findPC.ID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveCFindResponse(final) error = %v", err)
	}
	if final.Status != StatusSuccess {
		t.Fatalf("final status = 0x%04X, want success", final.Status)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestServeAssociationKeepsOpenAfterCMoveSCPStatus(t *testing.T) {
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
		serverDone <- ServeAssociation(ctx, assoc, AssociationSCPOptions{
			CMoveHandler: CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
				return nil, NewCMoveSCPError(StatusCMoveMoveDestinationUnknown, "move destination unknown", nil)
			}),
		})
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
		Priority:            PriorityMedium,
		MoveDestination:     "MISSING",
	}, object.FromElements(identifierElems, std.Dictionary), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMove() error = %v", err)
	}
	if rsp.Status != StatusCMoveMoveDestinationUnknown {
		t.Fatalf("C-MOVE status = 0x%04X, want 0x%04X", rsp.Status, StatusCMoveMoveDestinationUnknown)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestServeStudyRootCGetReportsSubOperationCounts(t *testing.T) {
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
			AETitle: "GETSCP",
			Context: ctx,
			SupportedAbstractSyntaxes: []string{
				StudyRootGetSOPClassUID,
				cGetTestStorageSOPClassUID,
			},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
			RoleSelections: []ul.RoleSelectionItem{
				{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true},
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		err = ServeStudyRootCGet(ctx, assoc, pc.ID, CGetHandlerFunc(func(_ context.Context, req CGetRequestContext) ([]CGetSubOperation, error) {
			if req.QueryRetrieveLevel != QueryRetrieveLevelStudy {
				t.Errorf("QueryRetrieveLevel = %q, want STUDY", req.QueryRetrieveLevel)
			}
			return []CGetSubOperation{
				{
					AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
					AffectedSOPInstanceUID: "1.2.3.4.5",
					LoadDataSet: func(context.Context) (*object.Object, error) {
						return cGetStoredObject(), nil
					},
				},
				{
					AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
					AffectedSOPInstanceUID: "1.2.3.4.6",
					LoadDataSet: func(context.Context) (*object.Object, error) {
						return object.FromElements([]core.Element{
							{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{cGetTestStorageSOPClassUID}},
							{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{"1.2.3.4.6"}},
						}, std.Dictionary), nil
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
		CalledAETitle:  "GETSCP",
		CallingAETitle: "GETSCU",
		Contexts: []ul.PresentationContext{
			StudyRootGetPresentationContext(),
			{AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}},
		},
		RoleSelections: []ul.RoleSelectionItem{
			{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	stores := 0
	progress, err := SendCGetWithProgress(ctx, assoc, pc.ID, CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           1,
	}, object.FromElements(cGetIdentifierElements(t), std.Dictionary), transfer.ImplicitVRLittleEndian, CGetStoreHandlerFunc(func(_ context.Context, req CGetStoreRequestContext) (uint16, error) {
		stores++
		if stores == 2 {
			return StatusCGetUnableToProcess, nil
		}
		return StatusSuccess, nil
	}))
	if err != nil {
		t.Fatalf("SendCGetWithProgress() error = %v", err)
	}
	var responses []*CGetResponse
	for event := range progress {
		if event.Err != nil {
			t.Fatalf("progress error = %v", event.Err)
		}
		responses = append(responses, event.Response)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want pending and final", len(responses))
	}
	assertCGetCounts(t, "pending", responses[0], StatusPending, 1, 1, 0, 0)
	assertCGetCounts(t, "final", responses[1], StatusCGetSubOperationsCompleteOneOrMoreFailures, 0, 1, 1, 0)

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestIsHandledSCPStatusErrorIncludesCancelSentinels(t *testing.T) {
	for _, err := range []error{ErrCFindCanceled, ErrCMoveCanceled, ErrCGetCanceled} {
		if !isHandledSCPStatusError(err) {
			t.Fatalf("%v should be handled", err)
		}
	}
}

func TestAssociationOperationErrorClassPrioritizesDeadlineOverCleanupCancel(t *testing.T) {
	err := errors.Join(context.DeadlineExceeded, context.Canceled)
	if got := associationOperationErrorClass(err); got != "timeout" {
		t.Fatalf("associationOperationErrorClass() = %q, want timeout", got)
	}
}

func TestServeAssociationCommandProgressTimeoutBoundsBlockedResponse(t *testing.T) {
	tests := []struct {
		name     string
		controls SCPControls
	}{
		{name: "command progress", controls: SCPControls{CommandProgressTimeout: 20 * time.Millisecond}},
		{name: "overall operation", controls: SCPControls{OperationTimeout: 20 * time.Millisecond}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
				ID:                1,
				AbstractSyntaxUID: VerificationSOPClassUID,
				TransferSyntaxUID: ul.ImplicitVRLittleEndian,
			}})
			sendDone := make(chan error, 1)
			go func() { sendDone <- SendCEchoRequest(peer, 1, 1) }()
			serveDone := make(chan error, 1)
			go func() {
				serveDone <- ServeAssociation(context.Background(), local, AssociationSCPOptions{Controls: tt.controls})
			}()
			if err := <-sendDone; err != nil {
				t.Fatalf("SendCEchoRequest() error = %v", err)
			}
			select {
			case err := <-serveDone:
				if !errors.Is(err, ul.ErrAssociationTimeout) {
					t.Fatalf("ServeAssociation() error = %v, want ErrAssociationTimeout", err)
				}
			case <-time.After(time.Second):
				t.Fatal("ServeAssociation() blocked writing C-ECHO response")
			}
		})
	}
}

func assertCGetCounts(t *testing.T, label string, rsp *CGetResponse, status uint16, remaining, completed, failed, warning uint16) {
	t.Helper()
	if rsp == nil {
		t.Fatalf("%s response is nil", label)
	}
	if rsp.Status != status {
		t.Fatalf("%s status = 0x%04X, want 0x%04X", label, rsp.Status, status)
	}
	if status != StatusPending && status != StatusCGetCancel && rsp.NumberOfRemainingSuboperationsOrNil != nil {
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
