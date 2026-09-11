package dimse

import (
	"context"
	"errors"
	"io"
)

// Next returns the next response, including each Pending response and its
// dataset. After the terminal response, the next call returns io.EOF.
func (o *AsyncOperation) Next(ctx context.Context) (AsyncMessage, error) {
	if o == nil {
		return AsyncMessage{}, ErrAsyncSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !o.cancelable {
		var cancel context.CancelFunc
		ctx, cancel = o.session.suboperationContext(ctx)
		defer cancel()
	}
	select {
	case message := <-o.responses:
		o.releaseResponseAccounting(message.messageBytes)
		return message, nil
	default:
	}
	select {
	case message := <-o.responses:
		o.releaseResponseAccounting(message.messageBytes)
		return message, nil
	case <-ctx.Done():
		return AsyncMessage{}, ctx.Err()
	case <-o.done:
		select {
		case message := <-o.responses:
			o.releaseResponseAccounting(message.messageBytes)
			return message, nil
		default:
			return AsyncMessage{}, o.operationError()
		}
	}
}

// Wait drains responses and returns the terminal message.
func (o *AsyncOperation) Wait(ctx context.Context) (AsyncMessage, error) {
	var terminal AsyncMessage
	for {
		message, err := o.Next(ctx)
		if err == nil {
			terminal = message
			continue
		}
		if errors.Is(err, io.EOF) && terminal.Command != nil {
			return terminal, nil
		}
		return AsyncMessage{}, err
	}
}

// DiscardResponses transfers ownership of all queued responses away from the
// session without returning them. Call it when an operation will not be
// drained with Next or Wait; otherwise its bounded mailbox remains accounted
// to MaxQueuedMessageBytes for as long as the operation is retained.
func (o *AsyncOperation) DiscardResponses() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.discarding = true
	o.mu.Unlock()
	for {
		select {
		case message := <-o.responses:
			o.releaseResponseAccounting(message.messageBytes)
		default:
			return
		}
	}
}

// MessageID returns the operation's association-wide correlation identifier.
func (o *AsyncOperation) MessageID() uint16 {
	if o == nil {
		return 0
	}
	return o.messageID
}

// Done closes after a terminal response or session failure.
func (o *AsyncOperation) Done() <-chan struct{} {
	if o == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return o.done
}

func (o *AsyncOperation) operationError() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	return io.EOF
}

func (o *AsyncOperation) releaseResponseAccounting(size int64) {
	if o == nil || o.session == nil || size <= 0 {
		return
	}
	o.mu.Lock()
	release := !o.accountingTransferred
	if release {
		o.queuedBytes -= size
		if o.queuedBytes < 0 {
			o.queuedBytes = 0
		}
	}
	o.mu.Unlock()
	if release {
		o.session.releaseMessageBytes(size)
	}
}

func (o *AsyncOperation) transferResponseAccounting() int64 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.accountingTransferred {
		return 0
	}
	o.accountingTransferred = true
	size := o.queuedBytes
	o.queuedBytes = 0
	return size
}

func (o *AsyncOperation) finish(err error) {
	o.finishOnce.Do(func() {
		o.mu.Lock()
		o.err = err
		o.finished = true
		cancel := o.contextCancel
		o.contextCancel = nil
		o.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		close(o.done)
	})
}

func (o *AsyncOperation) setContextCancel(cancel context.CancelFunc) {
	if o == nil || cancel == nil {
		return
	}
	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		cancel()
		return
	}
	o.contextCancel = cancel
	o.mu.Unlock()
}
