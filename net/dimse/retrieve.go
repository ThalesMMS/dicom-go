package dimse

import (
	"context"
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
// This package models the common C-MOVE command fields plus the optional Move
// Originator fields used when a C-MOVE was triggered by another DIMSE operation.
// Callers are responsible for choosing the SOP Class UID (AffectedSOPClassUID)
// appropriate for the desired Query/Retrieve Information Model.
//
// Ref: DICOM PS3.7 C.4.2 (C-MOVE).
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
// - (0000,1030) Move Originator Application Entity Title, optional
// - (0000,1031) Move Originator Message ID, optional
//
// The response command set includes Status and optional sub-operation counts.
type CMoveRequest struct {
	AffectedSOPClassUID          string
	MessageID                    uint16
	Priority                     uint16
	MoveDestination              string
	MoveOriginatorAETitle        string
	MoveOriginatorMessageIDOrNil *uint16
}

func (r CMoveRequest) CommandSet() []core.Element {
	elements := []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CMoveRQ),
		newUSCommandElement(MessageID, r.MessageID),
		core.Element{Header: core.ElementHeader{Tag: MoveDestination, VR: core.VRAE}, Value: core.StringValue{r.MoveDestination}},
		newUSCommandElement(Priority, r.Priority),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
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

// CMoveResponse represents a C-MOVE-RSP DIMSE command.
//
// Optional sub-operation counts are exposed when present in pending, warning,
// failure, cancel or final success responses. Identifier is only sent/received
// for statuses that allow a response identifier dataset.
type CMoveResponse struct {
	AffectedSOPClassUID                 string
	MessageIDBeingRespondedTo           uint16
	Status                              uint16
	ErrorComment                        string
	Identifier                          *object.Object
	NumberOfRemainingSuboperationsOrNil *uint16
	NumberOfCompletedSuboperationsOrNil *uint16
	NumberOfFailedSuboperationsOrNil    *uint16
	NumberOfWarningSuboperationsOrNil   *uint16
}

func (r CMoveResponse) CommandSet() []core.Element {
	datasetType := NoDataSet
	if r.Identifier != nil && cMoveResponseStatusAllowsIdentifier(r.Status) {
		datasetType = DataSetPresent
	}
	elements := []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CMoveRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, datasetType),
		newUSCommandElement(Status, r.Status),
	}
	if r.NumberOfRemainingSuboperationsOrNil != nil {
		elements = append(elements, newUSCommandElement(NumberOfRemainingSuboperations, *r.NumberOfRemainingSuboperationsOrNil))
	}
	if r.NumberOfCompletedSuboperationsOrNil != nil {
		elements = append(elements, newUSCommandElement(NumberOfCompletedSuboperations, *r.NumberOfCompletedSuboperationsOrNil))
	}
	if r.NumberOfFailedSuboperationsOrNil != nil {
		elements = append(elements, newUSCommandElement(NumberOfFailedSuboperations, *r.NumberOfFailedSuboperationsOrNil))
	}
	if r.NumberOfWarningSuboperationsOrNil != nil {
		elements = append(elements, newUSCommandElement(NumberOfWarningSuboperations, *r.NumberOfWarningSuboperationsOrNil))
	}
	if r.ErrorComment != "" {
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: ErrorComment, VR: core.VRLO},
			Value:  core.StringValue{r.ErrorComment},
		})
	}
	return elements
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
	req := &CMoveRequest{
		AffectedSOPClassUID: sopClassUID,
		MessageID:           messageID,
		Priority:            priority,
		MoveDestination:     moveDest,
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
	if dataSetType != NoDataSet {
		statusClass := ClassifyCMoveStatus(status)
		if statusClass != CMoveStatusSuccess && statusClass != CMoveStatusPending {
			identifier = object.New(nil)
		}
	}
	remaining, err := optionalCommandUint16(obj, NumberOfRemainingSuboperations)
	if err != nil {
		return nil, err
	}
	completed, err := optionalCommandUint16(obj, NumberOfCompletedSuboperations)
	if err != nil {
		return nil, err
	}
	failed, err := optionalCommandUint16(obj, NumberOfFailedSuboperations)
	if err != nil {
		return nil, err
	}
	warning, err := optionalCommandUint16(obj, NumberOfWarningSuboperations)
	if err != nil {
		return nil, err
	}
	errorComment := ""
	if s, ok := obj.GetString(ErrorComment); ok {
		errorComment = s
	}
	return &CMoveResponse{
		AffectedSOPClassUID:                 sopClassUID,
		MessageIDBeingRespondedTo:           msgID,
		Status:                              status,
		ErrorComment:                        errorComment,
		Identifier:                          identifier,
		NumberOfRemainingSuboperationsOrNil: remaining,
		NumberOfCompletedSuboperationsOrNil: completed,
		NumberOfFailedSuboperationsOrNil:    failed,
		NumberOfWarningSuboperationsOrNil:   warning,
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
	return SendCMoveResponseWithContext(nil, assoc, pcID, rsp)
}

func SendCMoveResponseWithContext(ctx context.Context, assoc *ul.Association, pcID byte, rsp CMoveResponse) error {
	if rsp.MessageIDBeingRespondedTo == 0 {
		return fmt.Errorf("dicom dimse: C-MOVE response MessageIDBeingRespondedTo must be non-zero")
	}
	if rsp.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: C-MOVE response AffectedSOPClassUID is required")
	}
	if rsp.Identifier != nil && !cMoveResponseStatusAllowsIdentifier(rsp.Status) {
		return fmt.Errorf("dicom dimse: C-MOVE response identifier dataset not allowed for status 0x%04X", rsp.Status)
	}
	if err := SendCommandSetWithContext(ctx, assoc, pcID, rsp.CommandSet()); err != nil {
		return err
	}
	if rsp.Identifier == nil {
		return nil
	}
	syntax, err := acceptedTransferSyntax(assoc, pcID)
	if err != nil {
		return err
	}
	return SendDataSetWithContext(ctx, assoc, pcID, rsp.Identifier, syntax)
}

func ReceiveCMoveResponse(assoc *ul.Association, pcID byte) (*CMoveResponse, error) {
	return receiveCMoveResponseWithContext(nil, assoc, pcID)
}

func receiveCMoveResponseWithContext(ctx context.Context, assoc *ul.Association, pcID byte) (*CMoveResponse, error) {
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
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
	rsp.Identifier, err = receiveIdentifierDataSetWithContext(ctx, assoc, pcID, syntax)
	if err != nil {
		return nil, err
	}
	return rsp, nil
}

func acceptedTransferSyntax(assoc *ul.Association, pcID byte) (transfer.Syntax, error) {
	return AcceptedTransferSyntax(assoc, pcID)
}

func cMoveResponseStatusAllowsIdentifier(status uint16) bool {
	switch ClassifyCMoveStatus(status) {
	case CMoveStatusCancel, CMoveStatusWarning, CMoveStatusFailure:
		return true
	default:
		return false
	}
}
