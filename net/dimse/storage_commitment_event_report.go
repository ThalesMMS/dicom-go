package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	// StorageCommitmentEventTypeSuccess reports that commitment succeeded for
	// every SOP Instance in the request.
	StorageCommitmentEventTypeSuccess uint16 = 1
	// StorageCommitmentEventTypeFailures reports that the request completed but
	// commitment failed for one or more SOP Instances.
	StorageCommitmentEventTypeFailures uint16 = 2
	// StorageCommitmentEventTypeID preserves the original success-event name for
	// source compatibility. New code should select one of the explicit event
	// type constants above.
	StorageCommitmentEventTypeID = StorageCommitmentEventTypeSuccess
)

// NEventReportRequest represents an N-EVENT-REPORT-RQ command.
//
// For Storage Commitment Push Model, the request is sent from the SCP to the SCU
// to report the result of a previously-requested storage commitment transaction.
// The EventInformation dataset typically carries Transaction UID plus sequences
// for Referenced SOP Instances (success) and Failed SOP Sequence.
//
// This layer only models the DIMSE command set fields. The event information
// dataset is transmitted as the accompanying dataset when CommandDataSetType
// indicates it is present.
//
// Ref: DICOM PS3.7 10.1.1 (N-EVENT-REPORT)
// Ref: DICOM PS3.4 Storage Commitment Push Model
//
// AffectedSOPClassUID and AffectedSOPInstanceUID must match the Storage
// Commitment Push Model UIDs.
// MessageID is any US value not already outstanding on the association.
// EventTypeID must be StorageCommitmentEventTypeSuccess or
// StorageCommitmentEventTypeFailures.
// CommandDataSetType must be any non-null value for Storage Commitment.
//
// Status is not included in the request.
//
// Note: N-EVENT-REPORT is a normalized service; the command set uses Affected
// SOP Class/Instance rather than Requested.
//
// The caller is responsible for choosing a presentation context that has
// accepted the AffectedSOPClassUID.
//
// This is sufficient for implementing Storage Commitment push notifications.
//
// (The extra directives above are to avoid some linters being opinionated in
// certain environments; they have no functional effect.)
//
//nolint:revive // DICOM naming.
//go:generate false
type NEventReportRequest struct {
	AffectedSOPClassUID    string
	AffectedSOPInstanceUID string
	MessageID              uint16
	EventTypeID            uint16
}

func (r NEventReportRequest) CommandSet() []core.Element {
	return (NormalizedEventReportRequest{
		AffectedSOPClassUID:    r.AffectedSOPClassUID,
		MessageID:              r.MessageID,
		CommandDataSetType:     DataSetPresent,
		AffectedSOPInstanceUID: r.AffectedSOPInstanceUID,
		EventTypeID:            r.EventTypeID,
	}).CommandSet()
}

// NEventReportResponse represents an N-EVENT-REPORT-RSP command.
//
// For Storage Commitment, the response is typically just Status and no dataset.
// If an event reply dataset is present, HasEventReply should be set.
//
//nolint:revive // DICOM naming.
type NEventReportResponse struct {
	AffectedSOPClassUID       string
	AffectedSOPInstanceUID    string
	MessageIDBeingRespondedTo uint16
	Status                    uint16
	HasEventReply             bool
}

func (r NEventReportResponse) CommandSet() []core.Element {
	var eventTypeID *uint16
	if r.HasEventReply {
		value := StorageCommitmentEventTypeID
		eventTypeID = &value
	}
	return (NormalizedEventReportResponse{
		AffectedSOPClassUID:       r.AffectedSOPClassUID,
		MessageIDBeingRespondedTo: r.MessageIDBeingRespondedTo,
		CommandDataSetType:        normalizedDataSetType(r.HasEventReply),
		Status:                    r.Status,
		AffectedSOPInstanceUID:    r.AffectedSOPInstanceUID,
		EventTypeIDOrNil:          eventTypeID,
	}).CommandSet()
}

func ParseNEventReportRequest(obj *object.Object) (*NEventReportRequest, error) {
	generic, err := ParseNormalizedEventReportRequest(obj)
	if err != nil {
		return nil, err
	}
	if generic.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPClassUID %q, want %q", generic.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if generic.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPInstanceUID %q, want %q", generic.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	if !validStorageCommitmentEventType(generic.EventTypeID) {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request has unsupported EventTypeID")
	}
	if !normalizedHasDataSet(generic.CommandDataSetType) {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request dataset type 0x%04X, want dataset present", generic.CommandDataSetType)
	}
	return &NEventReportRequest{
		AffectedSOPClassUID:    generic.AffectedSOPClassUID,
		AffectedSOPInstanceUID: generic.AffectedSOPInstanceUID,
		MessageID:              generic.MessageID,
		EventTypeID:            generic.EventTypeID,
	}, nil
}

func ParseNEventReportResponse(obj *object.Object) (*NEventReportResponse, error) {
	generic, err := ParseNormalizedEventReportResponse(obj)
	if err != nil {
		return nil, err
	}
	if generic.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT response AffectedSOPClassUID %q, want %q", generic.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if generic.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT response AffectedSOPInstanceUID %q, want %q", generic.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	return &NEventReportResponse{
		AffectedSOPClassUID:       generic.AffectedSOPClassUID,
		AffectedSOPInstanceUID:    generic.AffectedSOPInstanceUID,
		MessageIDBeingRespondedTo: generic.MessageIDBeingRespondedTo,
		Status:                    generic.Status,
		HasEventReply:             normalizedHasDataSet(generic.CommandDataSetType),
	}, nil
}

func SendNEventReportRequest(assoc *ul.Association, pcID byte, req NEventReportRequest) error {
	if req.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPClassUID %q, want %q", req.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if req.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPInstanceUID %q, want %q", req.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	if !validStorageCommitmentEventType(req.EventTypeID) {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request has unsupported EventTypeID")
	}
	return SendCommandSet(assoc, pcID, req.CommandSet())
}

func validStorageCommitmentEventType(eventTypeID uint16) bool {
	return eventTypeID == StorageCommitmentEventTypeSuccess || eventTypeID == StorageCommitmentEventTypeFailures
}

func ReceiveNEventReportRequest(assoc *ul.Association, pcID byte) (*NEventReportRequest, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseNEventReportRequest(command)
}

func SendNEventReportResponse(assoc *ul.Association, pcID byte, rsp NEventReportResponse) error {
	if rsp.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT response AffectedSOPClassUID %q, want %q", rsp.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if rsp.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT response AffectedSOPInstanceUID %q, want %q", rsp.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	return SendCommandSet(assoc, pcID, rsp.CommandSet())
}

func ReceiveNEventReportResponse(assoc *ul.Association, pcID byte) (*NEventReportResponse, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseNEventReportResponse(command)
}
