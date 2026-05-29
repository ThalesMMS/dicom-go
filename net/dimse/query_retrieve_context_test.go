package dimse

import (
	"errors"
	"slices"
	"testing"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestQueryRetrievePresentationContexts(t *testing.T) {
	tests := []struct {
		name             string
		build            func() ul.PresentationContext
		wantAbstract     string
		wantTransferUIDs []string
	}{
		{
			name:             "study-root-find",
			build:            StudyRootFindPresentationContext,
			wantAbstract:     StudyRootFindSOPClassUID,
			wantTransferUIDs: DefaultFindTransferSyntaxes,
		},
		{
			name:             "patient-root-find",
			build:            PatientRootFindPresentationContext,
			wantAbstract:     PatientRootFindSOPClassUID,
			wantTransferUIDs: DefaultFindTransferSyntaxes,
		},
		{
			name:             "study-root-move",
			build:            StudyRootMovePresentationContext,
			wantAbstract:     StudyRootMoveSOPClassUID,
			wantTransferUIDs: DefaultMoveTransferSyntaxes,
		},
		{
			name:             "patient-root-move",
			build:            PatientRootMovePresentationContext,
			wantAbstract:     PatientRootMoveSOPClassUID,
			wantTransferUIDs: DefaultMoveTransferSyntaxes,
		},
		{
			name:             "study-root-get",
			build:            StudyRootGetPresentationContext,
			wantAbstract:     StudyRootGetSOPClassUID,
			wantTransferUIDs: DefaultGetTransferSyntaxes,
		},
		{
			name:             "patient-root-get",
			build:            PatientRootGetPresentationContext,
			wantAbstract:     PatientRootGetSOPClassUID,
			wantTransferUIDs: DefaultGetTransferSyntaxes,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pc := tc.build()
			if pc.AbstractSyntaxUID != tc.wantAbstract {
				t.Fatalf("AbstractSyntaxUID = %q, want %q", pc.AbstractSyntaxUID, tc.wantAbstract)
			}
			if !slices.Equal(pc.TransferSyntaxUIDs, tc.wantTransferUIDs) {
				t.Fatalf("TransferSyntaxUIDs = %v, want %v", pc.TransferSyntaxUIDs, tc.wantTransferUIDs)
			}
			pc.TransferSyntaxUIDs[0] = "mutated"
			if tc.wantTransferUIDs[0] == "mutated" {
				t.Fatalf("%s returned shared transfer syntax slice", tc.name)
			}
			next := tc.build()
			if next.TransferSyntaxUIDs[0] == "mutated" {
				t.Fatalf("%s reused a mutated transfer syntax slice", tc.name)
			}
		})
	}
}

func TestAcceptedPresentationContextHelpers(t *testing.T) {
	assoc := &ul.Association{
		AcceptedContexts: []ul.AcceptedContext{
			{ID: 1, AbstractSyntaxUID: StudyRootFindSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID},
			{ID: 3, AbstractSyntaxUID: StudyRootMoveSOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID},
		},
		AcceptedRoleSelections: []ul.RoleSelectionItem{
			{SopClassUID: "1.2.3.storage", SCPRole: true},
		},
	}

	pc, err := AcceptedContextByID(assoc, 3)
	if err != nil {
		t.Fatalf("AcceptedContextByID() error = %v", err)
	}
	if pc.AbstractSyntaxUID != StudyRootMoveSOPClassUID {
		t.Fatalf("AcceptedContextByID() = %#v", pc)
	}
	syntax, err := AcceptedTransferSyntax(assoc, 3)
	if err != nil {
		t.Fatalf("AcceptedTransferSyntax() error = %v", err)
	}
	if syntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("AcceptedTransferSyntax() = %q", syntax.UID)
	}
	if !AcceptedStorageSCPRole(assoc, "1.2.3.storage") {
		t.Fatal("AcceptedStorageSCPRole() = false, want true")
	}
	if AcceptedStorageSCPRole(assoc, "1.2.3.other") {
		t.Fatal("AcceptedStorageSCPRole() = true for missing role")
	}
	if _, err := AcceptedContextByID(assoc, 7); err == nil {
		t.Fatal("AcceptedContextByID() missing context error = nil")
	}

	_, err = TransferSyntaxForAcceptedContext(ul.AcceptedContext{ID: 11, TransferSyntaxUID: "1.2.3.unknown"})
	if !errors.Is(err, transfer.ErrUnknownTransferSyntax) {
		t.Fatalf("TransferSyntaxForAcceptedContext() error = %v, want ErrUnknownTransferSyntax", err)
	}
}
