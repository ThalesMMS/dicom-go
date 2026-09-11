package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
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
	if err := validateStorescpLimits(opts.limits); err != nil {
		t.Fatalf("default storescp limits are unsafe: %v", err)
	}
	if opts.limits.maxPDU != uint(ul.DefaultMaxPDU) || opts.limits.maxDataSetBytes != defaultMaxDataSetBytes ||
		opts.limits.maxElementBytes != defaultMaxElementBytes || opts.limits.maxPixelDataBytes != defaultMaxPixelDataBytes ||
		opts.limits.maxElements != defaultMaxElements || opts.limits.maxSequenceDepth != defaultMaxSequenceDepth ||
		opts.limits.maxAssociations != defaultMaxAssociations || opts.limits.maxStores != defaultMaxStores ||
		opts.limits.storeQueueDepth != defaultStoreQueueDepth || opts.limits.shutdownTimeout != 10*time.Second {
		t.Fatalf("parseArgs() limit defaults = %#v", opts.limits)
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

func TestParseArgsCustomResourceLimitsAndTimeouts(t *testing.T) {
	opts, err := parseArgs([]string{
		"-max-associations", "12",
		"-max-stores", "3",
		"-store-queue-depth", "5",
		"-max-pdu-bytes", "32768",
		"-max-command-bytes", "2048",
		"-max-dataset-bytes", "8192",
		"-max-element-bytes", "1024",
		"-max-elements", "42",
		"-max-sequence-depth", "7",
		"-max-pixel-data-bytes", "4096",
		"-max-pixel-fragments", "9",
		"-negotiation-timeout", "3s",
		"-idle-timeout", "4s",
		"-read-progress-timeout", "5s",
		"-write-progress-timeout", "6s",
		"-release-timeout", "7s",
		"-shutdown-timeout", "8s",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.limits.maxAssociations != 12 || opts.limits.maxStores != 3 || opts.limits.storeQueueDepth != 5 ||
		opts.limits.maxPDU != 32768 || opts.limits.maxCommandBytes != 2048 || opts.limits.maxDataSetBytes != 8192 ||
		opts.limits.maxElementBytes != 1024 || opts.limits.maxElements != 42 || opts.limits.maxSequenceDepth != 7 ||
		opts.limits.maxPixelDataBytes != 4096 || opts.limits.maxPixelDataFragments != 9 ||
		opts.limits.negotiationTimeout != 3*time.Second || opts.limits.idleTimeout != 4*time.Second ||
		opts.limits.readProgressTimeout != 5*time.Second || opts.limits.writeProgressTimeout != 6*time.Second ||
		opts.limits.releaseTimeout != 7*time.Second || opts.limits.shutdownTimeout != 8*time.Second {
		t.Fatalf("custom storescp limits = %#v", opts.limits)
	}
	accept := storescpAcceptOptions(opts)
	if accept.MaxPDU != 32768 || accept.NegotiationTimeout != 3*time.Second || accept.IdleTimeout != 4*time.Second ||
		accept.ReadProgressTimeout != 5*time.Second || accept.WriteProgressTimeout != 6*time.Second || accept.ReleaseTimeout != 7*time.Second {
		t.Fatalf("AcceptOptions did not receive CLI limits: %#v", accept)
	}
}

func TestParseArgsRejectsUnsafeResourceLimits(t *testing.T) {
	for _, args := range [][]string{
		{"-max-associations", "0"},
		{"-max-associations", "2", "-max-stores", "3"},
		{"-max-associations", "4", "-max-stores", "2", "-store-queue-depth", "3"},
		{"-store-queue-depth", "-1"},
		{"-max-dataset-bytes", "0"},
		{"-max-dataset-bytes", "1024", "-max-pixel-data-bytes", "2048"},
		{"-max-elements", "-1"},
		{"-idle-timeout", "0s"},
		{"-shutdown-timeout", "0s"},
	} {
		if _, err := parseArgs(args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Fatalf("parseArgs(%v) error = %v, want errUsage", args, err)
		}
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

func TestReceiveDataSetWithLimitsRejectsSyntheticAdversarialPayloads(t *testing.T) {
	privateTag := core.NewTag(0x7777, 0x0010)
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nested := dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: []core.Element{
		core.NewRawElement(privateTag, core.VROB, []byte{1, 2}),
	}})
	tests := []struct {
		name    string
		data    []byte
		adjust  func(*storescpLimits)
		wantErr error
	}{
		{
			name: "dataset bytes",
			data: dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
				core.NewRawElement(privateTag, core.VROB, []byte{1, 2, 3, 4})),
			adjust:  func(l *storescpLimits) { l.maxDataSetBytes = 1 },
			wantErr: parser.ErrMaxTotalBytesExceeded,
		},
		{
			name: "element bytes",
			data: dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
				core.NewRawElement(privateTag, core.VROB, []byte{1, 2, 3, 4})),
			adjust:  func(l *storescpLimits) { l.maxElementBytes = 3 },
			wantErr: parser.ErrMaxElementBytesExceeded,
		},
		{
			name: "element count",
			data: dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
				core.NewRawElement(privateTag, core.VROB, []byte{1, 2}),
				core.NewRawElement(core.NewTag(0x7777, 0x0011), core.VROB, []byte{3, 4})),
			adjust:  func(l *storescpLimits) { l.maxElements = 1 },
			wantErr: parser.ErrMaxElementsExceeded,
		},
		{
			name:    "sequence depth",
			data:    dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, nested),
			adjust:  func(l *storescpLimits) { l.maxSequenceDepth = 1 },
			wantErr: parser.ErrMaxDepthExceeded,
		},
		{
			name: "pixel data bytes",
			data: dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
				core.NewRawElement(core.TagPixelData, core.VROB, []byte{1, 2, 3, 4})),
			adjust:  func(l *storescpLimits) { l.maxPixelDataBytes = 3 },
			wantErr: parser.ErrMaxPixelDataBytesExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := defaultStorescpLimits()
			tt.adjust(&limits)
			_, err := receiveDataSetWithLimits(nil, incomingCommand{
				pcID: 1, dataPrefix: tt.data, dataLast: true,
			}, transfer.ExplicitVRLittleEndian, limits)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("receiveDataSetWithLimits() error = %v, want %v", err, tt.wantErr)
			}
			if status := dataSetErrorStatus(err); status != statusOutOfResources {
				t.Fatalf("limit status = 0x%04X, want 0x%04X", status, statusOutOfResources)
			}
		})
	}
}

