package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestParseArgsDefaults(t *testing.T) {
	opts, err := parseArgs([]string{"127.0.0.1:104", "file.dcm"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.address != "127.0.0.1:104" || opts.calledAE != "ANY-SCP" || opts.callingAE != "STORESCU" || opts.timeout != defaultDialTimeout || len(opts.files) != 1 || opts.files[0] != "file.dcm" {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsCustomValues(t *testing.T) {
	opts, err := parseArgs([]string{
		"-called", "SCP",
		"-calling", "SCU",
		"-timeout", "2s",
		"127.0.0.1:104",
		"file.dcm",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.address != "127.0.0.1:104" || opts.calledAE != "SCP" || opts.callingAE != "SCU" || opts.timeout != 2*time.Second || len(opts.files) != 1 || opts.files[0] != "file.dcm" {
		t.Fatalf("parseArgs() = %#v", opts)
	}
}

func TestParseArgsUsageError(t *testing.T) {
	_, err := parseArgs(nil, &bytes.Buffer{})
	if !errors.Is(err, errUsage) {
		t.Fatalf("parseArgs(nil) error = %v, want errUsage", err)
	}

	_, err = parseArgs([]string{"-h"}, &bytes.Buffer{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseArgs(-h) error = %v, want flag.ErrHelp", err)
	}
}

func TestRunStoreSendsFixtureToLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	path := tempDICOMFile(t, data)

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

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
		defer func() { _ = assoc.Close() }()

		pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := dimse.ReceiveCStoreRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := dimse.ReceiveDataSet(assoc, pc.ID, transfer.ExplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		if err := dimse.SendCStoreResponse(assoc, pc.ID, dimse.CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    dimse.StatusSuccess,
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

	var stdout bytes.Buffer
	err = runStore(options{
		address:   listener.Addr().String(),
		calledAE:  "STORESCP",
		callingAE: "STORESCU",
		timeout:   2 * time.Second,
		files:     []string{path},
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runStore() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("C-STORE success")) {
		t.Fatalf("stdout = %q, want success", stdout.String())
	}
}

func tempDICOMFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.dcm")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return f.Name()
}
