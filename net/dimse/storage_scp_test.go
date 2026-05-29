package dimse

import (
	"context"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestServeStorageAssociationStoresAndReleases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var stored CStoreRequestContext
	addr, done := startStorageSCP(t, ctx, StorageSCPOptions{
		StoreHandler: CStoreHandlerFunc(func(_ context.Context, req CStoreRequestContext) (uint16, error) {
			stored = req
			return StatusSuccess, nil
		}),
	})

	assoc := dialStoreSCU(t, ctx, addr)
	file := readIntegrationFixture(t)
	result, err := NewStoreClient(assoc).StoreWithOptions(ctx, file.Dataset, CStoreOptions{MessageID: 9})
	if err != nil {
		t.Fatalf("StoreWithOptions() error = %v", err)
	}
	if result.Response.Status != StatusSuccess {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", result.Response.Status, StatusSuccess)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeStorageAssociation() error = %v", err)
	}

	if stored.Request.MessageID != 9 {
		t.Fatalf("stored message ID = %d, want 9", stored.Request.MessageID)
	}
	if stored.DataSetSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("stored syntax = %q, want %q", stored.DataSetSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	if got, ok := stored.DataSet.GetUID(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("stored dataset SOP Instance UID = %q ok=%v", got, ok)
	}
}

func TestServeStorageAssociationHandlesCEchoAndRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, done := startStorageSCP(t, ctx, StorageSCPOptions{
		StoreHandler: CStoreHandlerFunc(func(context.Context, CStoreRequestContext) (uint16, error) {
			t.Fatal("C-STORE handler should not be called for C-ECHO")
			return StatusSuccess, nil
		}),
	})

	assoc, pcID := dialVerificationSCU(t, ctx, addr)
	status, err := SendCEcho(assoc, pcID, 3)
	if err != nil {
		t.Fatalf("SendCEcho() error = %v", err)
	}
	if status.Status != StatusSuccess {
		t.Fatalf("C-ECHO status = 0x%04X, want 0x%04X", status.Status, StatusSuccess)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeStorageAssociation() error = %v", err)
	}
}

func TestServeStorageAssociationReturnsMismatchStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, done := startStorageSCP(t, ctx, StorageSCPOptions{
		StoreHandler: CStoreHandlerFunc(func(context.Context, CStoreRequestContext) (uint16, error) {
			t.Fatal("handler should not be called when validation fails")
			return StatusSuccess, nil
		}),
	})
	file := readIntegrationFixture(t)
	rsp := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              11,
		AffectedSOPInstanceUID: "1.2.3.4.5",
	})
	if rsp.Status != StatusCStoreDataSetDoesNotMatch {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", rsp.Status, StatusCStoreDataSetDoesNotMatch)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeStorageAssociation() error = %v", err)
	}
}

func TestServeStorageAssociationMapsStoreHandlerErrorToFailureStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, done := startStorageSCP(t, ctx, StorageSCPOptions{
		StoreHandler: CStoreHandlerFunc(func(context.Context, CStoreRequestContext) (uint16, error) {
			return 0, context.Canceled
		}),
	})
	file := readIntegrationFixture(t)
	rsp := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              14,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	})
	if rsp.Status != StatusCStoreCannotUnderstand {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", rsp.Status, StatusCStoreCannotUnderstand)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeStorageAssociation() error = %v", err)
	}
}

func TestServeStorageAssociationReturnsOutOfResourcesForDataSetLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var observedStatus uint16
	addr, done := startStorageSCP(t, ctx, StorageSCPOptions{
		MaxDataSetBytes: 1,
		StoreHandler: CStoreHandlerFunc(func(context.Context, CStoreRequestContext) (uint16, error) {
			t.Fatal("handler should not be called when dataset exceeds limit")
			return StatusSuccess, nil
		}),
		OnCStoreResponse: func(_ context.Context, _ CStoreRequestContext, status uint16) {
			observedStatus = status
		},
	})
	file := readIntegrationFixture(t)
	rsp := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              12,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	})
	if rsp.Status != StatusCStoreOutOfResources {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", rsp.Status, StatusCStoreOutOfResources)
	}
	if observedStatus != StatusCStoreOutOfResources {
		t.Fatalf("observed status = 0x%04X, want 0x%04X", observedStatus, StatusCStoreOutOfResources)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeStorageAssociation() error = %v", err)
	}
}

func TestServeStorageAssociationReturnsCannotUnderstandForMalformedDataSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addr, done := startStorageSCP(t, ctx, StorageSCPOptions{
		StoreHandler: CStoreHandlerFunc(func(context.Context, CStoreRequestContext) (uint16, error) {
			t.Fatal("handler should not be called for malformed dataset")
			return StatusSuccess, nil
		}),
	})
	rsp := sendMalformedCStore(t, ctx, addr)
	if rsp.Status != StatusCStoreCannotUnderstand {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", rsp.Status, StatusCStoreCannotUnderstand)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeStorageAssociation() error = %v", err)
	}
}

func startStorageSCP(t *testing.T, ctx context.Context, opts StorageSCPOptions) (string, <-chan error) {
	t.Helper()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		defer func() { _ = listener.Close() }()
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP",
			Context: ctx,
			SupportedAbstractSyntaxes: []string{
				dicomtest.TestSOPClassUID,
				VerificationSOPClassUID,
			},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		done <- ServeStorageAssociation(ctx, assoc, opts)
	}()
	return listener.Addr().String(), done
}

func dialVerificationSCU(t *testing.T, ctx context.Context, addr string) (*ul.Association, byte) {
	t.Helper()
	assoc, err := ul.DialContext(ctx, addr, ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "ECHOSCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	pc, ok := AcceptedVerificationContext(assoc)
	if !ok {
		t.Fatal("verification presentation context not accepted")
	}
	return assoc, pc.ID
}

func sendMalformedCStore(t *testing.T, ctx context.Context, addr string) *CStoreResponse {
	t.Helper()
	assoc := dialStoreSCU(t, ctx, addr)
	defer func() { _ = assoc.Close() }()
	pc, err := AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	req := CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              13,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}
	if err := SendCStoreRequest(assoc, pc.ID, req); err != nil {
		t.Fatalf("SendCStoreRequest() error = %v", err)
	}
	writer := NewPDataWriter(assoc, pc.ID, false, int(ul.DefaultMaxPDU+ul.PDUHeaderSize))
	if _, err := writer.Write([]byte{0x08}); err != nil {
		t.Fatalf("write malformed dataset error = %v", err)
	}
	if err := writer.Finish(); err != nil {
		t.Fatalf("finish malformed dataset error = %v", err)
	}
	rsp, err := ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCStoreResponse() error = %v", err)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	return rsp
}
