package dimse

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestNewFindClientForSOPClassDerivesAcceptedContextAndSyntax(t *testing.T) {
	assoc := &ul.Association{AcceptedContexts: []ul.AcceptedContext{{
		ID:                5,
		AbstractSyntaxUID: StudyRootFindSOPClassUID,
		TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}}}

	client, err := NewFindClientForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("NewFindClientForSOPClass() error = %v", err)
	}
	if client.PresentationCtxID != 5 {
		t.Fatalf("PresentationCtxID = %d, want 5", client.PresentationCtxID)
	}
	if client.IdentifierSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("IdentifierSyntax = %q", client.IdentifierSyntax.UID)
	}
}

func TestFindClientRejectsMismatchedAffectedSOPClassUID(t *testing.T) {
	client := &FindClient{
		Assoc:             &ul.Association{},
		SOPClassUID:       StudyRootFindSOPClassUID,
		PresentationCtxID: 1,
		IdentifierSyntax:  transfer.ImplicitVRLittleEndian,
	}

	results, errs := client.Find(CFindRequest{AffectedSOPClassUID: PatientRootFindSOPClassUID}, object.New(nil))
	if _, ok := <-results; ok {
		t.Fatal("Find() result channel is open, want closed")
	}
	err := <-errs
	if err == nil || !strings.Contains(err.Error(), "AffectedSOPClassUID") {
		t.Fatalf("Find() error = %v, want AffectedSOPClassUID mismatch", err)
	}
}

func TestFindClientValidateAcceptsEmptyAndPaddedEquivalentSOPClassUID(t *testing.T) {
	client := &FindClient{
		Assoc:             &ul.Association{},
		SOPClassUID:       StudyRootFindSOPClassUID,
		PresentationCtxID: 1,
		IdentifierSyntax:  transfer.ImplicitVRLittleEndian,
	}

	tests := []struct {
		name                string
		affectedSOPClassUID string
	}{
		{name: "empty", affectedSOPClassUID: ""},
		{name: "exact match", affectedSOPClassUID: StudyRootFindSOPClassUID},
		{name: "space and NUL padding", affectedSOPClassUID: StudyRootFindSOPClassUID + " \x00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizedSOPClassUID, err := client.validate(CFindRequest{AffectedSOPClassUID: tt.affectedSOPClassUID})
			if err != nil {
				t.Fatalf("validate(AffectedSOPClassUID=%q) error = %v", tt.affectedSOPClassUID, err)
			}
			if normalizedSOPClassUID != StudyRootFindSOPClassUID {
				t.Fatalf("validate(AffectedSOPClassUID=%q) = %q, want %q", tt.affectedSOPClassUID, normalizedSOPClassUID, StudyRootFindSOPClassUID)
			}
		})
	}
}
