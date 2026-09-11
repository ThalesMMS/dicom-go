package codeccost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

type cacheKey struct {
	codec    string
	fragment string
	rows     uint16
	columns  uint16
	samples  uint16
	bits     uint16
	stored   uint16
}

// CachedBackend records cache-hit versus cache-miss without launching again.
type CachedBackend struct {
	inner  Backend
	mu     sync.Mutex
	frames map[cacheKey][]byte
}

// NewCachedBackend wraps inner with an in-memory decoded-frame cache.
func NewCachedBackend(inner Backend) *CachedBackend {
	return &CachedBackend{inner: inner, frames: map[cacheKey][]byte{}}
}

// Decode returns a cached frame on hit. Misses call inner once.
func (b *CachedBackend) Decode(ctx context.Context, req DecodeRequest) (FrameResult, error) {
	key := cacheKey{
		codec:    req.Codec,
		fragment: hex.EncodeToString(sha256Sum(req.Fragment)),
		rows:     req.Metadata.Rows,
		columns:  req.Metadata.Columns,
		samples:  req.Metadata.SamplesPerPixel,
		bits:     req.Metadata.BitsAllocated,
		stored:   req.Metadata.BitsStored,
	}
	if req.Mode == ModeCacheHit {
		b.mu.Lock()
		pixels, ok := b.frames[key]
		b.mu.Unlock()
		if ok {
			clock := NewClock(nil)
			clock.Start()
			clock.Step(StagePreflight)
			clock.Step(StageAdmission)
			clock.Step(StagePrepare)
			clock.Step(StageLaunch)
			clock.Step(StageExecute)
			clock.Step(StageConvert)
			clock.Step(StageDeliver)
			sum := sha256.Sum256(pixels)
			return FrameResult{
				Codec:       req.Codec,
				Backend:     "cache",
				Cohort:      req.Cohort,
				Mode:        ModeCacheHit,
				StageRecord: clock.Record(),
				PixelSHA256: hex.EncodeToString(sum[:]),
				CacheHit:    true,
				Launches:    0,
				Pixels:      append([]byte(nil), pixels...),
				Elapsed:     clock.Inclusive(),
			}, nil
		}
	}
	result, err := b.inner.Decode(ctx, req)
	if err != nil {
		return result, err
	}
	b.mu.Lock()
	b.frames[key] = append([]byte(nil), result.Pixels...)
	b.mu.Unlock()
	if req.Mode == "" || req.Mode == ModeCacheHit {
		result.Mode = ModeCacheMiss
	}
	return result, nil
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
