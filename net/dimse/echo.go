package dimse

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

func AcceptedVerificationContext(assoc *ul.Association) (ul.AcceptedContext, bool) {
	if assoc == nil {
		return ul.AcceptedContext{}, false
	}
	for _, pc := range assoc.AcceptedContexts {
		if pc.AbstractSyntaxUID == VerificationSOPClassUID {
			return pc, true
		}
	}
	return ul.AcceptedContext{}, false
}

func SendCommandSet(assoc *ul.Association, pcID byte, elements []core.Element) error {
	data, err := EncodeCommandSet(elements)
	if err != nil {
		return err
	}
	writer := NewPDataWriter(assoc, pcID, true, peerMaxPDUWithHeader(assoc))
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return writer.Finish()
}

func ReceiveCommandSet(assoc *ul.Association, pcID byte) (*object.Object, error) {
	return receiveCommandSetWithContext(nil, assoc, pcID)
}

func receiveCommandSetWithContext(ctx context.Context, assoc *ul.Association, pcID byte) (*object.Object, error) {
	reader := newTypedPDataReaderWithContext(ctx, assoc, pcID, true)
	var data bytes.Buffer
	if _, err := io.Copy(&data, reader); err != nil {
		return nil, err
	}
	return DecodeCommandSet(data.Bytes())
}

type CEchoRequest struct {
	MessageID uint16
}

func (r CEchoRequest) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(AffectedSOPClassUID, VerificationSOPClassUID),
		newUSCommandElement(CommandField, CEchoRQ),
		newUSCommandElement(MessageID, r.MessageID),
		newUSCommandElement(CommandDataSetType, NoDataSet),
	}
}

type CEchoResponse struct {
	MessageIDBeingRespondedTo uint16
	Status                    uint16
}

func (r CEchoResponse) CommandSet() []core.Element {
	return []core.Element{
		newUIElement(AffectedSOPClassUID, VerificationSOPClassUID),
		newUSCommandElement(CommandField, CEchoRSP),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUSCommandElement(Status, r.Status),
	}
}

func ParseCEchoResponse(obj *object.Object) (*CEchoResponse, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CEchoRSP {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-ECHO-RSP 0x%04X", field, CEchoRSP)
	}
	messageID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dataSetType != NoDataSet {
		return nil, fmt.Errorf("dicom dimse: C-ECHO response dataset type 0x%04X, want no dataset 0x%04X", dataSetType, NoDataSet)
	}
	status, err := CommandUint16(obj, Status)
	if err != nil {
		return nil, err
	}
	return &CEchoResponse{MessageIDBeingRespondedTo: messageID, Status: status}, nil
}

func SendCEcho(assoc *ul.Association, pcID byte, messageID uint16) (*CEchoResponse, error) {
	req := CEchoRequest{MessageID: messageID}
	data, err := EncodeCommandSet(req.CommandSet())
	if err != nil {
		return nil, err
	}
	writer := NewPDataWriter(assoc, pcID, true, peerMaxPDUWithHeader(assoc))
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Finish(); err != nil {
		return nil, err
	}

	reader := newTypedPDataReader(assoc, pcID, true)
	var responseData bytes.Buffer
	if _, err := io.Copy(&responseData, reader); err != nil {
		return nil, err
	}
	obj, err := DecodeCommandSet(responseData.Bytes())
	if err != nil {
		return nil, err
	}
	resp, err := ParseCEchoResponse(obj)
	if err != nil {
		return nil, err
	}
	if resp.MessageIDBeingRespondedTo != messageID {
		return nil, fmt.Errorf("dicom dimse: response message ID %d, want %d", resp.MessageIDBeingRespondedTo, messageID)
	}
	if resp.Status != StatusSuccess {
		return nil, fmt.Errorf("dicom dimse: C-ECHO failed with status 0x%04X", resp.Status)
	}
	return resp, nil
}

func SendCEchoRequest(assoc *ul.Association, pcID byte, messageID uint16) error {
	req := CEchoRequest{MessageID: messageID}
	return SendCommandSet(assoc, pcID, req.CommandSet())
}

func ReceiveCEchoResponse(assoc *ul.Association, pcID byte, messageID uint16) (uint16, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return 0, err
	}
	resp, err := ParseCEchoResponse(command)
	if err != nil {
		return 0, err
	}
	if resp.MessageIDBeingRespondedTo != messageID {
		return 0, fmt.Errorf("dicom dimse: response message ID %d, want %d", resp.MessageIDBeingRespondedTo, messageID)
	}
	return resp.Status, nil
}

func ReceiveCEcho(assoc *ul.Association, pcID byte) (uint16, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return 0, err
	}
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return 0, err
	}
	if field != CEchoRQ {
		return 0, fmt.Errorf("dicom dimse: command field 0x%04X, want C-ECHO-RQ 0x%04X", field, CEchoRQ)
	}
	dataSetType, err := CommandUint16(command, CommandDataSetType)
	if err != nil {
		return 0, err
	}
	if dataSetType != NoDataSet {
		return 0, fmt.Errorf("dicom dimse: C-ECHO request dataset type 0x%04X, want no dataset 0x%04X", dataSetType, NoDataSet)
	}
	return CommandUint16(command, MessageID)
}

func SendCEchoResponse(assoc *ul.Association, pcID byte, messageID uint16, status uint16) error {
	resp := CEchoResponse{MessageIDBeingRespondedTo: messageID, Status: status}
	data, err := EncodeCommandSet(resp.CommandSet())
	if err != nil {
		return err
	}
	writer := NewPDataWriter(assoc, pcID, true, peerMaxPDUWithHeader(assoc))
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return writer.Finish()
}

func HandleCEcho(assoc *ul.Association) error {
	pc, ok := AcceptedVerificationContext(assoc)
	if !ok {
		return fmt.Errorf("dicom dimse: no accepted Verification SOP Class presentation context")
	}
	messageID, err := ReceiveCEcho(assoc, pc.ID)
	if err != nil {
		return err
	}
	return SendCEchoResponse(assoc, pc.ID, messageID, StatusSuccess)
}

func peerMaxPDUWithHeader(assoc *ul.Association) int {
	if assoc == nil || assoc.PeerMaxPDU == 0 {
		return int(ul.DefaultMaxPDU + ul.PDUHeaderSize)
	}
	if assoc.PeerMaxPDU > ^uint32(0)-ul.PDUHeaderSize {
		return int(^uint32(0))
	}
	return int(assoc.PeerMaxPDU + ul.PDUHeaderSize)
}
