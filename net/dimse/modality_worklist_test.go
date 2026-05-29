package dimse

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomencoding "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestBuildModalityWorklistIdentifierPreservesAbsentEmptyAndNestedKeys(t *testing.T) {
	query := ModalityWorklistQuery{
		TimezoneOffsetFromUTC: "+0300",
		PatientID:             MWLMatch("P-1"),
		AccessionNumber:       MWLReturnKey(),
		RequestedProcedureID:  MWLMatch("PROC-1"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			ScheduledStationAETitle:          MWLMatch("CT_AE"),
			Modality:                         MWLReturnKey(),
			ScheduledProcedureStepStartDate:  MWLMatch("20260808-20260809"),
			ScheduledProcedureStepStartTime:  MWLMatch("080000-120000"),
			ScheduledPerformingPhysicianName: MWLReturnKey(),
		},
	}

	identifier, err := BuildModalityWorklistIdentifier(query)
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	if identifier.Has(core.NewTag(0x0010, 0x0010)) {
		t.Fatal("absent PatientName was emitted")
	}
	if timezone, ok := identifier.GetString(tagMWLTimezoneOffsetFromUTC); !ok || timezone != "+0300" {
		t.Fatalf("TimezoneOffsetFromUTC = %q, present %t", timezone, ok)
	}
	accession, ok := identifier.GetString(core.NewTag(0x0008, 0x0050))
	if !ok || accession != "" {
		t.Fatalf("AccessionNumber = %q, present %t; want present empty return key", accession, ok)
	}
	steps, ok := identifier.GetSequence(core.NewTag(0x0040, 0x0100))
	if !ok || len(steps) != 1 {
		t.Fatalf("ScheduledProcedureStepSequence items = %d, present %t; want one", len(steps), ok)
	}
	aeTitles, ok := steps[0].GetStrings(core.NewTag(0x0040, 0x0001))
	if !ok || len(aeTitles) != 1 || aeTitles[0] != "CT_AE" {
		t.Fatalf("ScheduledStationAETitle = %#v, present %t", aeTitles, ok)
	}
	modality, ok := steps[0].GetString(core.NewTag(0x0008, 0x0060))
	if !ok || modality != "" {
		t.Fatalf("Modality = %q, present %t; want present empty return key", modality, ok)
	}
}

func TestParseAndProjectModalityWorklistTimezoneOffset(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		TimezoneOffsetFromUTC: "+0300",
		PatientName:           MWLMatch("*"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			ScheduledProcedureStepStartTime: MWLReturnKey(),
		},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	if parsed.Query.TimezoneOffsetFromUTC != "+0300" {
		t.Fatalf("TimezoneOffsetFromUTC = %q", parsed.Query.TimezoneOffsetFromUTC)
	}
	projected, err := ProjectModalityWorklistResult(parsed, modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000"))
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult() error = %v", err)
	}
	if timezone, ok := projected.GetString(tagMWLTimezoneOffsetFromUTC); !ok || timezone != "+0300" {
		t.Fatalf("projected TimezoneOffsetFromUTC = %q, present %t", timezone, ok)
	}
	for _, invalid := range []string{"-0000", "+1460", "+1401", "0300"} {
		if _, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{TimezoneOffsetFromUTC: invalid, PatientName: MWLMatch("*")}); err == nil {
			t.Fatalf("invalid TimezoneOffsetFromUTC %q was accepted", invalid)
		}
	}
	if _, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		TimezoneOffsetFromUTC: "+0300",
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			ScheduledProcedureStepStartDate: MWLReturnKey(),
		},
	}); err != nil {
		t.Fatalf("TimezoneOffsetFromUTC with only a DA key error = %v", err)
	}
	if _, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		TimezoneOffsetFromUTC: "+0300",
		PatientName:           MWLMatch("*"),
	}); err == nil {
		t.Fatal("TimezoneOffsetFromUTC without a DA or TM key was accepted")
	}
}

func TestServeModalityWorklistCFindStreamsBeforeProviderCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: ModalityWorklistFindSOPClassUID,
		TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	allowSecond := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(_ context.Context, _ ModalityWorklistRequest, yield ModalityWorklistYield) error {
			if err := yield(modalityWorklistTestRecord("One^Patient", "CT_AE", "CT", "20260809", "093000")); err != nil {
				return err
			}
			<-allowSecond
			return yield(modalityWorklistTestRecord("Two^Patient", "CT_AE", "CT", "20260810", "103000"))
		}))
	}()

	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName: MWLMatch("*"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			Modality:                        MWLReturnKey(),
			ScheduledProcedureStepStartDate: MWLReturnKey(),
		},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	request := CFindRequest{AffectedSOPClassUID: ModalityWorklistFindSOPClassUID, MessageID: 7, Priority: PriorityMedium}
	if err := SendCommandSetWithContext(ctx, peer, 1, request.CommandSet()); err != nil {
		t.Fatalf("SendCommandSetWithContext() error = %v", err)
	}
	if err := SendDataSetWithContext(ctx, peer, 1, identifier, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSetWithContext() error = %v", err)
	}

	first, firstIdentifier, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("first receiveCFindResponseWithContext() error = %v", err)
	}
	if first.Status != StatusPending || firstIdentifier == nil {
		t.Fatalf("first response = %#v, identifier nil %t", first, firstIdentifier == nil)
	}
	if got, _ := firstIdentifier.GetString(tagMWLPatientName); got != "One^Patient" {
		t.Fatalf("first PatientName = %q", got)
	}
	close(allowSecond)

	second, secondIdentifier, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("second receiveCFindResponseWithContext() error = %v", err)
	}
	if second.Status != StatusPending || secondIdentifier == nil {
		t.Fatalf("second response = %#v, identifier nil %t", second, secondIdentifier == nil)
	}
	final, finalIdentifier, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("final receiveCFindResponseWithContext() error = %v", err)
	}
	if final.Status != StatusSuccess || finalIdentifier != nil {
		t.Fatalf("final response = %#v, identifier nil %t", final, finalIdentifier == nil)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeModalityWorklistCFind() error = %v", err)
	}
}

func TestServeModalityWorklistCFindHonorsCCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: ModalityWorklistFindSOPClassUID,
		TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	providerCanceled := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(providerCtx context.Context, _ ModalityWorklistRequest, yield ModalityWorklistYield) error {
			if err := yield(modalityWorklistTestRecord("One^Patient", "CT_AE", "CT", "20260809", "093000")); err != nil {
				return err
			}
			<-providerCtx.Done()
			close(providerCanceled)
			return providerCtx.Err()
		}))
	}()

	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLMatch("*")})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	request := CFindRequest{AffectedSOPClassUID: ModalityWorklistFindSOPClassUID, MessageID: 0, Priority: PriorityMedium}
	if err := SendCommandSetWithContext(ctx, peer, 1, request.CommandSet()); err != nil {
		t.Fatalf("SendCommandSetWithContext() error = %v", err)
	}
	if err := SendDataSetWithContext(ctx, peer, 1, identifier, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSetWithContext() error = %v", err)
	}
	first, _, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil || first.Status != StatusPending {
		t.Fatalf("first response = %#v, error = %v", first, err)
	}
	if err := SendCCancelRequest(peer, 1, CCancelRequest{MessageIDBeingRespondedTo: request.MessageID}); err != nil {
		t.Fatalf("SendCCancelRequest() error = %v", err)
	}
	final, finalIdentifier, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("final response error = %v", err)
	}
	if final.Status != CFindStatusCancel || finalIdentifier != nil {
		t.Fatalf("final response = %#v, identifier nil %t", final, finalIdentifier == nil)
	}
	select {
	case <-providerCanceled:
	case <-ctx.Done():
		t.Fatal("provider did not observe C-CANCEL")
	}
	if err := <-serverDone; !errors.Is(err, ErrCFindCanceled) {
		t.Fatalf("ServeModalityWorklistCFind() error = %v, want %v", err, ErrCFindCanceled)
	}
}

func TestServeModalityWorklistCFindReturnsNoMatchesWithoutMatchingKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: ModalityWorklistFindSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(context.Context, ModalityWorklistRequest, ModalityWorklistYield) error {
			t.Error("provider ran for an Identifier without matching keys")
			return nil
		}))
	}()
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLReturnKey()})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	request := CFindRequest{AffectedSOPClassUID: ModalityWorklistFindSOPClassUID, MessageID: 10}
	if err := SendCommandSetWithContext(ctx, peer, 1, request.CommandSet()); err != nil {
		t.Fatalf("SendCommandSetWithContext() error = %v", err)
	}
	if err := SendDataSetWithContext(ctx, peer, 1, identifier, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSetWithContext() error = %v", err)
	}
	final, finalIdentifier, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil || final.Status != StatusSuccess || finalIdentifier != nil {
		t.Fatalf("final response = %#v, identifier nil %t, error = %v", final, finalIdentifier == nil, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeModalityWorklistCFind() error = %v", err)
	}
}

func TestModalityWorklistClientCancelDrainsFinalAndKeepsAssociationReusable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: ModalityWorklistFindSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		firstErr := ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(providerCtx context.Context, _ ModalityWorklistRequest, yield ModalityWorklistYield) error {
			if err := yield(modalityWorklistTestRecord("First^Patient", "CT_AE", "CT", "20260809", "093000")); err != nil {
				return err
			}
			<-providerCtx.Done()
			return providerCtx.Err()
		}))
		if !errors.Is(firstErr, ErrCFindCanceled) {
			serverDone <- firstErr
			return
		}
		serverDone <- ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(_ context.Context, _ ModalityWorklistRequest, yield ModalityWorklistYield) error {
			return yield(modalityWorklistTestRecord("Second^Patient", "CT_AE", "CT", "20260810", "103000"))
		}))
	}()

	client, err := NewModalityWorklistClient(peer)
	if err != nil {
		t.Fatalf("NewModalityWorklistClient() error = %v", err)
	}
	query := ModalityWorklistQuery{PatientName: MWLMatch("*")}
	findCtx, findCancel := context.WithCancel(ctx)
	result, err := client.Find(findCtx, query, func(match *object.Object) error {
		if got, _ := match.GetString(tagMWLPatientName); got != "First^Patient" {
			t.Fatalf("first PatientName = %q", got)
		}
		findCancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first Find() error = %v, want context.Canceled", err)
	}
	if result.FinalResponse == nil || result.FinalResponse.Status != CFindStatusCancel || !result.CancelSent || result.MatchCount != 1 {
		t.Fatalf("first Find() result = %#v", result)
	}

	var secondName string
	result, err = client.Find(ctx, query, func(match *object.Object) error {
		secondName, _ = match.GetString(tagMWLPatientName)
		return nil
	})
	if err != nil {
		t.Fatalf("second Find() error = %v", err)
	}
	if secondName != "Second^Patient" || result.FinalResponse == nil || result.FinalResponse.Status != StatusSuccess {
		t.Fatalf("second Find() name = %q, result = %#v", secondName, result)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestModalityWorklistClientCancelsWhileWaitingForFirstResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: ModalityWorklistFindSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	defer peer.Close()
	defer local.Close()

	providerStarted := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(providerCtx context.Context, _ ModalityWorklistRequest, _ ModalityWorklistYield) error {
			close(providerStarted)
			<-providerCtx.Done()
			return providerCtx.Err()
		}))
	}()
	client, err := NewModalityWorklistClient(peer)
	if err != nil {
		t.Fatalf("NewModalityWorklistClient() error = %v", err)
	}
	findCtx, findCancel := context.WithCancel(ctx)
	go func() {
		<-providerStarted
		findCancel()
	}()
	result, err := client.Find(findCtx, ModalityWorklistQuery{PatientName: MWLMatch("*")}, func(*object.Object) error {
		t.Fatal("unexpected match")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Find() error = %v, want context.Canceled", err)
	}
	if !result.CancelSent || result.FinalResponse == nil || result.FinalResponse.Status != CFindStatusCancel {
		t.Fatalf("Find() result = %#v", result)
	}
	if err := <-serverDone; !errors.Is(err, ErrCFindCanceled) {
		t.Fatalf("ServeModalityWorklistCFind() error = %v", err)
	}
}

