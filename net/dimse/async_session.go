package dimse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	defaultAsyncUnlimitedLimit  = 64
	defaultAsyncResponseQueue   = 32
	defaultAsyncDataSetBytes    = int64(1 << 30)
	defaultAsyncQueuedBytes     = int64(1 << 30)
	defaultAsyncDataSetElements = 1_000_000
	defaultAsyncDataSetDepth    = 128
	defaultAsyncCancelDrain     = 5 * time.Second
	defaultAsyncPendingRequests = 256
	maxAsyncOperationLimit      = 65_535
	maxAsyncResponseQueue       = 65_535
	maxAsyncDataSetDepth        = 1_024

	// StatusDuplicateInvocation is the DIMSE duplicate invocation status.
	StatusDuplicateInvocation uint16 = 0x0210
)

var (
	// ErrAsyncSessionClosed marks an operation on a stopped multiplexed session.
	ErrAsyncSessionClosed = errors.New("dicom dimse: asynchronous session closed")
	// ErrAsyncSessionReleasing marks a new invocation after graceful release began.
	ErrAsyncSessionReleasing = errors.New("dicom dimse: asynchronous session is releasing")
	// ErrAsyncWindowExceeded marks a peer that exceeded the negotiated performed window.
	ErrAsyncWindowExceeded = errors.New("dicom dimse: asynchronous operations window exceeded")
	// ErrAsyncMessageIDExhausted marks exhaustion of all non-zero local Message IDs.
	ErrAsyncMessageIDExhausted = errors.New("dicom dimse: asynchronous message IDs exhausted")
	// ErrAsyncOperationNotCancelable marks C-CANCEL on a non-cancelable request.
	ErrAsyncOperationNotCancelable = errors.New("dicom dimse: asynchronous operation is not cancelable")
	// ErrAsyncOperationComplete marks a control action after terminal response.
	ErrAsyncOperationComplete = errors.New("dicom dimse: asynchronous operation already complete")
	// ErrAsyncOperationsActive marks a release rejected while operations remain active.
	ErrAsyncOperationsActive = errors.New("dicom dimse: asynchronous operations remain active")
	// ErrAsyncTerminalResponseMissing marks a handler return before terminal response.
	ErrAsyncTerminalResponseMissing = errors.New("dicom dimse: asynchronous handler returned before terminal response")
	// ErrAsyncResponseQueueFull marks an operation whose caller did not consume
	// responses within its configured bounded mailbox.
	ErrAsyncResponseQueueFull = errors.New("dicom dimse: asynchronous response queue full")
	// ErrAsyncResourceLimit marks exhaustion of a configured local bound that
	// is distinct from a peer's negotiated wire window.
	ErrAsyncResourceLimit = errors.New("dicom dimse: asynchronous session resource limit exceeded")
	// ErrAsyncReleaseNotRequestor marks an A-RELEASE attempt by the association acceptor.
	ErrAsyncReleaseNotRequestor = errors.New("dicom dimse: only the association requestor may initiate release")
	// ErrAsyncRoleNotAccepted marks DIMSE traffic in a role not negotiated for
	// the selected presentation context.
	ErrAsyncRoleNotAccepted = errors.New("dicom dimse: local DIMSE role not accepted")
)

// AsyncSessionOptions bounds the local resources used by AsyncSession. Wire
// value zero still means unlimited. MaxInvokedOperations bounds local
// invocations for that case and may reduce a finite negotiated invoked window.
// MaxPerformedOperations bounds concurrent handler workers for a zero wire
// value; MaxPendingRequests bounds retained peer invocations, with excess work
// receiving a service-specific resource status. A finite performed window is a
// peer entitlement and cannot be reduced after association negotiation.
type AsyncSessionOptions struct {
	MaxInvokedOperations   int
	MaxPerformedOperations int
	ResponseQueueDepth     int
	MaxDataSetBytes        int64
	// MaxQueuedMessageBytes bounds retained command plus dataset payload across
	// all internal request and response queues.
	MaxQueuedMessageBytes int64
	MaxDataSetElements    int
	MaxDataSetDepth       int
	CancelDrainTimeout    time.Duration
	MaxPendingRequests    int
	// CGetStorageSOPClassUIDs identifies presentation contexts eligible for
	// reverse C-STORE during C-GET. Nil uses DefaultStorageSOPClassUIDs.
	CGetStorageSOPClassUIDs []string
	// Handlers are installed before the receive loop starts. The map is cloned.
	Handlers map[uint16]AsyncRequestHandler
}

// AsyncMessage is one fully assembled DIMSE message. DataSet is nil when the
// command declares no dataset. Received objects are detached and may be
// retained by the caller.
type AsyncMessage struct {
	PresentationContextID byte
	CommandField          uint16
	MessageID             uint16
	Command               *object.Object
	DataSet               *object.Object
	messageBytes          int64
	incomingGeneration    uint64
}

// AsyncRequest describes one outgoing DIMSE request. Invoke assigns and
// overwrites Message ID association-wide. Command and DataSet are borrowed
// only until Invoke returns.
type AsyncRequest struct {
	PresentationContextID byte
	Command               []core.Element
	DataSet               *object.Object
}

// AsyncRequestHandler handles one fully assembled incoming request. Handlers
// may run concurrently up to the negotiated performed window. The context is
// canceled by a matching C-CANCEL-RQ or session shutdown. A handler must send
// its responses through AsyncSession.Respond before returning.
type AsyncRequestHandler func(context.Context, *AsyncSession, AsyncMessage) error

type asyncHandlerSessionContextKey struct{}

// AsyncRequestHandlerError redacts arbitrary handler text while preserving the
// structural cause for errors.Is/errors.As at the direct API boundary.
type AsyncRequestHandlerError struct{ Err error }

func (e *AsyncRequestHandlerError) Error() string {
	return "dicom dimse: asynchronous request handler failed"
}

