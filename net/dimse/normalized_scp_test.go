package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestServeAssociationNormalizedSCPAllServices(t *testing.T) {
	const classUID = "1.2.840.10008.5.1.4.34.6.1"
	const instanceUID = "2.25.123456789"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})

	var (
		mu    sync.Mutex
		calls []string
	)
	record := func(service string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, service)
	}
	options := NormalizedSCPOptions{
		EventReportHandler: func(_ context.Context, request NormalizedEventReportRequest, dataSet *object.Object) (NormalizedEventReportSCPResult, error) {
			record("event")
			if dataSet == nil {
				return NormalizedEventReportSCPResult{}, fmt.Errorf("missing Event Information")
			}
			return NormalizedEventReportSCPResult{Response: NormalizedEventReportResponse{}, DataSet: normalizedTestDataSet("event reply")}, nil
		},
		GetHandler: func(_ context.Context, request NormalizedGetRequest) (NormalizedGetSCPResult, error) {
			record("get")
			return NormalizedGetSCPResult{DataSet: normalizedTestDataSet("attributes")}, nil
		},
		SetHandler: func(_ context.Context, request NormalizedSetRequest, dataSet *object.Object) (NormalizedSetSCPResult, error) {
			record("set")
			if dataSet == nil {
				return NormalizedSetSCPResult{}, fmt.Errorf("missing Modification List")
			}
			return NormalizedSetSCPResult{}, nil
		},
		ActionHandler: func(_ context.Context, request NormalizedActionRequest, dataSet *object.Object) (NormalizedActionSCPResult, error) {
			record("action")
			return NormalizedActionSCPResult{DataSet: normalizedTestDataSet("action reply")}, nil
		},
		CreateHandler: func(_ context.Context, request NormalizedCreateRequest, dataSet *object.Object) (NormalizedCreateSCPResult, error) {
			record("create")
			return NormalizedCreateSCPResult{Response: NormalizedCreateResponse{AffectedSOPInstanceUID: instanceUID}}, nil
		},
		DeleteHandler: func(_ context.Context, request NormalizedDeleteRequest) (NormalizedDeleteSCPResult, error) {
			record("delete")
			return NormalizedDeleteSCPResult{}, nil
		},
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeAssociation(context.Background(), peer, AssociationSCPOptions{NormalizedSCP: &options})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := NewNormalizedClient(local)
	if _, err := client.EventReport(ctx, NormalizedEventReportRequest{
		AffectedSOPClassUID: classUID, CommandDataSetType: DataSetPresent,
		AffectedSOPInstanceUID: instanceUID, EventTypeID: 1,
	}, normalizedTestDataSet("event information")); err != nil {
		t.Fatalf("EventReport() error = %v", err)
	}
	if _, err := client.Get(ctx, NormalizedGetRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID}); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := client.Set(ctx, NormalizedSetRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID}, normalizedTestDataSet("modification")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if _, err := client.Action(ctx, NormalizedActionRequest{
		RequestedSOPClassUID: classUID, CommandDataSetType: NoDataSet,
		RequestedSOPInstanceUID: instanceUID, ActionTypeID: 2,
	}, nil); err != nil {
		t.Fatalf("Action() error = %v", err)
	}
	if _, err := client.Create(ctx, NormalizedCreateRequest{AffectedSOPClassUID: classUID, CommandDataSetType: NoDataSet}, nil); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := client.Delete(ctx, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := local.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"event", "get", "set", "action", "create", "delete"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("handler calls = %v, want %v", calls, want)
	}
}

