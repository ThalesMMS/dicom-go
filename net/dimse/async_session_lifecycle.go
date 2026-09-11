package dimse

import (
	"context"
	"errors"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

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
		changed := s.registry.stateChanged
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
		active := len(s.registry.operations) != 0 || len(s.registry.incoming) != 0 || s.registry.pendingIncoming != 0
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
		if len(s.registry.operations) == 0 && len(s.registry.incoming) == 0 && s.registry.pendingIncoming == 0 {
			s.releaseSent = true
			s.signalStateChangedLocked()
			s.mu.Unlock()
			return nil
		}
		changed := s.registry.stateChanged
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
		operations := make([]*AsyncOperation, 0, len(s.registry.operations))
		for _, operation := range s.registry.operations {
			operations = append(operations, operation)
		}
		s.registry.operations = make(map[uint16]*AsyncOperation)
		incomingOperations := make([]asyncIncomingOperation, 0, len(s.registry.incoming))
		for _, incoming := range s.registry.incoming {
			incomingOperations = append(incomingOperations, incoming)
		}
		s.registry.incoming = make(map[uint16]asyncIncomingOperation)
		s.registry.metrics.ActiveInvoked = 0
		s.registry.metrics.ActivePerformed = 0
		s.registry.queuedMessageBytes = 0
		s.registry.metrics.QueuedMessageBytes = 0
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
	close(s.registry.stateChanged)
	s.registry.stateChanged = make(chan struct{})
}
