package ups

import (
	"context"
	"errors"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	ActionChangeState   uint16 = 1
	ActionRequestCancel uint16 = 2
	ActionSubscribe     uint16 = 3
	ActionUnsubscribe   uint16 = 4
	ActionSuspendGlobal uint16 = 5
)

type ChangeStateInformation struct {
	State          State
	TransactionUID string
}

func BuildChangeStateInformation(state State, transactionUID string) (*object.Object, error) {
	if !validState(state) || !validUID(transactionUID) {
		return nil, ErrInvalidDataSet
	}
	return NewDataSet(
		StringElement(TagProcedureStepState, core.VRCS, string(state)),
		StringElement(TagTransactionUID, core.VRUI, core.NormalizeUID(transactionUID)),
	), nil
}

func ParseChangeStateInformation(dataSet *object.Object) (ChangeStateInformation, error) {
	if dataSet == nil {
		return ChangeStateInformation{}, statusError("N-ACTION Change UPS State", StatusMissingAttribute, ErrInvalidDataSet)
	}
	stateElement, statePresent := dataSetElement(dataSet.ToDataSet(), TagProcedureStepState)
	if !statePresent {
		return ChangeStateInformation{}, statusError("N-ACTION Change UPS State", StatusMissingAttribute, ErrInvalidDataSet)
	}
	state := stateElement.StringValue()
	if strings.TrimSpace(state) == "" {
		return ChangeStateInformation{}, statusError("N-ACTION Change UPS State", StatusMissingAttributeValue, ErrInvalidDataSet)
	}
	if !validState(State(state)) {
		return ChangeStateInformation{}, statusError("N-ACTION Change UPS State", StatusOnlyScheduledViaCreate, ErrInvalidDataSet)
	}
	transactionUID, err := dataSet.LookupString(TagTransactionUID)
	if err != nil || !validUID(transactionUID) {
		return ChangeStateInformation{}, statusError("N-ACTION Change UPS State", StatusIncorrectTransactionUID, ErrInvalidDataSet)
	}
	elements := dataSet.Elements()
	if len(elements) != 2 {
		return ChangeStateInformation{}, ErrInvalidDataSet
	}
	seen := make(map[core.Tag]bool, 2)
	for _, element := range elements {
		if element.Tag() != TagProcedureStepState && element.Tag() != TagTransactionUID || seen[element.Tag()] {
			return ChangeStateInformation{}, ErrInvalidDataSet
		}
		seen[element.Tag()] = true
	}
	return ChangeStateInformation{State: State(state), TransactionUID: core.NormalizeUID(transactionUID)}, nil
}

func BuildSubscriptionInformation(receivingAETitle string, deletionLock bool, matchingKeys map[string][]string) (*object.Object, error) {
	if !validAETitle(receivingAETitle) || len(matchingKeys) != 0 {
		return nil, ErrInvalidDataSet
	}
	lock := "FALSE"
	if deletionLock {
		lock = "TRUE"
	}
	return NewDataSet(
		StringElement(TagReceivingAE, core.VRAE, receivingAETitle),
		StringElement(TagDeletionLock, core.VRLO, lock),
	), nil
}

func BuildUnsubscriptionInformation(receivingAETitle string) (*object.Object, error) {
	if !validAETitle(receivingAETitle) {
		return nil, ErrInvalidDataSet
	}
	return NewDataSet(StringElement(TagReceivingAE, core.VRAE, receivingAETitle)), nil
}

func parseSubscriptionInformation(dataSet *object.Object, requireLock bool) (receivingAE string, deletionLock bool, matchingKeys map[string][]string, err error) {
	receivingAE, err = requiredSubscriptionString(dataSet, TagReceivingAE)
	if err != nil {
		return "", false, nil, err
	}
	if !validAETitle(receivingAE) {
		return "", false, nil, ErrInvalidDataSet
	}
	allowed := map[core.Tag]bool{TagReceivingAE: true}
	if requireLock {
		value, valueErr := requiredSubscriptionString(dataSet, TagDeletionLock)
		if valueErr != nil {
			return "", false, nil, valueErr
		}
		if value != "TRUE" && value != "FALSE" {
			return "", false, nil, ErrInvalidDataSet
		}
		deletionLock = value == "TRUE"
		allowed[TagDeletionLock] = true
	}
	matchingKeys = make(map[string][]string)
	for _, element := range dataSet.Elements() {
		if allowed[element.Tag()] {
			continue
		}
		matchingKeys[element.Tag().String()] = element.StringValues()
	}
	return receivingAE, deletionLock, matchingKeys, nil
}

