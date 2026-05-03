package dimse

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestNActionRequest_CommandSet_RoundTrip(t *testing.T) {
	req := NActionRequest{
		RequestedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:               7,
		ActionTypeID:            StorageCommitmentActionTypeID,
	}
	obj := newCommandObject(req.CommandSet())
	parsed, err := ParseNActionRequest(obj)
	if err != nil {
		t.Fatalf("ParseNActionRequest() error = %v", err)
	}
	if *parsed != req {
		t.Fatalf("parsed=%+v, want %+v", *parsed, req)
	}
}

func TestNActionResponse_CommandSet_RoundTrip_NoDataset(t *testing.T) {
	rsp := NActionResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
		HasActionReply:            false,
	}
	obj := newCommandObject(rsp.CommandSet())
	parsed, err := ParseNActionResponse(obj)
	if err != nil {
		t.Fatalf("ParseNActionResponse() error = %v", err)
	}
	if *parsed != rsp {
		t.Fatalf("parsed=%+v, want %+v", *parsed, rsp)
	}
}

func TestNActionResponse_CommandSet_RoundTrip_WithDatasetFlag(t *testing.T) {
	rsp := NActionResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
		HasActionReply:            true,
	}
	obj := newCommandObject(rsp.CommandSet())
	parsed, err := ParseNActionResponse(obj)
	if err != nil {
		t.Fatalf("ParseNActionResponse() error = %v", err)
	}
	if *parsed != rsp {
		t.Fatalf("parsed=%+v, want %+v", *parsed, rsp)
	}
}

func TestNActionRequest_RequiresDatasetPresent(t *testing.T) {
	elems := []core.Element{
		newUIElement(RequestedSOPClassUID, StorageCommitmentPushModelSOPClassUID),
		newUSCommandElement(CommandField, NActionRQ),
		newUSCommandElement(MessageID, 1),
		newUIElement(RequestedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID),
		newUSCommandElement(ActionTypeID, StorageCommitmentActionTypeID),
		newUSCommandElement(CommandDataSetType, NoDataSet),
	}
	obj := newCommandObject(elems)
	assertParseNActionRequestError(t, obj, "N-ACTION request dataset type")
}

func TestNActionRequest_RejectsZeroMessageID(t *testing.T) {
	elems := []core.Element{
		newUIElement(RequestedSOPClassUID, StorageCommitmentPushModelSOPClassUID),
		newUSCommandElement(CommandField, NActionRQ),
		newUSCommandElement(MessageID, 0),
		newUIElement(RequestedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID),
		newUSCommandElement(ActionTypeID, StorageCommitmentActionTypeID),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}
	obj := newCommandObject(elems)
	assertParseNActionRequestError(t, obj, "MessageID must be non-zero")
}

func TestNActionRequest_RequiresStorageCommitmentActionTypeID(t *testing.T) {
	elems := []core.Element{
		newUIElement(RequestedSOPClassUID, StorageCommitmentPushModelSOPClassUID),
		newUSCommandElement(CommandField, NActionRQ),
		newUSCommandElement(MessageID, 1),
		newUIElement(RequestedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID),
		newUSCommandElement(ActionTypeID, StorageCommitmentActionTypeID+1),
		newUSCommandElement(CommandDataSetType, DataSetPresent),
	}
	obj := newCommandObject(elems)
	assertParseNActionRequestError(t, obj, "ActionTypeID")
}

func TestNActionRequest_RequiresStorageCommitmentUIDs(t *testing.T) {
	tests := []struct {
		name           string
		sopClassUID    string
		sopInstanceUID string
		wantErr        string
	}{
		{
			name:           "invalid class uid",
			sopClassUID:    "1.2.3",
			sopInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
			wantErr:        "RequestedSOPClassUID",
		},
		{
			name:           "invalid instance uid",
			sopClassUID:    StorageCommitmentPushModelSOPClassUID,
			sopInstanceUID: "1.2.3",
			wantErr:        "RequestedSOPInstanceUID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elems := []core.Element{
				newUIElement(RequestedSOPClassUID, tt.sopClassUID),
				newUSCommandElement(CommandField, NActionRQ),
				newUSCommandElement(MessageID, 1),
				newUIElement(RequestedSOPInstanceUID, tt.sopInstanceUID),
				newUSCommandElement(ActionTypeID, StorageCommitmentActionTypeID),
				newUSCommandElement(CommandDataSetType, DataSetPresent),
			}
			obj := newCommandObject(elems)
			assertParseNActionRequestError(t, obj, tt.wantErr)
		})
	}
}

func TestNActionResponse_RejectsInvalidDatasetType(t *testing.T) {
	rsp := NActionResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
	}
	elems := make([]core.Element, 0, len(rsp.CommandSet()))
	for _, e := range rsp.CommandSet() {
		if e.Header.Tag == CommandDataSetType {
			continue
		}
		elems = append(elems, e)
	}
	obj := newCommandObject(elems)
	obj.Put(newUSCommandElement(CommandDataSetType, 0x9999))
	assertParseNActionResponseError(t, obj, "dataset type")
}

func TestNActionResponse_RejectsZeroMessageIDBeingRespondedTo(t *testing.T) {
	elems := []core.Element{
		newUIElement(AffectedSOPClassUID, StorageCommitmentPushModelSOPClassUID),
		newUSCommandElement(CommandField, NActionRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, 0),
		newUIElement(AffectedSOPInstanceUID, StorageCommitmentPushModelSOPInstanceUID),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUSCommandElement(Status, StatusSuccess),
	}
	obj := newCommandObject(elems)
	assertParseNActionResponseError(t, obj, "MessageIDBeingRespondedTo must be non-zero")
}

func TestNActionResponse_UsesAffectedSOPUIDs(t *testing.T) {
	rsp := NActionResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
	}
	obj := newCommandObject(rsp.CommandSet())
	if obj.Has(RequestedSOPClassUID) || obj.Has(RequestedSOPInstanceUID) {
		t.Fatalf("NActionResponse command set used requested SOP tags")
	}
	if !obj.Has(AffectedSOPClassUID) || !obj.Has(AffectedSOPInstanceUID) {
		t.Fatalf("NActionResponse command set missing affected SOP tags")
	}
	if _, err := ParseNActionResponse(obj); err != nil {
		t.Fatalf("ParseNActionResponse() error = %v", err)
	}
}

func newCommandObject(elements []core.Element) *object.Object {
	obj := object.New(nil)
	for _, e := range elements {
		obj.Put(e)
	}
	return obj
}

func assertParseNActionRequestError(t *testing.T, obj *object.Object, want string) {
	t.Helper()
	_, err := ParseNActionRequest(obj)
	if err == nil {
		t.Fatalf("ParseNActionRequest() expected error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ParseNActionRequest() error = %v, want fragment %q", err, want)
	}
}

func assertParseNActionResponseError(t *testing.T, obj *object.Object, want string) {
	t.Helper()
	_, err := ParseNActionResponse(obj)
	if err == nil {
		t.Fatalf("ParseNActionResponse() expected error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ParseNActionResponse() error = %v, want fragment %q", err, want)
	}
}
