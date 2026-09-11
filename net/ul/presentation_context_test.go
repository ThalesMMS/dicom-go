package ul

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	ctImageStorageSOPClassUID = "1.2.840.10008.5.1.4.1.1.2"
	mrImageStorageSOPClassUID = "1.2.840.10008.5.1.4.1.1.4"
)

func TestPresentationContextOutcomeResultNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		result byte
		name   string
	}{
		{PresentationContextAcceptance, "acceptance"},
		{PresentationContextUserRejection, "user-rejection"},
		{PresentationContextNoReason, "no-reason"},
		{PresentationContextAbstractSyntaxNotSupported, "abstract-syntax-not-supported"},
		{PresentationContextTransferSyntaxesNotSupported, "transfer-syntaxes-not-supported"},
		{99, "unrecognized-result-99"},
	}
	for _, tc := range cases {
		got := PresentationContextOutcome{Result: tc.result}.ResultName()
		if got != tc.name {
			t.Fatalf("Result=%d ResultName() = %q, want %q", tc.result, got, tc.name)
		}
	}
}

func TestProcessAssociationACPreservesRejectedAndAcceptedContexts(t *testing.T) {
	t.Parallel()
	proposed := []PresentationContextProposed{
		{ID: 1, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian}},
		{ID: 3, AbstractSyntaxUID: ctImageStorageSOPClassUID, TransferSyntaxUIDs: []string{ExplicitVRLittleEndian}},
		{ID: 5, AbstractSyntaxUID: mrImageStorageSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
	}
	ac := &AssociationAC{
		ProtocolVersion:        DefaultProtocolVersion,
		ApplicationContextName: ApplicationContextName,
		PresentationContexts: []PresentationContextResult{
			{ID: 1, Result: PresentationContextAcceptance, TransferSyntaxUID: ExplicitVRLittleEndian},
			{ID: 3, Result: PresentationContextAbstractSyntaxNotSupported},
			{ID: 5, Result: PresentationContextTransferSyntaxesNotSupported},
		},
	}
	accepted, outcomes, err := processAssociationAC(ac, proposed, DialOptions{
		ProtocolVersion:        DefaultProtocolVersion,
		ApplicationContextName: ApplicationContextName,
	})
	if err != nil {
		t.Fatalf("processAssociationAC() error = %v", err)
	}
	if len(accepted) != 1 || accepted[0] != (AcceptedContext{ID: 1, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUID: ExplicitVRLittleEndian}) {
		t.Fatalf("accepted = %#v", accepted)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %#v, want 3", outcomes)
	}

	assoc := &Association{AcceptedContexts: accepted, ContextOutcomes: outcomes}
	rejected := assoc.RejectedPresentationContexts()
	if len(rejected) != 2 {
		t.Fatalf("RejectedPresentationContexts() len = %d, want 2", len(rejected))
	}
	if rejected[0].ResultName() == rejected[1].ResultName() {
		t.Fatalf("rejected reasons were not distinct: %#v", rejected)
	}
	if rejected[0].Result != PresentationContextAbstractSyntaxNotSupported || rejected[0].AbstractSyntaxUID != ctImageStorageSOPClassUID {
		t.Fatalf("first rejection = %#v", rejected[0])
	}
	if rejected[1].Result != PresentationContextTransferSyntaxesNotSupported || rejected[1].AbstractSyntaxUID != mrImageStorageSOPClassUID {
		t.Fatalf("second rejection = %#v", rejected[1])
	}
	if got := rejected[1].Explain(); !strings.Contains(got, ImplicitVRLittleEndian) {
		t.Fatalf("transfer-syntax diagnostic %q does not name proposed UIDs", got)
	}
	if strings.Contains(gotPHIProbe(rejected[1].Explain()), "PATIENT") {
		t.Fatalf("diagnostic leaked PHI probe: %q", rejected[1].Explain())
	}
}

