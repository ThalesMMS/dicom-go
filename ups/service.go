package ups

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
)

type Service struct {
	store                Store
	limits               Limits
	clock                func() time.Time
	defaultWorklistLabel string
	callbackResolver     CallbackResolver
	associationDialer    AssociationDialer
	eventHandler         EventHandler
	callbackCallingAE    string
	deliveryLimits       DeliveryLimits
	refuseDeletionLocks  bool
	fallbackReceivingAEs []string
}

func NewService(store Store, options ServiceOptions) (*Service, error) {
	if store == nil {
		return nil, ErrConflict
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.DefaultWorklistLabel == "" {
		options.DefaultWorklistLabel = "DEFAULT"
	}
	options.DefaultWorklistLabel = strings.TrimSpace(options.DefaultWorklistLabel)
	if !validDefaultWorklistLabel(options.DefaultWorklistLabel) {
		return nil, ErrInvalidDataSet
	}
	deliveryLimits, err := normalizeDeliveryLimits(options.DeliveryLimits)
	if err != nil {
		return nil, err
	}
	if options.AssociationDialer == nil {
		options.AssociationDialer = defaultAssociationDialer{}
	}
	if options.CallbackCallingAE == "" {
		options.CallbackCallingAE = "DICOMGO_UPS"
	}
	options.CallbackCallingAE = strings.TrimSpace(options.CallbackCallingAE)
	if !validAETitle(options.CallbackCallingAE) {
		return nil, ErrInvalidDataSet
	}
	fallbackReceivingAEs := make([]string, 0, len(options.FallbackReceivingAETitles))
	seenFallback := make(map[string]bool, len(options.FallbackReceivingAETitles))
	for _, ae := range options.FallbackReceivingAETitles {
		ae = strings.TrimSpace(ae)
		if !validAETitle(ae) {
			return nil, ErrInvalidDataSet
		}
		if !seenFallback[ae] {
			seenFallback[ae] = true
			fallbackReceivingAEs = append(fallbackReceivingAEs, ae)
		}
	}
	return &Service{
		store: store, limits: limits, clock: options.Clock, defaultWorklistLabel: options.DefaultWorklistLabel,
		callbackResolver: options.CallbackResolver, associationDialer: options.AssociationDialer,
		eventHandler:      options.EventHandler,
		callbackCallingAE: options.CallbackCallingAE,
		deliveryLimits:    deliveryLimits, refuseDeletionLocks: options.RefuseDeletionLocks,
		fallbackReceivingAEs: fallbackReceivingAEs,
	}, nil
}

func (service *Service) Create(ctx context.Context, request CreateRequest) (Step, error) {
	ctx = normalizeContext(ctx)
	if !validUID(request.SOPInstanceUID) {
		return Step{}, statusError("N-CREATE", StatusInvalidObjectInstance, ErrInvalidDataSet)
	}
	if request.Attributes == nil {
		return Step{}, statusError("N-CREATE", StatusMissingAttribute, ErrInvalidDataSet)
	}
	attributes, err := cloneDataSet(ctx, request.Attributes.ToDataSet(), service.limits)
	if err != nil {
		return Step{}, classifyDataSetError("N-CREATE", err)
	}
	if err := validateDataSet(ctx, attributes, service.limits); err != nil {
		return Step{}, classifyDataSetError("N-CREATE", err)
	}
	if err := service.validateCreateAttributes(&attributes); err != nil {
		return Step{}, err
	}
	now := service.clock().UTC()
	putElement(&attributes, StringElement(TagSOPClassUID, core.VRUI, PushSOPClassUID))
	putElement(&attributes, StringElement(TagSOPInstanceUID, core.VRUI, request.SOPInstanceUID))
	putElement(&attributes, StringElement(TagScheduledProcedureStepModificationDateTime, core.VRDT, dicomDateTime(now)))
	putElement(&attributes, StringElement(TagProcedureStepState, core.VRCS, string(StateScheduled)))
	putElement(&attributes, StringElement(TagTransactionUID, core.VRUI, ""))
	if err := validateDataSetEncoding(ctx, attributes); err != nil {
		return Step{}, classifyDataSetError("N-CREATE", err)
	}
	step := Step{
		SOPInstanceUID: request.SOPInstanceUID, State: StateScheduled,
		Attributes: attributes, CreatedAt: now, UpdatedAt: now,
	}
	events := service.eventsForCreate(step, now)
	result, err := service.store.CommitUPS(ctx, CommitRequest{
		Step: &StepMutation{ExpectedVersion: 0, Next: step}, Events: events,
	})
	if errors.Is(err, ErrConflict) {
		return Step{}, statusError("N-CREATE", StatusDuplicateSOPInstance, err)
	}
	if err != nil {
		return Step{}, safeRepositoryError(err)
	}
	if result.Step == nil {
		return Step{}, safeRepositoryError(ErrConflict)
	}
	return *result.Step, nil
}

func (service *Service) Get(ctx context.Context, sopInstanceUID string) (Step, error) {
	step, err := service.store.GetStep(normalizeContext(ctx), sopInstanceUID)
	if errors.Is(err, ErrNotFound) {
		return Step{}, statusError("N-GET", StatusUPSNotFound, err)
	}
	if err != nil {
		return Step{}, safeRepositoryError(err)
	}
	return step, nil
}

func (service *Service) Set(ctx context.Context, request SetRequest) (Step, error) {
	ctx = normalizeContext(ctx)
	if request.Modifications == nil {
		return Step{}, classifyDataSetError("N-SET", ErrInvalidDataSet)
	}
	modifications, err := cloneDataSet(ctx, request.Modifications.ToDataSet(), service.limits)
	if err != nil {
		return Step{}, classifyDataSetError("N-SET", err)
	}
	if err := validateDataSet(ctx, modifications, service.limits); err != nil {
		return Step{}, classifyDataSetError("N-SET", err)
	}
	if transactionUID, present := dataSetString(modifications, TagTransactionUID); present {
		if request.TransactionUID == "" {
			request.TransactionUID = transactionUID
		} else if core.NormalizeUID(transactionUID) != core.NormalizeUID(request.TransactionUID) {
			return Step{}, statusError("N-SET", StatusIncorrectTransactionUID, ErrConflict)
		}
		removeElement(&modifications, TagTransactionUID)
	}
	return service.updateStep(ctx, "N-SET", request.SOPInstanceUID, func(step Step) (Step, []Event, error) {
		if step.State == StateCompleted || step.State == StateCanceled {
			return Step{}, nil, statusError("N-SET", StatusMayNoLongerBeUpdated, ErrInvalidState)
		}
		if step.State == StateInProgress && core.NormalizeUID(step.TransactionUID) != core.NormalizeUID(request.TransactionUID) {
			return Step{}, nil, statusError("N-SET", StatusIncorrectTransactionUID, ErrConflict)
		}
		if step.State == StateScheduled && request.TransactionUID != "" {
			return Step{}, nil, statusError("N-SET", StatusIncorrectTransactionUID, ErrConflict)
		}
		effectiveModifications := core.DataSet{}
		for _, element := range modifications.Elements {
			if !settableUPSAttributes[element.Tag()] {
				return Step{}, nil, statusError("N-SET", StatusNoSuchAttribute, ErrInvalidDataSet)
			}
			if err := validateSetElement(element); err != nil {
				return Step{}, nil, statusError("N-SET", StatusInvalidAttributeValue, err)
			}
			current, present := dataSetElement(step.Attributes, element.Tag())
			if !present {
				return Step{}, nil, statusError("N-SET", StatusNoSuchAttribute, ErrInvalidDataSet)
			}
			if reflect.DeepEqual(current, element) {
				continue
			}
			putElement(&step.Attributes, element)
			effectiveModifications.Elements = append(effectiveModifications.Elements, element)
		}
		if len(effectiveModifications.Elements) == 0 {
			return step, nil, errNoChange
		}
		now := service.clock().UTC()
		putElement(&step.Attributes, StringElement(TagScheduledProcedureStepModificationDateTime, core.VRDT, dicomDateTime(now)))
		if err := validateDataSet(ctx, step.Attributes, service.limits); err != nil {
			return Step{}, nil, classifyDataSetError("N-SET", err)
		}
		if err := validateDataSetEncoding(ctx, step.Attributes); err != nil {
			return Step{}, nil, classifyDataSetError("N-SET", err)
		}
		step.UpdatedAt = now
		events := service.eventsForModification(step, effectiveModifications, now)
		return step, events, nil
	})
}

func (service *Service) ChangeState(ctx context.Context, request ChangeStateRequest) (Step, error) {
	ctx = normalizeContext(ctx)
	operation := "N-ACTION Change UPS State"
	return service.updateStep(ctx, operation, request.SOPInstanceUID, func(step Step) (Step, []Event, error) {
		if request.State == StateScheduled || !validState(request.State) {
			return Step{}, nil, statusError(operation, StatusOnlyScheduledViaCreate, ErrInvalidState)
		}
		if !validUID(request.TransactionUID) {
			return Step{}, nil, statusError(operation, StatusIncorrectTransactionUID, ErrInvalidDataSet)
		}
		if step.State != StateScheduled && core.NormalizeUID(step.TransactionUID) != core.NormalizeUID(request.TransactionUID) {
			return Step{}, nil, statusError(operation, StatusIncorrectTransactionUID, ErrConflict)
		}
		if request.State == StateScheduled {
			return Step{}, nil, statusError(operation, StatusOnlyScheduledViaCreate, ErrInvalidState)
		}
		switch step.State {
		case StateScheduled:
			if request.State != StateInProgress {
				return Step{}, nil, statusError(operation, StatusNotInProgress, ErrInvalidState)
			}
			step.State = StateInProgress
			step.TransactionUID = core.NormalizeUID(request.TransactionUID)
		case StateInProgress:
			if request.State == StateInProgress {
				return Step{}, nil, statusError(operation, StatusAlreadyInProgress, ErrInvalidState)
			}
			if request.State != StateCompleted && request.State != StateCanceled {
				return Step{}, nil, statusError(operation, StatusMayNoLongerBeUpdated, ErrInvalidState)
			}
			if request.State == StateCanceled {
				service.fillCancellationAttributes(&step.Attributes, false)
			}
			if err := validateFinalState(step.Attributes, request.State); err != nil {
				return Step{}, nil, statusError(operation, StatusFinalStateRequirementsNotMet, err)
			}
			step.State = request.State
		case StateCompleted:
			if request.State == StateCompleted {
				return Step{}, nil, statusError(operation, StatusAlreadyCompleted, ErrInvalidState)
			}
			return Step{}, nil, statusError(operation, StatusMayNoLongerBeUpdated, ErrInvalidState)
		case StateCanceled:
			if request.State == StateCanceled {
				return Step{}, nil, statusError(operation, StatusAlreadyCanceled, ErrInvalidState)
			}
			return Step{}, nil, statusError(operation, StatusMayNoLongerBeUpdated, ErrInvalidState)
		default:
			return Step{}, nil, statusError(operation, StatusMayNoLongerBeUpdated, ErrInvalidState)
		}
		now := service.clock().UTC()
		putElement(&step.Attributes, StringElement(TagProcedureStepState, core.VRCS, string(step.State)))
		putElement(&step.Attributes, StringElement(TagTransactionUID, core.VRUI, step.TransactionUID))
		step.UpdatedAt = now
		return step, []Event{service.stateEvent(step, now)}, nil
	})
}

func (service *Service) RequestCancel(ctx context.Context, request CancelRequest) (Step, error) {
	ctx = normalizeContext(ctx)
	request.RequestingAETitle = strings.TrimSpace(request.RequestingAETitle)
	if !validAETitle(request.RequestingAETitle) {
		return Step{}, statusError("N-ACTION Request UPS Cancel", StatusReceivingAEUnknown, ErrInvalidDataSet)
	}
	return service.updateStep(ctx, "N-ACTION Request UPS Cancel", request.SOPInstanceUID, func(step Step) (Step, []Event, error) {
		switch step.State {
		case StateCompleted:
			return Step{}, nil, statusError("N-ACTION Request UPS Cancel", StatusAlreadyCompletedCancelRequest, ErrInvalidState)
		case StateCanceled:
			return Step{}, nil, statusError("N-ACTION Request UPS Cancel", StatusAlreadyCanceled, ErrInvalidState)
		}
		now := service.clock().UTC()
		information := core.DataSet{Elements: []core.Element{StringElement(TagRequestingAE, core.VRAE, request.RequestingAETitle)}}
		if request.Information != nil {
			input, err := cloneDataSet(ctx, request.Information.ToDataSet(), service.limits)
			if err != nil {
				return Step{}, nil, classifyDataSetError("N-ACTION Request UPS Cancel", err)
			}
			if err := validateDataSet(ctx, input, service.limits); err != nil {
				return Step{}, nil, classifyDataSetError("N-ACTION Request UPS Cancel", err)
			}
			if err := validateCancelInformation(input, false); err != nil {
				return Step{}, nil, statusError("N-ACTION Request UPS Cancel", StatusInvalidAttributeValue, err)
			}
			for _, tag := range []core.Tag{TagReasonForCancellation, TagProcedureStepDiscontinuationReasonCodeSequence, TagContactURI, TagContactDisplayName} {
				if element, ok := dataSetElement(input, tag); ok {
					putElement(&information, element)
				}
			}
		}
		event := Event{Type: EventCancelRequested, SOPInstanceUID: step.SOPInstanceUID, Information: information, CreatedAt: now}
		if step.State == StateScheduled {
			// PS3.4 requires the SCP performer to take control before canceling a
			// scheduled step. The generated lock is internal and never returned.
			step.State = StateCanceled
			step.TransactionUID = internalTransactionUID(step.SOPInstanceUID, step.Version+1)
			if discontinuation, present := dataSetElement(information, TagProcedureStepDiscontinuationReasonCodeSequence); present {
				progress := core.DataSet{Elements: []core.Element{discontinuation}}
				putElement(&step.Attributes, core.Element{Header: core.ElementHeader{Tag: TagProcedureStepProgressInformationSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{progress}}})
			}
			service.fillCancellationAttributes(&step.Attributes, true)
			putElement(&step.Attributes, StringElement(TagProcedureStepState, core.VRCS, string(StateCanceled)))
			putElement(&step.Attributes, StringElement(TagTransactionUID, core.VRUI, step.TransactionUID))
			if err := validateFinalState(step.Attributes, StateCanceled); err != nil {
				return Step{}, nil, statusError("N-ACTION Request UPS Cancel", StatusFinalStateRequirementsNotMet, err)
			}
			step.UpdatedAt = now
			return step, []Event{service.stateEventWithState(step, StateInProgress, now), service.stateEvent(step, now), event}, nil
		}
		return step, []Event{event}, nil
	})
}

func validateCancelInformation(dataSet core.DataSet, allowRequestingAE bool) error {
	allowed := map[core.Tag]bool{
		TagReasonForCancellation:                          true,
		TagProcedureStepDiscontinuationReasonCodeSequence: true,
		TagContactURI:         true,
		TagContactDisplayName: true,
	}
	if allowRequestingAE {
		allowed[TagRequestingAE] = true
	}
	for _, element := range dataSet.Elements {
		if !allowed[element.Tag()] {
			return ErrInvalidDataSet
		}
		switch element.Tag() {
		case TagProcedureStepDiscontinuationReasonCodeSequence:
			if !requiredSingleCodeSequence(element) {
				return ErrInvalidDataSet
			}
		case TagReasonForCancellation, TagContactURI, TagContactDisplayName:
			values := element.StringValues()
			if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
				return ErrInvalidDataSet
			}
		case TagRequestingAE:
			values := element.StringValues()
			if len(values) != 1 || !validAETitle(values[0]) {
				return ErrInvalidDataSet
			}
		}
	}
	return nil
}

