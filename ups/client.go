package ups

import (
	"context"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var defaultTransferSyntaxUIDs = []string{
	transfer.ExplicitVRLittleEndian.UID,
	transfer.ImplicitVRLittleEndian.UID,
}

// TransferSyntaxUIDs returns the default transfer syntaxes proposed by UPS
// presentation-context helpers. The returned slice is caller-owned.
func TransferSyntaxUIDs() []string { return append([]string(nil), defaultTransferSyntaxUIDs...) }

// PresentationContext returns a default proposal for one UPS SOP Class.
func PresentationContext(sopClassUID string) (ul.PresentationContext, error) {
	switch sopClassUID {
	case PushSOPClassUID, WatchSOPClassUID, PullSOPClassUID, EventSOPClassUID, QuerySOPClassUID:
		return ul.PresentationContext{
			AbstractSyntaxUID:  sopClassUID,
			TransferSyntaxUIDs: TransferSyntaxUIDs(),
		}, nil
	default:
		return ul.PresentationContext{}, ErrInvalidDataSet
	}
}

// PresentationContexts returns stable, de-duplicated UPS context proposals in
// the caller's order. With no arguments it returns all five UPS SOP Classes.
func PresentationContexts(sopClassUIDs ...string) ([]ul.PresentationContext, error) {
	if len(sopClassUIDs) == 0 {
		sopClassUIDs = []string{PushSOPClassUID, WatchSOPClassUID, PullSOPClassUID, EventSOPClassUID, QuerySOPClassUID}
	}
	contexts := make([]ul.PresentationContext, 0, len(sopClassUIDs))
	seen := make(map[string]bool, len(sopClassUIDs))
	for _, uid := range sopClassUIDs {
		if seen[uid] {
			continue
		}
		context, err := PresentationContext(uid)
		if err != nil {
			return nil, err
		}
		seen[uid] = true
		contexts = append(contexts, context)
	}
	return contexts, nil
}

// OperationResult preserves the response status and any returned Attribute
// List. DataSet is owned by the caller. The association remains borrowed.
type OperationResult struct {
	Status              uint16
	SOPInstanceUID      string
	DataSet             *object.Object
	PresentationContext ul.AcceptedContext
}

// Client performs UPS normalized operations on an established, borrowed
// association. It does not release, abort, or close the association on a
// successful exchange. Transport/protocol failures follow NormalizedClient's
// association-uncertain policy.
type Client struct {
	normalized *dimse.NormalizedClient
}

func NewClient(association *ul.Association) *Client {
	return &Client{normalized: dimse.NewNormalizedClient(association)}
}

func (client *Client) Create(ctx context.Context, sopInstanceUID string, attributes *object.Object) (OperationResult, error) {
	result, err := client.normalized.CreateWithOptions(client.options(ctx, PushSOPClassUID), dimse.NormalizedCreateRequest{
		AffectedSOPClassUID: PushSOPClassUID, AffectedSOPInstanceUID: sopInstanceUID,
		CommandDataSetType: dataSetType(attributes),
	}, attributes)
	return createOperationResult(result), err
}

// Get retrieves attributes through Push, Pull, or Watch. The command SOP Class
// remains UPS Push as required by Annex CC.
func (client *Client) Get(ctx context.Context, abstractSyntaxUID, sopInstanceUID string, attributes []core.Tag) (OperationResult, error) {
	if !oneOf(abstractSyntaxUID, PushSOPClassUID, PullSOPClassUID, WatchSOPClassUID) {
		return OperationResult{}, ErrInvalidDataSet
	}
	result, err := client.normalized.GetWithOptions(client.options(ctx, abstractSyntaxUID), dimse.NormalizedGetRequest{
		RequestedSOPClassUID: PushSOPClassUID, RequestedSOPInstanceUID: sopInstanceUID,
		AttributeIdentifierList: append([]core.Tag(nil), attributes...),
	})
	return getOperationResult(result), err
}

func (client *Client) Set(ctx context.Context, sopInstanceUID string, modifications *object.Object) (OperationResult, error) {
	result, err := client.normalized.SetWithOptions(client.options(ctx, PullSOPClassUID), dimse.NormalizedSetRequest{
		RequestedSOPClassUID: PushSOPClassUID, RequestedSOPInstanceUID: sopInstanceUID,
	}, modifications)
	return setOperationResult(result), err
}

func (client *Client) ChangeState(ctx context.Context, sopInstanceUID string, state State, transactionUID string) (OperationResult, error) {
	information, err := BuildChangeStateInformation(state, transactionUID)
	if err != nil {
		return OperationResult{}, err
	}
	return client.action(ctx, PullSOPClassUID, sopInstanceUID, ActionChangeState, information)
}

func (client *Client) RequestCancel(ctx context.Context, abstractSyntaxUID, sopInstanceUID string, information *object.Object) (OperationResult, error) {
	if !oneOf(abstractSyntaxUID, PushSOPClassUID, WatchSOPClassUID) {
		return OperationResult{}, ErrInvalidDataSet
	}
	return client.action(ctx, abstractSyntaxUID, sopInstanceUID, ActionRequestCancel, information)
}

func (client *Client) Subscribe(ctx context.Context, sopInstanceUID, receivingAETitle string, deletionLock bool) (OperationResult, error) {
	information, err := BuildSubscriptionInformation(receivingAETitle, deletionLock, nil)
	if err != nil {
		return OperationResult{}, err
	}
	return client.action(ctx, WatchSOPClassUID, sopInstanceUID, ActionSubscribe, information)
}

func (client *Client) Unsubscribe(ctx context.Context, sopInstanceUID, receivingAETitle string) (OperationResult, error) {
	information, err := BuildUnsubscriptionInformation(receivingAETitle)
	if err != nil {
		return OperationResult{}, err
	}
	return client.action(ctx, WatchSOPClassUID, sopInstanceUID, ActionUnsubscribe, information)
}

func (client *Client) SuspendGlobal(ctx context.Context, receivingAETitle string) (OperationResult, error) {
	information, err := BuildUnsubscriptionInformation(receivingAETitle)
	if err != nil {
		return OperationResult{}, err
	}
	return client.action(ctx, WatchSOPClassUID, GlobalSubscriptionSOPInstanceUID, ActionSuspendGlobal, information)
}

func (client *Client) action(ctx context.Context, abstractSyntaxUID, sopInstanceUID string, actionType uint16, information *object.Object) (OperationResult, error) {
	result, err := client.normalized.ActionWithOptions(client.options(ctx, abstractSyntaxUID), dimse.NormalizedActionRequest{
		RequestedSOPClassUID: PushSOPClassUID, RequestedSOPInstanceUID: sopInstanceUID,
		ActionTypeID: actionType, CommandDataSetType: dataSetType(information),
	}, information)
	return actionOperationResult(result), err
}

func (client *Client) options(ctx context.Context, abstractSyntaxUID string) dimse.NormalizedOperationOptions {
	return dimse.NormalizedOperationOptions{
		OperationOptions:                     dimse.OperationOptions{Context: ctx},
		PresentationContextAbstractSyntaxUID: abstractSyntaxUID,
	}
}

func dataSetType(dataSet *object.Object) uint16 {
	if dataSet == nil {
		return dimse.NoDataSet
	}
	return dimse.DataSetPresent
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func createOperationResult(result dimse.NormalizedCreateResult) OperationResult {
	operation := OperationResult{DataSet: result.DataSet, PresentationContext: result.PresentationContext}
	if result.Response != nil {
		operation.Status = result.Response.Status
		operation.SOPInstanceUID = result.Response.AffectedSOPInstanceUID
	}
	return operation
}

func getOperationResult(result dimse.NormalizedGetResult) OperationResult {
	operation := OperationResult{DataSet: result.DataSet, PresentationContext: result.PresentationContext}
	if result.Response != nil {
		operation.Status = result.Response.Status
		operation.SOPInstanceUID = result.Response.AffectedSOPInstanceUID
	}
	return operation
}

func setOperationResult(result dimse.NormalizedSetResult) OperationResult {
	operation := OperationResult{DataSet: result.DataSet, PresentationContext: result.PresentationContext}
	if result.Response != nil {
		operation.Status = result.Response.Status
		operation.SOPInstanceUID = result.Response.AffectedSOPInstanceUID
	}
	return operation
}

func actionOperationResult(result dimse.NormalizedActionResult) OperationResult {
	operation := OperationResult{DataSet: result.DataSet, PresentationContext: result.PresentationContext}
	if result.Response != nil {
		operation.Status = result.Response.Status
		operation.SOPInstanceUID = result.Response.AffectedSOPInstanceUID
	}
	return operation
}
