package dimse

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestFind_ZeroMatches_FinalSuccessOnly(t *testing.T) {
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

		// Drain request cmd + identifier.
		rqCmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = ParseCFindRequest(rqCmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		finalCmd := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: 1,
			Status:                    StatusSuccess,
			CommandDataSetType:        NoDataSet,
		}
		if err := SendCommandSet(assoc, pc.ID, finalCmd.CommandSet()); err != nil {
			serverDone <- err
			return
		}

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

	identifier := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0008, 0x0052), core.VRCS, "STUDY")}, std.Dictionary)

	results, errs := Find(ctx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifier, transfer.ImplicitVRLittleEndian)

	var got []FindResult
	for r := range results {
		got = append(got, r)
	}
	if err := <-errs; err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	// Minimal API surfaces the final response on the results channel with a nil Identifier.
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1 (final response)", len(got))
	}
	if got[0].Response == nil || got[0].Response.Status != StatusSuccess {
		t.Fatalf("final response = %#v, want success", got[0].Response)
	}
	if got[0].Identifier != nil {
		t.Fatalf("expected nil identifier for final response")
	}

	// Server may close the connection after sending final status; don't require a clean UL release.
	_ = assoc.Close()
	_ = <-serverDone
}

func TestReceiveCFindResponse_InvalidDataSetType(t *testing.T) {
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

		// Drain request cmd + identifier.
		rqCmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = ParseCFindRequest(rqCmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		rsp := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: 1,
			Status:                    StatusSuccess,
			CommandDataSetType:        0x9999,
		}
		serverDone <- SendCommandSet(assoc, pc.ID, rsp.CommandSet())
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

	identifier := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0008, 0x0052), core.VRCS, "STUDY")}, std.Dictionary)

	results, errs := Find(ctx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifier, transfer.ImplicitVRLittleEndian)

	for range results {
	}
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "C-FIND response dataset type") {
		t.Fatalf("expected dataset type error, got %v", err)
	}
	// Server may close the connection intentionally.
	_ = <-serverDone
}

func TestSendCFindRequest_NoAcceptedPresentationContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		// Support no abstract syntaxes -> negotiation should fail.
		_, err := listener.AcceptAssociation(ul.AcceptOptions{AETitle: "FINDSCP", Context: ctx})
		serverDone <- err
	}()

	_, err = ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CallingAETitle: "FINDSCU",
		CalledAETitle:  "FINDSCP",
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err == nil {
		t.Fatalf("expected dial/association negotiation error")
	}
	if err := <-serverDone; err == nil {
		t.Fatalf("expected server accept error")
	}
}

func TestReceiveCFindResponse_TruncatedIdentifierDataSet(t *testing.T) {
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

		// Drain request
		rqCmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = ParseCFindRequest(rqCmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		pendingCmd := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: 1,
			Status:                    StatusPending,
			CommandDataSetType:        DataSetPresent,
		}
		if err := SendCommandSet(assoc, pc.ID, pendingCmd.CommandSet()); err != nil {
			serverDone <- err
			return
		}

		w := NewPDataWriter(assoc, pc.ID, false, peerMaxPDUWithHeader(assoc))
		if _, err := w.Write([]byte{0x01, 0x02, 0x03}); err != nil {
			serverDone <- err
			return
		}
		if err := w.Finish(); err != nil {
			serverDone <- err
			return
		}

		// Close association/connection after sending invalid dataset so client doesn't hang.
		serverDone <- assoc.Close()
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

	// First response OK (no identifier because we won't read it here).
	// Send a proper C-FIND request so server responds.
	identifier := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0008, 0x0052), core.VRCS, "STUDY")}, std.Dictionary)
	if err := SendCFindRequest(assoc, pc.ID, CFindRequest{AffectedSOPClassUID: StudyRootFindSOPClassUID, MessageID: 1}, identifier.Elements()); err != nil {
		t.Fatalf("SendCFindRequest() error = %v", err)
	}

	cmdObj, err := ReceiveCommandSet(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCommandSet() error = %v", err)
	}
	rsp, err := ParseCFindResponse(cmdObj)
	if err != nil {
		t.Fatalf("ParseCFindResponse() error = %v", err)
	}
	if rsp.Status != StatusPending {
		t.Fatalf("status = 0x%04X, want pending", rsp.Status)
	}

	// Now attempt to receive the identifier dataset and expect a parse error.
	_, err = ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
	if err == nil {
		t.Fatalf("expected dataset parse error")
	}
	if !strings.Contains(err.Error(), "receive dataset") {
		t.Fatalf("expected receive dataset error, got %v", err)
	}
	// Server may close the connection intentionally.
	_ = <-serverDone
}