func requiredSubscriptionString(dataSet *object.Object, tag core.Tag) (string, error) {
	if dataSet == nil {
		return "", statusError("N-ACTION UPS Watch", StatusMissingAttribute, ErrInvalidDataSet)
	}
	element, present := dataSetElement(dataSet.ToDataSet(), tag)
	if !present {
		return "", statusError("N-ACTION UPS Watch", StatusMissingAttribute, ErrInvalidDataSet)
	}
	value := element.StringValue()
	if strings.TrimSpace(value) == "" {
		return "", statusError("N-ACTION UPS Watch", StatusMissingAttributeValue, ErrInvalidDataSet)
	}
	return value, nil
}

// NormalizedOptions returns UPS handlers for an established association. The
// association is borrowed; it is used only for the triggering AE identity.
func (service *Service) NormalizedOptions(assoc *ul.Association) dimse.NormalizedSCPOptions {
	options := dimse.NormalizedSCPOptions{
		MaxDataSetBytes: service.limits.MaxDataSetBytes,
		PresentationContextPolicy: func(pc ul.AcceptedContext, commandSOPClassUID string) error {
			if commandSOPClassUID != PushSOPClassUID {
				return &dimse.NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: commandSOPClassUID}
			}
			switch pc.AbstractSyntaxUID {
			case PushSOPClassUID, PullSOPClassUID, WatchSOPClassUID, EventSOPClassUID:
				return nil
			default:
				return &dimse.NormalizedPresentationContextError{PresentationContextID: pc.ID, AbstractSyntaxUID: pc.AbstractSyntaxUID, CommandSOPClassUID: commandSOPClassUID}
			}
		},
	}
	options.CreateHandler = service.normalizedCreateHandler
	options.GetHandler = service.normalizedGetHandler
	options.SetHandler = service.normalizedSetHandler
	options.ActionHandler = func(ctx context.Context, request dimse.NormalizedActionRequest, information *object.Object) (dimse.NormalizedActionSCPResult, error) {
		return service.normalizedActionHandler(ctx, assoc, request, information)
	}
	if service.eventHandler != nil {
		options.EventReportHandler = service.normalizedEventReportHandler
	}
	return options
}

func (service *Service) normalizedCreateHandler(ctx context.Context, request dimse.NormalizedCreateRequest, attributes *object.Object) (dimse.NormalizedCreateSCPResult, error) {
	if err := requireNormalizedAbstractSyntax(ctx, "N-CREATE", PushSOPClassUID); err != nil {
		return dimse.NormalizedCreateSCPResult{Response: dimse.NormalizedCreateResponse{Status: dimse.StatusUnrecognizedOperation}}, err
	}
	step, err := service.Create(ctx, CreateRequest{SOPInstanceUID: request.AffectedSOPInstanceUID, Attributes: attributes})
	status, handlerErr := statusForHandlerError(err)
	return dimse.NormalizedCreateSCPResult{Response: dimse.NormalizedCreateResponse{
		Status: status, AffectedSOPInstanceUID: step.SOPInstanceUID,
	}}, handlerErr
}

func (service *Service) normalizedGetHandler(ctx context.Context, request dimse.NormalizedGetRequest) (dimse.NormalizedGetSCPResult, error) {
	if err := requireNormalizedAbstractSyntax(ctx, "N-GET", PushSOPClassUID, PullSOPClassUID, WatchSOPClassUID); err != nil {
		return dimse.NormalizedGetSCPResult{Response: dimse.NormalizedGetResponse{Status: dimse.StatusUnrecognizedOperation}}, err
	}
	for _, tag := range request.AttributeIdentifierList {
		if tag == TagTransactionUID || tag == TagSOPClassUID || tag == TagSOPInstanceUID {
			return dimse.NormalizedGetSCPResult{Response: dimse.NormalizedGetResponse{Status: StatusNoSuchAttribute}}, ErrInvalidDataSet
		}
	}
	step, err := service.Get(ctx, request.RequestedSOPInstanceUID)
	if err != nil {
		status, handlerErr := statusForHandlerError(err)
		return dimse.NormalizedGetSCPResult{Response: dimse.NormalizedGetResponse{Status: status}}, handlerErr
	}
	requested := make(map[core.Tag]bool, len(request.AttributeIdentifierList))
	for _, tag := range request.AttributeIdentifierList {
		requested[tag] = true
	}
	projected := core.DataSet{}
	missing := false
	if len(requested) == 0 {
		for _, element := range step.Attributes.Elements {
			if element.Tag() != TagTransactionUID && element.Tag() != TagSOPClassUID && element.Tag() != TagSOPInstanceUID {
				projected.Elements = append(projected.Elements, element)
			}
		}
	} else {
		for _, tag := range request.AttributeIdentifierList {
			element, ok := dataSetElement(step.Attributes, tag)
			if !ok {
				missing = true
				continue
			}
			projected.Elements = append(projected.Elements, element)
		}
	}
	status := StatusSuccess
	if missing {
		status = StatusRequestedOptionalUnsupported
	}
	return dimse.NormalizedGetSCPResult{
		Response: dimse.NormalizedGetResponse{Status: status},
		DataSet:  object.FromDataSet(projected, std.Dictionary),
	}, nil
}

