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
