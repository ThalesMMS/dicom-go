package dimse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrServiceQueueFull   = errors.New("dicom dimse: service queue full")
	ErrServiceQueueClosed = errors.New("dicom dimse: service queue closed")
)

// ServiceJob is one SCP/service unit of work admitted by ServiceQueue.
type ServiceJob func(context.Context)

// ServiceQueueOptions configures bounded SCP/service admission.
type ServiceQueueOptions struct {
	// Context cancels future admissions and skips queued jobs that have not
	// started yet. Nil means context.Background().
	Context context.Context
	// MaxConcurrentAssociations bounds active plus queued jobs when greater
	// than zero.
	MaxConcurrentAssociations int
	// MaxActiveOperations is the number of worker slots. It must be positive.
	MaxActiveOperations int
	// QueueDepth is the number of jobs that may wait for a worker.
	QueueDepth int
	// EnqueueTimeout bounds waiting for admission or queue space. Zero means a
	// full queue fails immediately.
	EnqueueTimeout time.Duration
	// MaxInFlightBytes bounds the caller-supplied byte estimates of active plus
	// queued jobs. Zero disables byte admission.
	MaxInFlightBytes int64
}

// ServiceQueue provides bounded worker and queue admission for SCP services.
type ServiceQueue struct {
	ctx            context.Context
	cancel         context.CancelFunc
	enqueueTimeout time.Duration
	jobs           chan queuedServiceJob
	admission      chan struct{}
	done           chan struct{}
	maxBytes       int64

	closeMu sync.RWMutex
	closed  bool
	workers sync.WaitGroup
	bytesMu sync.Mutex
	// byteChanged is replaced and closed whenever byte capacity is released.
	byteChanged   chan struct{}
	inFlightBytes int64

	active    atomic.Int64
	queued    atomic.Int64
	admitted  atomic.Uint64
	completed atomic.Uint64
	rejected  atomic.Uint64
	jobPanics atomic.Uint64
	peakBytes atomic.Int64
}

type queuedServiceJob struct {
	ctx     context.Context
	job     ServiceJob
	release func()
	cancel  func()
}

type ServiceQueueMetrics struct {
	Active            int64
	Queued            int64
	InFlightBytes     int64
	PeakInFlightBytes int64
	Admitted          uint64
	Completed         uint64
	Rejected          uint64
	JobPanics         uint64
}

func NewServiceQueue(options ServiceQueueOptions) (*ServiceQueue, error) {
	if options.MaxActiveOperations <= 0 {
		return nil, fmt.Errorf("dicom dimse: MaxActiveOperations must be positive")
	}
	if options.MaxConcurrentAssociations < 0 {
		return nil, fmt.Errorf("dicom dimse: MaxConcurrentAssociations must not be negative")
	}
	if options.QueueDepth < 0 {
		return nil, fmt.Errorf("dicom dimse: QueueDepth must not be negative")
	}
	if options.EnqueueTimeout < 0 {
		return nil, fmt.Errorf("dicom dimse: EnqueueTimeout must not be negative")
	}
	if options.MaxInFlightBytes < 0 {
		return nil, fmt.Errorf("dicom dimse: MaxInFlightBytes must not be negative")
	}

	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	queueCtx, cancelQueue := context.WithCancel(ctx)
	capacity := options.MaxActiveOperations + options.QueueDepth
	if options.MaxConcurrentAssociations > 0 && options.MaxConcurrentAssociations < capacity {
		capacity = options.MaxConcurrentAssociations
	}
	queue := &ServiceQueue{
		ctx:            queueCtx,
		cancel:         cancelQueue,
		enqueueTimeout: options.EnqueueTimeout,
		jobs:           make(chan queuedServiceJob, capacity),
		admission:      make(chan struct{}, capacity),
		done:           make(chan struct{}),
		maxBytes:       options.MaxInFlightBytes,
		byteChanged:    make(chan struct{}),
	}
	for i := 0; i < options.MaxActiveOperations; i++ {
		queue.workers.Add(1)
		go queue.worker()
	}
	go func() {
		queue.workers.Wait()
		close(queue.done)
	}()
	return queue, nil
}

func (q *ServiceQueue) Enqueue(ctx context.Context, job ServiceJob) error {
	return q.EnqueueBytes(ctx, 0, job)
}

