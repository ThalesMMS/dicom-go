package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
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
	if opts.host != "localhost" || opts.port != 11112 || opts.calledAE != "ANY-SCP" || opts.callingAE != "FINDSCU" || opts.level != "study" || opts.timeout != defaultTimeout || opts.maxResults != 0 || opts.output != findOutputText || opts.jsonBulkData != jsonBulkDataOmit || opts.jsonMaxResponseBytes != defaultJSONMaxResponseBytes || !opts.jsonSummary {
		t.Fatalf("parseArgs() defaults = %#v", opts)
	}
}

func TestParseArgsJSONLOptionsAndValidation(t *testing.T) {
	opts, err := parseArgs([]string{
		"-output", "JSONL",
		"-json-bulk-data", "INLINE",
		"-json-max-response-bytes", "4096",
		"-json-summary=false",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.output != findOutputJSONL || opts.jsonBulkData != jsonBulkDataInline || opts.jsonMaxResponseBytes != 4096 || opts.jsonSummary {
		t.Fatalf("JSONL options = %#v", opts)
	}
	for _, args := range [][]string{
		{"-output", "xml"},
		{"-json-bulk-data", "drop"},
		{"-json-max-response-bytes", "0"},
		{"-json-max-response-bytes", "268435457"},
	} {
		if _, err := parseArgs(args, io.Discard); !errors.Is(err, errUsage) {
			t.Fatalf("parseArgs(%v) error = %v, want usage", args, err)
		}
	}
}

func TestFindOutputGolden(t *testing.T) {
	identifier := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN}, Value: core.StringValue{"Doe^Jane"}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{"P-738"}},
		core.NewRawElement(core.NewTag(0x0011, 0x1010), core.VROB, []byte{1, 2, 3, 4}),
	}, std.Dictionary)
	for _, test := range []struct {
		name       string
		opts       options
		stdoutFile string
		stderrFile string
	}{
		{name: "text", opts: options{output: findOutputText}, stdoutFile: "testdata/find_text.golden"},
		{name: "jsonl", opts: options{output: findOutputJSONL, jsonBulkData: jsonBulkDataOmit, jsonSummary: true}, stdoutFile: "testdata/find_jsonl.golden", stderrFile: "testdata/find_jsonl_summary.golden"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := writeFindPreamble(&stdout, "study-root", test.opts); err != nil {
				t.Fatal(err)
			}
			if err := writeFindIdentifier(&stdout, identifier, transfer.ImplicitVRLittleEndian, test.opts); err != nil {
				t.Fatal(err)
			}
			if err := writeFindFinal(&stdout, &stderr, "study-root", 1, dimse.StatusSuccess, "", test.opts); err != nil {
				t.Fatal(err)
			}
			assertGoldenFile(t, test.stdoutFile, stdout.Bytes())
			if test.stderrFile == "" {
				if stderr.Len() != 0 {
					t.Fatalf("stderr = %q, want empty", stderr.String())
				}
			} else {
				assertGoldenFile(t, test.stderrFile, stderr.Bytes())
			}
			if test.opts.output == findOutputJSONL {
				lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
				if len(lines) != 1 || !json.Valid([]byte(lines[0])) || strings.Contains(stdout.String(), "Information model") || strings.Contains(stdout.String(), "Final status") {
					t.Fatalf("JSONL stdout = %q", stdout.String())
				}
			}
		})
	}
}