func TestProcessAssociationACRejectsAllResultCodesAsInspectableError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result byte
	}{
		{"user-rejection", PresentationContextUserRejection},
		{"no-reason", PresentationContextNoReason},
		{"abstract-syntax-not-supported", PresentationContextAbstractSyntaxNotSupported},
		{"transfer-syntaxes-not-supported", PresentationContextTransferSyntaxesNotSupported},
		{"unrecognized", 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			proposed := []PresentationContextProposed{{
				ID: 1, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
			}}
			ac := &AssociationAC{
				ProtocolVersion:        DefaultProtocolVersion,
				ApplicationContextName: ApplicationContextName,
				PresentationContexts: []PresentationContextResult{
					{ID: 1, Result: tc.result, TransferSyntaxUID: ImplicitVRLittleEndian},
				},
			}
			accepted, _, err := processAssociationAC(ac, proposed, DialOptions{
				ProtocolVersion:        DefaultProtocolVersion,
				ApplicationContextName: ApplicationContextName,
			})
			if len(accepted) != 0 {
				t.Fatalf("accepted = %#v, want none", accepted)
			}
			if !errors.Is(err, ErrNoAcceptedPresentationContexts) {
				t.Fatalf("error = %v, want ErrNoAcceptedPresentationContexts", err)
			}
			var detailed *NoAcceptedPresentationContextsError
			if !errors.As(err, &detailed) {
				t.Fatalf("error = %v, want NoAcceptedPresentationContextsError", err)
			}
			if len(detailed.Outcomes) != 1 || detailed.Outcomes[0].Result != tc.result {
				t.Fatalf("outcomes = %#v", detailed.Outcomes)
			}
			if detailed.Outcomes[0].ResultName() != presentationContextResultName(tc.result) {
				t.Fatalf("ResultName() = %q", detailed.Outcomes[0].ResultName())
			}
			if tc.result == PresentationContextTransferSyntaxesNotSupported && !strings.Contains(err.Error(), ImplicitVRLittleEndian) {
				t.Fatalf("error %q does not name proposed transfer syntax", err)
			}
		})
	}
}

func TestProcessAssociationACRejectsUnexpectedAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	proposed := []PresentationContextProposed{
		{ID: 1, AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
	}
	opts := DialOptions{ProtocolVersion: DefaultProtocolVersion, ApplicationContextName: ApplicationContextName}

	_, _, err := processAssociationAC(&AssociationAC{
		ProtocolVersion: DefaultProtocolVersion, ApplicationContextName: ApplicationContextName,
		PresentationContexts: []PresentationContextResult{
			{ID: 3, Result: PresentationContextAcceptance, TransferSyntaxUID: ImplicitVRLittleEndian},
		},
	}, proposed, opts)
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("unexpected ID error = %v, want ErrInvalidPDUField", err)
	}

	_, _, err = processAssociationAC(&AssociationAC{
		ProtocolVersion: DefaultProtocolVersion, ApplicationContextName: ApplicationContextName,
		PresentationContexts: []PresentationContextResult{
			{ID: 1, Result: PresentationContextAcceptance, TransferSyntaxUID: ImplicitVRLittleEndian},
			{ID: 1, Result: PresentationContextUserRejection},
		},
	}, append(proposed, PresentationContextProposed{ID: 3, AbstractSyntaxUID: ctImageStorageSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}}), opts)
	if !errors.Is(err, ErrInvalidPDUField) {
		t.Fatalf("duplicate ID error = %v, want ErrInvalidPDUField", err)
	}
}

func TestNoAcceptedPresentationContextsErrorIsDefensive(t *testing.T) {
	t.Parallel()
	err := noAcceptedPresentationContextsError([]PresentationContextOutcome{{
		ID: 1, AbstractSyntaxUID: verificationSOPClassUID, ProposedTransferSyntaxUIDs: []string{ImplicitVRLittleEndian}, Result: PresentationContextUserRejection,
	}})
	var detailed *NoAcceptedPresentationContextsError
	if !errors.As(err, &detailed) {
		t.Fatalf("error = %v", err)
	}
	cloned := detailed.Rejected()
	cloned[0].ProposedTransferSyntaxUIDs[0] = "mutated"
	cloned[0].AbstractSyntaxUID = "mutated"
	if detailed.Outcomes[0].ProposedTransferSyntaxUIDs[0] != ImplicitVRLittleEndian {
		t.Fatal("Rejected() did not copy proposed transfer syntaxes")
	}
}

