package dimse

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// ReceiveCFindResponse reads a single C-FIND-RSP and (if present) its Identifier dataset.
//
// The returned identifier is nil when the response indicates no dataset present.
//
// NOTE: Higher-level code is expected to loop calling ReceiveCFindResponse until
// it receives a final response status.
func ReceiveCFindResponse(assoc *ul.Association, pcID byte, identifierSyntax transfer.Syntax) (*CFindResponse, *object.Object, error) {
	return receiveCFindResponseWithContext(nil, assoc, pcID, identifierSyntax)
}

func receiveCFindResponseWithContext(ctx context.Context, assoc *ul.Association, pcID byte, identifierSyntax transfer.Syntax) (*CFindResponse, *object.Object, error) {
	if assoc == nil {
		return nil, nil, fmt.Errorf("dicom dimse: nil association")
	}

	cmd, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return nil, nil, err
	}
	resp, err := ParseCFindResponse(cmd)
	if err != nil {
		return nil, nil, err
	}

	switch resp.CommandDataSetType {
	case NoDataSet:
		return resp, nil, nil
	case DataSetPresent:
		id, err := receiveDataSetWithContext(ctx, assoc, pcID, identifierSyntax)
		if err != nil {
			return nil, nil, err
		}
		return resp, id, nil
	default:
		// Some implementations may send unexpected values. Treat as a protocol error.
		return nil, nil, fmt.Errorf("dicom dimse: C-FIND response dataset type 0x%04X (expected 0x%04X or 0x%04X)", resp.CommandDataSetType, NoDataSet, DataSetPresent)
	}
}
