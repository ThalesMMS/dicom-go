package jpip

import (
	"container/list"
	"sync"
)

const defaultCacheBytes int64 = 128 << 20

type cacheEntry struct {
	key      string
	response Response
	size     int64
}

// CacheStats is a PHI-free snapshot of the bounded response cache.
type CacheStats struct {
	Entries   int
	Bytes     int64
	MaxBytes  int64
	Hits      uint64
	Misses    uint64
	Evictions uint64
}

type responseCache struct {
	mu        sync.Mutex
	maxBytes  int64
	bytes     int64
	entries   map[string]*list.Element
	lru       *list.List
	hits      uint64
	misses    uint64
	evictions uint64
}

func newResponseCache(maxBytes int64) *responseCache {
	if maxBytes <= 0 {
		maxBytes = defaultCacheBytes
	}
	return &responseCache{
		maxBytes: maxBytes,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}
}

func (c *responseCache) get(key string) (Response, bool) {
	if c == nil {
		return Response{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem := c.entries[key]
	if elem == nil {
		c.misses++
		return Response{}, false
	}
	c.hits++
	c.lru.MoveToFront(elem)
	response := cloneResponse(elem.Value.(*cacheEntry).response)
	response.CacheHit = true
	return response, true
}

func (c *responseCache) put(key string, response Response) {
	if c == nil || key == "" || len(response.Data) == 0 {
		return
	}
	size := int64(len(response.Data))
	c.mu.Lock()
	defer c.mu.Unlock()
	if size > c.maxBytes {
		return
	}
	if elem := c.entries[key]; elem != nil {
		entry := elem.Value.(*cacheEntry)
		c.bytes -= entry.size
		entry.response = cloneResponse(response)
		entry.response.CacheHit = false
		entry.size = size
		c.bytes += size
		c.lru.MoveToFront(elem)
	} else {
		entry := &cacheEntry{key: key, response: cloneResponse(response), size: size}
		c.entries[key] = c.lru.PushFront(entry)
		c.bytes += size
	}
	for c.bytes > c.maxBytes {
		elem := c.lru.Back()
		if elem == nil {
			break
		}
		entry := elem.Value.(*cacheEntry)
		delete(c.entries, entry.key)
		c.bytes -= entry.size
		c.lru.Remove(elem)
		c.evictions++
	}
}

func (c *responseCache) stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{
		Entries:   len(c.entries),
		Bytes:     c.bytes,
		MaxBytes:  c.maxBytes,
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
	}
}

func cloneResponse(response Response) Response {
	response.Data = append([]byte(nil), response.Data...)
	return response
}
