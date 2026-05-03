package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	// Storage Commitment Push Model uses Event Type ID = 1 for commitment result.
	StorageCommitmentEventTypeID uint16 = 1
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
// MessageID must be non-zero.
// EventTypeID must be StorageCommitmentEventTypeID.
// CommandDataSetType is expected to be DataSetPresent for Storage Commitment.
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
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, NEventReportRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
		newUSCommandElement(EventTypeID, r.EventTypeID),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}
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
	datasetType := NoDataSet
	if r.HasEventReply {
		datasetType = DataSetPresent
	}
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, NEventReportRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
		newUSCommandElement(CommandDataSetType, datasetType),
		newUSCommandElement(Status, r.Status),
	}
}

func ParseNEventReportRequest(obj *object.Object) (*NEventReportRequest, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != NEventReportRQ {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want N-EVENT-REPORT-RQ 0x%04X", field, NEventReportRQ)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	if sopClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPClassUID %q, want %q", sopClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	msgID, err := CommandUint16(obj, MessageID)
	if err != nil {
		return nil, err
	}
	sopInstanceUID, err := commandUID(obj, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	if sopInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPInstanceUID %q, want %q", sopInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	eventTypeID, err := CommandUint16(obj, EventTypeID)
	if err != nil {
		return nil, err
	}
	if eventTypeID != StorageCommitmentEventTypeID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request EventTypeID %d, want StorageCommitmentEventTypeID %d", eventTypeID, StorageCommitmentEventTypeID)
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dataSetType != DataSetPresent {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT request dataset type 0x%04X, want dataset present 0x%04X", dataSetType, DataSetPresent)
	}
	return &NEventReportRequest{
		AffectedSOPClassUID:    sopClassUID,
		AffectedSOPInstanceUID: sopInstanceUID,
		MessageID:              msgID,
		EventTypeID:            eventTypeID,
	}, nil
}

func ParseNEventReportResponse(obj *object.Object) (*NEventReportResponse, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != NEventReportRSP {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want N-EVENT-REPORT-RSP 0x%04X", field, NEventReportRSP)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	if sopClassUID != StorageCommitmentPushModelSOPClassUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT response AffectedSOPClassUID %q, want %q", sopClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	msgID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	sopInstanceUID, err := commandUID(obj, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	if sopInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT response AffectedSOPInstanceUID %q, want %q", sopInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	var hasEventReply bool
	switch dataSetType {
	case DataSetPresent:
		hasEventReply = true
	case DataSetAbsent:
		hasEventReply = false
	default:
		return nil, fmt.Errorf("dicom dimse: N-EVENT-REPORT response dataset type 0x%04X, want 0x%04X or 0x%04X", dataSetType, DataSetPresent, DataSetAbsent)
	}
	status, err := CommandUint16(obj, Status)
	if err != nil {
		return nil, err
	}
	return &NEventReportResponse{
		AffectedSOPClassUID:       sopClassUID,
		AffectedSOPInstanceUID:    sopInstanceUID,
		MessageIDBeingRespondedTo: msgID,
		Status:                    status,
		HasEventReply:             hasEventReply,
	}, nil
}

func SendNEventReportRequest(assoc *ul.Association, pcID byte, req NEventReportRequest) error {
	if req.MessageID == 0 {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request MessageID must be non-zero")
	}
	if req.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPClassUID %q, want %q", req.AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID)
	}
	if req.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request AffectedSOPInstanceUID %q, want %q", req.AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID)
	}
	if req.EventTypeID != StorageCommitmentEventTypeID {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT request EventTypeID %d, want StorageCommitmentEventTypeID %d", req.EventTypeID, StorageCommitmentEventTypeID)
	}
	return SendCommandSet(assoc, pcID, req.CommandSet())
}

func ReceiveNEventReportRequest(assoc *ul.Association, pcID byte) (*NEventReportRequest, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseNEventReportRequest(command)
}

func SendNEventReportResponse(assoc *ul.Association, pcID byte, rsp NEventReportResponse) error {
	if rsp.MessageIDBeingRespondedTo == 0 {
		return fmt.Errorf("dicom dimse: N-EVENT-REPORT response MessageIDBeingRespondedTo must be non-zero")
	}
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