func (e *AsyncRequestHandlerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AsyncSessionMetrics is an exact PHI-free snapshot of multiplexing state.
type AsyncSessionMetrics struct {
	ActiveInvoked          int
	PeakInvoked            int
	ActivePerformed        int
	PeakPerformed          int
	QueuedMessageBytes     int64
	PeakQueuedMessageBytes int64
	WindowViolations       uint64
	DuplicateInvocations   uint64
}

// AsyncReleasePolicy controls normal session shutdown.
type AsyncReleasePolicy int

const (
	// AsyncReleaseWait waits for all invoked and performed operations before release.
	AsyncReleaseWait AsyncReleasePolicy = iota
	// AsyncReleaseRejectIfActive returns ErrAsyncOperationsActive immediately.
	AsyncReleaseRejectIfActive
	// AsyncReleaseAbort aborts instead of performing a normal release handshake.
	AsyncReleaseAbort
)

type asyncIncomingOperation struct {
	cancel         context.CancelFunc
	ctx            context.Context
	requestField   uint16
	pcID           byte
	cancelable     bool
	finishing      bool
	writeStarted   bool
	writeCompleted bool
	generation     uint64
	responseMu     *sync.Mutex
	slotHeld       bool
	messageBytes   int64
}

// AsyncSession is the sole reader and message-level writer coordinator for an
// established association. It is opt-in and intentionally cannot coexist with
// legacy helpers or Dispatcher on the same association.
type AsyncSession struct {
	assoc                  *ul.Association
	owner                  *ul.AssociationOperationToken
	ctx                    context.Context
	cancel                 context.CancelFunc
	options                AsyncSessionOptions
	wirePerformedUnlimited bool
	cGetStorageSOPClasses  map[string]struct{}

	invokedSlots   chan struct{}
	performedSlots chan struct{}

	mu                 sync.Mutex
	handlers           map[uint16]AsyncRequestHandler
	operations         map[uint16]*AsyncOperation
	incoming           map[uint16]asyncIncomingOperation
	pendingIncoming    int
	nextMessageID      uint16
	nextIncomingID     uint64
	closed             bool
	releasing          bool
	releaseSent        bool
	closeErr           error
	metrics            AsyncSessionMetrics
	queuedMessageBytes int64
	stateChanged       chan struct{}

	done         chan struct{}
	shutdownOnce sync.Once
	releaseDone  chan struct{}
	releaseErr   error
	releaseOnce  sync.Once
	abortMu      sync.Mutex
	abortStarted bool
	abortDone    chan struct{}
	abortErr     error
}

// AsyncOperation is one outstanding locally invoked DIMSE operation. Pending
// responses retain its Message ID and invoked slot until a terminal response.
type AsyncOperation struct {
	session               *AsyncSession
	messageID             uint16
	pcID                  byte
	responseField         uint16
	cancelable            bool
	responses             chan AsyncMessage
	done                  chan struct{}
	finishOnce            sync.Once
	cancelMu              sync.Mutex
	drainOnce             sync.Once
	mu                    sync.Mutex
	err                   error
	finished              bool
	contextCancel         context.CancelFunc
	cancelSent            bool
	terminalSeen          bool
	discarding            bool
	queuedBytes           int64
	accountingTransferred bool
}

// NewAsyncSession takes exclusive DIMSE ownership of assoc and starts its sole
// receive loop. Close, Release, peer release, abort, EOF, and protocol errors
// wake every outstanding operation.
func NewAsyncSession(assoc *ul.Association, options AsyncSessionOptions) (*AsyncSession, error) {
	if assoc == nil {
		return nil, fmt.Errorf("dicom dimse: nil association")
	}
	owner, ok := assoc.TryBeginExclusiveOperation()
	if !ok {
		return nil, ErrOperationInProgress
	}
	options, invokedLimit, performedLimit, err := normalizeAsyncSessionOptions(assoc, options)
	if err != nil {
		owner.End()
		return nil, err
	}
	parent := assoc.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	handlers := make(map[uint16]AsyncRequestHandler, len(options.Handlers))
	for commandField, handler := range options.Handlers {
		if handler != nil {
			handlers[commandField] = handler
		}
	}
	storageSOPClasses := make(map[string]struct{}, len(options.CGetStorageSOPClassUIDs))
	for _, uid := range options.CGetStorageSOPClassUIDs {
		storageSOPClasses[uid] = struct{}{}
	}
	session := &AsyncSession{
		assoc:                  assoc,
		owner:                  owner,
		ctx:                    ctx,
		cancel:                 cancel,
		options:                options,
		wirePerformedUnlimited: assoc.EffectiveAsynchronousOperationsWindow().MaximumPerformed == 0,
		cGetStorageSOPClasses:  storageSOPClasses,
		invokedSlots:           make(chan struct{}, invokedLimit),
		performedSlots:         make(chan struct{}, performedLimit),
		handlers:               handlers,
		operations:             make(map[uint16]*AsyncOperation),
		incoming:               make(map[uint16]asyncIncomingOperation),
		nextMessageID:          1,
		stateChanged:           make(chan struct{}),
		done:                   make(chan struct{}),
		releaseDone:            make(chan struct{}),
		abortDone:              make(chan struct{}),
	}
	go session.readLoop()
	return session, nil
}

func normalizeAsyncSessionOptions(assoc *ul.Association, options AsyncSessionOptions) (AsyncSessionOptions, int, int, error) {
	if options.MaxInvokedOperations < 0 || options.MaxPerformedOperations < 0 || options.ResponseQueueDepth < 0 || options.MaxDataSetBytes < 0 || options.MaxQueuedMessageBytes < 0 || options.MaxDataSetElements < 0 || options.MaxDataSetDepth < 0 || options.CancelDrainTimeout < 0 || options.MaxPendingRequests < 0 {
		return options, 0, 0, fmt.Errorf("dicom dimse: asynchronous session limits must not be negative")
	}
	if options.MaxInvokedOperations > maxAsyncOperationLimit || options.MaxPerformedOperations > maxAsyncOperationLimit {
		return options, 0, 0, fmt.Errorf("dicom dimse: asynchronous local operation limit exceeds %d", maxAsyncOperationLimit)
	}
	if options.ResponseQueueDepth > maxAsyncResponseQueue {
		return options, 0, 0, fmt.Errorf("dicom dimse: asynchronous response queue exceeds %d", maxAsyncResponseQueue)
	}
	if options.MaxDataSetDepth > maxAsyncDataSetDepth {
		return options, 0, 0, fmt.Errorf("dicom dimse: asynchronous dataset depth exceeds %d", maxAsyncDataSetDepth)
	}
	if options.ResponseQueueDepth == 0 {
		options.ResponseQueueDepth = defaultAsyncResponseQueue
	}
	if options.MaxDataSetBytes == 0 {
		options.MaxDataSetBytes = defaultAsyncDataSetBytes
	}
	if options.MaxQueuedMessageBytes == 0 {
		options.MaxQueuedMessageBytes = defaultAsyncQueuedBytes
	}
	if options.MaxDataSetElements == 0 {
		options.MaxDataSetElements = defaultAsyncDataSetElements
	}
	if options.MaxDataSetDepth == 0 {
		options.MaxDataSetDepth = defaultAsyncDataSetDepth
	}
	if options.CancelDrainTimeout == 0 {
		options.CancelDrainTimeout = defaultAsyncCancelDrain
	}
	if options.MaxPendingRequests == 0 {
		options.MaxPendingRequests = defaultAsyncPendingRequests
	}
	if len(options.CGetStorageSOPClassUIDs) == 0 {
		options.CGetStorageSOPClassUIDs = DefaultStorageSOPClassUIDs()
	} else {
		options.CGetStorageSOPClassUIDs = append([]string(nil), options.CGetStorageSOPClassUIDs...)
	}
	window := assoc.EffectiveAsynchronousOperationsWindow()
	invoked := effectiveAsyncLocalLimit(window.MaximumInvoked, options.MaxInvokedOperations)
	performed := 0
	if window.MaximumPerformed == 0 {
		performed = effectiveAsyncLocalLimit(0, options.MaxPerformedOperations)
	} else {
		performed = int(window.MaximumPerformed)
		if options.MaxPerformedOperations > 0 && options.MaxPerformedOperations < performed {
			return options, 0, 0, fmt.Errorf("dicom dimse: local performed limit %d is below negotiated peer window %d", options.MaxPerformedOperations, performed)
		}
	}
	if invoked <= 0 || performed <= 0 {
		return options, 0, 0, fmt.Errorf("dicom dimse: asynchronous local limits must be positive")
	}
	return options, invoked, performed, nil
}

func effectiveAsyncLocalLimit(negotiated uint16, configured int) int {
	if negotiated == 0 {
		if configured > 0 {
			return configured
		}
		return defaultAsyncUnlimitedLimit
	}
	limit := int(negotiated)
	if configured > 0 && configured < limit {
		limit = configured
	}
	return limit
}

// Handle registers a persistent request handler by command field. Register
// handlers before the peer can send the corresponding request.
func (s *AsyncSession) Handle(commandField uint16, handler AsyncRequestHandler) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if handler == nil {
		delete(s.handlers, commandField)
		return
	}
	s.handlers[commandField] = handler
}