func (service *Service) Events(ctx context.Context, query EventQuery) ([]Event, error) {
	if query.Limit == 0 {
		query.Limit = 1_000
	}
	if query.Limit < 0 || query.Limit > 1_000 {
		return nil, ErrResourceLimit
	}
	events, err := service.store.ListEvents(normalizeContext(ctx), query)
	if err != nil {
		return nil, safeRepositoryError(err)
	}
	return events, nil
}

func (service *Service) updateStep(ctx context.Context, operation, sopInstanceUID string, mutate func(Step) (Step, []Event, error)) (Step, error) {
	for attempt := 0; attempt < service.limits.MaxCASAttempts; attempt++ {
		step, err := service.store.GetStep(ctx, sopInstanceUID)
		if errors.Is(err, ErrNotFound) {
			return Step{}, statusError(operation, StatusUPSNotFound, err)
		}
		if err != nil {
			return Step{}, safeRepositoryError(err)
		}
		next, events, err := mutate(step)
		if errors.Is(err, errNoChange) {
			return step, nil
		}
		if err != nil {
			return Step{}, err
		}
		result, err := service.store.CommitUPS(ctx, CommitRequest{
			Step: &StepMutation{ExpectedVersion: step.Version, Next: next}, Events: events,
		})
		if errors.Is(err, ErrConcurrentUpdate) {
			continue
		}
		if err != nil {
			return Step{}, safeRepositoryError(err)
		}
		if result.Step == nil {
			return Step{}, safeRepositoryError(ErrConflict)
		}
		return *result.Step, nil
	}
	return Step{}, ErrConcurrentUpdate
}

