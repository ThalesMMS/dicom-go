package render

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// VolumeMemoryKind is a PHI-free ownership/accounting category. The voxel
// categories are store-owned. The remaining categories are explicit leases for
// allocations owned by a renderer or transport.
type VolumeMemoryKind uint32

const (
	VolumeMemoryUnknown VolumeMemoryKind = iota
	VolumeMemoryRawFrames
	VolumeMemoryNormalized
	VolumeMemoryRegularized
	VolumeMemoryRGBA
	VolumeMemoryBackendCopy
)

func (k VolumeMemoryKind) String() string {
	switch k {
	case VolumeMemoryRawFrames:
		return "raw_frames"
	case VolumeMemoryNormalized:
		return "normalized_volume"
	case VolumeMemoryRegularized:
		return "regularized_volume"
	case VolumeMemoryRGBA:
		return "rgba_render_buffer"
	case VolumeMemoryBackendCopy:
		return "backend_copy"
	default:
		return "unknown"
	}
}

// VolumeStoreOptions configures the optional residency ceiling. Zero means
// that residency changes only through Replace, Evict, allocation release or
// Close.
type VolumeStoreOptions struct {
	MaxLiveBytes uint64
}

type volumeRecord struct {
	descriptor VolumeDescriptor
	payload    volumePayload
	kind       VolumeMemoryKind
	leases     uint64
	pins       uint64
	retired    bool
}

type trackedMemory struct {
	id         uint64
	generation uint64
	kind       VolumeMemoryKind
	bytes      uint64
	release    func()
}

// VolumeStore owns immutable voxel generations and tracks every explicitly
// attached raw/render/backend allocation. It starts no goroutines.
type VolumeStore struct {
	mu sync.Mutex

	identity     uint64
	maxLiveBytes uint64
	nextGen      uint64
	current      uint64
	records      map[uint64]*volumeRecord
	tracked      map[uint64]*trackedMemory
	nextTracked  uint64
	closed       bool

	liveBytes    uint64
	evictions    uint64
	evictedBytes uint64
	replacements uint64
}

// VolumeStoreIdentity is an opaque, comparable identity for one VolumeStore
// instance. It lets backend adapters distinguish independent stores whose
// generation counters may both start at one without exposing the store or its
// mutation APIs.
type VolumeStoreIdentity struct {
	id uint64
}

var nextVolumeStoreIdentity atomic.Uint64

// VolumeResidencyKey is the PHI-free, process-local wire identity for one
// immutable volume generation. StoreIdentity is an opaque ordinal, never an
// address; Generation remains authoritative only within that store.
type VolumeResidencyKey struct {
	StoreIdentity uint64 `json:"store_identity"`
	Generation    uint64 `json:"generation"`
}

// Valid reports whether both halves of the composite key are non-zero.
func (key VolumeResidencyKey) Valid() bool {
	return key.StoreIdentity != 0 && key.Generation != 0
}

// NewVolumeStore constructs an empty store. The variadic form preserves a
// convenient zero-argument default while allowing a bounded store in tests and
// memory-pressure integrations.
func NewVolumeStore(options ...VolumeStoreOptions) *VolumeStore {
	var config VolumeStoreOptions
	if len(options) > 0 {
		config = options[0]
	}
	identity := nextVolumeStoreIdentity.Add(1)
	if identity == 0 {
		panic("render: volume store identity exhausted")
	}
	return &VolumeStore{
		identity:     identity,
		maxLiveBytes: config.MaxLiveBytes,
		records:      make(map[uint64]*volumeRecord),
		tracked:      make(map[uint64]*trackedMemory),
	}
}

// Replace installs a copied immutable generation and retires the previous
// current generation. Generation values are assigned under the store lock.
func (s *VolumeStore) Replace(input VolumeInput) (uint64, error) {
	if s == nil {
		return 0, ErrVolumeStoreClosed
	}
	descriptor := input.Descriptor
	if descriptor.Derivation == VolumeDerivationUnknown {
		descriptor.Derivation = VolumeDerivationNormalized
	}
	descriptor.VolumeGeneration = 0
	if err := validateVolumeDescriptor(descriptor, true); err != nil {
		return 0, err
	}
	if uint64(len(input.Payload)) != descriptor.ByteLength {
		return 0, fmt.Errorf("%w: payload length %d, descriptor byte length %d", ErrInvalidVolumeSnapshot, len(input.Payload), descriptor.ByteLength)
	}
	payload := make([]byte, len(input.Payload))
	copy(payload, input.Payload)
	return s.replaceOwned(descriptor, &byteVolumePayload{data: payload})
}

