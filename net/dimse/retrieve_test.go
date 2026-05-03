package dimse

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCMoveRequest_CommandSet_RoundTrip(t *testing.T) {
	req := CMoveRequest{
		AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.2.2.2", // Study Root Query/Retrieve Information Model - MOVE
		MessageID:           7,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}
	encoded, err := EncodeCommandSet(req.CommandSet())
	if err != nil {
		t.Fatalf("EncodeCommandSet: %v", err)
	}
	obj, err := DecodeCommandSet(encoded)
	if err != nil {
		t.Fatalf("DecodeCommandSet: %v", err)
	}
	parsed, err := ParseCMoveRequest(obj)
	if err != nil {
		t.Fatalf("ParseCMoveRequest: %v", err)
	}
	if *parsed != req {
		t.Fatalf("parsed=%+v want %+v", *parsed, req)
	}
}

func TestCMoveResponse_CommandSet_RoundTrip(t *testing.T) {
	rsp := CMoveResponse{
		AffectedSOPClassUID:       "1.2.840.10008.5.1.4.1.2.2.2",
		MessageIDBeingRespondedTo: 7,
		Status:                    StatusSuccess,
	}
	encoded, err := EncodeCommandSet(rsp.CommandSet())
	if err != nil {
		t.Fatalf("EncodeCommandSet: %v", err)
	}
	obj, err := DecodeCommandSet(encoded)
	if err != nil {
		t.Fatalf("DecodeCommandSet: %v", err)
	}
	parsed, err := ParseCMoveResponse(obj)
	if err != nil {
		t.Fatalf("ParseCMoveResponse: %v", err)
	}
	if *parsed != rsp {
		t.Fatalf("parsed=%+v want %+v", *parsed, rsp)
	}
}

func TestCMoveResponse_AllowsIdentifierForWarning(t *testing.T) {
	rsp := CMoveResponse{
		AffectedSOPClassUID:       "1.2.840.10008.5.1.4.1.2.2.2",
		MessageIDBeingRespondedTo: 7,
		Status:                    0xB000,
		Identifier:                object.New(nil),
	}
	encoded, err := EncodeCommandSet(rsp.CommandSet())
	if err != nil {
		t.Fatalf("EncodeCommandSet: %v", err)
	}
	obj, err := DecodeCommandSet(encoded)
	if err != nil {
		t.Fatalf("DecodeCommandSet: %v", err)
	}
	parsed, err := ParseCMoveResponse(obj)
	if err != nil {
		t.Fatalf("ParseCMoveResponse: %v", err)
	}
	if parsed.Status != rsp.Status || parsed.Identifier == nil {
		t.Fatalf("parsed=%+v, want status 0x%04X with identifier", *parsed, rsp.Status)
	}
}

func TestCMoveRequest_RequiresDataSetPresent(t *testing.T) {
	// Build a command set but override the dataset type to NoDataSet.
	req := CMoveRequest{
		AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.2.2.2",
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}
	cmd := req.CommandSet()
	for i := range cmd {
		if cmd[i].Tag() == CommandDataSetType {
			cmd[i] = newUSCommandElement(CommandDataSetType, NoDataSet)
		}
	}
	encoded, err := EncodeCommandSet(cmd)
	if err != nil {
		t.Fatalf("EncodeCommandSet: %v", err)
	}
	obj, err := DecodeCommandSet(encoded)
	if err != nil {
		t.Fatalf("DecodeCommandSet: %v", err)
	}
	if _, err := ParseCMoveRequest(obj); err == nil {
		t.Fatalf("expected error")
	}
}

func TestRetrieveCommandSet_ImplicitVRLittleEndian(t *testing.T) {
	// Sanity check that AE VR and US/UI encode/decode via object round trip.
	req := CMoveRequest{
		AffectedSOPClassUID: "1.2.3",
		MessageID:           2,
		Priority:            1,
		MoveDestination:     "AET",
	}
	data, err := EncodeCommandSet(req.CommandSet())
	if err != nil {
		t.Fatalf("EncodeCommandSet: %v", err)
	}
	obj, err := object.ReadDataSet(bytes.NewReader(data), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReadDataSet: %v", err)
	}
	if _, err := ParseCMoveRequest(obj); err != nil {
		t.Fatalf("ParseCMoveRequest: %v", err)
	}
}

func TestDialRetrieveSCU_ValidatesRequiredOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := DialRetrieveSCU(ctx, RetrieveDialOptions{}); err == nil {
		t.Fatalf("DialRetrieveSCU() expected missing address error")
	}
	if _, err := DialRetrieveSCU(ctx, RetrieveDialOptions{Address: " \t\n "}); err == nil {
		t.Fatalf("DialRetrieveSCU() expected whitespace address error")
	}
	if _, err := DialRetrieveSCU(ctx, RetrieveDialOptions{Address: "127.0.0.1:1"}); err == nil {
		t.Fatalf("DialRetrieveSCU() expected missing presentation contexts error")
	}

	_, err := DialRetrieveSCU(ctx, RetrieveDialOptions{
		Address: "127.0.0.1:1",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  "1.2.840.10008.5.1.4.1.2.2.2",
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err == nil {
		t.Fatalf("DialRetrieveSCU() expected connection error after validation")
	}
}

func TestSendCMoveWithProgress_ReturnsCanceledContextBeforeSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	identifier := object.New(nil)
	_, err := SendCMoveWithProgress(ctx, nil, 1, CMoveRequest{
		AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.2.2.2",
		MessageID:           1,
		Priority:            0,
		MoveDestination:     "DESTAE",
	}, identifier, transfer.ImplicitVRLittleEndian)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendCMoveWithProgress() error = %v, want context.Canceled", err)
	}
}
