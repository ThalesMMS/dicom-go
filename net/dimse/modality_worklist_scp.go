package dimse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrModalityWorklistResourceLimit = errors.New("dicom dimse: MWL resource limit exceeded")
	ErrModalityWorklistProvider      = errors.New("dicom dimse: MWL provider failed")
)

const (
	defaultModalityWorklistMaxMatches         = 10_000
	defaultModalityWorklistIdentifierElements = 256
	defaultModalityWorklistIdentifierDepth    = 4
	defaultModalityWorklistResponseElements   = 1_024
	defaultModalityWorklistResponseDepth      = 8
)

// ModalityWorklistRequest is borrowed for the duration of a handler call.
type ModalityWorklistRequest struct {
	Request               CFindRequest
	Identifier            ParsedModalityWorklistIdentifier
	RawIdentifier         *object.Object
	PresentationContextID byte
	IdentifierSyntax      transfer.Syntax
}

// ModalityWorklistYield synchronously validates, projects, and sends one
// candidate match. The candidate is borrowed only until Yield returns.
type ModalityWorklistYield func(candidate *object.Object) error

// ModalityWorklistHandler incrementally produces MWL candidates.
type ModalityWorklistHandler interface {
	Find(context.Context, ModalityWorklistRequest, ModalityWorklistYield) error
}

// ModalityWorklistHandlerFunc adapts a function to ModalityWorklistHandler.
type ModalityWorklistHandlerFunc func(context.Context, ModalityWorklistRequest, ModalityWorklistYield) error

func (f ModalityWorklistHandlerFunc) Find(ctx context.Context, request ModalityWorklistRequest, yield ModalityWorklistYield) error {
	if f == nil {
		return ErrModalityWorklistProvider
	}
	return f(ctx, request, yield)
}

type modalityWorklistCFindRouter struct {
	queryRetrieve CFindHandler
	worklist      ModalityWorklistHandler
	options       ModalityWorklistSCPOptions
}

