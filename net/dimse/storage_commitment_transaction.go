package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// StorageCommitmentEvent represents a correlated Storage Commitment push
// notification (N-EVENT-REPORT) for a previously requested transaction.
//
// This struct intentionally only correlates by TransactionUID; callers can parse
// the event information dataset separately if needed.
//
// When receiving Storage Commitment notifications, the DIMSE layer should match
// the TransactionUID inside the EventInformation dataset to an outstanding
// transaction.
//
// Note: The TransactionUID is part of the N-EVENT-REPORT event information
// dataset, not the command set.
//
// See Storage Commitment Push Model in DICOM PS3.4.
type StorageCommitmentEvent struct {
	TransactionUID string
	// ReceivedAt is when the event was matched and delivered.
	ReceivedAt time.Time
	// RequestMessageID is the Message ID of the originating N-ACTION request,
	// if known.
	RequestMessageID uint16
}

var (
	// ErrStorageCommitmentTransactionUnknown indicates a push notification was
	// received for a transaction UID that is not currently being tracked.
	ErrStorageCommitmentTransactionUnknown = errors.New("dicom dimse: storage commitment transaction UID not found")
	// ErrStorageCommitmentTransactionTimeout indicates that a tracked transaction
	// did not receive a matching push notification within the configured timeout.
	ErrStorageCommitmentTransactionTimeout = errors.New("dicom dimse: storage commitment transaction timed out")
)

// StorageCommitmentTransactionTracker tracks in-flight Storage Commitment
// transactions by Transaction UID and allows correlating an incoming
// N-EVENT-REPORT dataset.
//
// Concurrency: safe for concurrent use.
//
// This is in-memory only and is intended for a single process instance.
//
// Typical flow:
//  1. After sending N-ACTION with Transaction UID, call Track().
//  2. On receipt of N-EVENT-REPORT, extract Transaction UID from the event
//     information dataset and call Deliver().
//  3. The goroutine awaiting the transaction (Wait()) unblocks.
//
// This tracker does not parse datasets. Use helper(s) in higher layers to
// extract Transaction UID.
type StorageCommitmentTransactionTracker struct {
	mu sync.Mutex
	m  map[string]*trackedTransaction
}

type trackedTransaction struct {
	createdAt        time.Time
	requestMessageID uint16
	ch               chan StorageCommitmentEvent
	closed           bool
}

// NewStorageCommitmentTransactionTracker constructs a new tracker.
func NewStorageCommitmentTransactionTracker() *StorageCommitmentTransactionTracker {
	return &StorageCommitmentTransactionTracker{m: make(map[string]*trackedTransaction)}
}

// Track registers a Transaction UID for later delivery through Wait.
//
// It returns an error when the UID is empty or already being tracked.
func (t *StorageCommitmentTransactionTracker) Track(transactionUID string, requestMessageID uint16) error {
	if transactionUID == "" {
		return fmt.Errorf("dicom dimse: storage commitment Track requires non-empty transaction UID")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[transactionUID]; ok {
		return fmt.Errorf("dicom dimse: storage commitment transaction UID already tracked: %s", transactionUID)
	}
	t.m[transactionUID] = &trackedTransaction{
		createdAt:        time.Now(),
		requestMessageID: requestMessageID,
		ch:               make(chan StorageCommitmentEvent, 1),
	}
	return nil
}

// Wait blocks until an event for the given transaction UID is delivered, the
// context is canceled, or the timeout elapses.
//
// If timeout <= 0, Wait will only return when context is canceled or an event
// is delivered.
//
// On successful delivery, the transaction is removed from the tracker.
func (t *StorageCommitmentTransactionTracker) Wait(ctx context.Context, transactionUID string, timeout time.Duration) (StorageCommitmentEvent, error) {
	if transactionUID == "" {
		return StorageCommitmentEvent{}, fmt.Errorf("dicom dimse: storage commitment Wait requires non-empty transaction UID")
	}
	t.mu.Lock()
	tr, ok := t.m[transactionUID]
	t.mu.Unlock()
	if !ok {
		return StorageCommitmentEvent{}, ErrStorageCommitmentTransactionUnknown
	}

	var timerCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timerCh = t.C
	}

	select {
	case ev := <-tr.ch:
		t.remove(transactionUID)
		return ev, nil
	case <-timerCh:
		t.remove(transactionUID)
		return StorageCommitmentEvent{}, ErrStorageCommitmentTransactionTimeout
	case <-ctx.Done():
		t.remove(transactionUID)
		return StorageCommitmentEvent{}, ctx.Err()
	}
}

// Deliver matches a received push notification with a tracked transaction.
//
// If the transaction is found, Deliver publishes the event to the waiter and
// returns true.
//
// If not found, it returns false along with ErrStorageCommitmentTransactionUnknown.
func (t *StorageCommitmentTransactionTracker) Deliver(transactionUID string) (bool, error) {
	if transactionUID == "" {
		return false, fmt.Errorf("dicom dimse: storage commitment Deliver requires non-empty transaction UID")
	}
	t.mu.Lock()
	tr, ok := t.m[transactionUID]
	if !ok {
		t.mu.Unlock()
		return false, ErrStorageCommitmentTransactionUnknown
	}
	// Prepare event while holding lock, but do non-blocking send.
	ev := StorageCommitmentEvent{
		TransactionUID:   transactionUID,
		ReceivedAt:       time.Now(),
		RequestMessageID: tr.requestMessageID,
	}
	// Mark closed to prevent multiple deliveries.
	if tr.closed {
		t.mu.Unlock()
		return false, fmt.Errorf("dicom dimse: storage commitment transaction already delivered: %s", transactionUID)
	}
	tr.closed = true
	t.mu.Unlock()

	tr.ch <- ev
	return true, nil
}

func (t *StorageCommitmentTransactionTracker) remove(transactionUID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, transactionUID)
}
