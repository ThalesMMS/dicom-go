package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
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
	if opts.host != "localhost" || opts.port != 11112 || opts.calledAE != "ANY-SCP" || opts.callingAE != "FINDSCU" || opts.level != "study" || opts.timeout != defaultTimeout || opts.maxResults != 0 {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsCustomValues(t *testing.T) {
	opts, err := parseArgs([]string{
		"-host", "127.0.0.1",
		"-port", "104",
		"-called", "SCP",
		"-calling", "SCU",
		"-level", "series",
		"-timeout", "2s",
		"-max-results", "5",
		"-k", "PatientID=123",
		"-k", "StudyInstanceUID=1.2.3",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.host != "127.0.0.1" || opts.port != 104 || opts.calledAE != "SCP" || opts.callingAE != "SCU" || opts.level != "series" || opts.timeout != 2*time.Second || opts.maxResults != 5 {
		t.Fatalf("parseArgs() = %#v", opts)
	}
	if len(opts.keys) != 2 || opts.keys[0] != "PatientID=123" || opts.keys[1] != "StudyInstanceUID=1.2.3" {
		t.Fatalf("parseArgs() keys = %#v", opts.keys)
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

func TestRunFindSmokeAgainstLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = listener.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                 "FINDSCP",
			Context:                 ctx,
			AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{
				transfer.ImplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()

		pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}

		// Receive request command set.
		cmd, err := dimse.ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := dimse.ParseCFindRequest(cmd)
		if err != nil {
			serverDone <- err
			return
		}
		// Identifier dataset should follow.
		identifier, err := dimse.ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if req.AffectedSOPClassUID != dimse.StudyRootFindSOPClassUID {
			serverDone <- errors.New("unexpected AffectedSOPClassUID")
			return
		}
		// Basic check identifier contains QueryRetrieveLevel (0008,0052).
		if _, ok := identifier.Get(core.NewTag(0x0008, 0x0052)); !ok {
			serverDone <- errors.New("missing QueryRetrieveLevel")
			return
		}

		// Send one pending match
		match := object.FromElements([]core.Element{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{"123"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000D), VR: core.VRUI}, Value: core.StringValue{"1.2.3"}},
		}, std.Dictionary)
		if err := sendCFindResponse(assoc, pc.ID, dimse.CFindResponse{
			AffectedSOPClassUID:       dimse.StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    0xFF00,
		}, match, transfer.ImplicitVRLittleEndian); err != nil {
			serverDone <- err
			return
		}
		// Final success
		if err := sendCFindResponse(assoc, pc.ID, dimse.CFindResponse{
			AffectedSOPClassUID:       dimse.StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    dimse.StatusSuccess,
		}, nil, transfer.ImplicitVRLittleEndian); err != nil {
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
	err = runFind(options{
		host:       "127.0.0.1",
		port:       listener.Addr().(*net.TCPAddr).Port,
		calledAE:   "FINDSCP",
		callingAE:  "FINDSCU",
		level:      "study",
		timeout:    2 * time.Second,
		maxResults: 0,
		keys:       []string{"PatientID=123"},
	}, &stdout)
	if err != nil {
		t.Fatalf("runFind() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("(0010,0020)")) {
		t.Fatalf("stdout = %q, want to contain PatientID tag", stdout.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Final status=0x0000")) {
		t.Fatalf("stdout = %q, want final status", stdout.String())
	}
}
