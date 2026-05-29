package dimse

import (
	"context"
	"fmt"
	"sync"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// MaxNormalizedDataSetBytes bounds normalized request/response datasets read by
// the generic SCU and SCP facades. Callers may lower or explicitly raise it.
const MaxNormalizedDataSetBytes int64 = 4 << 20

// NormalizedOperationOptions configures one generic N-DIMSE exchange.
type NormalizedOperationOptions struct {
	OperationOptions

	// PresentationContextID selects an already accepted context explicitly.
	PresentationContextID byte
	// PresentationContextAbstractSyntaxUID selects a context by abstract syntax
	// and explicitly permits a command SOP Class that differs from that syntax,
	// as required by some Meta SOP Classes. When empty, the command SOP Class is
	// used and must match the accepted context.
	PresentationContextAbstractSyntaxUID string
	// MaxResponseDataSetBytes bounds a returned Attribute List, Action Reply, or
	// Event Reply. Zero uses MaxNormalizedDataSetBytes.
	MaxResponseDataSetBytes int64
}

// NormalizedExchangeInfo describes the presentation context used by an
// exchange and the optional response dataset.
type NormalizedExchangeInfo struct {
	PresentationContext ul.AcceptedContext
	TransferSyntax      transfer.Syntax
	DataSet             *object.Object
}

type NormalizedEventReportResult struct {
	NormalizedExchangeInfo
	Response *NormalizedEventReportResponse
}

type NormalizedGetResult struct {
	NormalizedExchangeInfo
	Response *NormalizedGetResponse
}

type NormalizedSetResult struct {
	NormalizedExchangeInfo
	Response *NormalizedSetResponse
}

type NormalizedActionResult struct {
	NormalizedExchangeInfo
	Response *NormalizedActionResponse
}

type NormalizedCreateResult struct {
	NormalizedExchangeInfo
	Response *NormalizedCreateResponse
}

type NormalizedDeleteResult struct {
	NormalizedExchangeInfo
	Response *NormalizedDeleteResponse
}

// NormalizedClient executes one confirmed N-DIMSE operation at a time on an
// established association. Asynchronous operation multiplexing remains the
// responsibility of the separately negotiated runtime.
type NormalizedClient struct {
	Assoc *ul.Association

	mu        sync.Mutex
	messageID uint16
}

func NewNormalizedClient(assoc *ul.Association) *NormalizedClient {
	return &NormalizedClient{Assoc: assoc}
}

func (c *NormalizedClient) EventReport(ctx context.Context, req NormalizedEventReportRequest, eventInformation *object.Object) (NormalizedEventReportResult, error) {
	return c.EventReportWithOptions(NormalizedOperationOptions{OperationOptions: OperationOptions{Context: ctx}}, req, eventInformation)
}

func (c *NormalizedClient) EventReportWithOptions(options NormalizedOperationOptions, req NormalizedEventReportRequest, eventInformation *object.Object) (NormalizedEventReportResult, error) {
	const service = "N-EVENT-REPORT"
	if err := requireNormalizedUID(service, "AffectedSOPClassUID", req.AffectedSOPClassUID); err != nil {
		return NormalizedEventReportResult{}, err
	}
	if err := requireNormalizedUID(service, "AffectedSOPInstanceUID", req.AffectedSOPInstanceUID); err != nil {
		return NormalizedEventReportResult{}, err
	}
	if err := validateNormalizedDataSetArgument(service, req.CommandDataSetType, eventInformation); err != nil {
		return NormalizedEventReportResult{}, err
	}
	req.MessageID = c.assignMessageID(req.MessageID)
	typeID := req.EventTypeID
	exchange, err := c.exchange(options, normalizedExchangeRequest{
		service:                   service,
		sopClassUID:               req.AffectedSOPClassUID,
		sopInstanceUID:            req.AffectedSOPInstanceUID,
		messageID:                 req.MessageID,
		commandSet:                req.CommandSet(),
		dataSet:                   eventInformation,
		responseCommandField:      NEventReportRSP,
		requestTypeIDOrNil:        &typeID,
		requireTypeIDWithResponse: true,
		parseResponse: func(command *object.Object) (any, normalizedResponseEnvelope, error) {
			response, err := ParseNormalizedEventReportResponse(command)
			if err != nil {
				return nil, normalizedResponseEnvelope{}, err
			}
			return response, normalizedResponseEnvelope{
				messageID: response.MessageIDBeingRespondedTo, dataSetType: response.CommandDataSetType,
				status: response.Status, classUID: response.AffectedSOPClassUID, instanceUID: response.AffectedSOPInstanceUID,
				typeIDOrNil: response.EventTypeIDOrNil, statusFields: response.StatusFields,
			}, nil
		},
	})
	result := NormalizedEventReportResult{NormalizedExchangeInfo: exchange.info}
	if exchange.response != nil {
		result.Response = exchange.response.(*NormalizedEventReportResponse)
	}
	return result, err
}

func (c *NormalizedClient) Get(ctx context.Context, req NormalizedGetRequest) (NormalizedGetResult, error) {
	return c.GetWithOptions(NormalizedOperationOptions{OperationOptions: OperationOptions{Context: ctx}}, req)
}

func (c *NormalizedClient) GetWithOptions(options NormalizedOperationOptions, req NormalizedGetRequest) (NormalizedGetResult, error) {
	const service = "N-GET"
	if err := validateNormalizedRequestedUIDs(service, req.RequestedSOPClassUID, req.RequestedSOPInstanceUID); err != nil {
		return NormalizedGetResult{}, err
	}
	req.MessageID = c.assignMessageID(req.MessageID)
	exchange, err := c.exchange(options, normalizedExchangeRequest{
		service: service, sopClassUID: req.RequestedSOPClassUID, sopInstanceUID: req.RequestedSOPInstanceUID,
		messageID: req.MessageID, commandSet: req.CommandSet(), responseCommandField: NGetRSP,
		requireDataSetOnSuccess: true,
		parseResponse: func(command *object.Object) (any, normalizedResponseEnvelope, error) {
			response, err := ParseNormalizedGetResponse(command)
			if err != nil {
				return nil, normalizedResponseEnvelope{}, err
			}
			return response, normalizedResponseEnvelope{
				messageID: response.MessageIDBeingRespondedTo, dataSetType: response.CommandDataSetType,
				status: response.Status, classUID: response.AffectedSOPClassUID, instanceUID: response.AffectedSOPInstanceUID,
				statusFields: response.StatusFields,
			}, nil
		},
	})
	result := NormalizedGetResult{NormalizedExchangeInfo: exchange.info}
	if exchange.response != nil {
		result.Response = exchange.response.(*NormalizedGetResponse)
	}
	return result, err
}

func (c *NormalizedClient) Set(ctx context.Context, req NormalizedSetRequest, modificationList *object.Object) (NormalizedSetResult, error) {
	return c.SetWithOptions(NormalizedOperationOptions{OperationOptions: OperationOptions{Context: ctx}}, req, modificationList)
}

func (c *NormalizedClient) SetWithOptions(options NormalizedOperationOptions, req NormalizedSetRequest, modificationList *object.Object) (NormalizedSetResult, error) {
	const service = "N-SET"
	if err := validateNormalizedRequestedUIDs(service, req.RequestedSOPClassUID, req.RequestedSOPInstanceUID); err != nil {
		return NormalizedSetResult{}, err
	}
	if modificationList == nil {
		return NormalizedSetResult{}, fmt.Errorf("dicom dimse: %s Modification List dataset is required", service)
	}
	req.MessageID = c.assignMessageID(req.MessageID)
	exchange, err := c.exchange(options, normalizedExchangeRequest{
		service: service, sopClassUID: req.RequestedSOPClassUID, sopInstanceUID: req.RequestedSOPInstanceUID,
		messageID: req.MessageID, commandSet: req.CommandSet(), dataSet: modificationList, responseCommandField: NSetRSP,
		parseResponse: func(command *object.Object) (any, normalizedResponseEnvelope, error) {
			response, err := ParseNormalizedSetResponse(command)
			if err != nil {
				return nil, normalizedResponseEnvelope{}, err
			}
			return response, normalizedResponseEnvelope{
				messageID: response.MessageIDBeingRespondedTo, dataSetType: response.CommandDataSetType,
				status: response.Status, classUID: response.AffectedSOPClassUID, instanceUID: response.AffectedSOPInstanceUID,
				statusFields: response.StatusFields,
			}, nil
		},
	})
	result := NormalizedSetResult{NormalizedExchangeInfo: exchange.info}
	if exchange.response != nil {
		result.Response = exchange.response.(*NormalizedSetResponse)
	}
	return result, err
}

func (c *NormalizedClient) Action(ctx context.Context, req NormalizedActionRequest, actionInformation *object.Object) (NormalizedActionResult, error) {
	return c.ActionWithOptions(NormalizedOperationOptions{OperationOptions: OperationOptions{Context: ctx}}, req, actionInformation)
}

func (c *NormalizedClient) ActionWithOptions(options NormalizedOperationOptions, req NormalizedActionRequest, actionInformation *object.Object) (NormalizedActionResult, error) {
	const service = "N-ACTION"
	if err := validateNormalizedRequestedUIDs(service, req.RequestedSOPClassUID, req.RequestedSOPInstanceUID); err != nil {
		return NormalizedActionResult{}, err
	}
	if err := validateNormalizedDataSetArgument(service, req.CommandDataSetType, actionInformation); err != nil {
		return NormalizedActionResult{}, err
	}
	req.MessageID = c.assignMessageID(req.MessageID)
	typeID := req.ActionTypeID
	exchange, err := c.exchange(options, normalizedExchangeRequest{
		service: service, sopClassUID: req.RequestedSOPClassUID, sopInstanceUID: req.RequestedSOPInstanceUID,
		messageID: req.MessageID, commandSet: req.CommandSet(), dataSet: actionInformation, responseCommandField: NActionRSP,
		requestTypeIDOrNil:        &typeID,
		requireTypeIDWithResponse: true,
		parseResponse: func(command *object.Object) (any, normalizedResponseEnvelope, error) {
			response, err := ParseNormalizedActionResponse(command)
			if err != nil {
				return nil, normalizedResponseEnvelope{}, err
			}
			return response, normalizedResponseEnvelope{
				messageID: response.MessageIDBeingRespondedTo, dataSetType: response.CommandDataSetType,
				status: response.Status, classUID: response.AffectedSOPClassUID, instanceUID: response.AffectedSOPInstanceUID,
				typeIDOrNil: response.ActionTypeIDOrNil, statusFields: response.StatusFields,
			}, nil
		},
	})
	result := NormalizedActionResult{NormalizedExchangeInfo: exchange.info}
	if exchange.response != nil {
		result.Response = exchange.response.(*NormalizedActionResponse)
	}
	return result, err
}

func (c *NormalizedClient) Create(ctx context.Context, req NormalizedCreateRequest, attributeList *object.Object) (NormalizedCreateResult, error) {
	return c.CreateWithOptions(NormalizedOperationOptions{OperationOptions: OperationOptions{Context: ctx}}, req, attributeList)
}

func (c *NormalizedClient) CreateWithOptions(options NormalizedOperationOptions, req NormalizedCreateRequest, attributeList *object.Object) (NormalizedCreateResult, error) {
	const service = "N-CREATE"
	if err := requireNormalizedUID(service, "AffectedSOPClassUID", req.AffectedSOPClassUID); err != nil {
		return NormalizedCreateResult{}, err
	}
	if err := validateNormalizedDataSetArgument(service, req.CommandDataSetType, attributeList); err != nil {
		return NormalizedCreateResult{}, err
	}
	req.MessageID = c.assignMessageID(req.MessageID)
	exchange, err := c.exchange(options, normalizedExchangeRequest{
		service: service, sopClassUID: req.AffectedSOPClassUID, sopInstanceUID: req.AffectedSOPInstanceUID,
		messageID: req.MessageID, commandSet: req.CommandSet(), dataSet: attributeList, responseCommandField: NCreateRSP,
		requireAssignedInstanceOnSuccess: req.AffectedSOPInstanceUID == "",
		parseResponse: func(command *object.Object) (any, normalizedResponseEnvelope, error) {
			response, err := ParseNormalizedCreateResponse(command)
			if err != nil {
				return nil, normalizedResponseEnvelope{}, err
			}
			return response, normalizedResponseEnvelope{
				messageID: response.MessageIDBeingRespondedTo, dataSetType: response.CommandDataSetType,
				status: response.Status, classUID: response.AffectedSOPClassUID, instanceUID: response.AffectedSOPInstanceUID,
				statusFields: response.StatusFields,
			}, nil
		},
	})
	result := NormalizedCreateResult{NormalizedExchangeInfo: exchange.info}
	if exchange.response != nil {
		result.Response = exchange.response.(*NormalizedCreateResponse)
	}
	return result, err
}

func (c *NormalizedClient) Delete(ctx context.Context, req NormalizedDeleteRequest) (NormalizedDeleteResult, error) {
	return c.DeleteWithOptions(NormalizedOperationOptions{OperationOptions: OperationOptions{Context: ctx}}, req)
}

func (c *NormalizedClient) DeleteWithOptions(options NormalizedOperationOptions, req NormalizedDeleteRequest) (NormalizedDeleteResult, error) {
	const service = "N-DELETE"
	if err := validateNormalizedRequestedUIDs(service, req.RequestedSOPClassUID, req.RequestedSOPInstanceUID); err != nil {
		return NormalizedDeleteResult{}, err
	}
	req.MessageID = c.assignMessageID(req.MessageID)
	exchange, err := c.exchange(options, normalizedExchangeRequest{
		service: service, sopClassUID: req.RequestedSOPClassUID, sopInstanceUID: req.RequestedSOPInstanceUID,
		messageID: req.MessageID, commandSet: req.CommandSet(), responseCommandField: NDeleteRSP,
		parseResponse: func(command *object.Object) (any, normalizedResponseEnvelope, error) {
			response, err := ParseNormalizedDeleteResponse(command)
			if err != nil {
				return nil, normalizedResponseEnvelope{}, err
			}
			return response, normalizedResponseEnvelope{
				messageID: response.MessageIDBeingRespondedTo, dataSetType: NoDataSet,
				status: response.Status, classUID: response.AffectedSOPClassUID, instanceUID: response.AffectedSOPInstanceUID,
				statusFields: response.StatusFields,
			}, nil
		},
	})
	result := NormalizedDeleteResult{NormalizedExchangeInfo: exchange.info}
	if exchange.response != nil {
		result.Response = exchange.response.(*NormalizedDeleteResponse)
	}
	return result, err
}

type normalizedExchangeRequest struct {
	service                          string
	sopClassUID                      string
	sopInstanceUID                   string
	messageID                        uint16
	commandSet                       []core.Element
	dataSet                          *object.Object
	responseCommandField             uint16
	requestTypeIDOrNil               *uint16
	requireTypeIDWithResponse        bool
	requireDataSetOnSuccess          bool
	requireAssignedInstanceOnSuccess bool
	parseResponse                    func(*object.Object) (any, normalizedResponseEnvelope, error)
}

type normalizedResponseEnvelope struct {
	messageID    uint16
	dataSetType  uint16
	status       uint16
	classUID     string
	instanceUID  string
	typeIDOrNil  *uint16
	statusFields *NormalizedStatusFields
}

type normalizedExchangeResult struct {
	response any
	info     NormalizedExchangeInfo
}

func (c *NormalizedClient) exchange(options NormalizedOperationOptions, request normalizedExchangeRequest) (normalizedExchangeResult, error) {
	var result normalizedExchangeResult
	if c == nil {
		return result, fmt.Errorf("dicom dimse: normalized client is nil")
	}
	if c.Assoc == nil {
		return result, fmt.Errorf("dicom dimse: normalized client association is nil")
	}
	if request.parseResponse == nil {
		return result, fmt.Errorf("dicom dimse: %s response parser is nil", request.service)
	}
	options.OperationOptions = operationOptionsWithDefaultPolicy(options.OperationOptions, OperationErrorPolicyAbort)
	ctx, cancel := operationContext(options.OperationOptions)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, newOperationError(request.service, err, false)
	}

	releaseOperation, err := beginAssociationOperation(c.Assoc)
	if err != nil {
		return result, newOperationError(request.service, err, false)
	}
	defer releaseOperation()

	pc, syntax, err := normalizedClientPresentationContext(c.Assoc, request.sopClassUID, options)
	if err != nil {
		return result, err
	}
	result.info.PresentationContext = pc
	result.info.TransferSyntax = syntax

	if err := SendCommandSetWithContext(ctx, c.Assoc, pc.ID, request.commandSet); err != nil {
		return result, c.normalizedOperationError(options, request.service, err)
	}
	if request.dataSet != nil {
		if err := SendDataSetWithContext(ctx, c.Assoc, pc.ID, request.dataSet, syntax); err != nil {
			return result, c.normalizedOperationError(options, request.service, err)
		}
	}

	responseCtx, responseCancel := operationResponseContext(ctx, options.ResponseTimeout)
	defer responseCancel()
	command, err := receiveCommandSetWithContext(responseCtx, c.Assoc, pc.ID)
	if err != nil {
		return result, c.normalizedOperationError(options, request.service, err)
	}
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return result, c.normalizedOperationError(options, request.service, err)
	}
	if field != request.responseCommandField {
		err = fmt.Errorf("dicom dimse: %s response command field 0x%04X, want 0x%04X", request.service, field, request.responseCommandField)
		return result, c.normalizedOperationError(options, request.service, err)
	}
	response, envelope, err := request.parseResponse(command)
	if err != nil {
		return result, c.normalizedOperationError(options, request.service, err)
	}
	result.response = response
	if err := validateNormalizedResponseCorrelation(request, envelope); err != nil {
		return result, c.normalizedOperationError(options, request.service, err)
	}
	if normalizedHasDataSet(envelope.dataSetType) {
		limit := options.MaxResponseDataSetBytes
		if limit == 0 {
			limit = MaxNormalizedDataSetBytes
		}
		dataSet, err := receiveDataSetWithContextAndLimit(responseCtx, c.Assoc, pc.ID, syntax, limit)
		if err != nil {
			return result, c.normalizedOperationError(options, request.service, err)
		}
		result.info.DataSet = dataSet
	}
	if request.requireDataSetOnSuccess && envelope.status == StatusSuccess && result.info.DataSet == nil {
		err := fmt.Errorf("dicom dimse: %s success response requires an Attribute List dataset", request.service)
		return result, c.normalizedOperationError(options, request.service, err)
	}
	if request.requireTypeIDWithResponse {
		if result.info.DataSet != nil && envelope.status == StatusSuccess && envelope.typeIDOrNil == nil {
			err := fmt.Errorf("dicom dimse: %s response dataset requires the conditional type ID", request.service)
			return result, c.normalizedOperationError(options, request.service, err)
		}
		if envelope.typeIDOrNil != nil && request.requestTypeIDOrNil != nil && *envelope.typeIDOrNil != *request.requestTypeIDOrNil {
			err := fmt.Errorf("dicom dimse: %s response type ID %d, want %d", request.service, *envelope.typeIDOrNil, *request.requestTypeIDOrNil)
			return result, c.normalizedOperationError(options, request.service, err)
		}
	}
	if request.requireAssignedInstanceOnSuccess && envelope.status == StatusSuccess && envelope.instanceUID == "" {
		err := fmt.Errorf("dicom dimse: %s success response must return the SCP-assigned SOP Instance UID", request.service)
		return result, c.normalizedOperationError(options, request.service, err)
	}
	if err := CheckNormalizedStatus(request.service, envelope.status, envelope.statusFields); err != nil {
		return result, err
	}
	return result, nil
}

