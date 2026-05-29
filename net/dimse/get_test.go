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

const cGetTestStorageSOPClassUID = "1.2.840.10008.5.1.4.1.1.2"

func TestCGetRequestAndResponseCommandSetRoundTrip(t *testing.T) {
	req := CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           7,
		Priority:            0,
	}
	reqObj := object.FromElements(req.CommandSet(), std.Dictionary)
	parsedReq, err := ParseCGetRequest(reqObj)
	if err != nil {
		t.Fatalf("ParseCGetRequest() error = %v", err)
	}
	if *parsedReq != req {
		t.Fatalf("ParseCGetRequest() = %#v, want %#v", *parsedReq, req)
	}

	remaining := uint16(0)
	completed := uint16(1)
	failed := uint16(0)
	warning := uint16(0)
	rsp := CGetResponse{
		AffectedSOPClassUID:                 StudyRootGetSOPClassUID,
		MessageIDBeingRespondedTo:           req.MessageID,
		Status:                              StatusSuccess,
		NumberOfRemainingSuboperationsOrNil: &remaining,
		NumberOfCompletedSuboperationsOrNil: &completed,
		NumberOfFailedSuboperationsOrNil:    &failed,
		NumberOfWarningSuboperationsOrNil:   &warning,
	}
	rspObj := object.FromElements(rsp.CommandSet(), std.Dictionary)
	parsedRsp, err := ParseCGetResponse(rspObj)
	if err != nil {
		t.Fatalf("ParseCGetResponse() error = %v", err)
	}
	if parsedRsp.Status != rsp.Status || cMoveCountValue(parsedRsp.NumberOfCompletedSuboperationsOrNil) != completed {
		t.Fatalf("ParseCGetResponse() = %#v, want status/counts from %#v", parsedRsp, rsp)
	}
}

func TestParseCGetResponseAllowsNonstandardNoDatasetType(t *testing.T) {
	remaining := uint16(1)
	completed := uint16(0)
	failed := uint16(0)
	warning := uint16(0)
	rspObj := object.FromElements([]core.Element{
		newUIElement(AffectedSOPClassUID, StudyRootGetSOPClassUID),
		newUSCommandElement(CommandField, CGetRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, 1),
		newUSCommandElement(CommandDataSetType, 0x0001),
		newUSCommandElement(Status, StatusPending),
		newUSCommandElement(NumberOfRemainingSuboperations, remaining),
		newUSCommandElement(NumberOfCompletedSuboperations, completed),
		newUSCommandElement(NumberOfFailedSuboperations, failed),
		newUSCommandElement(NumberOfWarningSuboperations, warning),
	}, std.Dictionary)

	parsedRsp, err := ParseCGetResponse(rspObj)
	if err != nil {
		t.Fatalf("ParseCGetResponse() error = %v", err)
	}
	if parsedRsp.Identifier != nil {
		t.Fatalf("Identifier = %#v, want nil for pending response without dataset", parsedRsp.Identifier)
	}
	if parsedRsp.Status != StatusPending || cMoveCountValue(parsedRsp.NumberOfRemainingSuboperationsOrNil) != remaining {
		t.Fatalf("ParseCGetResponse() = %#v, want pending status/counts", parsedRsp)
	}
}

