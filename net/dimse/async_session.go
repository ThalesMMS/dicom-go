package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
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

	mu          sync.Mutex
	handlers    map[uint16]AsyncRequestHandler
	registry    asyncOperationRegistry
	closed      bool
	releasing   bool
	releaseSent bool
	closeErr    error

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
		registry:               newAsyncOperationRegistry(),
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