func TestInMemoryModalityWorklistHandlerMatchesIncrementallyInInputOrder(t *testing.T) {
	records := []*object.Object{
		modalityWorklistTestRecord("Doe^One", "CT_AE", "CT", "20260809", "093000"),
		modalityWorklistTestRecord("Other^Patient", "CT_AE", "CT", "20260809", "100000"),
		modalityWorklistTestRecord("Doe^Two", "MR_AE", "MR", "20260810", "103000"),
	}
	handler, err := NewInMemoryModalityWorklistHandler(records)
	if err != nil {
		t.Fatalf("NewInMemoryModalityWorklistHandler() error = %v", err)
	}
	records[0] = nil
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLMatch("DOE*")})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	var names []string
	err = handler.Find(context.Background(), ModalityWorklistRequest{Identifier: parsed}, func(candidate *object.Object) error {
		name, _ := candidate.GetString(tagMWLPatientName)
		names = append(names, name)
		return nil
	})
	if err != nil {
		t.Fatalf("handler.Find() error = %v", err)
	}
	if got := strings.Join(names, ","); got != "Doe^One,Doe^Two" {
		t.Fatalf("matched names = %q", got)
	}
}

func TestServeAssociationRoutesModalityWorklistByAbstractSyntax(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	router, err := NewModalityWorklistCFindRouter(nil, ModalityWorklistHandlerFunc(func(_ context.Context, _ ModalityWorklistRequest, yield ModalityWorklistYield) error {
		return yield(modalityWorklistTestRecord("Routed^Patient", "CT_AE", "CT", "20260809", "093000"))
	}), ModalityWorklistSCPOptions{})
	if err != nil {
		t.Fatalf("NewModalityWorklistCFindRouter() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "MWLSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{ModalityWorklistFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = assoc.Close() }()
		serverDone <- ServeAssociation(ctx, assoc, AssociationSCPOptions{CFindHandler: router})
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle: "MWLSCP", CallingAETitle: "MWLSCU",
		Contexts: []ul.PresentationContext{ModalityWorklistFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	client, err := NewModalityWorklistClient(assoc)
	if err != nil {
		t.Fatalf("NewModalityWorklistClient() error = %v", err)
	}
	var name string
	if _, err := client.Find(ctx, ModalityWorklistQuery{PatientName: MWLMatch("*")}, func(match *object.Object) error {
		name, _ = match.GetString(tagMWLPatientName)
		return nil
	}); err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if name != "Routed^Patient" {
		t.Fatalf("PatientName = %q", name)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
}

func TestModalityWorklistLimitsRejectOversizedIdentifiersAndResponses(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName:     MWLReturnKey(),
		AccessionNumber: MWLReturnKey(),
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	if err := validateModalityWorklistObjectLimits(identifier, 1, 4); !errors.Is(err, ErrModalityWorklistResourceLimit) {
		t.Fatalf("identifier limit error = %v, want %v", err, ErrModalityWorklistResourceLimit)
	}
	if err := preflightModalityWorklistResponse(identifier, transfer.ImplicitVRLittleEndian, 1, 16, 4); !errors.Is(err, ErrModalityWorklistResourceLimit) {
		t.Fatalf("response limit error = %v, want %v", err, ErrModalityWorklistResourceLimit)
	}
}

func TestModalityWorklistResponsePreflightClassifiesInvalidProviderOutput(t *testing.T) {
	invalid := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN},
		Value:  core.BulkDataValue{URI: "invalid-for-PN"},
	}}, std.Dictionary)
	err := preflightModalityWorklistResponse(invalid, transfer.ImplicitVRLittleEndian, 16, 1024, 4)
	if !errors.Is(err, ErrModalityWorklistProvider) || errors.Is(err, ErrModalityWorklistResourceLimit) {
		t.Fatalf("preflight error = %v, want provider failure", err)
	}
}

func TestModalityWorklistSCPOptionsUseFiniteDefaultsAndRejectNegativeLimits(t *testing.T) {
	options, err := (ModalityWorklistSCPOptions{}).normalized()
	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}
	if options.MaxMatches <= 0 || options.MaxIdentifierBytes <= 0 || options.MaxIdentifierElements <= 0 || options.MaxIdentifierDepth <= 0 ||
		options.MaxResponseBytes <= 0 || options.MaxResponseElements <= 0 || options.MaxResponseDepth <= 0 {
		t.Fatalf("normalized limits are not finite: %#v", options)
	}
	if _, err := (ModalityWorklistSCPOptions{MaxResponseBytes: -1}).normalized(); err == nil {
		t.Fatal("negative MaxResponseBytes accepted")
	}
}

