package dimse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestServeStudyRootCFindReturnsMultipleMatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		err = ServeStudyRootCFind(ctx, assoc, pc.ID, CFindHandlerFunc(func(_ context.Context, req CFindRequestContext) ([]*object.Object, error) {
			if req.QueryRetrieveLevel != QueryRetrieveLevelStudy {
				t.Errorf("QueryRetrieveLevel = %q, want STUDY", req.QueryRetrieveLevel)
			}
			if req.Request.MessageID != 1 || req.Identifier == nil {
				t.Errorf("request context = %#v", req)
			}
			return []*object.Object{
				studyRootFindMatch("P1", "1.2.3.1"),
				studyRootFindMatch("P2", "1.2.3.2"),
			}, nil
		}))
		if err != nil {
			serverDone <- err
			return
		}
		pdu, err := assoc.ReadPDU()
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ul.ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "FINDSCP",
		CallingAETitle: "FINDSCU",
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil, "PatientID", "StudyInstanceUID")
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}

	results, errs := Find(ctx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, object.FromElements(identifierElems, std.Dictionary), transfer.ImplicitVRLittleEndian)

	var matches []string
	var finalStatus uint16
	for results != nil || errs != nil {
		select {
		case result, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			finalStatus = result.Status()
			if result.Status() == StatusPending {
				patientID, _ := result.Identifier.GetString(core.NewTag(0x0010, 0x0020))
				matches = append(matches, patientID)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("Find() error = %v", err)
			}
		}
	}
	if got, want := strings.Join(matches, ","), "P1,P2"; got != want {
		t.Fatalf("matches = %q, want %q", got, want)
	}
	if finalStatus != StatusSuccess {
		t.Fatalf("final status = 0x%04X, want success", finalStatus)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestServeStudyRootCFindCanReturnCancelStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runStudyRootCFindStatusTest(t, ctx, CFindHandlerFunc(func(context.Context, CFindRequestContext) ([]*object.Object, error) {
		return nil, ErrCFindCanceled
	}))
	if status != CFindStatusCancel {
		t.Fatalf("final status = 0x%04X, want cancel 0x%04X", status, CFindStatusCancel)
	}
	if !errors.Is(serverErr, ErrCFindCanceled) {
		t.Fatalf("server error = %v, want ErrCFindCanceled", serverErr)
	}
}

func TestServeStudyRootCFindMapsDeadlineDuringMatchesToUnableToProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runStudyRootCFindStatusTestWithServeTimeout(t, ctx, 100*time.Millisecond, CFindHandlerFunc(func(ctx context.Context, _ CFindRequestContext) ([]*object.Object, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	if status != CFindStatusUnableToProcess {
		t.Fatalf("final status = 0x%04X, want unable-to-process 0x%04X", status, CFindStatusUnableToProcess)
	}
	if !errors.Is(serverErr, context.DeadlineExceeded) {
		t.Fatalf("server error = %v, want context deadline", serverErr)
	}
}

func TestServeStudyRootCFindHonorsCanceledContextWhileReceivingCommand(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: StudyRootFindSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	defer func() { _ = peer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ServeStudyRootCFind(ctx, local, 1, CFindHandlerFunc(func(context.Context, CFindRequestContext) ([]*object.Object, error) {
		t.Fatal("handler should not run after context cancellation")
		return nil, nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeStudyRootCFind() error = %v, want context.Canceled", err)
	}
}

func TestServeStudyRootCFindRejectsUnsupportedLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runStudyRootCFindStatusTest(t, ctx, CFindHandlerFunc(func(context.Context, CFindRequestContext) ([]*object.Object, error) {
		return nil, errors.New("handler should not run for unsupported level")
	}), withQueryRetrieveLevel("PATIENT"))
	if status != CFindStatusUnableToProcess {
		t.Fatalf("final status = 0x%04X, want unable-to-process 0x%04X", status, CFindStatusUnableToProcess)
	}
	if serverErr == nil || !strings.Contains(serverErr.Error(), "unsupported QueryRetrieveLevel") {
		t.Fatalf("server error = %v, want unsupported QueryRetrieveLevel", serverErr)
	}
}

func TestServeStudyRootCFindAcceptsImageLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status, serverErr := runStudyRootCFindStatusTest(t, ctx, CFindHandlerFunc(func(_ context.Context, req CFindRequestContext) ([]*object.Object, error) {
		if req.QueryRetrieveLevel != QueryRetrieveLevelImage {
			return nil, errors.New("expected IMAGE query level")
		}
		return nil, nil
	}), withQueryRetrieveLevel("IMAGE"))
	if status != StatusSuccess {
		t.Fatalf("final status = 0x%04X, want success 0x%04X", status, StatusSuccess)
	}
	if serverErr != nil {
		t.Fatalf("server error = %v", serverErr)
	}
}

type findStatusTestOption func([]core.Element) []core.Element

func withQueryRetrieveLevel(level string) findStatusTestOption {
	return func(elements []core.Element) []core.Element {
		elements[0] = core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{level}}
		return elements
	}
}

func runStudyRootCFindStatusTest(t *testing.T, ctx context.Context, handler CFindHandler, options ...findStatusTestOption) (uint16, error) {
	return runStudyRootCFindStatusTestWithServeTimeout(t, ctx, 0, handler, options...)
}

func runStudyRootCFindStatusTestWithServeTimeout(t *testing.T, setupCtx context.Context, serveTimeout time.Duration, handler CFindHandler, options ...findStatusTestOption) (uint16, error) {
	t.Helper()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: setupCtx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	serveCtxCh := make(chan context.Context, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   setupCtx,
			SupportedAbstractSyntaxes: []string{StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		select {
		case serveCtx := <-serveCtxCh:
			serverDone <- ServeStudyRootCFind(serveCtx, assoc, pc.ID, handler)
		case <-setupCtx.Done():
			serverDone <- setupCtx.Err()
		}
	}()

	assoc, err := ul.DialContext(setupCtx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "FINDSCP",
		CallingAETitle: "FINDSCU",
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	identifierElems, err := BuildStudyRootStudyFindKeys(nil)
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	for _, option := range options {
		identifierElems = option(identifierElems)
	}
	serveCtx := setupCtx
	cancelServe := func() {}
	if serveTimeout > 0 {
		serveCtx, cancelServe = context.WithTimeout(setupCtx, serveTimeout)
	}
	defer cancelServe()
	serveCtxCh <- serveCtx

	if err := SendCFindRequest(assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, identifierElems); err != nil {
		t.Fatalf("SendCFindRequest() error = %v", err)
	}
	rsp, _, err := ReceiveCFindResponse(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("ReceiveCFindResponse() error = %v", err)
	}
	return rsp.Status, <-serverDone
}

func studyRootFindMatch(patientID, studyInstanceUID string) *object.Object {
	return object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{QueryRetrieveLevelStudy}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{patientID}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000D), VR: core.VRUI}, Value: core.StringValue{studyInstanceUID}},
	}, std.Dictionary)
}