// Invoke reserves the negotiated invoked window and an association-wide
// Message ID, then sends one message atomically.
func (s *AsyncSession) Invoke(ctx context.Context, request AsyncRequest) (*AsyncOperation, error) {
	if s == nil {
		return nil, ErrAsyncSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandField, err := commandFieldFromElements(request.Command)
	if err != nil || commandField&0x8000 != 0 || commandField == CCancelRQ {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X is not an invocable request", commandField)
	}
	if _, err := s.validateOutgoingMessage(request.PresentationContextID, request.Command, request.DataSet); err != nil {
		return nil, err
	}
	if !s.localMayInvoke(request.PresentationContextID) {
		return nil, ErrAsyncRoleNotAccepted
	}
	if err := s.acquireInvokedSlot(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed || s.releasing {
		releasing := s.releasing
		s.mu.Unlock()
		<-s.invokedSlots
		if releasing {
			return nil, ErrAsyncSessionReleasing
		}
		return nil, s.sessionError()
	}
	messageID, err := s.allocateMessageIDLocked()
	if err != nil {
		s.mu.Unlock()
		<-s.invokedSlots
		return nil, err
	}
	operation := &AsyncOperation{
		session:       s,
		messageID:     messageID,
		pcID:          request.PresentationContextID,
		responseField: commandField | 0x8000,
		cancelable:    asyncCommandAllowsPending(commandField),
		responses:     make(chan AsyncMessage, s.options.ResponseQueueDepth),
		done:          make(chan struct{}),
	}
	s.operations[messageID] = operation
	s.metrics.ActiveInvoked++
	if s.metrics.ActiveInvoked > s.metrics.PeakInvoked {
		s.metrics.PeakInvoked = s.metrics.ActiveInvoked
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()

	command, err := commandElementsWithMessageID(request.Command, MessageID, messageID)
	if err == nil {
		err = s.sendMessage(ctx, request.PresentationContextID, command, request.DataSet)
	}
	if err != nil {
		if command == nil || errors.Is(err, ul.ErrMessageWriteNotStarted) {
			s.finishOperation(messageID, err)
		} else {
			s.shutdown(err)
		}
		return nil, err
	}
	if ctx.Done() != nil {
		go s.watchOperationContext(ctx, operation)
	}
	return operation, nil
}

// acquireInvokedSlot closes the admission gate atomically with Release. A
// caller waiting for capacity is awakened by every state transition so that a
// concurrently started release cannot leave it blocked behind active work.
func (s *AsyncSession) acquireInvokedSlot(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.closed || s.releasing {
			releasing := s.releasing
			s.mu.Unlock()
			if releasing {
				return ErrAsyncSessionReleasing
			}
			return s.sessionError()
		}
		changed := s.stateChanged
		s.mu.Unlock()

		select {
		case s.invokedSlots <- struct{}{}:
			return nil
		case <-changed:
			continue
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return s.sessionError()
		}
	}
}

func (s *AsyncSession) watchOperationContext(ctx context.Context, operation *AsyncOperation) {
	select {
	case <-operation.done:
		return
	case <-ctx.Done():
	}
	controlCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if operation.cancelable {
		if err := operation.Cancel(controlCtx); err != nil && !errors.Is(err, ErrAsyncOperationComplete) && !errors.Is(err, ErrAsyncSessionClosed) && !errors.Is(err, net.ErrClosed) {
			s.shutdown(err)
		}
		return
	}
	operation.cancelMu.Lock()
	operation.mu.Lock()
	terminalSeen := operation.terminalSeen
	operation.mu.Unlock()
	if terminalSeen {
		operation.cancelMu.Unlock()
		return
	}
	_ = s.Abort(controlCtx)
	operation.cancelMu.Unlock()
}

// StartCEcho invokes C-ECHO using the accepted Verification context.
func (s *AsyncSession) StartCEcho(ctx context.Context) (*AsyncOperation, error) {
	if s == nil {
		return nil, ErrAsyncSessionClosed
	}
	pc, ok := AcceptedVerificationContext(s.assoc)
	if !ok {
		return nil, fmt.Errorf("dicom dimse: verification presentation context not accepted")
	}
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pc.ID, Command: (CEchoRequest{}).CommandSet()})
}

// StartCStore invokes C-STORE and sends dataset as the same atomic message.
func (s *AsyncSession) StartCStore(ctx context.Context, pcID byte, request CStoreRequest, dataset *object.Object) (*AsyncOperation, error) {
	ctx, cancel := s.suboperationContext(ctx)
	operation, err := s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: dataset})
	if err != nil {
		cancel()
		return nil, err
	}
	operation.setContextCancel(cancel)
	return operation, nil
}

// StartCFind invokes a cancelable C-FIND operation.
func (s *AsyncSession) StartCFind(ctx context.Context, pcID byte, request CFindRequest, identifier *object.Object) (*AsyncOperation, error) {
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: identifier})
}

// StartCMove invokes a cancelable C-MOVE operation.
func (s *AsyncSession) StartCMove(ctx context.Context, pcID byte, request CMoveRequest, identifier *object.Object) (*AsyncOperation, error) {
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: identifier})
}

// StartCGet invokes a cancelable C-GET operation. Reverse C-STORE requests are
// dispatched through the same session and consume its performed window.
func (s *AsyncSession) StartCGet(ctx context.Context, pcID byte, request CGetRequest, identifier *object.Object) (*AsyncOperation, error) {
	if !s.localHasReverseStoreRole(request.AffectedSOPClassUID) {
		return nil, ErrCGetStorageRoleNotAccepted
	}
	s.mu.Lock()
	storeHandler := s.handlers[CStoreRQ]
	s.mu.Unlock()
	if storeHandler == nil {
		return nil, fmt.Errorf("dicom dimse: C-GET requires a reverse C-STORE handler")
	}
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: request.CommandSet(), DataSet: identifier})
}

// StartNormalized invokes any DIMSE-N request. The caller supplies one of the
// existing Normalized*Request command sets and its optional dataset.
func (s *AsyncSession) StartNormalized(ctx context.Context, pcID byte, command []core.Element, dataSet *object.Object) (*AsyncOperation, error) {
	return s.Invoke(ctx, AsyncRequest{PresentationContextID: pcID, Command: command, DataSet: dataSet})
}

// Respond sends one response atomically and forces its correlation ID to the
// incoming request's Message ID.
func (s *AsyncSession) Respond(ctx context.Context, request AsyncMessage, command []core.Element, dataset *object.Object) error {
	if s == nil {
		return ErrAsyncSessionClosed
	}
	commandField, err := commandFieldFromElements(command)
	if err != nil {
		return err
	}
	if commandField&0x8000 == 0 {
		return fmt.Errorf("dicom dimse: command field 0x%04X is not a response", commandField)
	}
	if commandField != request.CommandField|0x8000 {
		return fmt.Errorf("dicom dimse: response field 0x%04X does not match request field 0x%04X", commandField, request.CommandField)
	}
	status, err := commandUint16FromElements(command, Status)
	if err != nil {
		return err
	}
	s.mu.Lock()
	incoming, active := s.incoming[request.MessageID]
	s.mu.Unlock()
	if !active || incoming.requestField != request.CommandField || incoming.pcID != request.PresentationContextID || incoming.generation != request.incomingGeneration {
		return ErrAsyncOperationComplete
	}
	incoming.responseMu.Lock()
	defer incoming.responseMu.Unlock()
	s.mu.Lock()
	incoming, active = s.incoming[request.MessageID]
	s.mu.Unlock()
	if !active || incoming.requestField != request.CommandField || incoming.pcID != request.PresentationContextID || incoming.generation != request.incomingGeneration || incoming.finishing {
		return ErrAsyncOperationComplete
	}
	command, err = commandElementsWithMessageID(command, MessageIDBeingRespondedTo, request.MessageID)
	if err != nil {
		return err
	}
	pending := asyncCommandStatusIsPending(request.CommandField, status)
	if (status == StatusPending || status == StatusPendingWarning) && !pending {
		return fmt.Errorf("dicom dimse: pending status 0x%04X is invalid for command field 0x%04X", status, request.CommandField)
	}
	terminal := !pending
	ctx, cancelResponse := s.responseContext(ctx)
	defer cancelResponse()
	syntax, err := s.validateOutgoingMessage(request.PresentationContextID, command, dataset)
	if err != nil {
		return err
	}
	if terminal {
		s.mu.Lock()
		current, ok := s.incoming[request.MessageID]
		if !ok || current.requestField != request.CommandField || current.generation != request.incomingGeneration || current.finishing {
			s.mu.Unlock()
			return ErrAsyncOperationComplete
		}
		current.finishing = true
		s.incoming[request.MessageID] = current
		s.signalStateChangedLocked()
		s.mu.Unlock()
	}
	if err := s.writeValidatedMessage(ctx, request.PresentationContextID, command, dataset, syntax, func() {
		if terminal {
			s.markIncomingWriteStarted(request.MessageID, request.CommandField, request.incomingGeneration)
		}
	}, func() {
		if terminal {
			s.markIncomingWriteCompleted(request.MessageID, request.CommandField, request.incomingGeneration)
			s.finishIncomingOperation(request.MessageID, request.CommandField, request.incomingGeneration)
		}
	}); err != nil {
		if terminal && errors.Is(err, ul.ErrMessageWriteNotStarted) {
			s.mu.Lock()
			if current, ok := s.incoming[request.MessageID]; ok && current.requestField == request.CommandField && current.generation == request.incomingGeneration {
				current.finishing = false
				current.writeStarted = false
				current.writeCompleted = false
				s.incoming[request.MessageID] = current
				s.signalStateChangedLocked()
			}
			s.mu.Unlock()
			return err
		}
		s.shutdown(err)
		return err
	}
	return nil
}

func (s *AsyncSession) sendMessage(ctx context.Context, pcID byte, command []core.Element, dataset *object.Object) error {
	if ctx == nil {
		ctx = context.Background()
	}
	syntax, err := s.validateOutgoingMessage(pcID, command, dataset)
	if err != nil {
		return err
	}
	return s.writeValidatedMessage(ctx, pcID, command, dataset, syntax, nil, nil)
}