var errNoChange = errors.New("dicom ups: no effective change")

func (service *Service) validateCreateAttributes(dataSet *core.DataSet) error {
	state, statePresent := dataSetString(*dataSet, TagProcedureStepState)
	if !statePresent {
		return statusError("N-CREATE", StatusMissingAttribute, ErrInvalidDataSet)
	}
	if strings.TrimSpace(state) == "" {
		return statusError("N-CREATE", StatusMissingAttributeValue, ErrInvalidDataSet)
	}
	if State(state) != StateScheduled {
		return statusError("N-CREATE", StatusCreateStateNotScheduled, ErrInvalidState)
	}
	transactionUID, transactionPresent := dataSetString(*dataSet, TagTransactionUID)
	if !transactionPresent {
		return statusError("N-CREATE", StatusMissingAttribute, ErrInvalidDataSet)
	}
	if transactionUID != "" {
		return statusError("N-CREATE", StatusInvalidAttributeValue, ErrInvalidDataSet)
	}
	if classUID, ok := dataSetString(*dataSet, TagSOPClassUID); ok && core.NormalizeUID(classUID) != PushSOPClassUID {
		return statusError("N-CREATE", StatusInvalidAttributeValue, ErrInvalidDataSet)
	}
	if _, present := dataSetElement(*dataSet, TagSOPInstanceUID); present {
		return statusError("N-CREATE", StatusNoSuchAttribute, ErrInvalidDataSet)
	}
	for _, element := range dataSet.Elements {
		if !createUPSAttributes[element.Tag()] {
			return statusError("N-CREATE", StatusNoSuchAttribute, ErrInvalidDataSet)
		}
	}
	for _, tag := range []core.Tag{TagScheduledProcedureStepPriority, TagProcedureStepLabel, TagScheduledProcedureStepStartDateTime, TagInputReadinessState} {
		value, present := dataSetString(*dataSet, tag)
		if !present {
			return statusError("N-CREATE", StatusMissingAttribute, ErrInvalidDataSet)
		}
		if strings.TrimSpace(value) == "" {
			return statusError("N-CREATE", StatusMissingAttributeValue, ErrInvalidDataSet)
		}
		if tag == TagInputReadinessState {
			element, _ := dataSetElement(*dataSet, tag)
			if err := validateSetElement(element); err != nil {
				return statusError("N-CREATE", StatusInvalidAttributeValue, err)
			}
		}
		if tag == TagScheduledProcedureStepPriority && value != "HIGH" && value != "MEDIUM" && value != "LOW" {
			return statusError("N-CREATE", StatusInvalidAttributeValue, ErrInvalidDataSet)
		}
	}
	if value, present := dataSetString(*dataSet, TagWorklistLabel); !present {
		return statusError("N-CREATE", StatusMissingAttribute, ErrInvalidDataSet)
	} else if value == "" {
		putElement(dataSet, StringElement(TagWorklistLabel, core.VRLO, service.defaultWorklistLabel))
	}
	// These attributes are conditional for the SCU but Type 2 for the SCP.
	// Preserve an explicitly supplied value and otherwise create the required
	// zero-length attribute.
	if _, present := dataSetElement(*dataSet, TagStudyInstanceUID); !present {
		putElement(dataSet, StringElement(TagStudyInstanceUID, core.VRUI, ""))
	}
	if _, present := dataSetElement(*dataSet, TagPatientID); !present {
		putElement(dataSet, StringElement(TagPatientID, core.VRLO, ""))
	}
	for _, tag := range createTypeTwoTags {
		if _, present := dataSetElement(*dataSet, tag); !present {
			return statusError("N-CREATE", StatusMissingAttribute, ErrInvalidDataSet)
		}
	}
	for _, tag := range []core.Tag{TagProcedureStepProgressInformationSequence, TagUnifiedProcedureStepPerformedProcedureSequence} {
		element, _ := dataSetElement(*dataSet, tag)
		sequence, ok := element.Value.(core.SequenceValue)
		if !ok || element.VR() != core.VRSQ || len(sequence.Items) != 0 {
			return statusError("N-CREATE", StatusInvalidAttributeValue, ErrInvalidDataSet)
		}
	}
	if element, present := dataSetElement(*dataSet, TagScheduledHumanPerformersSequence); present {
		if err := validateSetElement(element); err != nil {
			return statusError("N-CREATE", StatusInvalidAttributeValue, err)
		}
	}
	for _, tag := range []core.Tag{
		TagScheduledStationNameCodeSequence,
		TagScheduledStationClassCodeSequence,
		TagScheduledStationGeographicLocationCodeSequence,
		TagScheduledWorkitemCodeSequence,
		TagAdmittingDiagnosesCodeSequence,
	} {
		element, _ := dataSetElement(*dataSet, tag)
		if err := validateOptionalCodeSequence(element, tag == TagScheduledWorkitemCodeSequence); err != nil {
			return statusError("N-CREATE", StatusInvalidAttributeValue, err)
		}
	}
	if err := validateReferencedRequestSequence(*dataSet); err != nil {
		return statusError("N-CREATE", StatusInvalidAttributeValue, err)
	}
	if element, present := dataSetElement(*dataSet, TagReplacedProcedureStepSequence); present {
		if err := validateSOPInstanceReferenceSequence(element); err != nil {
			return statusError("N-CREATE", StatusInvalidAttributeValue, err)
		}
	}
	if err := validateReferencedInstancesAndAccess(*dataSet, TagInputInformationSequence); err != nil {
		return statusError("N-CREATE", StatusInvalidAttributeValue, err)
	}
	if element, _ := dataSetElement(*dataSet, TagScheduledProcessingParametersSequence); validateContentItemSequence(element) != nil {
		return statusError("N-CREATE", StatusInvalidAttributeValue, ErrInvalidDataSet)
	}
	if err := validatePatientIdentificationSequences(*dataSet); err != nil {
		return statusError("N-CREATE", StatusInvalidAttributeValue, err)
	}
	if issuer, _ := dataSetElement(*dataSet, TagIssuerOfAdmissionIDSequence); validateHierarchicDesignatorSequence(issuer) != nil {
		return statusError("N-CREATE", StatusInvalidAttributeValue, ErrInvalidDataSet)
	}
	return nil
}

