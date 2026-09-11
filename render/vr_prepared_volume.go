package render

import (
	"context"
	"fmt"
	"math"
	"sync"
)

// VRPreparedVolume freezes the effective voxel source selected by one VR
// presentation. Volume supplies immutable patient geometry; AcquireSnapshot
// supplies either its original canonical generation or a render-only filtered
// generation. The source and prepared identities are process-local and PHI-free.
type VRPreparedVolume struct {
	SourceIdentity VolumeResidencyKey
	Prefilter      VRPrefilterID
	Volume         *Volume
	Generation     uint64

	descriptor VolumeDescriptor
	store      *VolumeStore

	huMu      sync.Mutex
	huMin     float64
	huMax     float64
	huRangeOK bool

	histogramRangeMu sync.Mutex
	histogramRange   *vrPreparedIntensityRange
}

type vrPreparedCacheKey struct {
	source      VolumeResidencyKey
	prefilter   VRPrefilterID
	dimensions  [3]uint32
	scalar      VolumeScalarFormat
	domain      VolumeSampleDomain
	rowStride   uint64
	sliceStride uint64
}

type vrPreparedCacheEntry struct {
	ready  chan struct{}
	volume *VRPreparedVolume
	err    error
}

// PrepareVRVolume selects or builds the immutable render-only volume for a
// presentation. Repeated calls for the same source identity, pixel format and
// prefilter return the same cached object.
func PrepareVRVolume(volume *Volume, prefilter VRPrefilterID) (*VRPreparedVolume, error) {
	return PrepareVRVolumeContext(context.Background(), volume, prefilter)
}

// PrepareVRVolumeContext is the cancellable form of PrepareVRVolume. A canceled
// build is never published to the cache; a later presentation may retry it.
func PrepareVRVolumeContext(ctx context.Context, volume *Volume, prefilter VRPrefilterID) (*VRPreparedVolume, error) {
	if volume == nil {
		return nil, fmt.Errorf("render: nil VR volume")
	}
	return volume.PrepareVRVolumeContext(ctx, prefilter)
}

// PrepareVRVolume selects or builds a prepared volume from this source.
func (v *Volume) PrepareVRVolume(prefilter VRPrefilterID) (*VRPreparedVolume, error) {
	return v.PrepareVRVolumeContext(context.Background(), prefilter)
}

