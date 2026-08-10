package ups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestSetRejectsOutOfModelAttributesAndNoOpIsIdempotent(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.101")

	_, err := service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications:  NewDataSet(StringElement(TagPatientName, core.VRPN, "PATIENT^NAME")),
	})
	if !IsStatus(err, StatusNoSuchAttribute) {
		t.Fatalf("out-of-model N-SET error = %v", err)
	}

	before, err := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications:  NewDataSet(StringElement(TagWorklistLabel, core.VRLO, "RADIOLOGY")),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != step.Version || len(after) != len(before) {
		t.Fatalf("no-op changed version/events: version %d -> %d, events %d -> %d", step.Version, unchanged.Version, len(before), len(after))
	}
}

func TestSetAcceptsRequiredSCPAttributesCreatedByNCreate(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	attributes, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	attributes.Put(StringElement(TagSpecificCharacterSet, core.VRCS, "ISO_IR 100"))
	step, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.100", Attributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications: NewDataSet(
			StringElement(TagStudyInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.619.100.1"),
			StringElement(TagSpecificCharacterSet, core.VRCS, "ISO_IR 192"),
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := dataSetString(updated.Attributes, TagStudyInstanceUID); !ok || value == "" {
		t.Fatalf("Study Instance UID = %q, present %t", value, ok)
	}
}

func TestCreateRequiresWorklistLabelAndValidNestedTypeOneAttributes(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	missingLabel, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	missingLabel.Remove(TagWorklistLabel)
	if _, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.100.1", Attributes: missingLabel}); !IsStatus(err, StatusMissingAttribute) {
		t.Fatalf("missing Worklist Label error = %v", err)
	}

	for name, tag := range map[string]core.Tag{
		"referenced request":      TagReferencedRequestSequence,
		"input information":       TagInputInformationSequence,
		"processing parameters":   TagScheduledProcessingParametersSequence,
		"other patient IDs":       TagOtherPatientIDsSequence,
		"patient ID qualifiers":   TagIssuerOfPatientIDQualifiersSequence,
		"admission ID qualifiers": TagIssuerOfAdmissionIDSequence,
	} {
		t.Run(name, func(t *testing.T) {
			attributes, _ := BuildScheduledStep(ScheduledStepAttributes{
				Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
				StartDateTime: "20260808120000", InputReadinessState: "READY",
			})
			attributes.Put(core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}})
			if _, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.100.2", Attributes: attributes}); !IsStatus(err, StatusInvalidAttributeValue) {
				t.Fatalf("empty nested item error = %v", err)
			}
		})
	}

	unknown, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	unknown.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0010), VR: core.VRUS}, Value: core.Uint16Value{512}})
	if _, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.100.3", Attributes: unknown}); !IsStatus(err, StatusNoSuchAttribute) {
		t.Fatalf("out-of-model N-CREATE error = %v", err)
	}
}

func TestCreateAcceptsValidReplacedProcedureStepsAndRejectsMalformedReferences(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	attributes, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "REPLACEMENT", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	attributes.Put(core.Element{
		Header: core.ElementHeader{Tag: TagReplacedProcedureStepSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			StringElement(TagReferencedSOPClassUID, core.VRUI, PushSOPClassUID),
			StringElement(TagReferencedSOPInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.619.120.1"),
		}}}},
	})
	if _, err := service.Create(context.Background(), CreateRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.120.2", Attributes: attributes,
	}); err != nil {
		t.Fatalf("valid replacement reference rejected: %v", err)
	}

	malformed, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "REPLACEMENT", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	malformed.Put(core.Element{
		Header: core.ElementHeader{Tag: TagReplacedProcedureStepSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			StringElement(TagReferencedSOPClassUID, core.VRUI, PushSOPClassUID),
		}}}},
	})
	if _, err := service.Create(context.Background(), CreateRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.120.3", Attributes: malformed,
	}); !IsStatus(err, StatusInvalidAttributeValue) {
		t.Fatalf("malformed replacement reference error = %v", err)
	}
}

