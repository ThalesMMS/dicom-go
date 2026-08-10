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
}
