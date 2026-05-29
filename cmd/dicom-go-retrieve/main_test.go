package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
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
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()

	return listener.Addr().String(), serverDone
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
