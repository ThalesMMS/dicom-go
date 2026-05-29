package dimse

import (
	"context"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	StorageCommitmentTransactionUID        = core.NewTag(0x0008, 0x1195)
	StorageCommitmentReferencedSOPClass    = core.NewTag(0x0008, 0x1150)
	StorageCommitmentReferencedSOPInstance = core.NewTag(0x0008, 0x1155)
	StorageCommitmentReferencedSOPSequence = core.NewTag(0x0008, 0x1199)
	StorageCommitmentFailedSOPSequence     = core.NewTag(0x0008, 0x1198)
	StorageCommitmentFailureReason         = core.NewTag(0x0008, 0x1197)
)

// StorageCommitmentSOPReference identifies one SOP Instance in a Storage
// Commitment action or event information dataset.
type StorageCommitmentSOPReference struct {
	SOPClassUID    string
	SOPInstanceUID string
	FailureReason  uint16
}

// StorageCommitmentActionInformation is the parsed N-ACTION action information
// dataset for the Storage Commitment Push Model.
type StorageCommitmentActionInformation struct {
	TransactionUID string
	ReferencedSOPs []StorageCommitmentSOPReference
}

// StorageCommitmentEventInformation is the parsed N-EVENT-REPORT event
// information dataset for the Storage Commitment Push Model.
type StorageCommitmentEventInformation struct {
	TransactionUID string
	ReferencedSOPs []StorageCommitmentSOPReference
	FailedSOPs     []StorageCommitmentSOPReference
}

// StorageCommitmentActionContext is passed to a Storage Commitment SCP handler
// after a valid N-ACTION request and action information dataset are received.
type StorageCommitmentActionContext struct {
	Request               NActionRequest
	ActionInformation     *object.Object
	TransactionUID        string
	ReferencedSOPs        []StorageCommitmentSOPReference
	PresentationContextID byte
	ActionSyntax          transfer.Syntax
}

// StorageCommitmentActionResult controls the N-ACTION-RSP status returned by
// ServeStorageCommitmentSCP. StatusSuccess accepts the request; non-success
// statuses reject or fail the request at DIMSE level. Per-instance commitment
// success/failure is reported later through N-EVENT-REPORT datasets.
type StorageCommitmentActionResult struct {
	Status uint16
}

// StorageCommitmentActionHandler processes one Storage Commitment N-ACTION-RQ.
type StorageCommitmentActionHandler interface {
	Action(context.Context, StorageCommitmentActionContext) (StorageCommitmentActionResult, error)
}

// StorageCommitmentActionHandlerFunc adapts a function to
// StorageCommitmentActionHandler.
type StorageCommitmentActionHandlerFunc func(context.Context, StorageCommitmentActionContext) (StorageCommitmentActionResult, error)

func (f StorageCommitmentActionHandlerFunc) Action(ctx context.Context, req StorageCommitmentActionContext) (StorageCommitmentActionResult, error) {
	if f == nil {
		return StorageCommitmentActionResult{Status: StatusStorageCommitmentProcessingFailure}, fmt.Errorf("dicom dimse: nil Storage Commitment action handler")
	}
	return f(ctx, req)
}

// StorageCommitmentSCPOptions bounds the legacy single-request adapter. New
// long-lived services should prefer StorageCommitmentWorkflow.NormalizedOptions
// with ServeAssociation.
type StorageCommitmentSCPOptions struct {
	MaxDataSetBytes int64
}

// ServeStorageCommitmentSCP receives one Storage Commitment N-ACTION request,
// invokes handler and sends the corresponding N-ACTION response.
//
// The accepted presentation context must be for the Storage Commitment Push
// Model. A successful N-ACTION response only accepts the request; final
// per-instance commitment success or failure remains an application-level
// N-EVENT-REPORT workflow.
func ServeStorageCommitmentSCP(ctx context.Context, assoc *ul.Association, pcID byte, handler StorageCommitmentActionHandler) error {
	return ServeStorageCommitmentSCPWithOptions(ctx, assoc, pcID, handler, StorageCommitmentSCPOptions{})
}

