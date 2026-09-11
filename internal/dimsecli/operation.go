// Package dimsecli contains DIMSE operation orchestration shared by CLI tools.
package dimsecli

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/ThalesMMS/dicom-go/net/dimse"
)

const ExitCanceled = 130

// OperationReader keeps draining an AsyncOperation after its operation context
// is canceled. AsyncSession sends C-CANCEL exactly once; this reader gives the
// peer a bounded interval to return the terminal response.
type OperationReader struct {
	operation    *dimse.AsyncOperation
	operationCtx context.Context
	drainTimeout time.Duration
	drainCtx     context.Context
	drainCancel  context.CancelFunc
	draining     bool
	cause        error
}

func NewOperationReader(operationCtx context.Context, operation *dimse.AsyncOperation, drainTimeout time.Duration) *OperationReader {
	if operationCtx == nil {
		operationCtx = context.Background()
	}
	if drainTimeout <= 0 {
		drainTimeout = 5 * time.Second
	}
	return &OperationReader{operation: operation, operationCtx: operationCtx, drainTimeout: drainTimeout}
}

func (reader *OperationReader) Close() {
	if reader != nil && reader.drainCancel != nil {
		reader.drainCancel()
	}
}

func (reader *OperationReader) Cause() error {
	if reader == nil {
		return nil
	}
	return reader.cause
}

func (reader *OperationReader) Next() (dimse.AsyncMessage, error) {
	if reader == nil || reader.operation == nil {
		return dimse.AsyncMessage{}, dimse.ErrAsyncSessionClosed
	}
	for {
		waitCtx := reader.operationCtx
		if reader.draining {
			waitCtx = reader.drainCtx
		}
		message, err := reader.operation.Next(waitCtx)
		if err == nil || errors.Is(err, io.EOF) {
			return message, err
		}
		if reader.draining || reader.operationCtx.Err() == nil {
			return dimse.AsyncMessage{}, err
		}
		reader.cause = reader.operationCtx.Err()
		reader.drainCtx, reader.drainCancel = context.WithTimeout(context.Background(), reader.drainTimeout)
		reader.draining = true
		controlCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cancelErr := reader.operation.Cancel(controlCtx)
		cancel()
		if cancelErr != nil &&
			!errors.Is(cancelErr, dimse.ErrAsyncOperationComplete) &&
			!errors.Is(cancelErr, dimse.ErrAsyncSessionClosed) &&
			!errors.Is(cancelErr, net.ErrClosed) {
			return dimse.AsyncMessage{}, errors.Join(reader.cause, cancelErr)
		}
	}
}

func IsCanceled(ctx context.Context, cause error) bool {
	return errors.Is(cause, context.Canceled) || ctx != nil && errors.Is(ctx.Err(), context.Canceled)
}
