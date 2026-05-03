package dimse

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestNEventReportRequest_CommandSet_RoundTrip(t *testing.T) {
	req := NEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:              7,
		EventTypeID:            StorageCommitmentEventTypeID,
	}

	obj := object.New(nil)
	for _, e := range req.CommandSet() {
		obj.Put(e)
	}

	parsed, err := ParseNEventReportRequest(obj)
	if err != nil {
		t.Fatalf("ParseNEventReportRequest() error = %v", err)
	}
	if *parsed != req {
		t.Fatalf("parsed=%+v, want %+v", *parsed, req)
	}
}

func TestNEventReportResponse_CommandSet_RoundTrip(t *testing.T) {
	rsp := NEventReportResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
		HasEventReply:             false,
	}

	obj := object.New(nil)
	for _, e := range rsp.CommandSet() {
		obj.Put(e)
	}

	parsed, err := ParseNEventReportResponse(obj)
	if err != nil {
		t.Fatalf("ParseNEventReportResponse() error = %v", err)
	}
	if *parsed != rsp {
		t.Fatalf("parsed=%+v, want %+v", *parsed, rsp)
	}
}

func TestNEventReportResponse_CommandSet_RoundTrip_WithEventReply(t *testing.T) {
	rsp := NEventReportResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
		HasEventReply:             true,
	}

	obj := object.New(nil)
	for _, e := range rsp.CommandSet() {
		obj.Put(e)
	}
	obj.Put(core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1195), VR: core.VRUI},
		Value:  core.StringValue{"1.2.3.4.5"},
	})

	parsed, err := ParseNEventReportResponse(obj)
	if err != nil {
		t.Fatalf("ParseNEventReportResponse() error = %v", err)
	}
	if *parsed != rsp {
		t.Fatalf("parsed=%+v, want %+v", *parsed, rsp)
	}
}

func TestNEventReportRequest_Parse_RequiresDataSetPresent(t *testing.T) {
	req := NEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:              1,
		EventTypeID:            1,
	}
	cmd := req.CommandSet()
	// Replace CommandDataSetType value with NoDataSet.
	for i := range cmd {
		if cmd[i].Header.Tag == CommandDataSetType {
			cmd[i] = newUSCommandElement(CommandDataSetType, NoDataSet)
		}
	}
	obj := object.New(nil)
	for _, e := range cmd {
		obj.Put(e)
	}

	if _, err := ParseNEventReportRequest(obj); err == nil {
		t.Fatalf("ParseNEventReportRequest() expected error, got nil")
	}
}

func TestNEventReportRequest_RequiresStorageCommitmentEventTypeID(t *testing.T) {
	req := NEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:              1,
		EventTypeID:            StorageCommitmentEventTypeID,
	}
	cmd := req.CommandSet()
	for i := range cmd {
		if cmd[i].Header.Tag == EventTypeID {
			cmd[i] = newUSCommandElement(EventTypeID, StorageCommitmentEventTypeID+1)
		}
	}
	obj := object.New(nil)
	for _, e := range cmd {
		obj.Put(e)
	}

	if _, err := ParseNEventReportRequest(obj); err == nil {
		t.Fatalf("ParseNEventReportRequest() expected error, got nil")
	}
}

func TestNEventReportRequest_RequiresStorageCommitmentUIDs(t *testing.T) {
	req := NEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:              1,
		EventTypeID:            StorageCommitmentEventTypeID,
	}
	cmd := req.CommandSet()
	for i := range cmd {
		if cmd[i].Header.Tag == AffectedSOPClassUID {
			cmd[i] = newUIElement(AffectedSOPClassUID, "1.2.3")
		}
	}
	obj := object.New(nil)
	for _, e := range cmd {
		obj.Put(e)
	}

	if _, err := ParseNEventReportRequest(obj); err == nil {
		t.Fatalf("ParseNEventReportRequest() expected error, got nil")
	}
}

func TestNEventReportResponse_RejectsInvalidDatasetType(t *testing.T) {
	rsp := NEventReportResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
	}
	obj := object.New(nil)
	for _, e := range rsp.CommandSet() {
		obj.Put(e)
	}
	obj.Put(newUSCommandElement(CommandDataSetType, 0x9999))
	if _, err := ParseNEventReportResponse(obj); err == nil {
		t.Fatalf("ParseNEventReportResponse() expected error, got nil")
	}
}

func TestNEventReportResponse_RejectsNonStorageCommitmentUID(t *testing.T) {
	rsp := NEventReportResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
	}
	cmd := rsp.CommandSet()
	for i := range cmd {
		if cmd[i].Header.Tag == AffectedSOPClassUID {
			cmd[i] = newUIElement(AffectedSOPClassUID, "1.2.3")
		}
	}
	obj := object.New(nil)
	for _, e := range cmd {
		obj.Put(e)
	}

	if _, err := ParseNEventReportResponse(obj); err == nil {
		t.Fatalf("ParseNEventReportResponse() expected error, got nil")
	}
}
