package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// CMoveRequest represents a C-MOVE-RQ DIMSE command.
// The Identifier (query dataset) is transmitted as the command's accompanying
// dataset (i.e., CommandDataSetType == DataSetPresent) rather than as a command
// element.
//
// This package intentionally models a minimal subset: callers are responsible
// for choosing the SOP Class UID (AffectedSOPClassUID) appropriate for the
// desired Query/Retrieve Information Model.
//
// Ref: DICOM PS3.7 C.4.2 (C-MOVE).
//
// Note: The C-MOVE command set includes additional optional elements (e.g.
// Move Originator AE Title/Message ID) that are out of scope for this subtask.
//
// Priority uses the standard DIMSE Priority element (0=medium, 1=high, 2=low).
//
// MoveDestination must be a valid AE Title.
//
// MessageID is a non-zero caller-managed identifier.
//
// AffectedSOPClassUID must be non-empty.
//
// Identifier dataset is required for C-MOVE.
//
// Command set elements:
// - (0000,0002) Affected SOP Class UID
// - (0000,0100) Command Field
// - (0000,0110) Message ID
// - (0000,0600) Move Destination
// - (0000,0700) Priority
// - (0000,0800) Command Data Set Type
//
// The response command set includes Status and sub-operation counts, which are
// not modeled here yet.
//
// For now, Parse/CommandSet focus on the required base elements.
type CMoveRequest struct {
	AffectedSOPClassUID string
	MessageID           uint16
	Priority            uint16
	MoveDestination     string
}

func (r CMoveRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CMoveRQ),
		newUSCommandElement(MessageID, r.MessageID),
		core.Element{Header: core.ElementHeader{Tag: MoveDestination, VR: core.VRAE}, Value: core.StringValue{r.MoveDestination}},
		newUSCommandElement(Priority, r.Priority),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}
}

// CMoveResponse represents a C-MOVE-RSP DIMSE command.
//
// This minimal struct includes only the common response fields. The query/retrieve
// service defines additional optional fields (e.g. number of remaining/completed
// sub-operations) that are not represented here.
type CMoveResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	Status                    uint16
	Identifier                *object.Object
}

func (r CMoveResponse) CommandSet() []core.Element {
	datasetType := NoDataSet
	if r.Identifier != nil && cMoveResponseStatusAllowsIdentifier(r.Status) {
		datasetType = DataSetPresent
	}
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CMoveRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, datasetType),
		newUSCommandElement(Status, r.Status),
	}
}

func ParseCMoveRequest(obj *object.Object) (*CMoveRequest, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CMoveRQ {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-MOVE-RQ 0x%04X", field, CMoveRQ)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	messageID, err := CommandUint16(obj, MessageID)
	if err != nil {
		return nil, err
	}
	moveDest, ok := obj.GetString(MoveDestination)
	if !ok || moveDest == "" {
		return nil, fmt.Errorf("dicom dimse: missing command element %s", MoveDestination)
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
		return nil, fmt.Errorf("dicom dimse: C-MOVE request dataset type 0x%04X, want dataset present 0x%04X", dataSetType, DataSetPresent)
	}
	return &CMoveRequest{
		AffectedSOPClassUID: sopClassUID,
		MessageID:           messageID,
		Priority:            priority,
		MoveDestination:     moveDest,
	}, nil
}

func ParseCMoveResponse(obj *object.Object) (*CMoveResponse, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CMoveRSP {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-MOVE-RSP 0x%04X", field, CMoveRSP)
	}
	sopClassUID, err := commandUID(obj, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	msgID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	status, err := CommandUint16(obj, Status)
	if err != nil {
		return nil, err
	}
	var identifier *object.Object
	switch dataSetType {
	case NoDataSet:
	case DataSetPresent:
		statusClass := ClassifyCMoveStatus(status)
		if statusClass == CMoveStatusSuccess || statusClass == CMoveStatusPending {
			return nil, fmt.Errorf("dicom dimse: C-MOVE response dataset present for status 0x%04X", status)
		}
		identifier = object.New(nil)
	default:
		return nil, fmt.Errorf("dicom dimse: C-MOVE response dataset type 0x%04X, want 0x%04X or 0x%04X", dataSetType, DataSetPresent, NoDataSet)
	}
	return &CMoveResponse{
		AffectedSOPClassUID:       sopClassUID,
		MessageIDBeingRespondedTo: msgID,
		Status:                    status,
		Identifier:                identifier,
	}, nil
}

func SendCMoveRequest(assoc *ul.Association, pcID byte, req CMoveRequest) error {
	if req.MessageID == 0 {
		return fmt.Errorf("dicom dimse: C-MOVE request MessageID must be non-zero")
	}
	if req.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: C-MOVE request AffectedSOPClassUID is required")
	}
	if req.MoveDestination == "" {
		return fmt.Errorf("dicom dimse: C-MOVE request MoveDestination is required")
	}
	return SendCommandSet(assoc, pcID, req.CommandSet())
}

func ReceiveCMoveRequest(assoc *ul.Association, pcID byte) (*CMoveRequest, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	return ParseCMoveRequest(command)
}

func SendCMoveResponse(assoc *ul.Association, pcID byte, rsp CMoveResponse) error {
	if rsp.MessageIDBeingRespondedTo == 0 {
		return fmt.Errorf("dicom dimse: C-MOVE response MessageIDBeingRespondedTo must be non-zero")
	}
	if rsp.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: C-MOVE response AffectedSOPClassUID is required")
	}
	if rsp.Identifier != nil && !cMoveResponseStatusAllowsIdentifier(rsp.Status) {
		return fmt.Errorf("dicom dimse: C-MOVE response identifier dataset not allowed for status 0x%04X", rsp.Status)
	}
	if err := SendCommandSet(assoc, pcID, rsp.CommandSet()); err != nil {
		return err
	}
	if rsp.Identifier == nil {
		return nil
	}
	syntax, err := acceptedTransferSyntax(assoc, pcID)
	if err != nil {
		return err
	}
	return SendDataSet(assoc, pcID, rsp.Identifier, syntax)
}

func ReceiveCMoveResponse(assoc *ul.Association, pcID byte) (*CMoveResponse, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, err
	}
	rsp, err := ParseCMoveResponse(command)
	if err != nil {
		return nil, err
	}
	if rsp.Identifier == nil {
		return rsp, nil
	}
	syntax, err := acceptedTransferSyntax(assoc, pcID)
	if err != nil {
		return nil, err
	}
	rsp.Identifier, err = ReceiveDataSet(assoc, pcID, syntax)
	if err != nil {
		return nil, err
	}
	return rsp, nil
}

func acceptedTransferSyntax(assoc *ul.Association, pcID byte) (transfer.Syntax, error) {
	if assoc == nil {
		return transfer.Syntax{}, fmt.Errorf("dicom dimse: nil association")
	}
	for _, ac := range assoc.AcceptedContexts {
		if ac.ID != pcID {
			continue
		}
		if syntax, ok := transfer.DefaultRegistry.Get(ac.TransferSyntaxUID); ok {
			return syntax, nil
		}
		return transfer.Syntax{}, fmt.Errorf("dicom dimse: unsupported transfer syntax UID %q for presentation context %d", ac.TransferSyntaxUID, pcID)
	}
	return transfer.Syntax{}, fmt.Errorf("dicom dimse: no accepted presentation context %d", pcID)
}

func cMoveResponseStatusAllowsIdentifier(status uint16) bool {
	switch ClassifyCMoveStatus(status) {
	case CMoveStatusCancel, CMoveStatusWarning, CMoveStatusFailure:
		return true
	default:
		return false
	}
}
