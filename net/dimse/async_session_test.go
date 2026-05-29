package dimse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestAsyncSessionRoutesOutOfOrderResponsesAndRetainsPendingSlot(t *testing.T) {
	client, server := newAsyncSessionPair(t, 2, 2, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})

	received := make(chan AsyncMessage, 2)
	release := make(map[uint16]chan struct{})
	var releaseMu sync.Mutex
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		releaseMu.Lock()
		wait := make(chan struct{})
		release[message.MessageID] = wait
		releaseMu.Unlock()
		received <- message
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})

	op1, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	op2, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := <-received
	second := <-received
	if first.MessageID == second.MessageID || first.MessageID == 0 || second.MessageID == 0 {
		t.Fatalf("received message IDs = %d, %d", first.MessageID, second.MessageID)
	}

	releaseMu.Lock()
	close(release[op2.MessageID()])
	releaseMu.Unlock()
	if response, err := op2.Wait(testContext(t)); err != nil || response.MessageID != op2.MessageID() {
		t.Fatalf("second operation response = %#v, %v", response, err)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := op1.Next(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first operation before release error = %v", err)
	}
	releaseMu.Lock()
	close(release[op1.MessageID()])
	releaseMu.Unlock()
	if _, err := op1.Wait(testContext(t)); err != nil {
		t.Fatalf("first operation: %v", err)
	}
	metrics := client.Snapshot()
	if metrics.PeakInvoked != 2 || metrics.ActiveInvoked != 0 {
		t.Fatalf("client metrics = %+v", metrics)
	}
}

func TestAsyncSessionAppliesInvokedBackpressure(t *testing.T) {
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	release := make(chan struct{})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})

	first, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.StartCEcho(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second StartCEcho error = %v, want deadline", err)
	}
	close(release)
	if _, err := first.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSessionRejectsLocalPerformedCapBelowNegotiatedWindow(t *testing.T) {
	assoc := &ul.Association{
		Context: context.Background(),
		AcceptedContexts: []ul.AcceptedContext{{
			ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
		}},
		NegotiatedAsynchronousOperationsWindow: ul.AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 2},
		AsynchronousOperationsNegotiated:       true,
	}
	if _, err := NewAsyncSession(assoc, AsyncSessionOptions{MaxPerformedOperations: 1}); err == nil || !strings.Contains(err.Error(), "below negotiated peer window") {
		t.Fatalf("NewAsyncSession() error = %v", err)
	}
}

