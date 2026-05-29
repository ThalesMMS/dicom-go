package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// MaxIdentifierBytes bounds query/retrieve Identifier datasets. Image-bearing
// C-STORE datasets use their separate StorageSCPOptions.MaxDataSetBytes policy.
const MaxIdentifierBytes int64 = 4 << 20

type CStoreRequest struct {
	AffectedSOPClassUID          string
	MessageID                    uint16
	Priority                     uint16
	AffectedSOPInstanceUID       string
	MoveOriginatorAETitle        string
	MoveOriginatorMessageIDOrNil *uint16
}

func (r CStoreRequest) CommandSet() []core.Element {
	elements := []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CStoreRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(Priority, r.Priority),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
	}
	if r.MoveOriginatorAETitle != "" {
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: MoveOriginatorApplicationEntityTitle, VR: core.VRAE},
			Value:  core.StringValue{r.MoveOriginatorAETitle},
		})
	}
	if r.MoveOriginatorMessageIDOrNil != nil {
		elements = append(elements, newUSCommandElement(MoveOriginatorMessageID, *r.MoveOriginatorMessageIDOrNil))
	}
	return elements
}

type CStoreResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	AffectedSOPInstanceUID    string
	Status                    uint16
	ErrorComment              string
}

func (r CStoreResponse) CommandSet() []core.Element {
	elements := []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CStoreRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUSCommandElement(Status, r.Status),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
	}
	if r.ErrorComment != "" {
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: ErrorComment, VR: core.VRLO},
			Value:  core.StringValue{r.ErrorComment},
		})
	}
	return elements
}