// ReplaceFloat32 installs a copied, tightly packed F32 modality volume.
func (s *VolumeStore) ReplaceFloat32(descriptor VolumeDescriptor, values []float32) (uint64, error) {
	copied := make([]float32, len(values))
	copy(copied, values)
	return s.replaceFloat32Owned(descriptor, copied)
}

// ReplaceRegularizedFloat32 explicitly derives a new regular affine
// generation. The parent must still be known to the store at replacement time.
func (s *VolumeStore) ReplaceRegularizedFloat32(parentGeneration uint64, descriptor VolumeDescriptor, values []float32) (uint64, error) {
	copied := make([]float32, len(values))
	copy(copied, values)
	return s.replaceRegularizedFloat32Owned(parentGeneration, descriptor, copied)
}

func (s *VolumeStore) replaceFloat32Owned(descriptor VolumeDescriptor, values []float32) (uint64, error) {
	descriptor.ScalarFormat = VolumeScalarF32ModalityLE
	descriptor.SampleDomain = VolumeSampleDomainModality
	descriptor.RescaleSlope = 1
	descriptor.RescaleIntercept = 0
	descriptor.Derivation = VolumeDerivationNormalized
	descriptor.ParentGeneration = 0
	descriptor.VolumeGeneration = 0
	descriptor.ByteLength = uint64(len(values)) * 4
	if err := validateVolumeDescriptor(descriptor, true); err != nil {
		return 0, err
	}
	return s.replaceOwned(descriptor, &float32VolumePayload{data: values})
}

func (s *VolumeStore) replaceRegularizedFloat32Owned(parentGeneration uint64, descriptor VolumeDescriptor, values []float32) (uint64, error) {
	if parentGeneration == 0 {
		return 0, fmt.Errorf("%w: regularized generation lacks parent", ErrInvalidVolumeSnapshot)
	}
	descriptor.ScalarFormat = VolumeScalarF32ModalityLE
	descriptor.SampleDomain = VolumeSampleDomainModality
	descriptor.RescaleSlope = 1
	descriptor.RescaleIntercept = 0
	descriptor.Derivation = VolumeDerivationRegularized
	descriptor.ParentGeneration = parentGeneration
	descriptor.VolumeGeneration = 0
	descriptor.ByteLength = uint64(len(values)) * 4
	if err := validateVolumeDescriptor(descriptor, true); err != nil {
		return 0, err
	}
	s.mu.Lock()
	_, parentExists := s.records[parentGeneration]
	s.mu.Unlock()
	if !parentExists {
		return 0, fmt.Errorf("%w: parent generation %d", ErrVolumeNotFound, parentGeneration)
	}
	return s.replaceOwned(descriptor, &float32VolumePayload{data: values})
}

