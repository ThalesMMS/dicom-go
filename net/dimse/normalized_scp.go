package dimse

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	// StatusNoSuchSOPClass is the DIMSE-N failure used when the command SOP
	// Class is not valid for the selected presentation context or service.
	StatusNoSuchSOPClass uint16 = 0x0118
	// StatusNoSuchSOPInstance reports an unknown normalized SOP Instance.
	StatusNoSuchSOPInstance uint16 = 0x0112
	// StatusNoSuchEventType reports an unsupported N-EVENT-REPORT type.
	StatusNoSuchEventType uint16 = 0x0113
	// StatusNoSuchActionType reports an unsupported N-ACTION type.
	StatusNoSuchActionType uint16 = 0x0123
	// StatusUnrecognizedOperation is returned when the association supports the
	// abstract syntax but no handler is registered for the received N-service.
	StatusUnrecognizedOperation uint16 = 0x0211

	// MaxNormalizedDataSetElements bounds normalized request decoding before a
	// service-specific handler sees the dataset.
	MaxNormalizedDataSetElements = 16_384
	// MaxNormalizedDataSetDepth bounds normalized request sequence nesting.
	MaxNormalizedDataSetDepth = 32
)

// NormalizedPresentationContextPolicy validates command SOP Classes against an
// accepted presentation context. A callback is necessary for Meta SOP Classes
// whose abstract syntax intentionally differs from the command SOP Class.
type NormalizedPresentationContextPolicy func(pc ul.AcceptedContext, commandSOPClassUID string) error

type NormalizedEventReportHandler func(context.Context, NormalizedEventReportRequest, *object.Object) (NormalizedEventReportSCPResult, error)
type NormalizedGetHandler func(context.Context, NormalizedGetRequest) (NormalizedGetSCPResult, error)
type NormalizedSetHandler func(context.Context, NormalizedSetRequest, *object.Object) (NormalizedSetSCPResult, error)
type NormalizedActionHandler func(context.Context, NormalizedActionRequest, *object.Object) (NormalizedActionSCPResult, error)
type NormalizedCreateHandler func(context.Context, NormalizedCreateRequest, *object.Object) (NormalizedCreateSCPResult, error)
type NormalizedDeleteHandler func(context.Context, NormalizedDeleteRequest) (NormalizedDeleteSCPResult, error)

type normalizedActionResponseHook struct {
	after func(context.Context, NormalizedActionResponse, bool) error
}

func (e *normalizedActionResponseHook) Error() string {
	return "dicom dimse: normalized action response hook"
}

type NormalizedEventReportSCPResult struct {
	Response NormalizedEventReportResponse
	DataSet  *object.Object
}

type NormalizedGetSCPResult struct {
	Response NormalizedGetResponse
	DataSet  *object.Object
}

type NormalizedSetSCPResult struct {
	Response NormalizedSetResponse
	DataSet  *object.Object
}

type NormalizedActionSCPResult struct {
	Response NormalizedActionResponse
	DataSet  *object.Object
}

type NormalizedCreateSCPResult struct {
	Response NormalizedCreateResponse
	DataSet  *object.Object
}

type NormalizedDeleteSCPResult struct {
	Response NormalizedDeleteResponse
}

// NormalizedSCPOptions registers per-association handlers without mutable
// package globals. Missing handlers receive StatusUnrecognizedOperation.
type NormalizedSCPOptions struct {
	EventReportHandler NormalizedEventReportHandler
	GetHandler         NormalizedGetHandler
	SetHandler         NormalizedSetHandler
	ActionHandler      NormalizedActionHandler
	CreateHandler      NormalizedCreateHandler
	DeleteHandler      NormalizedDeleteHandler

	MaxDataSetBytes           int64
	PresentationContextPolicy NormalizedPresentationContextPolicy
}

// NormalizedSCPHandlerError reports an application handler failure after a
// DIMSE response was successfully sent. It is safe for association loops to
// continue after this error.
type NormalizedSCPHandlerError struct {
	Service string
	Err     error
}

func (e *NormalizedSCPHandlerError) Error() string {
	if e == nil {
		return "dicom dimse: normalized SCP handler failed"
	}
	return fmt.Sprintf("dicom dimse: %s handler failed: %v", e.Service, e.Err)
}