func (s *AsyncSession) validateOutgoingMessage(pcID byte, command []core.Element, dataset *object.Object) (transfer.Syntax, error) {
	commandField, err := commandFieldFromElements(command)
	if err != nil {
		return transfer.Syntax{}, err
	}
	if err := validateOutgoingCorrelation(object.FromElements(command, nil), commandField); err != nil {
		return transfer.Syntax{}, err
	}
	if err := validateAsyncCommandPresentationContext(s.assoc, pcID, object.FromElements(command, nil)); err != nil {
		return transfer.Syntax{}, err
	}
	dataSetType, err := commandUint16FromElements(command, CommandDataSetType)
	if err != nil {
		return transfer.Syntax{}, err
	}
	if dataSetType == NoDataSet && dataset != nil {
		return transfer.Syntax{}, fmt.Errorf("dicom dimse: command forbids accompanying dataset")
	}
	if dataSetType != NoDataSet && dataset == nil {
		return transfer.Syntax{}, fmt.Errorf("dicom dimse: command requires accompanying dataset")
	}
	syntax, err := AcceptedTransferSyntax(s.assoc, pcID)
	if err != nil {
		return transfer.Syntax{}, err
	}
	return syntax, nil
}

func (s *AsyncSession) writeValidatedMessage(ctx context.Context, pcID byte, command []core.Element, dataset *object.Object, syntax transfer.Syntax, beforeWrite, afterWrite func()) error {
	ctx = s.owner.Context(ctx)
	return s.assoc.SerializeMessageWriteContext(ctx, func() error {
		if beforeWrite != nil {
			beforeWrite()
		}
		if err := SendCommandSetWithContext(ctx, s.assoc, pcID, command); err != nil {
			return err
		}
		if dataset != nil {
			if err := SendDataSetWithContext(ctx, s.assoc, pcID, dataset, syntax); err != nil {
				return err
			}
		}
		if afterWrite != nil {
			afterWrite()
		}
		return nil
	})
}

func (s *AsyncSession) responseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx != nil && ctx.Value(asyncHandlerSessionContextKey{}) == s {
		return s.suboperationContext(ctx)
	}
	if ctx != nil {
		return ctx, func() {}
	}
	return s.ctx, func() {}
}

// suboperationContext detaches a reverse, non-cancelable DIMSE sub-operation
// from the parent handler's C-CANCEL signal while preserving an explicit
// deadline. This lets an outstanding C-STORE finish after C-GET cancellation
// without turning a caller's bounded wait into an unbounded one.
func (s *AsyncSession) suboperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	noop := func() {}
	if s == nil || ctx == nil || ctx.Value(asyncHandlerSessionContextKey{}) != s {
		return ctx, noop
	}
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(s.ctx, deadline)
	}
	return s.ctx, noop
}

// Next returns the next response, including each Pending response and its
// dataset. After the terminal response, the next call returns io.EOF.
func (o *AsyncOperation) Next(ctx context.Context) (AsyncMessage, error) {
	if o == nil {
		return AsyncMessage{}, ErrAsyncSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !o.cancelable {
		var cancel context.CancelFunc
		ctx, cancel = o.session.suboperationContext(ctx)
		defer cancel()
	}
	select {
	case message := <-o.responses:
		o.releaseResponseAccounting(message.messageBytes)
		return message, nil
	default:
	}
	select {
	case message := <-o.responses:
		o.releaseResponseAccounting(message.messageBytes)
		return message, nil
	case <-ctx.Done():
		return AsyncMessage{}, ctx.Err()
	case <-o.done:
		select {
		case message := <-o.responses:
			o.releaseResponseAccounting(message.messageBytes)
			return message, nil
		default:
			return AsyncMessage{}, o.operationError()
		}
	}
}

// Wait drains responses and returns the terminal message.
func (o *AsyncOperation) Wait(ctx context.Context) (AsyncMessage, error) {
	var terminal AsyncMessage
	for {
		message, err := o.Next(ctx)
		if err == nil {
			terminal = message
			continue
		}
		if errors.Is(err, io.EOF) && terminal.Command != nil {
			return terminal, nil
		}
		return AsyncMessage{}, err
	}
}

// DiscardResponses transfers ownership of all queued responses away from the
// session without returning them. Call it when an operation will not be
// drained with Next or Wait; otherwise its bounded mailbox remains accounted
// to MaxQueuedMessageBytes for as long as the operation is retained.
func (o *AsyncOperation) DiscardResponses() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.discarding = true
	o.mu.Unlock()
	for {
		select {
		case message := <-o.responses:
			o.releaseResponseAccounting(message.messageBytes)
		default:
			return
		}
	}
}

// Cancel sends C-CANCEL-RQ for a cancelable operation. Its Message ID and
// invoked slot remain reserved until the peer sends a terminal response.
func (o *AsyncOperation) Cancel(ctx context.Context) error {
	if o == nil || o.session == nil {
		return ErrAsyncSessionClosed
	}
	if !o.cancelable {
		return ErrAsyncOperationNotCancelable
	}
	o.cancelMu.Lock()
	defer o.cancelMu.Unlock()
	o.mu.Lock()
	terminalSeen := o.terminalSeen
	o.mu.Unlock()
	if terminalSeen {
		return ErrAsyncOperationComplete
	}
	select {
	case <-o.done:
		return ErrAsyncOperationComplete
	default:
	}
	if o.cancelSent {
		return nil
	}
	command := (CCancelRequest{MessageIDBeingRespondedTo: o.messageID}).CommandSet()
	if err := o.session.sendMessage(ctx, o.pcID, command, nil); err != nil {
		// The gate can reject this attempt before its callback starts. No bytes
		// reached the wire, so a later Cancel call may safely retry without
		// invalidating other operations on the association.
		if !errors.Is(err, ul.ErrMessageWriteNotStarted) {
			o.session.shutdown(err)
		}
		return err
	}
	o.cancelSent = true
	o.session.startCancelDrainTimer(o)
	return nil
}

func (s *AsyncSession) startCancelDrainTimer(operation *AsyncOperation) {
	if s == nil || operation == nil {
		return
	}
	operation.drainOnce.Do(func() {
		go func() {
			timer := time.NewTimer(s.options.CancelDrainTimeout)
			defer timer.Stop()
			select {
			case <-operation.done:
				return
			case <-s.done:
				return
			case <-timer.C:
				abortCtx, abortCancel := context.WithTimeout(context.Background(), time.Second)
				defer abortCancel()
				_ = s.Abort(abortCtx)
			}
		}()
	})
}

// MessageID returns the operation's association-wide correlation identifier.
func (o *AsyncOperation) MessageID() uint16 {
	if o == nil {
		return 0
	}
	return o.messageID
}

// Done closes after a terminal response or session failure.
func (o *AsyncOperation) Done() <-chan struct{} {
	if o == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return o.done
}

func (o *AsyncOperation) operationError() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	return io.EOF
}

func (o *AsyncOperation) releaseResponseAccounting(size int64) {
	if o == nil || o.session == nil || size <= 0 {
		return
	}
	o.mu.Lock()
	release := !o.accountingTransferred
	if release {
		o.queuedBytes -= size
		if o.queuedBytes < 0 {
			o.queuedBytes = 0
		}
	}
	o.mu.Unlock()
	if release {
		o.session.releaseMessageBytes(size)
	}
}

func (o *AsyncOperation) transferResponseAccounting() int64 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.accountingTransferred {
		return 0
	}
	o.accountingTransferred = true
	size := o.queuedBytes
	o.queuedBytes = 0
	return size
}

func (o *AsyncOperation) finish(err error) {
	o.finishOnce.Do(func() {
		o.mu.Lock()
		o.err = err
		o.finished = true
		cancel := o.contextCancel
		o.contextCancel = nil
		o.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		close(o.done)
	})
}

func (o *AsyncOperation) setContextCancel(cancel context.CancelFunc) {
	if o == nil || cancel == nil {
		return
	}
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		cancel()
		return
	}
	o.contextCancel = cancel
	o.mu.Unlock()
}

