package dimse

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

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
	incoming, active := s.registry.incoming[request.MessageID]
	s.mu.Unlock()
	if !active || incoming.requestField != request.CommandField || incoming.pcID != request.PresentationContextID || incoming.generation != request.incomingGeneration {
		return ErrAsyncOperationComplete
	}
	incoming.responseMu.Lock()
	defer incoming.responseMu.Unlock()
	s.mu.Lock()
	incoming, active = s.registry.incoming[request.MessageID]
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
		current, ok := s.registry.incoming[request.MessageID]
		if !ok || current.requestField != request.CommandField || current.generation != request.incomingGeneration || current.finishing {
			s.mu.Unlock()
			return ErrAsyncOperationComplete
		}
		current.finishing = true
		s.registry.incoming[request.MessageID] = current
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
			if current, ok := s.registry.incoming[request.MessageID]; ok && current.requestField == request.CommandField && current.generation == request.incomingGeneration {
				current.finishing = false
				current.writeStarted = false
				current.writeCompleted = false
				s.registry.incoming[request.MessageID] = current
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

func (s *AsyncSession) sendEncodedMessage(ctx context.Context, pcID byte, command []core.Element, writeDataSet CStoreDataSetWriter) error {
	if ctx == nil {
		ctx = context.Background()
	}
	syntax, err := s.validateOutgoingCommand(pcID, command, true)
	if err != nil {
		return err
	}
	return s.writeValidatedEncodedMessage(ctx, pcID, command, writeDataSet, syntax)
}

func (s *AsyncSession) validateOutgoingMessage(pcID byte, command []core.Element, dataset *object.Object) (transfer.Syntax, error) {
	return s.validateOutgoingCommand(pcID, command, dataset != nil)
}

func (s *AsyncSession) validateOutgoingCommand(pcID byte, command []core.Element, hasDataSet bool) (transfer.Syntax, error) {
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
	if dataSetType == NoDataSet && hasDataSet {
		return transfer.Syntax{}, fmt.Errorf("dicom dimse: command forbids accompanying dataset")
	}
	if dataSetType != NoDataSet && !hasDataSet {
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

func (s *AsyncSession) writeValidatedEncodedMessage(ctx context.Context, pcID byte, command []core.Element, writeDataSet CStoreDataSetWriter, syntax transfer.Syntax) error {
	ctx = s.owner.Context(ctx)
	return s.assoc.SerializeMessageWriteContext(ctx, func() error {
		if err := SendCommandSetWithContext(ctx, s.assoc, pcID, command); err != nil {
			return err
		}
		writeCtx := dataSetWriteContext(ctx)
		writer := NewPDataWriterWithContext(writeCtx, s.assoc, pcID, false, peerMaxPDUWithHeader(s.assoc))
		if err := writeDataSet(writeCtx, writer, syntax); err != nil {
			return fmt.Errorf("dicom dimse: send dataset: %w", err)
		}
		if err := writer.Finish(); err != nil {
			return fmt.Errorf("dicom dimse: finish dataset P-DATA: %w", err)
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
