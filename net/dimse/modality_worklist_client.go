package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const defaultModalityWorklistCancelDrainTimeout = time.Second

var ErrModalityWorklistStatus = errors.New("dicom dimse: MWL C-FIND returned non-success status")
var ErrModalityWorklistCallback = errors.New("dicom dimse: MWL match callback failed")

// ModalityWorklistStatusError reports a final DIMSE status without rendering
// the remote Error Comment in Error(). Callers may inspect ErrorComment
// explicitly when it is safe to do so.
type ModalityWorklistStatusError struct {
	Status       uint16
	ErrorComment string
}

func (e *ModalityWorklistStatusError) Error() string {
	if e == nil {
		return ErrModalityWorklistStatus.Error()
	}
	return fmt.Sprintf("%s 0x%04X", ErrModalityWorklistStatus, e.Status)
}

func (e *ModalityWorklistStatusError) Unwrap() error {
	return ErrModalityWorklistStatus
}

// ModalityWorklistFindResult summarizes one completed MWL operation.
type ModalityWorklistFindResult struct {
	FinalResponse *CFindResponse
	MatchCount    int
	CancelSent    bool
}

// ModalityWorklistFindOptions configures one blocking MWL C-FIND operation.
type ModalityWorklistFindOptions struct {
	Operation           OperationOptions
	MaxMatches          int
	MaxResponseBytes    int64
	MaxResponseElements int
	MaxResponseDepth    int
	CancelDrainTimeout  time.Duration
	Priority            uint16
}

func (options ModalityWorklistFindOptions) normalized() (ModalityWorklistFindOptions, error) {
	if options.MaxMatches < 0 || options.MaxResponseBytes < 0 || options.MaxResponseElements < 0 || options.MaxResponseDepth < 0 || options.CancelDrainTimeout < 0 {
		return ModalityWorklistFindOptions{}, fmt.Errorf("dicom dimse: MWL client limits must not be negative")
	}
	if options.Priority > PriorityLow {
		return ModalityWorklistFindOptions{}, fmt.Errorf("dicom dimse: MWL priority must be 0, 1, or 2")
	}
	if options.MaxMatches == 0 {
		options.MaxMatches = defaultModalityWorklistMaxMatches
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = MaxIdentifierBytes
	}
	if options.MaxResponseElements == 0 {
		options.MaxResponseElements = defaultModalityWorklistResponseElements
	}
	if options.MaxResponseDepth == 0 {
		options.MaxResponseDepth = defaultModalityWorklistResponseDepth
	}
	if options.CancelDrainTimeout == 0 {
		options.CancelDrainTimeout = defaultModalityWorklistCancelDrainTimeout
	}
	options.Operation = operationOptionsWithDefaultPolicy(options.Operation, OperationErrorPolicyAbort)
	return options, nil
}

// ModalityWorklistClient executes blocking, callback-streamed MWL queries over
// a borrowed established association. It never releases or closes the
// association after a normally completed operation.
type ModalityWorklistClient struct {
	assoc  *ul.Association
	pcID   byte
	syntax transfer.Syntax
	nextID atomic.Uint32
}

// NewModalityWorklistClient derives the accepted MWL presentation context.
func NewModalityWorklistClient(assoc *ul.Association) (*ModalityWorklistClient, error) {
	pc, err := AcceptedContextForSOPClass(assoc, ModalityWorklistFindSOPClassUID)
	if err != nil {
		return nil, err
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return nil, err
	}
	return &ModalityWorklistClient{assoc: assoc, pcID: pc.ID, syntax: syntax}, nil
}

// Find executes one MWL C-FIND and synchronously invokes yield for every
// pending Identifier. Returning from yield supplies backpressure to the peer.
func (client *ModalityWorklistClient) Find(ctx context.Context, query ModalityWorklistQuery, yield func(*object.Object) error) (ModalityWorklistFindResult, error) {
	return client.FindWithOptions(ModalityWorklistFindOptions{Operation: OperationOptions{Context: ctx}}, query, yield)
}