var createTypeTwoTags = []core.Tag{
	TagTransactionUID, TagWorklistLabel, TagScheduledProcessingParametersSequence,
	TagScheduledStationNameCodeSequence, TagScheduledStationClassCodeSequence,
	TagScheduledStationGeographicLocationCodeSequence, TagScheduledWorkitemCodeSequence,
	TagCommentsOnScheduledProcedureStep, TagInputInformationSequence,
	TagProcedureStepProgressInformationSequence, TagUnifiedProcedureStepPerformedProcedureSequence,
	TagPatientName, TagIssuerOfPatientID, TagIssuerOfPatientIDQualifiersSequence,
	TagOtherPatientIDsSequence, TagPatientBirthDate, TagPatientSex, TagAdmissionID,
	TagIssuerOfAdmissionIDSequence, TagAdmittingDiagnosesDescription,
	TagAdmittingDiagnosesCodeSequence, TagReferencedRequestSequence,
}

var createUPSAttributes = map[core.Tag]bool{
	TagSpecificCharacterSet: true, TagTimezoneOffsetFromUTC: true,
	TagSOPClassUID: true, TagTransactionUID: true, TagStudyInstanceUID: true,
	TagScheduledProcedureStepPriority: true, TagProcedureStepLabel: true, TagWorklistLabel: true,
	TagScheduledProcessingParametersSequence: true, TagScheduledStationNameCodeSequence: true,
	TagScheduledStationClassCodeSequence: true, TagScheduledStationGeographicLocationCodeSequence: true,
	TagScheduledHumanPerformersSequence: true, TagScheduledProcedureStepStartDateTime: true,
	TagExpectedCompletionDateTime: true, TagScheduledWorkitemCodeSequence: true,
	TagCommentsOnScheduledProcedureStep: true, TagInputReadinessState: true,
	TagInputInformationSequence: true, TagPatientName: true, TagPatientID: true,
	TagIssuerOfPatientID: true, TagIssuerOfPatientIDQualifiersSequence: true,
	TagOtherPatientIDsSequence: true, TagPatientBirthDate: true, TagPatientSex: true,
	TagAdmissionID: true, TagIssuerOfAdmissionIDSequence: true,
	TagAdmittingDiagnosesDescription: true, TagAdmittingDiagnosesCodeSequence: true,
	TagReferencedRequestSequence: true, TagReplacedProcedureStepSequence: true, TagProcedureStepState: true,
	TagProcedureStepProgressInformationSequence:       true,
	TagUnifiedProcedureStepPerformedProcedureSequence: true,
}

var settableUPSAttributes = map[core.Tag]bool{
	TagSpecificCharacterSet: true, TagStudyInstanceUID: true,
	TagScheduledProcedureStepPriority: true, TagProcedureStepLabel: true, TagWorklistLabel: true,
	TagScheduledProcessingParametersSequence: true, TagScheduledStationNameCodeSequence: true,
	TagScheduledStationClassCodeSequence: true, TagScheduledStationGeographicLocationCodeSequence: true,
	TagScheduledHumanPerformersSequence: true, TagScheduledProcedureStepStartDateTime: true,
	TagExpectedCompletionDateTime: true, TagScheduledWorkitemCodeSequence: true,
	TagCommentsOnScheduledProcedureStep: true, TagInputReadinessState: true,
	TagInputInformationSequence: true, TagProcedureStepProgressInformationSequence: true,
	TagUnifiedProcedureStepPerformedProcedureSequence: true,
}