func (s *AsyncSession) readLoop() {
	for {
		pcID, command, commandBytes, control, err := receiveDIMSECommandOrControl(s.owner.Context(s.ctx), s.assoc)
		if err != nil {
			s.shutdown(err)
			return
		}
		switch control := control.(type) {
		case nil:
		case *ul.ReleaseRQ:
			if s.assoc.IsAssociationRequestor {
				err := fmt.Errorf("%w: association acceptor attempted A-RELEASE-RQ", ul.ErrUnexpectedPDU)
				s.shutdown(err)
				return
			}
			s.mu.Lock()
			s.releasing = true
			s.signalStateChangedLocked()
			s.mu.Unlock()
			if err := s.waitForPeerInvocationsConfirmed(); err != nil {
				err := fmt.Errorf("%w: A-RELEASE-RQ received with outstanding DIMSE operations", ul.ErrUnexpectedPDU)
				s.shutdown(err)
				return
			}
			wireCtx := s.owner.Context(s.ctx)
			err := s.assoc.SerializeMessageWriteContext(wireCtx, func() error { return s.assoc.Send(wireCtx, &ul.ReleaseRP{}) })
			if err == nil {
				err = ErrAssociationReleased
			}
			s.shutdown(err)
			return
		case *ul.ReleaseRP:
			s.mu.Lock()
			expected := s.releaseSent && s.assoc.IsAssociationRequestor
			s.mu.Unlock()
			if !expected {
				err := fmt.Errorf("%w: unsolicited A-RELEASE-RP", ul.ErrUnexpectedPDU)
				s.shutdown(err)
				return
			}
			s.shutdownWithRelease(ErrAssociationReleased, nil)
			return
		case *ul.AbortRQ:
			err := &ul.AbortError{Source: control.Source, Reason: control.Reason}
			s.shutdown(err)
			return
		default:
			err := fmt.Errorf("%w: got %T at DIMSE message boundary", ul.ErrUnexpectedPDU, control)
			s.shutdown(err)
			return
		}
		if command == nil {
			continue
		}
		field, fieldErr := CommandUint16(command, CommandField)
		if fieldErr != nil {
			s.shutdown(fieldErr)
			return
		}
		requestDirection := field&0x8000 == 0 && field != CCancelRQ
		prepared, automaticStatus, generation := false, uint16(0), uint64(0)
		if requestDirection {
			prepared, automaticStatus, generation, err = s.prepareReceivedRequest(pcID, command)
			if err != nil {
				s.shutdown(err)
				return
			}
		}
		message, err := s.assembleAsyncMessage(pcID, command, commandBytes)
		if err != nil {
			if prepared {
				s.finishIncomingOperation(messageIDOrZero(command), field, generation)
			}
			s.shutdown(err)
			return
		}
		message.incomingGeneration = generation
		if prepared && !s.attachIncomingMessageBytes(message.MessageID, message.CommandField, generation, message.messageBytes) {
			s.releaseMessageBytes(message.messageBytes)
			s.shutdown(ErrAsyncOperationComplete)
			return
		}
		if requestDirection {
			if automaticStatus != 0 {
				err = s.sendAutomaticResponse(message, automaticStatus)
				s.releaseMessageBytes(message.messageBytes)
			} else {
				err = s.dispatchPreparedRequest(message)
			}
		} else {
			err = s.routeMessage(message)
		}
		if err != nil {
			if !prepared && automaticStatus == 0 {
				s.releaseMessageBytes(message.messageBytes)
			}
			s.shutdown(err)
			return
		}
	}
}

// waitForPeerInvocationsConfirmed closes the small boundary between the peer
// receiving a terminal response and the successful write callback removing
// that request from the registry. Non-terminal work still makes release a
// protocol error; terminal writes already in flight are allowed to publish
// completion before A-RELEASE-RP is sent.
func (s *AsyncSession) waitForPeerInvocationsConfirmed() error {
	for {
		s.mu.Lock()
		active := false
		for _, incoming := range s.incoming {
			if !incoming.finishing {
				active = true
				break
			}
		}
		if active || len(s.incoming) == 0 {
			s.mu.Unlock()
			if active {
				return ErrAsyncOperationsActive
			}
			return nil
		}
		changed := s.stateChanged
		s.mu.Unlock()
		select {
		case <-changed:
		case <-s.done:
			return s.sessionError()
		}
	}
}

func (s *AsyncSession) assembleAsyncMessage(pcID byte, command *object.Object, commandBytes int64) (message AsyncMessage, err error) {
	if commandBytes <= 0 {
		return AsyncMessage{}, fmt.Errorf("dicom dimse: invalid assembled command size %d", commandBytes)
	}
	if err := s.reserveMessageBytes(commandBytes); err != nil {
		return AsyncMessage{}, err
	}
	message.messageBytes = commandBytes
	defer func() {
		if err != nil {
			s.releaseMessageBytes(message.messageBytes)
			message = AsyncMessage{}
		}
	}()
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return AsyncMessage{}, err
	}
	messageID, err := asyncMessageID(command, field)
	if err != nil {
		return AsyncMessage{}, err
	}
	dataSetType, err := CommandUint16(command, CommandDataSetType)
	if err != nil {
		return AsyncMessage{}, err
	}
	message.PresentationContextID = pcID
	message.CommandField = field
	message.MessageID = messageID
	message.Command = command
	if err := validateAsyncCommandPresentationContext(s.assoc, pcID, command); err != nil {
		return AsyncMessage{}, err
	}
	if dataSetType != NoDataSet {
		syntax, err := AcceptedTransferSyntax(s.assoc, pcID)
		if err != nil {
			return AsyncMessage{}, err
		}
		maxBytes := s.availableDataSetBytes()
		if maxBytes <= 0 {
			return AsyncMessage{}, ErrAsyncResourceLimit
		}
		var dataSetBytes int64
		message.DataSet, dataSetBytes, err = receiveDataSetWithContextLimitsAndSize(s.owner.Context(s.ctx), s.assoc, pcID, syntax, maxBytes, s.options.MaxDataSetElements, s.options.MaxDataSetDepth)
		if err != nil {
			return AsyncMessage{}, err
		}
		if err := s.reserveMessageBytes(dataSetBytes); err != nil {
			return AsyncMessage{}, err
		}
		message.messageBytes += dataSetBytes
		trailing, err := takePDataCarryoverWithContext(s.owner.Context(s.ctx), s.assoc)
		if err != nil {
			return AsyncMessage{}, err
		}
		if len(trailing) != 0 {
			return AsyncMessage{}, fmt.Errorf("%w: multiple DIMSE messages in one P-DATA-TF", ul.ErrUnexpectedPDU)
		}
	} else {
		trailing, err := takePDataCarryoverWithContext(s.owner.Context(s.ctx), s.assoc)
		if err != nil {
			return AsyncMessage{}, err
		}
		if len(trailing) != 0 {
			return AsyncMessage{}, fmt.Errorf("%w: multiple DIMSE messages in one P-DATA-TF", ul.ErrUnexpectedPDU)
		}
	}
	return message, nil
}

func (s *AsyncSession) availableDataSetBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := s.options.MaxQueuedMessageBytes - s.queuedMessageBytes
	if remaining > s.options.MaxDataSetBytes {
		remaining = s.options.MaxDataSetBytes
	}
	return remaining
}

func (s *AsyncSession) reserveMessageBytes(size int64) error {
	if size <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if size > s.options.MaxQueuedMessageBytes-s.queuedMessageBytes {
		return ErrAsyncResourceLimit
	}
	s.queuedMessageBytes += size
	s.metrics.QueuedMessageBytes = s.queuedMessageBytes
	if s.queuedMessageBytes > s.metrics.PeakQueuedMessageBytes {
		s.metrics.PeakQueuedMessageBytes = s.queuedMessageBytes
	}
	return nil
}

func (s *AsyncSession) releaseMessageBytes(size int64) {
	if size <= 0 {
		return
	}
	s.mu.Lock()
	s.queuedMessageBytes -= size
	if s.queuedMessageBytes < 0 {
		s.queuedMessageBytes = 0
	}
	s.metrics.QueuedMessageBytes = s.queuedMessageBytes
	s.mu.Unlock()
}

func messageIDOrZero(command *object.Object) uint16 {
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return 0
	}
	id, _ := asyncMessageID(command, field)
	return id
}

func validateAsyncCommandPresentationContext(assoc *ul.Association, pcID byte, command *object.Object) error {
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	for _, tag := range []core.Tag{AffectedSOPClassUID, RequestedSOPClassUID} {
		if _, present := command.Get(tag); !present {
			continue
		}
		uid, ok := command.GetUID(tag)
		if !ok || uid == "" {
			return fmt.Errorf("dicom dimse: invalid command SOP Class UID element %s", tag)
		}
		if uid != pc.AbstractSyntaxUID {
			return fmt.Errorf("%w: command SOP Class %q on context %d for %q", ErrPresentationContextMismatch, uid, pcID, pc.AbstractSyntaxUID)
		}
	}
	return nil
}

func asyncMessageID(command *object.Object, commandField uint16) (uint16, error) {
	if command == nil {
		return 0, fmt.Errorf("dicom dimse: nil command set")
	}
	responseDirection := commandField&0x8000 != 0 || commandField == CCancelRQ
	wanted, forbidden := MessageID, MessageIDBeingRespondedTo
	if responseDirection {
		wanted, forbidden = MessageIDBeingRespondedTo, MessageID
	}
	if _, ok := command.Get(forbidden); ok {
		return 0, fmt.Errorf("dicom dimse: command field 0x%04X contains forbidden correlation element %s", commandField, forbidden)
	}
	if _, ok := command.Get(wanted); !ok {
		return 0, fmt.Errorf("dicom dimse: command field 0x%04X missing correlation element %s", commandField, wanted)
	}
	return CommandUint16(command, wanted)
}

