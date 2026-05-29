package dimse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrStreamingCFindProvider      = errors.New("dicom dimse: streaming C-FIND provider failed")
	ErrStreamingCFindResourceLimit = errors.New("dicom dimse: streaming C-FIND resource limit exceeded")
	ErrStreamingCFindIdentifier    = errors.New("dicom dimse: streaming C-FIND Identifier invalid")
	ErrStreamingCFindCallback      = errors.New("dicom dimse: streaming C-FIND callback failed")
)

const defaultStreamingCFindCancelDrainTimeout = time.Second

// StreamingCFindLimits bound a service-specific C-FIND request and every
// response Identifier. Zero values use finite defaults.
type StreamingCFindLimits struct {
	MaxMatches            int
	MaxIdentifierBytes    int64
	MaxIdentifierElements int
	MaxIdentifierDepth    int
	MaxResponseBytes      int64
	MaxResponseElements   int
	MaxResponseDepth      int
}

func (limits StreamingCFindLimits) normalized() (StreamingCFindLimits, error) {
	if limits.MaxMatches < 0 || limits.MaxIdentifierBytes < 0 || limits.MaxIdentifierElements < 0 || limits.MaxIdentifierDepth < 0 ||
		limits.MaxResponseBytes < 0 || limits.MaxResponseElements < 0 || limits.MaxResponseDepth < 0 {
		return StreamingCFindLimits{}, fmt.Errorf("dicom dimse: streaming C-FIND limits must not be negative")
	}
	if limits.MaxMatches == 0 {
		limits.MaxMatches = defaultModalityWorklistMaxMatches
	}
	if limits.MaxIdentifierBytes == 0 {
		limits.MaxIdentifierBytes = MaxIdentifierBytes
	}
	if limits.MaxIdentifierElements == 0 {
		limits.MaxIdentifierElements = defaultModalityWorklistIdentifierElements
	}
	if limits.MaxIdentifierDepth == 0 {
		limits.MaxIdentifierDepth = defaultModalityWorklistIdentifierDepth
	}
	if limits.MaxResponseBytes == 0 {
		limits.MaxResponseBytes = MaxIdentifierBytes
	}
	if limits.MaxResponseElements == 0 {
		limits.MaxResponseElements = defaultModalityWorklistResponseElements
	}
	if limits.MaxResponseDepth == 0 {
		limits.MaxResponseDepth = defaultModalityWorklistResponseDepth
	}
	return limits, nil
}

// StreamingCFindRequest is borrowed for one handler invocation.
type StreamingCFindRequest struct {
	Request               CFindRequest
	Identifier            *object.Object
	PresentationContextID byte
	IdentifierSyntax      transfer.Syntax
}

// StreamingCFindYield synchronously validates and sends one pending response.
// Status must be StatusPending or StatusPendingWarning. Identifier is borrowed
// only until the call returns.
type StreamingCFindYield func(status uint16, identifier *object.Object) error

type StreamingCFindHandler interface {
	Find(context.Context, StreamingCFindRequest, StreamingCFindYield) error
}

type StreamingCFindHandlerFunc func(context.Context, StreamingCFindRequest, StreamingCFindYield) error

func (handler StreamingCFindHandlerFunc) Find(ctx context.Context, request StreamingCFindRequest, yield StreamingCFindYield) error {
	if handler == nil {
		return ErrStreamingCFindProvider
	}
	return handler(ctx, request, yield)
}

// StreamingCFindRoute binds one exact abstract syntax to a service-specific,
// streaming provider. ResponseSOPClassUID defaults to SOPClassUID.
type StreamingCFindRoute struct {
	SOPClassUID         string
	ResponseSOPClassUID string
	Handler             StreamingCFindHandler
	Limits              StreamingCFindLimits
}

type streamingCFindRouter struct {
	base   CFindHandler
	routes map[string]StreamingCFindRoute
}

