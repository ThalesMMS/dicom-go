package render

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

const (
	// DefaultVRHistogramBins is the canonical clinical histogram resolution.
	DefaultVRHistogramBins         = 256
	defaultVRHistogramCacheEntries = 8
	maximumVRHistogramBins         = 65536
)

// VRHistogramDescriptor identifies the prepared scalar domain represented by
// a histogram. DomainMin/DomainMax are the exact observed modality values;
// they remain equal for a constant-valued volume.
type VRHistogramDescriptor struct {
	Bins      int
	DomainMin float64
	DomainMax float64
	Prefilter VRPrefilterID
}

// VRHistogramKey is PHI-free and contains no volume pointer. PreparedIdentity
// is the same immutable generation identity consumed by CPU and WebGPU VR.
type VRHistogramKey struct {
	PreparedIdentity VolumeResidencyKey
	Prefilter        VRPrefilterID
	Bins             int
	DomainMin        float64
	DomainMax        float64
}

func (key VRHistogramKey) Valid() bool {
	return key.PreparedIdentity.Valid() && key.Bins > 0 &&
		finite(key.DomainMin) && finite(key.DomainMax) && key.DomainMax >= key.DomainMin
}

// VRHistogram is immutable by convention after publication. Clone returns
// detached Counts for callers that cross an ownership boundary.
type VRHistogram struct {
	Descriptor VRHistogramDescriptor
	Counts     []uint64
	VoxelCount uint64
	PeakBin    int
	Generation uint64
	Key        VRHistogramKey
}

func (histogram VRHistogram) Clone() VRHistogram {
	histogram.Counts = append([]uint64(nil), histogram.Counts...)
	return histogram
}

// VRHistogramCacheOptions bounds completed entries. In-flight entries are
// never evicted; zero selects the small clinical default.
type VRHistogramCacheOptions struct {
	MaxEntries int
}

type VRHistogramCacheStats struct {
	Hits      uint64
	Misses    uint64
	Builds    uint64
	Evictions uint64
	Entries   int
}

type vrHistogramCacheEntry struct {
	ready     chan struct{}
	histogram VRHistogram
	err       error
	lastUsed  uint64
}

// VRHistogramCache deduplicates exact prepared-volume scans. Completed
// entries retain only the value key and 256-bin result, never the volume.
type VRHistogramCache struct {
	mu         sync.Mutex
	maxEntries int
	clock      uint64
	entries    map[VRHistogramKey]*vrHistogramCacheEntry
	hits       uint64
	misses     uint64
	builds     uint64
	evictions  uint64

	compute func(context.Context, *VRPreparedVolume, VRHistogramDescriptor) (VRHistogram, error)
}

func NewVRHistogramCache(options ...VRHistogramCacheOptions) *VRHistogramCache {
	maxEntries := defaultVRHistogramCacheEntries
	if len(options) > 0 && options[0].MaxEntries > 0 {
		maxEntries = options[0].MaxEntries
	}
	return &VRHistogramCache{
		maxEntries: maxEntries,
		entries:    make(map[VRHistogramKey]*vrHistogramCacheEntry),
	}
}

// Histogram returns the cached or newly calculated histogram.
func (cache *VRHistogramCache) Histogram(prepared *VRPreparedVolume, bins int) (VRHistogram, error) {
	return cache.HistogramContext(context.Background(), prepared, bins)
}

