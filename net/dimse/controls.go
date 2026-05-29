package dimse

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

var (
	ErrCancelGraceExceeded = errors.New("dicom dimse: cancel grace exceeded")
	ErrSCPHandlerPanic     = errors.New("dicom dimse: SCP handler panic")
)

const defaultSCPFailureResponseTimeout = time.Second

// SCPControls configures DIMSE phase limits. Progress timeouts renew whenever
// bytes move; OperationTimeout is the only absolute limit. Zero values preserve
// existing behavior.
type SCPControls struct {
	CommandProgressTimeout time.Duration
	DataSetProgressTimeout time.Duration
	OperationTimeout       time.Duration
	CancelGrace            time.Duration
}

type scpControlsContextKey struct{}
type scpResponseContextKey struct{}

func (c SCPControls) validate() error {
	if c.CommandProgressTimeout < 0 {
		return fmt.Errorf("dicom dimse: command progress timeout must not be negative")
	}
	if c.DataSetProgressTimeout < 0 {
		return fmt.Errorf("dicom dimse: dataset progress timeout must not be negative")
	}
	if c.OperationTimeout < 0 {
		return fmt.Errorf("dicom dimse: operation timeout must not be negative")
	}
	if c.CancelGrace < 0 {
		return fmt.Errorf("dicom dimse: cancel grace must not be negative")
	}
	return nil
}

func withSCPControls(ctx context.Context, controls SCPControls) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scpControlsContextKey{}, controls)
}

func scpControlsFromContext(ctx context.Context) SCPControls {
	if ctx == nil {
		return SCPControls{}
	}
	controls, _ := ctx.Value(scpControlsContextKey{}).(SCPControls)
	return controls
}

func withSCPResponseContext(ctx, responseCtx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if responseCtx == nil {
		responseCtx = context.Background()
	}
	return context.WithValue(ctx, scpResponseContextKey{}, responseCtx)
}

// scpResponseContext lets a timed-out/canceled handler still attempt the final
// DIMSE status through the association/server context. Phase progress limits
// remain attached, but the completed handler's absolute deadline is not reused.
func scpResponseContext(ctx context.Context, assoc *ul.Association) (context.Context, context.CancelFunc) {
	controls := scpControlsFromContext(ctx)
	if ctx != nil {
		if ctx.Err() == nil {
			return ctx, func() {}
		}
		if responseCtx, ok := ctx.Value(scpResponseContextKey{}).(context.Context); ok && responseCtx != nil {
			responseTimeout := controls.CancelGrace
			if responseTimeout <= 0 {
				responseTimeout = defaultSCPFailureResponseTimeout
			}
			bounded, cancel := context.WithTimeout(responseCtx, responseTimeout)
			return withSCPControls(bounded, controls), cancel
		}
	}
	responseCtx := context.Background()
	if assoc != nil && assoc.Context != nil {
		responseCtx = assoc.Context
	}
	responseTimeout := controls.CancelGrace
	if responseTimeout <= 0 {
		responseTimeout = defaultSCPFailureResponseTimeout
	}
	bounded, cancel := context.WithTimeout(responseCtx, responseTimeout)
	return withSCPControls(bounded, controls), cancel
}

func sendWithSCPResponseContext(ctx context.Context, assoc *ul.Association, send func(context.Context) error) error {
	responseCtx, cancel := scpResponseContext(ctx, assoc)
	defer cancel()
	return send(responseCtx)
}

func commandReadContext(ctx context.Context) context.Context {
	timeout := scpControlsFromContext(ctx).CommandProgressTimeout
	if timeout <= 0 {
		return ctx
	}
	return ul.WithReadProgressTimeout(ctx, timeout)
}

func commandWriteContext(ctx context.Context) context.Context {
	timeout := scpControlsFromContext(ctx).CommandProgressTimeout
	if timeout <= 0 {
		return ctx
	}
	return ul.WithWriteProgressTimeout(ctx, timeout)
}

func dataSetReadContext(ctx context.Context, assoc *ul.Association) context.Context {
	if ctx == nil && assoc != nil {
		ctx = assoc.Context
	}
	ctx = ul.WithActiveTransfer(ctx)
	timeout := scpControlsFromContext(ctx).DataSetProgressTimeout
	if timeout <= 0 {
		return ctx
	}
	return ul.WithReadProgressTimeout(ctx, timeout)
}

func dataSetWriteContext(ctx context.Context) context.Context {
	timeout := scpControlsFromContext(ctx).DataSetProgressTimeout
	if timeout <= 0 {
		return ctx
	}
	return ul.WithWriteProgressTimeout(ctx, timeout)
}

type scpHandlerResult[T any] struct {
	value T
	err   error
}

// runSCPHandler adds a bounded cooperative-cancel grace only when explicitly
// configured. A callback that ignores its context cannot be killed by Go; once
// grace expires the association is aborted/closed and the detached callback can
// no longer use the protocol stream.
func runSCPHandler[T any](ctx context.Context, assoc *ul.Association, grace time.Duration, handler func(context.Context) (T, error)) (T, error) {
	if grace <= 0 {
		return callSCPHandlerSafely(ctx, handler)
	}
	results := make(chan scpHandlerResult[T], 1)
	go func() {
		value, err := callSCPHandlerSafely(ctx, handler)
		results <- scpHandlerResult[T]{value: value, err: err}
	}()
	select {
	case result := <-results:
		return result.value, result.err
	case <-ctx.Done():
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.value, result.err
	case <-timer.C:
		abortCtx, cancelAbort := context.WithTimeout(context.Background(), 100*time.Millisecond)
		if assoc != nil {
			_ = assoc.AbortWithContext(abortCtx, ul.AbortReasonNotSpecified)
		}
		cancelAbort()
		var zero T
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		return zero, errors.Join(cause, ErrCancelGraceExceeded)
	}
}

func callSCPHandlerSafely[T any](ctx context.Context, handler func(context.Context) (T, error)) (value T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = ErrSCPHandlerPanic
		}
	}()
	return handler(ctx)
}