func TestModalityWorklistClientOptionsUseFiniteDefaultsAndRejectNegativeLimits(t *testing.T) {
	options, err := (ModalityWorklistFindOptions{}).normalized()
	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}
	if options.MaxMatches <= 0 || options.CancelDrainTimeout <= 0 || options.MaxResponseBytes <= 0 ||
		options.MaxResponseElements <= 0 || options.MaxResponseDepth <= 0 {
		t.Fatalf("normalized limits are not finite: %#v", options)
	}
	if _, err := (ModalityWorklistFindOptions{MaxResponseElements: -1}).normalized(); err == nil {
		t.Fatal("negative MaxResponseElements accepted")
	}
}

func TestModalityWorklistCallbackErrorsAndPanicsAreRedacted(t *testing.T) {
	sensitive := errors.New("PATIENT^NAME")
	err := callModalityWorklistCallback(func(*object.Object) error { return sensitive }, nil)
	if !errors.Is(err, ErrModalityWorklistCallback) || !errors.Is(err, sensitive) || strings.Contains(err.Error(), "PATIENT") {
		t.Fatalf("callback error = %v", err)
	}
	err = callModalityWorklistCallback(func(*object.Object) error { panic("PATIENT^NAME") }, nil)
	if !errors.Is(err, ErrModalityWorklistCallback) || strings.Contains(err.Error(), "PATIENT") {
		t.Fatalf("callback panic error = %v", err)
	}
}

func TestModalityWorklistProviderErrorsDoNotEnterWireComment(t *testing.T) {
	status, comment := modalityWorklistSCPStatus(errors.New("PATIENT^NAME"))
	if status != CFindStatusUnableToProcess || strings.Contains(comment, "PATIENT") {
		t.Fatalf("status/comment = 0x%04X %q", status, comment)
	}
}

func TestParseAndProjectModalityWorklistIdentifierUsesOnlyRequestedAttributes(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName:     MWLReturnKey(),
		AccessionNumber: MWLReturnKey(),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			Modality:                        MWLReturnKey(),
			ScheduledProcedureStepStartDate: MWLReturnKey(),
		},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLPatientID, VR: core.VRLO}, Value: core.StringValue{"PRIVATE-ID"}})
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLAccessionNumber, VR: core.VRSH}, Value: core.StringValue{"ACC-1"}})

	projected, err := ProjectModalityWorklistResult(parsed, candidate)
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult() error = %v", err)
	}
	if projected.Has(tagMWLPatientID) {
		t.Fatal("unrequested PatientID was returned")
	}
	if got, _ := projected.GetString(tagMWLPatientName); got != "Doe^Jane" {
		t.Fatalf("PatientName = %q", got)
	}
	steps, ok := projected.GetSequence(tagMWLScheduledProcedureStepSequence)
	if !ok || len(steps) != 1 {
		t.Fatalf("projected ScheduledProcedureStepSequence = %d item(s), present %t", len(steps), ok)
	}
	if got, _ := steps[0].GetString(tagMWLModality); got != "CT" {
		t.Fatalf("projected Modality = %q", got)
	}
	if steps[0].Has(tagMWLScheduledStationAETitle) || steps[0].Has(tagMWLScheduledProcedureStepStartTime) {
		t.Fatal("unrequested nested attributes were returned")
	}
}

func TestProjectModalityWorklistUniversalStepReturnsCompleteSelectedItem(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName:            MWLMatch("*"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	projected, err := ProjectModalityWorklistResult(parsed, modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000"))
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult() error = %v", err)
	}
	steps, ok := projected.GetSequence(tagMWLScheduledProcedureStepSequence)
	if !ok || len(steps) != 1 {
		t.Fatalf("ScheduledProcedureStepSequence = %d item(s), present %t", len(steps), ok)
	}
	for _, tag := range []core.Tag{tagMWLScheduledStationAETitle, tagMWLModality, tagMWLScheduledProcedureStepStartDate, tagMWLScheduledProcedureStepStartTime} {
		if !steps[0].Has(tag) {
			t.Fatalf("universal Scheduled Procedure Step omitted %s", tag)
		}
	}
}

func TestProjectModalityWorklistResultRejectsInvalidTypeOneReturnKeys(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName: MWLReturnKey(),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			Modality: MWLReturnKey(),
		},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*object.Object)
	}{
		{name: "empty patient name", mutate: func(candidate *object.Object) {
			candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.StringValue{""}})
		}},
		{name: "wrong modality VR", mutate: func(candidate *object.Object) {
			steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
			elements := steps[0].Elements()
			for i := range elements {
				if elements[i].Tag() == tagMWLModality {
					elements[i].Header.VR = core.VRLO
				}
			}
			candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: elements}}}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
			test.mutate(candidate)
			if _, err := ProjectModalityWorklistResult(parsed, candidate); err == nil {
				t.Fatal("ProjectModalityWorklistResult() error = nil")
			}
		})
	}
}

func TestProjectModalityWorklistResultHonorsLimitsBeforeCloning(t *testing.T) {
	identifier := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.StringValue{"*"}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			{Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{}},
		}}}}},
	}, std.Dictionary)
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
	stepElements := steps[0].Elements()
	stepElements = append(stepElements, core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"CODE"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0102), VR: core.VRSH}, Value: core.StringValue{"99TEST"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0104), VR: core.VRLO}, Value: core.StringValue{strings.Repeat("X", 64)}},
		}}}},
	})
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}}})
	if _, err := projectModalityWorklistResultWithLimits(parsed, candidate, 32, 16, 8); !errors.Is(err, ErrModalityWorklistResourceLimit) {
		t.Fatalf("projectModalityWorklistResultWithLimits() error = %v, want %v", err, ErrModalityWorklistResourceLimit)
	}
}

