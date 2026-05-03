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
