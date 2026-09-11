package dimse

// asyncOperationRegistry is the single owner of association-wide message IDs,
// active local and peer operations, and their bounded queue accounting. Its
// fields are accessed only while AsyncSession.mu is held.
type asyncOperationRegistry struct {
	operations         map[uint16]*AsyncOperation
	incoming           map[uint16]asyncIncomingOperation
	pendingIncoming    int
	nextMessageID      uint16
	nextIncomingID     uint64
	metrics            AsyncSessionMetrics
	queuedMessageBytes int64
	stateChanged       chan struct{}
}

func newAsyncOperationRegistry() asyncOperationRegistry {
	return asyncOperationRegistry{
		operations:    make(map[uint16]*AsyncOperation),
		incoming:      make(map[uint16]asyncIncomingOperation),
		nextMessageID: 1,
		stateChanged:  make(chan struct{}),
	}
}

func (s *AsyncSession) allocateMessageIDLocked() (uint16, error) {
	for attempts := 0; attempts < 65535; attempts++ {
		id := s.registry.nextMessageID
		if id == 0 {
			id = 1
		}
		s.registry.nextMessageID = id + 1
		if _, exists := s.registry.operations[id]; !exists {
			return id, nil
		}
	}
	return 0, ErrAsyncMessageIDExhausted
}

func (s *AsyncSession) finishOperation(messageID uint16, err error) {
	s.mu.Lock()
	operation, ok := s.registry.operations[messageID]
	s.mu.Unlock()
	if !ok {
		return
	}
	operation.cancelMu.Lock()
	defer operation.cancelMu.Unlock()
	s.mu.Lock()
	if current, stillActive := s.registry.operations[messageID]; !stillActive || current != operation {
		s.mu.Unlock()
		return
	}
	delete(s.registry.operations, messageID)
	s.registry.metrics.ActiveInvoked--
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
	return s.registry.metrics
}