func TestProjectModalityWorklistResultRejectsWideSequencesBeforeAllocating(t *testing.T) {
	identifier := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.StringValue{"*"}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			{Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{}},
		}}}}},
	}, std.Dictionary)
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
	stepElements := steps[0].Elements()
	items := make([]core.DataSet, 64)
	for i := range items {
		items[i] = core.DataSet{Elements: []core.Element{{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"CODE"}}}}
	}
	stepElements = append(stepElements, core.Element{Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: items}})
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}}})
	if _, err := projectModalityWorklistResultWithLimits(parsed, candidate, 8, 1024, 8); !errors.Is(err, ErrModalityWorklistResourceLimit) {
		t.Fatalf("wide sequence error = %v, want %v", err, ErrModalityWorklistResourceLimit)
	}
}

func TestProjectModalityWorklistUniversalStepChecksBudgetBeforeCodeSemantics(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName:            MWLMatch("*"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
	stepElements := steps[0].Elements()
	items := make([]core.DataSet, 64)
	for i := range items {
		items[i] = core.DataSet{Elements: []core.Element{{
			Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH},
			Value:  core.StringValue{"INCOMPLETE"},
		}}}
	}
	stepElements = append(stepElements, core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	})
	candidate.Put(core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
	})
	if _, err := projectModalityWorklistResultWithLimits(parsed, candidate, 8, 1024, 8); !errors.Is(err, ErrModalityWorklistResourceLimit) {
		t.Fatalf("universal wide sequence error = %v, want %v", err, ErrModalityWorklistResourceLimit)
	}
}

func TestProjectModalityWorklistResultRejectsMissingSupportedReturnKey(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName:     MWLMatch("*"),
		AccessionNumber: MWLReturnKey(),
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	candidate := modalityWorklistTestRecord("Doe^Missing", "CT_AE", "CT", "20260809", "093000")
	if _, err := ProjectModalityWorklistResult(parsed, candidate); err == nil {
		t.Fatal("missing supported return key was accepted")
	}
}

func TestUnsupportedOptionalMWLKeyIsReturnOnlyAndSelectsPendingWarningWhenAbsent(t *testing.T) {
	identifier := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.StringValue{"*"}},
		{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: tagMWLModality, VR: core.VRCS}, Value: core.StringValue{""}},
				{Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{}},
			}}}},
		},
	}, std.Dictionary)
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	if got := parsed.UnsupportedOptionalKeys; len(got) != 1 || got[0] != tagMWLScheduledProtocolCodeSequence {
		t.Fatalf("UnsupportedOptionalKeys = %v", got)
	}
	candidate := modalityWorklistTestRecord("Doe^Optional", "CT_AE", "CT", "20260809", "093000")
	if got := modalityWorklistPendingStatus(parsed, candidate); got != StatusPendingWarning {
		t.Fatalf("pending status without optional return key = 0x%04X", got)
	}
	projected, err := ProjectModalityWorklistResult(parsed, candidate)
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult(without optional key) error = %v", err)
	}
	projectedSteps, _ := projected.GetSequence(tagMWLScheduledProcedureStepSequence)
	if len(projectedSteps) != 1 || projectedSteps[0].Has(tagMWLScheduledProtocolCodeSequence) {
		t.Fatal("unavailable optional return key was projected")
	}
	steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
	stepElements := steps[0].Elements()
	stepElements = append(stepElements, core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"SYNTHETIC"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0102), VR: core.VRSH}, Value: core.StringValue{"99TEST"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0104), VR: core.VRLO}, Value: core.StringValue{"Synthetic protocol"}},
		}}}},
	})
	candidate.Put(core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
	})
	if got := modalityWorklistPendingStatus(parsed, candidate); got != StatusPending {
		t.Fatalf("pending status with optional return key = 0x%04X", got)
	}
	projected, err = ProjectModalityWorklistResult(parsed, candidate)
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult() error = %v", err)
	}
	projectedSteps, _ = projected.GetSequence(tagMWLScheduledProcedureStepSequence)
	if len(projectedSteps) != 1 || !projectedSteps[0].Has(tagMWLScheduledProtocolCodeSequence) {
		t.Fatal("optional return key was not projected")
	}
	stepElements[len(stepElements)-1].Value = core.SequenceValue{}
	candidate.Put(core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
	})
	if _, err := ProjectModalityWorklistResult(parsed, candidate); !errors.Is(err, ErrModalityWorklistProvider) {
		t.Fatalf("empty ScheduledProtocolCodeSequence error = %v, want %v", err, ErrModalityWorklistProvider)
	}
	stepElements[len(stepElements)-1].Value = core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"INCOMPLETE"}},
	}}}}
	candidate.Put(core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
	})
	if _, err := ProjectModalityWorklistResult(parsed, candidate); !errors.Is(err, ErrModalityWorklistProvider) {
		t.Fatalf("incomplete ScheduledProtocolCodeSequence error = %v, want %v", err, ErrModalityWorklistProvider)
	}
	for _, itemElements := range [][]core.Element{
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0120), VR: core.VRUR}, Value: core.StringValue{"urn:oid:1.2.3.4"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"SYNTHETIC"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0102), VR: core.VRSH}, Value: core.StringValue{"99TEST"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"SYNTHETIC"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0102), VR: core.VRSH}, Value: core.StringValue{"99TEST"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0103), VR: core.VRSH}, Value: core.StringValue{"2026"}},
		},
	} {
		stepElements[len(stepElements)-1].Value = core.SequenceValue{Items: []core.DataSet{{Elements: itemElements}}}
		candidate.Put(core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
		})
		if _, err := ProjectModalityWorklistResult(parsed, candidate); err != nil {
			t.Fatalf("valid optional-meaning ScheduledProtocolCodeSequence error = %v", err)
		}
	}
	for _, itemElements := range [][]core.Element{
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0119), VR: core.VRUC}, Value: core.StringValue{"SHORT"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0102), VR: core.VRSH}, Value: core.StringValue{"99TEST"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0100), VR: core.VRSH}, Value: core.StringValue{"urn:oid:1.2.3"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0102), VR: core.VRSH}, Value: core.StringValue{"99TEST"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0120), VR: core.VRUR}, Value: core.StringValue{"NOT_A_URI"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0120), VR: core.VRUR}, Value: core.StringValue{"urn:"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0120), VR: core.VRUR}, Value: core.StringValue{"https:"}},
		},
		{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0120), VR: core.VRUR}, Value: core.StringValue{"urn:oid:1.2.3"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0103), VR: core.VRSH}, Value: core.StringValue{"2026"}},
		},
	} {
		stepElements[len(stepElements)-1].Value = core.SequenceValue{Items: []core.DataSet{{Elements: itemElements}}}
		candidate.Put(core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
		})
		if _, err := ProjectModalityWorklistResult(parsed, candidate); !errors.Is(err, ErrModalityWorklistProvider) {
			t.Fatalf("invalid coded-entry value form error = %v, want %v", err, ErrModalityWorklistProvider)
		}
	}
}