func (c *NormalizedClient) normalizedOperationError(options NormalizedOperationOptions, service string, err error) error {
	return applyOperationErrorPolicy(c.Assoc, options.ErrorPolicy, newOperationError(service, err, true))
}

func (c *NormalizedClient) assignMessageID(messageID uint16) uint16 {
	if messageID != 0 || c == nil {
		return messageID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageID++
	if c.messageID == 0 {
		c.messageID++
	}
	return c.messageID
}

func normalizedClientPresentationContext(assoc *ul.Association, commandSOPClassUID string, options NormalizedOperationOptions) (ul.AcceptedContext, transfer.Syntax, error) {
	lookupUID := commandSOPClassUID
	explicitAbstractSyntax := options.PresentationContextAbstractSyntaxUID
	if explicitAbstractSyntax != "" {
		lookupUID = explicitAbstractSyntax
	}
	var (
		pc  ul.AcceptedContext
		err error
	)
	if options.PresentationContextID != 0 {
		pc, err = AcceptedContextByID(assoc, options.PresentationContextID)
	} else {
		pc, err = AcceptedContextForSOPClass(assoc, lookupUID)
	}
	if err != nil {
		return ul.AcceptedContext{}, transfer.Syntax{}, err
	}
	if explicitAbstractSyntax != "" && pc.AbstractSyntaxUID != explicitAbstractSyntax {
		return ul.AcceptedContext{}, transfer.Syntax{}, &NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: explicitAbstractSyntax}
	}
	if explicitAbstractSyntax == "" && pc.AbstractSyntaxUID != commandSOPClassUID {
		return ul.AcceptedContext{}, transfer.Syntax{}, &NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: commandSOPClassUID}
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return ul.AcceptedContext{}, transfer.Syntax{}, err
	}
	return pc, syntax, nil
}

