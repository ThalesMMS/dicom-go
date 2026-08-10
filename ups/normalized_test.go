package ups_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/ups"
)

func TestNormalizedOptionsImplementPushPullAndWatchOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	options := service.NormalizedOptions(nil)
	uid := "1.2.826.0.1.3680043.10.543.20"
	pushCtx := normalizedContext(ctx, ups.PushSOPClassUID)
	create, createErr := options.CreateHandler(pushCtx, dimse.NormalizedCreateRequest{
		AffectedSOPClassUID: ups.PushSOPClassUID, AffectedSOPInstanceUID: uid,
	}, scheduledAttributes(t))
	if createErr != nil || create.Response.Status != ups.StatusSuccess || create.Response.AffectedSOPInstanceUID != uid {
		t.Fatalf("create = %#v, %v", create, createErr)
	}

	const transactionUID = "1.2.826.0.1.3680043.10.543.200"
	changeInformation, err := ups.BuildChangeStateInformation(ups.StateInProgress, transactionUID)
	if err != nil {
		t.Fatal(err)
	}
	pullCtx := normalizedContext(ctx, ups.PullSOPClassUID)
	change, changeErr := options.ActionHandler(pullCtx, dimse.NormalizedActionRequest{
		RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: uid, ActionTypeID: ups.ActionChangeState,
	}, changeInformation)
	if changeErr != nil || change.Response.Status != ups.StatusSuccess {
		t.Fatalf("change = %#v, %v", change, changeErr)
	}

	subscribeInformation, err := ups.BuildSubscriptionInformation("WATCHER", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	watchCtx := normalizedContext(ctx, ups.WatchSOPClassUID)
	subscribe, subscribeErr := options.ActionHandler(watchCtx, dimse.NormalizedActionRequest{
		RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: uid, ActionTypeID: ups.ActionSubscribe,
	}, subscribeInformation)
	if subscribeErr != nil || subscribe.Response.Status != ups.StatusSuccess {
		t.Fatalf("subscribe = %#v, %v", subscribe, subscribeErr)
	}

	get, getErr := options.GetHandler(watchCtx, dimse.NormalizedGetRequest{
		RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: uid,
	})
	if getErr != nil || get.Response.Status != ups.StatusSuccess || get.DataSet == nil {
		t.Fatalf("get = %#v, %v", get, getErr)
	}
	if _, found := get.DataSet.Get(ups.TagTransactionUID); found {
		t.Fatal("N-GET exposed Transaction UID lock")
	}

	invalidCreate, invalidErr := options.CreateHandler(watchCtx, dimse.NormalizedCreateRequest{
		AffectedSOPClassUID: ups.PushSOPClassUID, AffectedSOPInstanceUID: "1.2.826.0.1.3680043.10.543.21",
	}, scheduledAttributes(t))
	if invalidErr == nil || invalidCreate.Response.Status != dimse.StatusUnrecognizedOperation {
		t.Fatalf("N-CREATE over Watch = %#v, %v", invalidCreate, invalidErr)
	}
}

func TestEventHandlerFailureIsPHIFree(t *testing.T) {
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backendErr := errors.New("PATIENT^NAME")
	service, err := ups.NewService(store, ups.ServiceOptions{EventHandler: ups.EventHandlerFunc(func(context.Context, ups.ReceivedEvent) error {
		return backendErr
	})})
	if err != nil {
		t.Fatal(err)
	}
	options := service.NormalizedOptions(nil)
	result, handlerErr := options.EventReportHandler(normalizedContext(context.Background(), ups.EventSOPClassUID), dimse.NormalizedEventReportRequest{
		AffectedSOPClassUID: ups.PushSOPClassUID, AffectedSOPInstanceUID: "1.2.3", EventTypeID: uint16(ups.EventUPSAssigned),
	}, ups.NewDataSet())
	if result.Response.Status != dimse.StatusProcessingFailure || handlerErr == nil || !errors.Is(handlerErr, backendErr) {
		t.Fatalf("event handler result = %#v, %v", result, handlerErr)
	}
	if strings.Contains(handlerErr.Error(), "PATIENT^NAME") {
		t.Fatalf("event handler error exposed PHI: %v", handlerErr)
	}
}

func TestNormalizedActionBuildersRejectMalformedDatasets(t *testing.T) {
	t.Parallel()
	if _, err := ups.ParseChangeStateInformation(object.New(nil)); err == nil {
		t.Fatal("empty Change State information accepted")
	}
	if _, err := ups.BuildSubscriptionInformation("WATCHER", true, map[string][]string{"unsupported": {"x"}}); err == nil {
		t.Fatal("unsupported filtered-global key accepted")
	}
}