func TestSetRejectsMalformedNestedModifications(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.100.4")
	conceptName := codeSequence(TagConceptNameCodeSequence, Code{Value: "NOTE", Scheme: "99TEST", Meaning: "Note"})
	for name, element := range map[string]core.Element{
		"processing parameters": {
			Header: core.ElementHeader{Tag: TagScheduledProcessingParametersSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{}}},
		},
		"communications URI": {
			Header: core.ElementHeader{Tag: TagProcedureStepProgressInformationSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: TagProcedureStepCommunicationsURISequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}},
			}}}},
		},
		"performed output": {
			Header: core.ElementHeader{Tag: TagUnifiedProcedureStepPerformedProcedureSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{}}},
		},
		"progress parameters": {
			Header: core.ElementHeader{Tag: TagProcedureStepProgressInformationSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: TagProcedureStepProgressParametersSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}},
			}}}},
		},
		"actual performers": {
			Header: core.ElementHeader{Tag: TagUnifiedProcedureStepPerformedProcedureSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				EmptySequence(TagOutputInformationSequence),
				{Header: core.ElementHeader{Tag: TagActualHumanPerformersSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}},
			}}}},
		},
		"performed parameters": {
			Header: core.ElementHeader{Tag: TagUnifiedProcedureStepPerformedProcedureSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				EmptySequence(TagOutputInformationSequence),
				{Header: core.ElementHeader{Tag: TagPerformedProcessingParametersSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}},
			}}}},
		},
		"content item modifier": {
			Header: core.ElementHeader{Tag: TagScheduledProcessingParametersSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				StringElement(TagValueType, core.VRCS, "TEXT"), conceptName,
				StringElement(TagTextValue, core.VRUT, "value"),
				{Header: core.ElementHeader{Tag: TagContentItemModifierSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}},
			}}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Set(context.Background(), SetRequest{SOPInstanceUID: step.SOPInstanceUID, Modifications: NewDataSet(element)}); !IsStatus(err, StatusInvalidAttributeValue) {
				t.Fatalf("malformed nested N-SET error = %v", err)
			}
		})
	}
	if _, err := service.Set(context.Background(), SetRequest{SOPInstanceUID: step.SOPInstanceUID, Modifications: NewDataSet(EmptySequence(TagProcedureStepProgressInformationSequence))}); err != nil {
		t.Fatalf("empty Type 2 Progress Information Sequence rejected: %v", err)
	}
}

func TestSetAcceptsMultiplePerformedCodeItems(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.100.6")
	const transactionUID = "1.2.826.0.1.3680043.10.543.619.100.6.1"
	if _, err := service.ChangeState(context.Background(), ChangeStateRequest{SOPInstanceUID: step.SOPInstanceUID, State: StateInProgress, TransactionUID: transactionUID}); err != nil {
		t.Fatal(err)
	}
	first := codeSequence(TagPerformedWorkitemCodeSequence, Code{Value: "FIRST", Scheme: "99TEST", Meaning: "First"})
	second := codeSequence(TagPerformedWorkitemCodeSequence, Code{Value: "SECOND", Scheme: "99TEST", Meaning: "Second"})
	firstItems := first.Value.(core.SequenceValue).Items
	secondItems := second.Value.(core.SequenceValue).Items
	performed := core.Element{
		Header: core.ElementHeader{Tag: TagUnifiedProcedureStepPerformedProcedureSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			{Header: core.ElementHeader{Tag: TagPerformedWorkitemCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: append(firstItems, secondItems...)}},
			EmptySequence(TagOutputInformationSequence),
		}}}},
	}
	if _, err := service.Set(context.Background(), SetRequest{SOPInstanceUID: step.SOPInstanceUID, TransactionUID: transactionUID, Modifications: NewDataSet(performed)}); err != nil {
		t.Fatalf("multiple performed workitem codes rejected: %v", err)
	}
}

func TestSetRejectsCharacterSetThatCannotEncodeExistingValues(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	attributes, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	attributes.Put(StringElement(TagSpecificCharacterSet, core.VRCS, "ISO_IR 100"))
	attributes.Put(StringElement(TagPatientName, core.VRPN, "José^Silva"))
	step, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.100.5", Attributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications:  NewDataSet(StringElement(TagSpecificCharacterSet, core.VRCS, "ISO_IR 6")),
	})
	if !IsStatus(err, StatusInvalidAttributeValue) {
		t.Fatalf("incompatible Specific Character Set error = %v", err)
	}
}

func TestChangeStateStatusPrecedence(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	if _, err := service.ChangeState(context.Background(), ChangeStateRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.404", State: StateInProgress, TransactionUID: "invalid",
	}); !IsStatus(err, StatusUPSNotFound) {
		t.Fatalf("missing UPS precedence error = %v", err)
	}
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.104.1")
	if _, err := service.ChangeState(context.Background(), ChangeStateRequest{
		SOPInstanceUID: step.SOPInstanceUID, State: StateScheduled, TransactionUID: "invalid",
	}); !IsStatus(err, StatusOnlyScheduledViaCreate) {
		t.Fatalf("SCHEDULED transition precedence error = %v", err)
	}
}

func TestSetEmitsEveryApplicableEventWithCurrentPayload(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.102")
	before, _ := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	progress := core.Element{
		Header: core.ElementHeader{Tag: TagProcedureStepProgressInformationSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			StringElement(TagProcedureStepProgress, core.VRDS, "25"),
		}}}},
	}
	_, err := service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications: NewDataSet(
			StringElement(TagInputReadinessState, core.VRCS, "UNAVAILABLE"),
			progress,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	if len(after) != len(before)+2 || after[len(before)].Type != EventStateReport || after[len(before)+1].Type != EventProgressReport {
		t.Fatalf("events = %#v", after)
	}
}

