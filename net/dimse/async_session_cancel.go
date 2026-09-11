package dimse

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

// Cancellation is isolated from message dispatch so it has one bounded path
// for context-triggered cancel, explicit C-CANCEL, draining, and escalation.
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