// ServeStorageCommitmentSCPWithOptions is the bounded form of
// ServeStorageCommitmentSCP.
func ServeStorageCommitmentSCPWithOptions(ctx context.Context, assoc *ul.Association, pcID byte, handler StorageCommitmentActionHandler, options StorageCommitmentSCPOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil Storage Commitment action handler")
	}
	if !storageCommitmentRequestorCanActAsSCU(assoc) {
		return ErrStorageCommitmentInvalidRequest
	}
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	if pc.AbstractSyntaxUID != StorageCommitmentPushModelSOPClassUID {
		return ErrPresentationContextMismatch
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		return err
	}
	maxDataSetBytes := options.MaxDataSetBytes
	if maxDataSetBytes < 0 {
		return ErrStorageCommitmentResourceLimit
	}
	if maxDataSetBytes == 0 {
		maxDataSetBytes = MaxNormalizedDataSetBytes
	}
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return err
	}
	normalizedRequest, err := ParseNormalizedActionRequest(command)
	if err != nil {
		return err
	}
	req := &NActionRequest{
		RequestedSOPClassUID: normalizedRequest.RequestedSOPClassUID, RequestedSOPInstanceUID: normalizedRequest.RequestedSOPInstanceUID,
		MessageID: normalizedRequest.MessageID, ActionTypeID: normalizedRequest.ActionTypeID,
	}
	if status, validationErr := validateStorageCommitmentActionCommandStatus(*normalizedRequest); validationErr != nil {
		_ = sendStorageCommitmentActionResponse(ctx, assoc, pcID, *req, status)
		return validationErr
	}
	actionInfo, err := receiveDataSetWithContextAndLimits(ctx, assoc, pcID, syntax, maxDataSetBytes, MaxNormalizedDataSetElements, MaxNormalizedDataSetDepth)
	if err != nil {
		_ = sendStorageCommitmentActionResponse(ctx, assoc, pcID, *req, StatusStorageCommitmentProcessingFailure)
		return err
	}
	parsed, err := ParseStorageCommitmentActionInformation(actionInfo)
	if err != nil {
		_ = sendStorageCommitmentActionResponse(ctx, assoc, pcID, *req, StatusStorageCommitmentProcessingFailure)
		return err
	}

	result, handlerErr := handler.Action(ctx, StorageCommitmentActionContext{
		Request:               *req,
		ActionInformation:     actionInfo,
		TransactionUID:        parsed.TransactionUID,
		ReferencedSOPs:        parsed.ReferencedSOPs,
		PresentationContextID: pcID,
		ActionSyntax:          syntax,
	})
	status := result.Status
	if handlerErr != nil && status == StatusSuccess {
		status = StatusStorageCommitmentProcessingFailure
	}
	if err := sendStorageCommitmentActionResponse(ctx, assoc, pcID, *req, status); err != nil {
		return err
	}
	return handlerErr
}

// BuildStorageCommitmentActionInformation builds the action information dataset
// carried by a Storage Commitment N-ACTION-RQ.
func BuildStorageCommitmentActionInformation(transactionUID string, refs []StorageCommitmentSOPReference) (*object.Object, error) {
	transactionUID = strings.TrimSpace(transactionUID)
	if !validStoreUID(transactionUID) {
		return nil, fmt.Errorf("dicom dimse: Storage Commitment TransactionUID is required")
	}
	normalized, err := normalizeStorageCommitmentReferences(refs, false, defaultStorageCommitmentMaxReferences)
	if err != nil {
		return nil, fmt.Errorf("dicom dimse: Storage Commitment referenced SOP sequence is required")
	}
	return object.FromElements([]core.Element{
		{
			Header: core.ElementHeader{Tag: StorageCommitmentTransactionUID, VR: core.VRUI},
			Value:  core.StringValue{transactionUID},
		},
		storageCommitmentSOPSequenceElement(StorageCommitmentReferencedSOPSequence, normalized, false),
	}, std.Dictionary), nil
}

// ParseStorageCommitmentActionInformation parses the action information dataset
// carried by a Storage Commitment N-ACTION-RQ.
func ParseStorageCommitmentActionInformation(dataset *object.Object) (StorageCommitmentActionInformation, error) {
	if dataset == nil {
		return StorageCommitmentActionInformation{}, fmt.Errorf("dicom dimse: missing Storage Commitment action information")
	}
	transactionUID, err := storageCommitmentSingleUID(dataset, StorageCommitmentTransactionUID)
	if err != nil {
		return StorageCommitmentActionInformation{}, fmt.Errorf("dicom dimse: missing Storage Commitment TransactionUID")
	}
	refs, err := storageCommitmentSOPReferences(dataset, StorageCommitmentReferencedSOPSequence, false)
	if err != nil {
		return StorageCommitmentActionInformation{}, err
	}
	if len(refs) == 0 {
		return StorageCommitmentActionInformation{}, fmt.Errorf("dicom dimse: missing Storage Commitment ReferencedSOPSequence")
	}
	return StorageCommitmentActionInformation{
		TransactionUID: transactionUID,
		ReferencedSOPs: refs,
	}, nil
}

