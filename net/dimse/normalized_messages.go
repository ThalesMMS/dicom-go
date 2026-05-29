package dimse

import (
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

// NormalizedEventReportRequest is the generic N-EVENT-REPORT-RQ command. A
// CommandDataSetType other than NoDataSet means EventInformation follows.
type NormalizedEventReportRequest struct {
	AffectedSOPClassUID    string
	MessageID              uint16
	CommandDataSetType     uint16
	AffectedSOPInstanceUID string
	EventTypeID            uint16
}

func (r NormalizedEventReportRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, NEventReportRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, r.CommandDataSetType),
		newUIElement(AffectedSOPInstanceUID, r.AffectedSOPInstanceUID),
		newUSCommandElement(EventTypeID, r.EventTypeID),
	}
}

// NormalizedEventReportResponse is the generic N-EVENT-REPORT-RSP command.
// Affected UIDs and EventTypeID are conditional DIMSE-N response fields.
type NormalizedEventReportResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	CommandDataSetType        uint16
	Status                    uint16
	AffectedSOPInstanceUID    string
	EventTypeIDOrNil          *uint16
	StatusFields              *NormalizedStatusFields
}

func (r NormalizedEventReportResponse) CommandSet() []core.Element {
	elements := appendOptionalUID(nil, AffectedSOPClassUID, r.AffectedSOPClassUID)
	elements = append(elements,
		newUSCommandElement(CommandField, NEventReportRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, r.CommandDataSetType),
		newUSCommandElement(Status, r.Status),
	)
	elements = appendOptionalUID(elements, AffectedSOPInstanceUID, r.AffectedSOPInstanceUID)
	elements = appendOptionalUS(elements, EventTypeID, r.EventTypeIDOrNil)
	return appendNormalizedStatusFields(elements, r.StatusFields)
}