// PrepareVRVolumeContext selects or builds a prepared volume from this source.
func (v *Volume) PrepareVRVolumeContext(ctx context.Context, prefilter VRPrefilterID) (*VRPreparedVolume, error) {
	if v == nil {
		return nil, fmt.Errorf("render: nil VR volume")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prefilter != VRPrefilterNone && prefilter != VRPrefilterBasicSmooth5x5 {
		return nil, fmt.Errorf("render: unsupported VR prefilter %d", prefilter)
	}
	grid, err := v.RegularGrid()
	if err != nil {
		return nil, err
	}
	if grid.closed.Load() {
		return nil, ErrVolumeStoreClosed
	}
	sourceLease, err := grid.AcquireSnapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	sourceSnapshot, err := sourceLease.Snapshot()
	if err != nil {
		_ = sourceLease.Release()
		return nil, err
	}
	descriptor, err := sourceSnapshot.Descriptor()
	if err != nil {
		_ = sourceLease.Release()
		return nil, err
	}
	sourceIdentity := sourceLease.ResidencyKey()
	key := vrPreparedCacheKey{
		source: sourceIdentity, prefilter: prefilter,
		dimensions: descriptor.Dimensions, scalar: descriptor.ScalarFormat,
		domain: descriptor.SampleDomain, rowStride: descriptor.RowStrideBytes,
		sliceStride: descriptor.SliceStrideBytes,
	}

	grid.vrPreparedMu.Lock()
	if grid.vrPreparedClosed {
		grid.vrPreparedMu.Unlock()
		_ = sourceLease.Release()
		return nil, ErrVolumeStoreClosed
	}
	if entry := grid.vrPrepared[key]; entry != nil {
		ready := entry.ready
		grid.vrPreparedMu.Unlock()
		_ = sourceLease.Release()
		select {
		case <-ready:
			if entry.err != nil {
				if ctx.Err() == nil &&
					(entry.err == context.Canceled || entry.err == context.DeadlineExceeded) {
					return grid.PrepareVRVolumeContext(ctx, prefilter)
				}
				return nil, entry.err
			}
			return entry.volume, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if grid.vrPrepared == nil {
		grid.vrPrepared = make(map[vrPreparedCacheKey]*vrPreparedCacheEntry)
	}
	entry := &vrPreparedCacheEntry{ready: make(chan struct{})}
	grid.vrPrepared[key] = entry
	grid.vrPreparedMu.Unlock()

	prepared, buildErr := buildVRPreparedVolume(ctx, grid, sourceLease, descriptor, sourceIdentity, prefilter)
	_ = sourceLease.Release()
	if buildErr == nil {
		buildErr = ctx.Err()
	}

	grid.vrPreparedMu.Lock()
	if grid.vrPreparedClosed && buildErr == nil {
		buildErr = ErrVolumeStoreClosed
	}
	entry.volume = prepared
	entry.err = buildErr
	close(entry.ready)
	if buildErr != nil && (ctx.Err() != nil || grid.vrPreparedClosed) {
		delete(grid.vrPrepared, key)
	}
	grid.vrPreparedMu.Unlock()
	if buildErr != nil {
		if prepared != nil && prepared.store != nil {
			_ = prepared.store.Close()
		}
		return nil, buildErr
	}
	return prepared, nil
}

func buildVRPreparedVolume(
	ctx context.Context,
	volume *Volume,
	sourceLease *VolumeLease,
	descriptor VolumeDescriptor,
	sourceIdentity VolumeResidencyKey,
	prefilter VRPrefilterID,
) (*VRPreparedVolume, error) {
	if prefilter == VRPrefilterNone {
		return &VRPreparedVolume{
			SourceIdentity: sourceIdentity,
			Prefilter:      prefilter,
			Volume:         volume,
			Generation:     descriptor.VolumeGeneration,
			descriptor:     descriptor,
		}, nil
	}
	sampler, err := newVolumeSamplerFromLease(volume, sourceLease)
	if err != nil {
		return nil, err
	}
	// The caller retains an idempotent release too; the sampler owns the lease
	// for the duration of the convolution so no source generation can retire.
	defer sampler.Close()
	values, err := basicSmooth5x5Context(ctx, sampler)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	preparedStore := NewVolumeStore()
	preparedDescriptor := descriptor
	preparedDescriptor.VolumeGeneration = 0
	preparedDescriptor.ParentGeneration = 0
	generation, err := preparedStore.replaceVRPrefilteredFloat32Owned(preparedDescriptor, values)
	if err != nil {
		_ = preparedStore.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = preparedStore.Close()
		return nil, err
	}
	preparedDescriptor.VolumeGeneration = generation
	preparedDescriptor.Derivation = VolumeDerivationVRPrefiltered
	preparedDescriptor.ScalarFormat = VolumeScalarF32ModalityLE
	preparedDescriptor.SampleDomain = VolumeSampleDomainModality
	preparedDescriptor.RescaleSlope = 1
	preparedDescriptor.RescaleIntercept = 0
	return &VRPreparedVolume{
		SourceIdentity: sourceIdentity,
		Prefilter:      prefilter,
		Volume:         volume,
		Generation:     generation,
		descriptor:     preparedDescriptor,
		store:          preparedStore,
	}, nil
}

var basicSmooth5x5Kernel = [5][5]float64{
	{1, 1, 1, 1, 1},
	{1, 4, 4, 4, 1},
	{1, 4, 12, 4, 1},
	{1, 4, 4, 4, 1},
	{1, 1, 1, 1, 1},
}

func basicSmooth5x5Context(ctx context.Context, sampler *volumeSampler) ([]float32, error) {
	if sampler == nil || sampler.cols <= 0 || sampler.rows <= 0 || sampler.depth <= 0 {
		return nil, ErrInvalidVolumeSnapshot
	}
	voxelCount, ok := checkedVoxelCount(sampler.cols, sampler.rows, sampler.depth)
	if !ok {
		return nil, fmt.Errorf("%w: prepared volume is too large", ErrInvalidVolumeSnapshot)
	}
	values := make([]float32, voxelCount)
	for z := 0; z < sampler.depth; z++ {
		for y := 0; y < sampler.rows; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := 0; x < sampler.cols; x++ {
				sum := 0.0
				for ky := -2; ky <= 2; ky++ {
					sy := min(max(y+ky, 0), sampler.rows-1)
					for kx := -2; kx <= 2; kx++ {
						sx := min(max(x+kx, 0), sampler.cols-1)
						value, ok := sampler.valueAt(sx, sy, z)
						if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
							return nil, fmt.Errorf("%w: invalid source voxel", ErrInvalidVolumeSnapshot)
						}
						sum += value * basicSmooth5x5Kernel[ky+2][kx+2]
					}
				}
				values[(z*sampler.rows+y)*sampler.cols+x] = float32(sum / 60)
			}
		}
	}
	return values, nil
}

// AcquireSnapshot returns a lease on the exact generation selected by this
// prepared volume.
func (p *VRPreparedVolume) AcquireSnapshot() (*VolumeLease, error) {
	return p.AcquireSnapshotContext(context.Background())
}

// AcquireSnapshotContext returns a lease on the exact selected generation.
func (p *VRPreparedVolume) AcquireSnapshotContext(ctx context.Context) (*VolumeLease, error) {
	if p == nil || p.Volume == nil || p.Generation == 0 {
		return nil, ErrInvalidVolumeSnapshot
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.store == nil {
		lease, err := p.Volume.AcquireSnapshotContext(ctx)
		if err != nil {
			return nil, err
		}
		if lease.ResidencyKey() != p.SourceIdentity {
			_ = lease.Release()
			return nil, fmt.Errorf("%w: prepared source identity changed", ErrInvalidVolumeSnapshot)
		}
		return lease, nil
	}
	return p.store.Acquire(p.Generation)
}

// ResidencyKey identifies the selected source for backend cache residency.
func (p *VRPreparedVolume) ResidencyKey() VolumeResidencyKey {
	if p == nil || p.Generation == 0 {
		return VolumeResidencyKey{}
	}
	if p.store == nil {
		return p.SourceIdentity
	}
	return VolumeResidencyKey{StoreIdentity: p.store.identity, Generation: p.Generation}
}

// Descriptor returns the immutable value-only descriptor for the selected
// generation.
func (p *VRPreparedVolume) Descriptor() VolumeDescriptor {
	if p == nil {
		return VolumeDescriptor{}
	}
	return p.descriptor
}

func (p *VRPreparedVolume) newSampler() (*volumeSampler, error) {
	lease, err := p.AcquireSnapshot()
	if err != nil {
		return nil, err
	}
	return newVolumeSamplerFromLease(p.Volume, lease)
}

func (p *VRPreparedVolume) huRange() (minimum, maximum float64, ok bool) {
	if p == nil || p.Volume == nil {
		return 0, 0, false
	}
	if p.Prefilter == VRPrefilterNone {
		return p.Volume.HURange()
	}
	p.huMu.Lock()
	defer p.huMu.Unlock()
	if p.huRangeOK {
		return p.huMin, p.huMax, true
	}
	sampler, err := p.newSampler()
	if err != nil {
		return 0, 0, false
	}
	defer sampler.Close()
	minimum, maximum = math.Inf(1), math.Inf(-1)
	found := false
	for z := 0; z < sampler.depth; z++ {
		for y := 0; y < sampler.rows; y++ {
			for x := 0; x < sampler.cols; x++ {
				value, valid := sampler.valueAt(x, y, z)
				if !valid || !finite(value) {
					continue
				}
				minimum = math.Min(minimum, value)
				maximum = math.Max(maximum, value)
				found = true
			}
		}
	}
	if !found {
		return 0, 0, false
	}
	if maximum <= minimum {
		minimum--
		maximum++
	}
	p.huMin, p.huMax, p.huRangeOK = minimum, maximum, true
	return minimum, maximum, true
}

func (v *Volume) closeVRPreparedVolumes() error {
	if v == nil {
		return nil
	}
	v.vrPreparedMu.Lock()
	v.vrPreparedClosed = true
	stores := make([]*VolumeStore, 0, len(v.vrPrepared))
	for _, entry := range v.vrPrepared {
		if entry != nil && entry.volume != nil && entry.volume.store != nil {
			stores = append(stores, entry.volume.store)
		}
	}
	v.vrPrepared = nil
	v.vrPreparedMu.Unlock()
	var first error
	for _, store := range stores {
		if err := store.Close(); first == nil {
			first = err
		}
	}
	return first
}