func TestSetDoesNotEmitEventsForUnchangedTriggerAttributes(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.102.1")
	before, _ := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	if _, err := service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications: NewDataSet(
			StringElement(TagInputReadinessState, core.VRCS, "READY"),
			StringElement(TagWorklistLabel, core.VRLO, "UPDATED"),
		),
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	if len(after) != len(before) {
		t.Fatalf("unchanged readiness emitted an event: %d -> %d", len(before), len(after))
	}
}

func TestSetRejectsEmptyRequiredPerformedWorkitemCodeSequence(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.103")
	const transactionUID = "1.2.826.0.1.3680043.10.543.619.103.1"
	if _, err := service.ChangeState(context.Background(), ChangeStateRequest{SOPInstanceUID: step.SOPInstanceUID, State: StateInProgress, TransactionUID: transactionUID}); err != nil {
		t.Fatal(err)
	}
	performedItem := core.DataSet{Elements: []core.Element{
		EmptySequence(TagPerformedStationNameCodeSequence),
		StringElement(TagPerformedProcedureStepStartDateTime, core.VRDT, "20260808120000"),
		EmptySequence(TagPerformedWorkitemCodeSequence),
		StringElement(TagPerformedProcedureStepEndDateTime, core.VRDT, "20260808120100"),
		EmptySequence(TagOutputInformationSequence),
	}}
	performed := core.Element{
		Header: core.ElementHeader{Tag: TagUnifiedProcedureStepPerformedProcedureSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{performedItem}},
	}
	if _, err := service.Set(context.Background(), SetRequest{SOPInstanceUID: step.SOPInstanceUID, TransactionUID: transactionUID, Modifications: NewDataSet(performed)}); !IsStatus(err, StatusInvalidAttributeValue) {
		t.Fatalf("performed workitem error = %v", err)
	}
}

func TestScheduledCancelSynthesizesValidFinalAttributes(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.104")
	result, err := service.RequestCancel(context.Background(), CancelRequest{
		SOPInstanceUID: step.SOPInstanceUID, RequestingAETitle: "REQUESTOR",
		Information: NewDataSet(
			StringElement(TagReasonForCancellation, core.VRLT, "operator request"),
			StringElement(TagContactURI, core.VRUR, "mailto:operator@example.test"),
			StringElement(TagContactDisplayName, core.VRLO, "Operator"),
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateCanceled || validateFinalState(result.Attributes, StateCanceled) != nil {
		t.Fatalf("canceled step is not a valid final state: %#v", result)
	}
	events, _ := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	cancel := events[len(events)-1]
	for _, tag := range []core.Tag{TagReasonForCancellation, TagContactURI, TagContactDisplayName} {
		if _, ok := dataSetElement(cancel.Information, tag); !ok {
			t.Fatalf("cancel event missing %s", tag)
		}
	}
}

func TestCancelRequestRejectsMalformedOptionalInformation(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.115")
	const transactionUID = "1.2.826.0.1.3680043.10.543.619.115.1"
	if _, err := service.ChangeState(context.Background(), ChangeStateRequest{SOPInstanceUID: step.SOPInstanceUID, State: StateInProgress, TransactionUID: transactionUID}); err != nil {
		t.Fatal(err)
	}
	_, err := service.RequestCancel(context.Background(), CancelRequest{
		SOPInstanceUID: step.SOPInstanceUID, RequestingAETitle: "REQUESTOR",
		Information: NewDataSet(core.Element{
			Header: core.ElementHeader{Tag: TagProcedureStepDiscontinuationReasonCodeSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{}}},
		}),
	})
	if !IsStatus(err, StatusInvalidAttributeValue) {
		t.Fatalf("malformed cancel information error = %v", err)
	}
}

func TestCreateDefaultsWorklistAndSynthesizesConditionalSCPAttributes(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{DefaultWorklistLabel: "DEFAULT-WL"})
	attributes, err := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	if err != nil {
		t.Fatal(err)
	}
	dataSet := attributes.ToDataSet()
	for _, tag := range []core.Tag{
		TagScheduledHumanPerformersSequence, TagStudyInstanceUID, TagPatientID,
	} {
		removeElement(&dataSet, tag)
	}
	step, err := service.Create(context.Background(), CreateRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.108", Attributes: NewDataSet(dataSet.Elements...),
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := dataSetString(step.Attributes, TagWorklistLabel); !ok || value != "DEFAULT-WL" {
		t.Fatalf("Worklist Label = %q, present %t", value, ok)
	}
	for _, tag := range []core.Tag{TagStudyInstanceUID, TagPatientID} {
		if value, ok := dataSetString(step.Attributes, tag); !ok || value != "" {
			t.Fatalf("conditional SCP attribute %s = %q, present %t", tag, value, ok)
		}
	}

	missingTypeTwo := attributes.ToDataSet()
	removeElement(&missingTypeTwo, TagPatientName)
	_, err = service.Create(context.Background(), CreateRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.109", Attributes: NewDataSet(missingTypeTwo.Elements...),
	})
	if !IsStatus(err, StatusMissingAttribute) {
		t.Fatalf("missing Type 2 N-CREATE attribute error = %v", err)
	}

	attributes.Put(StringElement(TagSOPInstanceUID, core.VRUI, "1.2.3"))
	_, err = service.Create(context.Background(), CreateRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.110", Attributes: attributes,
	})
	if !IsStatus(err, StatusNoSuchAttribute) {
		t.Fatalf("SOP Instance UID in N-CREATE Attribute List error = %v", err)
	}
}