func ParseNormalizedEventReportRequest(command *object.Object) (*NormalizedEventReportRequest, error) {
	const service = "N-EVENT-REPORT-RQ"
	if err := validateNormalizedCommandField(command, service, NEventReportRQ); err != nil {
		return nil, err
	}
	classUID, err := commandUID(command, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	messageID, err := CommandUint16(command, MessageID)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(command, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	instanceUID, err := commandUID(command, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	eventTypeID, err := CommandUint16(command, EventTypeID)
	if err != nil {
		return nil, err
	}
	return &NormalizedEventReportRequest{
		AffectedSOPClassUID:    classUID,
		MessageID:              messageID,
		CommandDataSetType:     dataSetType,
		AffectedSOPInstanceUID: instanceUID,
		EventTypeID:            eventTypeID,
	}, nil
}

func ParseNormalizedEventReportResponse(command *object.Object) (*NormalizedEventReportResponse, error) {
	const service = "N-EVENT-REPORT-RSP"
	if err := validateNormalizedCommandField(command, service, NEventReportRSP); err != nil {
		return nil, err
	}
	messageID, dataSetType, status, err := parseNormalizedResponseBase(command, service)
	if err != nil {
		return nil, err
	}
	classUID, _, err := optionalCommandUID(command, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	instanceUID, _, err := optionalCommandUID(command, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	eventTypeID, err := optionalCommandUint16(command, EventTypeID)
	if err != nil {
		return nil, err
	}
	if status == StatusSuccess && normalizedHasDataSet(dataSetType) && eventTypeID == nil {
		return nil, fmt.Errorf("dicom dimse: %s success response dataset requires EventTypeID", service)
	}
	fields, err := parseNormalizedStatusFields(command)
	if err != nil {
		return nil, err
	}
	return &NormalizedEventReportResponse{
		AffectedSOPClassUID:       classUID,
		MessageIDBeingRespondedTo: messageID,
		CommandDataSetType:        dataSetType,
		Status:                    status,
		AffectedSOPInstanceUID:    instanceUID,
		EventTypeIDOrNil:          eventTypeID,
		StatusFields:              fields,
	}, nil
}

// NormalizedGetRequest is the generic N-GET-RQ command. The optional
// AttributeIdentifierList contains the attributes the SCU wants returned.
type NormalizedGetRequest struct {
	RequestedSOPClassUID    string
	MessageID               uint16
	RequestedSOPInstanceUID string
	AttributeIdentifierList []core.Tag
}

func (r NormalizedGetRequest) CommandSet() []core.Element {
	elements := []core.Element{
		newUIElement(RequestedSOPClassUID, r.RequestedSOPClassUID),
		newUSCommandElement(CommandField, NGetRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUIElement(RequestedSOPInstanceUID, r.RequestedSOPInstanceUID),
	}
	if len(r.AttributeIdentifierList) > 0 {
		elements = append(elements, newATCommandElement(AttributeIdentifierList, r.AttributeIdentifierList...))
	}
	return elements
}

type NormalizedGetResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	CommandDataSetType        uint16
	Status                    uint16
	AffectedSOPInstanceUID    string
	StatusFields              *NormalizedStatusFields
}

func (r NormalizedGetResponse) CommandSet() []core.Element {
	return normalizedResponseCommandSet(NGetRSP, r.AffectedSOPClassUID, r.MessageIDBeingRespondedTo, r.CommandDataSetType, r.Status, r.AffectedSOPInstanceUID, r.StatusFields)
}

func ParseNormalizedGetRequest(command *object.Object) (*NormalizedGetRequest, error) {
	const service = "N-GET-RQ"
	if err := validateNormalizedCommandField(command, service, NGetRQ); err != nil {
		return nil, err
	}
	classUID, messageID, dataSetType, instanceUID, err := parseNormalizedRequestedRequestBase(command, service)
	if err != nil {
		return nil, err
	}
	if dataSetType != NoDataSet {
		return nil, fmt.Errorf("dicom dimse: %s CommandDataSetType 0x%04X, want no dataset 0x%04X", service, dataSetType, NoDataSet)
	}
	attributes, _, err := optionalCommandTags(command, AttributeIdentifierList)
	if err != nil {
		return nil, err
	}
	return &NormalizedGetRequest{
		RequestedSOPClassUID:    classUID,
		MessageID:               messageID,
		RequestedSOPInstanceUID: instanceUID,
		AttributeIdentifierList: append([]core.Tag(nil), attributes...),
	}, nil
}

func ParseNormalizedGetResponse(command *object.Object) (*NormalizedGetResponse, error) {
	base, err := parseNormalizedAffectedResponse(command, "N-GET-RSP", NGetRSP)
	if err != nil {
		return nil, err
	}
	return &NormalizedGetResponse{
		AffectedSOPClassUID:       base.classUID,
		MessageIDBeingRespondedTo: base.messageID,
		CommandDataSetType:        base.dataSetType,
		Status:                    base.status,
		AffectedSOPInstanceUID:    base.instanceUID,
		StatusFields:              base.statusFields,
	}, nil
}

// NormalizedSetRequest is the generic N-SET-RQ command. Its Modification List
// dataset is mandatory and follows the command set.
type NormalizedSetRequest struct {
	RequestedSOPClassUID    string
	MessageID               uint16
	RequestedSOPInstanceUID string
}

func (r NormalizedSetRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(RequestedSOPClassUID, r.RequestedSOPClassUID),
		newUSCommandElement(CommandField, NSetRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
		newUIElement(RequestedSOPInstanceUID, r.RequestedSOPInstanceUID),
	}
}

type NormalizedSetResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	CommandDataSetType        uint16
	Status                    uint16
	AffectedSOPInstanceUID    string
	StatusFields              *NormalizedStatusFields
}

func (r NormalizedSetResponse) CommandSet() []core.Element {
	return normalizedResponseCommandSet(NSetRSP, r.AffectedSOPClassUID, r.MessageIDBeingRespondedTo, r.CommandDataSetType, r.Status, r.AffectedSOPInstanceUID, r.StatusFields)
}

func ParseNormalizedSetRequest(command *object.Object) (*NormalizedSetRequest, error) {
	const service = "N-SET-RQ"
	if err := validateNormalizedCommandField(command, service, NSetRQ); err != nil {
		return nil, err
	}
	classUID, messageID, dataSetType, instanceUID, err := parseNormalizedRequestedRequestBase(command, service)
	if err != nil {
		return nil, err
	}
	if !normalizedHasDataSet(dataSetType) {
		return nil, fmt.Errorf("dicom dimse: %s requires a Modification List dataset", service)
	}
	return &NormalizedSetRequest{RequestedSOPClassUID: classUID, MessageID: messageID, RequestedSOPInstanceUID: instanceUID}, nil
}

func ParseNormalizedSetResponse(command *object.Object) (*NormalizedSetResponse, error) {
	base, err := parseNormalizedAffectedResponse(command, "N-SET-RSP", NSetRSP)
	if err != nil {
		return nil, err
	}
	return &NormalizedSetResponse{
		AffectedSOPClassUID:       base.classUID,
		MessageIDBeingRespondedTo: base.messageID,
		CommandDataSetType:        base.dataSetType,
		Status:                    base.status,
		AffectedSOPInstanceUID:    base.instanceUID,
		StatusFields:              base.statusFields,
	}, nil
}

// NormalizedActionRequest is the generic N-ACTION-RQ command. It deliberately
// does not embed service-specific Action Type or dataset semantics.
type NormalizedActionRequest struct {
	RequestedSOPClassUID    string
	MessageID               uint16
	CommandDataSetType      uint16
	RequestedSOPInstanceUID string
	ActionTypeID            uint16
}

func (r NormalizedActionRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(RequestedSOPClassUID, r.RequestedSOPClassUID),
		newUSCommandElement(CommandField, NActionRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, r.CommandDataSetType),
		newUIElement(RequestedSOPInstanceUID, r.RequestedSOPInstanceUID),
		newUSCommandElement(ActionTypeID, r.ActionTypeID),
	}
}

type NormalizedActionResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	CommandDataSetType        uint16
	Status                    uint16
	AffectedSOPInstanceUID    string
	ActionTypeIDOrNil         *uint16
	StatusFields              *NormalizedStatusFields
}

func (r NormalizedActionResponse) CommandSet() []core.Element {
	elements := normalizedResponseCommandSet(NActionRSP, r.AffectedSOPClassUID, r.MessageIDBeingRespondedTo, r.CommandDataSetType, r.Status, r.AffectedSOPInstanceUID, nil)
	elements = appendOptionalUS(elements, ActionTypeID, r.ActionTypeIDOrNil)
	return appendNormalizedStatusFields(elements, r.StatusFields)
}

func ParseNormalizedActionRequest(command *object.Object) (*NormalizedActionRequest, error) {
	const service = "N-ACTION-RQ"
	if err := validateNormalizedCommandField(command, service, NActionRQ); err != nil {
		return nil, err
	}
	classUID, messageID, dataSetType, instanceUID, err := parseNormalizedRequestedRequestBase(command, service)
	if err != nil {
		return nil, err
	}
	actionTypeID, err := CommandUint16(command, ActionTypeID)
	if err != nil {
		return nil, err
	}
	return &NormalizedActionRequest{
		RequestedSOPClassUID:    classUID,
		MessageID:               messageID,
		CommandDataSetType:      dataSetType,
		RequestedSOPInstanceUID: instanceUID,
		ActionTypeID:            actionTypeID,
	}, nil
}

func ParseNormalizedActionResponse(command *object.Object) (*NormalizedActionResponse, error) {
	base, err := parseNormalizedAffectedResponse(command, "N-ACTION-RSP", NActionRSP)
	if err != nil {
		return nil, err
	}
	actionTypeID, err := optionalCommandUint16(command, ActionTypeID)
	if err != nil {
		return nil, err
	}
	if base.status == StatusSuccess && normalizedHasDataSet(base.dataSetType) && actionTypeID == nil {
		return nil, fmt.Errorf("dicom dimse: N-ACTION-RSP success response dataset requires ActionTypeID")
	}
	return &NormalizedActionResponse{
		AffectedSOPClassUID:       base.classUID,
		MessageIDBeingRespondedTo: base.messageID,
		CommandDataSetType:        base.dataSetType,
		Status:                    base.status,
		AffectedSOPInstanceUID:    base.instanceUID,
		ActionTypeIDOrNil:         actionTypeID,
		StatusFields:              base.statusFields,
	}, nil
}

// NormalizedCreateRequest is the generic N-CREATE-RQ command. An empty
// AffectedSOPInstanceUID lets a service whose contract permits it assign the UID.
type NormalizedCreateRequest struct {
	AffectedSOPClassUID    string
	MessageID              uint16
	CommandDataSetType     uint16
	AffectedSOPInstanceUID string
}

func (r NormalizedCreateRequest) CommandSet() []core.Element {
	elements := []core.Element{
		newUIElement(AffectedSOPClassUID, r.AffectedSOPClassUID),
		newUSCommandElement(CommandField, NCreateRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, r.CommandDataSetType),
	}
	return appendOptionalUID(elements, AffectedSOPInstanceUID, r.AffectedSOPInstanceUID)
}

type NormalizedCreateResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	CommandDataSetType        uint16
	Status                    uint16
	AffectedSOPInstanceUID    string
	StatusFields              *NormalizedStatusFields
}

func (r NormalizedCreateResponse) CommandSet() []core.Element {
	return normalizedResponseCommandSet(NCreateRSP, r.AffectedSOPClassUID, r.MessageIDBeingRespondedTo, r.CommandDataSetType, r.Status, r.AffectedSOPInstanceUID, r.StatusFields)
}

func ParseNormalizedCreateRequest(command *object.Object) (*NormalizedCreateRequest, error) {
	const service = "N-CREATE-RQ"
	if err := validateNormalizedCommandField(command, service, NCreateRQ); err != nil {
		return nil, err
	}
	classUID, err := commandUID(command, AffectedSOPClassUID)
	if err != nil {
		return nil, err
	}
	messageID, err := CommandUint16(command, MessageID)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(command, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	instanceUID, _, err := optionalCommandUID(command, AffectedSOPInstanceUID)
	if err != nil {
		return nil, err
	}
	return &NormalizedCreateRequest{AffectedSOPClassUID: classUID, MessageID: messageID, CommandDataSetType: dataSetType, AffectedSOPInstanceUID: instanceUID}, nil
}

func ParseNormalizedCreateResponse(command *object.Object) (*NormalizedCreateResponse, error) {
	base, err := parseNormalizedAffectedResponse(command, "N-CREATE-RSP", NCreateRSP)
	if err != nil {
		return nil, err
	}
	return &NormalizedCreateResponse{
		AffectedSOPClassUID:       base.classUID,
		MessageIDBeingRespondedTo: base.messageID,
		CommandDataSetType:        base.dataSetType,
		Status:                    base.status,
		AffectedSOPInstanceUID:    base.instanceUID,
		StatusFields:              base.statusFields,
	}, nil
}

// NormalizedDeleteRequest is the generic N-DELETE-RQ command.
type NormalizedDeleteRequest struct {
	RequestedSOPClassUID    string
	MessageID               uint16
	RequestedSOPInstanceUID string
}

func (r NormalizedDeleteRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(RequestedSOPClassUID, r.RequestedSOPClassUID),
		newUSCommandElement(CommandField, NDeleteRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUIElement(RequestedSOPInstanceUID, r.RequestedSOPInstanceUID),
	}
}

type NormalizedDeleteResponse struct {
	AffectedSOPClassUID       string
	MessageIDBeingRespondedTo uint16
	Status                    uint16
	AffectedSOPInstanceUID    string
	StatusFields              *NormalizedStatusFields
}

func (r NormalizedDeleteResponse) CommandSet() []core.Element {
	return normalizedResponseCommandSet(NDeleteRSP, r.AffectedSOPClassUID, r.MessageIDBeingRespondedTo, NoDataSet, r.Status, r.AffectedSOPInstanceUID, r.StatusFields)
}

func ParseNormalizedDeleteRequest(command *object.Object) (*NormalizedDeleteRequest, error) {
	const service = "N-DELETE-RQ"
	if err := validateNormalizedCommandField(command, service, NDeleteRQ); err != nil {
		return nil, err
	}
	classUID, messageID, dataSetType, instanceUID, err := parseNormalizedRequestedRequestBase(command, service)
	if err != nil {
		return nil, err
	}
	if dataSetType != NoDataSet {
		return nil, fmt.Errorf("dicom dimse: %s CommandDataSetType 0x%04X, want no dataset 0x%04X", service, dataSetType, NoDataSet)
	}
	return &NormalizedDeleteRequest{RequestedSOPClassUID: classUID, MessageID: messageID, RequestedSOPInstanceUID: instanceUID}, nil
}

func ParseNormalizedDeleteResponse(command *object.Object) (*NormalizedDeleteResponse, error) {
	base, err := parseNormalizedAffectedResponse(command, "N-DELETE-RSP", NDeleteRSP)
	if err != nil {
		return nil, err
	}
	if base.dataSetType != NoDataSet {
		return nil, fmt.Errorf("dicom dimse: N-DELETE-RSP CommandDataSetType 0x%04X, want no dataset 0x%04X", base.dataSetType, NoDataSet)
	}
	return &NormalizedDeleteResponse{
		AffectedSOPClassUID:       base.classUID,
		MessageIDBeingRespondedTo: base.messageID,
		Status:                    base.status,
		AffectedSOPInstanceUID:    base.instanceUID,
		StatusFields:              base.statusFields,
	}, nil
}

type normalizedAffectedResponse struct {
	classUID     string
	messageID    uint16
	dataSetType  uint16
	status       uint16
	instanceUID  string
	statusFields *NormalizedStatusFields
}

func parseNormalizedAffectedResponse(command *object.Object, service string, commandField uint16) (normalizedAffectedResponse, error) {
	if err := validateNormalizedCommandField(command, service, commandField); err != nil {
		return normalizedAffectedResponse{}, err
	}
	messageID, dataSetType, status, err := parseNormalizedResponseBase(command, service)
	if err != nil {
		return normalizedAffectedResponse{}, err
	}
	classUID, _, err := optionalCommandUID(command, AffectedSOPClassUID)
	if err != nil {
		return normalizedAffectedResponse{}, err
	}
	instanceUID, _, err := optionalCommandUID(command, AffectedSOPInstanceUID)
	if err != nil {
		return normalizedAffectedResponse{}, err
	}
	fields, err := parseNormalizedStatusFields(command)
	if err != nil {
		return normalizedAffectedResponse{}, err
	}
	return normalizedAffectedResponse{classUID: classUID, messageID: messageID, dataSetType: dataSetType, status: status, instanceUID: instanceUID, statusFields: fields}, nil
}

func parseNormalizedResponseBase(command *object.Object, _ string) (messageID, dataSetType, status uint16, err error) {
	messageID, err = CommandUint16(command, MessageIDBeingRespondedTo)
	if err != nil {
		return 0, 0, 0, err
	}
	dataSetType, err = CommandUint16(command, CommandDataSetType)
	if err != nil {
		return 0, 0, 0, err
	}
	status, err = CommandUint16(command, Status)
	if err != nil {
		return 0, 0, 0, err
	}
	return messageID, dataSetType, status, nil
}

func parseNormalizedRequestedRequestBase(command *object.Object, _ string) (classUID string, messageID, dataSetType uint16, instanceUID string, err error) {
	classUID, err = commandUID(command, RequestedSOPClassUID)
	if err != nil {
		return "", 0, 0, "", err
	}
	messageID, err = CommandUint16(command, MessageID)
	if err != nil {
		return "", 0, 0, "", err
	}
	dataSetType, err = CommandUint16(command, CommandDataSetType)
	if err != nil {
		return "", 0, 0, "", err
	}
	instanceUID, err = commandUID(command, RequestedSOPInstanceUID)
	if err != nil {
		return "", 0, 0, "", err
	}
	return classUID, messageID, dataSetType, instanceUID, nil
}

func normalizedResponseCommandSet(commandField uint16, classUID string, messageID, dataSetType, status uint16, instanceUID string, fields *NormalizedStatusFields) []core.Element {
	elements := appendOptionalUID(nil, AffectedSOPClassUID, classUID)
	elements = append(elements,
		newUSCommandElement(CommandField, commandField),
		newUSCommandElement(MessageIDBeingRespondedTo, messageID),
		newUSCommandElement(CommandDataSetType, dataSetType),
		newUSCommandElement(Status, status),
	)
	elements = appendOptionalUID(elements, AffectedSOPInstanceUID, instanceUID)
	return appendNormalizedStatusFields(elements, fields)
}
