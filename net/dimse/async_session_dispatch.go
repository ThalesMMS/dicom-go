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
		for _, incoming := range s.registry.incoming {
			if !incoming.finishing {
				active = true
				break
			}
		}
		if active || len(s.registry.incoming) == 0 {
			s.mu.Unlock()
			if active {
				return ErrAsyncOperationsActive
			}
			return nil
		}
		changed := s.registry.stateChanged
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
	remaining := s.options.MaxQueuedMessageBytes - s.registry.queuedMessageBytes
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
	if size > s.options.MaxQueuedMessageBytes-s.registry.queuedMessageBytes {
		return ErrAsyncResourceLimit
	}
	s.registry.queuedMessageBytes += size
	s.registry.metrics.QueuedMessageBytes = s.registry.queuedMessageBytes
	if s.registry.queuedMessageBytes > s.registry.metrics.PeakQueuedMessageBytes {
		s.registry.metrics.PeakQueuedMessageBytes = s.registry.queuedMessageBytes
	}
	return nil
}

func (s *AsyncSession) releaseMessageBytes(size int64) {
	if size <= 0 {
		return
	}
	s.mu.Lock()
	s.registry.queuedMessageBytes -= size
	if s.registry.queuedMessageBytes < 0 {
		s.registry.queuedMessageBytes = 0
	}
	s.registry.metrics.QueuedMessageBytes = s.registry.queuedMessageBytes
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
		incoming, ok := s.registry.incoming[message.MessageID]
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
	operation, ok := s.registry.operations[message.MessageID]
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
	current, ok := s.registry.incoming[messageID]
	if !ok || current.requestField != requestField || current.generation != generation {
		return false
	}
	current.messageBytes += size
	s.registry.incoming[messageID] = current
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
	current, duplicate := s.registry.incoming[messageID]
	if duplicate {
		if current.finishing && current.writeCompleted {
			changed := s.registry.stateChanged
			s.mu.Unlock()
			select {
			case <-changed:
				goto retry
			case <-s.done:
				return false, 0, 0, s.sessionError()
			}
		}
		s.registry.metrics.DuplicateInvocations++
	}
	if !duplicate && s.wirePerformedUnlimited && len(s.registry.incoming) >= s.options.MaxPendingRequests {
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
		s.registry.metrics.WindowViolations++
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
	if current, duplicate = s.registry.incoming[messageID]; duplicate {
		finishing := current.finishing && current.writeCompleted
		changed := s.registry.stateChanged
		if !finishing {
			s.registry.metrics.DuplicateInvocations++
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
	s.registry.nextIncomingID++
	if s.registry.nextIncomingID == 0 {
		s.registry.nextIncomingID++
	}
	generation := s.registry.nextIncomingID
	handlerCtx, cancel := context.WithCancel(context.WithValue(s.ctx, asyncHandlerSessionContextKey{}, s))
	s.registry.incoming[messageID] = asyncIncomingOperation{
		cancel:       cancel,
		ctx:          handlerCtx,
		requestField: field,
		pcID:         pcID,
		cancelable:   asyncCommandAllowsPending(field),
		responseMu:   &sync.Mutex{},
		slotHeld:     slotHeld,
		generation:   generation,
	}
	s.registry.metrics.ActivePerformed++
	if s.registry.metrics.ActivePerformed > s.registry.metrics.PeakPerformed {
		s.registry.metrics.PeakPerformed = s.registry.metrics.ActivePerformed
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
	s.registry.pendingIncoming++
	s.signalStateChangedLocked()
	s.mu.Unlock()

	prepared, status, generation, err := s.prepareIncomingRequest(pcID, command)

	s.mu.Lock()
	if s.registry.pendingIncoming > 0 {
		s.registry.pendingIncoming--
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()
	return prepared, status, generation, err
}

func (s *AsyncSession) dispatchPreparedRequest(message AsyncMessage) error {
	s.mu.Lock()
	incoming, active := s.registry.incoming[message.MessageID]
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
				current, ok := s.registry.incoming[message.MessageID]
				if ok && current.requestField == message.CommandField && current.generation == message.incomingGeneration {
					current.slotHeld = true
					s.registry.incoming[message.MessageID] = current
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
		current, active := s.registry.incoming[message.MessageID]
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
	current, ok := s.registry.incoming[messageID]
	if !ok || current.requestField != requestField || current.generation != generation {
		return 0
	}
	size := current.messageBytes
	current.messageBytes = 0
	s.registry.incoming[messageID] = current
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
		for _, incoming := range s.registry.incoming {
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
		changed := s.registry.stateChanged
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
	current, ok := s.registry.incoming[messageID]
	if ok && current.requestField == requestField && current.generation == generation && current.finishing {
		current.writeStarted = true
		s.registry.incoming[messageID] = current
		s.signalStateChangedLocked()
	}
	s.mu.Unlock()
}

// finishIncomingOperation is idempotent and is called after a terminal
// response is fully written, while the message-level writer is still held.
func (s *AsyncSession) markIncomingWriteCompleted(messageID, requestField uint16, generation uint64) {
	s.mu.Lock()
	current, ok := s.registry.incoming[messageID]
	if ok && current.requestField == requestField && current.generation == generation && current.finishing {
		current.writeCompleted = true
		s.registry.incoming[messageID] = current
		s.signalStateChangedLocked()
	}
	s.mu.Unlock()
}

func (s *AsyncSession) finishIncomingOperation(messageID, requestField uint16, generation uint64) bool {
	s.mu.Lock()
	current, ok := s.registry.incoming[messageID]
	if !ok || current.requestField != requestField || current.generation != generation {
		s.mu.Unlock()
		return false
	}
	delete(s.registry.incoming, messageID)
	s.registry.metrics.ActivePerformed--
	if current.slotHeld {
		<-s.performedSlots
	}
	s.signalStateChangedLocked()
	s.mu.Unlock()
	s.releaseMessageBytes(current.messageBytes)
	return true
}
