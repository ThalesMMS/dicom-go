package dimse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

func TestAssociationOperationGuardRejectsConcurrentOperation(t *testing.T) {
	assoc := &ul.Association{}

	release, err := beginAssociationOperation(assoc)
	if err != nil {
		t.Fatalf("beginAssociationOperation() error = %v", err)
	}
	defer release()

	if _, err := beginAssociationOperation(assoc); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("beginAssociationOperation() error = %v, want ErrOperationInProgress", err)
	}

	release()
	release = func() {}

	releaseAgain, err := beginAssociationOperation(assoc)
	if err != nil {
		t.Fatalf("beginAssociationOperation() after release error = %v", err)
	}
	releaseAgain()
}

func TestAssociationOperationGuardIsAssociationLocal(t *testing.T) {
	var releases []func()
	for i := 0; i < 25; i++ {
		release, err := beginAssociationOperation(&ul.Association{})
		if err != nil {
			t.Fatalf("beginAssociationOperation(%d) error = %v", i, err)
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
}

func waitForAssociationOperationRelease(t *testing.T, assoc *ul.Association) {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		release, err := beginAssociationOperation(assoc)
		if err == nil {
			release()
			return
		}
		if !errors.Is(err, ErrOperationInProgress) {
			t.Fatalf("beginAssociationOperation() error = %v, want ErrOperationInProgress until release", err)
		}
		select {
		case <-deadline:
			t.Fatal("association operation guard was not released after context cancellation")
		case <-ticker.C:
		}
	}
}

func TestOperationContextAppliesOverallTimeout(t *testing.T) {
	ctx, cancel := operationContext(OperationOptions{OverallTimeout: time.Nanosecond})
	defer cancel()

	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("operation context error = %v, want context.DeadlineExceeded", ctx.Err())
	}
}

func TestOperationOptionsAndResponseContextDefaults(t *testing.T) {
	options := operationOptionsWithDefaultPolicy(OperationOptions{}, OperationErrorPolicyAbort)
	if options.ErrorPolicy != OperationErrorPolicyAbort {
		t.Fatalf("defaulted ErrorPolicy = %v, want abort", options.ErrorPolicy)
	}

	ctx := context.Background()
	respCtx, cancel := operationResponseContext(ctx, 0)
	defer cancel()
	if respCtx != ctx {
		t.Fatal("operationResponseContext(timeout=0) should return the original context")
	}

	respCtx, cancel = operationResponseContext(ctx, time.Nanosecond)
	defer cancel()
	<-respCtx.Done()
	if !errors.Is(respCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("response context error = %v, want context.DeadlineExceeded", respCtx.Err())
	}
}

func TestOperationErrorWrappingAndClassification(t *testing.T) {
	if got := newOperationError("C-FIND", nil, true); got != nil {
		t.Fatalf("newOperationError(nil) = %v, want nil", got)
	}

	err := newOperationError("C-FIND", context.Canceled, true)
	if !errors.Is(err, ErrOperationCanceled) {
		t.Fatalf("newOperationError(context.Canceled) = %v, want ErrOperationCanceled", err)
	}
	if !errors.Is(err, ErrAssociationStateUncertain) {
		t.Fatalf("newOperationError(uncertain) = %v, want ErrAssociationStateUncertain", err)
	}
	if !strings.Contains(err.Error(), "C-FIND") || !strings.Contains(err.Error(), "association state uncertain") {
		t.Fatalf("OperationError.Error() = %q", err.Error())
	}

	err = newOperationError("", ul.ErrAssociationTimeout, false)
	if !errors.Is(err, ErrOperationTimeout) {
		t.Fatalf("newOperationError(timeout) = %v, want ErrOperationTimeout", err)
	}
	if !strings.Contains(err.Error(), "DIMSE operation") {
		t.Fatalf("default operation name error = %q", err.Error())
	}

	var opErr *OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error type = %T, want *OperationError", err)
	}
	if opErr.Unwrap() == nil {
		t.Fatal("OperationError.Unwrap() = nil, want wrapped cause")
	}
	var nilOpErr *OperationError
	if nilOpErr.Error() != "<nil>" || nilOpErr.Unwrap() != nil || nilOpErr.Is(ErrAssociationStateUncertain) {
		t.Fatal("nil OperationError should format as <nil>, unwrap nil, and not match uncertainty")
	}

	if got := applyOperationErrorPolicy(nil, OperationErrorPolicyAbort, err); got != err {
		t.Fatalf("applyOperationErrorPolicy(nil assoc) = %v, want original error", got)
	}
	if got := applyOperationErrorPolicy(nil, OperationErrorPolicyAbort, nil); got != nil {
		t.Fatalf("applyOperationErrorPolicy(nil error) = %v, want nil", got)
	}
}
