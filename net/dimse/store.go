package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type CStoreRequest struct {
	AffectedSOPClassUID    string
	MessageID              uint16
	Priority               uint16
	AffectedSOPInstanceUID string
}

func (r CStoreRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CStoreRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(Priority, r.Priority),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
	}
}

type CStoreResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	AffectedSOPInstanceUID    string
	Status                    uint16
}

func (r CStoreResponse) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CStoreRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUSCommandElement(Status, r.Status),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
	}
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
	if dataSetType != DataSetPresent {
		return nil, fmt.Errorf("dicom dimse: C-STORE request dataset type 0x%04X, want dataset present 0x%04X", dataSetType, DataSetPresent)
	}
	sopInstanceUID, err := commandUID(obj, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	return &CStoreRequest{
		AffectedSOPClassUID:    sopClassUID,
		MessageID:              messageID,
		Priority:               priority,
		AffectedSOPInstanceUID: sopInstanceUID,
	}, nil
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
	return &CStoreResponse{
		AffectedSOPClassUID:       sopClassUID,
		MessageIDBeingRespondedTo: messageID,
		AffectedSOPInstanceUID:    sopInstanceUID,
		Status:                    status,
	}, nil
}

func SendCStoreRequest(assoc *ul.Association, pcID byte, req CStoreRequest) error {
	if req.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: missing C-STORE Affected SOP Class UID")
	}
	if req.AffectedSOPInstanceUID == "" {
		return fmt.Errorf("dicom dimse: missing C-STORE Affected SOP Instance UID")
	}
	return SendCommandSet(assoc, pcID, req.CommandSet())
}

func ReceiveCStoreRequest(assoc *ul.Association, pcID byte) (*CStoreRequest, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseCStoreRequest(command)
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
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseCStoreResponse(command)
}

func SendDataSet(assoc *ul.Association, pcID byte, dataset *object.Object, syntax transfer.Syntax) error {
	writer := NewPDataWriter(assoc, pcID, false, peerMaxPDUWithHeader(assoc))
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
	reader := newTypedPDataReaderWithContext(ctx, assoc, pcID, false)
	dataset, err := object.ReadDataSet(reader, syntax)
	if err != nil {
		return nil, fmt.Errorf("dicom dimse: receive dataset: %w", err)
	}
	return dataset, nil
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

func commandUID(command *object.Object, tag core.Tag) (string, error) {
	uid, ok := command.GetUID(tag)
	if !ok || uid == "" {
		return "", fmt.Errorf("dicom dimse: missing command UID element %s", tag)
	}
	return uid, nil
}
