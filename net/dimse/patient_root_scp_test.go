package dimse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestServePatientRootCFindAcceptsAllLevels(t *testing.T) {
	for _, level := range []string{
		QueryRetrieveLevelPatient,
		QueryRetrieveLevelStudy,
		QueryRetrieveLevelSeries,
		QueryRetrieveLevelImage,
	} {
		t.Run(level, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			status, serverErr := runPatientRootCFindStatusTest(t, ctx, CFindHandlerFunc(func(_ context.Context, req CFindRequestContext) ([]*object.Object, error) {
				if req.Request.AffectedSOPClassUID != PatientRootFindSOPClassUID {
					t.Errorf("AffectedSOPClassUID = %q, want Patient Root", req.Request.AffectedSOPClassUID)
				}
				if req.QueryRetrieveLevel != level {
					t.Errorf("QueryRetrieveLevel = %q, want %q", req.QueryRetrieveLevel, level)
				}
				if req.Identifier == nil || req.PresentationContextID == 0 || req.IdentifierSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
					t.Errorf("request context = %#v", req)
				}
				return nil, nil
			}), patientRootFindIdentifier(t, level))
			if status != StatusSuccess {
				t.Fatalf("final status = 0x%04X, want success 0x%04X", status, StatusSuccess)
			}
			if serverErr != nil {
				t.Fatalf("ServePatientRootCFind() error = %v", serverErr)
			}
		})
	}
}

func TestServePatientRootCFindCanReturnCancelStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runPatientRootCFindStatusTest(t, ctx, CFindHandlerFunc(func(context.Context, CFindRequestContext) ([]*object.Object, error) {
		return nil, ErrCFindCanceled
	}), patientRootFindIdentifier(t, QueryRetrieveLevelPatient))
	if status != CFindStatusCancel {
		t.Fatalf("final status = 0x%04X, want cancel 0x%04X", status, CFindStatusCancel)
	}
	if !errors.Is(serverErr, ErrCFindCanceled) {
		t.Fatalf("server error = %v, want ErrCFindCanceled", serverErr)
	}
}

func TestServePatientRootCMoveAcceptsAllLevels(t *testing.T) {
	for _, level := range []string{
		QueryRetrieveLevelPatient,
		QueryRetrieveLevelStudy,
		QueryRetrieveLevelSeries,
		QueryRetrieveLevelImage,
	} {
		t.Run(level, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			status, serverErr := runPatientRootCMoveStatusTest(t, ctx, CMoveHandlerFunc(func(_ context.Context, req CMoveRequestContext) ([]CMoveSubOperation, error) {
				if req.Request.AffectedSOPClassUID != PatientRootMoveSOPClassUID {
					t.Errorf("AffectedSOPClassUID = %q, want Patient Root", req.Request.AffectedSOPClassUID)
				}
				if req.Request.MoveDestination != "STOREAE" {
					t.Errorf("MoveDestination = %q, want STOREAE", req.Request.MoveDestination)
				}
				if req.QueryRetrieveLevel != level {
					t.Errorf("QueryRetrieveLevel = %q, want %q", req.QueryRetrieveLevel, level)
				}
				return nil, nil
			}), patientRootFindIdentifier(t, level))
			if status != StatusSuccess {
				t.Fatalf("final status = 0x%04X, want success 0x%04X", status, StatusSuccess)
			}
			if serverErr != nil {
				t.Fatalf("ServePatientRootCMove() error = %v", serverErr)
			}
		})
	}
}