// BuildStorageCommitmentEventInformation builds the event information dataset
// carried by a Storage Commitment N-EVENT-REPORT-RQ.
func BuildStorageCommitmentEventInformation(transactionUID string, committed []StorageCommitmentSOPReference, failed []StorageCommitmentSOPReference) (*object.Object, error) {
	transactionUID = strings.TrimSpace(transactionUID)
	if !validStoreUID(transactionUID) {
		return nil, fmt.Errorf("dicom dimse: Storage Commitment TransactionUID is required")
	}
	if len(committed) == 0 && len(failed) == 0 {
		return nil, fmt.Errorf("dicom dimse: Storage Commitment event information requires at least one referenced or failed SOP")
	}
	var err error
	if len(committed) > 0 {
		committed, err = normalizeStorageCommitmentReferences(committed, false, defaultStorageCommitmentMaxReferences)
		if err != nil {
			return nil, ErrStorageCommitmentInvalidResult
		}
	}
	if len(failed) > 0 {
		failed, err = normalizeStorageCommitmentReferences(failed, true, defaultStorageCommitmentMaxReferences)
		if err != nil {
			return nil, ErrStorageCommitmentInvalidResult
		}
	}
	seen := make(map[string]bool, len(committed))
	for _, ref := range committed {
		seen[ref.SOPInstanceUID] = true
	}
	for _, ref := range failed {
		if seen[ref.SOPInstanceUID] {
			return nil, ErrStorageCommitmentInvalidResult
		}
	}
	elements := []core.Element{
		{
			Header: core.ElementHeader{Tag: StorageCommitmentTransactionUID, VR: core.VRUI},
			Value:  core.StringValue{transactionUID},
		},
	}
	if len(committed) > 0 {
		elements = append(elements, storageCommitmentSOPSequenceElement(StorageCommitmentReferencedSOPSequence, committed, false))
	}
	if len(failed) > 0 {
		elements = append(elements, storageCommitmentSOPSequenceElement(StorageCommitmentFailedSOPSequence, failed, true))
	}
	return object.FromElements(elements, std.Dictionary), nil
}

// ParseStorageCommitmentEventInformation parses the event information dataset
// carried by a Storage Commitment N-EVENT-REPORT-RQ.
func ParseStorageCommitmentEventInformation(dataset *object.Object) (StorageCommitmentEventInformation, error) {
	if dataset == nil {
		return StorageCommitmentEventInformation{}, fmt.Errorf("dicom dimse: missing Storage Commitment event information")
	}
	transactionUID, err := storageCommitmentSingleUID(dataset, StorageCommitmentTransactionUID)
	if err != nil {
		return StorageCommitmentEventInformation{}, fmt.Errorf("dicom dimse: missing Storage Commitment TransactionUID")
	}
	committed, err := storageCommitmentSOPReferences(dataset, StorageCommitmentReferencedSOPSequence, false)
	if err != nil {
		return StorageCommitmentEventInformation{}, err
	}
	failed, err := storageCommitmentSOPReferences(dataset, StorageCommitmentFailedSOPSequence, true)
	if err != nil {
		return StorageCommitmentEventInformation{}, err
	}
	if len(committed) == 0 && len(failed) == 0 {
		return StorageCommitmentEventInformation{}, fmt.Errorf("dicom dimse: Storage Commitment event information missing referenced and failed SOP sequences")
	}
	seen := make(map[string]bool, len(committed)+len(failed))
	for _, refs := range [][]StorageCommitmentSOPReference{committed, failed} {
		for _, ref := range refs {
			key := ref.SOPInstanceUID
			if seen[key] {
				return StorageCommitmentEventInformation{}, ErrStorageCommitmentInvalidResult
			}
			seen[key] = true
		}
	}
	return StorageCommitmentEventInformation{
		TransactionUID: transactionUID,
		ReferencedSOPs: committed,
		FailedSOPs:     failed,
	}, nil
}