func TestAsyncSessionPendingResponseRetainsInvokedSlot(t *testing.T) {
	queryUID := "1.2.840.10008.5.1.4.1.2.2.1"
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: queryUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	pendingSent := make(chan struct{})
	final := make(chan struct{})
	server.Handle(CFindRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		if err := session.Respond(ctx, message, (CFindResponse{
			AffectedSOPClassUID: queryUID, CommandDataSetType: DataSetPresent, Status: StatusPending,
		}).CommandSet(), object.New(nil)); err != nil {
			return err
		}
		close(pendingSent)
		select {
		case <-final:
		case <-ctx.Done():
			return ctx.Err()
		}
		return session.Respond(ctx, message, (CFindResponse{
			AffectedSOPClassUID: queryUID, CommandDataSetType: NoDataSet, Status: StatusSuccess,
		}).CommandSet(), nil)
	})
	op, err := client.StartCFind(context.Background(), 1, CFindRequest{AffectedSOPClassUID: queryUID}, object.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	<-pendingSent
	pending, err := op.Next(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	status, _ := CommandUint16(pending.Command, Status)
	if status != StatusPending || pending.DataSet == nil {
		t.Fatalf("pending response = %#v status 0x%04x", pending, status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.StartCFind(ctx, 1, CFindRequest{AffectedSOPClassUID: queryUID}, object.New(nil)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second operation error = %v", err)
	}
	close(final)
	if response, err := op.Wait(testContext(t)); err != nil || response.Command == nil {
		t.Fatalf("final response = %#v, %v", response, err)
	}
}

func TestAsyncSessionMessageIDWrapSkipsZero(t *testing.T) {
	client, server := newAsyncSessionPair(t, 2, 2, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})
	client.mu.Lock()
	client.nextMessageID = 65535
	client.mu.Unlock()
	first, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageID() != 65535 || second.MessageID() != 1 {
		t.Fatalf("message IDs = %d, %d; want 65535, 1", first.MessageID(), second.MessageID())
	}
	if _, err := first.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSessionRoutesTargetedCancel(t *testing.T) {
	contexts := []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: "1.2.840.10008.5.1.4.1.2.2.1", TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}}
	client, server := newAsyncSessionPair(t, 2, 2, contexts)
	canceled := make(chan uint16, 2)
	started := make(chan AsyncMessage, 2)
	release := make(chan struct{})
	server.Handle(CFindRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		started <- message
		select {
		case <-ctx.Done():
			canceled <- message.MessageID
		case <-release:
		}
		return session.Respond(context.Background(), message, (CFindResponse{
			AffectedSOPClassUID: contexts[0].AbstractSyntaxUID,
			CommandDataSetType:  NoDataSet,
			Status:              0xFE00,
		}).CommandSet(), nil)
	})

	identifier := object.New(nil)
	op1, err := client.StartCFind(context.Background(), 1, CFindRequest{AffectedSOPClassUID: contexts[0].AbstractSyntaxUID}, identifier)
	if err != nil {
		t.Fatal(err)
	}
	op2, err := client.StartCFind(context.Background(), 1, CFindRequest{AffectedSOPClassUID: contexts[0].AbstractSyntaxUID}, identifier)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	<-started
	if err := op1.Cancel(testContext(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-canceled:
		if got != op1.MessageID() {
			t.Fatalf("canceled ID = %d, want %d", got, op1.MessageID())
		}
	case <-time.After(time.Second):
		t.Fatal("target handler was not canceled")
	}
	select {
	case got := <-canceled:
		t.Fatalf("unexpected second cancellation for %d", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if _, err := op1.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := op2.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncOperationCancelMayRetryWhenWriterGateWasNotEntered(t *testing.T) {
	queryUID := "1.2.840.10008.5.1.4.1.2.2.1"
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: queryUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	started := make(chan struct{})
	server.Handle(CFindRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		close(started)
		<-ctx.Done()
		return session.Respond(context.Background(), message, (CFindResponse{
			AffectedSOPClassUID: queryUID,
			CommandDataSetType:  NoDataSet,
			Status:              0xFE00,
		}).CommandSet(), nil)
	})
	operation, err := client.StartCFind(context.Background(), 1, CFindRequest{AffectedSOPClassUID: queryUID}, object.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	<-started

	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- client.assoc.SerializeMessageWriteContext(client.owner.Context(context.Background()), func() error {
			close(gateEntered)
			<-releaseGate
			return nil
		})
	}()
	<-gateEntered
	shortCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := operation.Cancel(shortCtx); !errors.Is(err, ul.ErrMessageWriteNotStarted) {
		t.Fatalf("first Cancel() error = %v, want ErrMessageWriteNotStarted", err)
	}
	select {
	case <-client.Done():
		t.Fatal("pre-write cancel failure closed the session")
	default:
	}
	close(releaseGate)
	if err := <-gateDone; err != nil {
		t.Fatal(err)
	}
	if err := operation.Cancel(testContext(t)); err != nil {
		t.Fatalf("retry Cancel() error = %v", err)
	}
	if _, err := operation.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSessionOperationContextSendsCancelAndDrainsFinal(t *testing.T) {
	queryUID := "1.2.840.10008.5.1.4.1.2.2.1"
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: queryUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	handlerStarted := make(chan struct{})
	server.Handle(CFindRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		close(handlerStarted)
		<-ctx.Done()
		return session.Respond(context.Background(), message, (CFindResponse{
			AffectedSOPClassUID: queryUID, CommandDataSetType: NoDataSet, Status: 0xFE00,
		}).CommandSet(), nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	operation, err := client.StartCFind(ctx, 1, CFindRequest{AffectedSOPClassUID: queryUID}, object.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	<-handlerStarted
	cancel()
	response, err := operation.Wait(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	status, _ := CommandUint16(response.Command, Status)
	if status != 0xFE00 {
		t.Fatalf("terminal status = 0x%04X, want cancel", status)
	}
	if client.Snapshot().ActiveInvoked != 0 {
		t.Fatalf("client metrics = %+v", client.Snapshot())
	}
}

func TestAsyncSessionCGetReverseCStoreUsesPerformedWindow(t *testing.T) {
	queryUID := "1.2.840.10008.5.1.4.1.2.2.3"
	storageUID := "1.2.840.10008.5.1.4.1.1.2"
	contexts := []ul.AcceptedContext{
		{ID: 1, AbstractSyntaxUID: queryUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID},
		{ID: 3, AbstractSyntaxUID: storageUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID},
	}
	client, server := newAsyncSessionPair(t, 2, 2, contexts)
	roles := []ul.RoleSelectionItem{{SopClassUID: storageUID, SCURole: true, SCPRole: true}}
	client.assoc.AcceptedRoleSelections = append([]ul.RoleSelectionItem(nil), roles...)
	server.assoc.AcceptedRoleSelections = append([]ul.RoleSelectionItem(nil), roles...)
	client.Handle(CStoreRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		request, err := ParseCStoreRequest(message.Command)
		if err != nil {
			return err
		}
		if message.DataSet == nil {
			return errors.New("missing reverse C-STORE dataset")
		}
		return session.Respond(ctx, message, (CStoreResponse{
			AffectedSOPClassUID:    request.AffectedSOPClassUID,
			AffectedSOPInstanceUID: request.AffectedSOPInstanceUID,
			Status:                 StatusSuccess,
		}).CommandSet(), nil)
	})
	server.Handle(CGetRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		store, err := session.StartCStore(ctx, 3, CStoreRequest{
			AffectedSOPClassUID:    storageUID,
			AffectedSOPInstanceUID: "1.2.3.4",
			Priority:               PriorityMedium,
		}, object.FromElements([]core.Element{{
			Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO},
			Value:  core.StringValue{"TEST"},
		}}, nil))
		if err != nil {
			return err
		}
		if _, err := store.Wait(ctx); err != nil {
			return err
		}
		return session.Respond(ctx, message, (CGetResponse{AffectedSOPClassUID: queryUID, Status: StatusSuccess}).CommandSet(), nil)
	})

	operation, err := client.StartCGet(context.Background(), 1, CGetRequest{AffectedSOPClassUID: queryUID}, object.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	response, err := operation.Wait(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCGetResponse(response.Command)
	if err != nil || parsed.Status != StatusSuccess {
		t.Fatalf("C-GET response = %#v, %v", parsed, err)
	}
	if metrics := client.Snapshot(); metrics.PeakPerformed != 1 {
		t.Fatalf("client metrics = %+v, want one reverse performed operation", metrics)
	}
}

func TestAsyncSessionSerializesFragmentedMessagesAtomically(t *testing.T) {
	storageUID := "1.2.840.10008.5.1.4.1.1.2"
	client, server := newAsyncSessionPair(t, 2, 2, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: storageUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	received := make(chan string, 2)
	server.Handle(CStoreRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		request, err := ParseCStoreRequest(message.Command)
		if err != nil {
			return err
		}
		patientID, ok := message.DataSet.GetString(core.NewTag(0x0010, 0x0020))
		if !ok {
			return errors.New("missing patient ID")
		}
		received <- patientID
		return session.Respond(ctx, message, (CStoreResponse{
			AffectedSOPClassUID:    request.AffectedSOPClassUID,
			AffectedSOPInstanceUID: request.AffectedSOPInstanceUID,
			Status:                 StatusSuccess,
		}).CommandSet(), nil)
	})

	start := make(chan struct{})
	results := make(chan error, 2)
	for index, patientID := range []string{"FIRST", "SECOND"} {
		index, patientID := index, patientID
		go func() {
			<-start
			dataSet := object.FromElements([]core.Element{
				{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{patientID}},
				core.NewRawElement(core.NewTag(0x7FE0, 0x0010), core.VROB, make([]byte, 128<<10)),
			}, nil)
			operation, err := client.StartCStore(context.Background(), 1, CStoreRequest{
				AffectedSOPClassUID:    storageUID,
				AffectedSOPInstanceUID: fmt.Sprintf("1.2.3.%d", index+1),
				Priority:               PriorityMedium,
			}, dataSet)
			if err == nil {
				waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, err = operation.Wait(waitCtx)
				cancel()
			}
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{<-received: true, <-received: true}
	if !seen["FIRST"] || !seen["SECOND"] {
		t.Fatalf("received patient IDs = %#v", seen)
	}
}

func TestAsyncSessionStressSlowPeerRespectsWindow(t *testing.T) {
	client, server := newAsyncSessionPair(t, 4, 4, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		delay := time.Duration(5-int(message.MessageID%5)) * time.Millisecond
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})
	type stressResult struct {
		stage string
		err   error
	}
	results := make(chan stressResult, 32)
	for range 32 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			operation, err := client.StartCEcho(ctx)
			if err != nil {
				results <- stressResult{stage: "start", err: err}
				return
			}
			_, err = operation.Wait(ctx)
			results <- stressResult{stage: "wait", err: err}
		}()
	}
	for range 32 {
		if result := <-results; result.err != nil {
			t.Fatalf("stress %s: %v; client err=%v metrics=%+v; server err=%v metrics=%+v", result.stage, result.err, client.Err(), client.Snapshot(), server.Err(), server.Snapshot())
		}
	}
	clientMetrics := client.Snapshot()
	serverMetrics := server.Snapshot()
	deadline := time.Now().Add(time.Second)
	for serverMetrics.ActivePerformed != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		serverMetrics = server.Snapshot()
	}
	if clientMetrics.ActiveInvoked != 0 || clientMetrics.PeakInvoked > 4 || clientMetrics.PeakInvoked < 2 {
		t.Fatalf("client metrics = %+v", clientMetrics)
	}
	if serverMetrics.ActivePerformed != 0 || serverMetrics.PeakPerformed > 4 || serverMetrics.PeakPerformed < 2 {
		t.Fatalf("server metrics = %+v", serverMetrics)
	}
}

func TestAsyncSessionStartedTerminalWriteBackpressuresFiniteWindow(t *testing.T) {
	session := &AsyncSession{
		incoming:       make(map[uint16]asyncIncomingOperation),
		performedSlots: make(chan struct{}, 1),
		stateChanged:   make(chan struct{}),
		done:           make(chan struct{}),
	}
	session.performedSlots <- struct{}{}
	session.incoming[1] = asyncIncomingOperation{
		requestField: CEchoRQ,
		finishing:    true,
		generation:   1,
		slotHeld:     true,
	}
	session.metrics.ActivePerformed = 1

	if session.acquirePerformedSlotForRequest() {
		t.Fatal("terminal waiting for the writer gate excused a finite-window violation")
	}
	session.mu.Lock()
	current := session.incoming[1]
	current.writeStarted = true
	session.incoming[1] = current
	session.signalStateChangedLocked()
	session.mu.Unlock()

	acquired := make(chan bool, 1)
	go func() { acquired <- session.acquirePerformedSlotForRequest() }()
	select {
	case got := <-acquired:
		t.Fatalf("next performed slot acquired before terminal completion: %t", got)
	case <-time.After(20 * time.Millisecond):
	}
	if metrics := session.Snapshot(); metrics.ActivePerformed != 1 {
		t.Fatalf("performed occupancy changed before terminal completion: %+v", metrics)
	}
	if !session.finishIncomingOperation(1, CEchoRQ, 1) {
		t.Fatal("terminal operation did not finish")
	}
	if !<-acquired {
		t.Fatal("next performed slot was not acquired after terminal completion")
	}
	<-session.performedSlots
}

func TestAsyncSessionReleaseWaitIncludesBackpressuredIncomingAdmission(t *testing.T) {
	session := &AsyncSession{
		assoc: &ul.Association{AcceptedContexts: []ul.AcceptedContext{{
			ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
		}}},
		ctx:            context.Background(),
		options:        AsyncSessionOptions{MaxPendingRequests: 2},
		operations:     make(map[uint16]*AsyncOperation),
		incoming:       make(map[uint16]asyncIncomingOperation),
		performedSlots: make(chan struct{}, 1),
		stateChanged:   make(chan struct{}),
		done:           make(chan struct{}),
	}
	session.performedSlots <- struct{}{}
	session.incoming[1] = asyncIncomingOperation{
		requestField: CEchoRQ,
		finishing:    true,
		writeStarted: true,
		generation:   1,
		slotHeld:     true,
	}
	session.metrics.ActivePerformed = 1

	type prepareResult struct {
		prepared   bool
		status     uint16
		generation uint64
		err        error
	}
	preparedCh := make(chan prepareResult, 1)
	command := object.FromElements((CEchoRequest{MessageID: 2}).CommandSet(), nil)
	go func() {
		prepared, status, generation, err := session.prepareReceivedRequest(1, command)
		preparedCh <- prepareResult{prepared: prepared, status: status, generation: generation, err: err}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		pending := session.pendingIncoming
		session.mu.Unlock()
		if pending == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("incoming admission did not become pending")
		}
		time.Sleep(time.Millisecond)
	}

	releaseCh := make(chan error, 1)
	go func() { releaseCh <- session.waitIdleAndCommitRelease(context.Background()) }()
	select {
	case err := <-releaseCh:
		t.Fatalf("release ignored pending incoming admission: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if !session.finishIncomingOperation(1, CEchoRQ, 1) {
		t.Fatal("first incoming operation did not finish")
	}
	got := <-preparedCh
	if got.err != nil || !got.prepared || got.status != 0 || got.generation == 0 {
		t.Fatalf("second incoming admission = %+v", got)
	}
	select {
	case err := <-releaseCh:
		t.Fatalf("release ignored registered incoming operation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	session.mu.Lock()
	second := session.incoming[2]
	session.mu.Unlock()
	second.cancel()
	if !session.finishIncomingOperation(2, CEchoRQ, got.generation) {
		t.Fatal("second incoming operation did not finish")
	}
	if err := <-releaseCh; err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSessionReleaseWaitsAndUsesSoleReader(t *testing.T) {
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	releaseHandler := make(chan struct{})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		select {
		case <-releaseHandler:
		case <-ctx.Done():
			return ctx.Err()
		}
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})
	operation, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Release(context.Background(), AsyncReleaseRejectIfActive); !errors.Is(err, ErrAsyncOperationsActive) {
		t.Fatalf("reject-if-active error = %v", err)
	}
	released := make(chan error, 1)
	go func() { released <- client.Release(testContext(t), AsyncReleaseWait) }()
	select {
	case err := <-released:
		t.Fatalf("release returned before operation completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := client.StartCEcho(context.Background()); !errors.Is(err, ErrAsyncSessionReleasing) {
		t.Fatalf("new invocation during release error = %v", err)
	}
	close(releaseHandler)
	if _, err := operation.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("release handshake did not complete")
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Release returned before session teardown completed")
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("peer session did not observe release")
	}
}

func TestAsyncSessionReleaseMayRetryWhenWriterGateWasNotEntered(t *testing.T) {
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})
	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- client.assoc.SerializeMessageWriteContext(client.owner.Context(context.Background()), func() error {
			close(gateEntered)
			<-releaseGate
			return nil
		})
	}()
	<-gateEntered
	shortCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.Release(shortCtx, AsyncReleaseWait); !errors.Is(err, ul.ErrMessageWriteNotStarted) {
		t.Fatalf("first Release() error = %v, want ErrMessageWriteNotStarted", err)
	}
	select {
	case <-client.Done():
		t.Fatal("pre-write release failure closed the session")
	default:
	}
	close(releaseGate)
	if err := <-gateDone; err != nil {
		t.Fatal(err)
	}
	operation, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatalf("StartCEcho after release rollback error = %v", err)
	}
	if _, err := operation.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := client.Release(testContext(t), AsyncReleaseWait); err != nil {
		t.Fatalf("retry Release() error = %v", err)
	}
}

func TestAsyncSessionExclusiveOwnerRejectsLegacyReadersAndHandlerBearerReuse(t *testing.T) {
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		if _, err := session.assoc.Receive(ctx); !errors.Is(err, ul.ErrAssociationExclusivelyOwned) {
			return fmt.Errorf("handler Association.Receive error = %v", err)
		}
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})
	if _, err := client.assoc.Receive(context.Background()); !errors.Is(err, ul.ErrAssociationExclusivelyOwned) {
		t.Fatalf("external Association.Receive error = %v", err)
	}
	if err := client.assoc.SerializeMessageWrite(func() error { return nil }); !errors.Is(err, ul.ErrAssociationExclusivelyOwned) {
		t.Fatalf("external SerializeMessageWrite error = %v", err)
	}
	dispatcher := NewDispatcher(client.assoc)
	if err := dispatcher.Next(testContext(t)); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("concurrent Dispatcher.Next error = %v", err)
	}
	operation, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operation.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSessionQueuedMessageBudgetIncludesCommandBytes(t *testing.T) {
	client, server := newAsyncSessionPairOptions(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}}, AsyncSessionOptions{}, AsyncSessionOptions{MaxQueuedMessageBytes: 1})
	operation, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operation.Wait(testContext(t)); err == nil {
		t.Fatal("Wait() error = nil after peer command budget violation")
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("command budget violation did not stop receiver session")
	}
	if err := server.Err(); !errors.Is(err, ErrAsyncResourceLimit) {
		t.Fatalf("server error = %v, want ErrAsyncResourceLimit", err)
	}
}

