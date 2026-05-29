package dimse

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

const (
	defaultStorageCommitmentMaxReferences  = 10_000
	defaultStorageCommitmentMaxDataSetSize = MaxNormalizedDataSetBytes
	defaultStorageCommitmentLeaseDuration  = 30 * time.Second
	minimumStorageCommitmentLeaseDuration  = time.Second
	maximumStorageCommitmentLeaseDuration  = time.Duration(1<<62 - 1)
	defaultStorageCommitmentMaxAttempts    = 5
	defaultStorageCommitmentInitialBackoff = time.Second
	defaultStorageCommitmentMaxBackoff     = time.Minute
	defaultMemoryCommitmentTransactions    = 10_000
)

var (
	// ErrStorageCommitmentTransactionNotFound indicates that no durable record
	// exists for the supplied transaction key.
	ErrStorageCommitmentTransactionNotFound = errors.New("dicom dimse: storage commitment transaction not found")
	// ErrStorageCommitmentTransactionConflict indicates a Transaction UID reuse
	// with different peers, references, or result content.
	ErrStorageCommitmentTransactionConflict = errors.New("dicom dimse: storage commitment transaction conflict")
	// ErrStorageCommitmentConcurrentUpdate indicates a failed versioned update.
	ErrStorageCommitmentConcurrentUpdate = errors.New("dicom dimse: storage commitment transaction changed concurrently")
	// ErrStorageCommitmentResourceLimit indicates a finite workflow limit.
	ErrStorageCommitmentResourceLimit = errors.New("dicom dimse: storage commitment resource limit")
	// ErrStorageCommitmentInvalidRequest indicates malformed request content.
	ErrStorageCommitmentInvalidRequest = errors.New("dicom dimse: invalid storage commitment request")
	// ErrStorageCommitmentInvalidResult indicates a result that is not the exact
	// disjoint partition of the original request required by PS3.4.
	ErrStorageCommitmentInvalidResult = errors.New("dicom dimse: invalid storage commitment result")
	// ErrStorageCommitmentCallbackUnknown indicates that callback policy did not
	// resolve an allowlisted endpoint for the requestor AE.
	ErrStorageCommitmentCallbackUnknown = errors.New("dicom dimse: storage commitment callback is unknown")
	// ErrStorageCommitmentDeliveryExhausted indicates that the configured number
	// of attempts has been consumed.
	ErrStorageCommitmentDeliveryExhausted = errors.New("dicom dimse: storage commitment delivery exhausted")
)

// StorageCommitmentDirection identifies the side that owns a durable record.
type StorageCommitmentDirection string

const (
	StorageCommitmentDirectionRequestor StorageCommitmentDirection = "requestor"
	StorageCommitmentDirectionCommitter StorageCommitmentDirection = "committer"
)

// StorageCommitmentState is the durable workflow state. Processing and
// delivery claims carry expiring leases so another process can recover them.
type StorageCommitmentState string

const (
	StorageCommitmentStateRequestPending     StorageCommitmentState = "request_pending"
	StorageCommitmentStateAcceptancePrepared StorageCommitmentState = "acceptance_prepared"
	StorageCommitmentStateAccepted           StorageCommitmentState = "accepted"
	StorageCommitmentStateProcessing         StorageCommitmentState = "processing"
	StorageCommitmentStateRequestRejected    StorageCommitmentState = "request_rejected"
	StorageCommitmentStateResultReady        StorageCommitmentState = "result_ready"
	StorageCommitmentStateDelivering         StorageCommitmentState = "delivering"
	StorageCommitmentStateDeliveryFailed     StorageCommitmentState = "delivery_failed"
	StorageCommitmentStateDeliveryExhausted  StorageCommitmentState = "delivery_exhausted"
	StorageCommitmentStateDelivered          StorageCommitmentState = "delivered"
	StorageCommitmentStateResultReceived     StorageCommitmentState = "result_received"
)

// StorageCommitmentDeliveryMode records the explicitly selected result path.
type StorageCommitmentDeliveryMode string

const (
	StorageCommitmentDeliverySameAssociation StorageCommitmentDeliveryMode = "same_association"
	StorageCommitmentDeliveryCallback        StorageCommitmentDeliveryMode = "callback"
	StorageCommitmentDeliveryManual          StorageCommitmentDeliveryMode = "manual"
)

