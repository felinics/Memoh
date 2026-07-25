package wecom

import (
	"strings"
	"sync"
	"time"
)

type callbackContext struct {
	ReqID       string
	ResponseURL string
	ChatID      string
	UserID      string
	CreatedAt   time.Time
}

type callbackContextCache struct {
	mu    sync.RWMutex
	items map[string]callbackContext
	ttl   time.Duration
}

func newCallbackContextCache(ttl time.Duration) *callbackContextCache {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &callbackContextCache{
		items: make(map[string]callbackContext),
		ttl:   ttl,
	}
}

func (c *callbackContextCache) Put(configID, messageID string, ctx callbackContext) {
	key := callbackContextCacheKey(configID, messageID)
	if key == "" {
		return
	}
	if ctx.CreatedAt.IsZero() {
		ctx.CreatedAt = time.Now().UTC()
	}
	c.mu.Lock()
	c.items[key] = ctx
	c.gcLocked()
	c.mu.Unlock()
}

func (c *callbackContextCache) Get(configID, messageID string) (callbackContext, bool) {
	key := callbackContextCacheKey(configID, messageID)
	if key == "" {
		return callbackContext{}, false
	}
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return callbackContext{}, false
	}
	if time.Since(item.CreatedAt) > c.ttl {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return callbackContext{}, false
	}
	return item, true
}

func callbackContextCacheKey(configID, messageID string) string {
	configID = strings.TrimSpace(configID)
	messageID = strings.TrimSpace(messageID)
	if configID == "" || messageID == "" {
		return ""
	}
	return configID + "\x00" + messageID
}

func (c *callbackContextCache) gcLocked() {
	if len(c.items) < 512 {
		return
	}
	now := time.Now().UTC()
	for key, item := range c.items {
		if now.Sub(item.CreatedAt) > c.ttl {
			delete(c.items, key)
		}
	}
}
