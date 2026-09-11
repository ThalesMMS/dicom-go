package jpeg2000

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoundDecoderContextUsesTighterCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	ctx, stop := boundDecoderContext(parent, time.Second)
	defer stop()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("bounded context missing deadline")
	}
	parentDeadline, _ := parent.Deadline()
	if deadline.After(parentDeadline) {
		t.Fatalf("deadline %v is later than caller deadline %v", deadline, parentDeadline)
	}
}

func TestBoundDecoderContextUsesCodecTimeoutWhenCallerIsLooser(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, stop := boundDecoderContext(parent, 25*time.Millisecond)
	defer stop()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("bounded context missing deadline")
	}
	parentDeadline, _ := parent.Deadline()
	if !deadline.Before(parentDeadline) {
		t.Fatalf("deadline %v should be the codec timeout before %v", deadline, parentDeadline)
	}
}

func TestMapBoundDecoderErrorKeepsCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	run, stop := boundDecoderContext(parent, time.Second)
	defer stop()
	<-run.Done()

	err := mapBoundDecoderError(parent, run)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrDecoderTimeout) {
		t.Fatalf("caller deadline classified as codec timeout: %v", err)
	}
}

func TestMapBoundDecoderErrorDoesNotWrapCodecTimeout(t *testing.T) {
	parent := context.Background()
	run, stop := boundDecoderContext(parent, 15*time.Millisecond)
	defer stop()
	<-run.Done()

	err := mapBoundDecoderError(parent, run)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("codec timeout wrapped context.DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, ErrDecoderTimeout) {
		t.Fatalf("codec timeout error = %v, want ErrDecoderTimeout", err)
	}
}

func TestMapBoundDecoderErrorPropagatesCallerCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	run, stop := boundDecoderContext(parent, time.Second)
	defer stop()
	cancel()
	<-run.Done()

	err := mapBoundDecoderError(parent, run)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancel error = %v, want context.Canceled", err)
	}
}