// EnqueueBytes admits one job together with a non-negative estimate of the
// memory/object bytes it keeps in flight. The estimate is released only after
// the job finishes or is skipped because its context was canceled.
func (q *ServiceQueue) EnqueueBytes(ctx context.Context, estimatedBytes int64, job ServiceJob) error {
	if q == nil {
		return ErrServiceQueueClosed
	}
	if job == nil {
		return fmt.Errorf("dicom dimse: nil service job")
	}
	if estimatedBytes < 0 {
		return fmt.Errorf("dicom dimse: estimated in-flight bytes must not be negative")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.closeMu.RLock()
	closed := q.closed
	q.closeMu.RUnlock()
	if closed {
		return ErrServiceQueueClosed
	}
	enqueueCtx := ctx
	cancel := func() {}
	if q.enqueueTimeout > 0 {
		enqueueCtx, cancel = context.WithTimeout(ctx, q.enqueueTimeout)
	}
	defer cancel()

	release, err := q.acquireAdmission(enqueueCtx)
	if err != nil {
		q.rejected.Add(1)
		return err
	}
	releaseBytes, err := q.acquireBytes(enqueueCtx, estimatedBytes)
	if err != nil {
		release()
		q.rejected.Add(1)
		return err
	}
	releaseResources := func() {
		releaseBytes()
		release()
	}

	q.closeMu.RLock()
	defer q.closeMu.RUnlock()
	if q.closed {
		releaseResources()
		return ErrServiceQueueClosed
	}
	jobCtx, cancelJob := context.WithCancel(ctx)
	stopQueueCancel := context.AfterFunc(q.ctx, cancelJob)
	cleanupJob := func() {
		stopQueueCancel()
		cancelJob()
	}
	q.queued.Add(1)
	q.admitted.Add(1)
	q.jobs <- queuedServiceJob{ctx: jobCtx, job: job, release: releaseResources, cancel: cleanupJob}
	return nil
}

func (q *ServiceQueue) Close(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.closeMu.Lock()
	if !q.closed {
		q.closed = true
		q.cancel()
		close(q.jobs)
	}
	q.closeMu.Unlock()

	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *ServiceQueue) acquireAdmission(ctx context.Context) (func(), error) {
	if err := q.admissionContextError(ctx); err != nil {
		return nil, err
	}
	if q.enqueueTimeout <= 0 {
		select {
		case q.admission <- struct{}{}:
			if err := q.admissionContextError(ctx); err != nil {
				q.releaseAdmission()
				return nil, err
			}
			return q.releaseAdmission, nil
		case <-ctx.Done():
			return nil, serviceQueueContextError(ctx.Err())
		case <-q.ctx.Done():
			return nil, serviceQueueContextError(q.ctx.Err())
		default:
			return nil, ErrServiceQueueFull
		}
	}
	select {
	case q.admission <- struct{}{}:
		if err := q.admissionContextError(ctx); err != nil {
			q.releaseAdmission()
			return nil, err
		}
		return q.releaseAdmission, nil
	case <-ctx.Done():
		return nil, serviceQueueContextError(ctx.Err())
	case <-q.ctx.Done():
		return nil, serviceQueueContextError(q.ctx.Err())
	}
}

func (q *ServiceQueue) admissionContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return serviceQueueContextError(err)
	}
	if err := q.ctx.Err(); err != nil {
		return serviceQueueContextError(err)
	}
	return nil
}

func (q *ServiceQueue) releaseAdmission() {
	<-q.admission
}

func (q *ServiceQueue) acquireBytes(ctx context.Context, estimatedBytes int64) (func(), error) {
	if q.maxBytes == 0 || estimatedBytes == 0 {
		return func() {}, nil
	}
	if estimatedBytes > q.maxBytes {
		return nil, ErrServiceQueueFull
	}
	for {
		if err := q.admissionContextError(ctx); err != nil {
			return nil, err
		}
		q.bytesMu.Lock()
		if q.inFlightBytes <= q.maxBytes-estimatedBytes {
			q.inFlightBytes += estimatedBytes
			inFlight := q.inFlightBytes
			q.bytesMu.Unlock()
			for peak := q.peakBytes.Load(); inFlight > peak && !q.peakBytes.CompareAndSwap(peak, inFlight); peak = q.peakBytes.Load() {
			}
			var once sync.Once
			return func() {
				once.Do(func() { q.releaseBytes(estimatedBytes) })
			}, nil
		}
		changed := q.byteChanged
		q.bytesMu.Unlock()
		if q.enqueueTimeout <= 0 {
			return nil, ErrServiceQueueFull
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, serviceQueueContextError(ctx.Err())
		case <-q.ctx.Done():
			return nil, serviceQueueContextError(q.ctx.Err())
		}
	}
}

func (q *ServiceQueue) releaseBytes(estimatedBytes int64) {
	q.bytesMu.Lock()
	q.inFlightBytes -= estimatedBytes
	close(q.byteChanged)
	q.byteChanged = make(chan struct{})
	q.bytesMu.Unlock()
}

func (q *ServiceQueue) worker() {
	defer q.workers.Done()
	for queued := range q.jobs {
		q.run(queued)
	}
}

func (q *ServiceQueue) run(queued queuedServiceJob) {
	defer queued.release()
	defer queued.cancel()
	q.queued.Add(-1)
	q.active.Add(1)
	defer func() {
		q.active.Add(-1)
		q.completed.Add(1)
		if recover() != nil {
			q.jobPanics.Add(1)
		}
	}()
	if q.ctx.Err() != nil || queued.ctx.Err() != nil {
		return
	}
	queued.job(queued.ctx)
}

func (q *ServiceQueue) Snapshot() ServiceQueueMetrics {
	if q == nil {
		return ServiceQueueMetrics{}
	}
	q.bytesMu.Lock()
	inFlightBytes := q.inFlightBytes
	q.bytesMu.Unlock()
	return ServiceQueueMetrics{
		Active:            q.active.Load(),
		Queued:            q.queued.Load(),
		InFlightBytes:     inFlightBytes,
		PeakInFlightBytes: q.peakBytes.Load(),
		Admitted:          q.admitted.Load(),
		Completed:         q.completed.Load(),
		Rejected:          q.rejected.Load(),
		JobPanics:         q.jobPanics.Load(),
	}
}

func serviceQueueContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrServiceQueueFull
	}
	return err
}
