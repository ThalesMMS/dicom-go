package dimse

import (
	"errors"
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

// MissingPresentationContextError explains why a SOP Class has no accepted
// presentation context using preserved UL negotiation results. It never
// inspects error strings and never includes dataset or patient identifiers.
type MissingPresentationContextError struct {
	SOPClassUID string
	Outcomes    []ul.PresentationContextOutcome
}

func (e *MissingPresentationContextError) Error() string {
	if e == nil {
		return "dicom dimse: no accepted presentation context"
	}
	if len(e.Outcomes) == 0 {
		return fmt.Sprintf("dicom dimse: no accepted presentation context for SOP Class UID %q", e.SOPClassUID)
	}
	parts := make([]string, 0, len(e.Outcomes))
	for _, outcome := range e.Outcomes {
		parts = append(parts, outcome.Explain())
	}
	return fmt.Sprintf("dicom dimse: no accepted presentation context for SOP Class UID %q: %s", e.SOPClassUID, strings.Join(parts, "; "))
}

// Result returns the peer result/reason of the first matching outcome.
func (e *MissingPresentationContextError) Result() byte {
	if e == nil || len(e.Outcomes) == 0 {
		return 0
	}
	return e.Outcomes[0].Result
}

// ResultName returns the stable label for Result.
func (e *MissingPresentationContextError) ResultName() string {
	if e == nil || len(e.Outcomes) == 0 {
		return ""
	}
	return e.Outcomes[0].ResultName()
}

// ExplainMissingPresentationContext reports why sopClassUID has no accepted
// presentation context. Callers should inspect Result/ResultName rather than
// matching diagnostic text.
func ExplainMissingPresentationContext(assoc *ul.Association, sopClassUID string) error {
	if assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	matching := make([]ul.PresentationContextOutcome, 0, 1)
	for _, outcome := range assoc.PresentationContextOutcomes() {
		if outcome.AbstractSyntaxUID == sopClassUID && !outcome.Accepted() {
			matching = append(matching, outcome)
		}
	}
	return &MissingPresentationContextError{SOPClassUID: sopClassUID, Outcomes: matching}
}

// FormatPresentationContextDiagnostic extracts a PHI-free presentation-context
// explanation from err when one is available.
func FormatPresentationContextDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	var missing *MissingPresentationContextError
	if errors.As(err, &missing) {
		return missing.Error()
	}
	var noneAccepted *ul.NoAcceptedPresentationContextsError
	if errors.As(err, &noneAccepted) {
		return noneAccepted.Error()
	}
	return ""
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
