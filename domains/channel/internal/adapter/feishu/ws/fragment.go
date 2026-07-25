package ws

import (
	"sync"
	"time"
)

// fragmentCache reassembles a multi-fragment Feishu data frame keyed by
// message id. Incomplete entries are evicted after ttl so a missing
// fragment can't leak memory.
//
// We use it instead of the SDK's larkcache.Cache, which relies on a
// runtime finalizer to stop its janitor goroutine. Tying the cache
// lifecycle to a single websocket session avoids that nondeterministic
// teardown.
type fragmentCache struct {
	ttl    time.Duration
	mu     sync.Mutex
	items  map[string]*fragmentEntry
	stopCh chan struct{}
	once   sync.Once
}

type fragmentEntry struct {
	parts    [][]byte
	expireAt time.Time
}

func newFragmentCache(ttl time.Duration) *fragmentCache {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	c := &fragmentCache{
		ttl:    ttl,
		items:  make(map[string]*fragmentEntry),
		stopCh: make(chan struct{}),
	}
	go c.janitor()
	return c
}

// add stores fragment seq of total sum bytes for msgID and returns the
// concatenated payload once all fragments have arrived. While the message
// is incomplete it returns nil.
func (c *fragmentCache) add(msgID string, sum, seq int, bs []byte) []byte {
	if sum <= 1 {
		return bs
	}
	if seq < 0 || seq >= sum {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[msgID]
	if !ok {
		entry = &fragmentEntry{parts: make([][]byte, sum)}
		c.items[msgID] = entry
	}
	entry.parts[seq] = bs
	entry.expireAt = time.Now().Add(c.ttl)

	total := 0
	for _, p := range entry.parts {
		if len(p) == 0 {
			return nil
		}
		total += len(p)
	}

	out := make([]byte, 0, total)
	for _, p := range entry.parts {
		out = append(out, p...)
	}
	delete(c.items, msgID)
	return out
}

func (c *fragmentCache) stop() {
	c.once.Do(func() { close(c.stopCh) })
}

func (c *fragmentCache) janitor() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case now := <-ticker.C:
			c.evictExpired(now)
		}
	}
}

func (c *fragmentCache) evictExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.items {
		if now.After(v.expireAt) {
			delete(c.items, k)
		}
	}
}