// FindWithOptions executes one bounded MWL C-FIND. Local cancellation sends
// C-CANCEL and drains the final response before returning; a failed drain is
// reported as association-state-uncertain and follows ErrorPolicy.
func (client *ModalityWorklistClient) FindWithOptions(options ModalityWorklistFindOptions, query ModalityWorklistQuery, yield func(*object.Object) error) (ModalityWorklistFindResult, error) {
	var result ModalityWorklistFindResult
	if client == nil || client.assoc == nil {
		return result, fmt.Errorf("dicom dimse: nil MWL client")
	}
	if yield == nil {
		return result, fmt.Errorf("dicom dimse: nil MWL match callback")
	}
	options, err := options.normalized()
	if err != nil {
		return result, err
	}
	identifier, err := BuildModalityWorklistIdentifier(query)
	if err != nil {
		return result, err
	}
	ctx, cancel := operationContext(options.Operation)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, newOperationError("MWL C-FIND", err, false)
	}
	releaseOperation, err := beginAssociationOperation(client.assoc)
	if err != nil {
		return result, newOperationError("MWL C-FIND", err, false)
	}
	defer releaseOperation()

	messageID := client.nextMessageID()
	request := CFindRequest{AffectedSOPClassUID: ModalityWorklistFindSOPClassUID, MessageID: messageID, Priority: options.Priority}
	if err := SendCommandSetWithContext(ctx, client.assoc, client.pcID, request.CommandSet()); err != nil {
		return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", err, true))
	}
	if err := SendDataSetWithContext(ctx, client.assoc, client.pcID, identifier, client.syntax); err != nil {
		return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", err, true))
	}

	for {
		if err := ctx.Err(); err != nil {
			return client.cancelAndDrain(result, request, options, err)
		}
		responseCtx, responseCancel := operationResponseContext(ctx, options.Operation.ResponseTimeout)
		response, match, receiveErr := receiveModalityWorklistResponse(responseCtx, client.assoc, client.pcID, client.syntax, options)
		responseCancel()
		if receiveErr != nil {
			if err := ctx.Err(); err != nil {
				return client.cancelAndDrain(result, request, options, err)
			}
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", receiveErr, true))
		}
		if err := validateModalityWorklistResponse(response, request.MessageID); err != nil {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", err, true))
		}
		switch CategorizeCFindStatus(response.Status) {
		case CFindStatusPending:
			if match == nil {
				return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", fmt.Errorf("pending response missing Identifier"), true))
			}
			if result.MatchCount >= options.MaxMatches {
				return client.cancelAndDrain(result, request, options, ErrModalityWorklistResourceLimit)
			}
			result.MatchCount++
			if err := callModalityWorklistCallback(yield, match); err != nil {
				return client.cancelAndDrain(result, request, options, err)
			}
			if err := ctx.Err(); err != nil {
				return client.cancelAndDrain(result, request, options, err)
			}
		case CFindStatusSuccess:
			if match != nil {
				return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", fmt.Errorf("final response included Identifier"), true))
			}
			result.FinalResponse = response
			return result, nil
		case CFindStatusFailure:
			if match != nil {
				return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", fmt.Errorf("final response included Identifier"), true))
			}
			result.FinalResponse = response
			return result, &ModalityWorklistStatusError{Status: response.Status, ErrorComment: response.ErrorComment}
		default:
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", &CFindInvalidStatusError{Status: response.Status}, true))
		}
	}
}

type modalityWorklistCallbackError struct {
	cause error
}

func (err *modalityWorklistCallbackError) Error() string {
	return ErrModalityWorklistCallback.Error()
}

func (err *modalityWorklistCallbackError) Unwrap() error {
	if err == nil || err.cause == nil {
		return ErrModalityWorklistCallback
	}
	return err.cause
}

func (err *modalityWorklistCallbackError) Is(target error) bool {
	return target == ErrModalityWorklistCallback
}

func callModalityWorklistCallback(callback func(*object.Object) error, match *object.Object) (err error) {
	defer func() {
		if recover() != nil {
			err = &modalityWorklistCallbackError{}
		}
	}()
	if callbackErr := callback(match); callbackErr != nil {
		return &modalityWorklistCallbackError{cause: callbackErr}
	}
	return nil
}