func (e *NormalizedSCPHandlerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type normalizedSCPStatusError struct {
	err error
}

func (e *normalizedSCPStatusError) Error() string { return e.err.Error() }
func (e *normalizedSCPStatusError) Unwrap() error { return e.err }

func isHandledNormalizedSCPError(err error) bool {
	var handlerErr *NormalizedSCPHandlerError
	if errors.As(err, &handlerErr) {
		return true
	}
	var statusErr *normalizedSCPStatusError
	return errors.As(err, &statusErr)
}

// RegisterNormalizedHandlers connects all six N-service request fields to the
// existing Dispatcher receive loop.
func RegisterNormalizedHandlers(dispatcher *Dispatcher, options NormalizedSCPOptions) error {
	if dispatcher == nil {
		return fmt.Errorf("dicom dimse: nil dispatcher")
	}
	for _, field := range []uint16{NEventReportRQ, NGetRQ, NSetRQ, NActionRQ, NCreateRQ, NDeleteRQ} {
		commandField := field
		dispatcher.Handle(commandField, func(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object) error {
			err := ServeNormalizedCommand(ctx, assoc, pcID, command, options)
			if isHandledNormalizedSCPError(err) {
				return nil
			}
			return err
		})
	}
	return nil
}

// ServeNormalizedCommand handles one already-decoded N-service request on the
// same association receive stack that supplied command.
func ServeNormalizedCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, options NormalizedSCPOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return err
	}
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return err
	}
	switch field {
	case NEventReportRQ:
		return serveNormalizedEventReport(ctx, assoc, pc, syntax, command, options)
	case NGetRQ:
		return serveNormalizedGet(ctx, assoc, pc, syntax, command, options)
	case NSetRQ:
		return serveNormalizedSet(ctx, assoc, pc, syntax, command, options)
	case NActionRQ:
		return serveNormalizedAction(ctx, assoc, pc, syntax, command, options)
	case NCreateRQ:
		return serveNormalizedCreate(ctx, assoc, pc, syntax, command, options)
	case NDeleteRQ:
		return serveNormalizedDelete(ctx, assoc, pc, syntax, command, options)
	default:
		return &UnexpectedCommandError{CommandField: field}
	}
}

func serveNormalizedEventReport(ctx context.Context, assoc *ul.Association, pc ul.AcceptedContext, syntax transfer.Syntax, command *object.Object, options NormalizedSCPOptions) error {
	const service = "N-EVENT-REPORT"
	request, err := ParseNormalizedEventReportRequest(command)
	if err != nil {
		return err
	}
	dataSet, err := receiveNormalizedRequestDataSet(ctx, assoc, pc.ID, syntax, request.CommandDataSetType, options)
	if err != nil {
		return err
	}
	if err := validateNormalizedSCPPresentationContext(pc, request.AffectedSOPClassUID, options.PresentationContextPolicy); err != nil {
		response := NormalizedEventReportResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusNoSuchSOPClass}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, &normalizedSCPStatusError{err: err})
	}
	if options.EventReportHandler == nil {
		response := NormalizedEventReportResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusUnrecognizedOperation}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, nil)
	}
	handlerCtx := withNormalizedRequestInfo(ctx, NormalizedRequestInfo{PresentationContext: pc, TransferSyntax: syntax})
	result, handlerErr := options.EventReportHandler(handlerCtx, *request, dataSet)
	response := result.Response
	correlationErr := errors.Join(
		correlateNormalizedResponse(&response.AffectedSOPClassUID, &response.AffectedSOPInstanceUID, &response.MessageIDBeingRespondedTo, request.AffectedSOPClassUID, request.AffectedSOPInstanceUID, request.MessageID, false),
		correlateNormalizedTypeID(&response.EventTypeIDOrNil, request.EventTypeID, "Event Type ID"),
	)
	if correlationErr != nil {
		response.Status = StatusProcessingFailure
		result.DataSet = nil
		handlerErr = errors.Join(handlerErr, correlationErr)
	}
	if result.DataSet != nil {
		response.CommandDataSetType = DataSetPresent
		if response.Status == StatusSuccess && response.EventTypeIDOrNil == nil {
			typeID := request.EventTypeID
			response.EventTypeIDOrNil = &typeID
		}
	} else {
		response.CommandDataSetType = NoDataSet
	}
	handlerErr = normalizeHandlerStatus(&response.Status, handlerErr)
	return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), result.DataSet, normalizedHandlerError(service, handlerErr))
}