func TestProjectModalityWorklistResultEmitsConditionalCharacterSetAndTimezoneOnlyWhenNeeded(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLMatch("*")})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	candidate := modalityWorklistTestRecord("Doe^ASCII", "CT_AE", "CT", "20260809", "093000")
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{"ISO_IR 192"}})
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLTimezoneOffsetFromUTC, VR: core.VRSH}, Value: core.StringValue{"+0300"}})
	projected, err := ProjectModalityWorklistResult(parsed, candidate)
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult(ASCII) error = %v", err)
	}
	if projected.Has(tagMWLSpecificCharacterSet) || projected.Has(tagMWLTimezoneOffsetFromUTC) {
		t.Fatal("conditional character set/timezone were emitted without a qualifying response value")
	}
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.StringValue{"José^Teste"}})
	projected, err = ProjectModalityWorklistResult(parsed, candidate)
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult(non-ASCII) error = %v", err)
	}
	if !projected.Has(tagMWLSpecificCharacterSet) {
		t.Fatal("SpecificCharacterSet was omitted for non-default repertoire")
	}
	dateIdentifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{
		PatientName: MWLMatch("*"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			ScheduledProcedureStepStartDate: MWLReturnKey(),
		},
	})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier(date only) error = %v", err)
	}
	dateParsed, err := ParseModalityWorklistIdentifier(dateIdentifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier(date only) error = %v", err)
	}
	projected, err = ProjectModalityWorklistResult(dateParsed, candidate)
	if err != nil {
		t.Fatalf("ProjectModalityWorklistResult(date only) error = %v", err)
	}
	if projected.Has(tagMWLTimezoneOffsetFromUTC) {
		t.Fatal("TimezoneOffsetFromUTC was emitted for a DA-only response")
	}
}

func TestProjectModalityWorklistResultValidatesRawTextWithCandidateCharacterSet(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLReturnKey()})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	parsed, err := ParseModalityWorklistIdentifier(identifier)
	if err != nil {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
	}
	tests := []struct {
		name          string
		characterSet  string
		encodedPerson []byte
	}{
		{name: "latin 1", characterSet: "ISO_IR 100", encodedPerson: []byte{'J', 'o', 's', 0xe9}},
		{name: "iso 2022 seven bit", characterSet: "ISO 2022 IR 87"},
	}
	charset, err := dicomencoding.ParseCharacterSet(tests[1].characterSet)
	if err != nil {
		t.Fatalf("ParseCharacterSet() error = %v", err)
	}
	tests[1].encodedPerson, err = charset.EncodePersonName("山田^太郎")
	if err != nil {
		t.Fatalf("EncodePersonName() error = %v", err)
	}
	if !strings.ContainsRune(string(tests[1].encodedPerson), '\x1b') {
		t.Fatalf("ISO 2022 fixture = % X, want escape sequence", tests[1].encodedPerson)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := object.FromElements([]core.Element{
				{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{test.characterSet}},
				{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.RawValue(test.encodedPerson)},
			}, std.Dictionary)
			projected, err := ProjectModalityWorklistResult(parsed, candidate)
			if err != nil {
				t.Fatalf("ProjectModalityWorklistResult() error = %v", err)
			}
			if !projected.Has(tagMWLSpecificCharacterSet) {
				t.Fatal("SpecificCharacterSet was omitted for encoded non-default repertoire")
			}
			if got, ok := projected.GetString(tagMWLPatientName); !ok || got == "" {
				t.Fatalf("projected PatientName = %q, present %t", got, ok)
			}
		})
	}
}

