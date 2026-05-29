package render

import (
	"bytes"
	"container/list"
	"fmt"
	"image"
	"image/png"
	"math"
	"sync"

	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

const defaultRenderCacheBytes int64 = 128 << 20

type RenderCache struct {
	mu sync.Mutex

	maxDecodedBytes int64
	decodedBytes    int64
	decodedEntries  map[*Frame]*list.Element
	decodedLRU      *list.List

	maxWindowBytes int64
	windowBytes    int64
	windowEntries  map[windowCacheKey]*list.Element
	windowLRU      *list.List

	pinnedDecoded map[*Frame]bool
	pinnedWindow  map[windowCacheKey]bool
	hits          uint64
	misses        uint64
	evictions     uint64

	decodeFrame func(*Frame) (*decodedFrame, error)
}

type decodedCacheEntry struct {
	slice *Frame
	frame *decodedFrame
	bytes int64
}

type windowCacheKey struct {
	slice      *Frame
	centerBits uint64
	widthBits  uint64
	function   int
	lut        *display.LUT
}

type windowCacheEntry struct {
	key   windowCacheKey
	image image.Image
	err   error
	bytes int64
}

func NewRenderCache(maxBytes int64) *RenderCache {
	if maxBytes <= 0 {
		maxBytes = defaultRenderCacheBytes
	}
	cache := &RenderCache{
		maxDecodedBytes: maxBytes,
		decodedEntries:  map[*Frame]*list.Element{},
		decodedLRU:      list.New(),
		maxWindowBytes:  maxBytes,
		windowEntries:   map[windowCacheKey]*list.Element{},
		windowLRU:       list.New(),
		pinnedDecoded:   map[*Frame]bool{},
		pinnedWindow:    map[windowCacheKey]bool{},
	}
	cache.decodeFrame = decodeSliceFrame
	return cache
}

func (c *RenderCache) RenderFrame(slice *Frame, window WindowLevel) (image.Image, error) {
	if slice == nil {
		return blankImage(512, 512), fmt.Errorf("render: nil slice")
	}
	if slice.DecodeErr != nil {
		return blankImage(int(slice.Metadata.Columns), int(slice.Metadata.Rows)), slice.DecodeErr
	}
	window = normalizedRenderWindow(slice, window)
	key := windowCacheKey{
		slice:      slice,
		centerBits: cacheFloatBits(window.Center),
		widthBits:  cacheFloatBits(window.Width),
		function:   int(window.Function),
		lut:        window.LUT,
	}
	if img, err, ok := c.cachedWindowImage(key); ok {
		c.recordResult(true)
		return img, err
	}
	c.recordResult(false)
	decoded, err := c.decoded(slice)
	if err != nil {
		return blankImage(int(slice.Metadata.Columns), int(slice.Metadata.Rows)), err
	}
	img, err := renderDecodedFrame(decoded, window)
	if img == nil {
		img = blankImage(int(slice.Metadata.Columns), int(slice.Metadata.Rows))
	}
	c.storeWindowImage(key, img, err)
	return img, err
}

// ContainsFrame reports whether the fully windowed frame is resident. It also
// promotes a hit in the LRU, allowing an interactive viewer to pin its visible
// frame while bounded background prefetch continues.
func (c *RenderCache) ContainsFrame(slice *Frame, window WindowLevel) bool {
	if c == nil || slice == nil {
		return false
	}
	window = normalizedRenderWindow(slice, window)
	key := windowCacheKey{
		slice:      slice,
		centerBits: cacheFloatBits(window.Center),
		widthBits:  cacheFloatBits(window.Width),
		function:   int(window.Function),
		lut:        window.LUT,
	}
	_, _, ok := c.cachedWindowImage(key)
	return ok
}

// PinnedFrame identifies one active frame/window pair that pressure eviction
// must retain.
type PinnedFrame struct {
	Frame  *Frame
	Window WindowLevel
}

// CacheStats reports PHI-free cache occupancy and counters.
type CacheStats struct {
	DecodedBytes int64
	WindowBytes  int64
	PinnedBytes  int64
	Hits         uint64
	Misses       uint64
	Evictions    uint64
}

// SetPinnedFrames replaces the active-frame set. Pinned entries may exceed the
// local limit, but every non-pinned entry remains evictable.
func (c *RenderCache) SetPinnedFrames(frames []PinnedFrame) {
	if c == nil {
		return
	}
	decoded := make(map[*Frame]bool, len(frames))
	windowed := make(map[windowCacheKey]bool, len(frames))
	for _, pinned := range frames {
		if pinned.Frame == nil {
			continue
		}
		decoded[pinned.Frame] = true
		window := normalizedRenderWindow(pinned.Frame, pinned.Window)
		windowed[windowCacheKey{
			slice:      pinned.Frame,
			centerBits: cacheFloatBits(window.Center),
			widthBits:  cacheFloatBits(window.Width),
			function:   int(window.Function),
			lut:        window.LUT,
		}] = true
	}
	c.mu.Lock()
	c.pinnedDecoded = decoded
	c.pinnedWindow = windowed
	c.evictDecodedToLimitLocked()
	c.evictWindowToLimitLocked()
	c.mu.Unlock()
}

// EvictNonPinned removes all non-visible decoded and windowed entries.
func (c *RenderCache) EvictNonPinned() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	before := c.decodedBytes + c.windowBytes
	for elem := c.decodedLRU.Back(); elem != nil; {
		previous := elem.Prev()
		entry := elem.Value.(*decodedCacheEntry)
		if !c.pinnedDecoded[entry.slice] {
			c.removeDecodedLocked(elem)
		}
		elem = previous
	}
	for elem := c.windowLRU.Back(); elem != nil; {
		previous := elem.Prev()
		entry := elem.Value.(*windowCacheEntry)
		if !c.pinnedWindow[entry.key] {
			c.removeWindowLocked(elem)
		}
		elem = previous
	}
	freed := before - c.decodedBytes - c.windowBytes
	c.mu.Unlock()
	return freed
}

