package dimse

import (
	"encoding/binary"
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
	OffendingElementOrNil         *core.Tag
	FailedSOPInstanceUIDListOrNil []string
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
	if r.OffendingElementOrNil != nil {
		elements = append(elements, newATCommandElement(OffendingElement, *r.OffendingElementOrNil))
	}
	if len(r.FailedSOPInstanceUIDListOrNil) > 0 {
		elements = append(elements, newUIListElement(FailedSOPInstanceUIDList, r.FailedSOPInstanceUIDListOrNil))
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
	if dst == NoDataSet {
		return nil, fmt.Errorf("dicom dimse: C-FIND request does not contain an Identifier dataset")
	}
	if priority > PriorityLow {
		return nil, fmt.Errorf("dicom dimse: C-FIND request priority 0x%04X is invalid", priority)
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
	affectedUID := ""
	if _, present := obj.Get(AffectedSOPClassUID); present {
		var ok bool
		affectedUID, ok = obj.GetString(AffectedSOPClassUID)
		if !ok {
			return nil, fmt.Errorf("dicom dimse: invalid command element %s", AffectedSOPClassUID)
		}
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
	offending, ok, err := optionalCommandTags(obj, OffendingElement)
	if err != nil {
		return nil, err
	}
	if ok && len(offending) > 0 {
		tag := offending[0]
		resp.OffendingElementOrNil = &tag
	}
	failed, ok, err := optionalCommandUIDs(obj, FailedSOPInstanceUIDList)
	if err != nil {
		return nil, err
	}
	if ok {
		resp.FailedSOPInstanceUIDListOrNil = failed
	}
	return resp, nil
}

func optionalCommandTags(obj *object.Object, tag core.Tag) ([]core.Tag, bool, error) {
	elem, ok := obj.Get(tag)
	if !ok {
		return nil, false, nil
	}
	if elem.VR() != core.VRAT {
		return nil, true, fmt.Errorf("dicom dimse: command element %s has VR %s, want AT", tag, elem.VR())
	}
	raw, ok := elem.RawBytes()
	if !ok {
		return nil, true, fmt.Errorf("dicom dimse: command element %s is not raw AT data", tag)
	}
	if len(raw)%4 != 0 {
		return nil, true, fmt.Errorf("dicom dimse: command element %s has %d byte value, want a multiple of 4", tag, len(raw))
	}
	tags := make([]core.Tag, len(raw)/4)
	for i := range tags {
		offset := i * 4
		tags[i] = core.NewTag(
			binary.LittleEndian.Uint16(raw[offset:]),
			binary.LittleEndian.Uint16(raw[offset+2:]),
		)
	}
	return tags, true, nil
}

func optionalCommandUIDs(obj *object.Object, tag core.Tag) ([]string, bool, error) {
	elem, ok := obj.Get(tag)
	if !ok {
		return nil, false, nil
	}
	if elem.VR() != core.VRUI {
		return nil, true, fmt.Errorf("dicom dimse: command element %s has VR %s, want UI", tag, elem.VR())
	}
	values, ok := obj.GetUIDs(tag)
	if !ok {
		return nil, true, fmt.Errorf("dicom dimse: command element %s is not a valid UI list", tag)
	}
	return values, true, nil
}
