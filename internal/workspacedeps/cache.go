package workspacedeps

import (
	"sync"
	"time"
)

// Snapshot is one cached discovery pass for a (bot, target) pair (design
// §8.5): the probed platform plus every observed dependency.
type Snapshot struct {
	Platform Platform
	Observed map[string]Observed
	At       time.Time
}

type cacheKey struct {
	botID    string
	targetID string
}

// Cache holds discovery snapshots per bot and workspace target. It is safe
// for concurrent use. Wiring Invalidate to the workspace manager's bridge
// reset hook is the service layer's job; this package does not import
// internal/workspace.
type Cache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[cacheKey]Snapshot
	now     func() time.Time
}

// NewCache returns an empty cache whose snapshots expire ttl after they were
// stored. A non-positive ttl never expires entries; they only leave through
// Invalidate.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl:     ttl,
		entries: make(map[cacheKey]Snapshot),
		now:     time.Now,
	}
}

// Get returns the snapshot for (botID, targetID). An expired snapshot is a
// miss and is evicted.
func (c *Cache) Get(botID, targetID string) (Snapshot, bool) {
	key := cacheKey{botID: botID, targetID: targetID}
	c.mu.RLock()
	snap, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return Snapshot{}, false
	}
	if c.expired(snap) {
		c.mu.Lock()
		if current, still := c.entries[key]; still && current.At.Equal(snap.At) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return Snapshot{}, false
	}
	return snap, true
}

// Put stores snap for (botID, targetID), stamping At when the caller left it
// zero. The Observed map is copied so later mutation by the caller cannot
// leak into the cache.
func (c *Cache) Put(botID, targetID string, snap Snapshot) {
	if snap.At.IsZero() {
		snap.At = c.now()
	}
	snap.Observed = cloneObserved(snap.Observed)
	c.mu.Lock()
	c.entries[cacheKey{botID: botID, targetID: targetID}] = snap
	c.mu.Unlock()
}

// Invalidate drops every target's snapshot for botID. Call it whenever the
// bot's container is restarted or rebuilt.
func (c *Cache) Invalidate(botID string) {
	c.mu.Lock()
	for key := range c.entries {
		if key.botID == botID {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// ObserveVersion overwrites the cached version of depID for (botID,
// targetID), for callers that learned the real version out of band such as
// the runtime handshake. It is a no-op when nothing is cached.
func (c *Cache) ObserveVersion(botID, targetID, depID, version string) {
	key := cacheKey{botID: botID, targetID: targetID}
	c.mu.Lock()
	defer c.mu.Unlock()
	snap, ok := c.entries[key]
	if !ok {
		return
	}
	obs, ok := snap.Observed[depID]
	if !ok {
		return
	}
	obs.Version = version
	if len(obs.Candidates) > 0 && obs.Candidates[0].Path == obs.Command {
		candidates := make([]Candidate, len(obs.Candidates))
		copy(candidates, obs.Candidates)
		candidates[0].Version = version
		obs.Candidates = candidates
	}
	observed := cloneObserved(snap.Observed)
	observed[depID] = obs
	snap.Observed = observed
	c.entries[key] = snap
}

func (c *Cache) expired(snap Snapshot) bool {
	return c.ttl > 0 && c.now().Sub(snap.At) >= c.ttl
}

func cloneObserved(observed map[string]Observed) map[string]Observed {
	if observed == nil {
		return nil
	}
	cloned := make(map[string]Observed, len(observed))
	for id, obs := range observed {
		cloned[id] = obs
	}
	return cloned
}
