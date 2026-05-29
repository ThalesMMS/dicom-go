package render

import (
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVolumeSnapshotV1ValidationAndReadOnlyCopy(t *testing.T) {
	descriptor := testVolumeDescriptor(2, 2, 2)
	payload := make([]byte, descriptor.ByteLength)
	for index := 0; index < 8; index++ {
		binary.LittleEndian.PutUint32(payload[index*4:], math.Float32bits(float32(index)+0.5))
	}
	store := NewVolumeStore()
	generation, err := store.Replace(VolumeInput{Descriptor: descriptor, Payload: payload})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	payload[0] = 0xff
	lease, err := store.Acquire(generation)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	snapshot, err := lease.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	gotDescriptor, err := snapshot.Descriptor()
	if err != nil {
		t.Fatalf("Descriptor: %v", err)
	}
	if gotDescriptor.ContractVersion != 1 || gotDescriptor.VolumeGeneration != generation ||
		gotDescriptor.Dimensions != [3]uint32{2, 2, 2} {
		t.Fatalf("descriptor = %+v", gotDescriptor)
	}
	gotDescriptor.Dimensions[0] = 99
	again, _ := snapshot.Descriptor()
	if again.Dimensions[0] != 2 {
		t.Fatal("descriptor mutation escaped into snapshot")
	}
	if value, ok := snapshot.ModalityAt(1, 0, 1); !ok || value != 5.5 {
		t.Fatalf("ModalityAt = %v/%v, want 5.5/true", value, ok)
	}
	copied, err := snapshot.CopyPayload()
	if err != nil {
		t.Fatalf("CopyPayload: %v", err)
	}
	copied[0] = 0
	if value, ok := snapshot.ModalityAt(0, 0, 0); !ok || value != 0.5 {
		t.Fatalf("copy mutated snapshot: %v/%v", value, ok)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if _, err := snapshot.Descriptor(); !errors.Is(err, ErrVolumeLeaseReleased) {
		t.Fatalf("Descriptor after release error = %v", err)
	}
}

func TestVolumeSnapshotV1RejectsMalformedContracts(t *testing.T) {
	valid := testVolumeDescriptor(2, 3, 4)
	tests := []struct {
		name   string
		mutate func(*VolumeDescriptor)
	}{
		{"version", func(d *VolumeDescriptor) { d.ContractVersion = 2 }},
		{"header", func(d *VolumeDescriptor) { d.HeaderSize-- }},
		{"generation", func(d *VolumeDescriptor) { d.VolumeGeneration = 0 }},
		{"dimension", func(d *VolumeDescriptor) { d.Dimensions[1] = 0 }},
		{"components", func(d *VolumeDescriptor) { d.Components = 2 }},
		{"scalar", func(d *VolumeDescriptor) { d.ScalarFormat = VolumeScalarUnknown }},
		{"domain", func(d *VolumeDescriptor) { d.SampleDomain = VolumeSampleDomainStored }},
		{"rescale", func(d *VolumeDescriptor) { d.RescaleSlope = 2 }},
		{"rescale infinity", func(d *VolumeDescriptor) { d.RescaleIntercept = math.Inf(1) }},
		{"row stride", func(d *VolumeDescriptor) { d.RowStrideBytes = 7 }},
		{"slice stride", func(d *VolumeDescriptor) { d.SliceStrideBytes-- }},
		{"slice multiplication overflow", func(d *VolumeDescriptor) {
			d.RowStrideBytes = math.MaxUint64
			d.SliceStrideBytes = math.MaxUint64
		}},
		{"final address overflow", func(d *VolumeDescriptor) {
			d.SliceStrideBytes = math.MaxUint64 / 2
			d.ByteLength = math.MaxUint64
		}},
		{"byte length", func(d *VolumeDescriptor) { d.ByteLength-- }},
		{"spacing zero", func(d *VolumeDescriptor) { d.SpacingMM[2] = 0 }},
		{"spacing nan", func(d *VolumeDescriptor) { d.SpacingMM[0] = math.NaN() }},
		{"spacing affine mismatch", func(d *VolumeDescriptor) { d.SpacingMM[0] = 1.01 }},
		{"affine nan", func(d *VolumeDescriptor) { d.IndexToPatientLPS[0] = math.NaN() }},
		{"final row", func(d *VolumeDescriptor) { d.IndexToPatientLPS[15] = 1 + 2e-12 }},
		{"singular", func(d *VolumeDescriptor) { d.PatientLPSToIndex[0] = 0 }},
		{"unknown derivation", func(d *VolumeDescriptor) { d.Derivation = VolumeDerivation(99) }},
		{"normalized parent", func(d *VolumeDescriptor) { d.ParentGeneration = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := valid
			test.mutate(&descriptor)
			if err := ValidateVolumeDescriptor(descriptor); !errors.Is(err, ErrInvalidVolumeSnapshot) {
				t.Fatalf("ValidateVolumeDescriptor error = %v", err)
			}
		})
	}
	withoutTrailingDerivation := valid
	withoutTrailingDerivation.Derivation = VolumeDerivationUnknown
	if err := ValidateVolumeDescriptor(withoutTrailingDerivation); err != nil {
		t.Fatalf("descriptor without optional derivation: %v", err)
	}

	stored := valid
	stored.ScalarFormat = VolumeScalarI16StoredLE
	stored.SampleDomain = VolumeSampleDomainStored
	stored.RescaleSlope = 2
	stored.RescaleIntercept = -1024
	stored.RowStrideBytes = 4
	stored.SliceStrideBytes = 12
	stored.ByteLength = 48
	if err := ValidateVolumeDescriptor(stored); err != nil {
		t.Fatalf("valid stored descriptor: %v", err)
	}

	store := NewVolumeStore()
	inputDescriptor := testVolumeDescriptor(2, 2, 1)
	if _, err := store.Replace(VolumeInput{Descriptor: inputDescriptor, Payload: make([]byte, inputDescriptor.ByteLength-1)}); !errors.Is(err, ErrInvalidVolumeSnapshot) {
		t.Fatalf("short payload Replace error = %v", err)
	}
}

func TestVolumeStoreHardBudgetAdmissionIsAtomic(t *testing.T) {
	oversized := NewVolumeStore(VolumeStoreOptions{MaxLiveBytes: 8})
	if _, err := oversized.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), make([]float32, 4)); !errors.Is(err, ErrVolumeBudgetExceeded) {
		t.Fatalf("oversized ReplaceFloat32 error = %v", err)
	}
	if stats := oversized.Stats(); stats.LiveBytes != 0 || stats.CurrentGeneration != 0 || stats.Replacements != 0 {
		t.Fatalf("oversized admission mutated store: %+v", stats)
	}

	store := NewVolumeStore(VolumeStoreOptions{MaxLiveBytes: 32})
	first, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), make([]float32, 4))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.Pin(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceFloat32(testVolumeDescriptor(3, 2, 1), make([]float32, 6)); !errors.Is(err, ErrVolumeBudgetExceeded) {
		t.Fatalf("Replace against pinned current error = %v", err)
	}
	if stats := store.Stats(); stats.CurrentGeneration != first || stats.LiveBytes != 16 || stats.Replacements != 1 {
		t.Fatalf("failed replacement changed current: %+v", stats)
	}
	_ = pin.Release()
	reader, err := store.Acquire(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceFloat32(testVolumeDescriptor(3, 2, 1), make([]float32, 6)); !errors.Is(err, ErrVolumeBudgetExceeded) {
		t.Fatalf("Replace against leased current error = %v", err)
	}
	if stats := store.Stats(); stats.CurrentGeneration != first || stats.LiveBytes != 16 || stats.Replacements != 1 {
		t.Fatalf("failed leased replacement changed current: %+v", stats)
	}
	_ = reader.Release()
	second, err := store.ReplaceFloat32(testVolumeDescriptor(3, 2, 1), make([]float32, 6))
	if err != nil {
		t.Fatalf("Replace after pin release: %v", err)
	}
	if stats := store.Stats(); stats.CurrentGeneration != second || stats.LiveBytes != 24 {
		t.Fatalf("replacement after reclaim = %+v", stats)
	}

	var callback atomic.Int32
	if _, err := store.TrackMemory(second, VolumeMemoryRGBA, 9, func() { callback.Add(1) }); !errors.Is(err, ErrVolumeBudgetExceeded) {
		t.Fatalf("TrackMemory over budget error = %v", err)
	}
	if callback.Load() != 0 {
		t.Fatal("rejected allocation invoked release callback")
	}
	tracked, err := store.TrackMemory(second, VolumeMemoryBackendCopy, 8, func() {
		_ = store.Stats() // Proves callbacks execute without the store mutex.
		callback.Add(1)
	})
	if err != nil {
		t.Fatalf("TrackMemory at budget: %v", err)
	}
	if stats := store.Stats(); stats.LiveBytes != 32 {
		t.Fatalf("stats at hard budget = %+v", stats)
	}
	done := make(chan struct{})
	go func() {
		_ = tracked.Release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release callback deadlocked on reentrant Stats")
	}
	if callback.Load() != 1 || store.Stats().LiveBytes != 24 {
		t.Fatalf("post-release callback=%d stats=%+v", callback.Load(), store.Stats())
	}
}

func TestStackUsesConfiguredVolumeStoreBudget(t *testing.T) {
	stack := gradientColumnStack(8, 8, 2)
	store := NewVolumeStore(VolumeStoreOptions{MaxLiveBytes: 8*8*2*4 - 1})
	if err := stack.SetVolumeStore(store); err != nil {
		t.Fatal(err)
	}
	if err := stack.SetVolumeStore(NewVolumeStore()); !errors.Is(err, ErrVolumeInUse) {
		t.Fatalf("second SetVolumeStore error = %v", err)
	}
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := volume.AcquireSnapshot(); !errors.Is(err, ErrVolumeBudgetExceeded) {
		t.Fatalf("AcquireSnapshot over configured budget error = %v", err)
	}
	if stats := stack.VolumeStoreStats(); stats.MaxLiveBytes != 8*8*2*4-1 ||
		stats.LiveBytes != 0 || stats.CurrentGeneration != 0 {
		t.Fatalf("configured store stats = %+v", stats)
	}
}

func TestVolumeStoreReplaceLeasePinEvictAndClose(t *testing.T) {
	store := NewVolumeStore()
	first, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), []float32{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("first ReplaceFloat32: %v", err)
	}
	lease, err := store.Acquire(first)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	pin, err := store.Pin(first)
	if err != nil {
		t.Fatalf("Pin first: %v", err)
	}
	second, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), []float32{5, 6, 7, 8})
	if err != nil {
		t.Fatalf("second ReplaceFloat32: %v", err)
	}
	if second <= first {
		t.Fatalf("generations first=%d second=%d", first, second)
	}
	if freed := store.Evict(math.MaxUint64, true); freed != 16 {
		t.Fatalf("Evict freed %d, want only unreferenced current 16", freed)
	}
	if _, err := store.Acquire(second); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("Acquire evicted second error = %v", err)
	}
	if snapshot, err := lease.Snapshot(); err != nil {
		t.Fatalf("leased retired snapshot: %v", err)
	} else if value, ok := snapshot.ModalityAt(1, 1, 0); !ok || value != 4 {
		t.Fatalf("leased retired value = %v/%v", value, ok)
	}
	stats := store.Stats()
	if stats.LiveBytes != 16 || stats.ActiveLeases != 1 || stats.ActivePins != 1 ||
		stats.LeasedBytes != 16 || stats.PinnedBytes != 16 {
		t.Fatalf("stats with lease+pin = %+v", stats)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if stats = store.Stats(); !stats.Closed || stats.LiveBytes != 16 {
		t.Fatalf("closed stats with references = %+v", stats)
	}
	if _, err := store.Acquire(first); !errors.Is(err, ErrVolumeStoreClosed) {
		t.Fatalf("Acquire after Close error = %v", err)
	}
	_ = lease.Release()
	if got := store.Stats().LiveBytes; got != 16 {
		t.Fatalf("bytes after lease release = %d, want pinned 16", got)
	}
	_ = pin.Release()
	if got := store.Stats().LiveBytes; got != 0 {
		t.Fatalf("bytes after final release = %d", got)
	}
}