func TestServePatientRootCMoveReportsSubOperationCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	responses, serverErr := runPatientRootCMoveProgressTest(t, ctx, CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
		return []CMoveSubOperation{
			{
				AffectedSOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
				AffectedSOPInstanceUID: "1.2.3.4.1",
				Store: func(context.Context) CMoveSubOperationResult {
					return CMoveSubOperationResult{Status: StatusSuccess}
				},
			},
			{
				AffectedSOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
				AffectedSOPInstanceUID: "1.2.3.4.2",
				Store: func(context.Context) CMoveSubOperationResult {
					return CMoveSubOperationResult{Status: StatusCMoveUnableToProcess, Err: errors.New("store failed")}
				},
			},
		}, nil
	}))
	if serverErr != nil {
		t.Fatalf("ServePatientRootCMove() error = %v", serverErr)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want pending and final", len(responses))
	}
	assertCMoveCounts(t, "pending", responses[0], StatusPending, 1, 1, 0, 0)
	assertCMoveCounts(t, "final", responses[1], StatusCMoveSubOperationsCompleteOneOrMoreFailures, 0, 1, 1, 0)
}

func TestServePatientRootCGetAcceptsAllLevels(t *testing.T) {
	for _, level := range []string{
		QueryRetrieveLevelPatient,
		QueryRetrieveLevelStudy,
		QueryRetrieveLevelSeries,
		QueryRetrieveLevelImage,
	} {
		t.Run(level, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			final, serverErr := runPatientRootCGetStatusTest(t, ctx, CGetHandlerFunc(func(_ context.Context, req CGetRequestContext) ([]CGetSubOperation, error) {
				if req.Request.AffectedSOPClassUID != PatientRootGetSOPClassUID {
					t.Errorf("AffectedSOPClassUID = %q, want Patient Root", req.Request.AffectedSOPClassUID)
				}
				if req.QueryRetrieveLevel != level {
					t.Errorf("QueryRetrieveLevel = %q, want %q", req.QueryRetrieveLevel, level)
				}
				return nil, nil
			}), patientRootFindIdentifier(t, level), nil)
			if serverErr != nil {
				t.Fatalf("ServePatientRootCGet() error = %v", serverErr)
			}
			assertCGetCounts(t, "final", final, StatusSuccess, 0, 0, 0, 0)
		})
	}
}

func TestServePatientRootCGetMapsStoreFailureIntoFinalCounters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runPatientRootCGetStatusTest(
		t,
		ctx,
		CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			return []CGetSubOperation{
				cGetSubOperation("1.2.3.4.5"),
				cGetSubOperation("1.2.3.4.6"),
			}, nil
		}),
		patientRootFindIdentifier(t, QueryRetrieveLevelPatient),
		CGetStoreHandlerFunc(func(_ context.Context, req CGetStoreRequestContext) (uint16, error) {
			if req.Request.AffectedSOPInstanceUID == "1.2.3.4.6" {
				return StatusCGetUnableToProcess, nil
			}
			return StatusSuccess, nil
		}),
	)
	if serverErr != nil {
		t.Fatalf("ServePatientRootCGet() error = %v", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetSubOperationsCompleteOneOrMoreFailures, 0, 1, 1, 0)
}

