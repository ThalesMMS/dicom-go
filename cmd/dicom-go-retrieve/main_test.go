package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

var (
	tagQueryRetrieveLevel = core.NewTag(0x0008, 0x0052)
	tagStudyInstanceUID   = core.NewTag(0x0020, 0x000D)
	tagSeriesInstanceUID  = core.NewTag(0x0020, 0x000E)
	tagSOPInstanceUID     = core.NewTag(0x0008, 0x0018)
)

func TestBuildMoveIdentifierByLevel(t *testing.T) {
	tests := []struct {
		name string
		opts retrieveOptions
		want map[core.Tag]string
	}{
		{
			name: "study",
			opts: retrieveOptions{level: "study", studyUID: "1.2.study"},
			want: map[core.Tag]string{
				tagQueryRetrieveLevel: "STUDY",
				tagStudyInstanceUID:   "1.2.study",
			},
		},
		{
			name: "series",
			opts: retrieveOptions{level: "series", studyUID: "1.2.study", seriesUID: "1.2.series"},
			want: map[core.Tag]string{
				tagQueryRetrieveLevel: "SERIES",
				tagStudyInstanceUID:   "1.2.study",
				tagSeriesInstanceUID:  "1.2.series",
			},
		},
		{
			name: "image",
			opts: retrieveOptions{level: "image", studyUID: "1.2.study", seriesUID: "1.2.series", sopInstanceUID: "1.2.sop"},
			want: map[core.Tag]string{
				tagQueryRetrieveLevel: "IMAGE",
				tagStudyInstanceUID:   "1.2.study",
				tagSeriesInstanceUID:  "1.2.series",
				tagSOPInstanceUID:     "1.2.sop",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identifier, err := buildMoveIdentifier(tt.opts)
			if err != nil {
				t.Fatalf("buildMoveIdentifier() error = %v", err)
			}
			assertIdentifierStrings(t, identifier, tt.want)
		})
	}
}