func TestVolumeStoreRegularizationCreatesExplicitGeneration(t *testing.T) {
	store := NewVolumeStore()
	source, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), []float32{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	sourceLease, err := store.Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	regularized, err := store.ReplaceRegularizedFloat32(source, testVolumeDescriptor(2, 2, 2), []float32{1, 2, 3, 4, 1, 2, 3, 4})
	if err != nil {
		t.Fatalf("ReplaceRegularizedFloat32: %v", err)
	}
	current, err := store.AcquireCurrent()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := current.Snapshot()
	descriptor, _ := snapshot.Descriptor()
	if descriptor.VolumeGeneration != regularized || descriptor.ParentGeneration != source ||
		descriptor.Derivation != VolumeDerivationRegularized {
		t.Fatalf("regularized descriptor = %+v", descriptor)
	}
	stats := store.Stats()
	if stats.LiveBytes != 48 || stats.LiveGenerations != 2 {
		t.Fatalf("regularization stats = %+v", stats)
	}
	_ = sourceLease.Release()
	if stats = store.Stats(); stats.LiveBytes != 32 || stats.LiveGenerations != 1 {
		t.Fatalf("source release stats = %+v", stats)
	}
	_ = current.Release()
}

func TestVolumeStoreCountsOnlyExplicitEvictions(t *testing.T) {
	store := NewVolumeStore()
	if _, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), []float32{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 1), []float32{5, 6, 7, 8}); err != nil {
		t.Fatal(err)
	}
	if stats := store.Stats(); stats.Evictions != 0 || stats.EvictedBytes != 0 {
		t.Fatalf("replacement reclamation counted as eviction: %+v", stats)
	}
	if freed := store.Evict(1, true); freed != 16 {
		t.Fatalf("Evict() freed %d bytes, want 16", freed)
	}
	if stats := store.Stats(); stats.Evictions != 1 || stats.EvictedBytes != 16 {
		t.Fatalf("explicit eviction stats = %+v", stats)
	}
}

