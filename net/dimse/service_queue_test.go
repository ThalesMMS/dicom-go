package dimse

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceQueueRejectsWhenAdmissionFull(t *testing.T) {
	queue, err := NewServiceQueue(ServiceQueueOptions{
		MaxConcurrentAssociations: 1,
		MaxActiveOperations:       1,
	})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if err := queue.Enqueue(context.Background(), func(context.Context) {
		close(started)
		<-release
	}); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	<-started

	err = queue.Enqueue(context.Background(), func(context.Context) {})
	close(release)
	if !errors.Is(err, ErrServiceQueueFull) {
		t.Fatalf("second Enqueue() error = %v, want ErrServiceQueueFull", err)
	}
	if err := queue.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestServiceQueueSkipsCanceledQueuedJob(t *testing.T) {
	queue, err := NewServiceQueue(ServiceQueueOptions{
		MaxConcurrentAssociations: 2,
		MaxActiveOperations:       1,
		QueueDepth:                1,
	})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if err := queue.Enqueue(context.Background(), func(context.Context) {
		close(started)
		<-release
	}); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	<-started

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	ran := make(chan struct{})
	if err := queue.Enqueue(queuedCtx, func(context.Context) {
		close(ran)
	}); err != nil {
		t.Fatalf("queued Enqueue() error = %v", err)
	}
	cancelQueued()
	close(release)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := queue.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-ran:
		t.Fatalf("canceled queued job ran")
	default:
	}
}

func TestServiceQueueCloseWaitsForRunningJob(t *testing.T) {
	queue, err := NewServiceQueue(ServiceQueueOptions{MaxActiveOperations: 1})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if err := queue.Enqueue(context.Background(), func(context.Context) {
		close(started)
		<-release
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	<-started

	closeDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		closeDone <- queue.Close(ctx)
	}()

	select {
	case err := <-closeDone:
		t.Fatalf("Close() returned before running job finished: %v", err)
	default:
	}

	close(release)
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestServiceQueueRejectsCanceledAdmissionWithoutEnqueueTimeout(t *testing.T) {
	tests := []struct {
		name        string
		queueCancel bool
	}{
		{name: "caller context"},
		{name: "queue context", queueCancel: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			queueCtx, cancelQueue := context.WithCancel(context.Background())
			if tc.queueCancel {
				cancelQueue()
			} else {
				defer cancelQueue()
			}
			queue, err := NewServiceQueue(ServiceQueueOptions{Context: queueCtx, MaxActiveOperations: 1})
			if err != nil {
				t.Fatalf("NewServiceQueue() error = %v", err)
			}
			defer func() { _ = queue.Close(context.Background()) }()

			jobCtx := context.Background()
			if !tc.queueCancel {
				canceled, cancelJob := context.WithCancel(jobCtx)
				cancelJob()
				jobCtx = canceled
			}
			ran := make(chan struct{})
			err = queue.Enqueue(jobCtx, func(context.Context) { close(ran) })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Enqueue() error = %v, want context.Canceled", err)
			}
			select {
			case <-ran:
				t.Fatal("canceled job ran")
			default:
			}
		})
	}
}

func TestServiceQueueRecoversJobPanicAndRunsNextJob(t *testing.T) {
	queue, err := NewServiceQueue(ServiceQueueOptions{MaxActiveOperations: 1, QueueDepth: 1})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}
	if err := queue.Enqueue(context.Background(), func(context.Context) { panic("job failure") }); err != nil {
		t.Fatalf("Enqueue(panicking job) error = %v", err)
	}
	ran := make(chan struct{})
	if err := queue.Enqueue(context.Background(), func(context.Context) { close(ran) }); err != nil {
		t.Fatalf("Enqueue(next job) error = %v", err)
	}
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("worker stopped after job panic")
	}
	if err := queue.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := queue.Snapshot().JobPanics; got != 1 {
		t.Fatalf("JobPanics = %d, want 1", got)
	}
}

func TestServiceQueueCloseCancelsRunningJob(t *testing.T) {
	queue, err := NewServiceQueue(ServiceQueueOptions{MaxActiveOperations: 1})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	if err := queue.Enqueue(context.Background(), func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	<-started
	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := queue.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Close() did not cancel running job context")
	}
	metrics := queue.Snapshot()
	if metrics.Active != 0 || metrics.Queued != 0 || metrics.Completed != 1 {
		t.Fatalf("queue metrics after Close = %+v", metrics)
	}
}

func TestServiceQueueBoundsEstimatedBytesInFlight(t *testing.T) {
	queue, err := NewServiceQueue(ServiceQueueOptions{
		MaxActiveOperations: 1,
		QueueDepth:          1,
		MaxInFlightBytes:    10,
	})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	if err := queue.EnqueueBytes(context.Background(), 6, func(context.Context) {
		close(started)
		<-release
	}); err != nil {
		t.Fatalf("EnqueueBytes(first) error = %v", err)
	}
	<-started
	if err := queue.EnqueueBytes(context.Background(), 5, func(context.Context) {}); !errors.Is(err, ErrServiceQueueFull) {
		t.Fatalf("EnqueueBytes(over budget) error = %v, want ErrServiceQueueFull", err)
	}
	metrics := queue.Snapshot()
	if metrics.InFlightBytes != 6 || metrics.PeakInFlightBytes != 6 {
		t.Fatalf("byte metrics = %+v", metrics)
	}
	if err := queue.EnqueueBytes(context.Background(), 11, func(context.Context) {}); !errors.Is(err, ErrServiceQueueFull) {
		t.Fatalf("EnqueueBytes(single oversized job) error = %v, want ErrServiceQueueFull", err)
	}
	close(release)
	if err := queue.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := queue.Snapshot().InFlightBytes; got != 0 {
		t.Fatalf("InFlightBytes after close = %d, want 0", got)
	}
}

func TestServiceQueuePreservesEnqueueContextValuesAndDeadline(t *testing.T) {
	type contextKey struct{}
	queue, err := NewServiceQueue(ServiceQueueOptions{MaxActiveOperations: 1})
	if err != nil {
		t.Fatalf("NewServiceQueue() error = %v", err)
	}
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), contextKey{}, "request-value"), deadline)
	defer cancel()
	observed := make(chan context.Context, 1)
	if err := queue.Enqueue(ctx, func(jobCtx context.Context) { observed <- jobCtx }); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	jobCtx := <-observed
	if got := jobCtx.Value(contextKey{}); got != "request-value" {
		t.Fatalf("job context value = %v", got)
	}
	if got, ok := jobCtx.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatalf("job deadline = %v, %t; want %v", got, ok, deadline)
	}
	if err := queue.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
