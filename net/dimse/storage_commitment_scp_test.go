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

func TestStorageCommitmentSCUConversationsWithLocalSCP_NActionThenNEventReport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	gotAction := make(chan *NActionRequest, 1)
	gotEvent := make(chan *NEventReportRequest, 1)

	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STGCMT_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}

		// 1) Receive N-ACTION-RQ + dataset
		actionReq, err := ReceiveNActionRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		gotAction <- actionReq
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		if err := SendNActionResponse(assoc, pc.ID, NActionResponse{
			AffectedSOPClassUID:       actionReq.RequestedSOPClassUID,
			AffectedSOPInstanceUID:    actionReq.RequestedSOPInstanceUID,
			MessageIDBeingRespondedTo: actionReq.MessageID,
			Status:                    StatusSuccess,
			HasActionReply:            false,
		}); err != nil {
			serverDone <- err
			return
		}

		// 2) Receive N-EVENT-REPORT-RQ + dataset (client acting as SCP in this test)
		evtReq, err := ReceiveNEventReportRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		gotEvent <- evtReq
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		if err := SendNEventReportResponse(assoc, pc.ID, NEventReportResponse{
			AffectedSOPClassUID:       evtReq.AffectedSOPClassUID,
			AffectedSOPInstanceUID:    evtReq.AffectedSOPInstanceUID,
			MessageIDBeingRespondedTo: evtReq.MessageID,
			Status:                    StatusSuccess,
			HasEventReply:             false,
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
		CalledAETitle:  "STGCMT_SCP",
		CallingAETitle: "STGCMT_SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  StorageCommitmentPushModelSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = assoc.Close() }()

	pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}

	// Send N-ACTION-RQ
	transactionUID := "1.2.3.4.5.6.7.8.9"
	act := NActionRequest{
		RequestedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:               1,
		ActionTypeID:            StorageCommitmentActionTypeID,
	}
	if err := SendNActionRequest(assoc, pc.ID, act); err != nil {
		t.Fatalf("SendNActionRequest() error = %v", err)
	}
	actDS := object.New(std.Dictionary)
	actDS.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1195), VR: core.VRUI}, Value: core.StringValue{transactionUID}}) // TransactionUID
	if err := SendDataSet(assoc, pc.ID, actDS, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet(N-ACTION dataset) error = %v", err)
	}
	actionResp, err := ReceiveNActionResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveNActionResponse() error = %v", err)
	}
	if actionResp.Status != StatusSuccess {
		t.Fatalf("N-ACTION response status = 0x%04X, want 0x%04X", actionResp.Status, StatusSuccess)
	}

	// Send N-EVENT-REPORT-RQ (simulating the SCP callback path, but over same assoc for deterministic test)
	evt := NEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:              2,
		EventTypeID:            StorageCommitmentEventTypeID,
	}
	if err := SendNEventReportRequest(assoc, pc.ID, evt); err != nil {
		t.Fatalf("SendNEventReportRequest() error = %v", err)
	}
	evtDS := object.New(std.Dictionary)
	evtDS.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1195), VR: core.VRUI}, Value: core.StringValue{transactionUID}}) // TransactionUID
	if err := SendDataSet(assoc, pc.ID, evtDS, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet(N-EVENT-REPORT dataset) error = %v", err)
	}
	if _, err := ReceiveNEventReportResponse(assoc, pc.ID); err != nil {
		t.Fatalf("ReceiveNEventReportResponse() error = %v", err)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	serverDoneReceived := false
	var actionReq *NActionRequest
	select {
	case actionReq = <-gotAction:
	case err := <-serverDone:
		serverDoneReceived = true
		if err != nil {
			t.Fatalf("server error before action request assertion: %v", err)
		}
		select {
		case actionReq = <-gotAction:
		default:
			t.Fatalf("server completed without publishing action request")
		}
	}
	if actionReq.ActionTypeID != StorageCommitmentActionTypeID {
		t.Fatalf("ActionTypeID = %d, want %d", actionReq.ActionTypeID, StorageCommitmentActionTypeID)
	}

	var evtReq *NEventReportRequest
	if serverDoneReceived {
		select {
		case evtReq = <-gotEvent:
		default:
			t.Fatalf("server completed without publishing event request")
		}
	} else {
		select {
		case evtReq = <-gotEvent:
		case err := <-serverDone:
			serverDoneReceived = true
			if err != nil {
				t.Fatalf("server error before event request assertion: %v", err)
			}
			select {
			case evtReq = <-gotEvent:
			default:
				t.Fatalf("server completed without publishing event request")
			}
		}
	}
	if evtReq.EventTypeID != StorageCommitmentEventTypeID {
		t.Fatalf("EventTypeID = %d, want %d", evtReq.EventTypeID, StorageCommitmentEventTypeID)
	}

	if !serverDoneReceived {
		if err := <-serverDone; err != nil {
			t.Fatalf("server error = %v", err)
		}
	}
}