func FuzzReceiveDataSetWithLimits(f *testing.F) {
	f.Add([]byte{})
	f.Add(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian,
		core.NewRawElement(core.TagPixelData, core.VROB, []byte{1, 2, 3, 4})))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		limits := defaultStorescpLimits()
		limits.maxDataSetBytes = int64(len(data)) + 1
		limits.maxElementBytes = 4 << 10
		limits.maxElements = 128
		limits.maxSequenceDepth = 16
		limits.maxPixelDataBytes = 4 << 10
		limits.maxPixelDataFragments = 128
		_, _ = receiveDataSetWithLimits(nil, incomingCommand{
			pcID: 1, dataPrefix: data, dataLast: true,
		}, transfer.ExplicitVRLittleEndian, limits)
	})
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

func TestHandleAssociationLimitReturnsDIMSEFailureAndClosesOnlyPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	limits := defaultStorescpLimits()
	limits.maxElements = 1
	outDir := t.TempDir()
	serverDone := make(chan error, 1)
	go func() {
		assoc, acceptErr := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx, AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		serverDone <- handleAssociationWithLimits(assoc, outDir, io.Discard, limits)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle: "STORESCP", CallingAETitle: "ADVERSARIAL-SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = assoc.Close() }()
	pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dimse.SendCStoreRequest(assoc, pc.ID, dimse.CStoreRequest{
		AffectedSOPClassUID: dicomtest.TestSOPClassUID, MessageID: 44,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := dimse.SendDataSet(assoc, pc.ID, file.Dataset, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	rsp, err := dimse.ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rsp.Status != statusOutOfResources {
		t.Fatalf("C-STORE status = 0x%04X, want 0x%04X", rsp.Status, statusOutOfResources)
	}
	if serverErr := <-serverDone; !errors.Is(serverErr, parser.ErrMaxElementsExceeded) {
		t.Fatalf("association error = %v, want parser element limit", serverErr)
	}
	if _, err := assoc.ReadPDU(); err == nil {
		t.Fatal("limited association remained open")
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("limited dataset wrote %d file(s)", len(entries))
	}
}

func TestStoreAdmissionBoundsActiveAndQueuedWorkAndRecovers(t *testing.T) {
	admission := newStoreAdmission(2, 2)
	first, err := admission.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := admission.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		release func()
		err     error
	}
	queued := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			release, acquireErr := admission.acquire(context.Background())
			queued <- result{release: release, err: acquireErr}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for len(admission.admitted) != 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(admission.admitted); got != 4 {
		t.Fatalf("admitted operations = %d, want active 2 + queued 2", got)
	}
	if got := len(admission.active); got != 2 {
		t.Fatalf("active operations = %d, want 2", got)
	}
	if _, err := admission.acquire(context.Background()); !errors.Is(err, errStoreQueueFull) {
		t.Fatalf("overload acquire error = %v, want errStoreQueueFull", err)
	}

	first()
	second()
	for i := 0; i < 2; i++ {
		result := <-queued
		if result.err != nil {
			t.Fatalf("queued acquire error = %v", result.err)
		}
		result.release()
	}
	if got := len(admission.admitted); got != 0 {
		t.Fatalf("admitted operations after drain = %d, want 0", got)
	}
	recovered, err := admission.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after overload error = %v", err)
	}
	recovered()
}

func TestStoreAdmissionCancellationRemovesQueuedWork(t *testing.T) {
	admission := newStoreAdmission(1, 1)
	release, err := admission.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := admission.acquire(ctx)
		result <- acquireErr
	}()
	deadline := time.Now().Add(time.Second)
	for len(admission.admitted) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued acquire error = %v, want context.Canceled", err)
	}
	if got := len(admission.admitted); got != 1 {
		t.Fatalf("admitted operations after cancellation = %d, want 1", got)
	}
	release()
}

