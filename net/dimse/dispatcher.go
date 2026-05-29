package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	// ErrUnexpectedCommand marks a command that has no matching dispatcher
	// handler for its command field and message ID.
	ErrUnexpectedCommand = errors.New("dicom dimse: unexpected command")
	// ErrAssociationReleased marks a clean A-RELEASE-RQ received from the peer.
	ErrAssociationReleased = errors.New("dicom dimse: association released")
)

// UnexpectedCommandError reports an unhandled DIMSE command.
type UnexpectedCommandError struct {
	CommandField uint16
	MessageID    uint16
}

func (e *UnexpectedCommandError) Error() string {
	if e == nil {
		return ErrUnexpectedCommand.Error()
	}
	return fmt.Sprintf("%s field=0x%04X messageID=%d", ErrUnexpectedCommand, e.CommandField, e.MessageID)
}

func (e *UnexpectedCommandError) Unwrap() error {
	return ErrUnexpectedCommand
}

// CommandHandler handles one decoded DIMSE command set.
type CommandHandler func(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object) error

type dispatcherHandlerKey struct {
	commandField uint16
	messageID    uint16
}

// Dispatcher routes DIMSE command sets received on an association.
type Dispatcher struct {
	assoc *ul.Association

	mu           sync.Mutex
	handlers     map[uint16]CommandHandler
	onceHandlers map[dispatcherHandlerKey]CommandHandler
}

// NewDispatcher creates a dispatcher for an established association.
func NewDispatcher(assoc *ul.Association) *Dispatcher {
	return &Dispatcher{
		assoc:        assoc,
		handlers:     make(map[uint16]CommandHandler),
		onceHandlers: make(map[dispatcherHandlerKey]CommandHandler),
	}
}

// Handle registers a persistent handler for a command field.
func (d *Dispatcher) Handle(commandField uint16, handler CommandHandler) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if handler == nil {
		delete(d.handlers, commandField)
		return
	}
	d.handlers[commandField] = handler
}

// HandleOnce registers a one-shot handler for a command field and message ID.
func (d *Dispatcher) HandleOnce(commandField uint16, messageID uint16, handler CommandHandler) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := dispatcherHandlerKey{commandField: commandField, messageID: messageID}
	if handler == nil {
		delete(d.onceHandlers, key)
		return
	}
	d.onceHandlers[key] = handler
}

// Remove removes a persistent command-field handler.
func (d *Dispatcher) Remove(commandField uint16) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.handlers, commandField)
}

// RemoveOnce removes a one-shot command-field/message-ID handler.
func (d *Dispatcher) RemoveOnce(commandField uint16, messageID uint16) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.onceHandlers, dispatcherHandlerKey{commandField: commandField, messageID: messageID})
}

// Next receives and dispatches one command set.
func (d *Dispatcher) Next(ctx context.Context) error {
	if d == nil {
		return fmt.Errorf("dicom dimse: nil dispatcher")
	}
	release, err := beginAssociationOperation(d.assoc)
	if err != nil {
		return err
	}
	defer release()
	return d.next(ctx)
}

func (d *Dispatcher) next(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := classifyDispatcherReceiveError(ctx.Err()); err != nil {
		return err
	}

	pcID, command, released, err := receiveDispatcherCommandOrRelease(ctx, d.assoc)
	if released {
		if err != nil {
			return fmt.Errorf("%w: release response failed: %w", ErrAssociationReleased, err)
		}
		return ErrAssociationReleased
	}
	if err != nil {
		return classifyDispatcherReceiveError(err)
	}

	commandField, err := CommandUint16(command, CommandField)
	if err != nil {
		return err
	}
	messageID, err := dispatcherMessageID(command)
	if err != nil {
		return err
	}
	handler, ok := d.takeHandler(commandField, messageID)
	if !ok {
		return &UnexpectedCommandError{CommandField: commandField, MessageID: messageID}
	}
	return handler(ctx, d.assoc, pcID, command)
}

// Run dispatches commands until an error occurs. A clean peer release is normal
// completion and returns nil.
func (d *Dispatcher) Run(ctx context.Context) error {
	if d == nil {
		return fmt.Errorf("dicom dimse: nil dispatcher")
	}
	release, err := beginAssociationOperation(d.assoc)
	if err != nil {
		return err
	}
	defer release()
	for {
		err := d.next(ctx)
		if err == nil {
			continue
		}
		if err == ErrAssociationReleased {
			return nil
		}
		return err
	}
}