func TestBuildMoveIdentifierReportsMissingRequiredUID(t *testing.T) {
	tests := []struct {
		name    string
		opts    retrieveOptions
		wantErr string
	}{
		{name: "study", opts: retrieveOptions{level: "study"}, wantErr: "-study-uid is required for -level=STUDY"},
		{name: "series", opts: retrieveOptions{level: "series", studyUID: "1.2.study"}, wantErr: "-series-uid is required for -level=SERIES"},
		{name: "image", opts: retrieveOptions{level: "image", studyUID: "1.2.study", seriesUID: "1.2.series"}, wantErr: "-sop-instance-uid is required for -level=IMAGE"},
		{name: "patient", opts: retrieveOptions{level: "patient"}, wantErr: "-level must be one of STUDY, SERIES, IMAGE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildMoveIdentifier(tt.opts)
			if err == nil {
				t.Fatal("buildMoveIdentifier() error = nil, want usage error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("buildMoveIdentifier() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunRetrieveMoveSeriesAndImageAgainstLocalSCP(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want map[core.Tag]string
	}{
		{
			name: "series",
			args: []string{"-level", "SERIES", "-study-uid", "1.2.study", "-series-uid", "1.2.series"},
			want: map[core.Tag]string{
				tagQueryRetrieveLevel: "SERIES",
				tagStudyInstanceUID:   "1.2.study",
				tagSeriesInstanceUID:  "1.2.series",
			},
		},
		{
			name: "image",
			args: []string{"-level", "IMAGE", "-study-uid", "1.2.study", "-series-uid", "1.2.series", "-sop-instance-uid", "1.2.sop"},
			want: map[core.Tag]string{
				tagQueryRetrieveLevel: "IMAGE",
				tagStudyInstanceUID:   "1.2.study",
				tagSeriesInstanceUID:  "1.2.series",
				tagSOPInstanceUID:     "1.2.sop",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote, serverDone := startMoveSCP(t, tt.want)

			args := append([]string{
				"-remote", remote,
				"-calling-aet", "MOVESCU",
				"-called-aet", "MOVESCP",
				"-move-destination", "DESTAE",
			}, tt.args...)

			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("run() exit = %d, stderr=%q", code, stderr.String())
			}
			if err := <-serverDone; err != nil {
				t.Fatalf("server error = %v", err)
			}
			if !strings.Contains(stdout.String(), "C-MOVE completed with status=0x0000") {
				t.Fatalf("stdout = %q, want final success", stdout.String())
			}
		})
	}
}

func TestRunRetrievePatientRootMove(t *testing.T) {
	remote, serverDone := startMoveSCPForModel(t, dimse.PatientRootMoveSOPClassUID, map[core.Tag]string{
		tagQueryRetrieveLevel:       "PATIENT",
		core.NewTag(0x0010, 0x0020): "PATIENT-735",
	})
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-model", "patient-root",
		"-remote", remote,
		"-calling-aet", "MOVESCU",
		"-called-aet", "MOVESCP",
		"-move-destination", "DESTAE",
		"-level", "PATIENT",
		"-patient-id", "PATIENT-735",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() exit = %d, stderr=%q", code, stderr.String())
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Information model=patient-root method=move") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRetrievePatientRootMissingPatientIDFailsBeforeDial(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"-model", "patient-root", "-level", "STUDY", "-study-uid", "1.2.3"}, io.Discard, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "-patient-id is required") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestRunRetrieveMissingRequiredUIDReturnsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-level", "SERIES", "-study-uid", "1.2.study"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-series-uid is required for -level=SERIES") {
		t.Fatalf("stderr = %q, want missing series UID usage", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestParseRetrieveArgsMethodDefaultsAndValidation(t *testing.T) {
	opts, err := parseRetrieveArgs(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.method != retrieveMethodMove || opts.outputDir != "." {
		t.Fatalf("defaults = %#v", opts)
	}
	opts, err = parseRetrieveArgs([]string{"-method", "GET", "-output", "received"}, io.Discard)
	if err != nil || opts.method != retrieveMethodGet || opts.outputDir != "received" {
		t.Fatalf("GET options = %#v error=%v", opts, err)
	}
	for _, args := range [][]string{{"-method", "pull"}, {"-method", "get", "-output", ""}} {
		if _, err := parseRetrieveArgs(args, io.Discard); !errors.Is(err, errRetrieveUsage) {
			t.Fatalf("parseRetrieveArgs(%v) error = %v, want usage", args, err)
		}
	}
}

func TestDocumentedCGetExamplesParseOffline(t *testing.T) {
	tests := []struct {
		name       string
		scopeArgs  []string
		outputDir  string
		identifier map[core.Tag]string
	}{
		{
			name:      "study",
			scopeArgs: []string{"-level", "STUDY", "-study-uid", "1.2.3"},
			outputDir: filepath.Join("temporary-root", "study"),
			identifier: map[core.Tag]string{
				tagQueryRetrieveLevel: "STUDY",
				tagStudyInstanceUID:   "1.2.3",
			},
		},
		{
			name:      "series",
			scopeArgs: []string{"-level", "SERIES", "-study-uid", "1.2.3", "-series-uid", "1.2.3.4"},
			outputDir: filepath.Join("temporary-root", "series"),
			identifier: map[core.Tag]string{
				tagQueryRetrieveLevel: "SERIES",
				tagStudyInstanceUID:   "1.2.3",
				tagSeriesInstanceUID:  "1.2.3.4",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{
				"-method", "get", "-model", "study-root",
				"-remote", "192.0.2.10:4007",
				"-called-aet", "TESTPACS", "-calling-aet", "TWINTEST",
				"-output", tt.outputDir, "-timeout", "60s",
			}
			args = append(args, tt.scopeArgs...)

			opts, err := parseRetrieveArgs(args, io.Discard)
			if err != nil {
				t.Fatalf("parseRetrieveArgs() error = %v", err)
			}
			if opts.method != retrieveMethodGet || opts.model != dimse.QueryRetrieveModelStudyRoot ||
				opts.remote != "192.0.2.10:4007" || opts.calledAET != "TESTPACS" ||
				opts.callingAET != "TWINTEST" || opts.outputDir != tt.outputDir || opts.timeout != time.Minute {
				t.Fatalf("parsed options = %#v", opts)
			}
			identifier, err := buildMoveIdentifier(opts)
			if err != nil {
				t.Fatalf("buildMoveIdentifier() error = %v", err)
			}
			assertIdentifierStrings(t, identifier, tt.identifier)
		})
	}
}

func TestRetrieveGetNegotiationProposesBoundedStorageSCPRoles(t *testing.T) {
	sopClassUID, contexts, roles := retrieveNegotiation(retrieveMethodGet, dimse.QueryRetrieveModelStudyRoot)
	if sopClassUID != dimse.StudyRootGetSOPClassUID || len(contexts) < 2 || len(contexts) > 128 {
		t.Fatalf("GET negotiation SOP=%q contexts=%d", sopClassUID, len(contexts))
	}
	if len(roles) != len(contexts)-1 {
		t.Fatalf("GET roles=%d contexts=%d", len(roles), len(contexts))
	}
	for i, role := range roles {
		if !role.SCPRole || role.SCURole || role.SopClassUID != contexts[i+1].AbstractSyntaxUID {
			t.Fatalf("GET role[%d]=%+v context=%+v", i, role, contexts[i+1])
		}
	}
	patientSOPClassUID, patientContexts, patientRoles := retrieveNegotiation(retrieveMethodGet, dimse.QueryRetrieveModelPatientRoot)
	if patientSOPClassUID != dimse.PatientRootGetSOPClassUID || patientContexts[0].AbstractSyntaxUID != dimse.PatientRootGetSOPClassUID || len(patientRoles) != len(roles) {
		t.Fatalf("Patient Root GET negotiation SOP=%q contexts=%d roles=%d", patientSOPClassUID, len(patientContexts), len(patientRoles))
	}
}

func TestPrintRetrieveFinalDistinguishesStatusClasses(t *testing.T) {
	for _, test := range []struct {
		status   uint16
		outcome  string
		exitCode int
	}{
		{status: dimse.StatusSuccess, outcome: "completed", exitCode: 0},
		{status: dimse.StatusCGetSubOperationsCompleteOneOrMoreFailures, outcome: "warning", exitCode: 0},
		{status: dimse.StatusCGetUnableToProcess, outcome: "failed", exitCode: 1},
		{status: dimse.StatusCGetCancel, outcome: "canceled", exitCode: 130},
	} {
		var out bytes.Buffer
		if got := printRetrieveFinal(&out, "C-GET", test.status, nil, nil, nil, nil); got != test.exitCode {
			t.Fatalf("status 0x%04X exit = %d, want %d", test.status, got, test.exitCode)
		}
		if !strings.Contains(out.String(), "C-GET "+test.outcome+" with status=") {
			t.Fatalf("status 0x%04X output = %q", test.status, out.String())
		}
	}
}

func TestRunRetrieveGetStoresDuplicateInstancesSafely(t *testing.T) {
	const (
		storageSOPClassUID = "1.2.840.10008.5.1.4.1.1.2"
		instanceUID        = "1.2.826.0.1.3680043.9.7433.735.1"
	)
	remote, serverDone := startGetSCP(t, storageSOPClassUID, []string{instanceUID, instanceUID})
	outDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-method", "get",
		"-remote", remote,
		"-calling-aet", "GETSCU",
		"-called-aet", "GETSCP",
		"-output", outDir,
		"-level", "STUDY",
		"-study-uid", "1.2.study",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() exit = %d, stderr=%q", code, stderr.String())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !strings.Contains(stdout.String(), "C-GET completed with status=0x0000") || !strings.Contains(stdout.String(), "completed=2") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	wantPaths := []string{
		filepath.Join(outDir, instanceUID+".dcm"),
		filepath.Join(outDir, instanceUID+".1.dcm"),
	}
	for _, path := range wantPaths {
		if _, err := object.OpenFile(path); err != nil {
			t.Fatalf("OpenFile(%s) error = %v", path, err)
		}
	}
}

func TestRunRetrieveGetCancellationLeavesNoPartialFile(t *testing.T) {
	remote, requestReceived, serverDone := startCancelingGetSCP(t)
	outDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	go func() {
		result <- runWithContext(ctx, []string{
			"-method", "get",
			"-remote", remote,
			"-calling-aet", "GETSCU",
			"-called-aet", "GETSCP",
			"-output", outDir,
			"-level", "STUDY",
			"-study-uid", "1.2.study",
			"-timeout", "2s",
		}, io.Discard, io.Discard)
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive C-GET request")
	}
	cancel()
	select {
	case code := <-result:
		if code != 130 {
			t.Fatalf("runWithContext() exit = %d, want 130", code)
		}
	case <-time.After(time.Second):
		t.Fatal("C-GET did not stop after cancellation")
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled C-GET left %d file(s)", len(entries))
	}
}

func TestRunRetrieveMoveCancellationSendsCCancel(t *testing.T) {
	remote, requestReceived, serverDone := startCancelingMoveSCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runWithContext(ctx, []string{
			"-remote", remote,
			"-calling-aet", "MOVESCU",
			"-called-aet", "MOVESCP",
			"-move-destination", "DESTAE",
			"-level", "STUDY",
			"-study-uid", "1.2.study",
			"-timeout", "2s",
		}, &stdout, &stderr)
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive C-MOVE request")
	}
	cancel()
	select {
	case code := <-result:
		if code != 130 {
			t.Fatalf("runWithContext() exit = %d, want 130; stderr=%q", code, stderr.String())
		}
	case <-time.After(time.Second):
		t.Fatal("C-MOVE did not finish bounded cancellation")
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !strings.Contains(stdout.String(), "C-MOVE canceled with status=0xFE00") {
		t.Fatalf("stdout = %q, want canceled final status", stdout.String())
	}
}

func TestRunRetrieveTimeoutRemainsFailureAfterCancelFinal(t *testing.T) {
	remote, _, serverDone := startCancelingMoveSCP(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-remote", remote,
		"-calling-aet", "MOVESCU",
		"-called-aet", "MOVESCP",
		"-move-destination", "DESTAE",
		"-level", "STUDY",
		"-study-uid", "1.2.study",
		"-timeout", "50ms",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() exit = %d, want timeout failure 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if !strings.Contains(stdout.String(), "C-MOVE canceled with status=0xFE00") || !strings.Contains(stderr.String(), "deadline exceeded") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRetrieveReleaseUsesConfiguredTimeout(t *testing.T) {
	remote, serverDone := startMoveSCPStallingRelease(t, map[core.Tag]string{
		tagQueryRetrieveLevel: "STUDY",
		tagStudyInstanceUID:   "1.2.study",
	}, 700*time.Millisecond)

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := run([]string{
		"-remote", remote,
		"-calling-aet", "MOVESCU",
		"-called-aet", "MOVESCP",
		"-move-destination", "DESTAE",
		"-level", "STUDY",
		"-study-uid", "1.2.study",
		"-timeout", "150ms",
	}, &stdout, &stderr)
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("run() exit = %d, stderr=%q", code, stderr.String())
	}
	if elapsed >= 450*time.Millisecond {
		t.Fatalf("run() took %s, want release bounded by configured timeout", elapsed)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func startMoveSCP(t *testing.T, wantIdentifier map[core.Tag]string) (string, <-chan error) {
	return startMoveSCPForModel(t, dimse.StudyRootMoveSOPClassUID, wantIdentifier)
}

func startMoveSCPForModel(t *testing.T, moveSOPClassUID string, wantIdentifier map[core.Tag]string) (string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{moveSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()

		pc, err := dimse.AcceptedContextForSOPClass(assoc, moveSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := dimse.ReceiveCMoveRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if req.MoveDestination != "DESTAE" {
			serverDone <- errors.New("unexpected MoveDestination " + req.MoveDestination)
			return
		}
		identifier, err := dimse.ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if err := checkIdentifierStrings(identifier, wantIdentifier); err != nil {
			serverDone <- err
			return
		}
		if err := dimse.SendCMoveResponse(assoc, pc.ID, dimse.CMoveResponse{
			AffectedSOPClassUID:       moveSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
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

	return listener.Addr().String(), serverDone
}

func startGetSCP(t *testing.T, storageSOPClassUID string, instanceUIDs []string) (string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "GETSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dimse.StudyRootGetSOPClassUID, storageSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID, transfer.ExplicitVRLittleEndian.UID},
			RoleSelections:            []ul.RoleSelectionItem{{SopClassUID: storageSOPClassUID, SCPRole: true}},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		err = dimse.ServeStudyRootCGet(ctx, assoc, pc.ID, dimse.CGetHandlerFunc(func(_ context.Context, request dimse.CGetRequestContext) ([]dimse.CGetSubOperation, error) {
			if request.QueryRetrieveLevel != "STUDY" {
				return nil, errors.New("unexpected retrieve level " + request.QueryRetrieveLevel)
			}
			operations := make([]dimse.CGetSubOperation, 0, len(instanceUIDs))
			for _, uid := range instanceUIDs {
				uid := uid
				operations = append(operations, dimse.CGetSubOperation{
					AffectedSOPClassUID: storageSOPClassUID, AffectedSOPInstanceUID: uid,
					LoadDataSet: func(context.Context) (*object.Object, error) {
						return cGetDataSet(storageSOPClassUID, uid), nil
					},
				})
			}
			return operations, nil
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
	return listener.Addr().String(), serverDone
}

func startCancelingGetSCP(t *testing.T) (string, <-chan struct{}, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	const storageSOPClassUID = "1.2.840.10008.5.1.4.1.1.2"
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requestReceived := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "GETSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dimse.StudyRootGetSOPClassUID, storageSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID},
			RoleSelections:            []ul.RoleSelectionItem{{SopClassUID: storageSOPClassUID, SCPRole: true}},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		request, _, err := dimse.ReceiveCGetRequest(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
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
		if err := dimse.SendCGetResponse(assoc, pc.ID, dimse.CGetResponse{
			AffectedSOPClassUID:       dimse.StudyRootGetSOPClassUID,
			MessageIDBeingRespondedTo: request.MessageID,
			Status:                    dimse.StatusCGetCancel,
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
			serverDone <- errors.New("server expected A-RELEASE-RQ after C-CANCEL")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()
	return listener.Addr().String(), requestReceived, serverDone
}

func startCancelingMoveSCP(t *testing.T) (string, <-chan struct{}, <-chan error) {
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
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dimse.StudyRootMoveSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.StudyRootMoveSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		request, err := dimse.ReceiveCMoveRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := dimse.ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian); err != nil {
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
		if err := dimse.SendCMoveResponse(assoc, pc.ID, dimse.CMoveResponse{
			AffectedSOPClassUID:       dimse.StudyRootMoveSOPClassUID,
			MessageIDBeingRespondedTo: request.MessageID,
			Status:                    dimse.StatusCGetCancel,
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
			serverDone <- errors.New("server expected A-RELEASE-RQ after C-CANCEL")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()
	return listener.Addr().String(), requestReceived, serverDone
}

func cGetDataSet(sopClassUID, instanceUID string) *object.Object {
	return object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{sopClassUID}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{instanceUID}},
	}, std.Dictionary)
}

func startMoveSCPStallingRelease(t *testing.T, wantIdentifier map[core.Tag]string, stall time.Duration) (string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{studyRootMoveUID},
			SupportedTransferSyntaxes: []string{transfer.ImplicitVRLittleEndian.UID},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()

		pc, err := dimse.AcceptedContextForSOPClass(assoc, studyRootMoveUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := dimse.ReceiveCMoveRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		identifier, err := dimse.ReceiveDataSet(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if err := checkIdentifierStrings(identifier, wantIdentifier); err != nil {
			serverDone <- err
			return
		}
		if err := dimse.SendCMoveResponse(assoc, pc.ID, dimse.CMoveResponse{
			AffectedSOPClassUID:       studyRootMoveUID,
			MessageIDBeingRespondedTo: req.MessageID,
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
		timer := time.NewTimer(stall)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			serverDone <- ctx.Err()
		case <-timer.C:
			serverDone <- nil
		}
	}()

	return listener.Addr().String(), serverDone
}

func assertIdentifierStrings(t *testing.T, obj *object.Object, want map[core.Tag]string) {
	t.Helper()
	if err := checkIdentifierStrings(obj, want); err != nil {
		t.Fatal(err)
	}
}

func checkIdentifierStrings(obj *object.Object, want map[core.Tag]string) error {
	for tag, wantValue := range want {
		got, ok := obj.GetString(tag)
		if !ok {
			return errors.New("missing identifier tag " + tag.String())
		}
		if got != wantValue {
			return errors.New("identifier " + tag.String() + " = " + got + ", want " + wantValue)
		}
	}
	return nil
}
