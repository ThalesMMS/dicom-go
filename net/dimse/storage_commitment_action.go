package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	// Storage Commitment Push Model SOP Class UID.
	StorageCommitmentPushModelSOPClassUID = "1.2.840.10008.1.20.1"
	// Well-known SOP Instance for Storage Commitment Push Model.
	StorageCommitmentPushModelSOPInstanceUID = "1.2.840.10008.1.20.1.1"

	// Storage Commitment Push Model uses Action Type ID = 1.
	StorageCommitmentActionTypeID uint16 = 1
)

// NActionRequest represents an N-ACTION-RQ command.
//
// For Storage Commitment Push Model, the request is sent to the well-known SOP
// instance of the Storage Commitment Push Model SOP Class.
//
// The ActionInformation dataset carries Transaction UID and Referenced SOP
// Sequence, among other elements.
//
// Ref: DICOM PS3.7 10.1.4 (N-ACTION), PS3.4 Storage Commitment.
//
// This implementation focuses on the required/commonly-used command set fields.
// It does not implement optional elements beyond those.
//
// CommandDataSetType MUST be DataSetPresent (action information provided).
//
// Note: Transaction UID and referenced SOP instances are part of the action
// information dataset and are not modeled at this layer.
//
// RequestedSOPClassUID and RequestedSOPInstanceUID must match the Storage
// Commitment Push Model UIDs.
// MessageID must be non-zero.
// ActionTypeID must be StorageCommitmentActionTypeID.
//
// The caller is responsible for choosing a presentation context that has
// accepted the RequestedSOPClassUID.
//
// Status is not included in the request.
type NActionRequest struct {
	RequestedSOPClassUID    string
	RequestedSOPInstanceUID string
	MessageID               uint16
	ActionTypeID            uint16
}

func (r NActionRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(RequestedSOPClassUID, r.RequestedSOPClassUID),
		newUSCommandElement(CommandField, NActionRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUIElement(RequestedSOPInstanceUID, r.RequestedSOPInstanceUID),
		newUSCommandElement(ActionTypeID, r.ActionTypeID),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}
}

// NActionResponse represents an N-ACTION-RSP command.
//
// A response may optionally include an ActionReply dataset; for Storage
// Commitment the response is typically just a Status and no dataset.
type NActionResponse struct {
	AffectedSOPClassUID       string
	AffectedSOPInstanceUID    string
	MessageIDBeingRespondedTo uint16
	Status                    uint16
	HasActionReply            bool
}

func (r NActionResponse) CommandSet() []core.Element {
	datasetType := NoDataSet
	if r.HasActionReply {
		datasetType = DataSetPresent
	}
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, NActionRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
		newUSCommandElement(CommandDataSetType, datasetType),
		newUSCommandElement(Status, r.Status),
	}
}

func ParseNActionRequest(obj *object.Object) (*NActionRequest, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != NActionRQ {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want N-ACTION-RQ 0x%04X", field, NActionRQ)
	}
	sopClassUID, err := commandUID(obj, RequestedSOPClassUID)
	if err != nil {
		return nil, err
	}
	if sopClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request RequestedSOPClassUID %q, want %q", sopClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	msgID, err := CommandUint16(obj, MessageID)
	if err != nil {
		return nil, err
	}
	if msgID == 0 {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request MessageID must be non-zero")
	}
	sopInstanceUID, err := commandUID(obj, RequestedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	if sopInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request RequestedSOPInstanceUID %q, want %q", sopInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	actionTypeID, err := CommandUint16(obj, ActionTypeID)
	if err != nil {
		return nil, err
	}
	if actionTypeID != StorageCommitmentActionTypeID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request ActionTypeID %d, want StorageCommitmentActionTypeID %d", actionTypeID, StorageCommitmentActionTypeID)
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dataSetType != DataSetPresent {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request dataset type 0x%04X, want dataset present 0x%04X", dataSetType, DataSetPresent)
	}
	return &NActionRequest{
		RequestedSOPClassUID:    sopClassUID,
		RequestedSOPInstanceUID: sopInstanceUID,
		MessageID:               msgID,
		ActionTypeID:            actionTypeID,
	}, nil
}

func ParseNActionResponse(obj *object.Object) (*NActionResponse, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != NActionRSP {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want N-ACTION-RSP 0x%04X", field, NActionRSP)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	if sopClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION response AffectedSOPClassUID %q, want %q", sopClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	msgID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	if msgID == 0 {
		return nil, fmt.Errorf("dicom dimse: N-ACTION response MessageIDBeingRespondedTo must be non-zero")
	}
	sopInstanceUID, err := commandUID(obj, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	if sopInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION response AffectedSOPInstanceUID %q, want %q", sopInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	var hasActionReply bool
	switch dataSetType {
	case DataSetPresent:
		hasActionReply = true
	case DataSetAbsent:
		hasActionReply = false
	default:
		return nil, fmt.Errorf("dicom dimse: N-ACTION response dataset type 0x%04X, want 0x%04X or 0x%04X", dataSetType, DataSetPresent, DataSetAbsent)
	}
	status, err := CommandUint16(obj, Status)
	if err != nil {
		return nil, err
	}
	return &NActionResponse{
		AffectedSOPClassUID:       sopClassUID,
		AffectedSOPInstanceUID:    sopInstanceUID,
		MessageIDBeingRespondedTo: msgID,
		Status:                    status,
		HasActionReply:            hasActionReply,
	}, nil
}

func SendNActionRequest(assoc *ul.Association, pcID byte, req NActionRequest) error {
	if req.MessageID == 0 {
		return fmt.Errorf("dicom dimse: N-ACTION request MessageID must be non-zero")
	}
	if req.RequestedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return fmt.Errorf("dicom dimse: N-ACTION request RequestedSOPClassUID %q, want %q", req.RequestedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if req.RequestedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return fmt.Errorf("dicom dimse: N-ACTION request RequestedSOPInstanceUID %q, want %q", req.RequestedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	if req.ActionTypeID != StorageCommitmentActionTypeID {
		return fmt.Errorf("dicom dimse: N-ACTION request ActionTypeID %d, want StorageCommitmentActionTypeID %d", req.ActionTypeID, StorageCommitmentActionTypeID)
	}
	return SendCommandSet(assoc, pcID, req.CommandSet())
}

func ReceiveNActionRequest(assoc *ul.Association, pcID byte) (*NActionRequest, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseNActionRequest(command)
}

func SendNActionResponse(assoc *ul.Association, pcID byte, rsp NActionResponse) error {
	if rsp.MessageIDBeingRespondedTo == 0 {
		return fmt.Errorf("dicom dimse: N-ACTION response MessageIDBeingRespondedTo must be non-zero")
	}
	if rsp.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return fmt.Errorf("dicom dimse: N-ACTION response AffectedSOPClassUID %q, want %q", rsp.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if rsp.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return fmt.Errorf("dicom dimse: N-ACTION response AffectedSOPInstanceUID %q, want %q", rsp.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	return SendCommandSet(assoc, pcID, rsp.CommandSet())
}

func ReceiveNActionResponse(assoc *ul.Association, pcID byte) (*NActionResponse, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseNActionResponse(command)
}
