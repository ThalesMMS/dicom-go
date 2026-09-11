package render

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestVRHistogramBinningUsesExactModalityDomainAndUpperBin(t *testing.T) {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	stack.Frames = []*Frame{volumeTestFrame(2, 2, []byte{0, 1, 2, 3}, 0)}
	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = volume.Close() })
	prepared, err := volume.PrepareVRVolume(VRPrefilterNone)
	if err != nil {
		t.Fatal(err)
	}
	histogram, err := NewVRHistogramCache().Histogram(prepared, 3)
	if err != nil {
		t.Fatal(err)
	}
	if histogram.Descriptor.DomainMin != 0 || histogram.Descriptor.DomainMax != 3 ||
		!reflect.DeepEqual(histogram.Counts, []uint64{1, 1, 2}) ||
		histogram.VoxelCount != 4 || histogram.PeakBin != 2 {
		t.Fatalf("histogram = %#v", histogram)
	}
	if histogram.Key.PreparedIdentity != prepared.ResidencyKey() ||
		histogram.Generation != prepared.ResidencyKey().Generation {
		t.Fatalf("histogram identity = %+v/%d, prepared %+v", histogram.Key, histogram.Generation, prepared.ResidencyKey())
	}
}

func TestVRHistogramPreservesSignedUnsignedAndRescaleSemantics(t *testing.T) {
	tests := []struct {
		name             string
		values           []int16
		signed           bool
		slope, offset    float64
		minimum, maximum float64
	}{
		{name: "signed", values: []int16{-2, -1, 0, 1}, signed: true, slope: 2, offset: 10, minimum: 6, maximum: 12},
		{name: "unsigned", values: []int16{0, 1, 2, 3}, slope: 3, offset: -4, minimum: -4, maximum: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := make([]byte, len(test.values)*2)
			for index, value := range test.values {
				binary.LittleEndian.PutUint16(data[index*2:], uint16(value))
			}
			frame := &Frame{
				Metadata: pixeldata.Metadata{
					Rows: 2, Columns: 2, SamplesPerPixel: 1,
					BitsAllocated: 16, BitsStored: 16, HighBit: 15,
					PhotometricInterpretation: "MONOCHROME2",
				},
				ByteOrder: binary.LittleEndian, PixelBytes: data,
				Rescale:          Rescale{Slope: test.slope, Intercept: test.offset},
				ImageOrientation: []float64{1, 0, 0, 0, 1, 0},
				ImagePosition:    []float64{0, 0, 0},
			}
			if test.signed {
				frame.Metadata.PixelRepresentation = 1
			}
			volume, err := BuildVolume(&Stack{Frames: []*Frame{frame}, PixelSpacing: []float64{1, 1}, SliceThickness: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer volume.Close() //nolint:errcheck
			prepared, err := volume.PrepareVRVolume(VRPrefilterNone)
			if err != nil {
				t.Fatal(err)
			}
			histogram, err := NewVRHistogramCache().Histogram(prepared, 4)
			if err != nil {
				t.Fatal(err)
			}
			if histogram.Descriptor.DomainMin != test.minimum || histogram.Descriptor.DomainMax != test.maximum ||
				!reflect.DeepEqual(histogram.Counts, []uint64{1, 1, 1, 1}) {
				t.Fatalf("histogram = %#v", histogram)
			}
		})
	}
}

func TestVRHistogramDegenerateDomainHasOneDeterministicPeak(t *testing.T) {
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	stack.Frames = []*Frame{volumeTestFrame(2, 3, []byte{7, 7, 7, 7, 7, 7}, 0)}
	volume, err := BuildVolume(stack)
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close() //nolint:errcheck
	prepared, _ := volume.PrepareVRVolume(VRPrefilterNone)
	histogram, err := NewVRHistogramCache().Histogram(prepared, DefaultVRHistogramBins)
	if err != nil {
		t.Fatal(err)
	}
	if histogram.Descriptor.DomainMin != 7 || histogram.Descriptor.DomainMax != 7 ||
		histogram.Counts[0] != 6 || histogram.PeakBin != 0 || histogram.VoxelCount != 6 {
		t.Fatalf("degenerate histogram = %#v", histogram)
	}
	for index, count := range histogram.Counts[1:] {
		if count != 0 {
			t.Fatalf("bin %d = %d, want zero", index+1, count)
		}
	}
}

func TestVRHistogramRejectsNonFinitePreparedVoxel(t *testing.T) {
	store := NewVolumeStore()
	t.Cleanup(func() { _ = store.Close() })
	descriptor := testVolumeDescriptor(2, 2, 1)
	generation, err := store.ReplaceFloat32(descriptor, []float32{0, 1, float32(math.NaN()), 2})
	if err != nil {
		t.Fatal(err)
	}
	prepared := &VRPreparedVolume{
		Prefilter:  VRPrefilterBasicSmooth5x5,
		Volume:     &Volume{Cols: 2, Rows: 2, Depth: 1},
		Generation: generation,
		descriptor: descriptor,
		store:      store,
	}
	if _, err := NewVRHistogramCache().Histogram(prepared, DefaultVRHistogramBins); !errors.Is(err, ErrInvalidVolumeSnapshot) {
		t.Fatalf("non-finite histogram error = %v", err)
	}
}

func TestVRHistogramRawFilteredCacheIdentityAndEviction(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(8, 8, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close() //nolint:errcheck
	raw, err := volume.PrepareVRVolume(VRPrefilterNone)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := volume.PrepareVRVolume(VRPrefilterBasicSmooth5x5)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewVRHistogramCache(VRHistogramCacheOptions{MaxEntries: 1})
	rawHistogram, err := cache.Histogram(raw, DefaultVRHistogramBins)
	if err != nil {
		t.Fatal(err)
	}
	rawAgain, err := cache.Histogram(raw, DefaultVRHistogramBins)
	if err != nil || !reflect.DeepEqual(rawAgain, rawHistogram) {
		t.Fatalf("raw cache hit = %#v, %v", rawAgain, err)
	}
	filteredHistogram, err := cache.Histogram(filtered, DefaultVRHistogramBins)
	if err != nil {
		t.Fatal(err)
	}
	if rawHistogram.Key == filteredHistogram.Key || rawHistogram.Key.Prefilter == filteredHistogram.Key.Prefilter ||
		rawHistogram.Key.PreparedIdentity == filteredHistogram.Key.PreparedIdentity {
		t.Fatalf("raw/filtered keys = %+v / %+v", rawHistogram.Key, filteredHistogram.Key)
	}
	stats := cache.Stats()
	if stats.Hits != 1 || stats.Misses != 2 || stats.Builds != 2 || stats.Evictions != 1 || stats.Entries != 1 {
		t.Fatalf("cache stats = %+v", stats)
	}
}

func TestVRHistogramMillionVoxelVolumeUsesExactUint64Total(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(512, 512, 4))
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close() //nolint:errcheck
	prepared, err := volume.PrepareVRVolume(VRPrefilterNone)
	if err != nil {
		t.Fatal(err)
	}
	histogram, err := NewVRHistogramCache().Histogram(prepared, DefaultVRHistogramBins)
	if err != nil {
		t.Fatal(err)
	}
	const want = uint64(512 * 512 * 4)
	var sum uint64
	for _, count := range histogram.Counts {
		sum += count
	}
	if histogram.VoxelCount != want || sum != want {
		t.Fatalf("million-voxel total = %d/sum %d, want %d", histogram.VoxelCount, sum, want)
	}
}

func TestVRHistogramCacheDeduplicatesConcurrentRequestsAndDetachesCounts(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(16, 16, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close() //nolint:errcheck
	prepared, _ := volume.PrepareVRVolume(VRPrefilterNone)
	cache := NewVRHistogramCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cache.compute = func(ctx context.Context, prepared *VRPreparedVolume, descriptor VRHistogramDescriptor) (VRHistogram, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return VRHistogram{}, ctx.Err()
		}
		return computeVRHistogramContext(ctx, prepared, descriptor)
	}

	const callers = 12
	results := make([]VRHistogram, callers)
	errs := make([]error, callers)
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = cache.HistogramContext(context.Background(), prepared, DefaultVRHistogramBins)
		}(index)
	}
	<-started
	close(release)
	group.Wait()
	if calls.Load() != 1 || cache.Stats().Builds != 1 {
		t.Fatalf("compute calls/stats = %d/%+v", calls.Load(), cache.Stats())
	}
	for index := range results {
		if errs[index] != nil || !reflect.DeepEqual(results[index], results[0]) {
			t.Fatalf("caller %d = %#v, %v", index, results[index], errs[index])
		}
	}
	results[0].Counts[0]++
	if results[1].Counts[0] == results[0].Counts[0] {
		t.Fatal("published histogram Counts share mutable backing storage")
	}
}