func (d *Dispatcher) takeHandler(commandField, messageID uint16) (CommandHandler, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := dispatcherHandlerKey{commandField: commandField, messageID: messageID}
	if handler, ok := d.onceHandlers[key]; ok {
		delete(d.onceHandlers, key)
		return handler, true
	}
	handler, ok := d.handlers[commandField]
	return handler, ok
}

func dispatcherMessageID(command *object.Object) (uint16, error) {
	if command == nil {
		return 0, fmt.Errorf("dicom dimse: nil command set")
	}
	if _, ok := command.Get(MessageIDBeingRespondedTo); ok {
		return CommandUint16(command, MessageIDBeingRespondedTo)
	}
	if _, ok := command.Get(MessageID); ok {
		return CommandUint16(command, MessageID)
	}
	return 0, fmt.Errorf("dicom dimse: command set has no Message ID correlation element")
}

func receiveDispatcherCommandOrRelease(ctx context.Context, assoc *ul.Association) (byte, *object.Object, bool, error) {
	pcID, command, _, control, err := receiveDIMSECommandOrControl(ctx, assoc)
	if err != nil {
		return 0, nil, false, err
	}
	if control == nil {
		return pcID, command, false, nil
	}
	if _, ok := control.(*ul.ReleaseRQ); !ok {
		return 0, nil, false, fmt.Errorf("%w: got %T while waiting for P-DATA-TF or A-RELEASE-RQ", ul.ErrUnexpectedPDU, control)
	}
	err = assoc.Send(ctx, &ul.ReleaseRP{})
	ctxErr := ctx.Err()
	if ctxErr == nil && errors.Is(err, ul.ErrAssociationTimeout) {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			ctxErr = context.DeadlineExceeded
		}
	}
	if err != nil && ctxErr != nil && !errors.Is(err, ctxErr) {
		err = errors.Join(err, ctxErr)
	}
	return 0, nil, true, err
}

// receiveDIMSECommandOrControl is the single command assembler shared by the
// legacy Dispatcher and AsyncSession. It never reads an accompanying dataset;
// the sole association owner must do so before requesting the next command.
func receiveDIMSECommandOrControl(ctx context.Context, assoc *ul.Association) (byte, *object.Object, int64, ul.PDU, error) {
	if assoc == nil {
		return 0, nil, 0, nil, fmt.Errorf("dicom dimse: nil association")
	}
	var (
		pcID     byte
		havePCID bool
		buf      []byte
	)
	for {
		var values []ul.PDataValue
		carryover, err := takePDataCarryoverWithContext(ctx, assoc)
		if err != nil {
			return 0, nil, 0, nil, err
		}
		if len(carryover) > 0 {
			values = carryover
		} else {
			pdu, err := assoc.Receive(ctx)
			if err != nil {
				return 0, nil, 0, nil, err
			}
			pdata, ok := pdu.(*ul.PDataTF)
			if !ok {
				if havePCID || len(buf) != 0 {
					return 0, nil, 0, nil, fmt.Errorf("%w: got %T during fragmented DIMSE command", ul.ErrUnexpectedPDU, pdu)
				}
				return 0, nil, 0, pdu, nil
			}
			values = pdata.Values
		}
		for i, value := range values {
			if !value.IsCommand {
				return 0, nil, 0, nil, fmt.Errorf("dicom dimse: command assembler expected command P-DATA")
			}
			if !havePCID {
				pcID = value.PresentationContextID
				havePCID = true
			} else if value.PresentationContextID != pcID {
				return 0, nil, 0, nil, fmt.Errorf("%w: got %d, want %d", ErrPresentationContextMismatch, value.PresentationContextID, pcID)
			}
			var err error
			buf, err = appendCommandSetFragment(buf, value.Data)
			if err != nil {
				return 0, nil, 0, nil, err
			}
			if value.IsLast {
				if err := storePDataCarryoverWithContext(ctx, assoc, values[i+1:]); err != nil {
					return 0, nil, 0, nil, err
				}
				command, err := DecodeCommandSet(buf)
				if err == nil {
					observeCommand(assoc, telemetry.Inbound, pcID, command, 0)
				}
				return pcID, command, int64(len(buf)), nil, err
			}
		}
		ctx = ul.WithActiveTransfer(ctx)
	}
}

func classifyDispatcherReceiveError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrOperationCanceled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ul.ErrAssociationTimeout) {
		return fmt.Errorf("%w: %w", ErrOperationTimeout, err)
	}
	return err
}