func TestProjectModalityWorklistResultEnforcesConditionalDescriptions(t *testing.T) {
	t.Run("requested procedure description", func(t *testing.T) {
		identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{RequestedProcedureDescription: MWLReturnKey()})
		if err != nil {
			t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
		}
		parsed, err := ParseModalityWorklistIdentifier(identifier)
		if err != nil {
			t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
		}
		candidate := object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: tagMWLRequestedProcedureDescription, VR: core.VRLO},
			Value:  core.StringValue{""},
		}}, std.Dictionary)
		if _, err := ProjectModalityWorklistResult(parsed, candidate); !errors.Is(err, ErrModalityWorklistProvider) {
			t.Fatalf("empty RequestedProcedureDescription error = %v, want %v", err, ErrModalityWorklistProvider)
		}
	})

	t.Run("scheduled step description alternative", func(t *testing.T) {
		identifier := object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepDescription, VR: core.VRLO}, Value: core.StringValue{""}},
				{Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ}, Value: core.SequenceValue{}},
			}}}},
		}}, std.Dictionary)
		parsed, err := ParseModalityWorklistIdentifier(identifier)
		if err != nil {
			t.Fatalf("ParseModalityWorklistIdentifier() error = %v", err)
		}
		candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
		steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
		stepElements := steps[0].Elements()
		for i := range stepElements {
			if stepElements[i].Tag() == tagMWLScheduledProcedureStepDescription {
				stepElements[i].Value = core.StringValue{""}
			}
		}
		candidate.Put(core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
		})
		if _, err := ProjectModalityWorklistResult(parsed, candidate); !errors.Is(err, ErrModalityWorklistProvider) {
			t.Fatalf("empty description without protocol error = %v, want %v", err, ErrModalityWorklistProvider)
		}
		stepElements = append(stepElements, core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProtocolCodeSequence, VR: core.VRSQ},
			Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
				{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0120), VR: core.VRUR}, Value: core.StringValue{"urn:oid:1.2.3.4"}},
			}}}},
		})
		candidate.Put(core.Element{
			Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
			Value:  core.SequenceValue{Items: []core.DataSet{{Elements: stepElements}}},
		})
		if _, err := ProjectModalityWorklistResult(parsed, candidate); err != nil {
			t.Fatalf("valid protocol alternative error = %v", err)
		}
	})
}

func TestValidateModalityWorklistResponseIdentifierCardinality(t *testing.T) {
	match := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	if err := validateModalityWorklistResponseIdentifier(CFindStatusPending, nil); err == nil {
		t.Fatal("pending response without Identifier was accepted")
	}
	if err := validateModalityWorklistResponseIdentifier(CFindStatusSuccess, match); err == nil {
		t.Fatal("final response with Identifier was accepted")
	}
	if err := validateModalityWorklistResponseIdentifier(CFindStatusPending, match); err != nil {
		t.Fatalf("valid pending response error = %v", err)
	}
}

func TestParseModalityWorklistIdentifierRejectsOutOfModelKey(t *testing.T) {
	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLReturnKey()})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	identifier.Put(core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS},
		Value:  core.StringValue{"STUDY"},
	})
	if _, err := ParseModalityWorklistIdentifier(identifier); !errors.Is(err, ErrModalityWorklistIdentifier) {
		t.Fatalf("ParseModalityWorklistIdentifier() error = %v, want %v", err, ErrModalityWorklistIdentifier)
	}
}

func TestBuildModalityWorklistIdentifierRejectsInvalidMatchingForms(t *testing.T) {
	tests := []struct {
		name  string
		query ModalityWorklistQuery
		want  string
	}{
		{name: "patient id wildcard", query: ModalityWorklistQuery{PatientID: MWLMatch("P*")}, want: "wildcard"},
		{name: "station ae list", query: ModalityWorklistQuery{ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{ScheduledStationAETitle: MWLMatch("A", "B")}}, want: "VM 1"},
		{name: "invalid date range", query: ModalityWorklistQuery{ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{ScheduledProcedureStepStartDate: MWLMatch("20260230-")}}, want: "DA range"},
		{name: "invalid time range", query: ModalityWorklistQuery{ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{ScheduledProcedureStepStartTime: MWLMatch("246000-")}}, want: "TM range"},
		{name: "invalid character set", query: ModalityWorklistQuery{SpecificCharacterSet: []string{"UNKNOWN"}, PatientName: MWLMatch("*")}, want: "Specific Character Set"},
		{name: "oversized patient id", query: ModalityWorklistQuery{PatientID: MWLMatch(strings.Repeat("X", 65))}, want: "invalid value"},
		{name: "lowercase modality", query: ModalityWorklistQuery{ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{Modality: MWLMatch("ct")}}, want: "invalid value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildModalityWorklistIdentifier(test.query)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildModalityWorklistIdentifier() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestMatchModalityWorklistSupportsWildcardUniversalAndTemporalRanges(t *testing.T) {
	query := ModalityWorklistQuery{
		PatientName:     MWLMatch("DOE*"),
		AccessionNumber: MWLReturnKey(),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			ScheduledStationAETitle:         MWLMatch("CT_AE"),
			Modality:                        MWLMatch("CT"),
			ScheduledProcedureStepStartDate: MWLMatch("20260808-20260810"),
			ScheduledProcedureStepStartTime: MWLMatch("080000-120000"),
		},
	}
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")

	matched, err := MatchModalityWorklist(query, candidate)
	if err != nil {
		t.Fatalf("MatchModalityWorklist() error = %v", err)
	}
	if !matched {
		t.Fatal("MatchModalityWorklist() = false, want true")
	}

	outsideRange := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260811", "093000")
	matched, err = MatchModalityWorklist(query, outsideRange)
	if err != nil {
		t.Fatalf("MatchModalityWorklist(outside range) error = %v", err)
	}
	if matched {
		t.Fatal("MatchModalityWorklist(outside range) = true, want false")
	}
}

func TestMatchModalityWorklistIsCaseSensitiveOutsidePersonNames(t *testing.T) {
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	matched, err := MatchModalityWorklist(ModalityWorklistQuery{
		PatientName: MWLMatch("DOE^JANE"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			Modality: MWLMatch("ct"),
		},
	}, candidate)
	if err != nil {
		t.Fatalf("MatchModalityWorklist() error = %v", err)
	}
	if matched {
		t.Fatal("lowercase CS matched uppercase candidate")
	}
}

func TestMatchModalityWorklistRejectsMultipleScheduledProcedureStepItems(t *testing.T) {
	candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "093000")
	steps, _ := candidate.GetSequence(tagMWLScheduledProcedureStepSequence)
	candidate.Put(core.Element{
		Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ},
		Value: core.SequenceValue{Items: []core.DataSet{
			{Elements: steps[0].Elements()},
			{Elements: steps[0].Elements()},
		}},
	})
	if _, err := MatchModalityWorklist(ModalityWorklistQuery{
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{Modality: MWLMatch("CT")},
	}, candidate); err == nil {
		t.Fatal("multiple Scheduled Procedure Step items were accepted")
	}
}

