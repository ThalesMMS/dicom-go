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

func TestServeStudyRootCGetReturnsSuccessForNoMatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(t, ctx, CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
		return nil, nil
	}))
	if serverErr != nil {
		t.Fatalf("ServeStudyRootCGet() error = %v, want nil", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusSuccess, 0, 0, 0, 0)
}

func TestServeStudyRootCGetReportsHandlerErrorStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	handlerErr := errors.New("lookup failed")
	final, serverErr := runStudyRootCGetStatusTest(t, ctx, CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
		return nil, handlerErr
	}))
	if !errors.Is(serverErr, handlerErr) {
		t.Fatalf("ServeStudyRootCGet() error = %v, want handler error", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetUnableToProcess, 0, 0, 0, 0)
}

func TestServeStudyRootCGetReportsCustomHandlerStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(t, ctx, CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
		return nil, NewCGetSCPError(StatusCGetCancel, "caller canceled", nil)
	}))
	var statusErr *CGetSCPError
	if !errors.As(serverErr, &statusErr) || statusErr.Status != StatusCGetCancel {
		t.Fatalf("ServeStudyRootCGet() error = %v, want CGetSCPError cancel", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetCancel, 0, 0, 0, 0)
}

func TestServeStudyRootCGetRejectsPatientLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(
		t,
		ctx,
		CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			t.Fatal("handler should not be called for invalid QueryRetrieveLevel")
			return nil, nil
		}),
		withCGetIdentifier([]core.Element{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"PATIENT"}},
		}),
	)
	if serverErr == nil {
		t.Fatal("ServeStudyRootCGet() error = nil, want validation error")
	}
	assertCGetCounts(t, "final", final, StatusCGetUnableToProcess, 0, 0, 0, 0)
}

func TestServeStudyRootCGetMapsStoreFailureIntoFinalCounters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(
		t,
		ctx,
		CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			return []CGetSubOperation{
				cGetSubOperation("1.2.3.4.5"),
				cGetSubOperation("1.2.3.4.6"),
			}, nil
		}),
		withCGetStoreHandler(CGetStoreHandlerFunc(func(_ context.Context, req CGetStoreRequestContext) (uint16, error) {
			if req.Request.AffectedSOPInstanceUID == "1.2.3.4.6" {
				return StatusCGetUnableToProcess, nil
			}
			return StatusSuccess, nil
		})),
	)
	if serverErr != nil {
		t.Fatalf("ServeStudyRootCGet() error = %v, want nil", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetSubOperationsCompleteOneOrMoreFailures, 0, 1, 1, 0)
	if got := final.FailedSOPInstanceUIDListOrNil; len(got) != 1 || got[0] != "1.2.3.4.6" {
		t.Fatalf("final FailedSOPInstanceUIDListOrNil = %v, want [1.2.3.4.6]", got)
	}
}

func TestServeStudyRootCGetMapsMissingStorageRoleIntoFailedCounter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(
		t,
		ctx,
		CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			return []CGetSubOperation{cGetSubOperation("1.2.3.4.5")}, nil
		}),
		withoutCGetStorageRole(),
	)
	if serverErr != nil {
		t.Fatalf("ServeStudyRootCGet() error = %v, want graceful failed sub-operation", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetSubOperationsCompleteOneOrMoreFailures, 0, 0, 1, 0)
	if got := final.FailedSOPInstanceUIDListOrNil; len(got) != 1 || got[0] != "1.2.3.4.5" {
		t.Fatalf("final FailedSOPInstanceUIDListOrNil = %v, want [1.2.3.4.5]", got)
	}
}

func TestServeStudyRootCGetMapsMissingStorageContextIntoFailedCounter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(
		t,
		ctx,
		CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
			return []CGetSubOperation{cGetSubOperation("1.2.3.4.5")}, nil
		}),
		withoutCGetStorageContext(),
	)
	if serverErr != nil {
		t.Fatalf("ServeStudyRootCGet() error = %v, want graceful failed sub-operation", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetSubOperationsCompleteOneOrMoreFailures, 0, 0, 1, 0)
	if got := final.FailedSOPInstanceUIDListOrNil; len(got) != 1 || got[0] != "1.2.3.4.5" {
		t.Fatalf("final FailedSOPInstanceUIDListOrNil = %v, want [1.2.3.4.5]", got)
	}
}

func TestServeStudyRootCGetReportsCancelFromSubOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, serverErr := runStudyRootCGetStatusTest(t, ctx, CGetHandlerFunc(func(context.Context, CGetRequestContext) ([]CGetSubOperation, error) {
		return []CGetSubOperation{{
			AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
			AffectedSOPInstanceUID: "1.2.3.4.5",
			LoadDataSet: func(context.Context) (*object.Object, error) {
				return nil, ErrCGetCanceled
			},
		}}, nil
	}))
	if !errors.Is(serverErr, ErrCGetCanceled) {
		t.Fatalf("ServeStudyRootCGet() error = %v, want ErrCGetCanceled", serverErr)
	}
	assertCGetCounts(t, "final", final, StatusCGetCancel, 1, 0, 0, 0)
}