// StorageCommitmentFailureClass is deliberately closed and contains no peer
// text, host, AE, UID, or clinical values.
type StorageCommitmentFailureClass string

const (
	StorageCommitmentFailureNone                StorageCommitmentFailureClass = ""
	StorageCommitmentFailureProcessing          StorageCommitmentFailureClass = "processing"
	StorageCommitmentFailureCallbackUnknown     StorageCommitmentFailureClass = "callback_unknown"
	StorageCommitmentFailureCallbackOffline     StorageCommitmentFailureClass = "callback_offline"
	StorageCommitmentFailureTLS                 StorageCommitmentFailureClass = "tls"
	StorageCommitmentFailureTimeout             StorageCommitmentFailureClass = "timeout"
	StorageCommitmentFailureCanceled            StorageCommitmentFailureClass = "canceled"
	StorageCommitmentFailureAssociationRejected StorageCommitmentFailureClass = "association_rejected"
	StorageCommitmentFailureDIMSEStatus         StorageCommitmentFailureClass = "dimse_status"
	StorageCommitmentFailureProtocol            StorageCommitmentFailureClass = "protocol"
)

// StorageCommitmentResult is the terminal per-instance decision. ReferencedSOPs
// and FailedSOPs together must equal the request exactly.
type StorageCommitmentResult struct {
	TransactionUID string
	ReferencedSOPs []StorageCommitmentSOPReference
	FailedSOPs     []StorageCommitmentSOPReference
	ProducedAt     time.Time
}

// StorageCommitmentTransaction is a value-only durable record. Store
// implementations must clone all slices on input and output.
type StorageCommitmentTransaction struct {
	TransactionUID   string
	Direction        StorageCommitmentDirection
	State            StorageCommitmentState
	DeliveryMode     StorageCommitmentDeliveryMode
	RequestorAETitle string
	CommitterAETitle string
	ReferencedSOPs   []StorageCommitmentSOPReference
	RequestMessageID uint16
	RequestDigest    [sha256.Size]byte
	ResultDigest     [sha256.Size]byte
	Result           *StorageCommitmentResult
	Version          uint64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      time.Time
	ProcessingTries  int
	DeliveryAttempts int
	NextAttemptAt    time.Time
	LeaseToken       string
	LeaseUntil       time.Time
	LastFailure      StorageCommitmentFailureClass
	LastDIMSEStatus  uint16
}

// StorageCommitmentTransactionQuery pages durable work without materializing
// an unbounded transaction set.
type StorageCommitmentTransactionQuery struct {
	DueAt  time.Time
	States []StorageCommitmentState
	Limit  int
}

// StorageCommitmentTransactionStore is the persistence boundary. Create must
// be durable before it returns. CompareAndSwap must update only when Version
// equals expectedVersion and must increment Version atomically.
type StorageCommitmentTransactionStore interface {
	Create(context.Context, StorageCommitmentTransaction) (bool, error)
	Get(context.Context, string) (StorageCommitmentTransaction, error)
	CompareAndSwap(context.Context, string, uint64, StorageCommitmentTransaction) (StorageCommitmentTransaction, error)
	List(context.Context, StorageCommitmentTransactionQuery) ([]StorageCommitmentTransaction, error)
	PurgeCompleted(context.Context, time.Time, int) (int, error)
}

// MemoryStorageCommitmentStore is a bounded, concurrency-safe implementation
// intended for tests and process-local deployments. Applications that require
// restart durability provide their own StorageCommitmentTransactionStore.
type MemoryStorageCommitmentStore struct {
	mu              sync.RWMutex
	maxTransactions int
	transactions    map[string]StorageCommitmentTransaction
}

// NewMemoryStorageCommitmentStore constructs a bounded in-memory store. Zero
// uses a finite default.
func NewMemoryStorageCommitmentStore(maxTransactions int) (*MemoryStorageCommitmentStore, error) {
	if maxTransactions < 0 {
		return nil, fmt.Errorf("%w: MaxTransactions", ErrStorageCommitmentResourceLimit)
	}
	if maxTransactions == 0 {
		maxTransactions = defaultMemoryCommitmentTransactions
	}
	return &MemoryStorageCommitmentStore{
		maxTransactions: maxTransactions,
		transactions:    make(map[string]StorageCommitmentTransaction),
	}, nil
}