func TestStorageCommitmentBuildersNormalizeTransactionUID(t *testing.T) {
	const padded = " 2.25.808 "
	action, err := BuildStorageCommitmentActionInformation(padded, storageCommitmentTestReferences[:1])
	if err != nil {
		t.Fatal(err)
	}
	parsedAction, err := ParseStorageCommitmentActionInformation(action)
	if err != nil || parsedAction.TransactionUID != strings.TrimSpace(padded) {
		t.Fatalf("parsed action TransactionUID = %q, %v", parsedAction.TransactionUID, err)
	}
	event, err := BuildStorageCommitmentEventInformation(padded, storageCommitmentTestReferences[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	parsedEvent, err := ParseStorageCommitmentEventInformation(event)
	if err != nil || parsedEvent.TransactionUID != strings.TrimSpace(padded) {
		t.Fatalf("parsed event TransactionUID = %q, %v", parsedEvent.TransactionUID, err)
	}
}

func TestServeStorageCommitmentSCPHandlesNAction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	gotAction := make(chan StorageCommitmentActionContext, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STGCMT_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		if err := ServeStorageCommitmentSCP(ctx, assoc, pc.ID, StorageCommitmentActionHandlerFunc(func(_ context.Context, req StorageCommitmentActionContext) (StorageCommitmentActionResult, error) {
			gotAction <- req
			return StorageCommitmentActionResult{Status: StatusSuccess}, nil
		})); err != nil {
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
		CalledAETitle:  "STGCMT_SCP",
		CallingAETitle: "STGCMT_SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  StorageCommitmentPushModelSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = assoc.Close() }()

	pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	transactionUID := "1.2.3.4.5.6.7.8.10"
	if err := SendNActionRequest(assoc, pc.ID, NActionRequest{
		RequestedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:               3,
		ActionTypeID:            StorageCommitmentActionTypeID,
	}); err != nil {
		t.Fatalf("SendNActionRequest() error = %v", err)
	}
	actionInfo, err := BuildStorageCommitmentActionInformation(transactionUID, []StorageCommitmentSOPReference{
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3"},
	})
	if err != nil {
		t.Fatalf("BuildStorageCommitmentActionInformation() error = %v", err)
	}
	if err := SendDataSet(assoc, pc.ID, actionInfo, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet() error = %v", err)
	}
	rsp, err := ReceiveNActionResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveNActionResponse() error = %v", err)
	}
	if rsp.Status != StatusSuccess {
		t.Fatalf("N-ACTION-RSP status = 0x%04X, want success", rsp.Status)
	}

	select {
	case req := <-gotAction:
		if req.TransactionUID != transactionUID {
			t.Fatalf("TransactionUID = %q, want %q", req.TransactionUID, transactionUID)
		}
		if len(req.ReferencedSOPs) != 1 || req.ReferencedSOPs[0].SOPInstanceUID != "1.2.3" {
			t.Fatalf("ReferencedSOPs = %#v", req.ReferencedSOPs)
		}
	case err := <-serverDone:
		t.Fatalf("server completed before handler assertion: %v", err)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestServeStorageCommitmentSCPReportsHandlerFailureStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	handlerErr := errors.New("commitment store unavailable")
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STGCMT_SCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServeStorageCommitmentSCP(ctx, assoc, pc.ID, StorageCommitmentActionHandlerFunc(func(context.Context, StorageCommitmentActionContext) (StorageCommitmentActionResult, error) {
			return StorageCommitmentActionResult{Status: StatusStorageCommitmentProcessingFailure}, handlerErr
		}))
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "STGCMT_SCP",
		CallingAETitle: "STGCMT_SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  StorageCommitmentPushModelSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer func() { _ = assoc.Close() }()

	pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	if err := SendNActionRequest(assoc, pc.ID, NActionRequest{
		RequestedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:               4,
		ActionTypeID:            StorageCommitmentActionTypeID,
	}); err != nil {
		t.Fatalf("SendNActionRequest() error = %v", err)
	}
	actionInfo, err := BuildStorageCommitmentActionInformation("1.2.3.4.5.6.7.8.11", []StorageCommitmentSOPReference{
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.4"},
	})
	if err != nil {
		t.Fatalf("BuildStorageCommitmentActionInformation() error = %v", err)
	}
	if err := SendDataSet(assoc, pc.ID, actionInfo, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet() error = %v", err)
	}
	rsp, err := ReceiveNActionResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveNActionResponse() error = %v", err)
	}
	if rsp.Status != StatusStorageCommitmentProcessingFailure {
		t.Fatalf("N-ACTION-RSP status = 0x%04X, want 0x%04X", rsp.Status, StatusStorageCommitmentProcessingFailure)
	}
	if err := <-serverDone; !errors.Is(err, handlerErr) {
		t.Fatalf("server error = %v, want handler error", err)
	}
}

func TestParseStorageCommitmentActionInformationRejectsSOPInstanceUnderMultipleClasses(t *testing.T) {
	references := []StorageCommitmentSOPReference{
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.826.0.1.3680043.10.543.999"},
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", SOPInstanceUID: "1.2.826.0.1.3680043.10.543.999"},
	}
	dataset := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: StorageCommitmentTransactionUID, VR: core.VRUI}, Value: core.StringValue{"2.25.999"}},
		storageCommitmentSOPSequenceElement(StorageCommitmentReferencedSOPSequence, references, false),
	}, std.Dictionary)
	if _, err := ParseStorageCommitmentActionInformation(dataset); !errors.Is(err, ErrStorageCommitmentInvalidResult) {
		t.Fatalf("duplicate SOP Instance UID error = %v", err)
	}
}