func TestNormalizedHandlersReturnUPSStatusForMalformedActionAndEvent(t *testing.T) {
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{EventHandler: ups.EventHandlerFunc(func(context.Context, ups.ReceivedEvent) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), ups.CreateRequest{SOPInstanceUID: "1.2.3", Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	options := service.NormalizedOptions(nil)
	change, changeErr := options.ActionHandler(normalizedContext(context.Background(), ups.PullSOPClassUID), dimse.NormalizedActionRequest{
		RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: "1.2.3", ActionTypeID: ups.ActionChangeState,
	}, ups.NewDataSet(ups.StringElement(ups.TagProcedureStepState, core.VRCS, string(ups.StateInProgress))))
	if changeErr == nil || change.Response.Status != ups.StatusIncorrectTransactionUID {
		t.Fatalf("missing Transaction UID = %#v, %v", change, changeErr)
	}
	for name, test := range map[string]struct {
		information *object.Object
		wantStatus  uint16
	}{
		"missing Action Information": {information: nil, wantStatus: ups.StatusMissingAttribute},
		"missing Procedure Step State": {
			information: ups.NewDataSet(ups.StringElement(ups.TagTransactionUID, core.VRUI, "1.2.3.4")),
			wantStatus:  ups.StatusMissingAttribute,
		},
		"empty Procedure Step State": {
			information: ups.NewDataSet(
				ups.StringElement(ups.TagProcedureStepState, core.VRCS, ""),
				ups.StringElement(ups.TagTransactionUID, core.VRUI, "1.2.3.4"),
			),
			wantStatus: ups.StatusMissingAttributeValue,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, actionErr := options.ActionHandler(normalizedContext(context.Background(), ups.PullSOPClassUID), dimse.NormalizedActionRequest{
				RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: "1.2.3", ActionTypeID: ups.ActionChangeState,
			}, test.information)
			if actionErr == nil || result.Response.Status != test.wantStatus {
				t.Fatalf("Change State result = %#v, %v; want status 0x%04X", result, actionErr, test.wantStatus)
			}
		})
	}

	event, eventErr := options.EventReportHandler(normalizedContext(context.Background(), ups.EventSOPClassUID), dimse.NormalizedEventReportRequest{
		AffectedSOPClassUID: ups.PushSOPClassUID, AffectedSOPInstanceUID: "1.2.3", EventTypeID: uint16(ups.EventProgressReport),
	}, ups.NewDataSet(ups.EmptySequence(ups.TagProcedureStepProgressInformationSequence)))
	if eventErr == nil || event.Response.Status != dimse.StatusProcessingFailure {
		t.Fatalf("malformed progress event = %#v, %v", event, eventErr)
	}
	emptyAssignment, emptyAssignmentErr := options.EventReportHandler(normalizedContext(context.Background(), ups.EventSOPClassUID), dimse.NormalizedEventReportRequest{
		AffectedSOPClassUID: ups.PushSOPClassUID, AffectedSOPInstanceUID: "1.2.3", EventTypeID: uint16(ups.EventUPSAssigned),
	}, ups.NewDataSet())
	if emptyAssignmentErr != nil || emptyAssignment.Response.Status != ups.StatusSuccess {
		t.Fatalf("empty assignment event = %#v, %v", emptyAssignment, emptyAssignmentErr)
	}

	wrongVRChange, wrongVRChangeErr := options.ActionHandler(normalizedContext(context.Background(), ups.PullSOPClassUID), dimse.NormalizedActionRequest{
		RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: "1.2.3", ActionTypeID: ups.ActionChangeState,
	}, ups.NewDataSet(
		ups.StringElement(ups.TagProcedureStepState, core.VRLO, string(ups.StateInProgress)),
		ups.StringElement(ups.TagTransactionUID, core.VRLO, "1.2.3.4"),
	))
	if wrongVRChangeErr == nil || wrongVRChange.Response.Status != dimse.StatusProcessingFailure {
		t.Fatalf("wrong-VR Change State = %#v, %v", wrongVRChange, wrongVRChangeErr)
	}

	for name, test := range map[string]struct {
		eventType   ups.EventType
		information *object.Object
	}{
		"wrong VR": {ups.EventStateReport, ups.NewDataSet(
			ups.StringElement(ups.TagProcedureStepState, core.VRLO, string(ups.StateScheduled)),
			ups.StringElement(ups.TagInputReadinessState, core.VRCS, "READY"),
		)},
		"invalid readiness": {ups.EventStateReport, ups.NewDataSet(
			ups.StringElement(ups.TagProcedureStepState, core.VRCS, string(ups.StateScheduled)),
			ups.StringElement(ups.TagInputReadinessState, core.VRCS, "BOGUS"),
		)},
		"empty communications URI": {ups.EventProgressReport, ups.NewDataSet(core.Element{
			Header: core.ElementHeader{Tag: ups.TagProcedureStepProgressInformationSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				ups.EmptySequence(ups.TagProcedureStepCommunicationsURISequence),
			}}}},
		})},
	} {
		t.Run(name, func(t *testing.T) {
			result, eventErr := options.EventReportHandler(normalizedContext(context.Background(), ups.EventSOPClassUID), dimse.NormalizedEventReportRequest{
				AffectedSOPClassUID: ups.PushSOPClassUID, AffectedSOPInstanceUID: "1.2.3", EventTypeID: uint16(test.eventType),
			}, test.information)
			if eventErr == nil || result.Response.Status != dimse.StatusProcessingFailure {
				t.Fatalf("malformed event = %#v, %v", result, eventErr)
			}
		})
	}
}