func (service *Service) normalizedSetHandler(ctx context.Context, request dimse.NormalizedSetRequest, modifications *object.Object) (dimse.NormalizedSetSCPResult, error) {
	if err := requireNormalizedAbstractSyntax(ctx, "N-SET", PullSOPClassUID); err != nil {
		return dimse.NormalizedSetSCPResult{Response: dimse.NormalizedSetResponse{Status: dimse.StatusUnrecognizedOperation}}, err
	}
	transactionUID := ""
	if modifications != nil {
		transactionUID, _ = modifications.LookupString(TagTransactionUID)
	}
	_, err := service.Set(ctx, SetRequest{SOPInstanceUID: request.RequestedSOPInstanceUID, TransactionUID: transactionUID, Modifications: modifications})
	status, handlerErr := statusForHandlerError(err)
	return dimse.NormalizedSetSCPResult{Response: dimse.NormalizedSetResponse{Status: status}}, handlerErr
}

func (service *Service) normalizedActionHandler(ctx context.Context, assoc *ul.Association, request dimse.NormalizedActionRequest, information *object.Object) (dimse.NormalizedActionSCPResult, error) {
	status := StatusSuccess
	var operationErr error
	if information != nil {
		if err := validateDataSet(ctx, information.ToDataSet(), service.limits); err != nil {
			status, operationErr = dimse.StatusProcessingFailure, err
		}
	}
	if operationErr != nil {
		actionType := request.ActionTypeID
		return dimse.NormalizedActionSCPResult{Response: dimse.NormalizedActionResponse{Status: status, ActionTypeIDOrNil: &actionType}}, operationErr
	}
	switch request.ActionTypeID {
	case ActionChangeState:
		if err := requireNormalizedAbstractSyntax(ctx, "N-ACTION Change UPS State", PullSOPClassUID); err != nil {
			status, operationErr = dimse.StatusUnrecognizedOperation, err
			break
		}
		if _, err := service.Get(ctx, request.RequestedSOPInstanceUID); err != nil {
			status, operationErr = statusForHandlerError(err)
			break
		}
		parsed, err := ParseChangeStateInformation(information)
		if err != nil {
			status, operationErr = statusForHandlerError(err)
			break
		}
		_, operationErr = service.ChangeState(ctx, ChangeStateRequest{SOPInstanceUID: request.RequestedSOPInstanceUID, State: parsed.State, TransactionUID: parsed.TransactionUID})
		status, operationErr = statusForHandlerError(operationErr)
	case ActionRequestCancel:
		if err := requireNormalizedAbstractSyntax(ctx, "N-ACTION Request UPS Cancel", PushSOPClassUID, WatchSOPClassUID); err != nil {
			status, operationErr = dimse.StatusUnrecognizedOperation, err
			break
		}
		requestingAE := ""
		if assoc != nil {
			requestingAE = assoc.CallingAETitle
		}
		_, operationErr = service.RequestCancel(ctx, CancelRequest{SOPInstanceUID: request.RequestedSOPInstanceUID, RequestingAETitle: requestingAE, Information: information})
		status, operationErr = statusForHandlerError(operationErr)
	case ActionSubscribe:
		if err := requireNormalizedAbstractSyntax(ctx, "N-ACTION Subscribe", WatchSOPClassUID); err != nil {
			status, operationErr = dimse.StatusUnrecognizedOperation, err
			break
		}
		receivingAE, deletionLock, matchingKeys, err := parseSubscriptionInformation(information, true)
		if err != nil {
			status, operationErr = statusForHandlerError(err)
			break
		}
		result, err := service.Subscribe(ctx, SubscribeRequest{SOPInstanceUID: request.RequestedSOPInstanceUID, ReceivingAETitle: receivingAE, DeletionLock: deletionLock, MatchingKeys: matchingKeys})
		if err != nil {
			status, operationErr = statusForHandlerError(err)
		} else {
			status = result.Status
		}
	case ActionUnsubscribe:
		if err := requireNormalizedAbstractSyntax(ctx, "N-ACTION Unsubscribe", WatchSOPClassUID); err != nil {
			status, operationErr = dimse.StatusUnrecognizedOperation, err
			break
		}
		receivingAE, _, matchingKeys, err := parseSubscriptionInformation(information, false)
		if err != nil {
			status, operationErr = statusForHandlerError(err)
			break
		}
		if len(matchingKeys) != 0 {
			status, operationErr = dimse.StatusProcessingFailure, ErrInvalidDataSet
			break
		}
		operationErr = service.Unsubscribe(ctx, UnsubscribeRequest{SOPInstanceUID: request.RequestedSOPInstanceUID, ReceivingAETitle: receivingAE})
		status, operationErr = statusForHandlerError(operationErr)
	case ActionSuspendGlobal:
		if err := requireNormalizedAbstractSyntax(ctx, "N-ACTION Suspend Global", WatchSOPClassUID); err != nil {
			status, operationErr = dimse.StatusUnrecognizedOperation, err
			break
		}
		if request.RequestedSOPInstanceUID != GlobalSubscriptionSOPInstanceUID {
			status, operationErr = StatusActionNotAppropriate, ErrInvalidDataSet
			break
		}
		receivingAE, _, matchingKeys, err := parseSubscriptionInformation(information, false)
		if err != nil {
			status, operationErr = statusForHandlerError(err)
			break
		}
		if len(matchingKeys) != 0 {
			status, operationErr = dimse.StatusProcessingFailure, ErrInvalidDataSet
			break
		}
		operationErr = service.SuspendGlobal(ctx, receivingAE)
		status, operationErr = statusForHandlerError(operationErr)
	default:
		status, operationErr = dimse.StatusNoSuchActionType, ErrInvalidDataSet
	}
	actionType := request.ActionTypeID
	return dimse.NormalizedActionSCPResult{Response: dimse.NormalizedActionResponse{Status: status, ActionTypeIDOrNil: &actionType}}, operationErr
}

