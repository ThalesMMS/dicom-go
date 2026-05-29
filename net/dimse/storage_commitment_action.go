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
// MessageID is any US value not already outstanding on the association.
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
	return (NormalizedActionRequest{
		RequestedSOPClassUID:    r.RequestedSOPClassUID,
		MessageID:               r.MessageID,
		CommandDataSetType:      DataSetPresent,
		RequestedSOPInstanceUID: r.RequestedSOPInstanceUID,
		ActionTypeID:            r.ActionTypeID,
	}).CommandSet()
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
	var actionTypeID *uint16
	if r.HasActionReply {
		value := StorageCommitmentActionTypeID
		actionTypeID = &value
	}
	return (NormalizedActionResponse{
		AffectedSOPClassUID:       r.AffectedSOPClassUID,
		MessageIDBeingRespondedTo: r.MessageIDBeingRespondedTo,
		CommandDataSetType:        normalizedDataSetType(r.HasActionReply),
		Status:                    r.Status,
		AffectedSOPInstanceUID:    r.AffectedSOPInstanceUID,
		ActionTypeIDOrNil:         actionTypeID,
	}).CommandSet()
}

func ParseNActionRequest(obj *object.Object) (*NActionRequest, error) {
	generic, err := ParseNormalizedActionRequest(obj)
	if err != nil {
		return nil, err
	}
	if generic.RequestedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request RequestedSOPClassUID %q, want %q", generic.RequestedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if generic.RequestedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request RequestedSOPInstanceUID %q, want %q", generic.RequestedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	if generic.ActionTypeID != StorageCommitmentActionTypeID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request ActionTypeID %d, want StorageCommitmentActionTypeID %d", generic.ActionTypeID, StorageCommitmentActionTypeID)
	}
	if !normalizedHasDataSet(generic.CommandDataSetType) {
		return nil, fmt.Errorf("dicom dimse: N-ACTION request dataset type 0x%04X, want dataset present", generic.CommandDataSetType)
	}
	return &NActionRequest{
		RequestedSOPClassUID:    generic.RequestedSOPClassUID,
		RequestedSOPInstanceUID: generic.RequestedSOPInstanceUID,
		MessageID:               generic.MessageID,
		ActionTypeID:            generic.ActionTypeID,
	}, nil
}

func ParseNActionResponse(obj *object.Object) (*NActionResponse, error) {
	generic, err := ParseNormalizedActionResponse(obj)
	if err != nil {
		return nil, err
	}
	if generic.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION response AffectedSOPClassUID %q, want %q", generic.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if generic.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-ACTION response AffectedSOPInstanceUID %q, want %q", generic.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	return &NActionResponse{
		AffectedSOPClassUID:       generic.AffectedSOPClassUID,
		AffectedSOPInstanceUID:    generic.AffectedSOPInstanceUID,
		MessageIDBeingRespondedTo: generic.MessageIDBeingRespondedTo,
		Status:                    generic.Status,
		HasActionReply:            normalizedHasDataSet(generic.CommandDataSetType),
	}, nil
}

func SendNActionRequest(assoc *ul.Association, pcID byte, req NActionRequest) error {
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