func validateSetElement(element core.Element) error {
	switch element.Tag() {
	case TagScheduledProcedureStepPriority:
		value := strings.TrimSpace(element.StringValue())
		if value != "HIGH" && value != "MEDIUM" && value != "LOW" {
			return ErrInvalidDataSet
		}
	case TagInputReadinessState:
		value := strings.TrimSpace(element.StringValue())
		if value != "INCOMPLETE" && value != "UNAVAILABLE" && value != "READY" {
			return ErrInvalidDataSet
		}
	case TagProcedureStepProgressInformationSequence:
		return validateProgressInformationElement(element)
	case TagUnifiedProcedureStepPerformedProcedureSequence:
		return validatePerformedProcedureElement(element)
	case TagInputInformationSequence:
		return validateReferencedInstancesAndAccess(core.DataSet{Elements: []core.Element{element}}, TagInputInformationSequence)
	case TagScheduledProcessingParametersSequence:
		return validateContentItemSequence(element)
	case TagScheduledHumanPerformersSequence:
		sequence, ok := element.Value.(core.SequenceValue)
		if element.VR() != core.VRSQ || !ok {
			return ErrInvalidDataSet
		}
		for _, item := range sequence.Items {
			code, codeOK := dataSetElement(item, TagHumanPerformerCodeSequence)
			name, nameOK := dataSetString(item, TagHumanPerformerName)
			organization, organizationOK := dataSetString(item, TagHumanPerformerOrganization)
			if !codeOK || !requiredSingleCodeSequence(code) || !nameOK || strings.TrimSpace(name) == "" || !organizationOK || strings.TrimSpace(organization) == "" {
				return ErrInvalidDataSet
			}
		}
	case TagScheduledStationNameCodeSequence, TagScheduledStationClassCodeSequence, TagScheduledStationGeographicLocationCodeSequence:
		return validateOptionalCodeSequence(element, false)
	case TagScheduledWorkitemCodeSequence:
		if !requiredSingleCodeSequence(element) {
			return ErrInvalidDataSet
		}
		return nil
	}
	return nil
}

func validateReferencedRequestSequence(dataSet core.DataSet) error {
	element, present := dataSetElement(dataSet, TagReferencedRequestSequence)
	if !present {
		return ErrInvalidDataSet
	}
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		studyUID, studyPresent := dataSetString(item, TagStudyInstanceUID)
		if !studyPresent || !validUID(studyUID) {
			return ErrInvalidDataSet
		}
		for _, required := range []core.Tag{
			TagAccessionNumber, TagIssuerOfAccessionNumberSequence,
			TagOrderPlacerIdentifierSequence, TagOrderFillerIdentifierSequence,
			TagRequestedProcedureID, TagRequestedProcedureDescription,
			TagRequestedProcedureCodeSequence,
		} {
			if _, found := dataSetElement(item, required); !found {
				return ErrInvalidDataSet
			}
		}
		if code, _ := dataSetElement(item, TagRequestedProcedureCodeSequence); validateOptionalCodeSequence(code, true) != nil {
			return ErrInvalidDataSet
		}
		for _, tag := range []core.Tag{TagIssuerOfAccessionNumberSequence, TagOrderPlacerIdentifierSequence, TagOrderFillerIdentifierSequence} {
			if hierarchy, _ := dataSetElement(item, tag); validateHierarchicDesignatorSequence(hierarchy) != nil {
				return ErrInvalidDataSet
			}
		}
	}
	return nil
}

func validateSOPInstanceReferenceSequence(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		classUID, classPresent := dataSetString(item, TagReferencedSOPClassUID)
		instanceUID, instancePresent := dataSetString(item, TagReferencedSOPInstanceUID)
		if !classPresent || !instancePresent || !validUID(classUID) || !validUID(instanceUID) {
			return ErrInvalidDataSet
		}
	}
	return nil
}

func validateContentItemSequence(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		valueType, valueTypePresent := dataSetString(item, TagValueType)
		conceptName, conceptNamePresent := dataSetElement(item, TagConceptNameCodeSequence)
		if !valueTypePresent || !conceptNamePresent || !requiredSingleCodeSequence(conceptName) {
			return ErrInvalidDataSet
		}
		var valueTag core.Tag
		switch valueType {
		case "DATETIME":
			valueTag = TagDateTime
		case "DATE":
			valueTag = TagDate
		case "TIME":
			valueTag = TagTime
		case "PNAME":
			valueTag = TagPersonName
		case "UIDREF":
			valueTag = TagUID
		case "TEXT":
			valueTag = TagTextValue
		case "CODE":
			valueTag = TagConceptCodeSequence
		case "NUMERIC":
			valueTag = TagNumericValue
		default:
			return ErrInvalidDataSet
		}
		value, present := dataSetElement(item, valueTag)
		if !present {
			return ErrInvalidDataSet
		}
		if valueTag == TagConceptCodeSequence {
			if !requiredSingleCodeSequence(value) {
				return ErrInvalidDataSet
			}
		} else if values := value.StringValues(); len(values) == 0 || strings.TrimSpace(values[0]) == "" {
			return ErrInvalidDataSet
		}
		if valueType == "NUMERIC" {
			units, unitsPresent := dataSetElement(item, TagMeasurementUnitsCodeSequence)
			if !unitsPresent || !requiredSingleCodeSequence(units) {
				return ErrInvalidDataSet
			}
		}
		if modifiers, present := dataSetElement(item, TagContentItemModifierSequence); present {
			modifierSequence, modifierOK := modifiers.Value.(core.SequenceValue)
			if !modifierOK || len(modifierSequence.Items) == 0 || validateContentItemSequence(modifiers) != nil {
				return ErrInvalidDataSet
			}
		}
	}
	return nil
}

func validatePatientIdentificationSequences(dataSet core.DataSet) error {
	qualifiers, present := dataSetElement(dataSet, TagIssuerOfPatientIDQualifiersSequence)
	if !present || validateIssuerQualifiersSequence(qualifiers) != nil {
		return ErrInvalidDataSet
	}
	otherIDs, present := dataSetElement(dataSet, TagOtherPatientIDsSequence)
	sequence, ok := otherIDs.Value.(core.SequenceValue)
	if !present || otherIDs.VR() != core.VRSQ || !ok {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		patientID, patientIDPresent := dataSetString(item, TagPatientID)
		_, issuerPresent := dataSetElement(item, TagIssuerOfPatientID)
		itemQualifiers, itemQualifiersPresent := dataSetElement(item, TagIssuerOfPatientIDQualifiersSequence)
		if !patientIDPresent || strings.TrimSpace(patientID) == "" || !issuerPresent || !itemQualifiersPresent || validateIssuerQualifiersSequence(itemQualifiers) != nil {
			return ErrInvalidDataSet
		}
	}
	return nil
}