func sendStorageCommitmentActionResponse(ctx context.Context, assoc *ul.Association, pcID byte, req NActionRequest, status uint16) error {
	response := NActionResponse{
		AffectedSOPClassUID:       req.RequestedSOPClassUID,
		AffectedSOPInstanceUID:    req.RequestedSOPInstanceUID,
		MessageIDBeingRespondedTo: req.MessageID,
		Status:                    status,
		HasActionReply:            false,
	}
	return SendCommandSetWithContext(ctx, assoc, pcID, response.CommandSet())
}

func storageCommitmentSOPSequenceElement(tag core.Tag, refs []StorageCommitmentSOPReference, includeFailureReason bool) core.Element {
	items := make([]core.DataSet, 0, len(refs))
	for _, ref := range refs {
		elements := []core.Element{
			{
				Header: core.ElementHeader{Tag: StorageCommitmentReferencedSOPClass, VR: core.VRUI},
				Value:  core.StringValue{ref.SOPClassUID},
			},
			{
				Header: core.ElementHeader{Tag: StorageCommitmentReferencedSOPInstance, VR: core.VRUI},
				Value:  core.StringValue{ref.SOPInstanceUID},
			},
		}
		if includeFailureReason {
			elements = append(elements, core.Element{
				Header: core.ElementHeader{Tag: StorageCommitmentFailureReason, VR: core.VRUS},
				Value:  core.RawValue{byte(ref.FailureReason), byte(ref.FailureReason >> 8)},
			})
		}
		items = append(items, core.DataSet{Elements: elements})
	}
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}
}

func storageCommitmentSOPReferences(dataset *object.Object, tag core.Tag, includeFailureReason bool) ([]StorageCommitmentSOPReference, error) {
	element, present := dataset.Get(tag)
	if present && element.VR() != core.VRSQ {
		return nil, ErrStorageCommitmentInvalidResult
	}
	items, ok := dataset.GetSequence(tag)
	if !ok {
		return nil, nil
	}
	if len(items) > defaultStorageCommitmentMaxReferences {
		return nil, ErrStorageCommitmentResourceLimit
	}
	refs := make([]StorageCommitmentSOPReference, 0, len(items))
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		sopClassUID, err := storageCommitmentSingleUID(item, StorageCommitmentReferencedSOPClass)
		if err != nil {
			return nil, fmt.Errorf("dicom dimse: Storage Commitment sequence item %d missing ReferencedSOPClassUID", i)
		}
		sopInstanceUID, err := storageCommitmentSingleUID(item, StorageCommitmentReferencedSOPInstance)
		if err != nil {
			return nil, fmt.Errorf("dicom dimse: Storage Commitment sequence item %d missing ReferencedSOPInstanceUID", i)
		}
		ref := StorageCommitmentSOPReference{SOPClassUID: sopClassUID, SOPInstanceUID: sopInstanceUID}
		key := ref.SOPInstanceUID
		if seen[key] {
			return nil, ErrStorageCommitmentInvalidResult
		}
		seen[key] = true
		if includeFailureReason {
			reason, err := commandLikeUint16(item, StorageCommitmentFailureReason)
			if err != nil {
				return nil, err
			}
			if !validStorageCommitmentFailureReason(reason) {
				return nil, ErrStorageCommitmentInvalidResult
			}
			ref.FailureReason = reason
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func commandLikeUint16(obj *object.Object, tag core.Tag) (uint16, error) {
	element, ok := obj.Get(tag)
	if !ok || element.VR() != core.VRUS {
		return 0, fmt.Errorf("dicom dimse: missing Storage Commitment element %s", tag)
	}
	raw, ok := obj.GetRaw(tag)
	if !ok {
		return 0, fmt.Errorf("dicom dimse: missing Storage Commitment element %s", tag)
	}
	if len(raw) != 2 {
		return 0, fmt.Errorf("dicom dimse: Storage Commitment element %s has invalid byte value", tag)
	}
	return uint16(raw[0]) | uint16(raw[1])<<8, nil
}

func storageCommitmentSingleUID(dataset *object.Object, tag core.Tag) (string, error) {
	element, ok := dataset.Get(tag)
	if !ok || element.VR() != core.VRUI {
		return "", ErrStorageCommitmentInvalidRequest
	}
	values := element.StringValues()
	if len(values) != 1 {
		return "", ErrStorageCommitmentInvalidRequest
	}
	value := strings.TrimSpace(values[0])
	if !validStoreUID(value) {
		return "", ErrStorageCommitmentInvalidRequest
	}
	return value, nil
}
