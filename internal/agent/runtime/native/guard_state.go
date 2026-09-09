package native

import (
	"sync"

	tools "github.com/felinics/memoh/internal/agent/tool"
)

type toolAbortRegistry struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func newToolAbortRegistry() *toolAbortRegistry {
	return &toolAbortRegistry{
		ids: make(map[string]struct{}),
	}
}

func (r *toolAbortRegistry) Add(toolCallID string) {
	if r == nil || toolCallID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids[toolCallID] = struct{}{}
}

func (r *toolAbortRegistry) Take(toolCallID string) bool {
	if r == nil || toolCallID == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ids[toolCallID]; !ok {
		return false
	}
	delete(r.ids, toolCallID)
	return true
}

func (r *toolAbortRegistry) Any() bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids) > 0
}

type toolEventCollector struct {
	mu     sync.Mutex
	closed bool
	events []tools.ToolStreamEvent
}

func newToolEventCollector() *toolEventCollector {
	return &toolEventCollector{}
}

func (c *toolEventCollector) Add(evt tools.ToolStreamEvent) bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.events = append(c.events, evt)
	return true
}

func (c *toolEventCollector) Close() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *toolEventCollector) CloseAndSnapshot() []tools.ToolStreamEvent {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	snapshot := make([]tools.ToolStreamEvent, len(c.events))
	copy(snapshot, c.events)
	return snapshot
}

// Snapshot returns a copy of collected events without closing the collector.
// Callers that own the collector lifetime should still invoke Close (or
// CloseAndSnapshot) so late emits are rejected.
func (c *toolEventCollector) Snapshot() []tools.ToolStreamEvent {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]tools.ToolStreamEvent, len(c.events))
	copy(out, c.events)
	return out
}
