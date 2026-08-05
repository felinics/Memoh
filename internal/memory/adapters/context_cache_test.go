package adapters

import (
	"testing"
	"time"
)

func TestMemoryContextCacheFreshAndStale(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	cache := NewMemoryContextCache(MemoryContextCacheConfig{
		TTL:      10 * time.Second,
		StaleTTL: 30 * time.Second,
		Now: func() time.Time {
			return now
		},
	})
	key := MemoryContextCacheKey{
		BotID:      "bot-1",
		ChatID:     "chat-1",
		ProviderID: "provider-1",
		QueryHash:  MemoryContextQueryHash("hello"),
	}

	refs := []string{"memory-1", "memory-2"}
	cache.Set(key, MemoryContextCacheValue{
		ContextText:   "<memory-context>hello</memory-context>",
		RetrievalMode: "graph",
		ResultCount:   2,
		ResultRefs:    refs,
	})
	refs[0] = "mutated-after-set"

	fresh, ok := cache.Get(key)
	if !ok {
		t.Fatal("expected fresh cache hit")
	}
	if fresh.RetrievalMode != "graph" {
		t.Fatalf("retrieval mode = %q, want graph", fresh.RetrievalMode)
	}
	if fresh.ResultCount != 2 || fresh.ResultRefs[0] != "memory-1" {
		t.Fatalf("fresh result trace = count %d refs %#v", fresh.ResultCount, fresh.ResultRefs)
	}
	fresh.ResultRefs[0] = "mutated-after-get"
	classified, state, ok := cache.GetFreshOrStale(key)
	if !ok || state != MemoryContextCacheFresh || classified.ResultRefs[0] != "memory-1" {
		t.Fatalf("classified fresh result = state %q value %#v ok %v", state, classified, ok)
	}

	now = now.Add(11 * time.Second)
	if _, ok := cache.Get(key); ok {
		t.Fatal("expected fresh cache miss after TTL")
	}
	stale, ok := cache.GetStale(key)
	if !ok {
		t.Fatal("expected stale cache hit inside grace window")
	}
	if stale.ContextText == "" {
		t.Fatal("expected stale context text")
	}
	if stale.ResultCount != 2 || stale.ResultRefs[0] != "memory-1" {
		t.Fatalf("stale result trace aliased a caller slice: count %d refs %#v", stale.ResultCount, stale.ResultRefs)
	}
	classified, state, ok = cache.GetFreshOrStale(key)
	if !ok || state != MemoryContextCacheStale || classified.ResultRefs[0] != "memory-1" {
		t.Fatalf("classified stale result = state %q value %#v ok %v", state, classified, ok)
	}
	stale.ResultRefs[0] = "mutated-after-get-stale"
	staleAgain, ok := cache.GetStale(key)
	if !ok {
		t.Fatal("expected second stale cache hit inside grace window")
	}
	if staleAgain.ResultRefs[0] != "memory-1" {
		t.Fatalf("second stale result trace aliased the first stale result: refs %#v", staleAgain.ResultRefs)
	}

	now = now.Add(31 * time.Second)
	if _, ok := cache.GetStale(key); ok {
		t.Fatal("expected stale cache miss after grace window")
	}
}

func TestMemoryContextCachePrunesOldestEntry(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	cache := NewMemoryContextCache(MemoryContextCacheConfig{
		TTL:        time.Minute,
		StaleTTL:   time.Minute,
		MaxEntries: 1,
		Now: func() time.Time {
			return now
		},
	})
	key1 := MemoryContextCacheKey{BotID: "bot-1", ChatID: "chat-1", ProviderID: "provider-1", QueryHash: "q1"}
	key2 := MemoryContextCacheKey{BotID: "bot-1", ChatID: "chat-1", ProviderID: "provider-1", QueryHash: "q2"}

	cache.Set(key1, MemoryContextCacheValue{ContextText: "one"})
	now = now.Add(time.Second)
	cache.Set(key2, MemoryContextCacheValue{ContextText: "two"})

	if _, ok := cache.Get(key1); ok {
		t.Fatal("expected oldest entry to be pruned")
	}
	if value, ok := cache.Get(key2); !ok || value.ContextText != "two" {
		t.Fatalf("expected newest entry to remain, got value=%+v ok=%v", value, ok)
	}
}