func TestVRHistogramCancellationFailureRetryAndCountOverflow(t *testing.T) {
	volume, err := BuildVolume(gradientXZStack(8, 8, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close() //nolint:errcheck
	prepared, _ := volume.PrepareVRVolume(VRPrefilterNone)
	cache := NewVRHistogramCache()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.HistogramContext(canceled, prepared, DefaultVRHistogramBins); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled histogram error = %v", err)
	}
	if histogram, err := cache.Histogram(prepared, DefaultVRHistogramBins); err != nil || histogram.VoxelCount != 128 {
		t.Fatalf("retry histogram = %#v, %v", histogram, err)
	}

	interruptible := NewVRHistogramCache()
	started := make(chan struct{})
	interruptible.compute = func(ctx context.Context, _ *VRPreparedVolume, _ VRHistogramDescriptor) (VRHistogram, error) {
		close(started)
		<-ctx.Done()
		return VRHistogram{}, ctx.Err()
	}
	ctx, stop := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := interruptible.HistogramContext(ctx, prepared, DefaultVRHistogramBins)
		result <- err
	}()
	<-started
	stop()
	if err := <-result; !errors.Is(err, context.Canceled) || interruptible.Stats().Entries != 0 {
		t.Fatalf("interrupted build = %v, stats %+v", err, interruptible.Stats())
	}
	interruptible.compute = nil
	if _, err := interruptible.Histogram(prepared, DefaultVRHistogramBins); err != nil {
		t.Fatalf("retry after interrupted build: %v", err)
	}

	overflow := VRHistogram{
		Descriptor: VRHistogramDescriptor{Bins: 2, DomainMin: 0, DomainMax: 1},
		Counts:     []uint64{math.MaxUint64, 1}, VoxelCount: math.MaxUint64,
		PeakBin: 0, Generation: 1,
		Key: VRHistogramKey{PreparedIdentity: VolumeResidencyKey{StoreIdentity: 1, Generation: 1}, Bins: 2, DomainMin: 0, DomainMax: 1},
	}
	if err := validateVRHistogram(overflow); err == nil {
		t.Fatal("overflowing histogram total was accepted")
	}
}