func TestAsyncOperationDiscardResponsesReleasesQueuedMessageBudget(t *testing.T) {
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		return session.Respond(ctx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
	})
	operation, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-operation.Done():
	case <-time.After(time.Second):
		t.Fatal("operation did not finish")
	}
	if metrics := client.Snapshot(); metrics.QueuedMessageBytes == 0 {
		t.Fatalf("queued response was not accounted: %+v", metrics)
	}
	operation.DiscardResponses()
	if metrics := client.Snapshot(); metrics.QueuedMessageBytes != 0 {
		t.Fatalf("DiscardResponses retained queued budget: %+v", metrics)
	}
}

func TestAsyncSessionIncomingGenerationSeparatesDuplicateFromLegalReuse(t *testing.T) {
	assoc := &ul.Association{AcceptedContexts: []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}}}
	session := &AsyncSession{
		assoc:          assoc,
		ctx:            context.Background(),
		options:        AsyncSessionOptions{MaxPendingRequests: 2},
		incoming:       make(map[uint16]asyncIncomingOperation),
		performedSlots: make(chan struct{}, 1),
		stateChanged:   make(chan struct{}),
		done:           make(chan struct{}),
		nextIncomingID: 1,
	}
	const messageID = 7
	session.incoming[messageID] = asyncIncomingOperation{
		cancel:       func() {},
		ctx:          context.Background(),
		requestField: CEchoRQ,
		pcID:         1,
		finishing:    true,
		generation:   1,
	}
	session.metrics.ActivePerformed = 1
	command := object.FromElements((CEchoRequest{MessageID: messageID}).CommandSet(), nil)
	prepared, status, generation, err := session.prepareIncomingRequest(1, command)
	if err != nil || prepared || status != StatusDuplicateInvocation || generation != 0 {
		t.Fatalf("pre-write duplicate = prepared:%t status:0x%04X generation:%d err:%v", prepared, status, generation, err)
	}

	session.mu.Lock()
	current := session.incoming[messageID]
	current.writeCompleted = true
	session.incoming[messageID] = current
	session.signalStateChangedLocked()
	session.mu.Unlock()
	type result struct {
		prepared   bool
		status     uint16
		generation uint64
		err        error
	}
	resultCh := make(chan result, 1)
	go func() {
		prepared, status, generation, err := session.prepareIncomingRequest(1, command)
		resultCh <- result{prepared: prepared, status: status, generation: generation, err: err}
	}()
	select {
	case got := <-resultCh:
		t.Fatalf("post-write reuse returned before old generation completed: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	if !session.finishIncomingOperation(messageID, CEchoRQ, 1) {
		t.Fatal("old generation did not finish")
	}
	got := <-resultCh
	if got.err != nil || !got.prepared || got.status != 0 || got.generation <= 1 {
		t.Fatalf("legal reuse result = %+v", got)
	}
	if session.finishIncomingOperation(messageID, CEchoRQ, 1) {
		t.Fatal("old generation removed the reused operation")
	}
	if !session.finishIncomingOperation(messageID, CEchoRQ, got.generation) {
		t.Fatal("new generation did not finish")
	}
}