func validateIssuerQualifiersSequence(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		universalID, universalPresent := dataSetString(item, TagUniversalEntityID)
		_, identifierPresent := dataSetElement(item, TagIdentifierTypeCode)
		facility, facilityPresent := dataSetElement(item, TagAssigningFacilitySequence)
		jurisdiction, jurisdictionPresent := dataSetElement(item, TagAssigningJurisdictionCodeSequence)
		agency, agencyPresent := dataSetElement(item, TagAssigningAgencyOrDepartmentCodeSequence)
		if !universalPresent || !identifierPresent || !facilityPresent || !jurisdictionPresent || !agencyPresent ||
			validateHierarchicDesignatorSequence(facility) != nil || validateOptionalCodeSequence(jurisdiction, true) != nil || validateOptionalCodeSequence(agency, true) != nil {
			return ErrInvalidDataSet
		}
		if strings.TrimSpace(universalID) != "" {
			idType, idTypePresent := dataSetString(item, TagUniversalEntityIDType)
			if !idTypePresent || strings.TrimSpace(idType) == "" {
				return ErrInvalidDataSet
			}
		}
	}
	return nil
}

func validateHierarchicDesignatorSequence(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok || len(sequence.Items) > 1 {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		local, localPresent := dataSetString(item, TagLocalNamespaceEntityID)
		universal, universalPresent := dataSetString(item, TagUniversalEntityID)
		if (!localPresent || strings.TrimSpace(local) == "") && (!universalPresent || strings.TrimSpace(universal) == "") {
			return ErrInvalidDataSet
		}
		if universalPresent && strings.TrimSpace(universal) != "" {
			idType, idTypePresent := dataSetString(item, TagUniversalEntityIDType)
			if !idTypePresent || strings.TrimSpace(idType) == "" {
				return ErrInvalidDataSet
			}
		}
	}
	return nil
}

func validateReferencedInstancesAndAccess(dataSet core.DataSet, sequenceTag core.Tag) error {
	element, present := dataSetElement(dataSet, sequenceTag)
	if !present {
		return ErrInvalidDataSet
	}
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		instanceType, typePresent := dataSetString(item, TagTypeOfInstances)
		references, referencesPresent := dataSetElement(item, TagReferencedSOPSequence)
		referenceItems, referencesOK := references.Value.(core.SequenceValue)
		if !typePresent || strings.TrimSpace(instanceType) == "" || !referencesPresent || references.VR() != core.VRSQ || !referencesOK || len(referenceItems.Items) == 0 {
			return ErrInvalidDataSet
		}
		for _, reference := range referenceItems.Items {
			classUID, classPresent := dataSetString(reference, TagReferencedSOPClassUID)
			instanceUID, instancePresent := dataSetString(reference, TagReferencedSOPInstanceUID)
			if !classPresent || !instancePresent || !validUID(classUID) || !validUID(instanceUID) {
				return ErrInvalidDataSet
			}
		}
		if !validReferencedAccessMethod(item) {
			return ErrInvalidDataSet
		}
	}
	return nil
}

func validReferencedAccessMethod(item core.DataSet) bool {
	requirements := []struct {
		sequence core.Tag
		required []core.Tag
	}{
		{TagDICOMRetrievalSequence, []core.Tag{TagRetrieveAETitle}},
		{TagDICOMMediaRetrievalSequence, []core.Tag{TagStorageMediaFileSetID, TagStorageMediaFileSetUID}},
		{TagWADORetrievalSequence, []core.Tag{TagRetrieveURI}},
		{TagXDSRetrievalSequence, []core.Tag{TagRepositoryUniqueID, TagHomeCommunityID}},
		{TagWADORSRetrievalSequence, []core.Tag{TagRetrieveURL}},
	}
	foundMethod := false
	for _, requirement := range requirements {
		element, present := dataSetElement(item, requirement.sequence)
		if !present {
			continue
		}
		sequence, ok := element.Value.(core.SequenceValue)
		if element.VR() != core.VRSQ || !ok || len(sequence.Items) == 0 {
			return false
		}
		foundMethod = true
		for _, methodItem := range sequence.Items {
			for _, tag := range requirement.required {
				value, valuePresent := dataSetString(methodItem, tag)
				if !valuePresent || tag != TagStorageMediaFileSetID && tag != TagHomeCommunityID && strings.TrimSpace(value) == "" {
					return false
				}
			}
		}
	}
	return foundMethod
}

func validateOptionalCodeSequence(element core.Element, singleItem bool) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok || singleItem && len(sequence.Items) > 1 {
		return ErrInvalidDataSet
	}
	if len(sequence.Items) == 0 {
		return nil
	}
	if !requiredCodeSequence(element) {
		return ErrInvalidDataSet
	}
	return nil
}

func validateProgressInformationElement(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok || len(sequence.Items) > 1 {
		return ErrInvalidDataSet
	}
	if len(sequence.Items) == 0 {
		return nil
	}
	item := sequence.Items[0]
	for _, tag := range []core.Tag{TagProcedureStepProgress, TagProcedureStepProgressDescription, TagProcedureStepCancellationDateTime, TagReasonForCancellation} {
		if value, present := dataSetString(item, tag); present && strings.TrimSpace(value) == "" {
			return ErrInvalidDataSet
		}
	}
	if reason, present := dataSetElement(item, TagProcedureStepDiscontinuationReasonCodeSequence); present && !requiredSingleCodeSequence(reason) {
		return ErrInvalidDataSet
	}
	if parameters, present := dataSetElement(item, TagProcedureStepProgressParametersSequence); present {
		parameterSequence, parameterOK := parameters.Value.(core.SequenceValue)
		if !parameterOK || len(parameterSequence.Items) == 0 || validateContentItemSequence(parameters) != nil {
			return ErrInvalidDataSet
		}
	}
	communications, present := dataSetElement(item, TagProcedureStepCommunicationsURISequence)
	if !present {
		return nil
	}
	communicationItems, communicationOK := communications.Value.(core.SequenceValue)
	if communications.VR() != core.VRSQ || !communicationOK || len(communicationItems.Items) == 0 {
		return ErrInvalidDataSet
	}
	for _, communication := range communicationItems.Items {
		contact, found := dataSetString(communication, TagContactURI)
		if !found || strings.TrimSpace(contact) == "" {
			return ErrInvalidDataSet
		}
		if display, present := dataSetString(communication, TagContactDisplayName); present && strings.TrimSpace(display) == "" {
			return ErrInvalidDataSet
		}
	}
	return nil
}