func ParseCStoreRequest(obj *object.Object) (*CStoreRequest, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CStoreRQ {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-STORE-RQ 0x%04X", field, CStoreRQ)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	messageID, err := CommandUint16(obj, MessageID)
	if err != nil {
		return nil, err
	}
	priority, err := CommandUint16(obj, Priority)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dataSetType == NoDataSet {
		return nil, fmt.Errorf("dicom dimse: C-STORE request dataset type 0x%04X, want dataset present", dataSetType)
	}
	sopInstanceUID, err := commandUID(obj, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	req := &CStoreRequest{
		AffectedSOPClassUID:    sopClassUID,
		MessageID:              messageID,
		Priority:               priority,
		AffectedSOPInstanceUID: sopInstanceUID,
	}
	if originator, ok := obj.GetString(MoveOriginatorApplicationEntityTitle); ok {
		req.MoveOriginatorAETitle = originator
	}
	originatorMessageID, err := optionalCommandUint16(obj, MoveOriginatorMessageID)
	if err != nil {
		return nil, err
	}
	req.MoveOriginatorMessageIDOrNil = originatorMessageID
	return req, nil
}

func ParseCStoreResponse(obj *object.Object) (*CStoreResponse, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CStoreRSP {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-STORE-RSP 0x%04X", field, CStoreRSP)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	messageID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dataSetType != NoDataSet {
		return nil, fmt.Errorf("dicom dimse: C-STORE response dataset type 0x%04X, want no dataset 0x%04X", dataSetType, NoDataSet)
	}
	status, err := CommandUint16(obj, Status)
	if err != nil {
		return nil, err
	}
	sopInstanceUID, err := commandUID(obj, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	var errorComment string
	if s, ok := obj.GetString(ErrorComment); ok {
		errorComment = s
	}
	return &CStoreResponse{
		AffectedSOPClassUID:       sopClassUID,
		MessageIDBeingRespondedTo: messageID,
		AffectedSOPInstanceUID:    sopInstanceUID,
		Status:                    status,
		ErrorComment:              errorComment,
	}, nil
}

func SendCStoreRequest(assoc *ul.Association, pcID byte, req CStoreRequest) error {
	return SendCStoreRequestWithContext(nil, assoc, pcID, req)
}

// SendCStoreRequestWithContext sends one C-STORE-RQ while making fragmented
// command P-DATA writes observe ctx.
func SendCStoreRequestWithContext(ctx context.Context, assoc *ul.Association, pcID byte, req CStoreRequest) error {
	if req.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: missing C-STORE Affected SOP Class UID")
	}
	if req.AffectedSOPInstanceUID == "" {
		return fmt.Errorf("dicom dimse: missing C-STORE Affected SOP Instance UID")
	}
	return SendCommandSetWithContext(ctx, assoc, pcID, req.CommandSet())
}

func ReceiveCStoreRequest(assoc *ul.Association, pcID byte) (*CStoreRequest, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseCStoreRequest(command)
}

// ReceiveCStoreRequestAny reads a C-STORE-RQ command on whichever accepted
// presentation context the SCU used, returning that PC ID alongside the parsed
// request. Use it instead of ReceiveCStoreRequest when an association accepts
// multiple presentation contexts and the SCU may issue C-STORE on any of them:
// the returned PC ID identifies the context whose transfer syntax must be used
// to decode the following dataset.
//
// Passing a nil context lets the read inherit the association's own context
// (assoc.Context), so an SCP that cancels its association context to stop the
// receiver unblocks this read cleanly instead of blocking until peer activity.
func ReceiveCStoreRequestAny(assoc *ul.Association) (byte, *CStoreRequest, error) {
	pcID, command, err := receiveCommandSetAnyWithContext(nil, assoc)
	if err != nil {
		return 0, nil, err
	}
	req, err := ParseCStoreRequest(command)
	if err != nil {
		return 0, nil, err
	}
	return pcID, req, nil
}

func SendCStoreResponse(assoc *ul.Association, pcID byte, rsp CStoreResponse) error {
	if rsp.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: missing C-STORE response Affected SOP Class UID")
	}
	if rsp.AffectedSOPInstanceUID == "" {
		return fmt.Errorf("dicom dimse: missing C-STORE response Affected SOP Instance UID")
	}
	return SendCommandSet(assoc, pcID, rsp.CommandSet())
}

func ReceiveCStoreResponse(assoc *ul.Association, pcID byte) (*CStoreResponse, error) {
	return ReceiveCStoreResponseWithContext(nil, assoc, pcID)
}

func ReceiveCStoreResponseWithContext(ctx context.Context, assoc *ul.Association, pcID byte) (*CStoreResponse, error) {
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseCStoreResponse(command)
}

func SendDataSet(assoc *ul.Association, pcID byte, dataset *object.Object, syntax transfer.Syntax) error {
	return SendDataSetWithContext(nil, assoc, pcID, dataset, syntax)
}

// SendDataSetWithContext sends one DIMSE dataset while making every fragmented
// P-DATA write observe ctx.
func SendDataSetWithContext(ctx context.Context, assoc *ul.Association, pcID byte, dataset *object.Object, syntax transfer.Syntax) error {
	ctx = dataSetWriteContext(ctx)
	writer := NewPDataWriterWithContext(ctx, assoc, pcID, false, peerMaxPDUWithHeader(assoc))
	if err := object.WriteDataSet(writer, dataset, syntax); err != nil {
		return fmt.Errorf("dicom dimse: send dataset: %w", err)
	}
	if err := writer.Finish(); err != nil {
		return fmt.Errorf("dicom dimse: finish dataset P-DATA: %w", err)
	}
	return nil
}

func ReceiveDataSet(assoc *ul.Association, pcID byte, syntax transfer.Syntax) (*object.Object, error) {
	return receiveDataSetWithContext(nil, assoc, pcID, syntax)
}

func receiveDataSetWithContext(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax) (*object.Object, error) {
	return receiveDataSetWithContextAndLimit(ctx, assoc, pcID, syntax, 0)
}

func receiveIdentifierDataSetWithContext(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax) (*object.Object, error) {
	return receiveDataSetWithContextAndLimit(ctx, assoc, pcID, syntax, MaxIdentifierBytes)
}

func receiveDataSetWithContextAndLimit(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, maxBytes int64) (*object.Object, error) {
	return receiveDataSetWithContextAndLimits(ctx, assoc, pcID, syntax, maxBytes, 0, 0)
}

func receiveDataSetWithContextAndLimits(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, maxBytes int64, maxElements, maxDepth int) (*object.Object, error) {
	dataset, _, err := receiveDataSetWithContextLimitsAndSize(ctx, assoc, pcID, syntax, maxBytes, maxElements, maxDepth)
	return dataset, err
}

func receiveDataSetWithContextLimitsAndSize(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, maxBytes int64, maxElements, maxDepth int) (*object.Object, int64, error) {
	ctx = dataSetReadContext(ctx, assoc)
	reader := newTypedPDataReaderWithContext(ctx, assoc, pcID, false)
	dataset, err := object.ReadDataSetWithOptions(reader, syntax, object.ReadFileOptions{
		MaxElementBytes:  maxBytes,
		MaxTotalBytes:    maxBytes,
		MaxElements:      maxElements,
		MaxSequenceDepth: maxDepth,
	})
	if err != nil {
		if shouldDrainDataSetPDataOnError(err) {
			if drainErr := drainPDataReader(reader); drainErr != nil {
				return nil, int64(reader.receivedBytes), fmt.Errorf("dicom dimse: drain rejected dataset: %w", drainErr)
			}
		}
		return nil, int64(reader.receivedBytes), fmt.Errorf("dicom dimse: receive dataset: %w", err)
	}
	return dataset, int64(reader.receivedBytes), nil
}

// AcceptedContextForSOPClass performs a first-match lookup in
// assoc.AcceptedContexts for the requested SOP Class UID. This minimal helper
// does not consider multiple accepted contexts for the same SOP Class or prefer
// one transfer syntax over another.
func AcceptedContextForSOPClass(assoc *ul.Association, sopClassUID string) (ul.AcceptedContext, error) {
	if assoc == nil {
		return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: nil association")
	}
	for _, pc := range assoc.AcceptedContexts {
		if pc.AbstractSyntaxUID == sopClassUID {
			return pc, nil
		}
	}
	return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: no accepted presentation context for SOP Class UID %q", sopClassUID)
}

// AcceptedContextForSOPClassTransferSyntaxes returns the accepted presentation
// context matching the requested SOP Class UID and the first matching transfer
// syntax UID in transferSyntaxUIDs preference order.
func AcceptedContextForSOPClassTransferSyntaxes(assoc *ul.Association, sopClassUID string, transferSyntaxUIDs []string) (ul.AcceptedContext, error) {
	if assoc == nil {
		return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: nil association")
	}
	wanted := normalizedTransferSyntaxUIDs(transferSyntaxUIDs)
	if len(wanted) == 0 {
		return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: no requested transfer syntax UID for SOP Class UID %q", sopClassUID)
	}
	for _, transferSyntaxUID := range wanted {
		for _, pc := range assoc.AcceptedContexts {
			if pc.AbstractSyntaxUID == sopClassUID && transfer.NormalizeUID(pc.TransferSyntaxUID) == transferSyntaxUID {
				return pc, nil
			}
		}
	}
	return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: no accepted presentation context for SOP Class UID %q and transfer syntax UIDs %v", sopClassUID, wanted)
}

func normalizedTransferSyntaxUIDs(transferSyntaxUIDs []string) []string {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(transferSyntaxUIDs))
	for _, uid := range transferSyntaxUIDs {
		uid = transfer.NormalizeUID(uid)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		normalized = append(normalized, uid)
	}
	return normalized
}

func commandUID(command *object.Object, tag core.Tag) (string, error) {
	uid, ok := command.GetUID(tag)
	if !ok || uid == "" {
		return "", fmt.Errorf("dicom dimse: missing command UID element %s", tag)
	}
	return uid, nil
}