// NewModalityWorklistCFindRouter augments an optional hierarchical C-FIND
// handler with an exact Modality Worklist SOP Class route. The returned value
// is installed in AssociationSCPOptions.CFindHandler.
func NewModalityWorklistCFindRouter(queryRetrieve CFindHandler, worklist ModalityWorklistHandler, options ModalityWorklistSCPOptions) (CFindHandler, error) {
	if worklist == nil {
		return nil, ErrModalityWorklistProvider
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	return &modalityWorklistCFindRouter{
		queryRetrieve: queryRetrieve,
		worklist:      worklist,
		options:       normalized,
	}, nil
}

func (router *modalityWorklistCFindRouter) Find(ctx context.Context, request CFindRequestContext) ([]*object.Object, error) {
	if router == nil || router.queryRetrieve == nil {
		return nil, fmt.Errorf("dicom dimse: hierarchical C-FIND handler is not configured")
	}
	return router.queryRetrieve.Find(ctx, request)
}

func (router *modalityWorklistCFindRouter) serveModalityWorklist(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object) error {
	if router == nil || router.worklist == nil {
		return ErrModalityWorklistProvider
	}
	return serveModalityWorklistCFindCommand(ctx, assoc, pcID, command, router.worklist, router.options)
}

// ModalityWorklistSCPOptions bounds one MWL C-FIND operation. Zero values use
// finite defaults.
type ModalityWorklistSCPOptions struct {
	MaxMatches            int
	MaxIdentifierBytes    int64
	MaxIdentifierElements int
	MaxIdentifierDepth    int
	MaxResponseBytes      int64
	MaxResponseElements   int
	MaxResponseDepth      int
}

func (options ModalityWorklistSCPOptions) normalized() (ModalityWorklistSCPOptions, error) {
	if options.MaxMatches < 0 || options.MaxIdentifierBytes < 0 || options.MaxIdentifierElements < 0 || options.MaxIdentifierDepth < 0 ||
		options.MaxResponseBytes < 0 || options.MaxResponseElements < 0 || options.MaxResponseDepth < 0 {
		return ModalityWorklistSCPOptions{}, fmt.Errorf("dicom dimse: MWL limits must not be negative")
	}
	if options.MaxMatches == 0 {
		options.MaxMatches = defaultModalityWorklistMaxMatches
	}
	if options.MaxIdentifierBytes == 0 {
		options.MaxIdentifierBytes = MaxIdentifierBytes
	}
	if options.MaxIdentifierElements == 0 {
		options.MaxIdentifierElements = defaultModalityWorklistIdentifierElements
	}
	if options.MaxIdentifierDepth == 0 {
		options.MaxIdentifierDepth = defaultModalityWorklistIdentifierDepth
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
	return options, nil
}

// ServeModalityWorklistCFind serves one MWL C-FIND request using finite default
// limits.
func ServeModalityWorklistCFind(ctx context.Context, assoc *ul.Association, pcID byte, handler ModalityWorklistHandler) error {
	return ServeModalityWorklistCFindWithOptions(ctx, assoc, pcID, handler, ModalityWorklistSCPOptions{})
}

// ServeModalityWorklistCFindWithOptions serves one MWL C-FIND request and
// streams provider candidates directly to pending responses.
func ServeModalityWorklistCFindWithOptions(ctx context.Context, assoc *ul.Association, pcID byte, handler ModalityWorklistHandler, options ModalityWorklistSCPOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	if handler == nil {
		return ErrModalityWorklistProvider
	}
	options, err := options.normalized()
	if err != nil {
		return err
	}
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return err
	}
	return serveModalityWorklistCFindCommand(ctx, assoc, pcID, command, handler, options)
}

func serveModalityWorklistCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler ModalityWorklistHandler, options ModalityWorklistSCPOptions) error {
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	if pc.AbstractSyntaxUID != ModalityWorklistFindSOPClassUID {
		return fmt.Errorf("dicom dimse: MWL presentation context has incompatible abstract syntax")
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return err
	}
	request, err := ParseCFindRequest(command)
	if err != nil {
		return err
	}
	if request.AffectedSOPClassUID != ModalityWorklistFindSOPClassUID {
		if sendErr := sendModalityWorklistFinal(ctx, assoc, pcID, *request, 0xA900, "Identifier does not match SOP Class", syntax); sendErr != nil {
			return sendErr
		}
		return ErrModalityWorklistIdentifier
	}
	if request.Priority > PriorityLow {
		if sendErr := sendModalityWorklistFinal(ctx, assoc, pcID, *request, CFindStatusUnableToProcess, "Unable to process", syntax); sendErr != nil {
			return sendErr
		}
		return ErrModalityWorklistIdentifier
	}
	rawIdentifier, err := receiveModalityWorklistIdentifier(ctx, assoc, pcID, syntax, options)
	if err != nil {
		if isModalityWorklistLimitError(err) {
			if sendErr := sendModalityWorklistFinal(ctx, assoc, pcID, *request, CFindStatusOutOfResources, "Out of resources", syntax); sendErr != nil {
				return sendErr
			}
			return ErrModalityWorklistResourceLimit
		}
		return err
	}
	if err := validateModalityWorklistObjectLimits(rawIdentifier, options.MaxIdentifierElements, options.MaxIdentifierDepth); err != nil {
		if sendErr := sendModalityWorklistFinal(ctx, assoc, pcID, *request, CFindStatusOutOfResources, "Out of resources", syntax); sendErr != nil {
			return sendErr
		}
		return err
	}
	identifier, err := ParseModalityWorklistIdentifier(rawIdentifier)
	if err != nil {
		if sendErr := sendModalityWorklistFinal(ctx, assoc, pcID, *request, 0xA900, "Identifier does not match SOP Class", syntax); sendErr != nil {
			return sendErr
		}
		return err
	}
	if !modalityWorklistHasMatchingKey(identifier.Query) {
		return sendModalityWorklistFinal(ctx, assoc, pcID, *request, StatusSuccess, "", syntax)
	}

	monitor := startSCPCancelMonitor(ctx, assoc, pcID, request.MessageID, ErrCFindCanceled, false)
	defer monitor.Stop()
	operationCtx := monitor.Context()
	var emitter modalityWorklistEmitter
	emitter.active = true
	emitter.yield = func(candidate *object.Object) error {
		if err := monitor.OperationError(); err != nil {
			return err
		}
		if emitter.matches >= options.MaxMatches {
			return ErrModalityWorklistResourceLimit
		}
		pendingStatus := modalityWorklistPendingStatus(identifier, candidate)
		projected, err := projectModalityWorklistResultWithLimits(identifier, candidate, options.MaxResponseElements, options.MaxResponseBytes, options.MaxResponseDepth)
		if err != nil {
			return err
		}
		if err := preflightModalityWorklistResponse(projected, syntax, options.MaxResponseElements, options.MaxResponseBytes, options.MaxResponseDepth); err != nil {
			return err
		}
		if err := sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
			return SendCFindResponseWithContext(responseCtx, assoc, pcID, CFindResponse{
				AffectedSOPClassUID:       ModalityWorklistFindSOPClassUID,
				MessageIDBeingRespondedTo: request.MessageID,
				Status:                    pendingStatus,
			}, projected, syntax)
		}); err != nil {
			return err
		}
		emitter.matches++
		return nil
	}
	_, handlerErr := runSCPHandler(operationCtx, assoc, scpControlsFromContext(ctx).CancelGrace, func(handlerCtx context.Context) (struct{}, error) {
		err := func() error {
			defer emitter.close()
			return handler.Find(handlerCtx, ModalityWorklistRequest{
				Request:               *request,
				Identifier:            identifier,
				RawIdentifier:         rawIdentifier,
				PresentationContextID: pcID,
				IdentifierSyntax:      syntax,
			}, emitter.emit)
		}()
		return struct{}{}, err
	})
	if monitorErr := monitor.Stop(); monitorErr != nil {
		handlerErr = errors.Join(handlerErr, monitorErr)
	}
	if handlerErr != nil {
		status, comment := modalityWorklistSCPStatus(handlerErr)
		if sendErr := sendModalityWorklistFinal(operationCtx, assoc, pcID, *request, status, comment, syntax); sendErr != nil {
			return sendErr
		}
		if errors.Is(handlerErr, ErrCFindCanceled) {
			return handlerErr
		}
		if errors.Is(handlerErr, ErrModalityWorklistResourceLimit) {
			return handlerErr
		}
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	return sendModalityWorklistFinal(ctx, assoc, pcID, *request, StatusSuccess, "", syntax)
}