func validatePerformedProcedureElement(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok || len(sequence.Items) > 1 {
		return ErrInvalidDataSet
	}
	if len(sequence.Items) == 0 {
		return nil
	}
	item := sequence.Items[0]
	output, outputPresent := dataSetElement(item, TagOutputInformationSequence)
	if !outputPresent || validateReferencedInstancesAndAccess(core.DataSet{Elements: []core.Element{output}}, TagOutputInformationSequence) != nil {
		return ErrInvalidDataSet
	}
	if station, present := dataSetElement(item, TagPerformedStationNameCodeSequence); present && validateOptionalCodeSequence(station, false) != nil {
		return ErrInvalidDataSet
	}
	if workitem, present := dataSetElement(item, TagPerformedWorkitemCodeSequence); present && !requiredCodeSequence(workitem) {
		return ErrInvalidDataSet
	}
	if performers, present := dataSetElement(item, TagActualHumanPerformersSequence); present && validateActualHumanPerformers(performers) != nil {
		return ErrInvalidDataSet
	}
	if parameters, present := dataSetElement(item, TagPerformedProcessingParametersSequence); present {
		parameterSequence, parameterOK := parameters.Value.(core.SequenceValue)
		if !parameterOK || len(parameterSequence.Items) == 0 || validateContentItemSequence(parameters) != nil {
			return ErrInvalidDataSet
		}
	}
	return nil
}

func validateActualHumanPerformers(element core.Element) error {
	sequence, ok := element.Value.(core.SequenceValue)
	if element.VR() != core.VRSQ || !ok || len(sequence.Items) == 0 {
		return ErrInvalidDataSet
	}
	for _, item := range sequence.Items {
		code, codePresent := dataSetElement(item, TagHumanPerformerCodeSequence)
		name, namePresent := dataSetString(item, TagHumanPerformerName)
		organization, organizationPresent := dataSetString(item, TagHumanPerformerOrganization)
		if !codePresent || !requiredSingleCodeSequence(code) || !namePresent || strings.TrimSpace(name) == "" || !organizationPresent || strings.TrimSpace(organization) == "" {
			return ErrInvalidDataSet
		}
	}
	return nil
}

func validateFinalState(dataSet core.DataSet, target State) error {
	if target == StateCompleted {
		performed, ok := dataSetElement(dataSet, TagUnifiedProcedureStepPerformedProcedureSequence)
		if !ok {
			return ErrInvalidDataSet
		}
		sequence, ok := performed.Value.(core.SequenceValue)
		if !ok || len(sequence.Items) != 1 {
			return ErrInvalidDataSet
		}
		for _, tag := range []core.Tag{TagPerformedStationNameCodeSequence, TagPerformedProcedureStepStartDateTime, TagPerformedWorkitemCodeSequence, TagPerformedProcedureStepEndDateTime, TagOutputInformationSequence} {
			element, present := dataSetElement(sequence.Items[0], tag)
			if !present {
				return ErrInvalidDataSet
			}
			if (tag == TagPerformedStationNameCodeSequence || tag == TagPerformedWorkitemCodeSequence) && !requiredCodeSequence(element) {
				return ErrInvalidDataSet
			}
			if (tag == TagPerformedProcedureStepStartDateTime || tag == TagPerformedProcedureStepEndDateTime) && strings.TrimSpace(element.StringValue()) == "" {
				return ErrInvalidDataSet
			}
		}
		if err := validateReferencedInstancesAndAccess(sequence.Items[0], TagOutputInformationSequence); err != nil {
			return ErrInvalidDataSet
		}
		return nil
	}
	if target == StateCanceled {
		progress, ok := dataSetElement(dataSet, TagProcedureStepProgressInformationSequence)
		if !ok {
			return ErrInvalidDataSet
		}
		sequence, ok := progress.Value.(core.SequenceValue)
		if !ok || len(sequence.Items) != 1 {
			return ErrInvalidDataSet
		}
		canceledAt, present := dataSetString(sequence.Items[0], TagProcedureStepCancellationDateTime)
		if !present || strings.TrimSpace(canceledAt) == "" {
			return ErrInvalidDataSet
		}
		reason, present := dataSetElement(sequence.Items[0], TagProcedureStepDiscontinuationReasonCodeSequence)
		if !present || !requiredSingleCodeSequence(reason) {
			return ErrInvalidDataSet
		}
		return nil
	}
	return ErrInvalidState
}

func (service *Service) fillCancellationAttributes(dataSet *core.DataSet, synthesizeReason bool) {
	progress, ok := dataSetElement(*dataSet, TagProcedureStepProgressInformationSequence)
	sequence, sequenceOK := progress.Value.(core.SequenceValue)
	if !ok || !sequenceOK || len(sequence.Items) == 0 {
		sequence = core.SequenceValue{Items: []core.DataSet{{}}}
	}
	item := sequence.Items[0]
	if value, present := dataSetString(item, TagProcedureStepCancellationDateTime); !present || value == "" {
		putElement(&item, StringElement(TagProcedureStepCancellationDateTime, core.VRDT, dicomDateTime(service.clock().UTC())))
	}
	if _, present := dataSetElement(item, TagProcedureStepDiscontinuationReasonCodeSequence); !present && synthesizeReason {
		putElement(&item, codeSequence(TagProcedureStepDiscontinuationReasonCodeSequence, Code{Value: "CANCEL", Scheme: "99DICOMGO", Meaning: "Canceled by UPS request"}))
	}
	sequence.Items[0] = item
	putElement(dataSet, core.Element{Header: core.ElementHeader{Tag: TagProcedureStepProgressInformationSequence, VR: core.VRSQ}, Value: sequence})
}

func (service *Service) eventsForModification(step Step, modifications core.DataSet, now time.Time) []Event {
	var events []Event
	if _, ok := dataSetElement(modifications, TagInputReadinessState); ok {
		events = append(events, service.stateEvent(step, now))
	}
	if _, ok := dataSetElement(modifications, TagProcedureStepProgressInformationSequence); ok {
		information := core.DataSet{}
		if progress, present := dataSetElement(step.Attributes, TagProcedureStepProgressInformationSequence); present {
			sequence, sequenceOK := progress.Value.(core.SequenceValue)
			if sequenceOK && len(sequence.Items) != 0 {
				information.Elements = append(information.Elements, progress)
			}
		}
		if len(information.Elements) != 0 {
			events = append(events, Event{Type: EventProgressReport, SOPInstanceUID: step.SOPInstanceUID, Information: information, CreatedAt: now})
		}
	}
	for _, tag := range []core.Tag{TagScheduledStationNameCodeSequence, TagScheduledHumanPerformersSequence} {
		if _, ok := dataSetElement(modifications, tag); ok {
			events = append(events, service.assignedEvents(step, now)...)
			break
		}
	}
	return events
}