func TestServeStorageCommitmentSCPReturnsSpecificInvalidCommandStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		instance   string
		actionType uint16
		wantStatus uint16
	}{
		{"instance", "1.2.3", StorageCommitmentActionTypeID, StatusNoSuchSOPInstance},
		{"action", StorageCommitmentPushModelSOPInstanceUID, StorageCommitmentActionTypeID + 1, StatusNoSuchActionType},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			defer closeOrFail(t, "listener", listener)
			serverDone := make(chan error, 1)
			go func() {
				assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
					AETitle: "STGCMT_SCP", Context: ctx,
					SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
					SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
				})
				if err != nil {
					serverDone <- err
					return
				}
				defer assoc.Close()
				pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
				if err == nil {
					err = ServeStorageCommitmentSCP(ctx, assoc, pc.ID, StorageCommitmentActionHandlerFunc(func(context.Context, StorageCommitmentActionContext) (StorageCommitmentActionResult, error) {
						return StorageCommitmentActionResult{}, errors.New("handler must not run")
					}))
				}
				serverDone <- err
			}()
			assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
				CalledAETitle: "STGCMT_SCP", CallingAETitle: "STGCMT_SCU",
				Contexts: []ul.PresentationContext{{AbstractSyntaxUID: StorageCommitmentPushModelSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer assoc.Close()
			pc, err := AcceptedContextForSOPClass(assoc, StorageCommitmentPushModelSOPClassUID)
			if err != nil {
				t.Fatal(err)
			}
			request := NormalizedActionRequest{
				RequestedSOPClassUID: StorageCommitmentPushModelSOPClassUID, RequestedSOPInstanceUID: test.instance,
				MessageID: 9, CommandDataSetType: DataSetPresent, ActionTypeID: test.actionType,
			}
			if err := SendCommandSetWithContext(ctx, assoc, pc.ID, request.CommandSet()); err != nil {
				t.Fatal(err)
			}
			responseCommand, err := ReceiveCommandSet(assoc, pc.ID)
			if err != nil {
				t.Fatal(err)
			}
			response, err := ParseNormalizedActionResponse(responseCommand)
			if err != nil || response.Status != test.wantStatus {
				t.Fatalf("response = %#v, %v", response, err)
			}
			if err := <-serverDone; err == nil || strings.Contains(err.Error(), "handler must not run") {
				t.Fatalf("server error = %v", err)
			}
		})
	}
}