func serveNormalizedGet(ctx context.Context, assoc *ul.Association, pc ul.AcceptedContext, syntax transfer.Syntax, command *object.Object, options NormalizedSCPOptions) error {
	const service = "N-GET"
	request, err := ParseNormalizedGetRequest(command)
	if err != nil {
		return err
	}
	if err := validateNormalizedSCPPresentationContext(pc, request.RequestedSOPClassUID, options.PresentationContextPolicy); err != nil {
		response := NormalizedGetResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusNoSuchSOPClass}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, &normalizedSCPStatusError{err: err})
	}
	if options.GetHandler == nil {
		response := NormalizedGetResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusUnrecognizedOperation}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, nil)
	}
	handlerCtx := withNormalizedRequestInfo(ctx, NormalizedRequestInfo{PresentationContext: pc, TransferSyntax: syntax})
	result, handlerErr := options.GetHandler(handlerCtx, *request)
	response := result.Response
	if correlationErr := correlateNormalizedResponse(&response.AffectedSOPClassUID, &response.AffectedSOPInstanceUID, &response.MessageIDBeingRespondedTo, request.RequestedSOPClassUID, request.RequestedSOPInstanceUID, request.MessageID, false); correlationErr != nil {
		response.Status = StatusProcessingFailure
		result.DataSet = nil
		handlerErr = errors.Join(handlerErr, correlationErr)
	}
	if result.DataSet != nil {
		response.CommandDataSetType = DataSetPresent
	} else {
		response.CommandDataSetType = NoDataSet
		if response.Status == StatusSuccess && handlerErr == nil {
			handlerErr = fmt.Errorf("N-GET success requires an Attribute List dataset")
		}
	}
	handlerErr = normalizeHandlerStatus(&response.Status, handlerErr)
	return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), result.DataSet, normalizedHandlerError(service, handlerErr))
}

func serveNormalizedSet(ctx context.Context, assoc *ul.Association, pc ul.AcceptedContext, syntax transfer.Syntax, command *object.Object, options NormalizedSCPOptions) error {
	const service = "N-SET"
	request, err := ParseNormalizedSetRequest(command)
	if err != nil {
		return err
	}
	modificationList, err := receiveNormalizedRequestDataSet(ctx, assoc, pc.ID, syntax, DataSetPresent, options)
	if err != nil {
		return err
	}
	if err := validateNormalizedSCPPresentationContext(pc, request.RequestedSOPClassUID, options.PresentationContextPolicy); err != nil {
		response := NormalizedSetResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusNoSuchSOPClass}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, &normalizedSCPStatusError{err: err})
	}
	if options.SetHandler == nil {
		response := NormalizedSetResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusUnrecognizedOperation}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, nil)
	}
	handlerCtx := withNormalizedRequestInfo(ctx, NormalizedRequestInfo{PresentationContext: pc, TransferSyntax: syntax})
	result, handlerErr := options.SetHandler(handlerCtx, *request, modificationList)
	response := result.Response
	if correlationErr := correlateNormalizedResponse(&response.AffectedSOPClassUID, &response.AffectedSOPInstanceUID, &response.MessageIDBeingRespondedTo, request.RequestedSOPClassUID, request.RequestedSOPInstanceUID, request.MessageID, false); correlationErr != nil {
		response.Status = StatusProcessingFailure
		result.DataSet = nil
		handlerErr = errors.Join(handlerErr, correlationErr)
	}
	response.CommandDataSetType = normalizedDataSetType(result.DataSet != nil)
	handlerErr = normalizeHandlerStatus(&response.Status, handlerErr)
	return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), result.DataSet, normalizedHandlerError(service, handlerErr))
}

func serveNormalizedAction(ctx context.Context, assoc *ul.Association, pc ul.AcceptedContext, syntax transfer.Syntax, command *object.Object, options NormalizedSCPOptions) error {
	const service = "N-ACTION"
	request, err := ParseNormalizedActionRequest(command)
	if err != nil {
		return err
	}
	actionInformation, err := receiveNormalizedRequestDataSet(ctx, assoc, pc.ID, syntax, request.CommandDataSetType, options)
	if err != nil {
		return err
	}
	if err := validateNormalizedSCPPresentationContext(pc, request.RequestedSOPClassUID, options.PresentationContextPolicy); err != nil {
		response := NormalizedActionResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusNoSuchSOPClass}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, &normalizedSCPStatusError{err: err})
	}
	if options.ActionHandler == nil {
		response := NormalizedActionResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusUnrecognizedOperation}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, nil)
	}
	handlerCtx := withNormalizedRequestInfo(ctx, NormalizedRequestInfo{PresentationContext: pc, TransferSyntax: syntax})
	result, handlerErr := options.ActionHandler(handlerCtx, *request, actionInformation)
	var responseHook *normalizedActionResponseHook
	if errors.As(handlerErr, &responseHook) {
		handlerErr = withoutNormalizedActionResponseHook(handlerErr)
	}
	response := result.Response
	correlationErr := errors.Join(
		correlateNormalizedResponse(&response.AffectedSOPClassUID, &response.AffectedSOPInstanceUID, &response.MessageIDBeingRespondedTo, request.RequestedSOPClassUID, request.RequestedSOPInstanceUID, request.MessageID, false),
		correlateNormalizedTypeID(&response.ActionTypeIDOrNil, request.ActionTypeID, "Action Type ID"),
	)
	if correlationErr != nil {
		response.Status = StatusProcessingFailure
		result.DataSet = nil
		handlerErr = errors.Join(handlerErr, correlationErr)
	}
	if result.DataSet != nil {
		response.CommandDataSetType = DataSetPresent
		if response.Status == StatusSuccess && response.ActionTypeIDOrNil == nil {
			typeID := request.ActionTypeID
			response.ActionTypeIDOrNil = &typeID
		}
	} else {
		response.CommandDataSetType = NoDataSet
	}
	handlerErr = normalizeHandlerStatus(&response.Status, handlerErr)
	sendErr := sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), result.DataSet, normalizedHandlerError(service, handlerErr))
	responseSent := sendErr == nil || isHandledNormalizedSCPError(sendErr)
	if responseHook != nil && responseHook.after != nil {
		observerErr := responseHook.after(ctx, response, responseSent)
		if observerErr != nil {
			observerErr = normalizedHandlerError(service, observerErr)
		}
		return errors.Join(sendErr, observerErr)
	}
	return sendErr
}