func (service *Service) eventsForCreate(step Step, now time.Time) []Event {
	events := []Event{service.stateEvent(step, now)}
	if hasNonEmptySequence(step.Attributes, TagScheduledStationNameCodeSequence) || hasNonEmptySequence(step.Attributes, TagScheduledHumanPerformersSequence) {
		events = append(events, service.assignedEvents(step, now)...)
	}
	return events
}

func (service *Service) assignedEvents(step Step, now time.Time) []Event {
	station, stationPresent := dataSetElement(step.Attributes, TagScheduledStationNameCodeSequence)
	stationPresent = stationPresent && hasNonEmptySequence(step.Attributes, TagScheduledStationNameCodeSequence)
	humans, humansPresent := dataSetElement(step.Attributes, TagScheduledHumanPerformersSequence)
	sequence, sequenceOK := humans.Value.(core.SequenceValue)
	if !humansPresent || !sequenceOK || len(sequence.Items) == 0 {
		if !stationPresent {
			// An N-SET that clears the current assignment still produces Event
			// Type 5. Every Event Information attribute is conditional on being
			// populated, so the resulting dataset is intentionally empty.
			return []Event{{Type: EventUPSAssigned, SOPInstanceUID: step.SOPInstanceUID, Information: core.DataSet{}, CreatedAt: now}}
		}
		return []Event{{Type: EventUPSAssigned, SOPInstanceUID: step.SOPInstanceUID, Information: core.DataSet{Elements: []core.Element{station}}, CreatedAt: now}}
	}
	events := make([]Event, 0, len(sequence.Items))
	for _, human := range sequence.Items {
		information := core.DataSet{}
		if stationPresent {
			information.Elements = append(information.Elements, station)
		}
		if code, present := dataSetElement(human, TagHumanPerformerCodeSequence); present {
			information.Elements = append(information.Elements, code)
		}
		if organization, present := dataSetElement(human, TagHumanPerformerOrganization); present {
			information.Elements = append(information.Elements, organization)
		}
		events = append(events, Event{Type: EventUPSAssigned, SOPInstanceUID: step.SOPInstanceUID, Information: information, CreatedAt: now})
	}
	return events
}

func hasNonEmptySequence(dataSet core.DataSet, tag core.Tag) bool {
	element, present := dataSetElement(dataSet, tag)
	sequence, ok := element.Value.(core.SequenceValue)
	return present && ok && len(sequence.Items) != 0
}

func requiredCodeSequence(element core.Element) bool {
	if element.VR() != core.VRSQ {
		return false
	}
	sequence, ok := element.Value.(core.SequenceValue)
	if !ok || len(sequence.Items) == 0 {
		return false
	}
	for _, item := range sequence.Items {
		value, valueOK := nonEmptyDataSetString(item, tagCodeValue)
		longValue, longValueOK := nonEmptyDataSetString(item, tagLongCodeValue)
		urnValue, urnValueOK := nonEmptyDataSetString(item, tagURNCodeValue)
		if boolCount(valueOK, longValueOK, urnValueOK) != 1 {
			return false
		}
		if valueOK && (len(value) > 16 || absoluteCodeURI(value)) {
			return false
		}
		if longValueOK && (len(longValue) <= 16 || absoluteCodeURI(longValue)) {
			return false
		}
		if urnValueOK && !absoluteCodeURI(urnValue) {
			return false
		}
		_, schemeOK := nonEmptyDataSetString(item, tagCodingSchemeDesignator)
		meaning, meaningOK := dataSetString(item, tagCodeMeaning)
		_, schemeVersionPresent := dataSetElement(item, tagCodingSchemeVersion)
		if (valueOK || longValueOK) && !schemeOK || schemeVersionPresent && !schemeOK || !meaningOK || strings.TrimSpace(meaning) == "" {
			return false
		}
	}
	return true
}

func requiredSingleCodeSequence(element core.Element) bool {
	sequence, ok := element.Value.(core.SequenceValue)
	return ok && len(sequence.Items) == 1 && requiredCodeSequence(element)
}

func nonEmptyDataSetString(dataSet core.DataSet, tag core.Tag) (string, bool) {
	value, present := dataSetString(dataSet, tag)
	value = strings.TrimSpace(value)
	return value, present && value != ""
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func absoluteCodeURI(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs() && (parsed.Opaque != "" || parsed.Host != "" || parsed.Path != "")
}

func (service *Service) stateEvent(step Step, now time.Time) Event {
	return service.stateEventWithState(step, step.State, now)
}

func (service *Service) stateEventWithState(step Step, state State, now time.Time) Event {
	information := core.DataSet{Elements: []core.Element{StringElement(TagProcedureStepState, core.VRCS, string(state))}}
	if readiness, ok := dataSetElement(step.Attributes, TagInputReadinessState); ok {
		information.Elements = append(information.Elements, readiness)
	}
	return Event{Type: EventStateReport, SOPInstanceUID: step.SOPInstanceUID, Information: information, CreatedAt: now}
}

func validState(state State) bool {
	switch state {
	case StateScheduled, StateInProgress, StateCompleted, StateCanceled:
		return true
	default:
		return false
	}
}

func validDefaultWorklistLabel(value string) bool {
	if value == "" || len(value) > 64 || strings.ContainsRune(value, '\\') {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func validUID(value string) bool {
	value = core.NormalizeUID(value)
	if value == "" || len(value) > 64 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	components := strings.Split(value, ".")
	if len(components) < 2 || components[0] != "0" && components[0] != "1" && components[0] != "2" {
		return false
	}
	for _, component := range components {
		if component == "" || len(component) > 1 && component[0] == '0' {
			return false
		}
		for _, character := range component {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	if components[0] != "2" {
		if len(components[1]) > 2 || len(components[1]) == 2 && components[1] > "39" {
			return false
		}
	}
	return true
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func classifyDataSetError(operation string, err error) error {
	if errors.Is(err, ErrResourceLimit) {
		return err
	}
	return statusError(operation, StatusInvalidAttributeValue, err)
}

func safeRepositoryError(err error) error {
	if err == nil || errors.Is(err, ErrRepository) {
		return err
	}
	return &RepositoryError{Err: err}
}

func dicomDateTime(value time.Time) string {
	return value.Format("20060102150405.000000-0700")
}

func internalTransactionUID(sopInstanceUID string, version uint64) string {
	// A deterministic UUID-derived UID keeps retries of the same transition
	// stable without colliding merely because two SOP UIDs have equal length.
	// The 2.25 UUID root plus 128 hash bits remains below DICOM's 64-byte limit.
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", core.NormalizeUID(sopInstanceUID), version)))
	return "2.25." + new(big.Int).SetBytes(digest[:16]).String()
}
