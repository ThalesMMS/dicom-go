package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

type scpInterleavedCommand struct {
	pcID    byte
	command *object.Object
}

// scpCancelMonitor owns association reads while one FIND/MOVE/GET operation is
// active. That keeps C-CANCEL handling out of timing-based socket polling and
// lets C-GET route same-association C-STORE responses through the same reader.
type scpCancelMonitor struct {
	targetPCID      byte
	targetMessageID uint16
	cancelErr       error
	allowStoreRSP   bool

	operationCtx    context.Context
	operationCancel context.CancelCauseFunc
	readCancel      context.CancelFunc
	commands        chan scpInterleavedCommand
	done            chan struct{}
	stopOnce        sync.Once

	mu       sync.Mutex
	canceled bool
	err      error
}

func startSCPCancelMonitor(parent context.Context, assoc *ul.Association, pcID byte, messageID uint16, cancelErr error, allowStoreRSP bool) *scpCancelMonitor {
	if parent == nil {
		parent = context.Background()
	}
	operationCtx, operationCancel := context.WithCancelCause(parent)
	readCtx, readCancel := context.WithCancel(parent)
	m := &scpCancelMonitor{
		targetPCID:      pcID,
		targetMessageID: messageID,
		cancelErr:       cancelErr,
		allowStoreRSP:   allowStoreRSP,
		operationCtx:    operationCtx,
		operationCancel: operationCancel,
		readCancel:      readCancel,
		commands:        make(chan scpInterleavedCommand, 1),
		done:            make(chan struct{}),
	}
	go m.run(readCtx, assoc)
	return m
}

func (m *scpCancelMonitor) Context() context.Context {
	if m == nil || m.operationCtx == nil {
		return context.Background()
	}
	return m.operationCtx
}

func (m *scpCancelMonitor) OperationError() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	err := m.err
	canceled := m.canceled
	m.mu.Unlock()
	if err != nil {
		if canceled {
			return errors.Join(m.cancelErr, err)
		}
		return err
	}
	if canceled {
		return m.cancelErr
	}
	return context.Cause(m.operationCtx)
}

func (m *scpCancelMonitor) Stop() error {
	if m == nil {
		return nil
	}
	m.stopOnce.Do(func() {
		m.readCancel()
		<-m.done
	})
	err := m.OperationError()
	m.operationCancel(context.Canceled)
	return err
}

func (m *scpCancelMonitor) receiveCStoreResponse(ctx context.Context, _ *ul.Association, expectedPCID byte) (*CStoreResponse, error) {
	if m == nil {
		return nil, fmt.Errorf("dicom dimse: nil C-GET command monitor")
	}
	select {
	case event, ok := <-m.commands:
		if !ok {
			if err := m.OperationError(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("dicom dimse: C-GET command monitor stopped before C-STORE response")
		}
		if event.pcID != expectedPCID {
			return nil, fmt.Errorf("%w: got %d, want %d", ErrPresentationContextMismatch, event.pcID, expectedPCID)
		}
		return ParseCStoreResponse(event.command)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *scpCancelMonitor) run(ctx context.Context, assoc *ul.Association) {
	defer close(m.done)
	defer close(m.commands)
	ctx = ul.WithoutIdleTimeout(ctx)
	for {
		pcID, command, err := ReceiveCommandSetAnyWithContext(ctx, assoc)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// The association layer reports a context deadline as
			// ErrAssociationTimeout. Preserve the context cause expected by the
			// SCP status mapping even when the socket deadline wins the race with
			// ctx.Err becoming observable.
			if errors.Is(err, ul.ErrAssociationTimeout) || errors.Is(err, context.DeadlineExceeded) {
				m.operationCancel(context.DeadlineExceeded)
				return
			}
			m.fail(err)
			return
		}
		field, err := CommandUint16(command, CommandField)
		if err != nil {
			m.fail(err)
			return
		}
		switch field {
		case CCancelRQ:
			cancel, err := ParseCCancelRequest(command)
			if err != nil {
				m.fail(err)
				return
			}
			if pcID != m.targetPCID || cancel.MessageIDBeingRespondedTo != m.targetMessageID {
				continue
			}
			m.mu.Lock()
			m.canceled = true
			m.mu.Unlock()
			m.operationCancel(m.cancelErr)
		case CStoreRSP:
			if !m.allowStoreRSP {
				m.fail(fmt.Errorf("dicom dimse: unexpected C-STORE-RSP during active operation"))
				return
			}
			select {
			case m.commands <- scpInterleavedCommand{pcID: pcID, command: command}:
			case <-ctx.Done():
				return
			}
		default:
			m.fail(fmt.Errorf("dicom dimse: unexpected command field 0x%04X during active operation", field))
			return
		}
	}
}

func (m *scpCancelMonitor) fail(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	if m.err == nil {
		m.err = err
	}
	m.mu.Unlock()
	m.operationCancel(err)
}
