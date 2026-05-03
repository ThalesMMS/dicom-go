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

// SendCMoveWithProgress is like SendCMove but returns a channel of progress
// events for each received response.
//
// The ctx.Done check is observed between ReceiveCMoveResponse calls. It does
// not abort a currently blocking ReceiveCMoveResponse call because the
// underlying transport API does not support receive cancellation.
//
// The returned channel is closed after a final response is received.
func SendCMoveWithProgress(ctx context.Context, assoc *ul.Association, pcID byte, req CMoveRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (<-chan CMoveProgress, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if identifier == nil {
		return nil, fmt.Errorf("dicom dimse: C-MOVE: identifier dataset is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := SendCMoveRequest(assoc, pcID, req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = abortCMoveAssociation(assoc, err)
		return nil, err
	}
	if err := SendDataSet(assoc, pcID, identifier, identifierSyntax); err != nil {
		return nil, abortCMoveAssociation(assoc, err)
	}

	out := make(chan CMoveProgress, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				err := abortCMoveAssociation(assoc, ctx.Err())
				out <- CMoveProgress{Final: true, Err: err}
				return
			default:
			}

			rsp, err := ReceiveCMoveResponse(assoc, pcID)
			if err != nil {
				out <- CMoveProgress{Final: true, Err: abortCMoveAssociation(assoc, err)}
				return
			}

			cls := ClassifyCMoveStatus(rsp.Status)
			final := cls != CMoveStatusPending
			out <- CMoveProgress{Response: rsp, Identifier: rsp.Identifier, Final: final, StatusClass: cls}
			if final {
				return
			}
		}
	}()
	return out, nil
}

func abortCMoveAssociation(assoc *ul.Association, err error) error {
	if assoc == nil {
		return err
	}
	if abortErr := assoc.Abort(ul.AbortReasonNotSpecified); abortErr != nil {
		return fmt.Errorf("%w; abort association failed: %v", err, abortErr)
	}
	return err
}
