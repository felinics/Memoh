package workspacedeps

import (
	"sync"
	"time"
)

// Snapshot caches the probed platform and observed dependencies for a bot.
type Snapshot struct {
	CatalogDigest string
	Platform      Platform
	Observed      map[string]Observed
	At            time.Time
}

// Cache holds discovery snapshots per bot. It is safe
// for concurrent use. Wiring Invalidate to the workspace manager's bridge
// reset hook is the service layer's job; this package does not import
// internal/workspace.
type Cache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]Snapshot
	now     func() time.Time
}

// NewCache returns an empty cache whose snapshots expire ttl after they were
// stored. A non-positive ttl never expires entries; they only leave through
// Invalidate.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl:     ttl,
		entries: make(map[string]Snapshot),
		now:     time.Now,
	}
}

// Get returns the snapshot for botID. An expired snapshot is a
// miss and is evicted.
func (c *Cache) Get(botID string) (Snapshot, bool) {
	key := botID
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
	snap.Observed = cloneObserved(snap.Observed)
	return snap, true
}

// Put stores snap for botID, stamping At when the caller left it
// zero. The Observed map is copied so later mutation by the caller cannot
// leak into the cache.
func (c *Cache) Put(botID string, snap Snapshot) {
	if snap.At.IsZero() {
		snap.At = c.now()
	}
	snap.Observed = cloneObserved(snap.Observed)
	c.mu.Lock()
	c.entries[botID] = snap
	c.mu.Unlock()
}

// Invalidate drops the snapshot for botID. Call it whenever the
// bot's container is restarted or rebuilt.
func (c *Cache) Invalidate(botID string) {
	c.mu.Lock()
	delete(c.entries, botID)
	c.mu.Unlock()
}

// ObserveVersion overwrites the cached version of depID for botID,
// for callers that learned the real version out of band such as
// the runtime handshake. It is a no-op when nothing is cached.
func (c *Cache) ObserveVersion(botID, depID, version string) {
	key := botID
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

// ObserveVersionAt is ObserveVersion for one specific copy: it overwrites
// the version of the candidate at path and, when that candidate is the
// winning copy, the dependency's version as well. Callers that know which
// copy actually ran (the launcher resolver) use it so a handshake-reported
// version is never attributed to a different copy. It is a no-op when
// nothing is cached or no candidate has that path.
func (c *Cache) ObserveVersionAt(botID, depID, path, version string) {
	key := botID
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
	index := -1
	for i, candidate := range obs.Candidates {
		if candidate.Path == path {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	candidates := make([]Candidate, len(obs.Candidates))
	copy(candidates, obs.Candidates)
	candidates[index].Version = version
	obs.Candidates = candidates
	if obs.Command == path {
		obs.Version = version
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
		obs.Entrypoints = cloneStringMap(obs.Entrypoints)
		obs.Candidates = append([]Candidate(nil), obs.Candidates...)
		obs.State = cloneState(obs.State)
		if obs.Receipt != nil {
			receipt := *obs.Receipt
			receipt.Previous = cloneState(receipt.Previous)
			receipt.Result.Entrypoints = cloneStringMap(receipt.Result.Entrypoints)
			receipt.Result.Raw = append([]byte(nil), receipt.Result.Raw...)
			receipt.Result.Receipt = nil
			obs.Receipt = &receipt
		}
		cloned[id] = obs
	}
	return cloned
}

func cloneState(original *State) *State {
	if original == nil {
		return nil
	}
	state := *original
	state.Entrypoints = cloneStringMap(state.Entrypoints)

	return &state
}
