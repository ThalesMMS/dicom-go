package dimse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/telemetry"
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
	return SendCommandSetWithContext(nil, assoc, pcID, elements)
}

// SendCommandSetWithContext encodes and sends one command set while making all
// fragmented P-DATA writes observe ctx.
func SendCommandSetWithContext(ctx context.Context, assoc *ul.Association, pcID byte, elements []core.Element) error {
	started := time.Now()
	ctx = commandWriteContext(ctx)
	data, err := EncodeCommandSet(elements)
	if err != nil {
		return err
	}
	writer := NewPDataWriterWithContext(ctx, assoc, pcID, true, peerMaxPDUWithHeader(assoc))
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Finish(); err != nil {
		return err
	}
	observeCommand(assoc, telemetry.Outbound, pcID, object.FromElements(elements, nil), time.Since(started))
	return nil
}

func ReceiveCommandSet(assoc *ul.Association, pcID byte) (*object.Object, error) {
	return receiveCommandSetWithContext(nil, assoc, pcID)
}

// ReceiveCommandSetAny reads the next command set from whichever presentation
// context the peer uses and returns that context ID with the decoded command.
// It is useful for DIMSE operations that can receive interleaved commands on
// multiple presentation contexts, such as C-GET C-STORE-RSP and C-CANCEL-RQ.
func ReceiveCommandSetAny(assoc *ul.Association) (byte, *object.Object, error) {
	return ReceiveCommandSetAnyWithContext(nil, assoc)
}

// ReceiveCommandSetAnyWithContext is ReceiveCommandSetAny with a caller-supplied
// receive context, useful for bounded polling of optional interleaved commands.
func ReceiveCommandSetAnyWithContext(ctx context.Context, assoc *ul.Association) (byte, *object.Object, error) {
	ctx = commandReadContext(ctx)
	return receiveCommandSetAnyWithContext(ctx, assoc)
}

func receiveCommandSetWithContext(ctx context.Context, assoc *ul.Association, pcID byte) (*object.Object, error) {
	ctx = commandReadContext(ctx)
	reader := newTypedPDataReaderWithContext(ctx, assoc, pcID, true)
	var data bytes.Buffer
	if _, err := io.Copy(&data, reader); err != nil {
		return nil, err
	}
	command, err := DecodeCommandSet(data.Bytes())
	if err == nil {
		observeCommand(assoc, telemetry.Inbound, pcID, command, 0)
	}
	return command, err
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
	if err := SendCommandSet(assoc, pcID, req.CommandSet()); err != nil {
		return nil, err
	}
	obj, err := ReceiveCommandSet(assoc, pcID)
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
	return SendCEchoResponseWithContext(nil, assoc, pcID, messageID, status)
}

func SendCEchoResponseWithContext(ctx context.Context, assoc *ul.Association, pcID byte, messageID uint16, status uint16) error {
	resp := CEchoResponse{MessageIDBeingRespondedTo: messageID, Status: status}
	return SendCommandSetWithContext(ctx, assoc, pcID, resp.CommandSet())
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
	maxPDU := ul.DefaultMaxPDU
	if assoc != nil && assoc.PeerMaxPDU > 0 && assoc.PeerMaxPDU < maxPDU {
		maxPDU = assoc.PeerMaxPDU
	}
	return int(maxPDU + ul.PDUHeaderSize)
}