type cGetStatusTestConfig struct {
	clientContexts []ul.PresentationContext
	clientRoles    []ul.RoleSelectionItem
	serverAbstract []string
	serverRoles    []ul.RoleSelectionItem
	identifier     []core.Element
	storeHandler   CGetStoreHandler
	rawCGetOnly    bool
}

type cGetStatusTestOption func(*cGetStatusTestConfig)

func runStudyRootCGetStatusTest(t *testing.T, ctx context.Context, handler CGetHandler, options ...cGetStatusTestOption) (*CGetResponse, error) {
	t.Helper()

	identifier, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	cfg := cGetStatusTestConfig{
		clientContexts: []ul.PresentationContext{
			StudyRootGetPresentationContext(),
			{AbstractSyntaxUID: cGetTestStorageSOPClassUID, TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian}},
		},
		clientRoles: []ul.RoleSelectionItem{{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true}},
		serverAbstract: []string{
			StudyRootGetSOPClassUID,
			cGetTestStorageSOPClassUID,
		},
		serverRoles:  []ul.RoleSelectionItem{{SopClassUID: cGetTestStorageSOPClassUID, SCPRole: true}},
		identifier:   identifier,
		storeHandler: CGetStoreHandlerFunc(func(context.Context, CGetStoreRequestContext) (uint16, error) { return StatusSuccess, nil }),
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
			AETitle:                   "GETSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: cfg.serverAbstract,
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
			RoleSelections:            cfg.serverRoles,
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServeStudyRootCGet(ctx, assoc, pc.ID, handler)
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "GETSCP",
		CallingAETitle: "GETSCU",
		Contexts:       cfg.clientContexts,
		RoleSelections: cfg.clientRoles,
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootGetSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	req := CGetRequest{
		AffectedSOPClassUID: StudyRootGetSOPClassUID,
		MessageID:           1,
		Priority:            PriorityMedium,
	}
	identifierObj := object.FromElements(cfg.identifier, std.Dictionary)
	if cfg.rawCGetOnly {
		if err := SendCGetRequest(assoc, pc.ID, req); err != nil {
			t.Fatalf("SendCGetRequest() error = %v", err)
		}
		if err := SendDataSet(assoc, pc.ID, identifierObj, transfer.ImplicitVRLittleEndian); err != nil {
			t.Fatalf("SendDataSet() error = %v", err)
		}
		command, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			t.Fatalf("ReceiveCommandSet() error = %v", err)
		}
		final, err := ParseCGetResponse(command)
		if err != nil {
			t.Fatalf("ParseCGetResponse() error = %v", err)
		}
		return final, <-serverDone
	}

	final, err := SendCGet(ctx, assoc, pc.ID, req, identifierObj, transfer.ImplicitVRLittleEndian, cfg.storeHandler)
	if err != nil {
		t.Fatalf("SendCGet() error = %v", err)
	}
	return final, <-serverDone
}

func withCGetIdentifier(identifier []core.Element) cGetStatusTestOption {
	return func(cfg *cGetStatusTestConfig) {
		cfg.identifier = identifier
	}
}

func withCGetStoreHandler(handler CGetStoreHandler) cGetStatusTestOption {
	return func(cfg *cGetStatusTestConfig) {
		cfg.storeHandler = handler
	}
}

func withoutCGetStorageRole() cGetStatusTestOption {
	return func(cfg *cGetStatusTestConfig) {
		cfg.clientRoles = nil
		cfg.serverRoles = nil
		cfg.rawCGetOnly = true
	}
}

func withoutCGetStorageContext() cGetStatusTestOption {
	return func(cfg *cGetStatusTestConfig) {
		cfg.clientContexts = []ul.PresentationContext{StudyRootGetPresentationContext()}
		cfg.serverAbstract = []string{StudyRootGetSOPClassUID}
		cfg.clientRoles = nil
		cfg.serverRoles = nil
		cfg.rawCGetOnly = true
	}
}

func cGetSubOperation(instanceUID string) CGetSubOperation {
	return CGetSubOperation{
		AffectedSOPClassUID:    cGetTestStorageSOPClassUID,
		AffectedSOPInstanceUID: instanceUID,
		LoadDataSet: func(context.Context) (*object.Object, error) {
			return object.FromElements([]core.Element{
				{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{cGetTestStorageSOPClassUID}},
				{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{instanceUID}},
			}, std.Dictionary), nil
		},
	}
}