type modalityWorklistEmitter struct {
	mu      sync.Mutex
	active  bool
	matches int
	yield   func(*object.Object) error
}

func (emitter *modalityWorklistEmitter) emit(candidate *object.Object) error {
	if emitter == nil {
		return ErrModalityWorklistProvider
	}
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if !emitter.active || emitter.yield == nil {
		return ErrModalityWorklistProvider
	}
	return emitter.yield(candidate)
}

func (emitter *modalityWorklistEmitter) close() {
	if emitter == nil {
		return
	}
	emitter.mu.Lock()
	emitter.active = false
	emitter.mu.Unlock()
}

func receiveModalityWorklistIdentifier(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, options ModalityWorklistSCPOptions) (*object.Object, error) {
	reader := newTypedPDataReaderWithContext(dataSetReadContext(ctx, assoc), assoc, pcID, false)
	identifier, err := object.ReadDataSetWithOptions(reader, syntax, object.ReadFileOptions{
		MaxElementBytes:  options.MaxIdentifierBytes,
		MaxTotalBytes:    options.MaxIdentifierBytes,
		MaxSequenceDepth: options.MaxIdentifierDepth,
		MaxElements:      options.MaxIdentifierElements,
	})
	if err != nil {
		return nil, fmt.Errorf("dicom dimse: receive MWL Identifier: %w", err)
	}
	return identifier, nil
}