// Stats returns a consistent cache snapshot.
func (c *RenderCache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := CacheStats{
		DecodedBytes: c.decodedBytes,
		WindowBytes:  c.windowBytes,
		Hits:         c.hits,
		Misses:       c.misses,
		Evictions:    c.evictions,
	}
	for _, elem := range c.decodedEntries {
		entry := elem.Value.(*decodedCacheEntry)
		if c.pinnedDecoded[entry.slice] {
			stats.PinnedBytes += entry.bytes
		}
	}
	for _, elem := range c.windowEntries {
		entry := elem.Value.(*windowCacheEntry)
		if c.pinnedWindow[entry.key] {
			stats.PinnedBytes += entry.bytes
		}
	}
	return stats
}

func (c *RenderCache) RenderFramePNG(slice *Frame, window WindowLevel) ([]byte, error) {
	img, err := c.RenderFrame(slice, window)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (c *RenderCache) RenderThumbnail(series *Stack) image.Image {
	if series == nil {
		return blankImage(128, 128)
	}
	img, err := c.RenderFrame(series.FirstDisplayFrame(), series.DefaultWindow)
	if err != nil || img == nil {
		return blankImage(128, 128)
	}
	return img
}

func (c *RenderCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decodedBytes = 0
	c.decodedEntries = map[*Frame]*list.Element{}
	c.decodedLRU.Init()
	c.windowBytes = 0
	c.windowEntries = map[windowCacheKey]*list.Element{}
	c.windowLRU.Init()
	c.pinnedDecoded = map[*Frame]bool{}
	c.pinnedWindow = map[windowCacheKey]bool{}
}

func (c *RenderCache) cachedWindowImage(key windowCacheKey) (image.Image, error, bool) {
	if c == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.windowEntries[key]
	if elem == nil {
		return nil, nil, false
	}
	c.windowLRU.MoveToFront(elem)
	entry := elem.Value.(*windowCacheEntry)
	return entry.image, entry.err, true
}

func (c *RenderCache) decoded(slice *Frame) (*decodedFrame, error) {
	if c == nil {
		return decodeSliceFrame(slice)
	}
	c.mu.Lock()
	if elem := c.decodedEntries[slice]; elem != nil {
		c.decodedLRU.MoveToFront(elem)
		entry := elem.Value.(*decodedCacheEntry)
		c.mu.Unlock()
		return entry.frame, nil
	}
	c.mu.Unlock()

	decoded, err := c.decodeFrame(slice)
	if err != nil {
		return nil, err
	}
	c.storeDecodedFrame(slice, decoded)
	return decoded, nil
}

func (c *RenderCache) storeDecodedFrame(slice *Frame, decoded *decodedFrame) {
	if c == nil || slice == nil || decoded == nil {
		return
	}
	bytes := decoded.cacheBytes()
	c.mu.Lock()
	defer c.mu.Unlock()
	if bytes > c.maxDecodedBytes && !c.pinnedDecoded[slice] {
		return
	}
	if elem := c.decodedEntries[slice]; elem != nil {
		entry := elem.Value.(*decodedCacheEntry)
		c.decodedBytes -= entry.bytes
		entry.frame = decoded
		entry.bytes = bytes
		c.decodedBytes += bytes
		c.decodedLRU.MoveToFront(elem)
	} else {
		elem = c.decodedLRU.PushFront(&decodedCacheEntry{slice: slice, frame: decoded, bytes: bytes})
		c.decodedEntries[slice] = elem
		c.decodedBytes += bytes
	}
	c.evictDecodedToLimitLocked()
}

func (c *RenderCache) storeWindowImage(key windowCacheKey, img image.Image, err error) {
	if c == nil || img == nil {
		return
	}
	bytes := imageCacheBytes(img)
	c.mu.Lock()
	defer c.mu.Unlock()
	if bytes > c.maxWindowBytes && !c.pinnedWindow[key] {
		return
	}
	if elem := c.windowEntries[key]; elem != nil {
		entry := elem.Value.(*windowCacheEntry)
		c.windowBytes -= entry.bytes
		entry.image = img
		entry.err = err
		entry.bytes = bytes
		c.windowBytes += bytes
		c.windowLRU.MoveToFront(elem)
	} else {
		elem = c.windowLRU.PushFront(&windowCacheEntry{key: key, image: img, err: err, bytes: bytes})
		c.windowEntries[key] = elem
		c.windowBytes += bytes
	}
	c.evictWindowToLimitLocked()
}

func (c *RenderCache) evictDecodedToLimitLocked() {
	for c.decodedBytes > c.maxDecodedBytes {
		elem := c.decodedLRU.Back()
		for elem != nil && c.pinnedDecoded[elem.Value.(*decodedCacheEntry).slice] {
			elem = elem.Prev()
		}
		if elem == nil {
			return
		}
		c.removeDecodedLocked(elem)
	}
}

func (c *RenderCache) evictWindowToLimitLocked() {
	for c.windowBytes > c.maxWindowBytes {
		elem := c.windowLRU.Back()
		for elem != nil && c.pinnedWindow[elem.Value.(*windowCacheEntry).key] {
			elem = elem.Prev()
		}
		if elem == nil {
			return
		}
		c.removeWindowLocked(elem)
	}
}

func (c *RenderCache) removeDecodedLocked(elem *list.Element) {
	entry := elem.Value.(*decodedCacheEntry)
	delete(c.decodedEntries, entry.slice)
	c.decodedBytes -= entry.bytes
	c.decodedLRU.Remove(elem)
	c.evictions++
}

func (c *RenderCache) removeWindowLocked(elem *list.Element) {
	entry := elem.Value.(*windowCacheEntry)
	delete(c.windowEntries, entry.key)
	c.windowBytes -= entry.bytes
	c.windowLRU.Remove(elem)
	c.evictions++
}

func (c *RenderCache) recordResult(hit bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if hit {
		c.hits++
	} else {
		c.misses++
	}
	c.mu.Unlock()
}

func normalizedRenderWindow(slice *Frame, window WindowLevel) WindowLevel {
	if window.LUT != nil {
		return window
	}
	if window.Width <= 0 || math.IsNaN(window.Width) || math.IsInf(window.Width, 0) {
		if slice != nil {
			window = slice.DefaultWindow
		}
	}
	if window.Width <= 0 {
		window = WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth}
	}
	return window
}

func cacheFloatBits(value float64) uint64 {
	if value == 0 {
		return 0
	}
	return math.Float64bits(value)
}

func imageCacheBytes(img image.Image) int64 {
	switch typed := img.(type) {
	case *image.Gray:
		return int64(len(typed.Pix))
	case *image.RGBA:
		return int64(len(typed.Pix))
	default:
		bounds := img.Bounds()
		return int64(bounds.Dx() * bounds.Dy() * 4)
	}
}