func TestServeAssociationDispatchesPatientRootQRRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	handled := make(chan string, 3)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "PATQRSCP",
			Context: ctx,
			SupportedAbstractSyntaxes: []string{
				PatientRootFindSOPClassUID,
				PatientRootMoveSOPClassUID,
				PatientRootGetSOPClassUID,
				cGetTestStorageSOPClassUID,
			},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
			RoleSelections: []ul.RoleSelectionItem{
				{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true},
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)
		serverDone <- ServeAssociation(ctx, assoc, AssociationSCPOptions{
			CFindHandler: CFindHandlerFunc(func(context.Context, CFindRequestContext) ([]*object.Object, error) {
				handled <- "find"
				return nil, nil
			}),
			CMoveHandler: CMoveHandlerFunc(func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
				handled <- "move"
				return nil, nil
			}),
			CGetHandler: CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
				handled <- "get"
				return nil, nil
			}),
		})
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "PATQRSCP",
		CallingAETitle: "PATQRSCU",
		Contexts: []ul.PresentationContext{
			PatientRootFindPresentationContext(),
			PatientRootMovePresentationContext(),
			PatientRootGetPresentationContext(),
			{AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}},
		},
		RoleSelections: []ul.RoleSelectionItem{
			{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	findPC, err := AcceptedContextForSOPClass(assoc, PatientRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass(FIND) error = %v", err)
	}
	if err := SendCFindRequest(assoc, findPC.ID, CFindRequest{
		AffectedSOPClassUID: PatientRootFindSOPClassUID,
		MessageID:           1,
	}, patientRootFindIdentifier(t, QueryRetrieveLevelPatient)); err != nil {
		t.Fatalf("SendCFindRequest() error = %v", err)
	}
	findRsp, _, err := ReceiveCFindResponse(assoc, findPC.ID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveCFindResponse() error = %v", err)
	}
	if findRsp.Status != StatusSuccess {
		t.Fatalf("C-FIND status = 0x%04X, want success", findRsp.Status)
	}

	movePC, err := AcceptedContextForSOPClass(assoc, PatientRootMoveSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass(MOVE) error = %v", err)
	}
	moveRsp, err := SendCMove(ctx, assoc, movePC.ID, CMoveRequest{
		AffectedSOPClassUID: PatientRootMoveSOPClassUID,
		MessageID:           2,
		Priority:            PriorityMedium,
		MoveDestination:     "STOREAE",
	}, object.FromElements(patientRootFindIdentifier(t, QueryRetrieveLevelPatient), std.Dictionary), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMove() error = %v", err)
	}
	if moveRsp.Status != StatusSuccess {
		t.Fatalf("C-MOVE status = 0x%04X, want success", moveRsp.Status)
	}

	getPC, err := AcceptedContextForSOPClass(assoc, PatientRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass(GET) error = %v", err)
	}
	getRsp, err := SendCGet(ctx, assoc, getPC.ID, CGetRequest{
		AffectedSOPClassUID: PatientRootGetSOPClassUID,
		MessageID:           3,
		Priority:            PriorityMedium,
	}, object.FromElements(patientRootFindIdentifier(t, QueryRetrieveLevelPatient), std.Dictionary), transfer.ImplicitVRLittleEndian, CGetStoreHandlerFunc(func(context.Context, CGetStoreRequestContext) (uint16, error) {
		return StatusSuccess, nil
	}))
	if err != nil {
		t.Fatalf("SendCGet() error = %v", err)
	}
	if getRsp.Status != StatusSuccess {
		t.Fatalf("C-GET status = 0x%04X, want success", getRsp.Status)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("ServeAssociation() error = %v", err)
	}
	close(handled)
	got := map[string]bool{}
	for name := range handled {
		got[name] = true
	}
	for _, name := range []string{"find", "move", "get"} {
		if !got[name] {
			t.Fatalf("%s handler was not called", name)
		}
	}
}

func runPatientRootCFindStatusTest(t *testing.T, ctx context.Context, handler CFindHandler, identifier []core.Element) (uint16, error) {
	t.Helper()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "PATFINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{PatientRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, PatientRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServePatientRootCFind(ctx, assoc, pc.ID, handler)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "PATFINDSCP",
		CallingAETitle: "PATFINDSCU",
		Contexts:       []ul.PresentationContext{PatientRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, PatientRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	if err := SendCFindRequest(assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: PatientRootFindSOPClassUID,
		MessageID:           1,
	}, identifier); err != nil {
		t.Fatalf("SendCFindRequest() error = %v", err)
	}
	rsp, _, err := ReceiveCFindResponse(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveCFindResponse() error = %v", err)
	}
	return rsp.Status, <-serverDone
}

func runPatientRootCMoveStatusTest(t *testing.T, ctx context.Context, handler CMoveHandler, identifier []core.Element) (uint16, error) {
	t.Helper()

	responses, err := runPatientRootCMoveProgressTest(t, ctx, handler, withPatientRootCMoveIdentifier(identifier))
	if len(responses) == 0 {
		t.Fatalf("responses = 0, want final response")
	}
	return responses[len(responses)-1].Status, err
}

type patientRootCMoveProgressOption func(*patientRootCMoveProgressConfig)

type patientRootCMoveProgressConfig struct {
	identifier []core.Element
}

func withPatientRootCMoveIdentifier(identifier []core.Element) patientRootCMoveProgressOption {
	return func(cfg *patientRootCMoveProgressConfig) {
		cfg.identifier = identifier
	}
}

func runPatientRootCMoveProgressTest(t *testing.T, ctx context.Context, handler CMoveHandler, options ...patientRootCMoveProgressOption) ([]*CMoveResponse, error) {
	t.Helper()

	cfg := patientRootCMoveProgressConfig{
		identifier: patientRootFindIdentifier(t, QueryRetrieveLevelPatient),
	}
	for _, option := range options {
		option(&cfg)
	}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "PATMOVESCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{PatientRootMoveSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, PatientRootMoveSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServePatientRootCMove(ctx, assoc, pc.ID, handler)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "PATMOVESCP",
		CallingAETitle: "PATMOVESCU",
		Contexts:       []ul.PresentationContext{PatientRootMovePresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, PatientRootMoveSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	progress, err := SendCMoveWithProgress(ctx, assoc, pc.ID, CMoveRequest{
		AffectedSOPClassUID: PatientRootMoveSOPClassUID,
		MessageID:           1,
		Priority:            PriorityMedium,
		MoveDestination:     "STOREAE",
	}, object.FromElements(cfg.identifier, std.Dictionary), transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCMoveWithProgress() error = %v", err)
	}

	var responses []*CMoveResponse
	for event := range progress {
		if event.Err != nil {
			t.Fatalf("progress error = %v", event.Err)
		}
		responses = append(responses, event.Response)
	}
	return responses, <-serverDone
}

func runPatientRootCGetStatusTest(t *testing.T, ctx context.Context, handler CGetHandler, identifier []core.Element, storeHandler CGetStoreHandler) (*CGetResponse, error) {
	t.Helper()

	if storeHandler == nil {
		storeHandler = CGetStoreHandlerFunc(func(context.Context, CGetStoreRequestContext) (uint16, error) { return StatusSuccess, nil })
	}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "PATGETSCP",
			Context: ctx,
			SupportedAbstractSyntaxes: []string{
				PatientRootGetSOPClassUID,
				cGetTestStorageSOPClassUID,
			},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
			RoleSelections: []ul.RoleSelectionItem{
				{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true},
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, PatientRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServePatientRootCGet(ctx, assoc, pc.ID, handler)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "PATGETSCP",
		CallingAETitle: "PATGETSCU",
		Contexts: []ul.PresentationContext{
			PatientRootGetPresentationContext(),
			{AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}},
		},
		RoleSelections: []ul.RoleSelectionItem{
			{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true},
		},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, PatientRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	final, err := SendCGet(ctx, assoc, pc.ID, CGetRequest{
		AffectedSOPClassUID: PatientRootGetSOPClassUID,
		MessageID:           1,
		Priority:            PriorityMedium,
	}, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian, storeHandler)
	if err != nil {
		t.Fatalf("SendCGet() error = %v", err)
	}
	return final, <-serverDone
}

func patientRootFindIdentifier(t *testing.T, level string) []core.Element {
	t.Helper()

	var (
		identifier []core.Element
		err        error
	)
	switch level {
	case QueryRetrieveLevelPatient:
		identifier, err = BuildPatientRootPatientFindKeys(nil)
	case QueryRetrieveLevelStudy:
		identifier, err = BuildPatientRootStudyFindKeys(nil)
	case QueryRetrieveLevelSeries:
		identifier, err = BuildPatientRootSeriesFindKeys(nil)
	case QueryRetrieveLevelImage:
		identifier, err = BuildPatientRootImageFindKeys(nil)
	default:
		t.Fatalf("unsupported Patient Root test level %q", level)
	}
	if err != nil {
		t.Fatalf("BuildPatientRoot%sFindKeys() error = %v", level, err)
	}
	return identifier
}