func TestMatchModalityWorklistCombinesDateAndTimeRangesContinuously(t *testing.T) {
	query := ModalityWorklistQuery{ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
		ScheduledProcedureStepStartDate: MWLMatch("20260808-20260810"),
		ScheduledProcedureStepStartTime: MWLMatch("080000-120000"),
	}}
	tests := []struct {
		name    string
		date    string
		time    string
		matched bool
	}{
		{name: "before combined start", date: "20260808", time: "075959", matched: false},
		{name: "middle day outside daily clock range", date: "20260809", time: "150000", matched: true},
		{name: "after combined end", date: "20260810", time: "120001", matched: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", test.date, test.time)
			matched, err := MatchModalityWorklist(query, candidate)
			if err != nil {
				t.Fatalf("MatchModalityWorklist() error = %v", err)
			}
			if matched != test.matched {
				t.Fatalf("MatchModalityWorklist() = %t, want %t", matched, test.matched)
			}
		})
	}
}

func TestMatchModalityWorklistCombinesDateAndTimeOnlyWhenBothAreRanges(t *testing.T) {
	query := ModalityWorklistQuery{ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
		ScheduledProcedureStepStartDate: MWLMatch("20260808-20260810"),
		ScheduledProcedureStepStartTime: MWLMatch("080000"),
	}}
	matched, err := MatchModalityWorklist(query, modalityWorklistTestRecord("Doe^Jane", "CT_AE", "CT", "20260809", "150000"))
	if err != nil {
		t.Fatalf("MatchModalityWorklist() error = %v", err)
	}
	if matched {
		t.Fatal("single Time value was treated as a combined continuous range")
	}
}

func TestServeModalityWorklistCFindSerializesConcurrentYieldAndRejectsLateYield(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: ModalityWorklistFindSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	defer peer.Close()
	defer local.Close()

	lateTrigger := make(chan struct{})
	lateResult := make(chan error, 1)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeModalityWorklistCFind(ctx, local, 1, ModalityWorklistHandlerFunc(func(_ context.Context, _ ModalityWorklistRequest, yield ModalityWorklistYield) error {
			var wait sync.WaitGroup
			errs := make(chan error, 2)
			for _, name := range []string{"One^Patient", "Two^Patient"} {
				name := name
				wait.Add(1)
				go func() {
					defer wait.Done()
					errs <- yield(modalityWorklistTestRecord(name, "CT_AE", "CT", "20260809", "093000"))
				}()
			}
			wait.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					return err
				}
			}
			go func() {
				<-lateTrigger
				lateResult <- yield(modalityWorklistTestRecord("Late^Patient", "CT_AE", "CT", "20260809", "093000"))
			}()
			return nil
		}))
	}()

	identifier, err := BuildModalityWorklistIdentifier(ModalityWorklistQuery{PatientName: MWLMatch("*")})
	if err != nil {
		t.Fatalf("BuildModalityWorklistIdentifier() error = %v", err)
	}
	request := CFindRequest{AffectedSOPClassUID: ModalityWorklistFindSOPClassUID, MessageID: 11}
	if err := SendCommandSetWithContext(ctx, peer, 1, request.CommandSet()); err != nil {
		t.Fatalf("SendCommandSetWithContext() error = %v", err)
	}
	if err := SendDataSetWithContext(ctx, peer, 1, identifier, transfer.ImplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSetWithContext() error = %v", err)
	}
	for i := 0; i < 2; i++ {
		response, match, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
		if err != nil || response.Status != StatusPending || match == nil {
			t.Fatalf("pending response %d = %#v, match nil %t, error %v", i, response, match == nil, err)
		}
	}
	final, match, err := receiveCFindResponseWithContext(ctx, peer, 1, transfer.ImplicitVRLittleEndian)
	if err != nil || final.Status != StatusSuccess || match != nil {
		t.Fatalf("final response = %#v, match nil %t, error %v", final, match == nil, err)
	}
	close(lateTrigger)
	if err := <-lateResult; !errors.Is(err, ErrModalityWorklistProvider) {
		t.Fatalf("late yield error = %v, want %v", err, ErrModalityWorklistProvider)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeModalityWorklistCFind() error = %v", err)
	}
}

func modalityWorklistTestRecord(patientName, stationAE, modality, date, timeValue string) *object.Object {
	step := core.DataSet{Elements: []core.Element{
		{Header: core.ElementHeader{Tag: tagMWLScheduledStationAETitle, VR: core.VRAE}, Value: core.StringValue{stationAE}},
		{Header: core.ElementHeader{Tag: tagMWLModality, VR: core.VRCS}, Value: core.StringValue{modality}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepStartDate, VR: core.VRDA}, Value: core.StringValue{date}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepStartTime, VR: core.VRTM}, Value: core.StringValue{timeValue}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledPerformingPhysicianName, VR: core.VRPN}, Value: core.StringValue{""}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepDescription, VR: core.VRLO}, Value: core.StringValue{"SYNTHETIC"}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepID, VR: core.VRSH}, Value: core.StringValue{"STEP-1"}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledStationName, VR: core.VRSH}, Value: core.StringValue{""}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepLocation, VR: core.VRSH}, Value: core.StringValue{""}},
	}}
	return object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagMWLPatientName, VR: core.VRPN}, Value: core.StringValue{patientName}},
		{Header: core.ElementHeader{Tag: tagMWLScheduledProcedureStepSequence, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{step}}},
	}, std.Dictionary)
}
