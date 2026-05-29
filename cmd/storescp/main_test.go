package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.address != defaultListenAddress || opts.aeTitle != "STORESCP" || opts.outDir != "." || opts.single {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsCustomValues(t *testing.T) {
	opts, err := parseArgs([]string{"-address", "127.0.0.1:104", "-aetitle", "SCP", "-output", "out", "-single"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.address != "127.0.0.1:104" || opts.aeTitle != "SCP" || opts.outDir != "out" || !opts.single {
		t.Fatalf("parseArgs() = %#v", opts)
	}
}

func TestParseArgsUsageError(t *testing.T) {
	_, err := parseArgs([]string{"extra"}, &bytes.Buffer{})
	if !errors.Is(err, errUsage) {
		t.Fatalf("parseArgs(extra) error = %v, want errUsage", err)
	}

	_, err = parseArgs([]string{"-h"}, &bytes.Buffer{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

func TestCreateUniqueInstanceFileDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, dicomtest.TestSOPInstanceUID+".dcm")
	if err := os.WriteFile(existing, []byte("existing"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	path, f, err := netstore.CreateInstanceFile(dir, dicomtest.TestSOPInstanceUID)
	if err != nil {
		t.Fatalf("CreateInstanceFile() error = %v", err)
	}
	_ = f.Close()
	if path == existing {
		t.Fatalf("CreateInstanceFile() overwrote existing path %s", path)
	}
	if want := filepath.Join(dir, dicomtest.TestSOPInstanceUID+".1.dcm"); path != want {
		t.Fatalf("CreateInstanceFile() path = %s, want %s", path, want)
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "existing" {
		t.Fatalf("existing file changed: data=%q err=%v", got, err)
	}
}

func TestHandleAssociationReceivesFixtureAndWritesReadablePart10(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	outDir := t.TempDir()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                 "STORESCP",
			Context:                 ctx,
			AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{
				transfer.ExplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		var stdout bytes.Buffer
		serverDone <- handleAssociation(assoc, outDir, &stdout)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
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
	pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	if err := dimse.SendCStoreRequest(assoc, pc.ID, dimse.CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              10,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}); err != nil {
		t.Fatalf("SendCStoreRequest() error = %v", err)
	}
	if err := dimse.SendDataSet(assoc, pc.ID, file.Dataset, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet() error = %v", err)
	}
	response, err := dimse.ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCStoreResponse() error = %v", err)
	}
	if response.Status != dimse.StatusSuccess {
		t.Fatalf("response status = 0x%04X, want success", response.Status)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}

	savedPath := filepath.Join(outDir, dicomtest.TestSOPInstanceUID+".dcm")
	saved, err := object.OpenFile(savedPath)
	if err != nil {
		t.Fatalf("OpenFile(saved) error = %v", err)
	}
	if got, ok := saved.GetUID(dicomtags.SOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("saved SOP Instance UID = %q ok=%v", got, ok)
	}
}

func TestHandleAssociationReturnsOutOfResourcesWithoutFatalError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	missingOutDir := filepath.Join(t.TempDir(), "missing", "out")
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                 "STORESCP",
			Context:                 ctx,
			AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{
				transfer.ExplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		var stdout bytes.Buffer
		serverDone <- handleAssociation(assoc, missingOutDir, &stdout)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
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
	pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	if err := dimse.SendCStoreRequest(assoc, pc.ID, dimse.CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              12,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}); err != nil {
		t.Fatalf("SendCStoreRequest() error = %v", err)
	}
	if err := dimse.SendDataSet(assoc, pc.ID, file.Dataset, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet() error = %v", err)
	}
	response, err := dimse.ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCStoreResponse() error = %v", err)
	}
	if response.Status != statusOutOfResources {
		t.Fatalf("response status = 0x%04X, want 0x%04X", response.Status, statusOutOfResources)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v, want nil after sending failure response", err)
	}
}

func TestSupportedTransferSyntaxUIDsIncludesNativeSyntaxes(t *testing.T) {
	got := supportedTransferSyntaxUIDs()
	want := []string{transfer.ImplicitVRLittleEndian.UID, transfer.ExplicitVRLittleEndian.UID}
	if len(got) != len(want) {
		t.Fatalf("supportedTransferSyntaxUIDs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("supportedTransferSyntaxUIDs() = %#v, want %#v", got, want)
		}
	}
}

func TestStorageSOPClassUIDsIncludesSecondaryCapture(t *testing.T) {
	found := false
	for _, uid := range dimse.DefaultStorageSOPClassUIDs() {
		if uid == "1.2.840.10008.5.1.4.1.1.7" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dimse.DefaultStorageSOPClassUIDs() missing Secondary Capture Image Storage")
	}
}

func TestHandleAssociationHandlesCEcho(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	outDir := t.TempDir()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: acceptedAbstractSyntaxes(),
			SupportedTransferSyntaxes: supportedTransferSyntaxUIDs(),
		})
		if err != nil {
			serverDone <- err
			return
		}
		var stdout bytes.Buffer
		serverDone <- handleAssociation(assoc, outDir, &stdout)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "ECHOSCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dimse.VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		t.Fatal("AcceptedVerificationContext() = false")
	}
	if err := dimse.SendCEchoRequest(assoc, pc.ID, 11); err != nil {
		t.Fatalf("SendCEchoRequest() error = %v", err)
	}
	status, err := dimse.ReceiveCEchoResponse(assoc, pc.ID, 11)
	if err != nil {
		t.Fatalf("ReceiveCEchoResponse() error = %v", err)
	}
	if status != dimse.StatusSuccess {
		t.Fatalf("status = 0x%04X, want success", status)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestHandleAssociationAbortsUnsupportedDIMSECommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	outDir := t.TempDir()
	stdout := &bytes.Buffer{}
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "STORESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: acceptedAbstractSyntaxes(),
			SupportedTransferSyntaxes: supportedTransferSyntaxUIDs(),
		})
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- handleAssociation(assoc, outDir, stdout)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "FINDSCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dimse.VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		t.Fatal("AcceptedVerificationContext() = false")
	}
	if err := dimse.SendCommandSet(assoc, pc.ID, dimse.CFindRequest{
		AffectedSOPClassUID: dimse.StudyRootFindSOPClassUID,
		MessageID:           21,
	}.CommandSet()); err != nil {
		t.Fatalf("SendCommandSet(C-FIND-RQ) error = %v", err)
	}
	_, err = assoc.ReadPDU()
	if !errors.Is(err, ul.ErrAssociationAborted) {
		t.Fatalf("ReadPDU() error = %v, want ErrAssociationAborted", err)
	}
	var abortErr *ul.AbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("ReadPDU() error = %v, want *ul.AbortError", err)
	}
	if abortErr.Source != ul.AbortSourceServiceUser || abortErr.Reason != ul.AbortReasonNotSpecified {
		t.Fatalf("abort = source %d reason %d, want %d/%d", abortErr.Source, abortErr.Reason, ul.AbortSourceServiceUser, ul.AbortReasonNotSpecified)
	}
	err = <-serverDone
	if !errors.Is(err, errUnsupportedDIMSECommand) {
		t.Fatalf("server error = %v, want errUnsupportedDIMSECommand", err)
	}
	if !strings.Contains(err.Error(), "C-FIND-RQ") {
		t.Fatalf("server error = %q, want command name", err)
	}
	if !strings.Contains(stdout.String(), "storescp supports only C-ECHO-RQ and C-STORE-RQ") {
		t.Fatalf("stdout = %q, want supported-command log", stdout.String())
	}
}
