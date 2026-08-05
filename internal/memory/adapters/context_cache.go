package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	defaultMemoryContextCacheTTL      = time.Minute
	defaultMemoryContextCacheStaleTTL = 5 * time.Minute
	defaultMemoryContextCacheMax      = 256
)

// MemoryContextCacheConfig configures the short-lived chat memory context
// cache. StaleTTL is the additional grace window after TTL expires.
type MemoryContextCacheConfig struct {
	TTL        time.Duration
	StaleTTL   time.Duration
	MaxEntries int
	Now        func() time.Time
}

// MemoryContextCacheKey identifies one rendered memory context payload.
type MemoryContextCacheKey struct {
	BotID         string
	ChatID        string
	ProviderID    string
	QueryHash     string
	MemoryVersion string
}

// MemoryContextCacheState classifies an available cache entry by age.
type MemoryContextCacheState string

const (
	MemoryContextCacheFresh MemoryContextCacheState = "fresh"
	MemoryContextCacheStale MemoryContextCacheState = "stale"
)

// MemoryContextCacheValue is a cached rendered memory context.
type MemoryContextCacheValue struct {
	ContextText    string
	RetrievalMode  string
	FallbackReason string
	ResultCount    int
	ResultRefs     []string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	StaleUntil     time.Time
	LastAccessedAt time.Time
}

// MemoryContextCache stores rendered memory context for fast first-token paths.
type MemoryContextCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	staleTTL   time.Duration
	maxEntries int
	now        func() time.Time
	entries    map[MemoryContextCacheKey]MemoryContextCacheValue
}

func NewMemoryContextCache(cfg MemoryContextCacheConfig) *MemoryContextCache {
	if cfg.TTL <= 0 {
		cfg.TTL = defaultMemoryContextCacheTTL
	}
	if cfg.StaleTTL <= 0 {
		cfg.StaleTTL = defaultMemoryContextCacheStaleTTL
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaultMemoryContextCacheMax
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MemoryContextCache{
		ttl:        cfg.TTL,
		staleTTL:   cfg.StaleTTL,
		maxEntries: cfg.MaxEntries,
		now:        cfg.Now,
		entries:    make(map[MemoryContextCacheKey]MemoryContextCacheValue),
	}
}

// MemoryContextQueryHash returns a stable compact hash for cache keys.
func MemoryContextQueryHash(query string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(query)))
	return hex.EncodeToString(sum[:])[:16]
}

// Get returns a fresh cache value.
func (c *MemoryContextCache) Get(key MemoryContextCacheKey) (MemoryContextCacheValue, bool) {
	if c == nil || !validMemoryContextCacheKey(key) {
		return MemoryContextCacheValue{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	entry, ok := c.entries[key]
	if !ok || now.After(entry.ExpiresAt) {
		return MemoryContextCacheValue{}, false
	}
	entry.LastAccessedAt = now
	c.entries[key] = entry
	return cloneMemoryContextCacheValue(entry), true
}

// GetStale returns a value inside its stale grace window.
func (c *MemoryContextCache) GetStale(key MemoryContextCacheKey) (MemoryContextCacheValue, bool) {
	if c == nil || !validMemoryContextCacheKey(key) {
		return MemoryContextCacheValue{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	entry, ok := c.entries[key]
	if !ok || now.After(entry.StaleUntil) {
		return MemoryContextCacheValue{}, false
	}
	entry.LastAccessedAt = now
	c.entries[key] = entry
	return cloneMemoryContextCacheValue(entry), true
}

// GetFreshOrStale returns an available value and atomically classifies its age.
func (c *MemoryContextCache) GetFreshOrStale(key MemoryContextCacheKey) (MemoryContextCacheValue, MemoryContextCacheState, bool) {
	if c == nil || !validMemoryContextCacheKey(key) {
		return MemoryContextCacheValue{}, "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	entry, ok := c.entries[key]
	if !ok || now.After(entry.StaleUntil) {
		return MemoryContextCacheValue{}, "", false
	}
	state := MemoryContextCacheFresh
	if now.After(entry.ExpiresAt) {
		state = MemoryContextCacheStale
	}
	entry.LastAccessedAt = now
	c.entries[key] = entry
	return cloneMemoryContextCacheValue(entry), state, true
}

// Set stores a rendered memory context.
func (c *MemoryContextCache) Set(key MemoryContextCacheKey, value MemoryContextCacheValue) {
	if c == nil || !validMemoryContextCacheKey(key) {
		return
	}
	value.ContextText = strings.TrimSpace(value.ContextText)
	if value.ContextText == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	value = cloneMemoryContextCacheValue(value)
	value.CreatedAt = now
	value.LastAccessedAt = now
	value.ExpiresAt = now.Add(c.ttl)
	value.StaleUntil = value.ExpiresAt.Add(c.staleTTL)
	c.entries[key] = value
	c.pruneLocked()
}

func cloneMemoryContextCacheValue(value MemoryContextCacheValue) MemoryContextCacheValue {
	value.ResultRefs = append([]string(nil), value.ResultRefs...)
	return value
}

func (c *MemoryContextCache) pruneLocked() {
	if len(c.entries) <= c.maxEntries {
		return
	}
	var oldestKey MemoryContextCacheKey
	var oldest time.Time
	for key, entry := range c.entries {
		accessed := entry.LastAccessedAt
		if accessed.IsZero() {
			accessed = entry.CreatedAt
		}
		if oldest.IsZero() || accessed.Before(oldest) {
			oldest = accessed
			oldestKey = key
		}
	}
	delete(c.entries, oldestKey)
}

func validMemoryContextCacheKey(key MemoryContextCacheKey) bool {
	return strings.TrimSpace(key.BotID) != "" &&
		strings.TrimSpace(key.ChatID) != "" &&
		strings.TrimSpace(key.ProviderID) != "" &&
		strings.TrimSpace(key.QueryHash) != ""
}
