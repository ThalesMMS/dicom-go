package dimse

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestNormalizedCommandSetsByteSemanticRoundTrip(t *testing.T) {
	classUID := "1.2.840.10008.5.1.4.34.6.1"
	instanceUID := "2.25.123456789"
	eventType := uint16(4)
	actionType := uint16(7)
	errorID := uint16(0x0201)
	statusFields := &NormalizedStatusFields{
		ErrorComment:            "status detail",
		ErrorIDOrNil:            &errorID,
		OffendingElements:       []core.Tag{core.NewTag(0x0010, 0x0020)},
		AttributeIdentifierList: []core.Tag{core.NewTag(0x0040, 0x1001)},
	}
	attributes := []core.Tag{core.NewTag(0x0010, 0x0010), core.NewTag(0x0010, 0x0020)}

	tests := []struct {
		name     string
		elements []core.Element
		parse    func(*object.Object) (any, error)
		want     any
	}{
		{
			name:  "N-EVENT-REPORT-RQ",
			want:  &NormalizedEventReportRequest{AffectedSOPClassUID: classUID, MessageID: 1, CommandDataSetType: 0x0102, AffectedSOPInstanceUID: instanceUID, EventTypeID: eventType},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedEventReportRequest(obj) },
		},
		{
			name:  "N-EVENT-REPORT-RSP",
			want:  &NormalizedEventReportResponse{AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: 1, CommandDataSetType: 0x0102, Status: 0x0115, AffectedSOPInstanceUID: instanceUID, EventTypeIDOrNil: &eventType, StatusFields: statusFields},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedEventReportResponse(obj) },
		},
		{
			name:  "N-GET-RQ",
			want:  &NormalizedGetRequest{RequestedSOPClassUID: classUID, MessageID: 2, RequestedSOPInstanceUID: instanceUID, AttributeIdentifierList: attributes},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedGetRequest(obj) },
		},
		{
			name:  "N-GET-RSP",
			want:  &NormalizedGetResponse{AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: 2, CommandDataSetType: DataSetPresent, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedGetResponse(obj) },
		},
		{
			name:  "N-SET-RQ",
			want:  &NormalizedSetRequest{RequestedSOPClassUID: classUID, MessageID: 3, RequestedSOPInstanceUID: instanceUID},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedSetRequest(obj) },
		},
		{
			name:  "N-SET-RSP",
			want:  &NormalizedSetResponse{AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: 3, CommandDataSetType: 0x0102, Status: 0x0107, AffectedSOPInstanceUID: instanceUID, StatusFields: statusFields},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedSetResponse(obj) },
		},
		{
			name:  "N-ACTION-RQ",
			want:  &NormalizedActionRequest{RequestedSOPClassUID: classUID, MessageID: 4, CommandDataSetType: NoDataSet, RequestedSOPInstanceUID: instanceUID, ActionTypeID: actionType},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedActionRequest(obj) },
		},
		{
			name:  "N-ACTION-RSP",
			want:  &NormalizedActionResponse{AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: 4, CommandDataSetType: DataSetPresent, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID, ActionTypeIDOrNil: &actionType},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedActionResponse(obj) },
		},
		{
			name:  "N-CREATE-RQ",
			want:  &NormalizedCreateRequest{AffectedSOPClassUID: classUID, MessageID: 5, CommandDataSetType: DataSetPresent},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedCreateRequest(obj) },
		},
		{
			name:  "N-CREATE-RSP",
			want:  &NormalizedCreateResponse{AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: 5, CommandDataSetType: DataSetPresent, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedCreateResponse(obj) },
		},
		{
			name:  "N-DELETE-RQ",
			want:  &NormalizedDeleteRequest{RequestedSOPClassUID: classUID, MessageID: 6, RequestedSOPInstanceUID: instanceUID},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedDeleteRequest(obj) },
		},
		{
			name:  "N-DELETE-RSP",
			want:  &NormalizedDeleteResponse{AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: 6, Status: 0x0112, StatusFields: statusFields},
			parse: func(obj *object.Object) (any, error) { return ParseNormalizedDeleteResponse(obj) },
		},
	}

	for i := range tests {
		test := &tests[i]
		switch want := test.want.(type) {
		case *NormalizedEventReportRequest:
			test.elements = want.CommandSet()
		case *NormalizedEventReportResponse:
			test.elements = want.CommandSet()
		case *NormalizedGetRequest:
			test.elements = want.CommandSet()
		case *NormalizedGetResponse:
			test.elements = want.CommandSet()
		case *NormalizedSetRequest:
			test.elements = want.CommandSet()
		case *NormalizedSetResponse:
			test.elements = want.CommandSet()
		case *NormalizedActionRequest:
			test.elements = want.CommandSet()
		case *NormalizedActionResponse:
			test.elements = want.CommandSet()
		case *NormalizedCreateRequest:
			test.elements = want.CommandSet()
		case *NormalizedCreateResponse:
			test.elements = want.CommandSet()
		case *NormalizedDeleteRequest:
			test.elements = want.CommandSet()
		case *NormalizedDeleteResponse:
			test.elements = want.CommandSet()
		default:
			t.Fatalf("unsupported fixture type %T", test.want)
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodeCommandSet(test.elements)
			if err != nil {
				t.Fatalf("EncodeCommandSet() error = %v", err)
			}
			decoded, err := DecodeCommandSet(encoded)
			if err != nil {
				t.Fatalf("DecodeCommandSet() error = %v", err)
			}
			got, err := test.parse(decoded)
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("round trip = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNormalizedParsersEnforceDatasetRules(t *testing.T) {
	classUID := "1.2.3"
	instanceUID := "1.2.3.4"

	get := NormalizedGetRequest{RequestedSOPClassUID: classUID, MessageID: 1, RequestedSOPInstanceUID: instanceUID}
	getObj := newCommandObject(get.CommandSet())
	getObj.Put(newUSCommandElement(CommandDataSetType, 0x0102))
	if _, err := ParseNormalizedGetRequest(getObj); err == nil || !strings.Contains(err.Error(), "want no dataset") {
		t.Fatalf("ParseNormalizedGetRequest() error = %v, want no-dataset error", err)
	}

	set := NormalizedSetRequest{RequestedSOPClassUID: classUID, MessageID: 2, RequestedSOPInstanceUID: instanceUID}
	setObj := newCommandObject(set.CommandSet())
	setObj.Put(newUSCommandElement(CommandDataSetType, NoDataSet))
	if _, err := ParseNormalizedSetRequest(setObj); err == nil || !strings.Contains(err.Error(), "requires a Modification List") {
		t.Fatalf("ParseNormalizedSetRequest() error = %v, want required-dataset error", err)
	}

	deleteResponse := NormalizedDeleteResponse{MessageIDBeingRespondedTo: 3, Status: StatusSuccess}
	deleteObj := newCommandObject(deleteResponse.CommandSet())
	deleteObj.Put(newUSCommandElement(CommandDataSetType, DataSetPresent))
	if _, err := ParseNormalizedDeleteResponse(deleteObj); err == nil || !strings.Contains(err.Error(), "want no dataset") {
		t.Fatalf("ParseNormalizedDeleteResponse() error = %v, want no-dataset error", err)
	}
}

func TestNormalizedParserRejectsMissingRequiredFields(t *testing.T) {
	command := newCommandObject([]core.Element{
		newUIElement(RequestedSOPClassUID, "1.2.3"),
		newUSCommandElement(CommandField, NActionRQ),
		newUSCommandElement(MessageID, 1),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUIElement(RequestedSOPInstanceUID, "1.2.3.4"),
	})
	if _, err := ParseNormalizedActionRequest(command); err == nil || !strings.Contains(err.Error(), ActionTypeID.String()) {
		t.Fatalf("ParseNormalizedActionRequest() error = %v, want missing ActionTypeID", err)
	}
}

func TestNormalizedParserRejectsMalformedUSValueLength(t *testing.T) {
	request := NormalizedDeleteRequest{
		RequestedSOPClassUID:    "1.2.3",
		MessageID:               1,
		RequestedSOPInstanceUID: "1.2.3.4",
	}
	command := newCommandObject(request.CommandSet())
	command.Put(core.NewRawElement(MessageID, core.VRUS, []byte{1, 0, 2, 0}))
	if _, err := ParseNormalizedDeleteRequest(command); err == nil || !strings.Contains(err.Error(), "want exactly 2") {
		t.Fatalf("ParseNormalizedDeleteRequest() error = %v, want malformed US length", err)
	}
}

func TestNormalizedParserRequiresTypeIDForSuccessfulReplyDataSet(t *testing.T) {
	response := NormalizedActionResponse{
		MessageIDBeingRespondedTo: 1,
		CommandDataSetType:        DataSetPresent,
		Status:                    StatusSuccess,
	}
	if _, err := ParseNormalizedActionResponse(newCommandObject(response.CommandSet())); err == nil || !strings.Contains(err.Error(), "requires ActionTypeID") {
		t.Fatalf("ParseNormalizedActionResponse() error = %v, want conditional ActionTypeID", err)
	}
	response.Status = 0x0115
	if _, err := ParseNormalizedActionResponse(newCommandObject(response.CommandSet())); err != nil {
		t.Fatalf("ParseNormalizedActionResponse(failure detail dataset) error = %v", err)
	}
}

func TestNormalizedParsersAcceptZeroMessageIDs(t *testing.T) {
	request := NormalizedDeleteRequest{
		RequestedSOPClassUID:    "1.2.3",
		MessageID:               0,
		RequestedSOPInstanceUID: "1.2.3.4",
	}
	parsedRequest, err := ParseNormalizedDeleteRequest(newCommandObject(request.CommandSet()))
	if err != nil || parsedRequest.MessageID != 0 {
		t.Fatalf("ParseNormalizedDeleteRequest(MessageID=0) = %#v, %v", parsedRequest, err)
	}
	response := NormalizedDeleteResponse{MessageIDBeingRespondedTo: 0, Status: StatusSuccess}
	parsedResponse, err := ParseNormalizedDeleteResponse(newCommandObject(response.CommandSet()))
	if err != nil || parsedResponse.MessageIDBeingRespondedTo != 0 {
		t.Fatalf("ParseNormalizedDeleteResponse(MessageID=0) = %#v, %v", parsedResponse, err)
	}
}

func TestNormalizedStatusErrorPreservesDetails(t *testing.T) {
	errorID := uint16(7)
	fields := &NormalizedStatusFields{ErrorComment: "attribute rejected", ErrorIDOrNil: &errorID, AttributeIdentifierList: []core.Tag{core.NewTag(0x0010, 0x0020)}}
	err := CheckNormalizedStatus("N-SET", 0xB000, fields)
	var statusErr *NormalizedStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("CheckNormalizedStatus() error = %v, want NormalizedStatusError", err)
	}
	if statusErr.Class != NormalizedStatusWarning || statusErr.Fields == fields || !reflect.DeepEqual(statusErr.Fields, fields) {
		t.Fatalf("NormalizedStatusError = %#v", statusErr)
	}
	fields.ErrorComment = "mutated"
	if statusErr.Fields.ErrorComment != "attribute rejected" {
		t.Fatalf("status detail aliasing = %q", statusErr.Fields.ErrorComment)
	}
}

func TestClassifyNormalizedStatusUsesPS37WarningClasses(t *testing.T) {
	tests := []struct {
		status uint16
		want   NormalizedStatusClass
	}{
		{status: 0x0000, want: NormalizedStatusSuccess},
		{status: 0x0001, want: NormalizedStatusWarning},
		{status: 0x0107, want: NormalizedStatusWarning},
		{status: 0x0116, want: NormalizedStatusWarning},
		{status: 0xB603, want: NormalizedStatusWarning},
		{status: 0x0106, want: NormalizedStatusFailure},
		{status: 0xC309, want: NormalizedStatusFailure},
	}
	for _, test := range tests {
		if got := ClassifyNormalizedStatus(test.status); got != test.want {
			t.Errorf("ClassifyNormalizedStatus(0x%04X) = %s, want %s", test.status, got, test.want)
		}
	}
}