// NewStreamingCFindRouter composes exact streaming routes with an optional
// existing hierarchical/MWL router without changing AssociationSCPOptions.
func NewStreamingCFindRouter(base CFindHandler, routes ...StreamingCFindRoute) (CFindHandler, error) {
	if len(routes) == 0 {
		return nil, ErrStreamingCFindProvider
	}
	router := &streamingCFindRouter{base: base, routes: make(map[string]StreamingCFindRoute, len(routes))}
	for _, route := range routes {
		if route.SOPClassUID == "" || route.Handler == nil || router.routes[route.SOPClassUID].SOPClassUID != "" {
			return nil, ErrStreamingCFindProvider
		}
		limits, err := route.Limits.normalized()
		if err != nil {
			return nil, err
		}
		route.Limits = limits
		if route.ResponseSOPClassUID == "" {
			route.ResponseSOPClassUID = route.SOPClassUID
		}
		router.routes[route.SOPClassUID] = route
	}
	return router, nil
}

func (router *streamingCFindRouter) Find(ctx context.Context, request CFindRequestContext) ([]*object.Object, error) {
	if router == nil || router.base == nil {
		return nil, ErrStreamingCFindProvider
	}
	return router.base.Find(ctx, request)
}

func (router *streamingCFindRouter) serveModalityWorklist(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object) error {
	base, ok := router.base.(interface {
		serveModalityWorklist(context.Context, *ul.Association, byte, *object.Object) error
	})
	if !ok {
		return ErrModalityWorklistProvider
	}
	return base.serveModalityWorklist(ctx, assoc, pcID, command)
}

func (router *streamingCFindRouter) serveStreamingCFind(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, sopClassUID string) (bool, error) {
	if router == nil {
		return false, nil
	}
	route, ok := router.routes[sopClassUID]
	if !ok {
		return false, nil
	}
	return true, ServeStreamingCFindCommand(ctx, assoc, pcID, command, route)
}

// ServeStreamingCFindCommand handles one already-decoded C-FIND-RQ through an
// exact streaming route. It is the public dispatcher seam for applications
// that own the association receive loop.
func ServeStreamingCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, route StreamingCFindRoute) error {
	if strings.TrimSpace(route.SOPClassUID) == "" || route.Handler == nil {
		return ErrStreamingCFindProvider
	}
	limits, err := route.Limits.normalized()
	if err != nil {
		return err
	}
	route.Limits = limits
	if route.ResponseSOPClassUID == "" {
		route.ResponseSOPClassUID = route.SOPClassUID
	}
	return serveStreamingCFindCommand(ctx, assoc, pcID, command, route)
}

func serveStreamingCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, route StreamingCFindRoute) error {
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return err
	}
	request, err := ParseCFindRequest(command)
	if err != nil {
		return err
	}
	identifier, err := receiveStreamingCFindDataSet(ctx, assoc, pcID, syntax, route.Limits.MaxIdentifierBytes, route.Limits.MaxIdentifierElements, route.Limits.MaxIdentifierDepth)
	if err != nil {
		status, comment := streamingCFindStatus(err)
		if sendErr := sendCFindFinal(ctx, assoc, pcID, *request, route.ResponseSOPClassUID, status, comment, syntax); sendErr != nil {
			return sendErr
		}
		return err
	}
	if pc.AbstractSyntaxUID != route.SOPClassUID || request.AffectedSOPClassUID != route.SOPClassUID {
		if sendErr := sendCFindFinal(ctx, assoc, pcID, *request, route.ResponseSOPClassUID, 0xA900, "Identifier does not match SOP Class", syntax); sendErr != nil {
			return sendErr
		}
		return ErrStreamingCFindIdentifier
	}
	monitor := startSCPCancelMonitor(ctx, assoc, pcID, request.MessageID, ErrCFindCanceled, false)
	defer monitor.Stop()
	operationCtx := monitor.Context()
	emitter := streamingCFindEmitter{active: true}
	emitter.yield = func(status uint16, response *object.Object) error {
		if status != StatusPending && status != StatusPendingWarning || response == nil {
			return ErrStreamingCFindProvider
		}
		if err := monitor.OperationError(); err != nil {
			return err
		}
		if emitter.matches >= route.Limits.MaxMatches {
			return ErrStreamingCFindResourceLimit
		}
		if err := preflightStreamingCFindDataSet(response, syntax, route.Limits.MaxResponseBytes, route.Limits.MaxResponseElements, route.Limits.MaxResponseDepth); err != nil {
			return err
		}
		if err := sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
			return SendCFindResponseWithContext(responseCtx, assoc, pcID, CFindResponse{
				AffectedSOPClassUID: route.ResponseSOPClassUID, MessageIDBeingRespondedTo: request.MessageID, Status: status,
			}, response, syntax)
		}); err != nil {
			return err
		}
		emitter.matches++
		return nil
	}
	_, handlerErr := runSCPHandler(operationCtx, assoc, scpControlsFromContext(ctx).CancelGrace, func(handlerCtx context.Context) (struct{}, error) {
		defer emitter.close()
		return struct{}{}, route.Handler.Find(handlerCtx, StreamingCFindRequest{
			Request: *request, Identifier: identifier, PresentationContextID: pcID, IdentifierSyntax: syntax,
		}, emitter.emit)
	})
	if monitorErr := monitor.Stop(); monitorErr != nil {
		handlerErr = errors.Join(handlerErr, monitorErr)
	}
	if handlerErr != nil {
		status, comment := streamingCFindStatus(handlerErr)
		if sendErr := sendCFindFinal(operationCtx, assoc, pcID, *request, route.ResponseSOPClassUID, status, comment, syntax); sendErr != nil {
			return sendErr
		}
		return handlerErr
	}
	return sendCFindFinal(ctx, assoc, pcID, *request, route.ResponseSOPClassUID, StatusSuccess, "", syntax)
}

