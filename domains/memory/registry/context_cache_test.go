package registry

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

	cache.Set(key, MemoryContextCacheValue{
		ContextText:   "<memory-context>hello</memory-context>",
		RetrievalMode: "graph",
	})

	fresh, ok := cache.Get(key)
	if !ok {
		t.Fatal("expected fresh cache hit")
	}
	if fresh.RetrievalMode != "graph" {
		t.Fatalf("retrieval mode = %q, want graph", fresh.RetrievalMode)
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
