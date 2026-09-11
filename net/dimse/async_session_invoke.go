package dimse

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

// Invoke reserves the negotiated invoked window and an association-wide
// Message ID, then sends one message atomically.
func (s *AsyncSession) Invoke(ctx context.Context, request AsyncRequest) (*AsyncOperation, error) {
	return s.invoke(ctx, request, nil)
}

func (s *AsyncSession) invoke(ctx context.Context, request AsyncRequest, writeDataSet CStoreDataSetWriter) (*AsyncOperation, error) {
	if s == nil {
		return nil, ErrAsyncSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if request.DataSet != nil && writeDataSet != nil {
		return nil, fmt.Errorf("dicom dimse: C-STORE dataset and encoded writer are mutually exclusive")
	}
	commandField, err := commandFieldFromElements(request.Command)
	if err != nil || commandField&0x8000 != 0 || commandField == CCancelRQ {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("dicom dimse: command field 0x%04X is not an invocable request", commandField)
	}
	hasDataSet := request.DataSet != nil || writeDataSet != nil
	if _, err := s.validateOutgoingCommand(request.PresentationContextID, request.Command, hasDataSet); err != nil {
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
	s.registry.operations[messageID] = operation
	s.registry.metrics.ActiveInvoked++
	if s.registry.metrics.ActiveInvoked > s.registry.metrics.PeakInvoked {
		s.registry.metrics.PeakInvoked = s.registry.metrics.ActiveInvoked
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()

	command, err := commandElementsWithMessageID(request.Command, MessageID, messageID)
	if err == nil {
		if writeDataSet != nil {
			err = s.sendEncodedMessage(ctx, request.PresentationContextID, command, writeDataSet)
		} else {
			err = s.sendMessage(ctx, request.PresentationContextID, command, request.DataSet)
		}
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
		changed := s.registry.stateChanged
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
