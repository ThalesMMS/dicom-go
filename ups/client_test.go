package ups_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/ups"
)

func TestPresentationContextsAreStableClonedAndDeduplicated(t *testing.T) {
	t.Parallel()
	contexts, err := ups.PresentationContexts(ups.WatchSOPClassUID, ups.PushSOPClassUID, ups.WatchSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 2 || contexts[0].AbstractSyntaxUID != ups.WatchSOPClassUID || contexts[1].AbstractSyntaxUID != ups.PushSOPClassUID {
		t.Fatalf("contexts = %#v", contexts)
	}
	contexts[0].TransferSyntaxUIDs[0] = "changed"
	again, err := ups.PresentationContexts(ups.WatchSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].TransferSyntaxUIDs[0] == "changed" {
		t.Fatal("presentation context transfer syntaxes alias caller state")
	}
	if _, err := ups.PresentationContext("1.2.3"); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("invalid context error = %v", err)
	}
}

func TestClientRejectsOperationOnWrongUPSModelBeforeAssociation(t *testing.T) {
	t.Parallel()
	client := ups.NewClient(nil)
	if _, err := client.Get(context.Background(), ups.EventSOPClassUID, "1.2.3", nil); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("Get wrong model error = %v", err)
	}
	if _, err := client.RequestCancel(context.Background(), ups.PullSOPClassUID, "1.2.3", nil); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("Cancel wrong model error = %v", err)
	}
	if _, err := client.SubscribeFiltered(context.Background(), "WATCHER", false, nil); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("SubscribeFiltered empty filter error = %v", err)
	}
	if _, err := client.SubscribeFiltered(context.Background(), "WATCHER", false, map[string][]string{
		ups.TagSOPInstanceUID.String(): {"1.2.*"},
	}); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("SubscribeFiltered invalid operator error = %v", err)
	}
}

func TestBuildDiscontinuationProgressProducesFinalStateMacro(t *testing.T) {
	t.Parallel()
	dataSet, err := ups.BuildDiscontinuationProgress(ups.Code{
		Value: "DISCONTINUE", Scheme: "99TEST", Meaning: "Operator discontinued",
	}, " 20260830123000+0000 ")
	if err != nil {
		t.Fatal(err)
	}
	items, ok := dataSet.GetSequence(ups.TagProcedureStepProgressInformationSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("progress sequence = %#v, %t", items, ok)
	}
	if got, ok := items[0].GetString(ups.TagProcedureStepCancellationDateTime); !ok || got != "20260830123000+0000" {
		t.Fatalf("CancellationDateTime = %q, %t", got, ok)
	}
	reasons, ok := items[0].GetSequence(ups.TagProcedureStepDiscontinuationReasonCodeSequence)
	if !ok || len(reasons) != 1 {
		t.Fatalf("reason sequence = %#v, %t", reasons, ok)
	}
	if _, err := ups.BuildDiscontinuationProgress(ups.Code{}, "20260830123000+0000"); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("empty reason error = %v", err)
	}
	if _, err := ups.BuildDiscontinuationProgress(ups.Code{Value: "X", Scheme: "99TEST", Meaning: "X"}, "not-a-DT"); !errors.Is(err, ups.ErrInvalidDataSet) {
		t.Fatalf("invalid datetime error = %v", err)
	}
}
