package client

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// runtimeLifecycleLocks serializes connect and revoke for one Runtime without
// blocking unrelated Runtime connections.
type runtimeLifecycleLocks struct {
	mu      sync.Mutex
	entries map[string]*runtimeLifecycleEntry
}

type runtimeLifecycleEntry struct {
	gate chan struct{}
	refs int
}

func newRuntimeLifecycleLocks() *runtimeLifecycleLocks {
	return &runtimeLifecycleLocks{entries: make(map[string]*runtimeLifecycleEntry)}
}

func (c *runtimeLifecycleLocks) lock(ctx context.Context, runtimeID string) (func(), error) {
	if c == nil {
		return nil, errors.New("runtime lifecycle coordinator is not configured")
	}
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return nil, ErrInvalidInput
	}
	c.mu.Lock()
	entry := c.entries[runtimeID]
	if entry == nil {
		entry = &runtimeLifecycleEntry{gate: make(chan struct{}, 1)}
		entry.gate <- struct{}{}
		c.entries[runtimeID] = entry
	}
	entry.refs++
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.releaseRef(runtimeID, entry)
		return nil, ctx.Err()
	case <-entry.gate:
	}
	if err := ctx.Err(); err != nil {
		entry.gate <- struct{}{}
		c.releaseRef(runtimeID, entry)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.gate <- struct{}{}
			c.releaseRef(runtimeID, entry)
		})
	}, nil
}

func (c *runtimeLifecycleLocks) releaseRef(runtimeID string, entry *runtimeLifecycleEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries[runtimeID] != entry {
		return
	}
	entry.refs--
	if entry.refs == 0 {
		delete(c.entries, runtimeID)
	}
}
