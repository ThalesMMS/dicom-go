package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// FindResult represents one C-FIND response.
//
// For pending responses, Identifier will typically be present.
// For the final response (success or failure), Identifier is typically absent.
//
// The raw response is included for detailed inspection (Status, ErrorComment, etc.).
type FindResult struct {
	// Response is the decoded C-FIND-RSP command.
	Response *CFindResponse
	// Identifier is the decoded response Identifier dataset (if present).
	Identifier *object.Object
}

// Status returns the raw DICOM status code.
func (r FindResult) Status() uint16 {
	if r.Response == nil {
		return 0
	}
	return r.Response.Status
}

// ErrorComment returns the optional (0000,0902) Error Comment from the response.
func (r FindResult) ErrorComment() string {
	if r.Response == nil {
		return ""
	}
	return r.Response.ErrorComment
}

func (r FindResult) String() string {
	if r.Response == nil {
		return "C-FIND-RSP <nil>"
	}
	if r.Identifier == nil {
		return fmt.Sprintf("C-FIND-RSP status=0x%04X", r.Response.Status)
	}
	// object.Object doesn't provide a stable string/dump API; include a terse summary.
	return fmt.Sprintf("C-FIND-RSP status=0x%04X identifier_present", r.Response.Status)
}

// Find performs a C-FIND operation over an already-established association.
//
// It sends a C-FIND-RQ and then iterates over C-FIND-RSP messages until a final
// response is received or an error occurs.
//
// Results are streamed on the returned channel. Exactly one error (if any) is
// sent on the error channel; it is closed after completion.
//
// The caller may cancel via ctx. Note: canceling the context does not
// automatically abort the association; the caller should decide whether to
// Release/Abort.
func Find(ctx context.Context, assoc *ul.Association, pcID byte, req CFindRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (<-chan FindResult, <-chan error) {
	if ctx == nil {
		return findError(fmt.Errorf("dicom dimse: nil context"))
	}
	return FindWithOptions(OperationOptions{Context: ctx}, assoc, pcID, req, identifier, identifierSyntax)
}

// FindWithOptions is like Find but uses OperationOptions for operation
// cancellation, overall timeout, per-response timeout, and association cleanup
// policy after uncertain errors.
func FindWithOptions(options OperationOptions, assoc *ul.Association, pcID byte, req CFindRequest, identifier *object.Object, identifierSyntax transfer.Syntax) (<-chan FindResult, <-chan error) {
	options = operationOptionsWithDefaultPolicy(options, OperationErrorPolicyLeaveOpen)

	out := make(chan FindResult)
	errCh := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errCh)

		ctx, cancel := operationContext(options)
		defer cancel()

		if err := ctx.Err(); err != nil {
			errCh <- newOperationError("C-FIND", err, false)
			return
		}

		if identifier == nil {
			errCh <- fmt.Errorf("dicom dimse: nil C-FIND identifier")
			return
		}
		if assoc == nil {
			errCh <- fmt.Errorf("dicom dimse: nil assoc")
			return
		}

		releaseOperation, err := beginAssociationOperation(assoc)
		if err != nil {
			errCh <- newOperationError("C-FIND", err, false)
			return
		}
		defer releaseOperation()

		if err := SendCommandSet(assoc, pcID, req.CommandSet()); err != nil {
			errCh <- applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-FIND", err, true))
			return
		}
		if err := SendDataSet(assoc, pcID, identifier, identifierSyntax); err != nil {
			errCh <- applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-FIND", err, true))
			return
		}

		for {
			select {
			case <-ctx.Done():
				errCh <- applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-FIND", ctx.Err(), true))
				return
			default:
			}

			responseCtx, responseCancel := operationResponseContext(ctx, options.ResponseTimeout)
			rsp, id, err := receiveCFindResponseWithContext(responseCtx, assoc, pcID, identifierSyntax)
			responseCancel()
			if err != nil {
				errCh <- applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-FIND", err, true))
				return
			}

			// Emit the response.
			select {
			case <-ctx.Done():
				errCh <- applyOperationErrorPolicy(assoc, options.ErrorPolicy, newOperationError("C-FIND", ctx.Err(), true))
				return
			case out <- FindResult{Response: rsp, Identifier: id}:
			}

			// Decide whether to continue.
			//
			// If the caller cancels the context after we've started receiving responses,
			// abort the operation cleanly by returning ctx.Err(). The association is
			// left to the caller to Release/Abort.
			category := CategorizeCFindStatus(rsp.Status)
			switch category {
			case CFindStatusPending:
				continue
			case CFindStatusSuccess:
				return
			case CFindStatusFailure:
				errCh <- CFindStatusError(rsp)
				return
			case CFindStatusInvalid:
				errCh <- CFindStatusError(rsp)
				return
			default:
				errCh <- fmt.Errorf("dicom dimse: unexpected C-FIND status category %s for status 0x%04X", category, rsp.Status)
				return
			}
		}
	}()

	return out, errCh
}

func findError(err error) (<-chan FindResult, <-chan error) {
	out := make(chan FindResult)
	errCh := make(chan error, 1)
	close(out)
	errCh <- err
	close(errCh)
	return out, errCh
}