func requireNormalizedAbstractSyntax(ctx context.Context, operation string, allowed ...string) error {
	info, ok := dimse.NormalizedRequestInfoFromContext(ctx)
	if !ok {
		return nil
	}
	for _, uid := range allowed {
		if info.PresentationContext.AbstractSyntaxUID == uid {
			return nil
		}
	}
	return statusError(operation, dimse.StatusUnrecognizedOperation, ErrInvalidState)
}

func statusForHandlerError(err error) (uint16, error) {
	if err == nil {
		return StatusSuccess, nil
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status, err
	}
	return dimse.StatusProcessingFailure, err
}

func validAETitle(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 16 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e || value[index] == '\\' {
			return false
		}
	}
	return true
}

func (service *Service) normalizedEventReportHandler(ctx context.Context, request dimse.NormalizedEventReportRequest, information *object.Object) (dimse.NormalizedEventReportSCPResult, error) {
	if err := requireNormalizedAbstractSyntax(ctx, "N-EVENT-REPORT", EventSOPClassUID); err != nil {
		return dimse.NormalizedEventReportSCPResult{Response: dimse.NormalizedEventReportResponse{Status: dimse.StatusUnrecognizedOperation}}, err
	}
	if request.EventTypeID < uint16(EventStateReport) || request.EventTypeID > uint16(EventUPSAssigned) {
		return dimse.NormalizedEventReportSCPResult{Response: dimse.NormalizedEventReportResponse{Status: dimse.StatusNoSuchEventType}}, ErrInvalidDataSet
	}
	if information == nil {
		return dimse.NormalizedEventReportSCPResult{Response: dimse.NormalizedEventReportResponse{Status: dimse.StatusProcessingFailure}}, ErrInvalidDataSet
	}
	if err := validateDataSet(ctx, information.ToDataSet(), service.limits); err != nil {
		return dimse.NormalizedEventReportSCPResult{Response: dimse.NormalizedEventReportResponse{Status: dimse.StatusProcessingFailure}}, err
	}
	if err := validateEventReportInformation(EventType(request.EventTypeID), information); err != nil {
		return dimse.NormalizedEventReportSCPResult{Response: dimse.NormalizedEventReportResponse{Status: dimse.StatusProcessingFailure}}, err
	}
	err := callEventHandler(service.eventHandler, ctx, ReceivedEvent{SOPInstanceUID: request.AffectedSOPInstanceUID, Type: EventType(request.EventTypeID), Information: information})
	status := StatusSuccess
	if err != nil {
		status = dimse.StatusProcessingFailure
	}
	eventType := request.EventTypeID
	return dimse.NormalizedEventReportSCPResult{Response: dimse.NormalizedEventReportResponse{Status: status, EventTypeIDOrNil: &eventType}}, err
}

