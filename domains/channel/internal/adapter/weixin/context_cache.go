package weixin

import (
	"strings"
	"sync"
	"time"
)

// contextTokenCache stores the latest context_token per target user.
// The WeChat API requires a context_token (issued per inbound message)
// for every outbound send. This cache is populated by the long-poll
// receiver and read by the sender, similar to WeCom's callbackContextCache.
type contextTokenCache struct {
	mu    sync.RWMutex
	items map[string]contextTokenEntry
	ttl   time.Duration
}

type contextTokenEntry struct {
	Token     string
	CreatedAt time.Time
}

func newContextTokenCache(ttl time.Duration) *contextTokenCache {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &contextTokenCache{
		items: make(map[string]contextTokenEntry),
		ttl:   ttl,
	}
}

func (c *contextTokenCache) Put(target string, token string) {
	key := strings.TrimSpace(target)
	if key == "" || strings.TrimSpace(token) == "" {
		return
	}
	c.mu.Lock()
	c.items[key] = contextTokenEntry{
		Token:     token,
		CreatedAt: time.Now().UTC(),
	}
	c.gcLocked()
	c.mu.Unlock()
}

func (c *contextTokenCache) Get(target string) (string, bool) {
	key := strings.TrimSpace(target)
	if key == "" {
		return "", false
	}
	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Since(entry.CreatedAt) > c.ttl {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return "", false
	}
	return entry.Token, true
}

func (c *contextTokenCache) gcLocked() {
	if len(c.items) < 512 {
		return
	}
	now := time.Now().UTC()
	for key, entry := range c.items {
		if now.Sub(entry.CreatedAt) > c.ttl {
			delete(c.items, key)
		}
	}
}