func TestCreateRejectsEmptyTypeOneAndNonEmptyInitialResultSequences(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	attributes, err := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	if err != nil {
		t.Fatal(err)
	}
	attributes.Put(StringElement(TagProcedureStepLabel, core.VRLO, ""))
	_, err = service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.111", Attributes: attributes})
	if !IsStatus(err, StatusMissingAttributeValue) {
		t.Fatalf("empty Type 1 value error = %v", err)
	}

	attributes, _ = BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	attributes.Put(StringElement(TagProcedureStepState, core.VRCS, ""))
	_, err = service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.111.1", Attributes: attributes})
	if !IsStatus(err, StatusMissingAttributeValue) {
		t.Fatalf("empty Procedure Step State error = %v", err)
	}

	attributes, _ = BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	attributes.Put(core.Element{Header: core.ElementHeader{Tag: TagProcedureStepProgressInformationSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}})
	_, err = service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.112", Attributes: attributes})
	if !IsStatus(err, StatusInvalidAttributeValue) {
		t.Fatalf("non-empty initial progress sequence error = %v", err)
	}

	attributes, _ = BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	attributes.Put(core.Element{Header: core.ElementHeader{Tag: TagScheduledStationNameCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{}}}})
	_, err = service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.114", Attributes: attributes})
	if !IsStatus(err, StatusInvalidAttributeValue) {
		t.Fatalf("malformed scheduled station code error = %v", err)
	}
}

func TestAssignedEventUsesNormativeTopLevelHumanPerformerAttributes(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	attributes, _ := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	human := core.DataSet{Elements: []core.Element{
		codeSequence(TagHumanPerformerCodeSequence, Code{Value: "RAD", Scheme: "99TEST", Meaning: "Radiologist"}),
		StringElement(TagHumanPerformerName, core.VRPN, "READER^ONE"),
		StringElement(TagHumanPerformerOrganization, core.VRLO, "Imaging"),
	}}
	attributes.Put(core.Element{Header: core.ElementHeader{Tag: TagScheduledHumanPerformersSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{human}}})
	step, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.113", Attributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	if err != nil {
		t.Fatal(err)
	}
	assigned := events[len(events)-1]
	if assigned.Type != EventUPSAssigned || !hasNonEmptySequence(assigned.Information, TagHumanPerformerCodeSequence) {
		t.Fatalf("assigned event = %#v", assigned)
	}
	if _, ok := dataSetElement(assigned.Information, TagHumanPerformerOrganization); !ok {
		t.Fatal("assigned event omitted Human Performer's Organization")
	}
	if _, ok := dataSetElement(assigned.Information, TagScheduledHumanPerformersSequence); ok {
		t.Fatal("assigned event emitted the Scheduled Human Performers wrapper")
	}
	if _, err := service.Set(context.Background(), SetRequest{
		SOPInstanceUID: step.SOPInstanceUID,
		Modifications:  NewDataSet(EmptySequence(TagScheduledHumanPerformersSequence)),
	}); err != nil {
		t.Fatal(err)
	}
	events, _ = service.Events(context.Background(), EventQuery{SOPInstanceUID: step.SOPInstanceUID})
	cleared := events[len(events)-1]
	if cleared.Type != EventUPSAssigned || len(cleared.Information.Elements) != 0 {
		t.Fatalf("cleared assignment event = %#v", cleared)
	}
}

