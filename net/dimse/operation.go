package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

var (
	// ErrOperationInProgress indicates that an association already has an
	// active DIMSE operation owned by this package.
	ErrOperationInProgress = errors.New("dicom dimse: operation already in progress")
	// ErrOperationCanceled marks local operation cancellation.
	ErrOperationCanceled = errors.New("dicom dimse: operation canceled")
	// ErrOperationTimeout marks an overall operation or per-response timeout.
	ErrOperationTimeout = errors.New("dicom dimse: operation timeout")
	// ErrAssociationStateUncertain marks an error after a DIMSE command or
	// dataset may already have been exchanged on the association.
	ErrAssociationStateUncertain = errors.New("dicom dimse: association state uncertain")
)

// OperationErrorPolicy controls what the executor does to an association after
// an error that leaves the operation state uncertain.
type OperationErrorPolicy int

const (
	// OperationErrorPolicyDefault uses the helper's default cleanup policy.
	OperationErrorPolicyDefault OperationErrorPolicy = iota
	// OperationErrorPolicyLeaveOpen leaves Release or Abort to the caller.
	OperationErrorPolicyLeaveOpen
	// OperationErrorPolicyRelease attempts a normal A-RELEASE after the error.
	OperationErrorPolicyRelease
	// OperationErrorPolicyAbort sends A-ABORT and closes the association.
	OperationErrorPolicyAbort
)

// OperationOptions configures the shared single-operation DIMSE executor used
// by SCU helpers in this package.
type OperationOptions struct {
	// Context controls cancellation. Nil means context.Background().
	Context context.Context
	// OverallTimeout bounds the whole operation when greater than zero.
	OverallTimeout time.Duration
	// ResponseTimeout bounds each response receive when greater than zero.
	ResponseTimeout time.Duration
	// ErrorPolicy controls association cleanup after uncertain operation errors.
	ErrorPolicy OperationErrorPolicy
}

// OperationError wraps DIMSE operation failures with operation name and
// association-state metadata while preserving errors.Is/As for the cause.
type OperationError struct {
	Operation                 string
	Err                       error
	AssociationStateUncertain bool
}

func (e *OperationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	prefix := "DIMSE operation"
	if e.Operation != "" {
		prefix = e.Operation
	}
	if e.AssociationStateUncertain {
		return fmt.Sprintf("dicom dimse: %s: %v; association state uncertain", prefix, e.Err)
	}
	return fmt.Sprintf("dicom dimse: %s: %v", prefix, e.Err)
}

func (e *OperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *OperationError) Is(target error) bool {
	if target == ErrAssociationStateUncertain && e != nil && e.AssociationStateUncertain {
		return true
	}
	return false
}

func beginAssociationOperation(assoc *ul.Association) (func(), error) {
	if assoc == nil {
		return func() {}, nil
	}
	if !assoc.TryBeginOperation() {
		return nil, ErrOperationInProgress
	}
	var once sync.Once
	return func() {
		once.Do(assoc.EndOperation)
	}, nil
}

func operationContext(options OperationOptions) (context.Context, context.CancelFunc) {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if options.OverallTimeout > 0 {
		return context.WithTimeout(ctx, options.OverallTimeout)
	}
	return ctx, func() {}
}

func operationOptionsWithDefaultPolicy(options OperationOptions, defaultPolicy OperationErrorPolicy) OperationOptions {
	if options.ErrorPolicy == OperationErrorPolicyDefault {
		options.ErrorPolicy = defaultPolicy
	}
	return options
}

func operationResponseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func newOperationError(operation string, err error, uncertain bool) error {
	if err == nil {
		return nil
	}
	return &OperationError{
		Operation:                 operation,
		Err:                       classifyOperationError(err),
		AssociationStateUncertain: uncertain,
	}
}

func classifyOperationError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		if errors.Is(err, ErrOperationCanceled) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrOperationCanceled, err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ul.ErrAssociationTimeout):
		if errors.Is(err, ErrOperationTimeout) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrOperationTimeout, err)
	default:
		return err
	}
}

func applyOperationErrorPolicy(assoc *ul.Association, policy OperationErrorPolicy, err error) error {
	if err == nil {
		return nil
	}
	if assoc == nil {
		return err
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
	defer cancelCleanup()
	switch policy {
	case OperationErrorPolicyRelease:
		if releaseErr := assoc.Release(cleanupCtx); releaseErr != nil {
			return fmt.Errorf("%w; release association failed: %v", err, releaseErr)
		}
	case OperationErrorPolicyAbort:
		if abortErr := assoc.AbortWithContext(cleanupCtx, ul.AbortReasonNotSpecified); abortErr != nil {
			return fmt.Errorf("%w; abort association failed: %v", err, abortErr)
		}
	}
	return err
}