func withoutNormalizedActionResponseHook(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*normalizedActionResponseHook); ok {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		remaining := make([]error, 0, len(joined.Unwrap()))
		for _, nested := range joined.Unwrap() {
			if nested = withoutNormalizedActionResponseHook(nested); nested != nil {
				remaining = append(remaining, nested)
			}
		}
		return errors.Join(remaining...)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		var hook *normalizedActionResponseHook
		if errors.As(wrapped.Unwrap(), &hook) {
			return withoutNormalizedActionResponseHook(wrapped.Unwrap())
		}
	}
	return err
}

func serveNormalizedCreate(ctx context.Context, assoc *ul.Association, pc ul.AcceptedContext, syntax transfer.Syntax, command *object.Object, options NormalizedSCPOptions) error {
	const service = "N-CREATE"
	request, err := ParseNormalizedCreateRequest(command)
	if err != nil {
		return err
	}
	attributeList, err := receiveNormalizedRequestDataSet(ctx, assoc, pc.ID, syntax, request.CommandDataSetType, options)
	if err != nil {
		return err
	}
	if err := validateNormalizedSCPPresentationContext(pc, request.AffectedSOPClassUID, options.PresentationContextPolicy); err != nil {
		response := NormalizedCreateResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusNoSuchSOPClass}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, &normalizedSCPStatusError{err: err})
	}
	if options.CreateHandler == nil {
		response := NormalizedCreateResponse{MessageIDBeingRespondedTo: request.MessageID, CommandDataSetType: NoDataSet, Status: StatusUnrecognizedOperation}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, nil)
	}
	handlerCtx := withNormalizedRequestInfo(ctx, NormalizedRequestInfo{PresentationContext: pc, TransferSyntax: syntax})
	result, handlerErr := options.CreateHandler(handlerCtx, *request, attributeList)
	response := result.Response
	if correlationErr := correlateNormalizedResponse(&response.AffectedSOPClassUID, &response.AffectedSOPInstanceUID, &response.MessageIDBeingRespondedTo, request.AffectedSOPClassUID, request.AffectedSOPInstanceUID, request.MessageID, true); correlationErr != nil {
		response.Status = StatusProcessingFailure
		result.DataSet = nil
		handlerErr = errors.Join(handlerErr, correlationErr)
	}
	response.CommandDataSetType = normalizedDataSetType(result.DataSet != nil)
	if response.Status == StatusSuccess && response.AffectedSOPInstanceUID == "" && handlerErr == nil {
		handlerErr = fmt.Errorf("N-CREATE success requires an Affected SOP Instance UID")
	}
	handlerErr = normalizeHandlerStatus(&response.Status, handlerErr)
	return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), result.DataSet, normalizedHandlerError(service, handlerErr))
}