func TestHandleAssociationStoreOverloadReturnsA700ThenContinues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	limits := defaultStorescpLimits()
	limits.maxStores = 1
	limits.storeQueueDepth = 0
	stores := newStoreAdmission(1, 0)
	held, err := stores.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	serverDone := make(chan error, 1)
	go func() {
		assoc, acceptErr := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "STORESCP", Context: ctx, AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID},
		})
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		serverDone <- handleAssociationWithRuntime(ctx, assoc, outDir, io.Discard, limits, stores)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle: "STORESCP", CallingAETitle: "LOAD-SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = assoc.Close() }()
	pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	sendStore := func(messageID uint16) uint16 {
		t.Helper()
		if err := dimse.SendCStoreRequest(assoc, pc.ID, dimse.CStoreRequest{
			AffectedSOPClassUID: dicomtest.TestSOPClassUID, MessageID: messageID,
			AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
		}); err != nil {
			t.Fatal(err)
		}
		if err := dimse.SendDataSet(assoc, pc.ID, file.Dataset, transfer.ExplicitVRLittleEndian); err != nil {
			t.Fatal(err)
		}
		rsp, err := dimse.ReceiveCStoreResponse(assoc, pc.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rsp.Status
	}
	if status := sendStore(70); status != statusOutOfResources {
		t.Fatalf("overload C-STORE status = 0x%04X, want 0x%04X", status, statusOutOfResources)
	}
	held()
	if status := sendStore(71); status != dimse.StatusSuccess {
		t.Fatalf("post-overload C-STORE status = 0x%04X, want success", status)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored files = %d, want only the post-overload object", len(entries))
	}
}

func TestRunServerWithCanceledContextStopsWithoutLeak(t *testing.T) {
	opts, err := parseArgs([]string{"-address", "127.0.0.1:0", "-output", t.TempDir(), "-shutdown-timeout", "20ms"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- runServerWithContext(ctx, opts, io.Discard, io.Discard) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServerWithContext() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runServerWithContext() did not stop after cancellation")
	}
}

func TestRunServerRejectsAssociationOverloadAndShutsDown(t *testing.T) {
	opts, err := parseArgs([]string{
		"-address", "127.0.0.1:0",
		"-output", t.TempDir(),
		"-max-associations", "1",
		"-max-stores", "1",
		"-store-queue-depth", "0",
		"-negotiation-timeout", "1s",
		"-shutdown-timeout", "20ms",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stdoutReader, stdoutWriter := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runServerWithContext(ctx, opts, stdoutWriter, io.Discard)
		_ = stdoutWriter.Close()
	}()
	t.Cleanup(func() {
		cancel()
		_ = stdoutReader.Close()
	})
	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	const listenPrefix = "storescp listening on "
	if !strings.HasPrefix(line, listenPrefix) {
		t.Fatalf("unexpected listen line %q", line)
	}
	address := strings.TrimSpace(strings.TrimPrefix(line, listenPrefix))
	if address == "" {
		t.Fatalf("empty listen address in %q", line)
	}

	silent, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	time.Sleep(20 * time.Millisecond)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	defer cancelDial()
	_, err = ul.DialContext(dialCtx, address, ul.DialOptions{
		CalledAETitle: "STORESCP", CallingAETitle: "OVERLOAD-SCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dimse.VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	var rejection *ul.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("overload DialContext() error = %v, want RejectionError", err)
	}
	if rejection.PDU().Reason != ul.AssociateRJReasonLocalLimitExceeded {
		t.Fatalf("overload rejection = %+v", rejection.PDU())
	}

	cancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("runServerWithContext() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after shutdown deadline")
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