func (s *MemoryStorageCommitmentStore) Create(ctx context.Context, transaction StorageCommitmentTransaction) (bool, error) {
	if err := storageCommitmentContextError(ctx); err != nil {
		return false, err
	}
	transaction = cloneStorageCommitmentTransaction(transaction)
	if transaction.TransactionUID == "" {
		return false, ErrStorageCommitmentInvalidRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.transactions[transaction.TransactionUID]; ok {
		return false, nil
	}
	if len(s.transactions) >= s.maxTransactions {
		return false, ErrStorageCommitmentResourceLimit
	}
	transaction.Version = 1
	s.transactions[transaction.TransactionUID] = transaction
	return true, nil
}

func (s *MemoryStorageCommitmentStore) Get(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	if err := storageCommitmentContextError(ctx); err != nil {
		return StorageCommitmentTransaction{}, err
	}
	s.mu.RLock()
	transaction, ok := s.transactions[transactionUID]
	s.mu.RUnlock()
	if !ok {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionNotFound
	}
	return cloneStorageCommitmentTransaction(transaction), nil
}

func (s *MemoryStorageCommitmentStore) CompareAndSwap(ctx context.Context, transactionUID string, expectedVersion uint64, next StorageCommitmentTransaction) (StorageCommitmentTransaction, error) {
	if err := storageCommitmentContextError(ctx); err != nil {
		return StorageCommitmentTransaction{}, err
	}
	next = cloneStorageCommitmentTransaction(next)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.transactions[transactionUID]
	if !ok {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionNotFound
	}
	if current.Version != expectedVersion {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
	}
	if next.TransactionUID != transactionUID || next.Direction != current.Direction || next.RequestDigest != current.RequestDigest {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	next.Version = expectedVersion + 1
	s.transactions[transactionUID] = next
	return cloneStorageCommitmentTransaction(next), nil
}

func (s *MemoryStorageCommitmentStore) List(ctx context.Context, query StorageCommitmentTransactionQuery) ([]StorageCommitmentTransaction, error) {
	if err := storageCommitmentContextError(ctx); err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		return nil, fmt.Errorf("%w: List limit", ErrStorageCommitmentResourceLimit)
	}
	wanted := make(map[StorageCommitmentState]bool, len(query.States))
	for _, state := range query.States {
		wanted[state] = true
	}
	s.mu.RLock()
	items := make([]StorageCommitmentTransaction, 0, min(query.Limit, len(s.transactions)))
	for _, transaction := range s.transactions {
		if err := storageCommitmentContextError(ctx); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		if len(wanted) > 0 && !wanted[transaction.State] {
			continue
		}
		if !query.DueAt.IsZero() && !transactionDue(transaction, query.DueAt) {
			continue
		}
		items = append(items, cloneStorageCommitmentTransaction(transaction))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].TransactionUID < items[j].TransactionUID
		}
		return items[i].UpdatedAt.Before(items[j].UpdatedAt)
	})
	if len(items) > query.Limit {
		items = items[:query.Limit]
	}
	return items, nil
}

// PurgeCompleted removes at most limit terminal records last updated before
// before. Active or retryable transactions are never removed.
func (s *MemoryStorageCommitmentStore) PurgeCompleted(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := storageCommitmentContextError(ctx); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, ErrStorageCommitmentResourceLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, min(limit, len(s.transactions)))
	for key, transaction := range s.transactions {
		if storageCommitmentTerminal(transaction.State) && transaction.UpdatedAt.Before(before) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	for _, key := range keys {
		delete(s.transactions, key)
	}
	return len(keys), nil
}

func transactionDue(transaction StorageCommitmentTransaction, now time.Time) bool {
	if transaction.State == StorageCommitmentStateProcessing || transaction.State == StorageCommitmentStateDelivering {
		return !transaction.LeaseUntil.After(now)
	}
	return transaction.NextAttemptAt.IsZero() || !transaction.NextAttemptAt.After(now)
}

// StorageCommitmentLimits bounds both request and result datasets.
type StorageCommitmentLimits struct {
	MaxReferences   int
	MaxDataSetBytes int64
	MaxListBatch    int
}