func validateEventReportInformation(eventType EventType, information *object.Object) error {
	if information == nil {
		return ErrInvalidDataSet
	}
	dataSet := information.ToDataSet()
	switch eventType {
	case EventStateReport:
		state, stateOK := dataSetString(dataSet, TagProcedureStepState)
		readiness, readinessOK := dataSetElement(dataSet, TagInputReadinessState)
		if !stateOK || !validState(State(state)) || !readinessOK || validateSetElement(readiness) != nil {
			return ErrInvalidDataSet
		}
	case EventCancelRequested:
		requestingAE, ok := dataSetString(dataSet, TagRequestingAE)
		if !ok || !validAETitle(requestingAE) || validateCancelInformation(dataSet, true) != nil {
			return ErrInvalidDataSet
		}
	case EventProgressReport:
		progress, ok := dataSetElement(dataSet, TagProcedureStepProgressInformationSequence)
		sequence, sequenceOK := progress.Value.(core.SequenceValue)
		if !ok || !sequenceOK || len(sequence.Items) != 1 || validateProgressInformationElement(progress) != nil {
			return ErrInvalidDataSet
		}
	case EventSCPStatusChange:
		status, statusOK := dataSetString(dataSet, TagSCPStatus)
		subscriptions, subscriptionsOK := dataSetString(dataSet, TagSubscriptionListStatus)
		steps, stepsOK := dataSetString(dataSet, TagUnifiedProcedureStepListStatus)
		if !statusOK || !subscriptionsOK || !stepsOK || !validSCPStatusChange(SCPStatusChange{
			Status: SCPStatus(status), SubscriptionListStatus: ListStatus(subscriptions), UPSListStatus: ListStatus(steps),
		}) {
			return ErrInvalidDataSet
		}
	case EventUPSAssigned:
		if _, wrapped := dataSetElement(dataSet, TagScheduledHumanPerformersSequence); wrapped {
			return ErrInvalidDataSet
		}
		station := hasNonEmptySequence(dataSet, TagScheduledStationNameCodeSequence)
		_, stationPresent := dataSetElement(dataSet, TagScheduledStationNameCodeSequence)
		human := hasNonEmptySequence(dataSet, TagHumanPerformerCodeSequence)
		_, humanPresent := dataSetElement(dataSet, TagHumanPerformerCodeSequence)
		organization, organizationPresent := dataSetString(dataSet, TagHumanPerformerOrganization)
		organizationPresent = organizationPresent && strings.TrimSpace(organization) != ""
		if stationPresent && !station || humanPresent && !human || human != organizationPresent {
			return ErrInvalidDataSet
		}
		if stationElement, present := dataSetElement(dataSet, TagScheduledStationNameCodeSequence); present && !requiredSingleCodeSequence(stationElement) {
			return ErrInvalidDataSet
		}
		if humanElement, present := dataSetElement(dataSet, TagHumanPerformerCodeSequence); present && !requiredSingleCodeSequence(humanElement) {
			return ErrInvalidDataSet
		}
	default:
		return ErrInvalidDataSet
	}
	return nil
}

func callEventHandler(handler EventHandler, ctx context.Context, event ReceivedEvent) (err error) {
	defer func() {
		if recover() != nil {
			err = &DeliveryError{Class: DeliveryFailureCallbackUnknown, Err: ErrDeliveryFailed}
		}
	}()
	if err := handler.HandleUPSEvent(ctx, event); err != nil {
		return &DeliveryError{Class: DeliveryFailureCallbackUnknown, Err: err}
	}
	return nil
}