func TestDialIndependentPeerPreservesPartialPresentationContextRefusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		pdu, err := ReadPDU(conn, DefaultMaxPDU)
		if err != nil {
			serverDone <- err
			return
		}
		rq, ok := pdu.(*AssociationRQ)
		if !ok {
			serverDone <- errors.New("expected A-ASSOCIATE-RQ")
			return
		}
		if len(rq.PresentationContexts) != 3 {
			serverDone <- errors.New("expected three proposed contexts")
			return
		}
		ac := &AssociationAC{
			ProtocolVersion:        DefaultProtocolVersion,
			CalledAETitle:          rq.CalledAETitle,
			CallingAETitle:         rq.CallingAETitle,
			ApplicationContextName: ApplicationContextName,
			PresentationContexts: []PresentationContextResult{
				{ID: rq.PresentationContexts[0].ID, Result: PresentationContextAcceptance, TransferSyntaxUID: ExplicitVRLittleEndian},
				{ID: rq.PresentationContexts[1].ID, Result: PresentationContextAbstractSyntaxNotSupported, TransferSyntaxUID: ExplicitVRLittleEndian},
				{ID: rq.PresentationContexts[2].ID, Result: PresentationContextTransferSyntaxesNotSupported, TransferSyntaxUID: ImplicitVRLittleEndian},
			},
			UserInfo: implementationUserInfo(DefaultMaxPDU, ImplementationClassUID, ImplementationVersionName),
		}
		serverDone <- WritePDU(conn, ac)
		<-ctx.Done()
	}()

	assoc, err := DialContext(ctx, ln.Addr().String(), DialOptions{
		CalledAETitle:  "PEER_SCP",
		CallingAETitle: "PEER_SCU",
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian, ExplicitVRLittleEndian}},
			{AbstractSyntaxUID: ctImageStorageSOPClassUID, TransferSyntaxUIDs: []string{ExplicitVRLittleEndian}},
			{AbstractSyntaxUID: mrImageStorageSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer assoc.Close()

	if len(assoc.AcceptedContexts) != 1 {
		t.Fatalf("AcceptedContexts = %#v", assoc.AcceptedContexts)
	}
	rejected := assoc.RejectedPresentationContexts()
	if len(rejected) != 2 {
		t.Fatalf("RejectedPresentationContexts() = %#v", rejected)
	}
	if rejected[0].Result != PresentationContextAbstractSyntaxNotSupported {
		t.Fatalf("SOP Class refusal = %#v", rejected[0])
	}
	if rejected[1].Result != PresentationContextTransferSyntaxesNotSupported {
		t.Fatalf("transfer syntax refusal = %#v", rejected[1])
	}
	if !strings.Contains(rejected[1].Explain(), ImplicitVRLittleEndian) {
		t.Fatalf("transfer syntax diagnostic = %q", rejected[1].Explain())
	}
	mutated := assoc.PresentationContextOutcomes()
	mutated[0].AbstractSyntaxUID = "mutated"
	if assoc.PresentationContextOutcomes()[0].AbstractSyntaxUID == "mutated" {
		t.Fatal("PresentationContextOutcomes() did not copy")
	}
	if err := <-serverDone; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("independent peer error = %v", err)
	}
}