func validateOutgoingCorrelation(command *object.Object, commandField uint16) error {
	if command == nil {
		return fmt.Errorf("dicom dimse: nil command set")
	}
	forbidden := MessageIDBeingRespondedTo
	if commandField&0x8000 != 0 || commandField == CCancelRQ {
		forbidden = MessageID
	}
	if _, ok := command.Get(forbidden); ok {
		return fmt.Errorf("dicom dimse: command field 0x%04X contains forbidden correlation element %s", commandField, forbidden)
	}
	return nil
}

func (s *AsyncSession) routeMessage(message AsyncMessage) error {
	if message.CommandField == CCancelRQ {
		if message.DataSet != nil {
			return fmt.Errorf("%w: C-CANCEL-RQ carries a dataset", ErrUnexpectedCommand)
		}
		s.mu.Lock()
		incoming, ok := s.incoming[message.MessageID]
		s.mu.Unlock()
		if ok && incoming.pcID == message.PresentationContextID && incoming.cancelable {
			incoming.cancel()
		}
		s.releaseMessageBytes(message.messageBytes)
		return nil
	}
	if message.CommandField&0x8000 != 0 {
		return s.routeResponse(message)
	}
	return fmt.Errorf("%w: unprepared request field 0x%04X", ErrUnexpectedCommand, message.CommandField)
}

func (s *AsyncSession) routeResponse(message AsyncMessage) error {
	s.mu.Lock()
	operation, ok := s.operations[message.MessageID]
	s.mu.Unlock()
	if !ok {
		return &UnexpectedCommandError{CommandField: message.CommandField, MessageID: message.MessageID}
	}
	if message.CommandField != operation.responseField {
		return fmt.Errorf("%w: response field 0x%04X for request expecting 0x%04X", ErrUnexpectedCommand, message.CommandField, operation.responseField)
	}
	if message.PresentationContextID != operation.pcID {
		return fmt.Errorf("%w: response presentation context %d for request on context %d", ErrPresentationContextMismatch, message.PresentationContextID, operation.pcID)
	}
	status, err := CommandUint16(message.Command, Status)
	if err != nil {
		return err
	}
	requestField := operation.responseField &^ 0x8000
	pending := asyncCommandStatusIsPending(requestField, status)
	if (status == StatusPending || status == StatusPendingWarning) && !pending {
		return fmt.Errorf("%w: pending status 0x%04X for command field 0x%04X", ErrUnexpectedCommand, status, operation.responseField)
	}
	terminal := !pending
	if terminal {
		operation.cancelMu.Lock()
	}
	operation.mu.Lock()
	discarding := operation.discarding
	newlyDiscarding := false
	queued := false
	if !discarding {
		select {
		case operation.responses <- message:
			queued = true
			operation.queuedBytes += message.messageBytes
		default:
			operation.discarding = true
			discarding = true
			newlyDiscarding = true
		}
	}
	if terminal {
		operation.terminalSeen = true
	}
	operation.mu.Unlock()
	if terminal {
		operation.cancelMu.Unlock()
	}
	if newlyDiscarding && !terminal {
		go func() {
			cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := operation.Cancel(cancelCtx); err != nil && !errors.Is(err, ErrAsyncOperationComplete) && !errors.Is(err, ErrAsyncSessionClosed) {
				s.shutdown(err)
			}
		}()
	}
	if discarding || !queued {
		s.releaseMessageBytes(message.messageBytes)
	}
	if terminal {
		if discarding || !queued {
			s.finishOperation(message.MessageID, ErrAsyncResponseQueueFull)
		} else {
			s.finishOperation(message.MessageID, nil)
		}
	}
	return nil
}

func (s *AsyncSession) attachIncomingMessageBytes(messageID, requestField uint16, generation uint64, size int64) bool {
	if size <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.incoming[messageID]
	if !ok || current.requestField != requestField || current.generation != generation {
		return false
	}
	current.messageBytes += size
	s.incoming[messageID] = current
	return true
}