func normalizeStorageCommitmentLimits(limits StorageCommitmentLimits) (StorageCommitmentLimits, error) {
	if limits.MaxReferences < 0 || limits.MaxDataSetBytes < 0 || limits.MaxListBatch < 0 {
		return StorageCommitmentLimits{}, ErrStorageCommitmentResourceLimit
	}
	if limits.MaxReferences == 0 {
		limits.MaxReferences = defaultStorageCommitmentMaxReferences
	}
	if limits.MaxReferences > defaultStorageCommitmentMaxReferences {
		return StorageCommitmentLimits{}, ErrStorageCommitmentResourceLimit
	}
	if limits.MaxDataSetBytes == 0 {
		limits.MaxDataSetBytes = defaultStorageCommitmentMaxDataSetSize
	}
	if limits.MaxListBatch == 0 {
		limits.MaxListBatch = 128
	}
	return limits, nil
}

// StorageCommitmentRetryPolicy controls persisted callback attempts.
type StorageCommitmentRetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func normalizeStorageCommitmentRetryPolicy(policy StorageCommitmentRetryPolicy) (StorageCommitmentRetryPolicy, error) {
	if policy.MaxAttempts < 0 || policy.InitialBackoff < 0 || policy.MaxBackoff < 0 {
		return StorageCommitmentRetryPolicy{}, ErrStorageCommitmentResourceLimit
	}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = defaultStorageCommitmentMaxAttempts
	}
	if policy.InitialBackoff == 0 {
		policy.InitialBackoff = defaultStorageCommitmentInitialBackoff
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = defaultStorageCommitmentMaxBackoff
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return StorageCommitmentRetryPolicy{}, ErrStorageCommitmentResourceLimit
	}
	return policy, nil
}

// StorageCommitmentProcessor decides commitment for one claimed request.
// Implementations may be called again after a lease expires and must therefore
// be idempotent by Transaction UID and RequestDigest.
type StorageCommitmentProcessor interface {
	ProcessStorageCommitment(context.Context, StorageCommitmentTransaction) (StorageCommitmentResult, error)
}

type StorageCommitmentProcessorFunc func(context.Context, StorageCommitmentTransaction) (StorageCommitmentResult, error)