type streamingCFindEmitter struct {
	mu      sync.Mutex
	active  bool
	matches int
	yield   StreamingCFindYield
}

func (emitter *streamingCFindEmitter) emit(status uint16, identifier *object.Object) error {
	if emitter == nil {
		return ErrStreamingCFindProvider
	}
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if !emitter.active || emitter.yield == nil {
		return ErrStreamingCFindProvider
	}
	return emitter.yield(status, identifier)
}

func (emitter *streamingCFindEmitter) close() {
	if emitter == nil {
		return
	}
	emitter.mu.Lock()
	emitter.active = false
	emitter.mu.Unlock()
}

func receiveStreamingCFindDataSet(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, maxBytes int64, maxElements, maxDepth int) (*object.Object, error) {
	reader := newTypedPDataReaderWithContext(dataSetReadContext(ctx, assoc), assoc, pcID, false)
	value, err := object.ReadDataSetWithOptions(reader, syntax, object.ReadFileOptions{
		MaxElementBytes: maxBytes, MaxTotalBytes: maxBytes, MaxElements: maxElements, MaxSequenceDepth: maxDepth,
	})
	if err != nil {
		if shouldDrainDataSetPDataOnError(err) {
			if drainErr := drainPDataReader(reader); drainErr != nil {
				return nil, drainErr
			}
		}
		if isModalityWorklistLimitError(err) {
			return nil, ErrStreamingCFindResourceLimit
		}
		return nil, ErrStreamingCFindIdentifier
	}
	if err := validateModalityWorklistObjectLimits(value, maxElements, maxDepth); err != nil {
		return nil, ErrStreamingCFindResourceLimit
	}
	return value, nil
}

func preflightStreamingCFindDataSet(value *object.Object, syntax transfer.Syntax, maxBytes int64, maxElements, maxDepth int) error {
	if err := preflightModalityWorklistResponse(value, syntax, maxElements, maxBytes, maxDepth); err != nil {
		if errors.Is(err, ErrModalityWorklistResourceLimit) {
			return ErrStreamingCFindResourceLimit
		}
		return ErrStreamingCFindProvider
	}
	return nil
}

func streamingCFindStatus(err error) (uint16, string) {
	switch {
	case errors.Is(err, ErrCFindCanceled), errors.Is(err, context.Canceled):
		return CFindStatusCancel, "C-FIND canceled"
	case errors.Is(err, ErrStreamingCFindResourceLimit):
		return CFindStatusOutOfResources, "Out of resources"
	case errors.Is(err, ErrStreamingCFindIdentifier):
		return 0xA900, "Identifier does not match SOP Class"
	default:
		return CFindStatusUnableToProcess, "Unable to process"
	}
}

// StreamingCFindClientOptions configure one raw service-specific C-FIND.
type StreamingCFindClientOptions struct {
	Operation           OperationOptions
	MaxMatches          int
	MaxResponseBytes    int64
	MaxResponseElements int
	MaxResponseDepth    int
	CancelDrainTimeout  time.Duration
	Priority            uint16
}

