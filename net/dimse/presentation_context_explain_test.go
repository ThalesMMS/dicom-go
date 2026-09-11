package dimse

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestExplainMissingPresentationContextDistinguishesRefusalReasons(t *testing.T) {
	t.Parallel()
	assoc := &ul.Association{
		AcceptedContexts: []ul.AcceptedContext{{
			ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
		}},
		ContextOutcomes: []ul.PresentationContextOutcome{
			{ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, ProposedTransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID}, Result: ul.PresentationContextAcceptance, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID},
			{ID: 3, AbstractSyntaxUID: "1.2.840.10008.5.1.4.1.1.2", ProposedTransferSyntaxUIDs: []string{transfer.JPEGBaseline.UID}, Result: ul.PresentationContextAbstractSyntaxNotSupported},
			{ID: 5, AbstractSyntaxUID: "1.2.840.10008.5.1.4.1.1.4", ProposedTransferSyntaxUIDs: []string{transfer.JPEGLosslessSV1.UID, transfer.ImplicitVRLittleEndian.UID}, Result: ul.PresentationContextTransferSyntaxesNotSupported},
		},
	}

	abstractErr := ExplainMissingPresentationContext(assoc, "1.2.840.10008.5.1.4.1.1.2")
	var abstract *MissingPresentationContextError
	if !errors.As(abstractErr, &abstract) {
		t.Fatalf("abstract error = %v", abstractErr)
	}
	if abstract.Result() != ul.PresentationContextAbstractSyntaxNotSupported {
		t.Fatalf("abstract result = %d", abstract.Result())
	}

	transferErr := ExplainMissingPresentationContext(assoc, "1.2.840.10008.5.1.4.1.1.4")
	var ts *MissingPresentationContextError
	if !errors.As(transferErr, &ts) {
		t.Fatalf("transfer error = %v", transferErr)
	}
	if ts.Result() != ul.PresentationContextTransferSyntaxesNotSupported {
		t.Fatalf("transfer result = %d", ts.Result())
	}
	if ts.ResultName() == abstract.ResultName() {
		t.Fatalf("reasons were not distinct: %q vs %q", abstract.ResultName(), ts.ResultName())
	}
	if !strings.Contains(transferErr.Error(), transfer.JPEGLosslessSV1.UID) {
		t.Fatalf("transfer diagnostic %q omitted proposed UIDs", transferErr)
	}
	if strings.Contains(strings.ToUpper(transferErr.Error()), "PATIENT") || strings.Contains(transferErr.Error(), "John") {
		t.Fatalf("diagnostic leaked PHI: %q", transferErr)
	}

	_, err := AcceptedContextForSOPClass(assoc, "1.2.840.10008.5.1.4.1.1.4")
	if !errors.As(err, &ts) {
		t.Fatalf("AcceptedContextForSOPClass() error = %v, want MissingPresentationContextError", err)
	}
}

func TestFormatPresentationContextDiagnosticFromTypedULError(t *testing.T) {
	t.Parallel()
	err := &ul.NoAcceptedPresentationContextsError{Outcomes: []ul.PresentationContextOutcome{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, ProposedTransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID}, Result: ul.PresentationContextUserRejection,
	}}}
	wrapped := fmt.Errorf("associate: %w", err)
	got := FormatPresentationContextDiagnostic(wrapped)
	if !strings.Contains(got, "user-rejection") {
		t.Fatalf("diagnostic = %q", got)
	}
	if strings.Contains(got, "PATIENT") {
		t.Fatalf("diagnostic leaked PHI: %q", got)
	}
}
