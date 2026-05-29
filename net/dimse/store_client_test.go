package dimse

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// dialStoreSCU establishes an association offering the test SOP Class so a
// StoreClient can negotiate a presentation context for it.
func dialStoreSCU(t *testing.T, ctx context.Context, addr string) *ul.Association {
	t.Helper()
	assoc, err := ul.DialContext(ctx, addr, ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "STORESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	return assoc
}

func TestStoreClientStoresReadablePart10(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	file := readIntegrationFixture(t)
	outDir := t.TempDir()
	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{outDir: outDir}, 1)

	assoc := dialStoreSCU(t, ctx, addr)
	client := NewStoreClient(assoc)
	response, err := client.Store(ctx, file.Dataset)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if response.Status != StatusSuccess {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", response.Status, StatusSuccess)
	}
	if response.AffectedSOPInstanceUID != dicomtest.TestSOPInstanceUID {
		t.Fatalf("response SOP Instance UID = %q, want %q", response.AffectedSOPInstanceUID, dicomtest.TestSOPInstanceUID)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}

	savedPath := filepath.Join(outDir, dicomtest.TestSOPInstanceUID+".dcm")
	saved, err := object.OpenFile(savedPath)
	if err != nil {
		t.Fatalf("OpenFile(saved) error = %v", err)
	}
	if got, ok := saved.GetUID(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("saved SOP Instance UID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
}

func TestStoreClientRejectsInvalidPriorityBeforeAssociationUse(t *testing.T) {
	file := readIntegrationFixture(t)
	client := &StoreClient{}
	_, err := client.StoreWithOptions(context.Background(), file.Dataset, CStoreOptions{Priority: 3})
	if !errors.Is(err, ErrStoreInvalidOptions) {
		t.Fatalf("StoreWithOptions() error = %v, want ErrStoreInvalidOptions", err)
	}
	_, err = client.StoreEncodedWithOptions(context.Background(), func(context.Context, io.Writer, transfer.Syntax) error {
		return nil
	}, CStoreOptions{Priority: 3})
	if !errors.Is(err, ErrStoreInvalidOptions) {
		t.Fatalf("StoreEncodedWithOptions() error = %v, want ErrStoreInvalidOptions", err)
	}
}

// TestStoreClientAssignsMessageID confirms a successful store advances the
// client's per-call Message ID counter from its zero value.
func TestStoreClientAssignsMessageID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{outDir: t.TempDir()}, 1)

	assoc := dialStoreSCU(t, ctx, addr)
	client := NewStoreClient(assoc)

	file := readIntegrationFixture(t)
	if _, err := client.Store(ctx, file.Dataset); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if client.messageID != 1 {
		t.Fatalf("messageID after store = %d, want 1", client.messageID)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestStoreClientStoreWithOptionsPropagatesPriorityAndMoveOriginator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	requests := make(chan CStoreRequest, 1)
	done := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = assoc.Close() }()

		pc, err := AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
		if err != nil {
			done <- err
			return
		}
		req, err := ReceiveCStoreRequest(assoc, pc.ID)
		if err != nil {
			done <- err
			return
		}
		requests <- *req
		if _, err := ReceiveDataSet(assoc, pc.ID, transfer.ExplicitVRLittleEndian); err != nil {
			done <- err
			return
		}
		if err := SendCStoreResponse(assoc, pc.ID, CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    StatusSuccess,
		}); err != nil {
			done <- err
			return
		}
		done <- respondToStoreRelease(assoc)
	}()

	assoc := dialStoreSCU(t, ctx, listener.Addr().String())
	client := NewStoreClient(assoc)
	file := readIntegrationFixture(t)
	originatorMessageID := uint16(42)
	result, err := client.StoreWithOptions(ctx, file.Dataset, CStoreOptions{
		Priority:                     PriorityHigh,
		MoveOriginatorAETitle:        "MOVE_SCP",
		MoveOriginatorMessageIDOrNil: &originatorMessageID,
	})
	if err != nil {
		t.Fatalf("StoreWithOptions() error = %v", err)
	}
	if result.Response == nil || result.Response.Status != StatusSuccess {
		t.Fatalf("StoreWithOptions() response = %#v, want success", result.Response)
	}
	if result.PresentationContext.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("negotiated transfer syntax = %q", result.PresentationContext.TransferSyntaxUID)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	req := <-requests
	if req.Priority != PriorityHigh {
		t.Fatalf("Priority = %d, want %d", req.Priority, PriorityHigh)
	}
	if req.MoveOriginatorAETitle != "MOVE_SCP" {
		t.Fatalf("MoveOriginatorAETitle = %q", req.MoveOriginatorAETitle)
	}
	if req.MoveOriginatorMessageIDOrNil == nil || *req.MoveOriginatorMessageIDOrNil != originatorMessageID {
		t.Fatalf("MoveOriginatorMessageIDOrNil = %v, want %d", req.MoveOriginatorMessageIDOrNil, originatorMessageID)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestStoreClientStoreWithOptionsUsesRequestedTransferSyntax(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	receivedPCID := make(chan byte, 1)
	done := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dicomtest.TestSOPClassUID},
			SupportedTransferSyntaxes: []string{
				transfer.ImplicitVRLittleEndian.UID,
				transfer.ExplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = assoc.Close() }()

		req, err := ReceiveCStoreRequest(assoc, 3)
		if err != nil {
			done <- err
			return
		}
		receivedPCID <- 3
		if _, err := ReceiveDataSet(assoc, 3, transfer.ExplicitVRLittleEndian); err != nil {
			done <- err
			return
		}
		if err := SendCStoreResponse(assoc, 3, CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    StatusSuccess,
		}); err != nil {
			done <- err
			return
		}
		done <- respondToStoreRelease(assoc)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "STORESCU",
		Contexts: []ul.PresentationContext{
			{
				AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
				TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
			},
			{
				AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
				TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
			},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	client := NewStoreClient(assoc)
	file := readIntegrationFixture(t)
	result, err := client.StoreWithOptions(ctx, file.Dataset, CStoreOptions{
		TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
	})
	if err != nil {
		t.Fatalf("StoreWithOptions() error = %v", err)
	}
	if result.PresentationContext.ID != 3 {
		t.Fatalf("presentation context ID = %d, want 3", result.PresentationContext.ID)
	}
	if result.PresentationContext.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax = %q, want explicit VR little endian", result.PresentationContext.TransferSyntaxUID)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if pcID := <-receivedPCID; pcID != 3 {
		t.Fatalf("server received PC ID = %d, want 3", pcID)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestStoreClientStorePreservesFailureResponseWithoutStatusError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{
		outDir: filepath.Join(t.TempDir(), "missing"),
	}, 1)

	assoc := dialStoreSCU(t, ctx, addr)
	client := NewStoreClient(assoc)
	file := readIntegrationFixture(t)
	response, err := client.Store(ctx, file.Dataset)
	if err != nil {
		t.Fatalf("Store() error = %v, want nil status error", err)
	}
	if response == nil || response.Status != testStoreStatusOutOfResources {
		t.Fatalf("Store() response = %#v, want out-of-resources status", response)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestCheckCStoreStatusAllowsWarningsAndReportsFailureComments(t *testing.T) {
	if err := CheckCStoreStatus(&CStoreResponse{Status: StatusSuccess}); err != nil {
		t.Fatalf("success status error = %v", err)
	}
	if err := CheckCStoreStatus(&CStoreResponse{Status: 0xB000}); err != nil {
		t.Fatalf("warning status error = %v", err)
	}
	err := CheckCStoreStatus(&CStoreResponse{Status: 0xA700, ErrorComment: "out of disk"})
	if err == nil {
		t.Fatal("failure status error = nil, want error")
	}
	if !strings.Contains(err.Error(), "0xA700") || !strings.Contains(err.Error(), "out of disk") {
		t.Fatalf("failure status error = %q, want status and error comment", err.Error())
	}
}

func TestStoreClientStoreValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := (*StoreClient)(nil).Store(ctx, nil); err == nil {
		t.Fatal("Store() on nil client error = nil, want error")
	}
	if _, err := NewStoreClient(nil).Store(ctx, nil); err == nil {
		t.Fatal("Store() with nil association error = nil, want error")
	}

	file := readIntegrationFixture(t)
	if _, err := NewStoreClient(&ul.Association{}).Store(ctx, nil); err == nil {
		t.Fatal("Store() with nil dataset error = nil, want error")
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := NewStoreClient(&ul.Association{}).Store(canceled, file.Dataset); err == nil {
		t.Fatal("Store() with canceled context error = nil, want error")
	}

	// Association with no accepted contexts → no PC for the SOP Class.
	if _, err := NewStoreClient(&ul.Association{}).Store(ctx, file.Dataset); err == nil {
		t.Fatal("Store() without accepted context error = nil, want error")
	}
}

func TestStoreClientReservesAssociationOperation(t *testing.T) {
	assoc := &ul.Association{}
	if !assoc.TryBeginOperation() {
		t.Fatal("TryBeginOperation() = false, want true")
	}
	defer assoc.EndOperation()

	file := readIntegrationFixture(t)
	_, err := NewStoreClient(assoc).Store(context.Background(), file.Dataset)
	if !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("Store() error = %v, want ErrOperationInProgress", err)
	}
}

func respondToStoreRelease(assoc *ul.Association) error {
	pdu, err := assoc.ReadPDU()
	if err != nil {
		return err
	}
	if _, ok := pdu.(*ul.ReleaseRQ); !ok {
		return ul.ErrUnexpectedPDU
	}
	return assoc.WritePDU(&ul.ReleaseRP{})
}
