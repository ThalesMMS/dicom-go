package jpeg2000

import (
	"context"
	"errors"
	"time"
)

func boundDecoderContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	if deadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(deadline); remaining <= timeout {
			return context.WithCancel(parent)
		}
	}
	return context.WithTimeout(parent, timeout)
}

func mapBoundDecoderError(parent, run context.Context) error {
	if parent != nil {
		if err := parent.Err(); err != nil {
			return err
		}
	}
	if run == nil {
		return nil
	}
	if errors.Is(run.Err(), context.DeadlineExceeded) {
		return ErrDecoderTimeout
	}
	return run.Err()
}