func serveNormalizedDelete(ctx context.Context, assoc *ul.Association, pc ul.AcceptedContext, syntax transfer.Syntax, command *object.Object, options NormalizedSCPOptions) error {
	const service = "N-DELETE"
	request, err := ParseNormalizedDeleteRequest(command)
	if err != nil {
		return err
	}
	if err := validateNormalizedSCPPresentationContext(pc, request.RequestedSOPClassUID, options.PresentationContextPolicy); err != nil {
		response := NormalizedDeleteResponse{MessageIDBeingRespondedTo: request.MessageID, Status: StatusNoSuchSOPClass}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, &normalizedSCPStatusError{err: err})
	}
	if options.DeleteHandler == nil {
		response := NormalizedDeleteResponse{MessageIDBeingRespondedTo: request.MessageID, Status: StatusUnrecognizedOperation}
		return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, nil)
	}
	handlerCtx := withNormalizedRequestInfo(ctx, NormalizedRequestInfo{PresentationContext: pc, TransferSyntax: syntax})
	result, handlerErr := options.DeleteHandler(handlerCtx, *request)
	response := result.Response
	if correlationErr := correlateNormalizedResponse(&response.AffectedSOPClassUID, &response.AffectedSOPInstanceUID, &response.MessageIDBeingRespondedTo, request.RequestedSOPClassUID, request.RequestedSOPInstanceUID, request.MessageID, false); correlationErr != nil {
		response.Status = StatusProcessingFailure
		handlerErr = errors.Join(handlerErr, correlationErr)
	}
	handlerErr = normalizeHandlerStatus(&response.Status, handlerErr)
	return sendNormalizedSCPResponse(ctx, assoc, pc.ID, syntax, response.CommandSet(), nil, normalizedHandlerError(service, handlerErr))
}

func receiveNormalizedRequestDataSet(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, dataSetType uint16, options NormalizedSCPOptions) (*object.Object, error) {
	if !normalizedHasDataSet(dataSetType) {
		return nil, nil
	}
	if options.MaxDataSetBytes == 0 {
		options.MaxDataSetBytes = MaxNormalizedDataSetBytes
	}
	return receiveDataSetWithContextAndLimits(ctx, assoc, pcID, syntax, options.MaxDataSetBytes, MaxNormalizedDataSetElements, MaxNormalizedDataSetDepth)
}

func sendNormalizedSCPResponse(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, commandSet []core.Element, dataSet *object.Object, resultErr error) error {
	responseCtx, cancel := scpResponseContext(ctx, assoc)
	defer cancel()
	if err := SendCommandSetWithContext(responseCtx, assoc, pcID, commandSet); err != nil {
		return err
	}
	if dataSet != nil {
		if err := SendDataSetWithContext(responseCtx, assoc, pcID, dataSet, syntax); err != nil {
			return err
		}
	}
	return resultErr
}

func validateNormalizedSCPPresentationContext(pc ul.AcceptedContext, commandSOPClassUID string, policy NormalizedPresentationContextPolicy) error {
	if policy != nil {
		return policy(pc, commandSOPClassUID)
	}
	if pc.AbstractSyntaxUID != commandSOPClassUID {
		return &NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: commandSOPClassUID}
	}
	return nil
}

func correlateNormalizedResponse(classUID, instanceUID *string, messageID *uint16, requestClassUID, requestInstanceUID string, requestMessageID uint16, allowAssignedInstance bool) error {
	var correlationErr error
	if *classUID != "" && *classUID != requestClassUID {
		correlationErr = errors.Join(correlationErr, fmt.Errorf("handler response SOP Class UID %q does not match request %q", *classUID, requestClassUID))
	}
	*classUID = requestClassUID
	if requestInstanceUID != "" {
		if *instanceUID != "" && *instanceUID != requestInstanceUID {
			correlationErr = errors.Join(correlationErr, fmt.Errorf("handler response SOP Instance UID %q does not match request %q", *instanceUID, requestInstanceUID))
		}
		*instanceUID = requestInstanceUID
	} else if !allowAssignedInstance && *instanceUID != "" {
		correlationErr = errors.Join(correlationErr, fmt.Errorf("handler response SOP Instance UID %q has no corresponding request value", *instanceUID))
		*instanceUID = ""
	}
	*messageID = requestMessageID
	return correlationErr
}

func correlateNormalizedTypeID(responseTypeID **uint16, requestTypeID uint16, name string) error {
	if *responseTypeID == nil || **responseTypeID == requestTypeID {
		return nil
	}
	responseValue := **responseTypeID
	correctedTypeID := requestTypeID
	*responseTypeID = &correctedTypeID
	return fmt.Errorf("handler response %s %d does not match request %d", name, responseValue, requestTypeID)
}

func normalizeHandlerStatus(status *uint16, handlerErr error) error {
	if handlerErr != nil && *status == StatusSuccess {
		*status = StatusProcessingFailure
	}
	return handlerErr
}

func normalizedHandlerError(service string, err error) error {
	if err == nil {
		return nil
	}
	return &NormalizedSCPHandlerError{Service: service, Err: err}
}
