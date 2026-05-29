package jpip

import "testing"

func TestResponseCacheEvictsLeastRecentlyUsedRepresentation(t *testing.T) {
	cache := newResponseCache(8)
	cache.put("one", Response{Data: []byte("1111")})
	cache.put("two", Response{Data: []byte("2222")})
	if _, ok := cache.get("one"); !ok {
		t.Fatal("expected first entry")
	}
	cache.put("three", Response{Data: []byte("3333")})
	if _, ok := cache.get("two"); ok {
		t.Fatal("least recently used entry survived eviction")
	}
	if got, ok := cache.get("one"); !ok || string(got.Data) != "1111" || !got.CacheHit {
		t.Fatalf("first entry = %#v ok=%t", got, ok)
	}
	stats := cache.stats()
	if stats.Entries != 2 || stats.Bytes != 8 || stats.Evictions != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestResponseCacheDoesNotRetainOversizedEntry(t *testing.T) {
	cache := newResponseCache(3)
	cache.put("large", Response{Data: []byte("1234")})
	if _, ok := cache.get("large"); ok {
		t.Fatal("oversized entry was cached")
	}
	if stats := cache.stats(); stats.Entries != 0 || stats.Bytes != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}