func validateNormalizedRequestedUIDs(service, classUID, instanceUID string) error {
	if err := requireNormalizedUID(service, "RequestedSOPClassUID", classUID); err != nil {
		return err
	}
	return requireNormalizedUID(service, "RequestedSOPInstanceUID", instanceUID)
}

func validateNormalizedDataSetArgument(service string, dataSetType uint16, dataSet *object.Object) error {
	hasDataSet := normalizedHasDataSet(dataSetType)
	if hasDataSet && dataSet == nil {
		return fmt.Errorf("dicom dimse: %s CommandDataSetType 0x%04X requires a dataset", service, dataSetType)
	}
	if !hasDataSet && dataSet != nil {
		return fmt.Errorf("dicom dimse: %s CommandDataSetType 0x%04X forbids a dataset", service, dataSetType)
	}
	return nil
}

func validateNormalizedResponseCorrelation(request normalizedExchangeRequest, response normalizedResponseEnvelope) error {
	if response.messageID != request.messageID {
		return fmt.Errorf("dicom dimse: %s response MessageIDBeingRespondedTo %d, want %d", request.service, response.messageID, request.messageID)
	}
	if response.classUID != "" && response.classUID != request.sopClassUID {
		return fmt.Errorf("dicom dimse: %s response AffectedSOPClassUID %q, want %q", request.service, response.classUID, request.sopClassUID)
	}
	if request.sopInstanceUID != "" && response.instanceUID != "" && response.instanceUID != request.sopInstanceUID {
		return fmt.Errorf("dicom dimse: %s response AffectedSOPInstanceUID %q, want %q", request.service, response.instanceUID, request.sopInstanceUID)
	}
	return nil
}