func (f StorageCommitmentProcessorFunc) ProcessStorageCommitment(ctx context.Context, transaction StorageCommitmentTransaction) (StorageCommitmentResult, error) {
	if f == nil {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	return f(ctx, transaction)
}

// StorageCommitmentResultConsumer observes a durably stored inbound result.
// It is at-least-once until the completed state is persisted and should be
// idempotent by Transaction UID and result digest.
type StorageCommitmentResultConsumer interface {
	ConsumeStorageCommitmentResult(context.Context, StorageCommitmentTransaction) error
}

type StorageCommitmentResultConsumerFunc func(context.Context, StorageCommitmentTransaction) error

func (f StorageCommitmentResultConsumerFunc) ConsumeStorageCommitmentResult(ctx context.Context, transaction StorageCommitmentTransaction) error {
	if f == nil {
		return nil
	}
	return f(ctx, transaction)
}

// StorageCommitmentWorkflowOptions freezes workflow dependencies. Store,
// Resolver, Dialer, Processor and Consumer remain owned by the caller.
type StorageCommitmentWorkflowOptions struct {
	Store               StorageCommitmentTransactionStore
	Limits              StorageCommitmentLimits
	RetryPolicy         StorageCommitmentRetryPolicy
	LeaseDuration       time.Duration
	UIDGenerator        func() (string, error)
	Processor           StorageCommitmentProcessor
	Consumer            StorageCommitmentResultConsumer
	Resolver            StorageCommitmentCallbackResolver
	Dialer              StorageCommitmentAssociationDialer
	Now                 func() time.Time
	DefaultDeliveryMode StorageCommitmentDeliveryMode
}

// StorageCommitmentWorkflow owns no association or persistence resource.
type StorageCommitmentWorkflow struct {
	store               StorageCommitmentTransactionStore
	limits              StorageCommitmentLimits
	retry               StorageCommitmentRetryPolicy
	leaseDuration       time.Duration
	uidGenerator        func() (string, error)
	processor           StorageCommitmentProcessor
	consumer            StorageCommitmentResultConsumer
	resolver            StorageCommitmentCallbackResolver
	dialer              StorageCommitmentAssociationDialer
	now                 func() time.Time
	defaultDeliveryMode StorageCommitmentDeliveryMode

	requestMu sync.Mutex
	messageID uint16
}

func NewStorageCommitmentWorkflow(options StorageCommitmentWorkflowOptions) (*StorageCommitmentWorkflow, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("dicom dimse: Storage Commitment store is required")
	}
	limits, err := normalizeStorageCommitmentLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	retry, err := normalizeStorageCommitmentRetryPolicy(options.RetryPolicy)
	if err != nil {
		return nil, err
	}
	if options.LeaseDuration < 0 || options.LeaseDuration > maximumStorageCommitmentLeaseDuration ||
		options.LeaseDuration > 0 && options.LeaseDuration < minimumStorageCommitmentLeaseDuration {
		return nil, ErrStorageCommitmentResourceLimit
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = defaultStorageCommitmentLeaseDuration
	}
	if options.UIDGenerator == nil {
		options.UIDGenerator = GenerateStorageCommitmentTransactionUID
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Dialer == nil {
		options.Dialer = storageCommitmentDefaultDialer{}
	}
	if options.DefaultDeliveryMode == "" {
		options.DefaultDeliveryMode = StorageCommitmentDeliveryCallback
	}
	if !validStorageCommitmentDeliveryMode(options.DefaultDeliveryMode) {
		return nil, ErrStorageCommitmentInvalidRequest
	}
	return &StorageCommitmentWorkflow{
		store: options.Store, limits: limits, retry: retry,
		leaseDuration: options.LeaseDuration, uidGenerator: options.UIDGenerator,
		processor: options.Processor, consumer: options.Consumer,
		resolver: options.Resolver, dialer: options.Dialer, now: options.Now,
		defaultDeliveryMode: options.DefaultDeliveryMode,
	}, nil
}

func (w *StorageCommitmentWorkflow) storeCreate(ctx context.Context, transaction StorageCommitmentTransaction) (bool, error) {
	created, err := w.store.Create(ctx, transaction)
	return created, storageCommitmentStoreError(err)
}

func (w *StorageCommitmentWorkflow) storeGet(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	transaction, err := w.store.Get(ctx, transactionUID)
	return transaction, storageCommitmentStoreError(err)
}

func (w *StorageCommitmentWorkflow) storeCompareAndSwap(ctx context.Context, transactionUID string, version uint64, transaction StorageCommitmentTransaction) (StorageCommitmentTransaction, error) {
	updated, err := w.store.CompareAndSwap(ctx, transactionUID, version, transaction)
	return updated, storageCommitmentStoreError(err)
}

func (w *StorageCommitmentWorkflow) storeList(ctx context.Context, query StorageCommitmentTransactionQuery) ([]StorageCommitmentTransaction, error) {
	transactions, err := w.store.List(ctx, query)
	return transactions, storageCommitmentStoreError(err)
}

func (w *StorageCommitmentWorkflow) storePurgeCompleted(ctx context.Context, before time.Time, limit int) (int, error) {
	removed, err := w.store.PurgeCompleted(ctx, before, limit)
	return removed, storageCommitmentStoreError(err)
}

func storageCommitmentStoreError(err error) error {
	if err == nil {
		return nil
	}
	var safe *StorageCommitmentDeliveryError
	if errors.As(err, &safe) {
		return err
	}
	return sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
}

// GenerateStorageCommitmentTransactionUID returns a valid 2.25 UID using 128
// random bits without mutable global state.
func GenerateStorageCommitmentTransactionUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("dicom dimse: generate Storage Commitment transaction UID: %w", err)
	}
	integer := new(big.Int).SetBytes(value[:])
	return "2.25." + integer.String(), nil
}

// StorageCommitmentRequestOptions configures one N-ACTION submission on a
// borrowed association.
type StorageCommitmentRequestOptions struct {
	TransactionUID string
	DeliveryMode   StorageCommitmentDeliveryMode
	Operation      NormalizedOperationOptions
}