func (options StreamingCFindClientOptions) normalized() (StreamingCFindClientOptions, error) {
	if options.MaxMatches < 0 || options.MaxResponseBytes < 0 || options.MaxResponseElements < 0 || options.MaxResponseDepth < 0 || options.CancelDrainTimeout < 0 || options.Priority > PriorityLow {
		return StreamingCFindClientOptions{}, ErrStreamingCFindResourceLimit
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
		options.CancelDrainTimeout = defaultStreamingCFindCancelDrainTimeout
	}
	options.Operation = operationOptionsWithDefaultPolicy(options.Operation, OperationErrorPolicyAbort)
	return options, nil
}

type StreamingCFindResult struct {
	FinalResponse *CFindResponse
	MatchCount    int
	CancelSent    bool
}

type StreamingCFindStatusError struct {
	Status uint16
}

func (err *StreamingCFindStatusError) Error() string {
	return fmt.Sprintf("dicom dimse: streaming C-FIND status 0x%04X", err.Status)
}

// StreamingCFindClient performs one callback-streamed C-FIND at a time over a
// borrowed association.
type StreamingCFindClient struct {
	assoc               *ul.Association
	pcID                byte
	syntax              transfer.Syntax
	requestSOPClassUID  string
	responseSOPClassUID string
	nextID              atomic.Uint32
}

func NewStreamingCFindClient(assoc *ul.Association, requestSOPClassUID, responseSOPClassUID string) (*StreamingCFindClient, error) {
	pc, err := AcceptedContextForSOPClass(assoc, requestSOPClassUID)
	if err != nil {
		return nil, err
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return nil, err
	}
	if responseSOPClassUID == "" {
		responseSOPClassUID = requestSOPClassUID
	}
	return &StreamingCFindClient{assoc: assoc, pcID: pc.ID, syntax: syntax, requestSOPClassUID: requestSOPClassUID, responseSOPClassUID: responseSOPClassUID}, nil
}

func (client *StreamingCFindClient) Find(ctx context.Context, identifier *object.Object, yield func(uint16, *object.Object) error) (StreamingCFindResult, error) {
	return client.FindWithOptions(StreamingCFindClientOptions{Operation: OperationOptions{Context: ctx}}, identifier, yield)
}

func (client *StreamingCFindClient) FindWithOptions(options StreamingCFindClientOptions, identifier *object.Object, yield func(uint16, *object.Object) error) (StreamingCFindResult, error) {
	var result StreamingCFindResult
	if client == nil || client.assoc == nil || identifier == nil || yield == nil {
		return result, ErrStreamingCFindProvider
	}
	options, err := options.normalized()
	if err != nil {
		return result, err
	}
	ctx, cancel := operationContext(options.Operation)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, newOperationError("streaming C-FIND", err, false)
	}
	release, err := beginAssociationOperation(client.assoc)
	if err != nil {
		return result, newOperationError("streaming C-FIND", err, false)
	}
	defer release()
	messageID := client.nextMessageID()
	request := CFindRequest{AffectedSOPClassUID: client.requestSOPClassUID, MessageID: messageID, Priority: options.Priority}
	if err := SendCommandSetWithContext(ctx, client.assoc, client.pcID, request.CommandSet()); err != nil {
		return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", err, true))
	}
	if err := SendDataSetWithContext(ctx, client.assoc, client.pcID, identifier, client.syntax); err != nil {
		return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", err, true))
	}
	for {
		if err := ctx.Err(); err != nil {
			return client.cancelAndDrain(result, request, options, err)
		}
		responseCtx, responseCancel := operationResponseContext(ctx, options.Operation.ResponseTimeout)
		response, match, receiveErr := receiveStreamingCFindResponse(responseCtx, client, options)
		responseCancel()
		if receiveErr != nil {
			if err := ctx.Err(); err != nil {
				return client.cancelAndDrain(result, request, options, err)
			}
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", receiveErr, true))
		}
		if err := client.validateResponse(response, messageID); err != nil {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", err, true))
		}
		category := CategorizeCFindStatus(response.Status)
		if err := validateModalityWorklistResponseIdentifier(category, match); err != nil {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", err, true))
		}
		switch category {
		case CFindStatusPending:
			if result.MatchCount >= options.MaxMatches {
				return client.cancelAndDrain(result, request, options, ErrStreamingCFindResourceLimit)
			}
			result.MatchCount++
			if err := callStreamingCFindCallback(yield, response.Status, match); err != nil {
				return client.cancelAndDrain(result, request, options, err)
			}
		case CFindStatusSuccess:
			result.FinalResponse = response
			return result, nil
		case CFindStatusFailure:
			result.FinalResponse = response
			return result, &StreamingCFindStatusError{Status: response.Status}
		default:
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", &CFindInvalidStatusError{Status: response.Status}, true))
		}
	}
}

