package dimse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	testStoreStatusOutOfResources      uint16 = 0xA700
	testStoreStatusDataSetDoesNotMatch uint16 = 0xA900
)

func TestCStoreEndToEndSavesReadablePart10(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	file := readIntegrationFixture(t)
	outDir := t.TempDir()
	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{
		outDir: outDir,
	}, 1)

	response := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              1,
		Priority:               0,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	})
	if response.Status != StatusSuccess {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", response.Status, StatusSuccess)
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

func TestCStoreAssociationRejectedWithoutMatchingPresentationContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{VerificationSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if assoc != nil {
			_ = assoc.Close()
		}
		serverDone <- err
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "STORESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if assoc != nil {
		_ = assoc.Close()
	}
	if err == nil {
		t.Fatal("DialContext() error = nil, want association rejection")
	}
	var rejection *ul.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("DialContext() error = %T %v, want RejectionError", err, err)
	}
	if serverErr := <-serverDone; !errors.Is(serverErr, ul.ErrNoAcceptedPresentationContexts) {
		t.Fatalf("server error = %v, want ErrNoAcceptedPresentationContexts", serverErr)
	}
}

func TestCStoreReturnsOutOfResourcesWhenOutputDirectoryMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	file := readIntegrationFixture(t)
	missingOutDir := filepath.Join(t.TempDir(), "missing", "out")
	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{
		outDir: missingOutDir,
	}, 1)

	response := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              2,
		Priority:               0,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	})
	if response.Status != testStoreStatusOutOfResources {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", response.Status, testStoreStatusOutOfResources)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

func TestCStoreFileCollisionAppendsNumericSuffix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	file := readIntegrationFixture(t)
	outDir := t.TempDir()
	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{
		outDir: outDir,
	}, 2)

	for i := 0; i < 2; i++ {
		response := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
			AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
			MessageID:              uint16(i + 3),
			Priority:               0,
			AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
		})
		if response.Status != StatusSuccess {
			t.Fatalf("C-STORE #%d status = 0x%04X, want success", i+1, response.Status)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}

	for _, name := range []string{dicomtest.TestSOPInstanceUID + ".dcm", dicomtest.TestSOPInstanceUID + ".1.dcm"} {
		path := filepath.Join(outDir, name)
		if _, err := object.OpenFile(path); err != nil {
			t.Fatalf("OpenFile(%s) error = %v", path, err)
		}
	}
}

func TestCStoreReturnsDataSetDoesNotMatchForSOPInstanceMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	file := readIntegrationFixture(t)
	addr, done := startCStoreIntegrationSCP(t, ctx, cStoreIntegrationSCPOptions{
		outDir: t.TempDir(),
	}, 1)

	response := sendIntegrationCStore(t, ctx, addr, file, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              5,
		Priority:               0,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID + ".999",
	})
	if response.Status != testStoreStatusDataSetDoesNotMatch {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", response.Status, testStoreStatusDataSetDoesNotMatch)
	}
	if err := <-done; err != nil {
		t.Fatalf("SCP error = %v", err)
	}
}

type cStoreIntegrationSCPOptions struct {
	outDir string
}

func startCStoreIntegrationSCP(t *testing.T, ctx context.Context, opts cStoreIntegrationSCPOptions, associations int) (string, <-chan error) {
	t.Helper()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		defer func() { _ = listener.Close() }()
		for i := 0; i < associations; i++ {
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
			if err := handleCStoreIntegrationAssociation(assoc, opts.outDir); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return listener.Addr().String(), done
}

func handleCStoreIntegrationAssociation(assoc *ul.Association, outDir string) error {
	defer func() { _ = assoc.Close() }()
	if len(assoc.AcceptedContexts) == 0 {
		return errors.New("no accepted presentation contexts")
	}
	pc := assoc.AcceptedContexts[0]
	syntax, ok := transfer.DefaultRegistry.Get(pc.TransferSyntaxUID)
	if !ok {
		return fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, pc.TransferSyntaxUID)
	}

	req, err := ReceiveCStoreRequest(assoc, pc.ID)
	if err != nil {
		return err
	}

	status := StatusSuccess
	dataset, err := ReceiveDataSet(assoc, pc.ID, syntax)
	if err != nil {
		status = 0xC000
	} else if err := netstore.ValidateCStoreDataSet(req.AffectedSOPClassUID, req.AffectedSOPInstanceUID, pc, dataset); err != nil {
		status = testStoreStatusDataSetDoesNotMatch
	} else if _, err := netstore.SavePart10(outDir, dataset, syntax); err != nil {
		status = testStoreStatusOutOfResources
	}

	if err := SendCStoreResponse(assoc, pc.ID, CStoreResponse{
		AffectedSOPClassUID:       req.AffectedSOPClassUID,
		MessageIDBeingRespondedTo: req.MessageID,
		AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
		Status:                    status,
	}); err != nil {
		return err
	}

	pdu, err := assoc.ReadPDU()
	if err != nil {
		return err
	}
	if _, ok := pdu.(*ul.ReleaseRQ); !ok {
		return fmt.Errorf("%w: got %T while waiting for A-RELEASE-RQ", ul.ErrUnexpectedPDU, pdu)
	}
	return assoc.WritePDU(&ul.ReleaseRP{})
}

func sendIntegrationCStore(t *testing.T, ctx context.Context, addr string, file *object.File, req CStoreRequest) *CStoreResponse {
	t.Helper()
	assoc, err := ul.DialContext(ctx, addr, ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "STORESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  req.AffectedSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = assoc.Close()
		}
	}()

	pc, err := AcceptedContextForSOPClass(assoc, req.AffectedSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	if err := SendCStoreRequest(assoc, pc.ID, req); err != nil {
		t.Fatalf("SendCStoreRequest() error = %v", err)
	}
	if err := SendDataSet(assoc, pc.ID, file.Dataset, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet() error = %v", err)
	}
	response, err := ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCStoreResponse() error = %v", err)
	}
	if response.MessageIDBeingRespondedTo != req.MessageID {
		t.Fatalf("response MessageIDBeingRespondedTo = %d, want %d", response.MessageIDBeingRespondedTo, req.MessageID)
	}
	if response.AffectedSOPInstanceUID != req.AffectedSOPInstanceUID {
		t.Fatalf("response SOP Instance UID = %q, want %q", response.AffectedSOPInstanceUID, req.AffectedSOPInstanceUID)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	released = true
	return response
}

func readIntegrationFixture(t *testing.T) *object.File {
	t.Helper()
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return file
}