// Request persists the request before sending N-ACTION. An identical duplicate
// returns the existing record without sending a second operation.
func (w *StorageCommitmentWorkflow) Request(ctx context.Context, assoc *ul.Association, references []StorageCommitmentSOPReference, options StorageCommitmentRequestOptions) (StorageCommitmentTransaction, error) {
	if assoc == nil || !storageCommitmentRequestorCanActAsSCU(assoc) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentInvalidRequest
	}
	transactionUID := strings.TrimSpace(options.TransactionUID)
	if transactionUID == "" {
		var err error
		transactionUID, err = w.uidGenerator()
		if err != nil {
			return StorageCommitmentTransaction{}, err
		}
	}
	mode := options.DeliveryMode
	if mode == "" {
		mode = w.defaultDeliveryMode
	}
	references, err := normalizeStorageCommitmentReferences(references, false, w.limits.MaxReferences)
	if err != nil || !validStoreUID(transactionUID) || !validStorageCommitmentDeliveryMode(mode) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentInvalidRequest
	}
	now := w.now()
	transaction := StorageCommitmentTransaction{
		TransactionUID: transactionUID, Direction: StorageCommitmentDirectionRequestor,
		State: StorageCommitmentStateRequestPending, DeliveryMode: mode,
		RequestorAETitle: strings.TrimSpace(assoc.CallingAETitle),
		CommitterAETitle: strings.TrimSpace(assoc.CalledAETitle),
		ReferencedSOPs:   references, RequestMessageID: w.nextMessageID(),
		CreatedAt: now, UpdatedAt: now,
	}
	transaction.RequestDigest = storageCommitmentRequestDigest(transaction)
	created, err := w.storeCreate(ctx, transaction)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	if !created {
		existing, getErr := w.storeGet(ctx, transactionUID)
		if getErr != nil {
			return StorageCommitmentTransaction{}, getErr
		}
		if !sameStorageCommitmentRequest(existing, transaction) {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		return existing, nil
	}
	createdRecord, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	return w.submitRequest(ctx, assoc, createdRecord, options.Operation)
}

// RetryRequest explicitly retries a durably pending N-ACTION. It never creates
// or reuses a Transaction UID for a different request.
func (w *StorageCommitmentWorkflow) RetryRequest(ctx context.Context, assoc *ul.Association, transactionUID string, operation NormalizedOperationOptions) (StorageCommitmentTransaction, error) {
	if assoc == nil || !storageCommitmentRequestorCanActAsSCU(assoc) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentInvalidRequest
	}
	transaction, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	if transaction.Direction != StorageCommitmentDirectionRequestor || transaction.State != StorageCommitmentStateRequestPending {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	transaction.RequestMessageID = w.nextMessageID()
	transaction.UpdatedAt = w.now()
	transaction, err = w.storeCompareAndSwap(ctx, transactionUID, transaction.Version, transaction)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	return w.submitRequest(ctx, assoc, transaction, operation)
}

func (w *StorageCommitmentWorkflow) submitRequest(ctx context.Context, assoc *ul.Association, transaction StorageCommitmentTransaction, operation NormalizedOperationOptions) (StorageCommitmentTransaction, error) {
	dataSet, err := BuildStorageCommitmentActionInformation(transaction.TransactionUID, transaction.ReferencedSOPs)
	if err != nil {
		return transaction, ErrStorageCommitmentInvalidRequest
	}
	operation.OperationOptions.Context = ctx
	result, err := NewNormalizedClient(assoc).ActionWithOptions(operation, NormalizedActionRequest{
		RequestedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		RequestedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		MessageID:               transaction.RequestMessageID, CommandDataSetType: DataSetPresent,
		ActionTypeID: StorageCommitmentActionTypeID,
	}, dataSet)
	if err != nil {
		var statusErr *NormalizedStatusError
		if result.Response != nil && errors.As(err, &statusErr) {
			persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
			defer cancelPersist()
			updated, updateErr := w.updateState(persistCtx, transaction.TransactionUID, []StorageCommitmentState{StorageCommitmentStateRequestPending}, StorageCommitmentStateRequestRejected, func(next *StorageCommitmentTransaction) {
				next.LastDIMSEStatus = result.Response.Status
				next.CompletedAt = w.now()
			})
			if updateErr != nil {
				return transaction, updateErr
			}
			return updated, CheckStorageCommitmentStatus("N-ACTION-RSP", result.Response.Status)
		}
		return transaction, err
	}
	if result.Response == nil {
		return transaction, ErrStorageCommitmentInvalidRequest
	}
	if err := CheckStorageCommitmentStatus("N-ACTION-RSP", result.Response.Status); err != nil {
		persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
		defer cancelPersist()
		updated, updateErr := w.updateState(persistCtx, transaction.TransactionUID, []StorageCommitmentState{StorageCommitmentStateRequestPending}, StorageCommitmentStateRequestRejected, func(next *StorageCommitmentTransaction) {
			next.LastDIMSEStatus = result.Response.Status
			next.CompletedAt = w.now()
		})
		if updateErr != nil {
			return transaction, updateErr
		}
		return updated, err
	}
	return w.markRequestorAccepted(ctx, transaction.TransactionUID)
}

