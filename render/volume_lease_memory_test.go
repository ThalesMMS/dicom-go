package render

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVolumeLeaseTrackMemoryUsesExactGenerationAfterReplacement(t *testing.T) {
	store := NewVolumeStore()
	t.Cleanup(func() { _ = store.Close() })
	descriptor := testVolumeDescriptor(1, 1, 1)
	first, err := store.ReplaceFloat32(descriptor, []float32{1})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReplaceFloat32(descriptor, []float32{2})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("replacement reused a generation")
	}

	var callbacks atomic.Int32
	memory, err := lease.TrackMemory(
		VolumeMemoryBackendCopy,
		7,
		func() { callbacks.Add(1) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if got := store.Stats(); got.CurrentGeneration != second ||
		got.LiveGenerations != 2 ||
		got.TrackedRecords != 1 ||
		got.ActiveLeases != 1 {
		t.Fatalf("stats after exact-generation tracking = %+v", got)
	}

	third, err := store.ReplaceFloat32(descriptor, []float32{3})
	if err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 {
		t.Fatal("G1 callback was incorrectly attached to retired G2")
	}
	if got := store.Stats(); got.CurrentGeneration != third ||
		got.LiveGenerations != 2 ||
		got.TrackedRecords != 1 ||
		got.ActiveLeases != 1 {
		t.Fatalf("stats after G2 retirement = %+v", got)
	}

	if err := memory.Release(); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("release callbacks = %d, want 1", callbacks.Load())
	}
	if got := store.Stats(); got.LiveGenerations != 1 ||
		got.TrackedRecords != 0 ||
		got.ActiveLeases != 0 {
		t.Fatalf("stats after G1 backend release = %+v", got)
	}
	if _, err := lease.TrackMemory(
		VolumeMemoryBackendCopy,
		1,
		nil,
	); !errors.Is(err, ErrVolumeLeaseReleased) {
		t.Fatalf("TrackMemory after lease release error = %v", err)
	}
}

func TestVolumeLeaseTrackMemoryConcurrentReleaseIsLinearizable(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		store := NewVolumeStore()
		descriptor := testVolumeDescriptor(1, 1, 1)
		generation, err := store.ReplaceFloat32(
			descriptor,
			[]float32{float32(iteration)},
		)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.Acquire(generation)
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		done := make(chan struct{})
		var wait sync.WaitGroup
		var memory *VolumeMemoryLease
		var trackErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			memory, trackErr = lease.TrackMemory(
				VolumeMemoryBackendCopy,
				1,
				nil,
			)
		}()
		go func() {
			defer wait.Done()
			<-start
			_ = lease.Release()
		}()
		close(start)
		go func() {
			wait.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("TrackMemory and Release deadlocked")
		}

		switch {
		case trackErr == nil && memory != nil:
			if err := memory.Release(); err != nil {
				t.Fatal(err)
			}
		case errors.Is(trackErr, ErrVolumeLeaseReleased) && memory == nil:
		default:
			t.Fatalf(
				"non-linearizable result memory=%v error=%v",
				memory,
				trackErr,
			)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