func TestDialIndependentPeerTotalRejectionIsTyped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		pdu, err := ReadPDU(conn, DefaultMaxPDU)
		if err != nil {
			return
		}
		rq, ok := pdu.(*AssociationRQ)
		if !ok || len(rq.PresentationContexts) == 0 {
			return
		}
		_ = WritePDU(conn, &AssociationAC{
			ProtocolVersion:        DefaultProtocolVersion,
			CalledAETitle:          rq.CalledAETitle,
			CallingAETitle:         rq.CallingAETitle,
			ApplicationContextName: ApplicationContextName,
			PresentationContexts: []PresentationContextResult{
				{ID: rq.PresentationContexts[0].ID, Result: PresentationContextUserRejection, TransferSyntaxUID: ImplicitVRLittleEndian},
			},
			UserInfo: implementationUserInfo(DefaultMaxPDU, ImplementationClassUID, ImplementationVersionName),
		})
	}()

	_, err = DialContext(ctx, ln.Addr().String(), DialOptions{
		Contexts: []PresentationContext{{
			AbstractSyntaxUID:  verificationSOPClassUID,
			TransferSyntaxUIDs: []string{ImplicitVRLittleEndian},
		}},
	})
	if !errors.Is(err, ErrNoAcceptedPresentationContexts) {
		t.Fatalf("DialContext() error = %v, want ErrNoAcceptedPresentationContexts", err)
	}
	var detailed *NoAcceptedPresentationContextsError
	if !errors.As(err, &detailed) {
		t.Fatalf("DialContext() error = %v, want NoAcceptedPresentationContextsError", err)
	}
	if len(detailed.Outcomes) != 1 || detailed.Outcomes[0].Result != PresentationContextUserRejection {
		t.Fatalf("outcomes = %#v", detailed.Outcomes)
	}
}

func TestAcceptAssociationTotalRejectionPreservesResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		_, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{"1.2.3.4"},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		serverErr <- err
	}()

	_, err = DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	var reject *RejectionError
	if !errors.As(err, &reject) {
		t.Fatalf("DialContext() error = %v, want RejectionError", err)
	}
	err = <-serverErr
	if !errors.Is(err, ErrNoAcceptedPresentationContexts) {
		t.Fatalf("AcceptAssociation() error = %v", err)
	}
	var detailed *NoAcceptedPresentationContextsError
	if !errors.As(err, &detailed) {
		t.Fatalf("AcceptAssociation() error = %v, want NoAcceptedPresentationContextsError", err)
	}
	if len(detailed.Outcomes) != 1 || detailed.Outcomes[0].Result != PresentationContextAbstractSyntaxNotSupported {
		t.Fatalf("SCP outcomes = %#v", detailed.Outcomes)
	}
}

func TestAcceptAssociationPartialPreservesRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := Listen(ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	cleanupClose(t, "listener", listener)

	serverErr := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(AcceptOptions{
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{verificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ImplicitVRLittleEndian},
		})
		if err != nil {
			serverErr <- err
			return
		}
		defer assoc.Close()
		if len(assoc.AcceptedContexts) != 1 {
			serverErr <- fmt.Errorf("server accepted = %#v", assoc.AcceptedContexts)
			return
		}
		rejected := assoc.RejectedPresentationContexts()
		if len(rejected) != 1 || rejected[0].Result != PresentationContextAbstractSyntaxNotSupported {
			serverErr <- fmt.Errorf("server rejected = %#v", rejected)
			return
		}
		serverErr <- nil
		<-ctx.Done()
	}()

	assoc, err := DialContext(ctx, listener.Addr().String(), DialOptions{
		Contexts: []PresentationContext{
			{AbstractSyntaxUID: verificationSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
			{AbstractSyntaxUID: ctImageStorageSOPClassUID, TransferSyntaxUIDs: []string{ImplicitVRLittleEndian}},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer assoc.Close()
	rejected := assoc.RejectedPresentationContexts()
	if len(rejected) != 1 || rejected[0].AbstractSyntaxUID != ctImageStorageSOPClassUID {
		t.Fatalf("client rejected = %#v", rejected)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestIndependentPeerPresentationContextRefusalsInterop(t *testing.T) {
	if os.Getenv("DICOMGO_INTEGRATION") != "1" {
		t.Skip("set DICOMGO_INTEGRATION=1 for live independent-peer interop; in-process coverage is TestDialIndependentPeerPreservesPartialPresentationContextRefusals")
	}
	TestDialIndependentPeerPreservesPartialPresentationContextRefusals(t)
	TestDialIndependentPeerTotalRejectionIsTyped(t)
}

func gotPHIProbe(value string) string {
	return strings.ToUpper(value)
}