func (s *VolumeStore) replaceOwned(descriptor VolumeDescriptor, payload volumePayload) (uint64, error) {
	if s == nil || payload == nil {
		return 0, ErrVolumeStoreClosed
	}
	if payload.byteLen() != descriptor.ByteLength {
		return 0, fmt.Errorf("%w: payload length %d, descriptor byte length %d", ErrInvalidVolumeSnapshot, payload.byteLen(), descriptor.ByteLength)
	}

	var releases []func()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrVolumeStoreClosed
	}
	if descriptor.ParentGeneration != 0 {
		if _, ok := s.records[descriptor.ParentGeneration]; !ok {
			s.mu.Unlock()
			return 0, fmt.Errorf("%w: parent generation %d", ErrVolumeNotFound, descriptor.ParentGeneration)
		}
	}
	_, callbacks := s.collectRetiredLocked()
	releases = append(releases, callbacks...)
	reclaimable := uint64(0)
	if previous := s.records[s.current]; previous != nil && previous.leases == 0 && previous.pins == 0 {
		reclaimable = previous.payload.byteLen()
	}
	prospective, ok := checkedAdd64(s.liveBytes, payload.byteLen())
	if !ok || prospective < reclaimable ||
		(s.maxLiveBytes > 0 && prospective-reclaimable > s.maxLiveBytes) {
		liveBytes, limit := s.liveBytes, s.maxLiveBytes
		s.mu.Unlock()
		runReleaseCallbacks(releases)
		return 0, fmt.Errorf("%w: live=%d incoming=%d reclaimable=%d limit=%d",
			ErrVolumeBudgetExceeded, liveBytes, payload.byteLen(), reclaimable, limit)
	}
	if s.nextGen == math.MaxUint64 {
		s.mu.Unlock()
		runReleaseCallbacks(releases)
		return 0, fmt.Errorf("%w: generation overflow", ErrInvalidVolumeSnapshot)
	}
	descriptor.VolumeGeneration = s.nextGen + 1
	if err := validateVolumeDescriptor(descriptor, false); err != nil {
		s.mu.Unlock()
		runReleaseCallbacks(releases)
		return 0, err
	}
	if previous := s.records[s.current]; previous != nil {
		previous.retired = true
	}
	kind := VolumeMemoryNormalized
	if descriptor.Derivation == VolumeDerivationRegularized {
		kind = VolumeMemoryRegularized
	}
	record := &volumeRecord{descriptor: descriptor, payload: payload, kind: kind}
	if math.MaxUint64-s.liveBytes < payload.byteLen() {
		s.mu.Unlock()
		runReleaseCallbacks(releases)
		return 0, fmt.Errorf("%w: live-byte accounting overflow", ErrInvalidVolumeSnapshot)
	}
	s.nextGen = descriptor.VolumeGeneration
	s.records[descriptor.VolumeGeneration] = record
	s.current = descriptor.VolumeGeneration
	s.liveBytes += payload.byteLen()
	s.replacements++
	_, callbacks = s.collectRetiredLocked()
	releases = append(releases, callbacks...)
	s.mu.Unlock()
	runReleaseCallbacks(releases)
	return descriptor.VolumeGeneration, nil
}

// AcquireCurrent creates an active read lease for the latest generation.
func (s *VolumeStore) AcquireCurrent() (*VolumeLease, error) {
	if s == nil {
		return nil, ErrVolumeStoreClosed
	}
	s.mu.Lock()
	generation := s.current
	s.mu.Unlock()
	if generation == 0 {
		return nil, ErrVolumeNotFound
	}
	return s.Acquire(generation)
}

// Acquire creates an active read lease. Leased data cannot be evicted, reused
// or freed by Replace/Close until Release is called.
func (s *VolumeStore) Acquire(generation uint64) (*VolumeLease, error) {
	if s == nil {
		return nil, ErrVolumeStoreClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrVolumeStoreClosed
	}
	record := s.records[generation]
	if record == nil {
		return nil, fmt.Errorf("%w: %d", ErrVolumeNotFound, generation)
	}
	record.leases++
	return &VolumeLease{store: s, generation: generation}, nil
}

// Pin retains a generation for an active viewer without granting payload
// access. Pins and leases are independently observable and idempotently
// releasable.
func (s *VolumeStore) Pin(generation uint64) (*VolumePin, error) {
	if s == nil {
		return nil, ErrVolumeStoreClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrVolumeStoreClosed
	}
	record := s.records[generation]
	if record == nil {
		return nil, fmt.Errorf("%w: %d", ErrVolumeNotFound, generation)
	}
	record.pins++
	return &VolumePin{store: s, generation: generation}, nil
}

// PinCurrent pins the latest generation.
func (s *VolumeStore) PinCurrent() (*VolumePin, error) {
	if s == nil {
		return nil, ErrVolumeStoreClosed
	}
	s.mu.Lock()
	generation := s.current
	s.mu.Unlock()
	if generation == 0 {
		return nil, ErrVolumeNotFound
	}
	return s.Pin(generation)
}

// CurrentGeneration returns zero for an empty or closed store.
func (s *VolumeStore) CurrentGeneration() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	return s.current
}

// Evict releases up to targetBytes from unleased and unpinned generations.
// Retired generations are considered first, oldest generation first. A current
// generation is evictable only when includeCurrent is explicitly true.
func (s *VolumeStore) Evict(targetBytes uint64, includeCurrent ...bool) uint64 {
	if s == nil || targetBytes == 0 {
		return 0
	}
	allowCurrent := len(includeCurrent) > 0 && includeCurrent[0]
	s.mu.Lock()
	freed, callbacks := s.evictLocked(targetBytes, allowCurrent)
	s.mu.Unlock()
	runReleaseCallbacks(callbacks)
	return freed
}