func TestReceiveCFindResponse_IdentifierSyntaxMismatch(t *testing.T) {
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

		// Drain request
		rqCmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = ParseCFindRequest(rqCmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		pendingCmd := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: 1,
			Status:                    StatusPending,
			CommandDataSetType:        DataSetPresent,
		}
		if err := SendCommandSet(assoc, pc.ID, pendingCmd.CommandSet()); err != nil {
			serverDone <- err
			return
		}

		// Encode dataset in Implicit VR LE and send.
		obj := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "P1")}, std.Dictionary)
		var buf bytes.Buffer
		if err := object.WriteDataSet(&buf, obj, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}

		w := NewPDataWriter(assoc, pc.ID, false, peerMaxPDUWithHeader(assoc))
		if _, err := w.Write(buf.Bytes()); err != nil {
			serverDone <- err
			return
		}
		if err := w.Finish(); err != nil {
			serverDone <- err
			return
		}

		// Close association/connection after sending mismatch dataset so client doesn't hang.
		serverDone <- assoc.Close()
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

	// Read command set and then try to decode dataset with wrong syntax.
	// Send a proper C-FIND request so server responds.
	identifier := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0008, 0x0052), core.VRCS, "STUDY")}, std.Dictionary)
	if err := SendCFindRequest(assoc, pc.ID, CFindRequest{AffectedSOPClassUID: StudyRootFindSOPClassUID, MessageID: 1}, identifier.Elements()); err != nil {
		t.Fatalf("SendCFindRequest() error = %v", err)
	}

	cmdObj, err := ReceiveCommandSet(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCommandSet() error = %v", err)
	}
	rsp, err := ParseCFindResponse(cmdObj)
	if err != nil {
		t.Fatalf("ParseCFindResponse() error = %v", err)
	}
	if rsp.CommandDataSetType != DataSetPresent {
		t.Fatalf("expected dataset present")
	}

	wrong := transfer.Syntax{UID: transfer.ExplicitVRBigEndian.UID, Name: "Explicit VR Big Endian"}
	_, err = ReceiveDataSet(assoc, pc.ID, wrong)
	if err == nil {
		t.Fatalf("expected dataset parse error")
	}
	if !strings.Contains(err.Error(), "receive dataset") {
		t.Fatalf("expected receive dataset error, got %v", err)
	}

	// Server may close the connection intentionally.
	_ = <-serverDone
}

func TestFind_Cancellation(t *testing.T) {
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

		// Drain request
		rqCmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		_, err = ParseCFindRequest(rqCmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		// Send pending, then just wait; client should cancel.
		pendingCmd := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: 1,
			Status:                    StatusPending,
			CommandDataSetType:        DataSetPresent,
		}
		if err := SendCommandSet(assoc, pc.ID, pendingCmd.CommandSet()); err != nil {
			serverDone <- err
			return
		}
		pendingID := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "PENDING")}, std.Dictionary)
		if err := sendIdentifierObject(assoc, pc.ID, pendingID, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		// If client cancels without releasing, we expect a network error on further IO.
		_, err = ReceiveCommandSet(assoc, pc.ID)
		if err == nil {
			serverDone <- errors.New("expected connection to close")
			return
		}
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

	identifier := object.FromElements([]core.Element{dicomtest.NewStringElement(core.NewTag(0x0008, 0x0052), core.VRCS, "STUDY")}, std.Dictionary)

	findCtx, findCancel := context.WithCancel(ctx)
	results, errs := Find(findCtx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifier, transfer.ImplicitVRLittleEndian)

	// Consume first pending response then cancel.
	<-results
	findCancel()
	closeOrFail(t, "client association", assoc)

	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("Find() error = %v, want context.Canceled", err)
	}
	_ = <-serverDone
}
