package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

// CCancelRequest represents a DIMSE C-CANCEL-RQ command for the operation with
// Message ID Being Responded To. It has no response and no dataset.
type CCancelRequest struct {
	MessageIDBeingRespondedTo uint16
}

func (r CCancelRequest) CommandSet() []core.Element {
	return []core.Element{
		newUSCommandElement(CommandField, CCancelRQ),
		newUSCommandElement(MessageIDBeingRespondedTo, r.MessageIDBeingRespondedTo),
		newUSCommandElement(CommandDataSetType, NoDataSet),
	}
}

func ParseCCancelRequest(obj *object.Object) (*CCancelRequest, error) {
	field, err := CommandUint16(obj, CommandField)
	if err != nil {
		return nil, err
	}
	if field != CCancelRQ {
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X, want C-CANCEL-RQ 0x%04X", field, CCancelRQ)
	}
	msgID, err := CommandUint16(obj, MessageIDBeingRespondedTo)
	if err != nil {
		return nil, err
	}
	dataSetType, err := CommandUint16(obj, CommandDataSetType)
	if err != nil {
		return nil, err
	}
	if dataSetType != NoDataSet {
		return nil, fmt.Errorf("dicom dimse: C-CANCEL request dataset type 0x%04X, want no dataset 0x%04X", dataSetType, NoDataSet)
	}
	return &CCancelRequest{MessageIDBeingRespondedTo: msgID}, nil
}

func SendCCancelRequest(assoc *ul.Association, pcID byte, req CCancelRequest) error {
	return SendCCancelRequestWithContext(nil, assoc, pcID, req)
}

// SendCCancelRequestWithContext sends a C-CANCEL-RQ whose command write
// observes ctx.
func SendCCancelRequestWithContext(ctx context.Context, assoc *ul.Association, pcID byte, req CCancelRequest) error {
	return SendCommandSetWithContext(ctx, assoc, pcID, req.CommandSet())
}