func TestNormalizedSCPMissingHandlerDrainsDatasetAndKeepsAssociationUsable(t *testing.T) {
	const classUID = "1.2.3"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeAssociation(context.Background(), peer, AssociationSCPOptions{NormalizedSCP: &NormalizedSCPOptions{}})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := NewNormalizedClient(local)
	setResult, err := client.Set(ctx, NormalizedSetRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID}, normalizedTestDataSet("must be drained"))
	var statusErr *NormalizedStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != StatusUnrecognizedOperation {
		t.Fatalf("Set() error = %v, result=%#v", err, setResult)
	}
	deleteResult, err := client.Delete(ctx, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID})
	if !errors.As(err, &statusErr) || statusErr.Status != StatusUnrecognizedOperation {
		t.Fatalf("Delete() error = %v, result=%#v", err, deleteResult)
	}
	if err := local.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestNormalizedSCPHandlerErrorReturnsFailureAndContinues(t *testing.T) {
	const classUID = "1.2.3"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	options := NormalizedSCPOptions{
		DeleteHandler: func(context.Context, NormalizedDeleteRequest) (NormalizedDeleteSCPResult, error) {
			return NormalizedDeleteSCPResult{}, errors.New("backend unavailable")
		},
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeAssociation(context.Background(), peer, AssociationSCPOptions{NormalizedSCP: &options})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := NewNormalizedClient(local)
	result, err := client.Delete(ctx, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID})
	var statusErr *NormalizedStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != StatusProcessingFailure || result.Response == nil {
		t.Fatalf("Delete() error = %v, result=%#v", err, result)
	}
	// A second operation proves ServeAssociation swallowed only the already
	// responded handler error and kept its receive loop alive.
	result, err = client.Delete(ctx, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID})
	if !errors.As(err, &statusErr) || result.Response == nil {
		t.Fatalf("second Delete() error = %v, result=%#v", err, result)
	}
	if err := local.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestNormalizedSCPRejectsMismatchedHandlerResponseAndContinues(t *testing.T) {
	const classUID = "1.2.3"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	var calls atomic.Int32
	options := NormalizedSCPOptions{
		ActionHandler: func(context.Context, NormalizedActionRequest, *object.Object) (NormalizedActionSCPResult, error) {
			if calls.Add(1) == 1 {
				wrongTypeID := uint16(99)
				return NormalizedActionSCPResult{
					Response: NormalizedActionResponse{
						AffectedSOPClassUID:    "9.9.9",
						AffectedSOPInstanceUID: "9.9.9.1",
						ActionTypeIDOrNil:      &wrongTypeID,
					},
					DataSet: normalizedTestDataSet("must not be sent"),
				}, nil
			}
			return NormalizedActionSCPResult{}, nil
		},
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeAssociation(context.Background(), peer, AssociationSCPOptions{NormalizedSCP: &options})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := NewNormalizedClient(local)
	request := NormalizedActionRequest{
		RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID, ActionTypeID: 7, CommandDataSetType: NoDataSet,
	}
	result, err := client.Action(ctx, request, nil)
	var statusErr *NormalizedStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != StatusProcessingFailure {
		t.Fatalf("Action() mismatch error = %v, result=%#v", err, result)
	}
	if result.Response == nil || result.Response.AffectedSOPClassUID != classUID || result.Response.AffectedSOPInstanceUID != instanceUID {
		t.Fatalf("Action() correlated response = %#v", result.Response)
	}
	if result.DataSet != nil {
		t.Fatalf("Action() failure dataset = %#v, want nil", result.DataSet)
	}

	result, err = client.Action(ctx, request, nil)
	if err != nil || result.Response == nil || result.Response.Status != StatusSuccess {
		t.Fatalf("valid Action() after mismatch: result=%#v error=%v", result, err)
	}
	if err := local.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestWithoutNormalizedActionResponseHookPreservesJoinedErrors(t *testing.T) {
	hook := &normalizedActionResponseHook{}
	first := errors.New("first handler failure")
	second := errors.New("second handler failure")
	err := fmt.Errorf("outer handler: %w", errors.Join(hook, first, fmt.Errorf("inner handler: %w", second)))

	filtered := withoutNormalizedActionResponseHook(err)
	if !errors.Is(filtered, first) || !errors.Is(filtered, second) {
		t.Fatalf("filtered error = %v, want both handler failures", filtered)
	}
	var remainingHook *normalizedActionResponseHook
	if errors.As(filtered, &remainingHook) {
		t.Fatalf("filtered error retained response hook: %v", filtered)
	}
	if got := withoutNormalizedActionResponseHook(fmt.Errorf("wrapped hook: %w", hook)); got != nil {
		t.Fatalf("hook-only error = %v, want nil", got)
	}
}

func TestCorrelateNormalizedResponseAllowsOnlyCreateAssignedInstance(t *testing.T) {
	tests := []struct {
		name                  string
		responseClass         string
		responseInstance      string
		requestInstance       string
		allowAssignedInstance bool
		wantErr               bool
		wantInstance          string
	}{
		{name: "matching", responseClass: "1.2.3", responseInstance: "1.2.3.4", requestInstance: "1.2.3.4", wantInstance: "1.2.3.4"},
		{name: "fills omitted", requestInstance: "1.2.3.4", wantInstance: "1.2.3.4"},
		{name: "rejects mismatched", responseClass: "9.9.9", responseInstance: "9.9.9.1", requestInstance: "1.2.3.4", wantErr: true, wantInstance: "1.2.3.4"},
		{name: "allows create assigned", responseInstance: "2.25.1", allowAssignedInstance: true, wantInstance: "2.25.1"},
		{name: "rejects otherwise unrequested", responseInstance: "2.25.1", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			classUID := tc.responseClass
			instanceUID := tc.responseInstance
			messageID := uint16(99)
			err := correlateNormalizedResponse(&classUID, &instanceUID, &messageID, "1.2.3", tc.requestInstance, 7, tc.allowAssignedInstance)
			if (err != nil) != tc.wantErr {
				t.Fatalf("correlateNormalizedResponse() error = %v, wantErr %t", err, tc.wantErr)
			}
			if classUID != "1.2.3" || instanceUID != tc.wantInstance || messageID != 7 {
				t.Fatalf("correlated response = class %q instance %q message %d", classUID, instanceUID, messageID)
			}
		})
	}
}

func TestNormalizedSCPPresentationContextFailureKeepsAssociationUsable(t *testing.T) {
	const classUID = "1.2.3"
	const wrongClassUID = "1.2.999"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	options := NormalizedSCPOptions{
		DeleteHandler: func(context.Context, NormalizedDeleteRequest) (NormalizedDeleteSCPResult, error) {
			return NormalizedDeleteSCPResult{}, nil
		},
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeAssociation(context.Background(), peer, AssociationSCPOptions{NormalizedSCP: &options})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := SendCommandSetWithContext(ctx, local, 1, (NormalizedDeleteRequest{
		RequestedSOPClassUID:    wrongClassUID,
		MessageID:               7,
		RequestedSOPInstanceUID: instanceUID,
	}).CommandSet()); err != nil {
		t.Fatalf("send mismatched N-DELETE: %v", err)
	}
	command, err := receiveCommandSetWithContext(ctx, local, 1)
	if err != nil {
		t.Fatalf("receive mismatched N-DELETE response: %v", err)
	}
	response, err := ParseNormalizedDeleteResponse(command)
	if err != nil {
		t.Fatalf("parse mismatched N-DELETE response: %v", err)
	}
	if response.Status != StatusNoSuchSOPClass || response.MessageIDBeingRespondedTo != 7 {
		t.Fatalf("mismatched N-DELETE response = %#v", response)
	}

	result, err := NewNormalizedClient(local).Delete(ctx, NormalizedDeleteRequest{
		RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID,
	})
	if err != nil || result.Response == nil || result.Response.Status != StatusSuccess {
		t.Fatalf("valid N-DELETE after mismatch: result=%#v error=%v", result, err)
	}
	if err := local.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestRegisterNormalizedHandlersUsesExistingDispatcher(t *testing.T) {
	const classUID = "1.2.3"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	dispatcher := NewDispatcher(peer)
	var called atomic.Bool
	if err := RegisterNormalizedHandlers(dispatcher, NormalizedSCPOptions{
		DeleteHandler: func(context.Context, NormalizedDeleteRequest) (NormalizedDeleteSCPResult, error) {
			called.Store(true)
			return NormalizedDeleteSCPResult{}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterNormalizedHandlers() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- dispatcher.Next(context.Background()) }()

	result, err := NewNormalizedClient(local).Delete(context.Background(), NormalizedDeleteRequest{
		RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if result.Response == nil || !called.Load() {
		t.Fatalf("response=%#v called=%t", result.Response, called.Load())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("Dispatcher.Next() error = %v", err)
	}
}
