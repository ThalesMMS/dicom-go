package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

// CFindPriority is the DIMSE Priority value used by C-FIND requests.
//
// See DICOM PS3.7.
//
// Note: Priority is optional in many implementations, but we include it for
// completeness.
const (
	PriorityLow    uint16 = 0x0002
	PriorityMedium uint16 = 0x0000
	PriorityHigh   uint16 = 0x0001
)

type CFindRequest struct {
	// AffectedSOPClassUID is the Query/Retrieve Information Model - FIND SOP
	// Class UID (e.g. Study Root Find).
	AffectedSOPClassUID string
	MessageID           uint16
	Priority            uint16
}

func (r CFindRequest) CommandSet() []core.Element {
	priority := r.Priority
	if priority == 0 {
		priority = PriorityMedium
	}
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CFindRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(Priority, priority),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}
}

type CFindResponse struct {
	AffectedSOPClassUID           string
	MessageIDBeingRespondedTo     uint16
	Status                        uint16
	CommandDataSetType            uint16
	ErrorComment                  string
	OffendingElementOrNil         *core.Tag // TODO: populate from ParseCFindResponse when command parsing supports it.
	FailedSOPInstanceUIDListOrNil []string  // TODO: populate from ParseCFindResponse when command parsing supports it.
}

func (r CFindResponse) CommandSet() []core.Element {
	elements := []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, CFindRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, r.CommandDataSetType),
		newUSCommandElement(Status, r.Status),
	}
	if r.ErrorComment != "" {
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: ErrorComment, VR: core.VRLO},
			Value:  core.StringValue{r.ErrorComment},
		})
	}
	return elements
}

func ParseCFindRequest(obj *object.Object) (*CFindRequest, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CFindRQ {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-FIND-RQ 0x%04X", field, CFindRQ)
	}
	messageID, err := CommandUint16(obj, MessageID)
	if err != nil {
		return nil, err
	}
	priority, err := CommandUint16(obj, Priority)
	if err != nil {
		return nil, err
	}
	dst, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dst != DataSetPresent {
		return nil, fmt.Errorf("dicom dimse: C-FIND request dataset type 0x%04X, want dataset present 0x%04X", dst, DataSetPresent)
	}
	affectedUID, ok := obj.GetString(AffectedSOPClassUID)
	if !ok {
		return nil, fmt.Errorf("dicom dimse: missing or invalid command element %s", AffectedSOPClassUID)
	}
	return &CFindRequest{AffectedSOPClassUID: affectedUID, MessageID: messageID, Priority: priority}, nil
}

func ParseCFindResponse(obj *object.Object) (*CFindResponse, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CFindRSP {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-FIND-RSP 0x%04X", field, CFindRSP)
	}
	messageID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	dst, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	status, err := CommandUint16(obj, Status)
	if err != nil {
		return nil, err
	}
	affectedUID, ok := obj.GetString(AffectedSOPClassUID)
	if !ok {
		return nil, fmt.Errorf("dicom dimse: missing or invalid command element %s", AffectedSOPClassUID)
	}

	resp := &CFindResponse{
		AffectedSOPClassUID:       affectedUID,
		MessageIDBeingRespondedTo: messageID,
		Status:                    status,
		CommandDataSetType:        dst,
	}
	if s, ok := obj.GetString(ErrorComment); ok {
		resp.ErrorComment = s
	}
	return resp, nil
}
