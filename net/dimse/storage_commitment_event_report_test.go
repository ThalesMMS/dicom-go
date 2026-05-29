package dimse

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
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

func TestNEventReportParsersAcceptZeroMessageID(t *testing.T) {
	request := NEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:              0,
		EventTypeID:            StorageCommitmentEventTypeID,
	}
	parsedRequest, err := ParseNEventReportRequest(object.FromElements(request.CommandSet(), nil))
	if err != nil || parsedRequest.MessageID != 0 {
		t.Fatalf("ParseNEventReportRequest() = %+v, %v", parsedRequest, err)
	}
	response := NEventReportResponse{
		AffectedSOPClassUID:       StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID:    StorageCommitmentPushModelSOPInstanceUID,
		MessageIDBeingRespondedTo: 0,
		Status:                    StatusSuccess,
	}
	parsedResponse, err := ParseNEventReportResponse(object.FromElements(response.CommandSet(), nil))
	if err != nil || parsedResponse.MessageIDBeingRespondedTo != 0 {
		t.Fatalf("ParseNEventReportResponse() = %+v, %v", parsedResponse, err)
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
	for _, eventTypeID := range []uint16{StorageCommitmentEventTypeSuccess, StorageCommitmentEventTypeFailures} {
		req := NEventReportRequest{
			AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
			AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
			MessageID:              1,
			EventTypeID:            eventTypeID,
		}
		obj := object.New(nil)
		for _, element := range req.CommandSet() {
			obj.Put(element)
		}
		if _, err := ParseNEventReportRequest(obj); err != nil {
			t.Fatalf("ParseNEventReportRequest(EventTypeID=%d) error = %v", eventTypeID, err)
		}
	}
	for _, eventTypeID := range []uint16{0, 3} {
		req := NEventReportRequest{
			AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
			AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
			MessageID:              1,
			EventTypeID:            eventTypeID,
		}
		obj := object.New(nil)
		for _, element := range req.CommandSet() {
			obj.Put(element)
		}
		if _, err := ParseNEventReportRequest(obj); err == nil {
			t.Fatalf("ParseNEventReportRequest(EventTypeID=%d) expected error", eventTypeID)
		}
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

func TestNEventReportResponse_AcceptsAnyNonNullDatasetType(t *testing.T) {
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
	obj.Put(newUSCommandElement(EventTypeID, StorageCommitmentEventTypeID))
	parsed, err := ParseNEventReportResponse(obj)
	if err != nil {
		t.Fatalf("ParseNEventReportResponse() error = %v", err)
	}
	if !parsed.HasEventReply {
		t.Fatal("HasEventReply = false, want true for non-null CommandDataSetType")
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

func TestStorageCommitmentEventInformationRoundTripReportsFailures(t *testing.T) {
	transactionUID := "1.2.3.4.5.6"
	info, err := BuildStorageCommitmentEventInformation(transactionUID,
		[]StorageCommitmentSOPReference{
			{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3"},
		},
		[]StorageCommitmentSOPReference{
			{SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", SOPInstanceUID: "1.2.4", FailureReason: StatusStorageCommitmentProcessingFailure},
		},
	)
	if err != nil {
		t.Fatalf("BuildStorageCommitmentEventInformation() error = %v", err)
	}

	parsed, err := ParseStorageCommitmentEventInformation(info)
	if err != nil {
		t.Fatalf("ParseStorageCommitmentEventInformation() error = %v", err)
	}
	if parsed.TransactionUID != transactionUID {
		t.Fatalf("TransactionUID = %q, want %q", parsed.TransactionUID, transactionUID)
	}
	if len(parsed.ReferencedSOPs) != 1 || parsed.ReferencedSOPs[0].SOPInstanceUID != "1.2.3" {
		t.Fatalf("ReferencedSOPs = %#v", parsed.ReferencedSOPs)
	}
	if len(parsed.FailedSOPs) != 1 || parsed.FailedSOPs[0].FailureReason != StatusStorageCommitmentProcessingFailure {
		t.Fatalf("FailedSOPs = %#v", parsed.FailedSOPs)
	}
}

func TestStorageCommitmentEventInformationRejectsInstanceInBothResults(t *testing.T) {
	committed := []StorageCommitmentSOPReference{
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3"},
	}
	failed := []StorageCommitmentSOPReference{
		{SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", SOPInstanceUID: "1.2.3", FailureReason: StatusStorageCommitmentProcessingFailure},
	}
	if _, err := BuildStorageCommitmentEventInformation("1.2.3.4.5.6", committed, failed); !errors.Is(err, ErrStorageCommitmentInvalidResult) {
		t.Fatalf("BuildStorageCommitmentEventInformation() error = %v, want %v", err, ErrStorageCommitmentInvalidResult)
	}

	dataset := object.FromElements([]core.Element{
		{
			Header: core.ElementHeader{Tag: StorageCommitmentTransactionUID, VR: core.VRUI},
			Value:  core.StringValue{"1.2.3.4.5.6"},
		},
		storageCommitmentSOPSequenceElement(StorageCommitmentReferencedSOPSequence, committed, false),
		storageCommitmentSOPSequenceElement(StorageCommitmentFailedSOPSequence, failed, true),
	}, std.Dictionary)
	if _, err := ParseStorageCommitmentEventInformation(dataset); !errors.Is(err, ErrStorageCommitmentInvalidResult) {
		t.Fatalf("ParseStorageCommitmentEventInformation() error = %v, want %v", err, ErrStorageCommitmentInvalidResult)
	}
}