func TestGlobalSubscribeReactivatesSpecificTombstone(t *testing.T) {
	resolver := CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
		return CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})
	service, _ := testServiceAndStore(t, ServiceOptions{CallbackResolver: resolver})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.105")
	if _, err := service.Subscribe(context.Background(), SubscribeRequest{SOPInstanceUID: step.SOPInstanceUID, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Unsubscribe(context.Background(), UnsubscribeRequest{SOPInstanceUID: step.SOPInstanceUID, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(context.Background(), SubscribeRequest{SOPInstanceUID: GlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", DeletionLock: true}); err != nil {
		t.Fatal(err)
	}
	got, err := service.Subscription(context.Background(), step.SOPInstanceUID, "WATCHER")
	if err != nil || got.State != SubscriptionWithDeletionLock {
		t.Fatalf("reactivated subscription = %#v, %v", got, err)
	}
}

func TestMemoryStoreSubscriptionLimitCountsOnlyActiveEntries(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreOptions{MaxSubscriptions: 1})
	if err != nil {
		t.Fatal(err)
	}
	store.steps["1.2.3"] = Step{SOPInstanceUID: "1.2.3"}
	store.subscriptions[subscriptionKey("missing", "OLD")] = Subscription{
		SOPInstanceUID: "missing", ReceivingAETitle: "OLD", State: SubscriptionNone,
	}
	result, err := store.CommitUPS(context.Background(), CommitRequest{Subscription: &SubscriptionMutation{
		Kind: SubscriptionMutationSubscribe, SOPInstanceUID: "1.2.3", ReceivingAETitle: "ACTIVE",
	}})
	if err != nil {
		t.Fatalf("CommitUPS() with one active subscription and a tombstone error = %v", err)
	}
	if result.Subscription == nil || result.Subscription.State == SubscriptionNone {
		t.Fatalf("committed subscription = %#v", result.Subscription)
	}
	if len(store.subscriptions) != 1 {
		t.Fatalf("stored subscriptions = %#v, want reclaimed tombstone", store.subscriptions)
	}
}

func TestValidAETitleRejectsNonASCIIAndControls(t *testing.T) {
	for _, valid := range []string{"A", "WATCHER_1", strings.Repeat("A", 16)} {
		if !validAETitle(valid) {
			t.Errorf("validAETitle(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"", "   ", strings.Repeat("A", 17), "BAD\\AE", "BAD\nAE", "CAFÉ"} {
		if validAETitle(invalid) {
			t.Errorf("validAETitle(%q) = true", invalid)
		}
	}
}

func TestReclaimSubscriptionTombstonesIsBoundedAndPreservesActive(t *testing.T) {
	now := time.Now()
	subscriptions := map[string]Subscription{
		"global": {SOPInstanceUID: GlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", State: SubscriptionWithoutLock},
		"active": {SOPInstanceUID: "active", ReceivingAETitle: "OTHER", State: SubscriptionWithoutLock},
		"old":    {SOPInstanceUID: "old", ReceivingAETitle: "WATCHER", State: SubscriptionNone, UpdatedAt: now.Add(-time.Hour)},
		"new":    {SOPInstanceUID: "new", ReceivingAETitle: "WATCHER", State: SubscriptionNone, UpdatedAt: now},
		"orphan": {SOPInstanceUID: "missing", ReceivingAETitle: "WATCHER", State: SubscriptionNone},
		"local":  {SOPInstanceUID: "old", ReceivingAETitle: "LOCAL", State: SubscriptionNone},
	}
	steps := map[string]Step{"active": {}, "old": {}, "new": {}}

	reclaimSubscriptionTombstones(subscriptions, steps, 1)
	for _, key := range []string{"global", "active", "new"} {
		if _, ok := subscriptions[key]; !ok {
			t.Fatalf("subscription %q was reclaimed: %#v", key, subscriptions)
		}
	}
	for _, key := range []string{"old", "orphan", "local"} {
		if _, ok := subscriptions[key]; ok {
			t.Fatalf("subscription %q was retained: %#v", key, subscriptions)
		}
	}
}

func TestReportSCPStatusChangeQueuesUniqueDurableRecipients(t *testing.T) {
	resolver := CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
		return CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})
	service, _ := testServiceAndStore(t, ServiceOptions{CallbackResolver: resolver, FallbackReceivingAETitles: []string{"FALLBACK", "WATCHER", "FALLBACK"}})
	step := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.106")
	if _, err := service.Subscribe(context.Background(), SubscribeRequest{SOPInstanceUID: step.SOPInstanceUID, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportSCPStatusChange(context.Background(), SCPStatusChange{
		Status: SCPStatusRestarted, SubscriptionListStatus: ListStatusWarmStart, UPSListStatus: ListStatusWarmStart,
	}); err != nil {
		t.Fatal(err)
	}
	deliveries, err := service.Deliveries(context.Background(), DeliveryQuery{SOPInstanceUID: GlobalSubscriptionSOPInstanceUID})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("status deliveries = %#v", deliveries)
	}
	for _, delivery := range deliveries {
		if delivery.EventType != EventSCPStatusChange {
			t.Fatalf("delivery event type = %d", delivery.EventType)
		}
	}
}

func TestReportSCPStatusChangeLimitsUniqueRecipientsAfterDeduplication(t *testing.T) {
	resolver := CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
		return CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})
	service, _ := testServiceAndStore(t, ServiceOptions{
		CallbackResolver:          resolver,
		FallbackReceivingAETitles: []string{"FALLBACK"},
		Limits:                    Limits{MaxStatusRecipients: 2},
	})
	for index := 0; index < 4; index++ {
		uid := fmt.Sprintf("1.2.826.0.1.3680043.10.543.619.130.%d", index)
		step := createTestStep(t, service, uid)
		if _, err := service.Subscribe(context.Background(), SubscribeRequest{SOPInstanceUID: step.SOPInstanceUID, ReceivingAETitle: "WATCHER"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ReportSCPStatusChange(context.Background(), SCPStatusChange{
		Status: SCPStatusRestarted, SubscriptionListStatus: ListStatusColdStart, UPSListStatus: ListStatusColdStart,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFilteredGlobalAndUnknownSpecificUnsubscribeReturnUPSNotFound(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
		return CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	_, err := service.Subscribe(context.Background(), SubscribeRequest{
		SOPInstanceUID: FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
		MatchingKeys: map[string][]string{"00741000": {"SCHEDULED"}},
	})
	if !IsStatus(err, StatusUPSNotFound) {
		t.Fatalf("filtered global subscribe error = %v", err)
	}
	err = service.Unsubscribe(context.Background(), UnsubscribeRequest{
		SOPInstanceUID: "1.2.826.0.1.3680043.10.543.619.404", ReceivingAETitle: "WATCHER",
	})
	if !IsStatus(err, StatusUPSNotFound) {
		t.Fatalf("unknown specific unsubscribe error = %v", err)
	}
}

func TestQueryRejectsZeroLengthTimezoneDeclaration(t *testing.T) {
	if _, err := BuildQueryIdentifier(Query{Keys: map[core.Tag]QueryKey{TagTimezoneOffsetFromUTC: ReturnKey()}}); err == nil {
		t.Fatal("zero-length Timezone Offset query was built")
	}
	identifier := NewDataSet(StringElement(TagTimezoneOffsetFromUTC, core.VRSH, ""))
	if _, err := ParseQueryIdentifier(identifier); err == nil {
		t.Fatal("zero-length Timezone Offset query was parsed")
	}
}

func TestRequiredCodeSequenceAcceptsLongAndURNCodeValues(t *testing.T) {
	for name, item := range map[string]core.DataSet{
		"long": {Elements: []core.Element{
			StringElement(tagLongCodeValue, core.VRUC, "A-CODE-VALUE-LONGER-THAN-SIXTEEN"),
			StringElement(tagCodingSchemeDesignator, core.VRSH, "99TEST"),
			StringElement(tagCodeMeaning, core.VRLO, "Long code"),
		}},
		"urn": {Elements: []core.Element{
			StringElement(tagURNCodeValue, core.VRUR, "urn:oid:1.2.826.0.1.3680043.10.543"),
			StringElement(tagCodeMeaning, core.VRLO, "URN code"),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			element := core.Element{Header: core.ElementHeader{Tag: TagPerformedWorkitemCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{item}}}
			if !requiredCodeSequence(element) {
				t.Fatal("valid UPS code sequence was rejected")
			}
		})
	}
}

func TestClaimPersistsAttemptBeforeCallbackCompletes(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitUPS(context.Background(), CommitRequest{Events: []Event{{
		Type: EventSCPStatusChange, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID,
		DirectReceivingAE: "WATCHER", Information: core.DataSet{},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	first, err := store.ClaimDueDeliveries(context.Background(), now, 1, time.Second)
	if err != nil || len(first) != 1 || first[0].Attempts != 1 {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	second, err := store.ClaimDueDeliveries(context.Background(), now.Add(2*time.Second), 1, time.Second)
	if err != nil || len(second) != 1 || second[0].Attempts != 2 {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
}

func TestMemoryStoreRejectsDuplicateEventIDsWithinOneCommit(t *testing.T) {
	store := mustMemoryStore(t)
	event := Event{ID: "event-fixed", Type: EventSCPStatusChange, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID, DirectReceivingAE: "WATCHER"}
	if _, err := store.CommitUPS(context.Background(), CommitRequest{Events: []Event{event, event}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate event IDs error = %v", err)
	}
	events, err := store.ListEvents(context.Background(), EventQuery{Limit: 10})
	if err != nil || len(events) != 0 {
		t.Fatalf("duplicate commit mutated events: %#v, %v", events, err)
	}
}

func TestDeliverDueClaimsEachDeliveryImmediatelyBeforeItsAttempt(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitUPS(context.Background(), CommitRequest{Events: []Event{
		{Type: EventSCPStatusChange, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID, DirectReceivingAE: "WATCHER", Information: core.DataSet{}},
		{Type: EventSCPStatusChange, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID, DirectReceivingAE: "WATCHER", Information: core.DataSet{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var dialMu sync.Mutex
	dials := 0
	service, err := NewService(store, ServiceOptions{
		CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
			return CallbackTarget{Address: "127.0.0.1:11112"}, nil
		}),
		AssociationDialer: AssociationDialerFunc(func(context.Context, string, ul.DialOptions) (*ul.Association, error) {
			dialMu.Lock()
			dials++
			call := dials
			dialMu.Unlock()
			if call == 1 {
				close(started)
				<-release
			}
			return nil, errors.New("offline")
		}),
		DeliveryLimits: DeliveryLimits{AttemptTimeout: 2 * time.Second, CleanupTimeout: 100 * time.Millisecond, LeaseDuration: 3 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.DeliverDue(context.Background(), 2) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first delivery attempt did not start")
	}
	deliveries, err := service.Deliveries(context.Background(), DeliveryQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	delivering, pending := 0, 0
	for _, delivery := range deliveries {
		switch delivery.State {
		case DeliveryDelivering:
			delivering++
		case DeliveryPending:
			pending++
		}
	}
	if delivering != 1 || pending != 1 {
		t.Fatalf("states while first attempt is blocked: delivering=%d pending=%d, deliveries=%#v", delivering, pending, deliveries)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("offline delivery unexpectedly succeeded")
	}
}

func TestExpiredClaimCannotBypassMaxAttempts(t *testing.T) {
	store, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitUPS(context.Background(), CommitRequest{Events: []Event{{
		Type: EventSCPStatusChange, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID,
		DirectReceivingAE: "WATCHER", Information: core.DataSet{},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := store.ClaimDueDeliveries(context.Background(), now, 1, time.Millisecond)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("simulated crashed claim = %#v, %v", claimed, err)
	}
	dials := 0
	service, err := NewService(store, ServiceOptions{
		Clock: func() time.Time { return now.Add(time.Second) },
		DeliveryLimits: DeliveryLimits{
			MaxAttempts: 1, AttemptTimeout: time.Millisecond, CleanupTimeout: time.Millisecond,
			LeaseDuration: 10 * time.Millisecond,
		},
		CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
			return CallbackTarget{Address: "127.0.0.1:11112"}, nil
		}),
		AssociationDialer: AssociationDialerFunc(func(context.Context, string, ul.DialOptions) (*ul.Association, error) {
			dials++
			return nil, errors.New("must not dial")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverDue(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	deliveries, err := service.Deliveries(context.Background(), DeliveryQuery{})
	if err != nil || len(deliveries) != 1 || deliveries[0].State != DeliveryExhausted || dials != 0 {
		t.Fatalf("post-crash delivery = %#v, dials=%d, err=%v", deliveries, dials, err)
	}
}

func TestRepositoryErrorAndTransferSyntaxDefaultsAreSafe(t *testing.T) {
	err := safeRepositoryError(errors.New("PATIENT^NAME"))
	if strings.Contains(err.Error(), "PATIENT") || !errors.Is(err, ErrRepository) {
		t.Fatalf("repository error = %v", err)
	}
	uids := TransferSyntaxUIDs()
	uids[0] = "9.9.9"
	context, err := PresentationContext(PushSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	if context.TransferSyntaxUIDs[0] == "9.9.9" {
		t.Fatal("caller mutated package transfer syntax defaults")
	}
}

func TestDeliveryOptionsAreDeepClonedAndDurationOverflowIsRejected(t *testing.T) {
	original := ul.DialOptions{
		UserIdentity:        &ul.UserIdentityRequest{PrimaryField: []byte("user"), SecondaryField: []byte("secret")},
		ExtendedNegotiation: []ul.SopClassExtendedNegotiationItem{{SopClassUID: PushSOPClassUID, Data: []byte{1, 2, 3}}},
	}
	cloned, err := cloneUPSDialOptions(original)
	if err != nil {
		t.Fatal(err)
	}
	original.UserIdentity.PrimaryField[0] = 'X'
	original.UserIdentity.SecondaryField[0] = 'X'
	original.ExtendedNegotiation[0].Data[0] = 9
	if string(cloned.UserIdentity.PrimaryField) != "user" || string(cloned.UserIdentity.SecondaryField) != "secret" || cloned.ExtendedNegotiation[0].Data[0] != 1 {
		t.Fatal("callback dial credentials alias resolver-owned buffers")
	}
	if _, err := normalizeDeliveryLimits(DeliveryLimits{
		AttemptTimeout: time.Duration(math.MaxInt64), CleanupTimeout: 2,
		LeaseDuration: time.Duration(math.MaxInt64),
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("overflowing delivery durations error = %v", err)
	}
}

func TestDeliveryClosesAssociationReturnedWithDialError(t *testing.T) {
	local, peer := net.Pipe()
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, peer)
		_ = peer.Close()
		close(drained)
	}()
	service, err := NewService(mustMemoryStore(t), ServiceOptions{
		CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
			return CallbackTarget{Address: "callback.example:11112"}, nil
		}),
		AssociationDialer: AssociationDialerFunc(func(context.Context, string, ul.DialOptions) (*ul.Association, error) {
			return &ul.Association{Conn: local, Context: context.Background()}, errors.New("dial failed after allocating association")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, attemptErr := service.attemptDelivery(context.Background(), Delivery{ReceivingAETitle: "WATCHER"})
	if attemptErr == nil {
		t.Fatal("partial dial failure unexpectedly succeeded")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("association returned with dial error was not closed")
	}
}

func TestUIDValidationAndInternalCancellationUIDUniqueness(t *testing.T) {
	for _, uid := range []string{"1", "3.1", "1.40.1"} {
		if validUID(uid) {
			t.Fatalf("invalid UID %q accepted", uid)
		}
	}
	service, _ := testServiceAndStore(t, ServiceOptions{})
	first := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.181")
	second := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.182")
	first, err := service.RequestCancel(context.Background(), CancelRequest{SOPInstanceUID: first.SOPInstanceUID, RequestingAETitle: "REQUESTOR"})
	if err != nil {
		t.Fatal(err)
	}
	second, err = service.RequestCancel(context.Background(), CancelRequest{SOPInstanceUID: second.SOPInstanceUID, RequestingAETitle: "REQUESTOR"})
	if err != nil {
		t.Fatal(err)
	}
	if first.TransactionUID == second.TransactionUID || !validUID(first.TransactionUID) || !validUID(second.TransactionUID) {
		t.Fatalf("internal Transaction UIDs = %q, %q", first.TransactionUID, second.TransactionUID)
	}
}

func mustMemoryStore(t *testing.T) *MemoryStore {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestQueryExactScanLimitCompletesSuccessfully(t *testing.T) {
	service, _ := testServiceAndStore(t, ServiceOptions{})
	createTestStep(t, service, "1.2.826.0.1.3680043.10.543.619.107")
	routes, err := service.QueryRoutes(QuerySCPOptions{MaxScannedSteps: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := BuildQueryIdentifier(Query{Keys: map[core.Tag]QueryKey{TagProcedureStepState: Match(string(StateScheduled))}})
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	err = routes[0].Handler.Find(context.Background(), dimse.StreamingCFindRequest{Identifier: identifier}, func(uint16, *object.Object) error {
		matches++
		return nil
	})
	if err != nil || matches != 1 {
		t.Fatalf("exact-limit query = %d matches, %v", matches, err)
	}
}

func TestQueryDateTimeNegativeOffsetIsRangeDelimiter(t *testing.T) {
	if queryValueMatch(core.VRDT, "20260808120000-0300", "20260808120000-0300") {
		t.Fatal("negative DT offset was incorrectly treated as single-value matching")
	}
	if !queryValueMatch(core.VRDT, "20260808110000-0300-20260808130000-0300", "20260808120000-0300") {
		t.Fatal("DT range with negative offsets did not match by instant")
	}
}

func TestQueryTemporalMatchingUsesDeclaredTimezoneOffsets(t *testing.T) {
	query := ParsedQuery{Keys: map[core.Tag]QueryKey{
		TagExpectedCompletionDateTime: Match("20260808120000"),
		TagTimezoneOffsetFromUTC:      Match("+0300"),
	}}
	step := Step{Attributes: core.DataSet{Elements: []core.Element{
		StringElement(TagExpectedCompletionDateTime, core.VRDT, "20260808090000"),
		StringElement(TagTimezoneOffsetFromUTC, core.VRSH, "+0000"),
	}}}
	if !queryMatchesStep(query, step) {
		t.Fatal("equivalent DT instants under declared timezone offsets did not match")
	}
}

func TestQueryControlDeclarationsDoNotBecomeUnsupportedReturnKeys(t *testing.T) {
	query := ParsedQuery{
		Keys: map[core.Tag]QueryKey{
			TagSpecificCharacterSet:  Match("ISO_IR 192"),
			TagTimezoneOffsetFromUTC: Match("+0300"),
			TagProcedureStepState:    Match(string(StateScheduled)),
		},
		order: []core.Tag{TagSpecificCharacterSet, TagTimezoneOffsetFromUTC, TagProcedureStepState},
	}
	step := Step{Attributes: core.DataSet{Elements: []core.Element{
		StringElement(TagProcedureStepState, core.VRCS, string(StateScheduled)),
	}}}
	result, warning := projectQueryResult(query, step)
	if warning {
		t.Fatal("context declarations caused FF01")
	}
	if result.Len() != 1 {
		t.Fatalf("projected result has %d elements, want 1", result.Len())
	}
}

func TestQueryRejectsInvalidContextDeclarations(t *testing.T) {
	for _, query := range []Query{
		{Keys: map[core.Tag]QueryKey{TagTimezoneOffsetFromUTC: Match("-0000")}},
		{Keys: map[core.Tag]QueryKey{TagSpecificCharacterSet: ReturnKey()}},
	} {
		if _, err := BuildQueryIdentifier(query); err == nil {
			t.Fatalf("invalid query declaration accepted: %#v", query)
		}
	}
	for _, identifier := range []*object.Object{
		NewDataSet(StringElement(TagTimezoneOffsetFromUTC, core.VRSH, "-0000")),
		NewDataSet(StringElement(TagSpecificCharacterSet, core.VRCS, "")),
	} {
		if _, err := ParseQueryIdentifier(identifier); err == nil {
			t.Fatal("invalid parsed query declaration accepted")
		}
	}
}

func testServiceAndStore(t *testing.T, options ServiceOptions) (*Service, *MemoryStore) {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, options)
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func createTestStep(t *testing.T, service *Service, uid string) Step {
	t.Helper()
	attributes, err := BuildScheduledStep(ScheduledStepAttributes{
		Priority: "MEDIUM", ProcedureStepLabel: "VERIFY", WorklistLabel: "RADIOLOGY",
		StartDateTime: "20260808120000", InputReadinessState: "READY",
	})
	if err != nil {
		t.Fatal(err)
	}
	step, err := service.Create(context.Background(), CreateRequest{SOPInstanceUID: uid, Attributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	return step
}
