package dimse

import (
	"bytes"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// SendCFindRequest sends a C-FIND-RQ command set followed by the identifier dataset.
//
// pcID must refer to an accepted presentation context suitable for the query model
// (e.g. Study Root Query/Retrieve Information Model - FIND). The command set is
// always encoded as Implicit VR Little Endian per the DICOM standard.
//
// The identifier dataset is sent as dataset P-DATA (IsCommand=false) and must be
// encoded using the transfer syntax negotiated for that presentation context.
//
// NOTE: This is low-level DIMSE plumbing. Higher-level APIs will handle model/
// key composition and response iteration.
func SendCFindRequest(assoc *ul.Association, pcID byte, req CFindRequest, identifier []core.Element) error {
	if assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	if req.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: missing C-FIND Affected SOP Class UID")
	}
	// Per standard, C-FIND always has an Identifier dataset.
	if identifier == nil {
		return fmt.Errorf("dicom dimse: missing C-FIND identifier dataset")
	}

	// 1) Send command set PDVs.
	if err := SendCommandSet(assoc, pcID, req.CommandSet()); err != nil {
		return err
	}

	// 2) Send identifier dataset PDVs.
	//
	// Transfer syntax used for the dataset is implied by the negotiated presentation
	// context.
	var accepted ul.AcceptedContext
	for _, pc := range assoc.AcceptedContexts {
		if pc.ID == pcID {
			accepted = pc
			break
		}
	}
	if accepted.ID != pcID {
		return fmt.Errorf("dicom dimse: no accepted presentation context with ID %d", pcID)
	}
	syntax, ok := transfer.DefaultRegistry.Get(accepted.TransferSyntaxUID)
	if !ok {
		return fmt.Errorf("dicom dimse: unsupported transfer syntax %q", accepted.TransferSyntaxUID)
	}
	var buf bytes.Buffer
	w := parser.NewWriter(&buf, syntax)
	for _, el := range identifier {
		if err := w.WriteElement(el); err != nil {
			return err
		}
	}
	data := buf.Bytes()
	writer := NewPDataWriter(assoc, pcID, false, peerMaxPDUWithHeader(assoc))
	if _, err := writer.Write(data); err != nil {
		return err
	}
	return writer.Finish()
}