// HistogramContext scans outside the caller/UI lock, deduplicates concurrent
// requests, and never publishes a canceled or incomplete result.
func (cache *VRHistogramCache) HistogramContext(ctx context.Context, prepared *VRPreparedVolume, bins int) (VRHistogram, error) {
	if cache == nil {
		return VRHistogram{}, fmt.Errorf("render: nil VR histogram cache")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return VRHistogram{}, err
	}
	descriptor, key, err := prepared.vrHistogramDescriptorContext(ctx, bins)
	if err != nil {
		return VRHistogram{}, err
	}

	for {
		cache.mu.Lock()
		cache.clock++
		if entry := cache.entries[key]; entry != nil {
			entry.lastUsed = cache.clock
			cache.hits++
			ready := entry.ready
			cache.mu.Unlock()
			select {
			case <-ready:
				if entry.err != nil {
					if ctx.Err() == nil && (errors.Is(entry.err, context.Canceled) || errors.Is(entry.err, context.DeadlineExceeded)) {
						continue
					}
					return VRHistogram{}, entry.err
				}
				return entry.histogram.Clone(), nil
			case <-ctx.Done():
				return VRHistogram{}, ctx.Err()
			}
		}
		entry := &vrHistogramCacheEntry{ready: make(chan struct{}), lastUsed: cache.clock}
		cache.entries[key] = entry
		cache.misses++
		cache.builds++
		compute := cache.compute
		cache.mu.Unlock()

		if compute == nil {
			compute = computeVRHistogramContext
		}
		histogram, buildErr := compute(ctx, prepared, descriptor)
		if buildErr == nil {
			histogram.Descriptor = descriptor
			histogram.Key = key
			histogram.Generation = key.PreparedIdentity.Generation
			buildErr = validateVRHistogram(histogram)
		}
		if buildErr == nil {
			buildErr = ctx.Err()
		}

		cache.mu.Lock()
		entry.histogram = histogram.Clone()
		entry.err = buildErr
		close(entry.ready)
		if buildErr != nil {
			delete(cache.entries, key)
		} else {
			cache.evictLocked(key)
		}
		cache.mu.Unlock()
		if buildErr != nil {
			return VRHistogram{}, buildErr
		}
		return histogram.Clone(), nil
	}
}

func (cache *VRHistogramCache) Stats() VRHistogramCacheStats {
	if cache == nil {
		return VRHistogramCacheStats{}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return VRHistogramCacheStats{
		Hits: cache.hits, Misses: cache.misses, Builds: cache.builds,
		Evictions: cache.evictions, Entries: len(cache.entries),
	}
}

func (cache *VRHistogramCache) Clear() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	for key, entry := range cache.entries {
		select {
		case <-entry.ready:
			delete(cache.entries, key)
		default:
		}
	}
	cache.mu.Unlock()
}

func (cache *VRHistogramCache) evictLocked(keep VRHistogramKey) {
	for len(cache.entries) > cache.maxEntries {
		var oldestKey VRHistogramKey
		oldestClock := ^uint64(0)
		found := false
		for key, entry := range cache.entries {
			if key == keep {
				continue
			}
			select {
			case <-entry.ready:
				if entry.lastUsed < oldestClock {
					oldestKey, oldestClock, found = key, entry.lastUsed, true
				}
			default:
			}
		}
		if !found {
			return
		}
		delete(cache.entries, oldestKey)
		cache.evictions++
	}
}

type vrPreparedIntensityRange struct {
	ready            chan struct{}
	minimum, maximum float64
	err              error
}

func (prepared *VRPreparedVolume) vrHistogramDescriptorContext(ctx context.Context, bins int) (VRHistogramDescriptor, VRHistogramKey, error) {
	if prepared == nil || prepared.Volume == nil || prepared.Generation == 0 {
		return VRHistogramDescriptor{}, VRHistogramKey{}, ErrInvalidVolumeSnapshot
	}
	if bins <= 0 || bins > maximumVRHistogramBins {
		return VRHistogramDescriptor{}, VRHistogramKey{}, fmt.Errorf("render: invalid VR histogram bin count %d", bins)
	}
	minimum, maximum, err := prepared.intensityRangeContext(ctx)
	if err != nil {
		return VRHistogramDescriptor{}, VRHistogramKey{}, err
	}
	descriptor := VRHistogramDescriptor{Bins: bins, DomainMin: minimum, DomainMax: maximum, Prefilter: prepared.Prefilter}
	key := VRHistogramKey{
		PreparedIdentity: prepared.ResidencyKey(), Prefilter: prepared.Prefilter,
		Bins: bins, DomainMin: minimum, DomainMax: maximum,
	}
	if !key.Valid() {
		return VRHistogramDescriptor{}, VRHistogramKey{}, ErrInvalidVolumeSnapshot
	}
	return descriptor, key, nil
}