func TestVolumeStoreExactAccountingAllOwnershipCategories(t *testing.T) {
	store := NewVolumeStore()
	generation, err := store.ReplaceFloat32(testVolumeDescriptor(2, 2, 2), make([]float32, 8))
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	raw, err := store.TrackMemory(generation, VolumeMemoryRawFrames, 16, func() { callbacks.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	rgba, err := store.TrackMemory(generation, VolumeMemoryRGBA, 64, func() { callbacks.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.TrackMemory(generation, VolumeMemoryBackendCopy, 32, func() { callbacks.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	stats := store.Stats()
	if stats.LiveBytes != 32+16+64+32 || stats.ActiveLeases != 3 ||
		stats.LeasedBytes != 32+16+64+32 {
		t.Fatalf("accounting = %+v", stats)
	}
	want := map[VolumeMemoryKind]uint64{
		VolumeMemoryNormalized:  32,
		VolumeMemoryRawFrames:   16,
		VolumeMemoryRGBA:        64,
		VolumeMemoryBackendCopy: 32,
	}
	for _, category := range stats.ByKind {
		if expected, ok := want[category.Kind]; ok {
			if category.LiveBytes != expected {
				t.Fatalf("%s bytes = %d, want %d", category.Kind, category.LiveBytes, expected)
			}
			delete(want, category.Kind)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing categories: %v", want)
	}
	_ = raw.Release()
	_ = rgba.Release()
	_ = backend.Release()
	if callbacks.Load() != 3 {
		t.Fatalf("release callbacks = %d", callbacks.Load())
	}
	if got := store.Stats().LiveBytes; got != 32 {
		t.Fatalf("live bytes after tracked releases = %d", got)
	}
}

func TestVolumeStoreConcurrentAcquireReplaceEvictClose(t *testing.T) {
	const readers = 16
	store := NewVolumeStore()
	initial, err := store.ReplaceFloat32(testVolumeDescriptor(8, 8, 2), make([]float32, 128))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.Pin(initial)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	acquired := make(chan struct{})
	var acquiredOnce sync.Once
	var wg sync.WaitGroup
	for worker := 0; worker < readers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < 200; iteration++ {
				generation := store.CurrentGeneration()
				if generation == 0 {
					return
				}
				lease, err := store.Acquire(generation)
				if err != nil {
					if errors.Is(err, ErrVolumeStoreClosed) || errors.Is(err, ErrVolumeNotFound) {
						continue
					}
					t.Errorf("Acquire: %v", err)
					return
				}
				acquiredOnce.Do(func() { close(acquired) })
				snapshot, err := lease.Snapshot()
				if err == nil {
					_, _ = snapshot.ModalityAt(0, 0, 0)
				}
				_ = lease.Release()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for iteration := 0; iteration < 40; iteration++ {
			_, err := store.ReplaceFloat32(testVolumeDescriptor(8, 8, 2), make([]float32, 128))
			if err != nil && !errors.Is(err, ErrVolumeStoreClosed) {
				t.Errorf("Replace: %v", err)
				return
			}
			store.Evict(512)
		}
	}()
	close(start)
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("no worker acquired a lease before shutdown")
	}
	_ = store.Close()
	_ = pin.Release()
	wg.Wait()
	if stats := store.Stats(); stats.LiveBytes != 0 || stats.LiveGenerations != 0 || stats.TrackedRecords != 0 {
		t.Fatalf("final stats = %+v", stats)
	}
}

func TestVolumeStoreRepeatedOpenSwitchCloseHasNoWorkers(t *testing.T) {
	before := runtime.NumGoroutine()
	for cycle := 0; cycle < 200; cycle++ {
		store := NewVolumeStore()
		generation, err := store.ReplaceFloat32(testVolumeDescriptor(16, 16, 4), make([]float32, 1024))
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.Acquire(generation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReplaceFloat32(testVolumeDescriptor(16, 16, 4), make([]float32, 1024)); err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
		_ = lease.Release()
		if got := store.Stats().LiveBytes; got != 0 {
			t.Fatalf("cycle %d live bytes = %d", cycle, got)
		}
	}
	runtime.GC()
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
}

func TestVolumeLeaseStoreIdentityDistinguishesIndependentGenerationCounters(t *testing.T) {
	left := NewVolumeStore()
	right := NewVolumeStore()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	descriptor := testVolumeDescriptor(2, 2, 1)
	leftGeneration, err := left.ReplaceFloat32(
		descriptor, []float32{1, 2, 3, 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	rightGeneration, err := right.ReplaceFloat32(
		descriptor, []float32{5, 6, 7, 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	if leftGeneration != rightGeneration {
		t.Fatalf(
			"fixture generations differ: left=%d right=%d",
			leftGeneration, rightGeneration,
		)
	}
	leftLease, err := left.Acquire(leftGeneration)
	if err != nil {
		t.Fatal(err)
	}
	rightLease, err := right.Acquire(rightGeneration)
	if err != nil {
		t.Fatal(err)
	}
	leftIdentity := leftLease.StoreIdentity()
	rightIdentity := rightLease.StoreIdentity()
	if leftIdentity == (VolumeStoreIdentity{}) ||
		rightIdentity == (VolumeStoreIdentity{}) ||
		leftIdentity == rightIdentity {
		t.Fatalf(
			"store identities left=%v right=%v must be nonzero and distinct",
			leftIdentity, rightIdentity,
		)
	}
	leftKey := leftLease.ResidencyKey()
	rightKey := rightLease.ResidencyKey()
	if !leftKey.Valid() || !rightKey.Valid() ||
		leftKey.Generation != leftGeneration ||
		rightKey.Generation != rightGeneration ||
		leftKey == rightKey {
		t.Fatalf(
			"residency keys left=%+v right=%+v must be valid and distinct",
			leftKey, rightKey,
		)
	}
	leftSnapshot, err := leftLease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rightSnapshot, err := rightLease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if leftSnapshot.ResidencyKey() != leftKey ||
		rightSnapshot.ResidencyKey() != rightKey {
		t.Fatalf(
			"snapshot residency keys left=%+v right=%+v, want %+v and %+v",
			leftSnapshot.ResidencyKey(), rightSnapshot.ResidencyKey(),
			leftKey, rightKey,
		)
	}
	if err := leftLease.Release(); err != nil {
		t.Fatal(err)
	}
	if leftLease.StoreIdentity() != (VolumeStoreIdentity{}) {
		t.Fatal("released lease retained its store identity")
	}
	if leftLease.ResidencyKey().Valid() || leftSnapshot.ResidencyKey().Valid() {
		t.Fatal("released lease or snapshot retained its residency key")
	}
	if err := rightLease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeUsesOneCanonicalGenerationAcrossRenderers(t *testing.T) {
	stack := gradientColumnStack(12, 10, 4)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	again, err := stack.Volume()
	if err != nil || again != volume {
		t.Fatalf("Stack.Volume reuse = %p/%v, want %p", again, err, volume)
	}
	first, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	second, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation() == 0 || second.Generation() != first.Generation() {
		t.Fatalf("snapshot generations first=%d second=%d", first.Generation(), second.Generation())
	}
	_ = ResliceOblique(volume, volume.OrthogonalPlane(MPRPlaneAxial, volume.VoxelToPatient(Vec3{X: 5, Y: 5, Z: 2})), 12, 10, WindowLevel{Center: 6, Width: 12})
	preset, _ := VRPresetByName(PresetBonesBW)
	_ = RenderVR(volume, NewVRCamera(volume.BoundingRadiusMM()), preset, WindowLevel{Center: 6, Width: 12}, false, nil, DefaultVRQuality(8, 8))
	stats := volume.VolumeStoreStats()
	if stats.LiveGenerations != 1 || stats.Replacements != 1 ||
		stats.LiveBytes != uint64(volume.Cols*volume.Rows*volume.Depth*4) {
		t.Fatalf("canonical stats after MPR/VR = %+v", stats)
	}
	_ = first.Release()
	_ = second.Release()
}

func TestVolumeRegularizationPublishesExplicitGeneration(t *testing.T) {
	stack := gradientXZStack(4, 4, 3)
	stack.Frames[0].ImagePosition = []float64{0, 0, 0}
	stack.Frames[1].ImagePosition = []float64{0, 0, 1}
	stack.Frames[2].ImagePosition = []float64{0, 0, 3}
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := lease.Snapshot()
	descriptor, _ := snapshot.Descriptor()
	if descriptor.Derivation != VolumeDerivationRegularized || descriptor.ParentGeneration == 0 ||
		descriptor.VolumeGeneration <= descriptor.ParentGeneration {
		t.Fatalf("regularized descriptor = %+v", descriptor)
	}
	stats := volume.VolumeStoreStats()
	if stats.CurrentGeneration != descriptor.VolumeGeneration || stats.LiveGenerations != 1 {
		t.Fatalf("regularized stats = %+v", stats)
	}
	geometry := volume.Geometry()
	if !geometry.RequiresResampling || geometry.Regular {
		t.Fatalf("source geometry was silently mutated: %+v", geometry)
	}
	_ = lease.Release()
}

func TestVolumeCloseDefersReleaseUntilRendererLeaseEnds(t *testing.T) {
	stack := gradientColumnStack(8, 8, 4)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := lease.Snapshot()
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Volume(); !errors.Is(err, ErrVolumeStoreClosed) {
		t.Fatalf("Stack.Volume after Close error = %v", err)
	}
	if value, ok := snapshot.ModalityAt(3, 2, 1); !ok || value != 3 {
		t.Fatalf("leased value after close = %v/%v", value, ok)
	}
	if stats := volume.VolumeStoreStats(); !stats.Closed || stats.LiveBytes == 0 {
		t.Fatalf("closed store released active lease: %+v", stats)
	}
	_ = lease.Release()
	if stats := volume.VolumeStoreStats(); stats.LiveBytes != 0 || stats.LiveGenerations != 0 {
		t.Fatalf("stats after final renderer release = %+v", stats)
	}
}

func TestVolumeReaderKeepsOneLeaseAcrossClose(t *testing.T) {
	stack := gradientColumnStack(8, 8, 4)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := volume.AcquireReader()
	if err != nil {
		t.Fatal(err)
	}
	if stats := volume.VolumeStoreStats(); stats.ActiveLeases != 1 ||
		stats.LeasedBytes != stats.LiveBytes || stats.LeasedBytes == 0 {
		t.Fatalf("stats after AcquireReader = %+v, want one active lease over all live bytes", stats)
	}
	patient := volume.VoxelToPatient(Vec3{X: 3, Y: 2, Z: 1})
	if value, ok := reader.SamplePatient(patient); !ok || value != 3 {
		t.Fatalf("reader value before close = %v/%v", value, ok)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	if value, ok := reader.SamplePatient(patient); !ok || value != 3 {
		t.Fatalf("reader value after close = %v/%v", value, ok)
	}
	if stats := volume.VolumeStoreStats(); !stats.Closed || stats.LiveBytes == 0 {
		t.Fatalf("reader lease did not retain closed generation: %+v", stats)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second reader Close = %v", err)
	}
	if stats := volume.VolumeStoreStats(); stats.LiveBytes != 0 || stats.LiveGenerations != 0 {
		t.Fatalf("stats after reader release = %+v", stats)
	}
}

func TestVolumeReaderSamplingDoesNotReenterStore(t *testing.T) {
	stack := gradientColumnStack(16, 16, 8)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := volume.AcquireReader()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	type sampleResult struct {
		value float64
		ok    bool
	}
	done := make(chan sampleResult, 1)
	volume.store.mu.Lock()
	go func() {
		var result sampleResult
		for iteration := 0; iteration < 10_000; iteration++ {
			result.value, result.ok = reader.SamplePatient(volume.VoxelToPatient(Vec3{X: 3, Y: 2, Z: 1}))
		}
		done <- result
	}()
	select {
	case result := <-done:
		volume.store.mu.Unlock()
		if !result.ok || result.value != 3 {
			t.Fatalf("reader sample under store contention = %v/%v", result.value, result.ok)
		}
	case <-time.After(time.Second):
		volume.store.mu.Unlock()
		<-done
		t.Fatal("reader sampling reentered the VolumeStore mutex")
	}
}

func TestVolumeConcurrentAcquireAndClose(t *testing.T) {
	stack := gradientColumnStack(16, 16, 8)
	volume, err := stack.Volume()
	if err != nil {
		t.Fatal(err)
	}
	initial, err := volume.AcquireSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	_ = initial.Release()

	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < 100; iteration++ {
				lease, acquireErr := volume.AcquireSnapshot()
				if acquireErr != nil {
					if errors.Is(acquireErr, ErrVolumeStoreClosed) || errors.Is(acquireErr, ErrVolumeNotFound) {
						return
					}
					t.Errorf("AcquireSnapshot: %v", acquireErr)
					return
				}
				snapshot, snapshotErr := lease.Snapshot()
				if snapshotErr == nil {
					_, _ = snapshot.ModalityAt(2, 2, 2)
				}
				_ = lease.Release()
			}
		}()
	}
	close(start)
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if stats := volume.VolumeStoreStats(); stats.LiveBytes != 0 || stats.LiveGenerations != 0 {
		t.Fatalf("concurrent close stats = %+v", stats)
	}
}

func BenchmarkVolumeReaderTrilinearAt(b *testing.B) {
	stack := gradientColumnStack(32, 32, 16)
	volume, err := stack.Volume()
	if err != nil {
		b.Fatal(err)
	}
	reader, err := volume.AcquireReader()
	if err != nil {
		b.Fatal(err)
	}
	defer reader.Close()
	point := Vec3{X: 12.25, Y: 13.5, Z: 7.75}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		_, _ = reader.TrilinearAt(point)
	}
}

func testVolumeDescriptor(cols, rows, depth uint32) VolumeDescriptor {
	row := uint64(cols) * 4
	slice := uint64(rows) * row
	identity := GeometryAffine{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
	return VolumeDescriptor{
		ContractVersion:   VolumeSnapshotContractVersion,
		HeaderSize:        VolumeSnapshotHeaderSizeV1,
		VolumeGeneration:  1,
		Derivation:        VolumeDerivationNormalized,
		Dimensions:        [3]uint32{cols, rows, depth},
		Components:        1,
		ScalarFormat:      VolumeScalarF32ModalityLE,
		SampleDomain:      VolumeSampleDomainModality,
		RowStrideBytes:    row,
		SliceStrideBytes:  slice,
		ByteLength:        uint64(depth) * slice,
		RescaleSlope:      1,
		SpacingMM:         [3]float64{1, 1, 1},
		IndexToPatientLPS: identity,
		PatientLPSToIndex: identity,
	}
}