func TestWriteFindIdentifierInlineBulkData(t *testing.T) {
	identifier := object.FromElements([]core.Element{core.NewRawElement(core.NewTag(0x0011, 0x1010), core.VROB, []byte{1, 2, 3, 4})}, std.Dictionary)
	var out bytes.Buffer
	if err := writeFindIdentifier(&out, identifier, transfer.ImplicitVRLittleEndian, options{output: findOutputJSONL, jsonBulkData: jsonBulkDataInline}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "{\"00111010\":{\"vr\":\"OB\",\"InlineBinary\":\"AQIDBA==\"}}\n" {
		t.Fatalf("inline JSONL = %q", got)
	}
}

func TestFindSessionOptionsBoundsJSONLOnly(t *testing.T) {
	textOptions := findSessionOptions(options{output: findOutputText})
	if textOptions.MaxDataSetBytes != 0 || textOptions.ResponseQueueDepth != 0 || textOptions.MaxQueuedMessageBytes != 0 {
		t.Fatalf("text session options changed: %+v", textOptions)
	}
	jsonOptions := findSessionOptions(options{output: findOutputJSONL, jsonMaxResponseBytes: 4096})
	if jsonOptions.MaxDataSetBytes != 4096 || jsonOptions.ResponseQueueDepth != 4 || jsonOptions.MaxQueuedMessageBytes != 4*4096+4<<20 {
		t.Fatalf("JSONL session options = %+v", jsonOptions)
	}
}

func assertGoldenFile(t *testing.T, path string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output mismatch for %s\n got: %q\nwant: %q", path, got, want)
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

func TestParseArgsImageLevel(t *testing.T) {
	opts, err := parseArgs([]string{"-level", "image", "-k", "SOPInstanceUID=1.2.3"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if opts.level != "image" {
		t.Fatalf("level = %q, want image", opts.level)
	}
}

func TestParseArgsModelLevelValidation(t *testing.T) {
	opts, err := parseArgs([]string{"-model", "patient-root", "-level", "patient"}, &bytes.Buffer{})
	if err != nil || opts.model != dimse.QueryRetrieveModelPatientRoot || opts.level != "patient" {
		t.Fatalf("patient options = %#v error=%v", opts, err)
	}
	if _, err := parseArgs([]string{"-model", "study-root", "-level", "patient"}, &bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Fatalf("Study Root patient level error = %v, want usage", err)
	}
	if _, err := parseArgs([]string{"-model", "retired"}, &bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Fatalf("invalid model error = %v, want usage", err)
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

		if err := dimse.ServeStudyRootCFind(ctx, assoc, pc.ID, dimse.CFindHandlerFunc(func(_ context.Context, req dimse.CFindRequestContext) ([]*object.Object, error) {
			if req.Request.AffectedSOPClassUID != dimse.StudyRootFindSOPClassUID {
				return nil, errors.New("unexpected AffectedSOPClassUID")
			}
			if _, ok := req.Identifier.Get(core.NewTag(0x0008, 0x0052)); !ok {
				return nil, errors.New("missing QueryRetrieveLevel")
			}
			match := object.FromElements([]core.Element{
				{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{"123"}},
				{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000D), VR: core.VRUI}, Value: core.StringValue{"1.2.3"}},
			}, std.Dictionary)
			return []*object.Object{match}, nil
		})); err != nil {
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
	err = runFind(context.Background(), options{
		host:       "127.0.0.1",
		port:       listener.Addr().(*net.TCPAddr).Port,
		calledAE:   "FINDSCP",
		callingAE:  "FINDSCU",
		level:      "study",
		timeout:    2 * time.Second,
		maxResults: 0,
		keys:       []string{"PatientID=123"},
	}, &stdout, io.Discard)
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

func TestRunFindPatientRootAgainstLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "FINDSCP", Context: ctx,
			SupportedAbstractSyntaxes: []string{dimse.PatientRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.PatientRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		err = dimse.ServePatientRootCFind(ctx, assoc, pc.ID, dimse.CFindHandlerFunc(func(_ context.Context, req dimse.CFindRequestContext) ([]*object.Object, error) {
			level, _ := req.Identifier.GetString(core.NewTag(0x0008, 0x0052))
			if level != "PATIENT" || req.Request.AffectedSOPClassUID != dimse.PatientRootFindSOPClassUID {
				return nil, errors.New("unexpected Patient Root request")
			}
			return []*object.Object{object.FromElements([]core.Element{
				{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{"PATIENT-736"}},
			}, std.Dictionary)}, nil
		}))
		if err != nil {
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
	var stdout, stderr bytes.Buffer
	err = runFind(context.Background(), options{
		model: dimse.QueryRetrieveModelPatientRoot,
		host:  "127.0.0.1", port: listener.Addr().(*net.TCPAddr).Port,
		calledAE: "FINDSCP", callingAE: "FINDSCU", level: "patient",
		timeout: 2 * time.Second, keys: []string{"PatientID=PATIENT-736"},
		output: findOutputJSONL, jsonBulkData: jsonBulkDataOmit,
		jsonMaxResponseBytes: defaultJSONMaxResponseBytes, jsonSummary: true,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(stdout.String(), "\n")
	if !json.Valid([]byte(line)) || line != "{\"00100020\":{\"vr\":\"LO\",\"Value\":[\"PATIENT-736\"]}}" || strings.Contains(stdout.String(), "Information model") || strings.Contains(stdout.String(), "Final status") {
		t.Fatalf("JSONL stdout = %q", stdout.String())
	}
	if stderr.String() != "findscu: summary model=patient-root matches=1 status=0x0000\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFindCancellationSendsCCancel(t *testing.T) {
	remote, requestReceived, serverDone := startCancelingFindSCP(t)
	host, portText, err := net.SplitHostPort(remote)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runWithContext(ctx, []string{
			"-host", host,
			"-port", portText,
			"-called", "FINDSCP",
			"-calling", "FINDSCU",
			"-level", "study",
			"-timeout", "2s",
		}, &stdout, &stderr)
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive C-FIND request")
	}
	cancel()
	select {
	case code := <-result:
		if code != 130 {
			t.Fatalf("runWithContext() exit = %d, want 130; stderr=%q", code, stderr.String())
		}
	case <-time.After(time.Second):
		t.Fatal("C-FIND did not finish bounded cancellation")
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Final status=0xFE00")) {
		t.Fatalf("stdout = %q, want canceled final status", stdout.String())
	}
}

func TestRunFindTimeoutRemainsFailureAfterCancelFinal(t *testing.T) {
	remote, _, serverDone := startCancelingFindSCP(t)
	host, portText, err := net.SplitHostPort(remote)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-host", host,
		"-port", portText,
		"-called", "FINDSCP",
		"-calling", "FINDSCU",
		"-level", "study",
		"-timeout", "50ms",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() exit = %d, want timeout failure 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Final status=0xFE00")) || !bytes.Contains(stderr.Bytes(), []byte("deadline exceeded")) {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func startCancelingFindSCP(t *testing.T) (string, <-chan struct{}, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requestReceived := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dimse.StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID},
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
		request, _, err := dimse.ReceiveCFindRequest(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		close(requestReceived)
		command, err := dimse.ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		cancelRequest, err := dimse.ParseCCancelRequest(command)
		if err != nil || cancelRequest.MessageIDBeingRespondedTo != request.MessageID {
			serverDone <- fmt.Errorf("C-CANCEL request = %#v, error = %v", cancelRequest, err)
			return
		}
		if err := dimse.SendCFindResponse(assoc, pc.ID, dimse.CFindResponse{
			AffectedSOPClassUID:       dimse.StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: request.MessageID,
			Status:                    dimse.CFindStatusCancel,
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
			serverDone <- errors.New("server expected A-RELEASE-RQ after C-CANCEL")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()
	return listener.Addr().String(), requestReceived, serverDone
}
