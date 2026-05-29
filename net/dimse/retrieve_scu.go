package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// CMoveStatus represents the high-level class of a C-MOVE response status.
//
// Ref: DICOM PS3.7 C.4.2.2.4 (C-MOVE Status values).
// This is intentionally minimal.
type CMoveStatus int

const (
	CMoveStatusUnknown CMoveStatus = iota
	CMoveStatusPending
	CMoveStatusSuccess
	CMoveStatusCancel
	CMoveStatusWarning
	CMoveStatusFailure
)

func ClassifyCMoveStatus(status uint16) CMoveStatus {
	// Pending
	if status == 0xFF00 || status == 0xFF01 {
		return CMoveStatusPending
	}
	// Success
	if status == 0x0000 {
		return CMoveStatusSuccess
	}
	// Cancel
	if status == 0xFE00 {
		return CMoveStatusCancel
	}
	// Warnings: Bxxx
	if status&0xF000 == 0xB000 {
		return CMoveStatusWarning
	}
	// Failures: treat everything else as failure (including 0xAxxx and 0xCxxx).
	return CMoveStatusFailure
}

// CMoveProgress represents a C-MOVE response event. It can carry a pending
// update, a terminal response when Final is true, or a terminal error when Err
// is non-nil.
//
// Identifier is typically absent from pending responses for C-MOVE, but is
// included here for completeness.
type CMoveProgress struct {
	Response    *CMoveResponse
	Identifier  *object.Object
	Final       bool
	StatusClass CMoveStatus
	Err         error
}

// SendCMove performs a basic SCU C-MOVE exchange:
// - Sends the C-MOVE-RQ command set
// - Sends the Identifier dataset
// - Receives 1..N C-MOVE-RSP responses until a final (non-pending) response
//
// Cancellation is supported by canceling the provided context; this stops the
// receive loop between response reads and returns ctx.Err(). It does not emit a
// C-CANCEL request.
//
// The caller must supply the presentation context ID and the transfer syntax
// used to encode the Identifier dataset.
func SendCMove(ctx context.Context, assoc *ul.Association, pcID byte, req CMoveRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (*CMoveResponse, error) {
	ch, err := SendCMoveWithProgress(ctx, assoc, pcID, req, identifier, identifierSyntax)
	if err != nil {
		return nil, err
	}
	var last *CMoveResponse
	for p := range ch {
		if p.Err != nil {
			return nil, p.Err
		}
		if p.Response != nil {
			last = p.Response
		}
	}
	if last == nil {
		return nil, fmt.Errorf("dicom dimse: C-MOVE: no response received")
	}
	return last, nil
}

// SendCMoveWithOptions is like SendCMove but uses OperationOptions for
// cancellation, overall timeout, per-response timeout, and association cleanup
// policy after uncertain errors.
func SendCMoveWithOptions(options OperationOptions, assoc *ul.Association, pcID byte, req CMoveRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (*CMoveResponse, error) {
	ch, err := SendCMoveWithProgressWithOptions(options, assoc, pcID, req, identifier, identifierSyntax)
	if err != nil {
		return nil, err
	}
	var last *CMoveResponse
	for p := range ch {
		if p.Err != nil {
			return nil, p.Err
		}
		if p.Response != nil {
			last = p.Response
		}
	}
	if last == nil {
		return nil, fmt.Errorf("dicom dimse: C-MOVE: no response received")
	}
	return last, nil
}

// SendCMoveWithProgress is like SendCMove but returns a channel of progress
// events for each received response.
//
// The ctx.Done check is observed between response reads. Receive cancellation
// and per-response deadlines are available through
// SendCMoveWithProgressWithOptions.
//
// The returned channel is closed after a final response is received.
func SendCMoveWithProgress(ctx context.Context, assoc *ul.Association, pcID byte, req CMoveRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (<-chan CMoveProgress, error) {
	return SendCMoveWithProgressWithOptions(OperationOptions{Context: ctx}, assoc, pcID, req, identifier, identifierSyntax)
}

// SendCMoveWithProgressWithOptions is like SendCMoveWithProgress but uses
// OperationOptions for cancellation, overall timeout, per-response timeout, and
// association cleanup policy after uncertain errors.
func SendCMoveWithProgressWithOptions(options OperationOptions, assoc *ul.Association, pcID byte, req CMoveRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (<-chan CMoveProgress, error) {
	options = operationOptionsWithDefaultPolicy(options, OperationErrorPolicyAbort)

	ctx, cancel := operationContext(options)
	if identifier == nil {
		cancel()
		return nil, fmt.Errorf("dicom dimse: C-MOVE: identifier dataset is required")
	}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, newOperationError("C-MOVE", err, false)
	}

	releaseOperation, err := beginAssociationOperation(assoc)
	if err != nil {
		cancel()
		return nil, newOperationError("C-MOVE", err, false)
	}

	if err := SendCMoveRequest(assoc, pcID, req); err != nil {
		releaseOperation()
		cancel()
		return nil, applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-MOVE", err, true))
	}
	if err := ctx.Err(); err != nil {
		releaseOperation()
		cancel()
		return nil, applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-MOVE", err, true))
	}
	if err := SendDataSet(assoc, pcID, identifier, identifierSyntax); err != nil {
		releaseOperation()
		cancel()
		return nil, applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-MOVE", err, true))
	}

	out := make(chan CMoveProgress, 1)
	go func() {
		defer close(out)
		defer releaseOperation()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				err := applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-MOVE", ctx.Err(), true))
				emitCMoveProgress(ctx, out, CMoveProgress{Final: true, Err: err})
				return
			default:
			}

			responseCtx, responseCancel := operationResponseContext(ctx, options.ResponseTimeout)
			rsp, err := receiveCMoveResponseWithContext(responseCtx, assoc, pcID)
			responseCancel()
			if err != nil {
				emitCMoveProgress(ctx, out, CMoveProgress{Final: true, Err: applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-MOVE", err, true))})
				return
			}

			cls := ClassifyCMoveStatus(rsp.Status)
			final := cls != CMoveStatusPending
			if !emitCMoveProgress(ctx, out, CMoveProgress{Response: rsp, Identifier: rsp.Identifier, Final: final, StatusClass: cls}) {
				return
			}
			if final {
				return
			}
		}
	}()
	return out, nil
}

func emitCMoveProgress(ctx context.Context, out chan<- CMoveProgress, progress CMoveProgress) bool {
	select {
	case out <- progress:
		return true
	default:
	}
	select {
	case <-ctx.Done():
		return false
	case out <- progress:
		return true
	}
}