func (client *ModalityWorklistClient) cancelAndDrain(result ModalityWorklistFindResult, request CFindRequest, options ModalityWorklistFindOptions, cause error) (ModalityWorklistFindResult, error) {
	drainCtx, cancel := context.WithTimeout(context.Background(), options.CancelDrainTimeout)
	defer cancel()
	if err := SendCCancelRequestWithContext(drainCtx, client.assoc, client.pcID, CCancelRequest{MessageIDBeingRespondedTo: request.MessageID}); err != nil {
		joined := errors.Join(cause, err)
		return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", joined, true))
	}
	result.CancelSent = true
	for {
		response, match, err := receiveModalityWorklistResponse(drainCtx, client.assoc, client.pcID, client.syntax, options)
		if err != nil {
			joined := errors.Join(cause, err)
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", joined, true))
		}
		if err := validateModalityWorklistResponse(response, request.MessageID); err != nil {
			joined := errors.Join(cause, err)
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", joined, true))
		}
		category := CategorizeCFindStatus(response.Status)
		if err := validateModalityWorklistResponseIdentifier(category, match); err != nil {
			joined := errors.Join(cause, err)
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", joined, true))
		}
		if category == CFindStatusPending {
			continue
		}
		if category == CFindStatusInvalid {
			joined := errors.Join(cause, &CFindInvalidStatusError{Status: response.Status})
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("MWL C-FIND", joined, true))
		}
		result.FinalResponse = response
		return result, newOperationError("MWL C-FIND", cause, false)
	}
}

func validateModalityWorklistResponseIdentifier(category CFindStatusCategory, identifier *object.Object) error {
	if category == CFindStatusPending && identifier == nil {
		return fmt.Errorf("dicom dimse: pending MWL C-FIND response is missing Identifier")
	}
	if category != CFindStatusPending && identifier != nil {
		return fmt.Errorf("dicom dimse: final MWL C-FIND response included Identifier")
	}
	return nil
}

func receiveModalityWorklistResponse(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, options ModalityWorklistFindOptions) (*CFindResponse, *object.Object, error) {
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return nil, nil, err
	}
	response, err := ParseCFindResponse(command)
	if err != nil {
		return nil, nil, err
	}
	if response.CommandDataSetType == NoDataSet {
		return response, nil, nil
	}
	reader := newTypedPDataReaderWithContext(dataSetReadContext(ctx, assoc), assoc, pcID, false)
	match, err := object.ReadDataSetWithOptions(reader, syntax, object.ReadFileOptions{
		MaxElementBytes:  options.MaxResponseBytes,
		MaxTotalBytes:    options.MaxResponseBytes,
		MaxSequenceDepth: options.MaxResponseDepth,
		MaxElements:      options.MaxResponseElements,
	})
	if err != nil {
		if isModalityWorklistLimitError(err) {
			return nil, nil, ErrModalityWorklistResourceLimit
		}
		return nil, nil, fmt.Errorf("dicom dimse: receive MWL response Identifier: %w", err)
	}
	if err := validateModalityWorklistObjectLimits(match, options.MaxResponseElements, options.MaxResponseDepth); err != nil {
		return nil, nil, err
	}
	return response, match, nil
}

func (client *ModalityWorklistClient) nextMessageID() uint16 {
	for {
		id := uint16(client.nextID.Add(1))
		if id != 0 {
			return id
		}
	}
}

func validateModalityWorklistResponse(response *CFindResponse, messageID uint16) error {
	if response == nil {
		return fmt.Errorf("dicom dimse: nil MWL C-FIND response")
	}
	if response.MessageIDBeingRespondedTo != messageID {
		return fmt.Errorf("dicom dimse: MWL response message ID mismatch")
	}
	if response.AffectedSOPClassUID != "" && response.AffectedSOPClassUID != ModalityWorklistFindSOPClassUID {
		return fmt.Errorf("dicom dimse: MWL response SOP Class mismatch")
	}
	return nil
}