func (s *AsyncSession) prepareIncomingRequest(pcID byte, command *object.Object) (bool, uint16, uint64, error) {
	field, err := CommandUint16(command, CommandField)
	if err != nil {
		return false, 0, 0, err
	}
	messageID, err := asyncMessageID(command, field)
	if err != nil {
		return false, 0, 0, err
	}
	if err := validateAsyncCommandPresentationContext(s.assoc, pcID, command); err != nil {
		return false, 0, 0, err
	}
	if !s.localMayPerform(pcID) {
		return false, 0, 0, ErrAsyncRoleNotAccepted
	}

retry:
	s.mu.Lock()
	if s.releaseSent || s.closed {
		s.mu.Unlock()
		return false, 0, 0, ErrAsyncSessionReleasing
	}
	current, duplicate := s.incoming[messageID]
	if duplicate {
		if current.finishing && current.writeCompleted {
			changed := s.stateChanged
			s.mu.Unlock()
			select {
			case <-changed:
				goto retry
			case <-s.done:
				return false, 0, 0, s.sessionError()
			}
		}
		s.metrics.DuplicateInvocations++
	}
	if !duplicate && s.wirePerformedUnlimited && len(s.incoming) >= s.options.MaxPendingRequests {
		s.mu.Unlock()
		return false, asyncResourceLimitStatus(field), 0, nil
	}
	s.mu.Unlock()
	if duplicate {
		return false, StatusDuplicateInvocation, 0, nil
	}
	slotHeld := false
	if !s.wirePerformedUnlimited && !s.acquirePerformedSlotForRequest() {
		s.mu.Lock()
		s.metrics.WindowViolations++
		s.mu.Unlock()
		return false, 0, 0, ErrAsyncWindowExceeded
	} else if !s.wirePerformedUnlimited {
		slotHeld = true
	}

	s.mu.Lock()
	if s.releaseSent || s.closed {
		s.mu.Unlock()
		if slotHeld {
			<-s.performedSlots
		}
		return false, 0, 0, ErrAsyncSessionReleasing
	}
	if current, duplicate = s.incoming[messageID]; duplicate {
		finishing := current.finishing && current.writeCompleted
		changed := s.stateChanged
		if !finishing {
			s.metrics.DuplicateInvocations++
		}
		s.mu.Unlock()
		if slotHeld {
			<-s.performedSlots
		}
		if finishing {
			select {
			case <-changed:
				goto retry
			case <-s.done:
				return false, 0, 0, s.sessionError()
			}
		}
		return false, StatusDuplicateInvocation, 0, nil
	}
	s.nextIncomingID++
	if s.nextIncomingID == 0 {
		s.nextIncomingID++
	}
	generation := s.nextIncomingID
	handlerCtx, cancel := context.WithCancel(context.WithValue(s.ctx, asyncHandlerSessionContextKey{}, s))
	s.incoming[messageID] = asyncIncomingOperation{
		cancel:       cancel,
		ctx:          handlerCtx,
		requestField: field,
		pcID:         pcID,
		cancelable:   asyncCommandAllowsPending(field),
		responseMu:   &sync.Mutex{},
		slotHeld:     slotHeld,
		generation:   generation,
	}
	s.metrics.ActivePerformed++
	if s.metrics.ActivePerformed > s.metrics.PeakPerformed {
		s.metrics.PeakPerformed = s.metrics.ActivePerformed
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()
	return true, 0, generation, nil
}

// prepareReceivedRequest keeps a peer request visible to release waiters while
// finite-window admission is backpressured and before the request has an
// incoming registry entry of its own.
func (s *AsyncSession) prepareReceivedRequest(pcID byte, command *object.Object) (bool, uint16, uint64, error) {
	s.mu.Lock()
	s.pendingIncoming++
	s.signalStateChangedLocked()
	s.mu.Unlock()

	prepared, status, generation, err := s.prepareIncomingRequest(pcID, command)

	s.mu.Lock()
	if s.pendingIncoming > 0 {
		s.pendingIncoming--
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()
	return prepared, status, generation, err
}

func (s *AsyncSession) dispatchPreparedRequest(message AsyncMessage) error {
	s.mu.Lock()
	incoming, active := s.incoming[message.MessageID]
	handler := s.handlers[message.CommandField]
	s.mu.Unlock()
	if !active || incoming.requestField != message.CommandField || incoming.pcID != message.PresentationContextID || incoming.generation != message.incomingGeneration {
		return ErrAsyncOperationComplete
	}
	go func() {
		defer func() {
			incoming.cancel()
			s.finishIncomingOperation(message.MessageID, message.CommandField, message.incomingGeneration)
		}()
		if !incoming.slotHeld {
			select {
			case s.performedSlots <- struct{}{}:
				s.mu.Lock()
				current, ok := s.incoming[message.MessageID]
				if ok && current.requestField == message.CommandField && current.generation == message.incomingGeneration {
					current.slotHeld = true
					s.incoming[message.MessageID] = current
				} else {
					<-s.performedSlots
				}
				s.mu.Unlock()
				if !ok || current.generation != message.incomingGeneration {
					return
				}
			case <-s.done:
				return
			}
		}
		s.releaseMessageBytes(s.takeIncomingMessageBytes(message.MessageID, message.CommandField, message.incomingGeneration))
		if handler == nil {
			if err := s.respondAutomaticallyToIncoming(message, StatusUnrecognizedOperation); err != nil {
				s.shutdown(err)
			}
			return
		}
		if err := callAsyncRequestHandler(incoming.ctx, handler, s, message); err != nil {
			s.shutdown(err)
			return
		}
		s.mu.Lock()
		current, active := s.incoming[message.MessageID]
		active = active && current.generation == message.incomingGeneration
		s.mu.Unlock()
		if active {
			s.shutdown(ErrAsyncTerminalResponseMissing)
		}
	}()
	return nil
}

func (s *AsyncSession) takeIncomingMessageBytes(messageID, requestField uint16, generation uint64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.incoming[messageID]
	if !ok || current.requestField != requestField || current.generation != generation {
		return 0
	}
	size := current.messageBytes
	current.messageBytes = 0
	s.incoming[messageID] = current
	return size
}

func (s *AsyncSession) localMayInvoke(pcID byte) bool {
	pc, err := AcceptedContextByID(s.assoc, pcID)
	if err != nil {
		return false
	}
	role, found := acceptedRoleSelection(s.assoc, pc.AbstractSyntaxUID)
	if s.assoc.IsAssociationRequestor {
		return !found || role.SCURole
	}
	return found && role.SCPRole
}

func (s *AsyncSession) localMayPerform(pcID byte) bool {
	pc, err := AcceptedContextByID(s.assoc, pcID)
	if err != nil {
		return false
	}
	role, found := acceptedRoleSelection(s.assoc, pc.AbstractSyntaxUID)
	if s.assoc.IsAssociationRequestor {
		return found && role.SCPRole
	}
	return !found || role.SCURole
}

func (s *AsyncSession) localHasReverseStoreRole(querySOPClassUID string) bool {
	if s == nil || s.assoc == nil {
		return false
	}
	for _, pc := range s.assoc.AcceptedContexts {
		if pc.AbstractSyntaxUID == querySOPClassUID {
			continue
		}
		if _, storage := s.cGetStorageSOPClasses[pc.AbstractSyntaxUID]; storage && s.localMayPerform(pc.ID) {
			return true
		}
	}
	return false
}

func acceptedRoleSelection(assoc *ul.Association, sopClassUID string) (ul.RoleSelectionItem, bool) {
	if assoc == nil {
		return ul.RoleSelectionItem{}, false
	}
	for _, role := range assoc.AcceptedRoleSelections {
		if role.SopClassUID == sopClassUID {
			return role, true
		}
	}
	return ul.RoleSelectionItem{}, false
}

func (s *AsyncSession) acquirePerformedSlotForRequest() bool {
	for {
		select {
		case s.performedSlots <- struct{}{}:
			return true
		default:
		}
		s.mu.Lock()
		finishing := false
		for _, incoming := range s.incoming {
			// A compliant peer can observe the terminal bytes and send its next
			// request before the local write callback releases the performed
			// slot. Wait only after the terminal writer entered the message gate;
			// a terminal still waiting for that gate does not excuse an actual
			// negotiated-window violation.
			if incoming.finishing && incoming.writeStarted {
				finishing = true
				break
			}
		}
		changed := s.stateChanged
		s.mu.Unlock()
		if !finishing {
			return false
		}
		select {
		case <-changed:
		case <-s.done:
			return false
		}
	}
}

func callAsyncRequestHandler(ctx context.Context, handler AsyncRequestHandler, session *AsyncSession, message AsyncMessage) (err error) {
	defer func() {
		if recover() != nil {
			err = &AsyncRequestHandlerError{}
		}
	}()
	if err := handler(ctx, session, message); err != nil {
		return &AsyncRequestHandlerError{Err: err}
	}
	return nil
}

func (s *AsyncSession) sendAutomaticResponse(request AsyncMessage, status uint16) error {
	return s.sendMessage(s.ctx, request.PresentationContextID, automaticResponseCommand(request, status), nil)
}

func (s *AsyncSession) respondAutomaticallyToIncoming(request AsyncMessage, status uint16) error {
	return s.Respond(s.ctx, request, automaticResponseCommand(request, status), nil)
}

func automaticResponseCommand(request AsyncMessage, status uint16) []core.Element {
	command := []core.Element{}
	if uid, ok := request.Command.GetUID(AffectedSOPClassUID); ok && uid != "" {
		command = append(command, newUIElement(AffectedSOPClassUID, uid))
	} else if uid, ok := request.Command.GetUID(RequestedSOPClassUID); ok && uid != "" {
		command = append(command, newUIElement(AffectedSOPClassUID, uid))
	}
	command = append(command,
		newUSCommandElement(CommandField, request.CommandField|0x8000),
		newUSCommandElement(MessageIDBeingRespondedTo, request.MessageID),
		newUSCommandElement(CommandDataSetType, NoDataSet),
		newUSCommandElement(Status, status),
	)
	if uid, ok := request.Command.GetUID(AffectedSOPInstanceUID); ok && uid != "" {
		command = append(command, newUIElement(AffectedSOPInstanceUID, uid))
	} else if uid, ok := request.Command.GetUID(RequestedSOPInstanceUID); ok && uid != "" {
		command = append(command, newUIElement(AffectedSOPInstanceUID, uid))
	}
	for _, tag := range []core.Tag{EventTypeID, ActionTypeID} {
		if value, err := CommandUint16(request.Command, tag); err == nil {
			command = append(command, newUSCommandElement(tag, value))
		}
	}
	return command
}

func (s *AsyncSession) markIncomingWriteStarted(messageID, requestField uint16, generation uint64) {
	s.mu.Lock()
	current, ok := s.incoming[messageID]
	if ok && current.requestField == requestField && current.generation == generation && current.finishing {
		current.writeStarted = true
		s.incoming[messageID] = current
		s.signalStateChangedLocked()
	}
	s.mu.Unlock()
}

// finishIncomingOperation is idempotent and is called after a terminal
// response is fully written, while the message-level writer is still held.
func (s *AsyncSession) markIncomingWriteCompleted(messageID, requestField uint16, generation uint64) {
	s.mu.Lock()
	current, ok := s.incoming[messageID]
	if ok && current.requestField == requestField && current.generation == generation && current.finishing {
		current.writeCompleted = true
		s.incoming[messageID] = current
		s.signalStateChangedLocked()
	}
	s.mu.Unlock()
}

func (s *AsyncSession) finishIncomingOperation(messageID, requestField uint16, generation uint64) bool {
	s.mu.Lock()
	current, ok := s.incoming[messageID]
	if !ok || current.requestField != requestField || current.generation != generation {
		s.mu.Unlock()
		return false
	}
	delete(s.incoming, messageID)
	s.metrics.ActivePerformed--
	if current.slotHeld {
		<-s.performedSlots
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()
	s.releaseMessageBytes(current.messageBytes)
	return true
}

func (s *AsyncSession) allocateMessageIDLocked() (uint16, error) {
	for attempts := 0; attempts < 65535; attempts++ {
		id := s.nextMessageID
		if id == 0 {
			id = 1
		}
		s.nextMessageID = id + 1
		if _, exists := s.operations[id]; !exists {
			return id, nil
		}
	}
	return 0, ErrAsyncMessageIDExhausted
}

func (s *AsyncSession) finishOperation(messageID uint16, err error) {
	s.mu.Lock()
	operation, ok := s.operations[messageID]
	s.mu.Unlock()
	if !ok {
		return
	}
	operation.cancelMu.Lock()
	defer operation.cancelMu.Unlock()
	s.mu.Lock()
	if current, stillActive := s.operations[messageID]; !stillActive || current != operation {
		s.mu.Unlock()
		return
	}
	delete(s.operations, messageID)
	s.metrics.ActiveInvoked--
	s.signalStateChangedLocked()
	s.mu.Unlock()
	<-s.invokedSlots
	operation.finish(err)
}

// Snapshot returns exact local concurrency and protocol-violation counters.
func (s *AsyncSession) Snapshot() AsyncSessionMetrics {
	if s == nil {
		return AsyncSessionMetrics{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metrics
}

// Release shuts down the session according to policy. Normal release is only
// valid for the association requestor and never races the session's reader.
func (s *AsyncSession) Release(ctx context.Context, policy AsyncReleasePolicy) error {
	if s == nil {
		return nil
	}
	if policy == AsyncReleaseAbort {
		return s.Abort(ctx)
	}
	if !s.assoc.IsAssociationRequestor {
		return ErrAsyncReleaseNotRequestor
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return s.sessionError()
		}
		if !s.releasing {
			s.releasing = true
			s.signalStateChangedLocked()
			s.mu.Unlock()
			break
		}
		changed := s.stateChanged
		s.mu.Unlock()
		select {
		case <-changed:
			continue
		case <-s.releaseDone:
			return s.releaseError()
		case <-s.done:
			return s.sessionError()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if policy == AsyncReleaseRejectIfActive {
		s.mu.Lock()
		active := len(s.operations) != 0 || len(s.incoming) != 0 || s.pendingIncoming != 0
		if active {
			s.releasing = false
			s.signalStateChangedLocked()
		} else {
			s.releaseSent = true
		}
		s.mu.Unlock()
		if active {
			return ErrAsyncOperationsActive
		}
	} else if err := s.waitIdleAndCommitRelease(ctx); err != nil {
		s.mu.Lock()
		if !s.closed {
			s.releasing = false
			s.releaseSent = false
			s.signalStateChangedLocked()
		}
		s.mu.Unlock()
		return err
	}
	wireCtx := s.owner.Context(ctx)
	if err := s.assoc.SerializeMessageWriteContext(wireCtx, func() error { return s.assoc.Send(wireCtx, &ul.ReleaseRQ{}) }); err != nil {
		if errors.Is(err, ul.ErrMessageWriteNotStarted) {
			s.abortMu.Lock()
			aborting := s.abortStarted
			s.abortMu.Unlock()
			s.mu.Lock()
			if !s.closed && !aborting {
				s.releasing = false
				s.releaseSent = false
				s.signalStateChangedLocked()
			}
			s.mu.Unlock()
			return err
		}
		s.shutdown(err)
		return err
	}
	select {
	case <-s.releaseDone:
		return s.releaseError()
	case <-ctx.Done():
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Abort(cleanupCtx)
		return ctx.Err()
	}
}

func (s *AsyncSession) waitIdleAndCommitRelease(ctx context.Context) error {
	for {
		s.mu.Lock()
		if len(s.operations) == 0 && len(s.incoming) == 0 && s.pendingIncoming == 0 {
			s.releaseSent = true
			s.signalStateChangedLocked()
			s.mu.Unlock()
			return nil
		}
		changed := s.stateChanged
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return s.sessionError()
		}
	}
}

// Abort sends A-ABORT, closes the owned association, and wakes all waiters.
func (s *AsyncSession) Abort(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.abortMu.Lock()
	if s.abortStarted {
		done := s.abortDone
		s.abortMu.Unlock()
		select {
		case <-done:
			s.abortMu.Lock()
			err := s.abortErr
			s.abortMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	closed := s.closed
	if !closed {
		s.releasing = true
		s.signalStateChangedLocked()
	}
	s.mu.Unlock()
	if closed {
		s.abortMu.Unlock()
		return nil
	}
	s.abortStarted = true
	if s.abortDone == nil {
		s.abortDone = make(chan struct{})
	}
	s.abortMu.Unlock()
	wireCtx := s.owner.Context(ctx)
	err := s.assoc.SerializeMessageWriteContext(wireCtx, func() error {
		return s.assoc.Send(wireCtx, &ul.AbortRQ{Source: ul.AbortSourceServiceUser, Reason: ul.AbortReasonNotSpecified})
	})
	closeErr := s.assoc.Close()
	s.shutdown(ErrAsyncSessionClosed)
	result := closeErr
	if err != nil {
		result = err
	}
	s.abortMu.Lock()
	s.abortErr = result
	close(s.abortDone)
	s.abortMu.Unlock()
	return result
}

// Close aborts the owned association. Use Release for graceful shutdown.
func (s *AsyncSession) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return s.Abort(ctx)
}

// Done closes when the session stops.
func (s *AsyncSession) Done() <-chan struct{} {
	if s == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.done
}

// Err returns the terminal session error, or nil while the session is active.
func (s *AsyncSession) Err() error {
	if s == nil {
		return ErrAsyncSessionClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *AsyncSession) shutdown(err error) {
	s.shutdownWithRelease(err, err)
}

func (s *AsyncSession) shutdownWithRelease(err, releaseErr error) {
	if err == nil {
		err = ErrAsyncSessionClosed
	}
	s.shutdownOnce.Do(func() {
		s.cancel()
		_ = s.assoc.Close()
		s.mu.Lock()
		s.closed = true
		s.closeErr = err
		operations := make([]*AsyncOperation, 0, len(s.operations))
		for _, operation := range s.operations {
			operations = append(operations, operation)
		}
		s.operations = make(map[uint16]*AsyncOperation)
		incomingOperations := make([]asyncIncomingOperation, 0, len(s.incoming))
		for _, incoming := range s.incoming {
			incomingOperations = append(incomingOperations, incoming)
		}
		s.incoming = make(map[uint16]asyncIncomingOperation)
		s.metrics.ActiveInvoked = 0
		s.metrics.ActivePerformed = 0
		s.queuedMessageBytes = 0
		s.metrics.QueuedMessageBytes = 0
		s.signalStateChangedLocked()
		s.mu.Unlock()
		for range operations {
			<-s.invokedSlots
		}
		for _, operation := range operations {
			_ = operation.transferResponseAccounting()
			operation.finish(err)
		}
		for _, incoming := range incomingOperations {
			incoming.cancel()
			if incoming.slotHeld {
				<-s.performedSlots
			}
		}
		s.owner.End()
		close(s.done)
		s.signalRelease(releaseErr)
	})
}

func (s *AsyncSession) signalRelease(err error) {
	s.releaseOnce.Do(func() {
		s.mu.Lock()
		s.releaseErr = err
		s.mu.Unlock()
		close(s.releaseDone)
	})
}

func (s *AsyncSession) releaseError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseErr
}

func (s *AsyncSession) sessionError() error {
	if s == nil {
		return ErrAsyncSessionClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeErr != nil {
		return s.closeErr
	}
	return ErrAsyncSessionClosed
}

func (s *AsyncSession) signalStateChangedLocked() {
	close(s.stateChanged)
	s.stateChanged = make(chan struct{})
}

func commandFieldFromElements(elements []core.Element) (uint16, error) {
	return commandUint16FromElements(elements, CommandField)
}

func asyncCommandAllowsPending(commandField uint16) bool {
	switch commandField {
	case CFindRQ, CMoveRQ, CGetRQ:
		return true
	default:
		return false
	}
}

func asyncCommandStatusIsPending(commandField, status uint16) bool {
	switch commandField {
	case CFindRQ:
		return status == StatusPending || status == StatusPendingWarning
	case CMoveRQ, CGetRQ:
		return status == StatusPending
	default:
		return false
	}
}

func asyncResourceLimitStatus(commandField uint16) uint16 {
	switch commandField {
	case CStoreRQ, CFindRQ:
		return 0xA700
	case CMoveRQ, CGetRQ:
		return 0xA701
	default:
		return StatusProcessingFailure
	}
}

func commandUint16FromElements(elements []core.Element, tag core.Tag) (uint16, error) {
	return CommandUint16(object.FromElements(elements, nil), tag)
}

func commandElementsWithMessageID(elements []core.Element, tag core.Tag, messageID uint16) ([]core.Element, error) {
	cloned := append([]core.Element(nil), elements...)
	found := false
	for index := range cloned {
		if cloned[index].Header.Tag != tag {
			continue
		}
		if found {
			return nil, fmt.Errorf("dicom dimse: duplicate command element %s", tag)
		}
		cloned[index] = newUSCommandElement(tag, messageID)
		found = true
	}
	if !found {
		cloned = append(cloned, newUSCommandElement(tag, messageID))
	}
	return cloned, nil
}
