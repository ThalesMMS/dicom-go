package render

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVolumePreparationPublishesOneCanonicalGeneration(t *testing.T) {
	stack := gradientColumnStack(32, 24, 12)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()

	first, err := volume.PrepareSnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := uint64(volume.Cols * volume.Rows * volume.Depth * 4)
	if first.Reused || first.VoxelBytes != wantBytes || first.TransientBytes != 0 ||
		first.Frames != volume.Depth || first.TotalDuration < first.NormalizationDuration+first.CanonicalizationDuration {
		t.Fatalf("first preparation stats = %+v, want one direct canonical build of %d bytes", first, wantBytes)
	}

	second, err := volume.PrepareSnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.VoxelBytes != wantBytes || second.TransientBytes != 0 ||
		second.NormalizationDuration != 0 || second.CanonicalizationDuration != 0 {
		t.Fatalf("warm preparation stats = %+v", second)
	}
	stats := volume.VolumeStoreStats()
	if stats.Replacements != 1 || stats.LiveGenerations != 1 || stats.LiveBytes != wantBytes {
		t.Fatalf("canonical store stats = %+v", stats)
	}

	lease, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	snapshot, err := lease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := snapshot.ModalityAt(11, 7, 4); !ok || value != 11 {
		t.Fatalf("canonical modality = %v/%v, want 11/true", value, ok)
	}
}

func TestVolumePreparationCancellationDoesNotPoisonRetry(t *testing.T) {
	stack := gradientColumnStack(64, 64, 24)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()

	ctx := &cancelAfterErrChecks{done: make(chan struct{})}
	ctx.remaining.Store(8)
	if _, err := volume.PrepareSnapshotContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation error = %v, want context.Canceled", err)
	}
	if got := volume.VolumeStoreStats().Replacements; got != 0 {
		t.Fatalf("canceled preparation published %d generations", got)
	}

	stats, err := volume.PrepareSnapshotContext(context.Background())
	if err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
	if stats.Reused {
		t.Fatalf("retry stats = %+v, want a fresh build", stats)
	}
	if got := volume.VolumeStoreStats().Replacements; got != 1 {
		t.Fatalf("retry replacements = %d, want 1", got)
	}
}

func TestVolumePreparationConcurrentCallersShareBuild(t *testing.T) {
	stack := gradientColumnStack(128, 128, 48)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()

	const workers = 12
	start := make(chan struct{})
	results := make(chan VolumePreparationStats, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stats, prepareErr := volume.PrepareSnapshotContext(context.Background())
			results <- stats
			errs <- prepareErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent preparation: %v", err)
		}
	}
	builders := 0
	for stats := range results {
		if !stats.Reused {
			builders++
		}
	}
	if builders != 1 {
		t.Fatalf("non-reused preparations = %d, want exactly one builder", builders)
	}
	if got := volume.VolumeStoreStats().Replacements; got != 1 {
		t.Fatalf("concurrent replacements = %d, want 1", got)
	}
}

type cancelAfterErrChecks struct {
	remaining atomic.Int64
	once      sync.Once
	done      chan struct{}
}

func (c *cancelAfterErrChecks) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterErrChecks) Done() <-chan struct{}       { return c.done }
func (c *cancelAfterErrChecks) Value(any) any               { return nil }

func (c *cancelAfterErrChecks) Err() error {
	if c.remaining.Add(-1) > 0 {
		return nil
	}
	c.once.Do(func() { close(c.done) })
	return context.Canceled
}
