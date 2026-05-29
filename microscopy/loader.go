package microscopy

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
)

var ErrLoaderClosed = errors.New("dicom/microscopy: tile loader is closed")

type TileResult struct {
	Tile       Tile
	Image      image.Image
	Err        error
	Generation uint64
	FromCache  bool
}

type CacheStats struct {
	Entries  int
	Bytes    int64
	MaxBytes int64
	Hits     uint64
	Misses   uint64
}

type cacheEntry struct {
	key   TileKey
	image image.Image
	bytes int64
}

type tileCache struct {
	mu       sync.Mutex
	maxBytes int64
	used     int64
	hits     uint64
	misses   uint64
	entries  map[TileKey]*list.Element
	lru      *list.List
}

func newTileCache(maxBytes int64) *tileCache {
	if maxBytes < 0 {
		maxBytes = 0
	}
	return &tileCache{
		maxBytes: maxBytes,
		entries:  make(map[TileKey]*list.Element),
		lru:      list.New(),
	}
}

func (c *tileCache) get(key TileKey) (image.Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element := c.entries[key]; element != nil {
		c.hits++
		c.lru.MoveToFront(element)
		return element.Value.(*cacheEntry).image, true
	}
	c.misses++
	return nil, false
}

func (c *tileCache) put(key TileKey, value image.Image) {
	if value == nil || c.maxBytes <= 0 {
		return
	}
	bytes := imageBytes(value)
	if bytes <= 0 || bytes > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element := c.entries[key]; element != nil {
		entry := element.Value.(*cacheEntry)
		c.used += bytes - entry.bytes
		entry.image, entry.bytes = value, bytes
		c.lru.MoveToFront(element)
	} else {
		c.entries[key] = c.lru.PushFront(&cacheEntry{key: key, image: value, bytes: bytes})
		c.used += bytes
	}
	for c.used > c.maxBytes {
		element := c.lru.Back()
		if element == nil {
			break
		}
		entry := element.Value.(*cacheEntry)
		delete(c.entries, entry.key)
		c.lru.Remove(element)
		c.used -= entry.bytes
	}
}

func (c *tileCache) stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{
		Entries: len(c.entries), Bytes: c.used, MaxBytes: c.maxBytes,
		Hits: c.hits, Misses: c.misses,
	}
}

func (c *tileCache) clear() {
	c.mu.Lock()
	c.entries = make(map[TileKey]*list.Element)
	c.lru.Init()
	c.used = 0
	c.mu.Unlock()
}

// Loader prioritizes the caller-provided tile order, bounds decoded memory,
// cancels prior viewport work, and serializes publication against generation
// changes. Publish callbacks must not call Request or Close on the same Loader.
type Loader struct {
	source  TileSource
	workers int
	cache   *tileCache

	mu         sync.Mutex
	generation uint64
	cancel     context.CancelFunc
	closed     bool
}

func NewLoader(source TileSource, maxCacheBytes int64, workers int) *Loader {
	if workers <= 0 {
		workers = 4
	}
	return &Loader{
		source: source, workers: workers,
		cache: newTileCache(maxCacheBytes),
	}
}

// Request cancels the preceding viewport request. The returned channel closes
// after every worker exits and yields at most one aggregate error.
func (l *Loader) Request(parent context.Context, tiles []Tile, publish func(TileResult)) (<-chan error, error) {
	if l == nil || l.source == nil {
		return nil, fmt.Errorf("dicom/microscopy: tile loader has no source")
	}
	if parent == nil {
		parent = context.Background()
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrLoaderClosed
	}
	if l.cancel != nil {
		l.cancel()
	}
	l.generation++
	generation := l.generation
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.mu.Unlock()

	tiles = deduplicateTiles(tiles)
	done := make(chan error, 1)
	if len(tiles) == 0 {
		close(done)
		return done, nil
	}
	jobs := make(chan Tile)
	errs := make(chan error, len(tiles))
	var workers sync.WaitGroup
	workerCount := min(l.workers, len(tiles))
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for tile := range jobs {
				if ctx.Err() != nil {
					return
				}
				result := TileResult{Tile: tile, Generation: generation}
				result.Image, result.FromCache = l.cache.get(tile.Key())
				if !result.FromCache {
					result.Image, result.Err = l.source.FetchTile(ctx, tile)
					if result.Err == nil && result.Image != nil {
						l.cache.put(tile.Key(), result.Image)
					}
				}
				if result.Err != nil && !errors.Is(result.Err, context.Canceled) {
					errs <- result.Err
				}
				if !l.publishCurrent(generation, result, publish) {
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, tile := range tiles {
			select {
			case <-ctx.Done():
				return
			case jobs <- tile:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(errs)
		cancel()
		var combined error
		for err := range errs {
			combined = errors.Join(combined, err)
		}
		if combined != nil {
			done <- combined
		}
		close(done)
	}()
	return done, nil
}

func (l *Loader) publishCurrent(generation uint64, result TileResult, publish func(TileResult)) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || generation != l.generation {
		return false
	}
	if publish != nil {
		publish(result)
	}
	return true
}

func (l *Loader) Cancel() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.generation++
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.mu.Unlock()
}

func (l *Loader) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		l.generation++
		if l.cancel != nil {
			l.cancel()
			l.cancel = nil
		}
	}
	l.mu.Unlock()
	l.cache.clear()
}

func (l *Loader) CacheStats() CacheStats {
	if l == nil || l.cache == nil {
		return CacheStats{}
	}
	return l.cache.stats()
}

func deduplicateTiles(tiles []Tile) []Tile {
	seen := make(map[TileKey]bool, len(tiles))
	out := make([]Tile, 0, len(tiles))
	for _, tile := range tiles {
		key := tile.Key()
		if key.FrameNumber <= 0 || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tile)
	}
	return out
}

func imageBytes(value image.Image) int64 {
	if value == nil {
		return 0
	}
	bounds := value.Bounds()
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if width <= 0 || height <= 0 || width > (1<<62)/(height*4) {
		return 0
	}
	return width * height * 4
}