func isModalityWorklistLimitError(err error) bool {
	return errors.Is(err, ErrModalityWorklistResourceLimit) ||
		errors.Is(err, parser.ErrMaxElementBytesExceeded) ||
		errors.Is(err, parser.ErrMaxTotalBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementsExceeded) ||
		errors.Is(err, parser.ErrMaxDepthExceeded)
}

func validateModalityWorklistObjectLimits(value *object.Object, maxElements, maxDepth int) error {
	if value == nil {
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	remaining := maxElements
	var walk func([]core.Element, int) error
	walk = func(elements []core.Element, depth int) error {
		if depth > maxDepth {
			return ErrModalityWorklistResourceLimit
		}
		for _, element := range elements {
			remaining--
			if remaining < 0 {
				return ErrModalityWorklistResourceLimit
			}
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok {
				continue
			}
			if depth+1 > maxDepth {
				return ErrModalityWorklistResourceLimit
			}
			for _, item := range sequence.Items {
				remaining--
				if remaining < 0 {
					return ErrModalityWorklistResourceLimit
				}
				if depth+2 > maxDepth {
					return ErrModalityWorklistResourceLimit
				}
				if err := walk(item.Elements, depth+2); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if maxElements <= 0 || maxDepth <= 0 || walk(value.Elements(), 0) != nil {
		return ErrModalityWorklistResourceLimit
	}
	return nil
}

func preflightModalityWorklistResponse(value *object.Object, syntax transfer.Syntax, maxElements int, maxBytes int64, maxDepth int) error {
	if err := validateModalityWorklistObjectLimits(value, maxElements, maxDepth); err != nil {
		return err
	}
	writer := &modalityWorklistLimitWriter{remaining: maxBytes}
	if maxBytes <= 0 {
		return ErrModalityWorklistResourceLimit
	}
	if err := object.WriteDataSet(writer, value, syntax); err != nil {
		if errors.Is(err, ErrModalityWorklistResourceLimit) {
			return ErrModalityWorklistResourceLimit
		}
		return fmt.Errorf("%w", ErrModalityWorklistProvider)
	}
	return nil
}

type modalityWorklistLimitWriter struct {
	remaining int64
}

func (writer *modalityWorklistLimitWriter) Write(value []byte) (int, error) {
	if writer == nil || int64(len(value)) > writer.remaining {
		return 0, ErrModalityWorklistResourceLimit
	}
	writer.remaining -= int64(len(value))
	return len(value), nil
}

var _ io.Writer = (*modalityWorklistLimitWriter)(nil)

func modalityWorklistHasMatchingKey(query ModalityWorklistQuery) bool {
	keys := []MWLKey{query.PatientName, query.PatientID, query.AccessionNumber, query.RequestedProcedureID, query.RequestedProcedureDescription}
	if query.ScheduledProcedureStep != nil {
		step := query.ScheduledProcedureStep
		keys = append(keys,
			step.ScheduledStationAETitle,
			step.Modality,
			step.ScheduledProcedureStepStartDate,
			step.ScheduledProcedureStepStartTime,
			step.ScheduledPerformingPhysicianName,
			step.ScheduledProcedureStepDescription,
			step.ScheduledProcedureStepID,
			step.ScheduledStationName,
			step.ScheduledProcedureStepLocation,
		)
	}
	for _, key := range keys {
		if mwlKeyHasMatchingValue(key) {
			return true
		}
	}
	return false
}

func modalityWorklistSCPStatus(err error) (uint16, string) {
	switch {
	case errors.Is(err, ErrCFindCanceled), errors.Is(err, context.Canceled):
		return CFindStatusCancel, "C-FIND canceled"
	case errors.Is(err, ErrModalityWorklistResourceLimit):
		return CFindStatusOutOfResources, "Out of resources"
	default:
		return CFindStatusUnableToProcess, "Unable to process"
	}
}

func sendModalityWorklistFinal(ctx context.Context, assoc *ul.Association, pcID byte, request CFindRequest, status uint16, comment string, syntax transfer.Syntax) error {
	return sendCFindFinal(ctx, assoc, pcID, request, ModalityWorklistFindSOPClassUID, status, comment, syntax)
}
