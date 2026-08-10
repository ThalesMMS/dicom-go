package ups_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/ups"
)

func TestServiceClaimsScheduledStepAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{Clock: func() time.Time {
		return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	attributes := scheduledAttributes(t)
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.1", Attributes: attributes}); err != nil {
		t.Fatal(err)
	}

	type claimResult struct {
		step ups.Step
		err  error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 32)
	var group sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			step, claimErr := service.ChangeState(ctx, ups.ChangeStateRequest{
				SOPInstanceUID: "1.2.826.0.1.3680043.10.543.1",
				State:          ups.StateInProgress,
				TransactionUID: fmt.Sprintf("1.2.826.0.1.3680043.10.543.9.%d", index+1),
			})
			results <- claimResult{step: step, err: claimErr}
		}(index)
	}
	close(start)
	group.Wait()
	close(results)

	winners := 0
	for result := range results {
		if result.err == nil {
			winners++
			if result.step.State != ups.StateInProgress || result.step.TransactionUID == "" {
				t.Fatalf("winning claim = %#v", result.step)
			}
			continue
		}
		if !ups.IsStatus(result.err, ups.StatusIncorrectTransactionUID) && !errors.Is(result.err, ups.ErrConcurrentUpdate) {
			t.Fatalf("losing claim error = %v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d, want 1", winners)
	}
}

func TestServiceEnforcesTransactionUIDAndFinalImmutability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t)
	uid := "1.2.826.0.1.3680043.10.543.2"
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeState(ctx, ups.ChangeStateRequest{SOPInstanceUID: uid, State: ups.StateInProgress}); !ups.IsStatus(err, ups.StatusIncorrectTransactionUID) {
		t.Fatalf("claim without Transaction UID error = %v", err)
	}
	const transactionUID = "1.2.826.0.1.3680043.10.543.200"
	if _, err := service.ChangeState(ctx, ups.ChangeStateRequest{SOPInstanceUID: uid, State: ups.StateInProgress, TransactionUID: transactionUID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Set(ctx, ups.SetRequest{SOPInstanceUID: uid, TransactionUID: "1.2.3", Modifications: progressAttributes("10")}); !ups.IsStatus(err, ups.StatusIncorrectTransactionUID) {
		t.Fatalf("set with stale Transaction UID error = %v", err)
	}
	if _, err := service.Set(ctx, ups.SetRequest{SOPInstanceUID: uid, TransactionUID: transactionUID, Modifications: completedAttributes()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeState(ctx, ups.ChangeStateRequest{SOPInstanceUID: uid, State: ups.StateCompleted, TransactionUID: transactionUID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Set(ctx, ups.SetRequest{SOPInstanceUID: uid, TransactionUID: transactionUID, Modifications: progressAttributes("90")}); !ups.IsStatus(err, ups.StatusMayNoLongerBeUpdated) {
		t.Fatalf("set after completion error = %v", err)
	}
	if _, err := service.ChangeState(ctx, ups.ChangeStateRequest{SOPInstanceUID: uid, State: ups.StateCompleted, TransactionUID: transactionUID}); !ups.IsStatus(err, ups.StatusAlreadyCompleted) {
		t.Fatalf("repeat completion error = %v", err)
	}
}

func TestCancelRequestDoesNotCancelInProgressStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t)
	uid := "1.2.826.0.1.3680043.10.543.3"
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeState(ctx, ups.ChangeStateRequest{
		SOPInstanceUID: uid,
		State:          ups.StateInProgress,
		TransactionUID: "1.2.826.0.1.3680043.10.543.300",
	}); err != nil {
		t.Fatal(err)
	}
	step, err := service.RequestCancel(ctx, ups.CancelRequest{SOPInstanceUID: uid, RequestingAETitle: "REQUESTOR"})
	if err != nil {
		t.Fatal(err)
	}
	if step.State != ups.StateInProgress {
		t.Fatalf("state after cancel request = %s, want %s", step.State, ups.StateInProgress)
	}
	events, err := service.Events(ctx, ups.EventQuery{SOPInstanceUID: uid, Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[len(events)-1].Type != ups.EventCancelRequested {
		t.Fatalf("events after cancel request = %#v", events)
	}
}

func scheduledAttributes(t *testing.T) *object.Object {
	t.Helper()
	attributes, err := ups.BuildScheduledStep(ups.ScheduledStepAttributes{
		Priority:            "MEDIUM",
		ProcedureStepLabel:  "VERIFY",
		WorklistLabel:       "RADIOLOGY",
		StartDateTime:       "20260808120000",
		InputReadinessState: "READY",
	})
	if err != nil {
		t.Fatal(err)
	}
	return attributes
}

func progressAttributes(progress string) *object.Object {
	return ups.NewDataSet(core.Element{
		Header: core.ElementHeader{Tag: ups.TagProcedureStepProgressInformationSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{
			{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: ups.TagProcedureStepProgress, VR: core.VRDS}, Value: core.StringValue{progress}},
			}},
		}},
	})
}

func completedAttributes() *object.Object {
	attributes, err := ups.BuildPerformedProcedure(ups.PerformedProcedureAttributes{
		Station:       ups.Code{Value: "STATION", Scheme: "99TEST", Meaning: "Test station"},
		Workitem:      ups.Code{Value: "VERIFY", Scheme: "99TEST", Meaning: "Verify"},
		StartDateTime: "20260808120100",
		EndDateTime:   "20260808120200",
	})
	if err != nil {
		panic(err)
	}
	return attributes
}

func newTestService(t *testing.T) *ups.Service {
	t.Helper()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