func TestNormalizedGetRejectsCommandOnlyUIDsAndCancelAllowsNoInformation(t *testing.T) {
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	uid := "1.2.826.0.1.3680043.10.543.619.190"
	if _, err := service.Create(context.Background(), ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	options := service.NormalizedOptions(&ul.Association{CallingAETitle: "REQUESTOR"})
	for _, tag := range []core.Tag{ups.TagTransactionUID, ups.TagSOPClassUID, ups.TagSOPInstanceUID} {
		result, getErr := options.GetHandler(normalizedContext(context.Background(), ups.PullSOPClassUID), dimse.NormalizedGetRequest{
			RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: uid, AttributeIdentifierList: []core.Tag{tag},
		})
		if getErr == nil || result.Response.Status != ups.StatusNoSuchAttribute {
			t.Fatalf("N-GET command-only tag %s = %#v, %v", tag, result, getErr)
		}
	}
	cancel, cancelErr := options.ActionHandler(normalizedContext(context.Background(), ups.PushSOPClassUID), dimse.NormalizedActionRequest{
		RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: uid, ActionTypeID: ups.ActionRequestCancel,
	}, nil)
	if cancelErr != nil || cancel.Response.Status != ups.StatusSuccess {
		t.Fatalf("Request Cancel without Action Information = %#v, %v", cancel, cancelErr)
	}
}

func TestNormalizedWatchActionsDistinguishMissingAndEmptyTypeOneAttributes(t *testing.T) {
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	options := service.NormalizedOptions(nil)
	ctx := normalizedContext(context.Background(), ups.WatchSOPClassUID)

	tests := []struct {
		name        string
		actionType  uint16
		instance    string
		information *object.Object
		wantStatus  uint16
	}{
		{
			name: "subscribe missing receiving AE", actionType: ups.ActionSubscribe,
			instance: "1.2.3", information: ups.NewDataSet(ups.StringElement(ups.TagDeletionLock, core.VRLO, "TRUE")),
			wantStatus: ups.StatusMissingAttribute,
		},
		{
			name: "subscribe empty receiving AE", actionType: ups.ActionSubscribe,
			instance: "1.2.3", information: ups.NewDataSet(
				ups.StringElement(ups.TagReceivingAE, core.VRAE, ""),
				ups.StringElement(ups.TagDeletionLock, core.VRLO, "TRUE"),
			),
			wantStatus: ups.StatusMissingAttributeValue,
		},
		{
			name: "subscribe missing deletion lock", actionType: ups.ActionSubscribe,
			instance: "1.2.3", information: ups.NewDataSet(ups.StringElement(ups.TagReceivingAE, core.VRAE, "WATCHER")),
			wantStatus: ups.StatusMissingAttribute,
		},
		{
			name: "subscribe empty deletion lock", actionType: ups.ActionSubscribe,
			instance: "1.2.3", information: ups.NewDataSet(
				ups.StringElement(ups.TagReceivingAE, core.VRAE, "WATCHER"),
				ups.StringElement(ups.TagDeletionLock, core.VRLO, ""),
			),
			wantStatus: ups.StatusMissingAttributeValue,
		},
		{
			name: "unsubscribe missing receiving AE", actionType: ups.ActionUnsubscribe,
			instance: "1.2.3", information: nil, wantStatus: ups.StatusMissingAttribute,
		},
		{
			name: "unsubscribe empty receiving AE", actionType: ups.ActionUnsubscribe,
			instance: "1.2.3", information: ups.NewDataSet(ups.StringElement(ups.TagReceivingAE, core.VRAE, "")),
			wantStatus: ups.StatusMissingAttributeValue,
		},
		{
			name: "suspend missing receiving AE", actionType: ups.ActionSuspendGlobal,
			instance: ups.GlobalSubscriptionSOPInstanceUID, information: nil, wantStatus: ups.StatusMissingAttribute,
		},
		{
			name: "suspend empty receiving AE", actionType: ups.ActionSuspendGlobal,
			instance:    ups.GlobalSubscriptionSOPInstanceUID,
			information: ups.NewDataSet(ups.StringElement(ups.TagReceivingAE, core.VRAE, "")),
			wantStatus:  ups.StatusMissingAttributeValue,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, actionErr := options.ActionHandler(ctx, dimse.NormalizedActionRequest{
				RequestedSOPClassUID: ups.PushSOPClassUID, RequestedSOPInstanceUID: test.instance, ActionTypeID: test.actionType,
			}, test.information)
			if actionErr == nil || result.Response.Status != test.wantStatus {
				t.Fatalf("action result = %#v, %v; want status 0x%04X", result, actionErr, test.wantStatus)
			}
		})
	}
}

func normalizedContext(ctx context.Context, abstractSyntaxUID string) context.Context {
	return dimse.ContextWithNormalizedRequestInfo(ctx, dimse.NormalizedRequestInfo{
		PresentationContext: ul.AcceptedContext{ID: 1, AbstractSyntaxUID: abstractSyntaxUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID},
		TransferSyntax:      transfer.ExplicitVRLittleEndian,
	})
}
