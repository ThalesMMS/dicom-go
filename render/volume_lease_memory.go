package render

// TrackMemory attaches an external allocation to the exact immutable
// generation protected by this lease. Holding the lease lock across
// VolumeStore.TrackMemory makes TrackMemory and Release linearizable: either
// the allocation is registered against this generation, or the released-lease
// error is returned without registering anything. Retired-generation release
// callbacks may run while the lease mutex is held; callbacks must not call
// Release or TrackMemory on this same non-reentrant VolumeLease.
func (l *VolumeLease) TrackMemory(
	kind VolumeMemoryKind,
	byteCount uint64,
	release func(),
) (*VolumeMemoryLease, error) {
	if l == nil {
		return nil, ErrVolumeLeaseReleased
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.store == nil || l.generation == 0 {
		return nil, ErrVolumeLeaseReleased
	}
	return l.store.TrackMemory(
		l.generation,
		kind,
		byteCount,
		release,
	)
}