func TestSendCGetWithProgressHandlesSameAssociationCStore(t *testing.T) {
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
			AETitle:                   "GETSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootGetSOPClassUID, cGetTestStorageSOPClassUID},
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

		getPC, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		storePC, err := AcceptedContextForSOPClass(assoc, cGetTestStorageSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, identifier, err := ReceiveCGetRequest(assoc, getPC.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if identifier == nil {
			serverDone <- errors.New("server received nil C-GET identifier")
			return
		}
		if err := SendCStoreRequest(assoc, storePC.ID, CStoreRequest{
			AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
			MessageID:              21,
			Priority:               0,
			AffectedSOPInstanceUID: "1.2.3.4.5",
		}); err != nil {
			serverDone <- err
			return
		}
		if err := SendDataSet(assoc, storePC.ID, cGetStoredObject(), transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		storeRsp, err := ReceiveCStoreResponse(assoc, storePC.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if storeRsp.Status != StatusSuccess {
			serverDone <- errors.New("server received non-success C-STORE response")
			return
		}
		completed := uint16(1)
		zero := uint16(0)
		if err := SendCGetResponse(assoc, getPC.ID, CGetResponse{
			AffectedSOPClassUID:                 StudyRootGetSOPClassUID,
			MessageIDBeingRespondedTo:           req.MessageID,
			Status:                              StatusSuccess,
			NumberOfRemainingSuboperationsOrNil: &zero,
			NumberOfCompletedSuboperationsOrNil: &completed,
			NumberOfFailedSuboperationsOrNil:    &zero,
			NumberOfWarningSuboperationsOrNil:   &zero,
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

	getPC, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifier := object.FromElements(cGetIdentifierElements(t), std.Dictionary)
	progress, err := SendCGetWithProgress(ctx, assoc, getPC.ID, CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           1,
		Priority:            0,
	}, identifier, transfer.ImplicitVRLittleEndian, CGetStoreHandlerFunc(func(_ context.Context, req CGetStoreRequestContext) (uint16, error) {
		if req.Request.AffectedSOPInstanceUID != "1.2.3.4.5" || req.DataSet == nil {
			t.Fatalf("store request context = %#v", req)
		}
		return StatusSuccess, nil
	}))
	if err != nil {
		t.Fatalf("SendCGetWithProgress() error = %v", err)
	}
	var final *CGetResponse
	for event := range progress {
		if event.Err != nil {
			t.Fatalf("progress error = %v", event.Err)
		}
		if event.Final {
			final = event.Response
		}
	}
	if final == nil || final.Status != StatusSuccess || cMoveCountValue(final.NumberOfCompletedSuboperationsOrNil) != 1 {
		t.Fatalf("final response = %#v, want success with one completed sub-operation", final)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestSendCGetWithProgressCancellationReleasesOperationWhenConsumerStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	opCtx, cancelOp := context.WithCancel(ctx)
	defer cancelOp()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "GETSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootGetSOPClassUID, cGetTestStorageSOPClassUID},
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

		getPC, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, identifier, err := ReceiveCGetRequest(assoc, getPC.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if identifier == nil {
			serverDone <- errors.New("server received nil C-GET identifier")
			return
		}
		if err := SendCGetResponse(assoc, getPC.ID, CGetResponse{
			AffectedSOPClassUID:       StudyRootGetSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    StatusPending,
		}); err != nil {
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
	defer func() { _ = assoc.Close() }()

	getPC, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	progress, err := SendCGetWithProgress(opCtx, assoc, getPC.ID, CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           1,
		Priority:            0,
	}, object.FromElements(cGetIdentifierElements(t), std.Dictionary), transfer.ImplicitVRLittleEndian, CGetStoreHandlerFunc(func(context.Context, CGetStoreRequestContext) (uint16, error) {
		return StatusSuccess, nil
	}))
	if err != nil {
		t.Fatalf("SendCGetWithProgress() error = %v", err)
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

func TestSendCGetWithProgressContinuesAfterStoreFailure(t *testing.T) {
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
			AETitle:                   "GETSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootGetSOPClassUID, cGetTestStorageSOPClassUID},
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

		getPC, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		storePC, err := AcceptedContextForSOPClass(assoc, cGetTestStorageSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, _, err := ReceiveCGetRequest(assoc, getPC.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if err := SendCStoreRequest(assoc, storePC.ID, CStoreRequest{
			AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
			MessageID:              21,
			Priority:               0,
			AffectedSOPInstanceUID: "1.2.3.4.5",
		}); err != nil {
			serverDone <- err
			return
		}
		if err := SendDataSet(assoc, storePC.ID, cGetStoredObject(), transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		storeRsp, err := ReceiveCStoreResponse(assoc, storePC.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if storeRsp.Status != StatusCGetUnableToProcess {
			serverDone <- errors.New("server received unexpected C-STORE failure status")
			return
		}
		failed := uint16(1)
		zero := uint16(0)
		if err := SendCGetResponse(assoc, getPC.ID, CGetResponse{
			AffectedSOPClassUID:                 StudyRootGetSOPClassUID,
			MessageIDBeingRespondedTo:           req.MessageID,
			Status:                              StatusCGetSubOperationsCompleteOneOrMoreFailures,
			NumberOfRemainingSuboperationsOrNil: &zero,
			NumberOfCompletedSuboperationsOrNil: &zero,
			NumberOfFailedSuboperationsOrNil:    &failed,
			NumberOfWarningSuboperationsOrNil:   &zero,
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

	getPC, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	progress, err := SendCGetWithProgress(ctx, assoc, getPC.ID, CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           1,
		Priority:            0,
	}, object.FromElements(cGetIdentifierElements(t), std.Dictionary), transfer.ImplicitVRLittleEndian, CGetStoreHandlerFunc(func(context.Context, CGetStoreRequestContext) (uint16, error) {
		return StatusSuccess, errors.New("local store failed")
	}))
	if err != nil {
		t.Fatalf("SendCGetWithProgress() error = %v", err)
	}
	var final *CGetResponse
	for event := range progress {
		if event.Err != nil {
			t.Fatalf("progress error = %v", event.Err)
		}
		if event.Final {
			final = event.Response
		}
	}
	if final == nil || final.Status != StatusCGetSubOperationsCompleteOneOrMoreFailures || cMoveCountValue(final.NumberOfFailedSuboperationsOrNil) != 1 {
		t.Fatalf("final response = %#v, want warning final response with one failed sub-operation", final)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestSendCGetRequiresAcceptedStorageSCPRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	assoc := &ul.Association{
		AcceptedContexts: []ul.AcceptedContext{
			{ID: 1, AbstractSyntaxUID: StudyRootGetSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
			{ID: 3, AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUID: ul.ImplicitVRLittleEndian},
		},
	}
	_, err := SendCGetWithProgress(ctx, assoc, 1, CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           1,
	}, object.FromElements(cGetIdentifierElements(t), std.Dictionary), transfer.ImplicitVRLittleEndian, CGetStoreHandlerFunc(func(context.Context, CGetStoreRequestContext) (uint16, error) {
		return StatusSuccess, nil
	}))
	if !errors.Is(err, ErrCGetStorageRoleNotAccepted) {
		t.Fatalf("SendCGetWithProgress() error = %v, want ErrCGetStorageRoleNotAccepted", err)
	}
}

func cGetIdentifierElements(t *testing.T) []core.Element {
	t.Helper()
	elems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	return elems
}

func cGetStoredObject() *object.Object {
	return object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{cGetTestStorageSOPClassUID}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{"1.2.3.4.5"}},
	}, std.Dictionary)
}