func (s *VolumeStore) evictLocked(targetBytes uint64, includeCurrent bool) (uint64, []func()) {
	generations := make([]uint64, 0, len(s.records))
	for generation, record := range s.records {
		if record.leases != 0 || record.pins != 0 {
			continue
		}
		if generation == s.current && !includeCurrent {
			continue
		}
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool {
		leftCurrent := generations[i] == s.current
		rightCurrent := generations[j] == s.current
		if leftCurrent != rightCurrent {
			return !leftCurrent
		}
		return generations[i] < generations[j]
	})
	var freed uint64
	var callbacks []func()
	for _, generation := range generations {
		if freed >= targetBytes {
			break
		}
		bytes, releases := s.removeGenerationLocked(generation, true)
		freed += bytes
		callbacks = append(callbacks, releases...)
	}
	return freed, callbacks
}

// TrackMemory accounts an already allocated raw-frame, RGBA or backend copy.
// The returned lease is the explicit owner: while it is live, its generation
// cannot be evicted. Release invokes release exactly once outside the store
// lock. Metadata overhead is not included in byteCount.
func (s *VolumeStore) TrackMemory(generation uint64, kind VolumeMemoryKind, byteCount uint64, release func()) (*VolumeMemoryLease, error) {
	if s == nil {
		return nil, ErrVolumeStoreClosed
	}
	if kind != VolumeMemoryRawFrames && kind != VolumeMemoryRGBA && kind != VolumeMemoryBackendCopy {
		return nil, fmt.Errorf("%w: memory kind %s cannot be externally tracked", ErrInvalidVolumeSnapshot, kind)
	}
	if byteCount == 0 {
		return nil, fmt.Errorf("%w: zero tracked allocation", ErrInvalidVolumeSnapshot)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrVolumeStoreClosed
	}
	record := s.records[generation]
	if record == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %d", ErrVolumeNotFound, generation)
	}
	if math.MaxUint64-s.liveBytes < byteCount {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: live-byte accounting overflow", ErrInvalidVolumeSnapshot)
	}
	_, callbacks := s.collectRetiredLocked()
	if s.maxLiveBytes > 0 && s.liveBytes+byteCount > s.maxLiveBytes {
		liveBytes, limit := s.liveBytes, s.maxLiveBytes
		s.mu.Unlock()
		runReleaseCallbacks(callbacks)
		return nil, fmt.Errorf("%w: live=%d incoming=%d limit=%d", ErrVolumeBudgetExceeded, liveBytes, byteCount, limit)
	}
	if s.nextTracked == math.MaxUint64 {
		s.mu.Unlock()
		runReleaseCallbacks(callbacks)
		return nil, fmt.Errorf("%w: tracked allocation id overflow", ErrInvalidVolumeSnapshot)
	}
	s.nextTracked++
	id := s.nextTracked
	s.tracked[id] = &trackedMemory{id: id, generation: generation, kind: kind, bytes: byteCount, release: release}
	record.leases++
	s.liveBytes += byteCount
	s.mu.Unlock()
	runReleaseCallbacks(callbacks)
	return &VolumeMemoryLease{store: s, id: id}, nil
}

// Close rejects new operations and releases every unleased generation. Data
// protected by a lease/pin is retired and released by the last Release.
func (s *VolumeStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.current = 0
	for _, record := range s.records {
		record.retired = true
	}
	_, callbacks := s.collectRetiredLocked()
	s.mu.Unlock()
	runReleaseCallbacks(callbacks)
	return nil
}

// VolumeLease is an idempotent active-reader handle.
type VolumeLease struct {
	mu         sync.Mutex
	store      *VolumeStore
	generation uint64
	released   bool
}

// Snapshot returns the immutable view tied to this lease.
func (l *VolumeLease) Snapshot() (VolumeSnapshot, error) {
	if _, err := l.record(); err != nil {
		return VolumeSnapshot{}, err
	}
	return VolumeSnapshot{lease: l}, nil
}

// Generation returns zero after Release.
func (l *VolumeLease) Generation() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return 0
	}
	return l.generation
}

