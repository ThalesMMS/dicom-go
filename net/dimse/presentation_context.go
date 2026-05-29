package dimse

import (
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func presentationContextFor(abstractSyntaxUID string, transferSyntaxUIDs []string) ul.PresentationContext {
	return ul.PresentationContext{
		AbstractSyntaxUID:  abstractSyntaxUID,
		TransferSyntaxUIDs: append([]string(nil), transferSyntaxUIDs...),
	}
}

// AcceptedContextByID returns the accepted presentation context with the given
// presentation context ID.
func AcceptedContextByID(assoc *ul.Association, pcID byte) (ul.AcceptedContext, error) {
	if assoc == nil {
		return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: nil association")
	}
	for _, pc := range assoc.AcceptedContexts {
		if pc.ID == pcID {
			return pc, nil
		}
	}
	return ul.AcceptedContext{}, fmt.Errorf("dicom dimse: no accepted presentation context %d", pcID)
}

// AcceptedTransferSyntax returns the transfer syntax accepted for a presentation
// context ID.
func AcceptedTransferSyntax(assoc *ul.Association, pcID byte) (transfer.Syntax, error) {
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return transfer.Syntax{}, err
	}
	return TransferSyntaxForAcceptedContext(pc)
}

// TransferSyntaxForAcceptedContext resolves an accepted context transfer syntax.
func TransferSyntaxForAcceptedContext(pc ul.AcceptedContext) (transfer.Syntax, error) {
	if syntax, ok := transfer.DefaultRegistry.Get(pc.TransferSyntaxUID); ok {
		return syntax, nil
	}
	return transfer.Syntax{}, fmt.Errorf("%w: %q for presentation context %d", transfer.ErrUnknownTransferSyntax, pc.TransferSyntaxUID, pc.ID)
}

// AcceptedStorageSCPRole reports whether an association accepted SCP role for a
// storage SOP Class, as required by C-GET sub-operation storage.
func AcceptedStorageSCPRole(assoc *ul.Association, sopClassUID string) bool {
	if assoc == nil {
		return false
	}
	sopClassUID = strings.TrimSpace(sopClassUID)
	for _, role := range assoc.AcceptedRoleSelections {
		if role.SopClassUID == sopClassUID && role.SCPRole {
			return true
		}
	}
	return false
}