func (client *StreamingCFindClient) cancelAndDrain(result StreamingCFindResult, request CFindRequest, options StreamingCFindClientOptions, cause error) (StreamingCFindResult, error) {
	drainCtx, cancel := context.WithTimeout(context.Background(), options.CancelDrainTimeout)
	defer cancel()
	if err := SendCCancelRequestWithContext(drainCtx, client.assoc, client.pcID, CCancelRequest{MessageIDBeingRespondedTo: request.MessageID}); err != nil {
		return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", errors.Join(cause, err), true))
	}
	result.CancelSent = true
	for {
		response, match, err := receiveStreamingCFindResponse(drainCtx, client, options)
		if err != nil {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", errors.Join(cause, err), true))
		}
		if err := client.validateResponse(response, request.MessageID); err != nil {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", errors.Join(cause, err), true))
		}
		category := CategorizeCFindStatus(response.Status)
		if err := validateModalityWorklistResponseIdentifier(category, match); err != nil {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", errors.Join(cause, err), true))
		}
		if category == CFindStatusPending {
			continue
		}
		if category == CFindStatusInvalid {
			return result, applyOperationErrorPolicy(client.assoc, options.Operation.ErrorPolicy, newOperationError("streaming C-FIND", errors.Join(cause, &CFindInvalidStatusError{Status: response.Status}), true))
		}
		result.FinalResponse = response
		return result, newOperationError("streaming C-FIND", cause, false)
	}
}

func receiveStreamingCFindResponse(ctx context.Context, client *StreamingCFindClient, options StreamingCFindClientOptions) (*CFindResponse, *object.Object, error) {
	command, err := receiveCommandSetWithContext(ctx, client.assoc, client.pcID)
	if err != nil {
		return nil, nil, err
	}
	response, err := ParseCFindResponse(command)
	if err != nil || response.CommandDataSetType == NoDataSet {
		return response, nil, err
	}
	match, err := receiveStreamingCFindDataSet(ctx, client.assoc, client.pcID, client.syntax, options.MaxResponseBytes, options.MaxResponseElements, options.MaxResponseDepth)
	if err != nil {
		return nil, nil, err
	}
	return response, match, nil
}

func (client *StreamingCFindClient) validateResponse(response *CFindResponse, messageID uint16) error {
	if response == nil || response.MessageIDBeingRespondedTo != messageID {
		return ErrStreamingCFindProvider
	}
	if response.AffectedSOPClassUID != "" && response.AffectedSOPClassUID != client.responseSOPClassUID {
		return ErrStreamingCFindProvider
	}
	return nil
}

func (client *StreamingCFindClient) nextMessageID() uint16 {
	for {
		id := uint16(client.nextID.Add(1))
		if id != 0 {
			return id
		}
	}
}

func callStreamingCFindCallback(callback func(uint16, *object.Object) error, status uint16, match *object.Object) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrStreamingCFindCallback
		}
	}()
	if err := callback(status, match); err != nil {
		return &streamingCFindCallbackError{cause: err}
	}
	return nil
}

type streamingCFindCallbackError struct {
	cause error
}

func (err *streamingCFindCallbackError) Error() string { return ErrStreamingCFindCallback.Error() }

func (err *streamingCFindCallbackError) Unwrap() error {
	if err == nil || err.cause == nil {
		return ErrStreamingCFindCallback
	}
	return err.cause
}

func (err *streamingCFindCallbackError) Is(target error) bool {
	return target == ErrStreamingCFindCallback
}