func TestAsyncSessionUnlimitedPerformedLocalCapRespondsWithoutBlockingDemux(t *testing.T) {
	queryUID := "1.2.840.10008.5.1.4.1.2.2.1"
	client, server := newAsyncSessionPairOptions(t, 2, 0, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: queryUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}}, AsyncSessionOptions{}, AsyncSessionOptions{MaxPerformedOperations: 1, MaxPendingRequests: 1})
	started := make(chan AsyncMessage, 1)
	server.Handle(CFindRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		started <- message
		<-ctx.Done()
		return session.Respond(context.Background(), message, (CFindResponse{
			AffectedSOPClassUID: queryUID,
			CommandDataSetType:  NoDataSet,
			Status:              0xFE00,
		}).CommandSet(), nil)
	})
	first, err := client.StartCFind(context.Background(), 1, CFindRequest{AffectedSOPClassUID: queryUID}, object.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := client.StartCFind(context.Background(), 1, CFindRequest{AffectedSOPClassUID: queryUID}, object.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	response, err := second.Wait(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCFindResponse(response.Command)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Status != 0xA700 {
		t.Fatalf("resource-limited C-FIND status = 0x%04X, want 0xA700", parsed.Status)
	}
	if err := first.Cancel(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSessionAbortWakesAllWaiters(t *testing.T) {
	client, server := newAsyncSessionPair(t, 2, 2, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	started := make(chan struct{}, 2)
	server.Handle(CEchoRQ, func(ctx context.Context, _ *AsyncSession, _ AsyncMessage) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	first, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	<-started
	if err := server.Abort(testContext(t)); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []*AsyncOperation{first, second} {
		select {
		case <-operation.Done():
		case <-time.After(time.Second):
			t.Fatal("operation waiter was not awakened")
		}
		if _, err := operation.Next(context.Background()); err == nil {
			t.Fatal("operation error = nil after abort")
		}
	}
	if metrics := client.Snapshot(); metrics.ActiveInvoked != 0 {
		t.Fatalf("client metrics after abort = %+v", metrics)
	}
}

func TestAsyncSessionConcurrentAbortIsIdempotent(t *testing.T) {
	client, _ := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			results <- client.Abort(ctx)
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if !errors.Is(first, second) && !errors.Is(second, first) && fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("concurrent Abort results differ: %v, %v", first, second)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Abort returned before session teardown")
	}
}

func TestAsyncSessionRedactsHandlerPanicAndStopsCleanly(t *testing.T) {
	client, server := newAsyncSessionPair(t, 1, 1, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(context.Context, *AsyncSession, AsyncMessage) error {
		panic("PATIENT^NAME")
	})
	operation, err := client.StartCEcho(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("server did not stop after handler panic")
	}
	serverErr := server.Err()
	if serverErr == nil || strings.Contains(serverErr.Error(), "PATIENT") {
		t.Fatalf("server error = %v", serverErr)
	}
	var handlerErr *AsyncRequestHandlerError
	if !errors.As(serverErr, &handlerErr) {
		t.Fatalf("server error = %T, want AsyncRequestHandlerError", serverErr)
	}
	select {
	case <-operation.Done():
	case <-time.After(time.Second):
		t.Fatal("client operation remained blocked")
	}
}

func newAsyncSessionPair(t *testing.T, invoked, performed uint16, contexts []ul.AcceptedContext) (*AsyncSession, *AsyncSession) {
	return newAsyncSessionPairOptions(t, invoked, performed, contexts, AsyncSessionOptions{}, AsyncSessionOptions{})
}

func newAsyncSessionPairOptions(t *testing.T, invoked, performed uint16, contexts []ul.AcceptedContext, clientOptions, serverOptions AsyncSessionOptions) (*AsyncSession, *AsyncSession) {
	t.Helper()
	left, right := net.Pipe()
	window := ul.AsynchronousOperationsWindow{MaximumInvoked: invoked, MaximumPerformed: performed}
	clientAssoc := &ul.Association{
		Conn: left, Context: context.Background(), AcceptedContexts: append([]ul.AcceptedContext(nil), contexts...),
		MaxPDU: ul.DefaultMaxPDU, PeerMaxPDU: ul.DefaultMaxPDU,
		NegotiatedAsynchronousOperationsWindow: window, AsynchronousOperationsNegotiated: true, IsAssociationRequestor: true,
	}
	serverAssoc := &ul.Association{
		Conn: right, Context: context.Background(), AcceptedContexts: append([]ul.AcceptedContext(nil), contexts...),
		MaxPDU: ul.DefaultMaxPDU, PeerMaxPDU: ul.DefaultMaxPDU,
		NegotiatedAsynchronousOperationsWindow: window, AsynchronousOperationsNegotiated: true,
	}
	client, err := NewAsyncSession(clientAssoc, clientOptions)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewAsyncSession(serverAssoc, serverOptions)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