func (w *StorageCommitmentWorkflow) markRequestorAccepted(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
	defer cancelPersist()
	for attempts := 0; attempts < 8; attempts++ {
		current, err := w.storeGet(persistCtx, transactionUID)
		if err != nil {
			return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
		}
		if current.Direction != StorageCommitmentDirectionRequestor {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		switch current.State {
		case StorageCommitmentStateAccepted, StorageCommitmentStateResultReady, StorageCommitmentStateResultReceived:
			return current, nil
		case StorageCommitmentStateRequestPending:
		default:
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		next := cloneStorageCommitmentTransaction(current)
		next.State = StorageCommitmentStateAccepted
		next.UpdatedAt = w.now()
		updated, err := w.storeCompareAndSwap(persistCtx, current.TransactionUID, current.Version, next)
		if errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			continue
		}
		if err != nil {
			return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
		}
		return updated, nil
	}
	return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
}

func (w *StorageCommitmentWorkflow) nextMessageID() uint16 {
	w.requestMu.Lock()
	defer w.requestMu.Unlock()
	w.messageID++
	if w.messageID == 0 {
		w.messageID++
	}
	return w.messageID
}

// Get returns a detached durable snapshot.
func (w *StorageCommitmentWorkflow) Get(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	return w.storeGet(ctx, transactionUID)
}

// PurgeCompleted applies the configured finite batch limit to retention.
func (w *StorageCommitmentWorkflow) PurgeCompleted(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 || limit > w.limits.MaxListBatch {
		return 0, ErrStorageCommitmentResourceLimit
	}
	return w.storePurgeCompleted(ctx, before, limit)
}

// Wait polls a persistent store without deleting or mutating the transaction
// when the caller cancels. Zero interval uses a bounded default.
func (w *StorageCommitmentWorkflow) Wait(ctx context.Context, transactionUID string, interval time.Duration) (StorageCommitmentTransaction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval < 0 {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentInvalidRequest
	}
	if interval == 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		transaction, err := w.storeGet(ctx, transactionUID)
		if err != nil {
			return StorageCommitmentTransaction{}, err
		}
		if storageCommitmentTerminal(transaction.State) {
			return transaction, nil
		}
		select {
		case <-ctx.Done():
			return StorageCommitmentTransaction{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func storageCommitmentTerminal(state StorageCommitmentState) bool {
	return state == StorageCommitmentStateRequestRejected || state == StorageCommitmentStateDelivered || state == StorageCommitmentStateDeliveryExhausted || state == StorageCommitmentStateResultReceived
}

func (w *StorageCommitmentWorkflow) updateState(ctx context.Context, transactionUID string, allowed []StorageCommitmentState, state StorageCommitmentState, mutate func(*StorageCommitmentTransaction)) (StorageCommitmentTransaction, error) {
	for attempts := 0; attempts < 8; attempts++ {
		current, err := w.storeGet(ctx, transactionUID)
		if err != nil {
			return StorageCommitmentTransaction{}, err
		}
		if current.State == state {
			return current, nil
		}
		if !containsStorageCommitmentState(allowed, current.State) {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		next := cloneStorageCommitmentTransaction(current)
		next.State = state
		next.UpdatedAt = w.now()
		if mutate != nil {
			mutate(&next)
		}
		updated, err := w.storeCompareAndSwap(ctx, transactionUID, current.Version, next)
		if errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			continue
		}
		return updated, err
	}
	return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
}

func containsStorageCommitmentState(states []StorageCommitmentState, state StorageCommitmentState) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func validStorageCommitmentDeliveryMode(mode StorageCommitmentDeliveryMode) bool {
	return mode == StorageCommitmentDeliverySameAssociation || mode == StorageCommitmentDeliveryCallback || mode == StorageCommitmentDeliveryManual
}

func normalizeStorageCommitmentReferences(references []StorageCommitmentSOPReference, failed bool, limit int) ([]StorageCommitmentSOPReference, error) {
	if len(references) == 0 || len(references) > limit {
		return nil, ErrStorageCommitmentResourceLimit
	}
	result := append([]StorageCommitmentSOPReference(nil), references...)
	seen := make(map[string]bool, len(result))
	for i := range result {
		result[i].SOPClassUID = strings.TrimSpace(result[i].SOPClassUID)
		result[i].SOPInstanceUID = strings.TrimSpace(result[i].SOPInstanceUID)
		if !validStoreUID(result[i].SOPClassUID) || !validStoreUID(result[i].SOPInstanceUID) {
			return nil, ErrStorageCommitmentInvalidRequest
		}
		key := result[i].SOPInstanceUID
		if seen[key] {
			return nil, ErrStorageCommitmentInvalidRequest
		}
		seen[key] = true
		if failed {
			if !validStorageCommitmentFailureReason(result[i].FailureReason) {
				return nil, ErrStorageCommitmentInvalidResult
			}
		} else if result[i].FailureReason != 0 {
			return nil, ErrStorageCommitmentInvalidResult
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SOPClassUID == result[j].SOPClassUID {
			return result[i].SOPInstanceUID < result[j].SOPInstanceUID
		}
		return result[i].SOPClassUID < result[j].SOPClassUID
	})
	return result, nil
}

func validStorageCommitmentFailureReason(reason uint16) bool {
	switch reason {
	case 0x0110, 0x0112, 0x0213, 0x0122, 0x0119, 0x0131:
		return true
	default:
		return false
	}
}

func storageCommitmentRequestDigest(transaction StorageCommitmentTransaction) [sha256.Size]byte {
	hash := sha256.New()
	writeDigestString(hash, string(transaction.Direction))
	writeDigestString(hash, string(transaction.DeliveryMode))
	writeDigestString(hash, transaction.TransactionUID)
	writeDigestString(hash, transaction.RequestorAETitle)
	writeDigestString(hash, transaction.CommitterAETitle)
	for _, reference := range transaction.ReferencedSOPs {
		writeDigestString(hash, reference.SOPClassUID)
		writeDigestString(hash, reference.SOPInstanceUID)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

type digestWriter interface{ Write([]byte) (int, error) }

func writeDigestString(writer digestWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(value))
}

func storageCommitmentResultDigest(result StorageCommitmentResult) [sha256.Size]byte {
	hash := sha256.New()
	writeDigestString(hash, result.TransactionUID)
	for _, reference := range result.ReferencedSOPs {
		writeDigestString(hash, reference.SOPClassUID)
		writeDigestString(hash, reference.SOPInstanceUID)
	}
	for _, reference := range result.FailedSOPs {
		writeDigestString(hash, reference.SOPClassUID)
		writeDigestString(hash, reference.SOPInstanceUID)
		var reason [2]byte
		binary.BigEndian.PutUint16(reason[:], reference.FailureReason)
		_, _ = hash.Write(reason[:])
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func sameStorageCommitmentRequest(a, b StorageCommitmentTransaction) bool {
	return a.RequestDigest == b.RequestDigest && a.Direction == b.Direction && a.RequestorAETitle == b.RequestorAETitle && a.CommitterAETitle == b.CommitterAETitle
}

func cloneStorageCommitmentTransaction(transaction StorageCommitmentTransaction) StorageCommitmentTransaction {
	clone := transaction
	clone.ReferencedSOPs = append([]StorageCommitmentSOPReference(nil), transaction.ReferencedSOPs...)
	if transaction.Result != nil {
		result := cloneStorageCommitmentResult(*transaction.Result)
		clone.Result = &result
	}
	return clone
}

func cloneStorageCommitmentResult(result StorageCommitmentResult) StorageCommitmentResult {
	result.ReferencedSOPs = append([]StorageCommitmentSOPReference(nil), result.ReferencedSOPs...)
	result.FailedSOPs = append([]StorageCommitmentSOPReference(nil), result.FailedSOPs...)
	return result
}

func storageCommitmentContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func newStorageCommitmentLeaseToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// storageCommitmentResultFromEvent validates event semantics before contextual
// request validation.
func storageCommitmentResultFromEvent(request NormalizedEventReportRequest, information StorageCommitmentEventInformation) (StorageCommitmentResult, error) {
	if !validStorageCommitmentEventType(request.EventTypeID) {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	if request.EventTypeID == StorageCommitmentEventTypeSuccess && len(information.FailedSOPs) != 0 {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	if request.EventTypeID == StorageCommitmentEventTypeFailures && len(information.FailedSOPs) == 0 {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	return StorageCommitmentResult{
		TransactionUID: information.TransactionUID,
		ReferencedSOPs: information.ReferencedSOPs,
		FailedSOPs:     information.FailedSOPs,
	}, nil
}
