package ul

import (
	"fmt"
	"strings"
)

// PresentationContextOutcome records one A-ASSOCIATE-AC presentation-context
// result together with the matching proposal. Association accessors return
// defensive copies so callers cannot mutate negotiation state.
type PresentationContextOutcome struct {
	ID                         byte
	AbstractSyntaxUID          string
	ProposedTransferSyntaxUIDs []string
	Result                     byte
	TransferSyntaxUID          string
}

// Accepted reports whether the peer accepted this presentation context.
func (o PresentationContextOutcome) Accepted() bool {
	return o.Result == PresentationContextAcceptance
}

// ResultName returns a stable, PHI-free label for the PS3.8 result/reason.
func (o PresentationContextOutcome) ResultName() string {
	return presentationContextResultName(o.Result)
}

// Explain returns a diagnostic that names the SOP Class UID, proposed transfer
// syntax UIDs, and result without dataset or patient identifiers.
func (o PresentationContextOutcome) Explain() string {
	proposed := strings.Join(o.ProposedTransferSyntaxUIDs, ",")
	if proposed == "" {
		proposed = "<none>"
	}
	if o.Accepted() {
		return fmt.Sprintf("presentation context %d abstract-syntax=%s transfer-syntax=%s result=%s",
			o.ID, o.AbstractSyntaxUID, o.TransferSyntaxUID, o.ResultName())
	}
	return fmt.Sprintf("presentation context %d abstract-syntax=%s proposed-transfer-syntaxes=[%s] result=%s",
		o.ID, o.AbstractSyntaxUID, proposed, o.ResultName())
}

func presentationContextResultName(result byte) string {
	switch result {
	case PresentationContextAcceptance:
		return "acceptance"
	case PresentationContextUserRejection:
		return "user-rejection"
	case PresentationContextNoReason:
		return "no-reason"
	case PresentationContextAbstractSyntaxNotSupported:
		return "abstract-syntax-not-supported"
	case PresentationContextTransferSyntaxesNotSupported:
		return "transfer-syntaxes-not-supported"
	default:
		return fmt.Sprintf("unrecognized-result-%d", result)
	}
}

// NoAcceptedPresentationContextsError is returned when an A-ASSOCIATE-AC
// accepts no presentation context. It unwraps to
// ErrNoAcceptedPresentationContexts and exposes every peer result.
type NoAcceptedPresentationContextsError struct {
	Outcomes []PresentationContextOutcome
}

func (e *NoAcceptedPresentationContextsError) Error() string {
	if e == nil {
		return ErrNoAcceptedPresentationContexts.Error()
	}
	if len(e.Outcomes) == 0 {
		return ErrNoAcceptedPresentationContexts.Error()
	}
	parts := make([]string, 0, len(e.Outcomes))
	for _, outcome := range e.Outcomes {
		parts = append(parts, outcome.Explain())
	}
	return fmt.Sprintf("%s: %s", ErrNoAcceptedPresentationContexts, strings.Join(parts, "; "))
}

func (e *NoAcceptedPresentationContextsError) Unwrap() error {
	return ErrNoAcceptedPresentationContexts
}

// Rejected returns a defensive copy of the rejected outcomes.
func (e *NoAcceptedPresentationContextsError) Rejected() []PresentationContextOutcome {
	if e == nil {
		return nil
	}
	return clonePresentationContextOutcomes(e.Outcomes)
}

// PresentationContextOutcomes returns a defensive copy of every negotiated
// presentation-context result, including rejections on a partially accepted
// association.
func (a *Association) PresentationContextOutcomes() []PresentationContextOutcome {
	if a == nil {
		return nil
	}
	return clonePresentationContextOutcomes(a.ContextOutcomes)
}

// RejectedPresentationContexts returns a defensive copy of the peer results
// that were not accepted.
func (a *Association) RejectedPresentationContexts() []PresentationContextOutcome {
	if a == nil {
		return nil
	}
	rejected := make([]PresentationContextOutcome, 0, len(a.ContextOutcomes))
	for _, outcome := range a.ContextOutcomes {
		if !outcome.Accepted() {
			rejected = append(rejected, clonePresentationContextOutcome(outcome))
		}
	}
	if len(rejected) == 0 {
		return nil
	}
	return rejected
}

func clonePresentationContextOutcomes(outcomes []PresentationContextOutcome) []PresentationContextOutcome {
	if len(outcomes) == 0 {
		return nil
	}
	cloned := make([]PresentationContextOutcome, len(outcomes))
	for i, outcome := range outcomes {
		cloned[i] = clonePresentationContextOutcome(outcome)
	}
	return cloned
}

func clonePresentationContextOutcome(outcome PresentationContextOutcome) PresentationContextOutcome {
	outcome.ProposedTransferSyntaxUIDs = append([]string(nil), outcome.ProposedTransferSyntaxUIDs...)
	return outcome
}

func presentationContextOutcomesFromResults(proposed []PresentationContextProposed, results []PresentationContextResult) []PresentationContextOutcome {
	proposedByID := make(map[byte]PresentationContextProposed, len(proposed))
	for _, pc := range proposed {
		proposedByID[pc.ID] = pc
	}
	outcomes := make([]PresentationContextOutcome, 0, len(results))
	for _, result := range results {
		pc := proposedByID[result.ID]
		outcome := PresentationContextOutcome{
			ID:                         result.ID,
			AbstractSyntaxUID:          pc.AbstractSyntaxUID,
			ProposedTransferSyntaxUIDs: append([]string(nil), pc.TransferSyntaxUIDs...),
			Result:                     result.Result,
		}
		if result.Result == PresentationContextAcceptance {
			outcome.TransferSyntaxUID = result.TransferSyntaxUID
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func noAcceptedPresentationContextsError(outcomes []PresentationContextOutcome) error {
	return &NoAcceptedPresentationContextsError{Outcomes: clonePresentationContextOutcomes(outcomes)}
}