// StoreIdentity returns the identity of the store protected by this lease.
// The zero value is returned after Release.
func (l *VolumeLease) StoreIdentity() VolumeStoreIdentity {
	if l == nil {
		return VolumeStoreIdentity{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return VolumeStoreIdentity{}
	}
	return l.store.storeIdentity()
}

// ResidencyKey returns the wire-safe composite identity of this lease. The
// zero value is returned after Release.
func (l *VolumeLease) ResidencyKey() VolumeResidencyKey {
	if l == nil {
		return VolumeResidencyKey{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.store == nil || l.generation == 0 {
		return VolumeResidencyKey{}
	}
	return VolumeResidencyKey{
		StoreIdentity: l.store.identity,
		Generation:    l.generation,
	}
}

func (s *VolumeStore) storeIdentity() VolumeStoreIdentity {
	if s == nil {
		return VolumeStoreIdentity{}
	}
	return VolumeStoreIdentity{id: s.identity}
}

func (l *VolumeLease) record() (*volumeRecord, error) {
	if l == nil {
		return nil, ErrVolumeLeaseReleased
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.store == nil {
		return nil, ErrVolumeLeaseReleased
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	record := l.store.records[l.generation]
	if record == nil {
		return nil, fmt.Errorf("%w: %d", ErrVolumeNotFound, l.generation)
	}
	return record, nil
}

// Release is idempotent.
func (l *VolumeLease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	store, generation := l.store, l.generation
	l.store = nil
	l.generation = 0
	l.mu.Unlock()
	return store.releaseReference(generation, false)
}

// Close aliases Release for io.Closer-style use.
func (l *VolumeLease) Close() error { return l.Release() }

// VolumePin is an idempotent active-viewer retention handle.
type VolumePin struct {
	mu         sync.Mutex
	store      *VolumeStore
	generation uint64
	released   bool
}

func (p *VolumePin) Generation() uint64 {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.released {
		return 0
	}
	return p.generation
}

// Release is idempotent.
func (p *VolumePin) Release() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.released {
		p.mu.Unlock()
		return nil
	}
	p.released = true
	store, generation := p.store, p.generation
	p.store = nil
	p.generation = 0
	p.mu.Unlock()
	return store.releaseReference(generation, true)
}

// Close aliases Release.
func (p *VolumePin) Close() error { return p.Release() }

// VolumeMemoryLease owns one exactly accounted external allocation.
type VolumeMemoryLease struct {
	mu       sync.Mutex
	store    *VolumeStore
	id       uint64
	released bool
}

// Release removes accounting and invokes the registered release callback once.
func (l *VolumeMemoryLease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	store, id := l.store, l.id
	l.store = nil
	l.id = 0
	l.mu.Unlock()

	store.mu.Lock()
	tracked := store.tracked[id]
	if tracked == nil {
		store.mu.Unlock()
		return nil
	}
	delete(store.tracked, id)
	store.liveBytes -= tracked.bytes
	record := store.records[tracked.generation]
	if record != nil && record.leases > 0 {
		record.leases--
	}
	_, callbacks := store.collectRetiredLocked()
	callback := tracked.release
	store.mu.Unlock()
	if callback != nil {
		callback()
	}
	runReleaseCallbacks(callbacks)
	return nil
}

// Close aliases Release.
func (l *VolumeMemoryLease) Close() error { return l.Release() }

func (s *VolumeStore) releaseReference(generation uint64, pin bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	record := s.records[generation]
	if record == nil {
		s.mu.Unlock()
		return nil
	}
	if pin {
		if record.pins == 0 {
			s.mu.Unlock()
			return nil
		}
		record.pins--
	} else {
		if record.leases == 0 {
			s.mu.Unlock()
			return nil
		}
		record.leases--
	}
	_, callbacks := s.collectRetiredLocked()
	s.mu.Unlock()
	runReleaseCallbacks(callbacks)
	return nil
}

func (s *VolumeStore) collectRetiredLocked() (uint64, []func()) {
	generations := make([]uint64, 0, len(s.records))
	for generation, record := range s.records {
		if record.retired && record.leases == 0 && record.pins == 0 {
			generations = append(generations, generation)
		}
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	var freed uint64
	var callbacks []func()
	for _, generation := range generations {
		bytes, releases := s.removeGenerationLocked(generation, false)
		freed += bytes
		callbacks = append(callbacks, releases...)
	}
	return freed, callbacks
}

func (s *VolumeStore) removeGenerationLocked(generation uint64, countEviction bool) (uint64, []func()) {
	record := s.records[generation]
	if record == nil || record.leases != 0 || record.pins != 0 {
		return 0, nil
	}
	bytes := record.payload.byteLen()
	var callbacks []func()
	for id, tracked := range s.tracked {
		if tracked.generation != generation {
			continue
		}
		// Tracked allocations hold a record lease, so this branch is defensive.
		delete(s.tracked, id)
		bytes += tracked.bytes
		if tracked.release != nil {
			callbacks = append(callbacks, tracked.release)
		}
	}
	delete(s.records, generation)
	if generation == s.current {
		s.current = 0
	}
	s.liveBytes -= bytes
	if countEviction {
		s.evictions++
		s.evictedBytes += bytes
	}
	return bytes, callbacks
}

func runReleaseCallbacks(callbacks []func()) {
	for _, callback := range callbacks {
		if callback != nil {
			callback()
		}
	}
}

// VolumeMemoryStats is one PHI-free category total.
type VolumeMemoryStats struct {
	Kind        VolumeMemoryKind
	LiveBytes   uint64
	LeasedBytes uint64
	PinnedBytes uint64
	Allocations uint64
}

// VolumeStoreStats is a consistent, value-only telemetry snapshot. LiveBytes
// exactly counts payload backing allocations and explicitly tracked buffers;
// Go object headers, maps, locks and descriptor records are documented
// separately by MetadataRecords/TrackedRecords and intentionally excluded.
// ActiveLeases/ActivePins are exact reference counts; protected byte totals
// count each allocation once.
type VolumeStoreStats struct {
	Closed            bool
	MaxLiveBytes      uint64
	CurrentGeneration uint64
	LiveGenerations   uint64
	MetadataRecords   uint64
	TrackedRecords    uint64
	LiveBytes         uint64
	ActiveLeases      uint64
	ActivePins        uint64
	LeasedBytes       uint64
	PinnedBytes       uint64
	Replacements      uint64
	Evictions         uint64
	EvictedBytes      uint64
	ByKind            []VolumeMemoryStats
}

// Stats returns exact live-buffer accounting without payload content or PHI.
func (s *VolumeStore) Stats() VolumeStoreStats {
	if s == nil {
		return VolumeStoreStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := VolumeStoreStats{
		Closed:            s.closed,
		MaxLiveBytes:      s.maxLiveBytes,
		CurrentGeneration: s.current,
		LiveGenerations:   uint64(len(s.records)),
		MetadataRecords:   uint64(len(s.records)),
		TrackedRecords:    uint64(len(s.tracked)),
		LiveBytes:         s.liveBytes,
		Replacements:      s.replacements,
		Evictions:         s.evictions,
		EvictedBytes:      s.evictedBytes,
	}
	byKind := make(map[VolumeMemoryKind]*VolumeMemoryStats)
	get := func(kind VolumeMemoryKind) *VolumeMemoryStats {
		entry := byKind[kind]
		if entry == nil {
			entry = &VolumeMemoryStats{Kind: kind}
			byKind[kind] = entry
		}
		return entry
	}
	for _, record := range s.records {
		bytes := record.payload.byteLen()
		entry := get(record.kind)
		entry.LiveBytes += bytes
		entry.Allocations++
		stats.ActiveLeases += record.leases
		stats.ActivePins += record.pins
		if record.leases > 0 {
			entry.LeasedBytes += bytes
			stats.LeasedBytes += bytes
		}
		if record.pins > 0 {
			entry.PinnedBytes += bytes
			stats.PinnedBytes += bytes
		}
	}
	for _, tracked := range s.tracked {
		entry := get(tracked.kind)
		entry.LiveBytes += tracked.bytes
		entry.LeasedBytes += tracked.bytes
		entry.Allocations++
		stats.LeasedBytes += tracked.bytes
	}
	for _, entry := range byKind {
		stats.ByKind = append(stats.ByKind, *entry)
	}
	sort.Slice(stats.ByKind, func(i, j int) bool { return stats.ByKind[i].Kind < stats.ByKind[j].Kind })
	return stats
}

// CopyPayload is a convenience for tests and explicit transport upload. It
// returns a caller-owned copy and never exposes the store's backing allocation.
func (s VolumeSnapshot) CopyPayload() ([]byte, error) {
	descriptor, err := s.Descriptor()
	if err != nil {
		return nil, err
	}
	if descriptor.ByteLength > uint64(math.MaxInt) {
		return nil, fmt.Errorf("%w: payload too large for Go slice", ErrInvalidVolumeSnapshot)
	}
	var out bytes.Buffer
	out.Grow(int(descriptor.ByteLength))
	if err := s.WritePayloadTo(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