func (prepared *VRPreparedVolume) intensityRangeContext(ctx context.Context) (float64, float64, error) {
	if prepared == nil {
		return 0, 0, ErrInvalidVolumeSnapshot
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		prepared.histogramRangeMu.Lock()
		if entry := prepared.histogramRange; entry != nil {
			ready := entry.ready
			prepared.histogramRangeMu.Unlock()
			select {
			case <-ready:
				if entry.err != nil {
					if ctx.Err() == nil && (errors.Is(entry.err, context.Canceled) || errors.Is(entry.err, context.DeadlineExceeded)) {
						continue
					}
					return 0, 0, entry.err
				}
				return entry.minimum, entry.maximum, nil
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			}
		}
		entry := &vrPreparedIntensityRange{ready: make(chan struct{})}
		prepared.histogramRange = entry
		prepared.histogramRangeMu.Unlock()

		minimum, maximum := math.Inf(1), math.Inf(-1)
		var count uint64
		err := prepared.visitVoxelsContext(ctx, func(value float64) error {
			minimum = math.Min(minimum, value)
			maximum = math.Max(maximum, value)
			if count == math.MaxUint64 {
				return fmt.Errorf("render: VR histogram voxel count overflow")
			}
			count++
			return nil
		})
		if err == nil && count == 0 {
			err = ErrInvalidVolumeSnapshot
		}
		prepared.histogramRangeMu.Lock()
		entry.minimum, entry.maximum, entry.err = minimum, maximum, err
		close(entry.ready)
		if err != nil {
			prepared.histogramRange = nil
		}
		prepared.histogramRangeMu.Unlock()
		return minimum, maximum, err
	}
}

func computeVRHistogramContext(ctx context.Context, prepared *VRPreparedVolume, descriptor VRHistogramDescriptor) (VRHistogram, error) {
	counts := make([]uint64, descriptor.Bins)
	var voxelCount uint64
	err := prepared.visitVoxelsContext(ctx, func(value float64) error {
		index := 0
		if descriptor.DomainMax > descriptor.DomainMin {
			u := (value - descriptor.DomainMin) / (descriptor.DomainMax - descriptor.DomainMin)
			switch {
			case u <= 0:
				index = 0
			case u >= 1:
				index = descriptor.Bins - 1
			default:
				index = min(int(math.Floor(u*float64(descriptor.Bins))), descriptor.Bins-1)
			}
		}
		if counts[index] == math.MaxUint64 || voxelCount == math.MaxUint64 {
			return fmt.Errorf("render: VR histogram count overflow")
		}
		counts[index]++
		voxelCount++
		return nil
	})
	if err != nil {
		return VRHistogram{}, err
	}
	expected, ok := checkedVoxelCount(prepared.Volume.Cols, prepared.Volume.Rows, prepared.Volume.Depth)
	if !ok || voxelCount != uint64(expected) {
		return VRHistogram{}, fmt.Errorf("%w: VR histogram voxel count mismatch", ErrInvalidVolumeSnapshot)
	}
	peakBin := 0
	for index := 1; index < len(counts); index++ {
		if counts[index] > counts[peakBin] {
			peakBin = index
		}
	}
	return VRHistogram{Descriptor: descriptor, Counts: counts, VoxelCount: voxelCount, PeakBin: peakBin}, nil
}

func (prepared *VRPreparedVolume) visitVoxelsContext(ctx context.Context, visit func(float64) error) error {
	if prepared == nil || visit == nil {
		return ErrInvalidVolumeSnapshot
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sampler, err := prepared.newSampler()
	if err != nil {
		return err
	}
	defer sampler.Close()
	for z := 0; z < sampler.depth; z++ {
		for y := 0; y < sampler.rows; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := 0; x < sampler.cols; x++ {
				value, ok := sampler.valueAt(x, y, z)
				if !ok || !finite(value) {
					return fmt.Errorf("%w: invalid prepared voxel", ErrInvalidVolumeSnapshot)
				}
				if err := visit(value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateVRHistogram(histogram VRHistogram) error {
	if !histogram.Key.Valid() || histogram.Descriptor.Bins != len(histogram.Counts) ||
		histogram.Descriptor.Bins != histogram.Key.Bins || histogram.Generation == 0 ||
		histogram.PeakBin < 0 || histogram.PeakBin >= len(histogram.Counts) {
		return ErrInvalidVolumeSnapshot
	}
	var sum uint64
	for _, count := range histogram.Counts {
		if math.MaxUint64-sum < count {
			return fmt.Errorf("render: VR histogram total overflow")
		}
		sum += count
	}
	if sum != histogram.VoxelCount {
		return fmt.Errorf("%w: VR histogram count mismatch", ErrInvalidVolumeSnapshot)
	}
	return nil
}
