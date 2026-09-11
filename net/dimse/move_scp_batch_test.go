package dimse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type cMoveBatchTestHandler struct {
	prepare func(context.Context, CMoveRequestContext) (CMoveBatch, error)
	move    func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error)
}

func (h cMoveBatchTestHandler) Move(ctx context.Context, req CMoveRequestContext) ([]CMoveSubOperation, error) {
	if h.move == nil {
		return nil, errors.New("legacy Move unexpectedly called")
	}
	return h.move(ctx, req)
}

func (h cMoveBatchTestHandler) PrepareCMoveBatch(ctx context.Context, req CMoveRequestContext) (CMoveBatch, error) {
	if h.prepare == nil {
		return CMoveBatch{}, errors.New("nil batch prepare")
	}
	return h.prepare(ctx, req)
}

func TestServePatientRootCMoveBatchReportsIncrementalCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runCalls := 0
	handler := cMoveBatchTestHandler{prepare: func(_ context.Context, req CMoveRequestContext) (CMoveBatch, error) {
		if req.Request.MoveDestination != "STOREAE" || req.QueryRetrieveLevel != QueryRetrieveLevelPatient || req.Identifier == nil {
			t.Errorf("request context = %#v", req)
		}
		return CMoveBatch{
			SubOperations: []CMoveSubOperationDescriptor{
				{AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.1"},
				{AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.2"},
				{AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.3"},
			},
			Run: func(ctx context.Context, yield func(CMoveSubOperationResult) error) error {
				runCalls++
				for _, result := range []CMoveSubOperationResult{
					{Status: StatusSuccess},
					{Status: 0xB007},
					{Status: StatusCMoveUnableToProcess, Err: errors.New("store failed")},
				} {
					if err := ctx.Err(); err != nil {
						return err
					}
					if err := yield(result); err != nil {
						return err
					}
				}
				return nil
			},
		}, nil
	}}

	responses, serverErr := runPatientRootCMoveProgressTest(t, ctx, handler)
	if serverErr != nil {
		t.Fatalf("ServePatientRootCMove() error = %v", serverErr)
	}
	if runCalls != 1 {
		t.Fatalf("Run calls = %d, want 1", runCalls)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %d, want two pending and one final", len(responses))
	}
	assertCMoveCounts(t, "first pending", responses[0], StatusPending, 2, 1, 0, 0)
	assertCMoveCounts(t, "second pending", responses[1], StatusPending, 1, 1, 0, 1)
	assertCMoveCounts(t, "final", responses[2], StatusCMoveSubOperationsCompleteOneOrMoreFailures, 0, 1, 1, 1)
}

func TestServeStudyRootCMoveBatchRejectsYieldCardinalityViolations(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, func(CMoveSubOperationResult) error) error
	}{
		{
			name: "extra",
			run: func(_ context.Context, yield func(CMoveSubOperationResult) error) error {
				if err := yield(CMoveSubOperationResult{Status: StatusSuccess}); err != nil {
					return err
				}
				_ = yield(CMoveSubOperationResult{Status: StatusSuccess})
				return nil
			},
		},
		{
			name: "insufficient",
			run: func(_ context.Context, yield func(CMoveSubOperationResult) error) error {
				return yield(CMoveSubOperationResult{Status: StatusSuccess})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			descriptors := []CMoveSubOperationDescriptor{{
				AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.1",
			}}
			if test.name == "insufficient" {
				descriptors = append(descriptors, CMoveSubOperationDescriptor{
					AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.2",
				})
			}
			status, serverErr := runStudyRootCMoveStatusTest(t, ctx, cMoveBatchTestHandler{prepare: func(context.Context, CMoveRequestContext) (CMoveBatch, error) {
				return CMoveBatch{SubOperations: descriptors, Run: test.run}, nil
			}})
			if status != StatusCMoveUnableToProcess {
				t.Fatalf("final status = 0x%04X, want unable to process", status)
			}
			if !errors.Is(serverErr, ErrCMoveBatchContract) {
				t.Fatalf("server error = %v, want ErrCMoveBatchContract", serverErr)
			}
		})
	}
}

func TestServeStudyRootCMoveBatchHonorsInterleavedCCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: StudyRootMoveSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeStudyRootCMove(ctx, local, 1, cMoveBatchTestHandler{prepare: func(context.Context, CMoveRequestContext) (CMoveBatch, error) {
			return CMoveBatch{
				SubOperations: []CMoveSubOperationDescriptor{
					{AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.1"},
					{AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", AffectedSOPInstanceUID: "1.2.3.2"},
				},
				Run: func(ctx context.Context, yield func(CMoveSubOperationResult) error) error {
					if err := yield(CMoveSubOperationResult{Status: StatusSuccess}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				},
			}, nil
		}})
	}()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := CMoveRequest{AffectedSOPClassUID: StudyRootMoveSOPClassUID, MessageID: 81, MoveDestination: "STOREAE"}
	if err := SendCMoveRequest(peer, 1, request); err != nil {
		t.Fatal(err)
	}
	if err := SendDataSet(peer, 1, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	pending, err := ReceiveCMoveResponse(peer, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertCMoveCounts(t, "pending", pending, StatusPending, 1, 1, 0, 0)
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: request.MessageID}); err != nil {
		t.Fatal(err)
	}
	final, err := ReceiveCMoveResponse(peer, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertCMoveCounts(t, "cancel", final, StatusCMoveCancel, 1, 1, 0, 0)
	if err := <-serverDone; !errors.Is(err, ErrCMoveCanceled) {
		t.Fatalf("ServeStudyRootCMove() error = %v, want ErrCMoveCanceled", err)
	}
}

func TestValidateCMoveBatchRejectsLimitsUIDsAndMissingRun(t *testing.T) {
	validRun := func(context.Context, func(CMoveSubOperationResult) error) error { return nil }
	tests := []struct {
		name        string
		descriptors []CMoveSubOperationDescriptor
		run         func(context.Context, func(CMoveSubOperationResult) error) error
		contains    string
	}{
		{name: "missing run", run: nil, contains: "missing Run"},
		{name: "invalid class UID", descriptors: []CMoveSubOperationDescriptor{{AffectedSOPClassUID: "3.1", AffectedSOPInstanceUID: "1.2.3"}}, run: validRun, contains: "Class UID"},
		{name: "invalid instance UID", descriptors: []CMoveSubOperationDescriptor{{AffectedSOPClassUID: "1.2.3", AffectedSOPInstanceUID: "1.40"}}, run: validRun, contains: "Instance UID"},
		{name: "count overflow", descriptors: make([]CMoveSubOperationDescriptor, maxCMoveSubOperations+1), run: validRun, contains: "too many"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCMoveBatch(test.descriptors, test.run)
			if !errors.Is(err, ErrCMoveBatchContract) || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("validateCMoveBatch() error = %v, want contract error containing %q", err, test.contains)
			}
		})
	}
}
