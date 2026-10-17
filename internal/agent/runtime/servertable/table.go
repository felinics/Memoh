// Package servertable owns the lifecycle of the agent process a direct
// External Agent driver shares between the conversations of one Bot Agent.
package servertable

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// Resource is the shape the table manages: a process-backed handle that can
// be torn down and reports its own exit.
type Resource interface {
	Close() error
	Done() <-chan struct{}
}

// Table owns one shared resource per key. It exists because an agent server
// is used concurrently (turns, model catalog, login) and recycled
// asynchronously (configuration updates, wedged-turn recovery), and ad-hoc
// checks over a bare map kept racing each other: kill-by-bot aborted sibling
// turns, an is-anyone-else-active check raced registration, and a recycle
// could miss a server still starting outside the lock.
//
// The model is reference counting plus displacement:
//
//   - Acquire takes the current entry under the lock and holds a reference
//     for the whole use, so a user is visible from the moment it commits to
//     the server — there is no window between "decided to use it" and
//     "registered".
//   - Recycle displaces the entry from the table. New acquires build a fresh
//     server (a new generation); existing references keep the displaced
//     server alive until the last release, which tears it down. A wedged
//     turn is thereby confined to a dying process instead of forcing a
//     choice between killing siblings and sharing the process forever.
//   - a server still starting when displaced discovers it on completion and
//     destroys itself; the acquire retries and rebuilds from current state.
type Table struct {
	mu      sync.Mutex
	entries map[string]*entry
	start   func(ctx context.Context, key string) (Resource, error)
	logger  *slog.Logger
}

type entry struct {
	resource Resource
	refs     int
	draining bool
	starting bool
	ready    chan struct{}
}

func New(start func(ctx context.Context, key string) (Resource, error), logger *slog.Logger) *Table {
	return &Table{
		entries: map[string]*entry{},
		start:   start,
		logger:  logger,
	}
}

var errDisplaced = errors.New("agent server was recycled during startup")

// Acquire returns the key's live server and a release the caller must invoke
// when done with it (idempotent). It waits for a concurrent startup, retries
// once when a startup loses to a recycle, and never returns a displaced or
// dead server.
func (t *Table) Acquire(ctx context.Context, key string) (Resource, func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		t.mu.Lock()
		current := t.entries[key]
		if current != nil {
			if current.starting {
				ready := current.ready
				t.mu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				continue
			}
			if current.resource != nil && alive(current.resource) {
				current.refs++
				t.mu.Unlock()
				return current.resource, t.releaseFunc(current), nil
			}
			// A dead remnant the exit reaper has not collected yet.
			if t.entries[key] == current {
				delete(t.entries, key)
			}
		}
		placeholder := &entry{starting: true, ready: make(chan struct{})}
		t.entries[key] = placeholder
		t.mu.Unlock()

		resource, err := t.start(ctx, key)
		t.mu.Lock()
		displaced := t.entries[key] != placeholder || placeholder.draining
		if err != nil {
			if t.entries[key] == placeholder {
				delete(t.entries, key)
			}
			close(placeholder.ready)
			t.mu.Unlock()
			return nil, nil, err
		}
		if displaced {
			// Recycled mid-startup (a configuration update, a shutdown): this
			// server was built from pre-recycle state and must not serve.
			close(placeholder.ready)
			t.mu.Unlock()
			_ = resource.Close()
			continue
		}
		placeholder.resource = resource
		placeholder.starting = false
		placeholder.refs = 1
		close(placeholder.ready)
		t.mu.Unlock()
		go t.reap(key, placeholder, resource)
		return resource, t.releaseFunc(placeholder), nil
	}
	return nil, nil, errDisplaced
}

func (t *Table) releaseFunc(held *entry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			held.refs--
			closeNow := held.draining && held.refs == 0 && held.resource != nil
			t.mu.Unlock()
			if closeNow {
				_ = held.resource.Close()
			}
		})
	}
}

// Recycle displaces the key's entry: new acquires build a fresh server, and
// the displaced one dies when idle — immediately if nothing holds it, at the
// last release otherwise, or on startup completion if it was still starting.
func (t *Table) Recycle(key string) {
	t.RecycleResource(key, nil)
}

// RecycleResource only displaces the observed generation. Discovery runs
// outside the table lock; another caller may already have replaced it by the
// time the discovery result arrives.
func (t *Table) RecycleResource(key string, expected Resource) {
	t.mu.Lock()
	current := t.entries[key]
	if current == nil || (expected != nil && current.resource != expected) {
		t.mu.Unlock()
		return
	}
	delete(t.entries, key)
	current.draining = true
	closeNow := !current.starting && current.refs == 0 && current.resource != nil
	resource := current.resource
	t.mu.Unlock()
	if closeNow {
		_ = resource.Close()
	}
}

func (t *Table) RecycleWhere(match func(string) bool) {
	t.mu.Lock()
	resources := make([]Resource, 0)
	for key, current := range t.entries {
		if !match(key) {
			continue
		}
		delete(t.entries, key)
		current.draining = true
		if !current.starting && current.refs == 0 && current.resource != nil {
			resources = append(resources, current.resource)
		}
	}
	t.mu.Unlock()
	for _, resource := range resources {
		_ = resource.Close()
	}
}

// Peek returns the key's current live server without creating one and
// without taking a reference; callers may only touch in-memory state.
func (t *Table) Peek(key string) Resource {
	t.mu.Lock()
	defer t.mu.Unlock()
	current := t.entries[key]
	if current == nil || current.starting || current.resource == nil || !alive(current.resource) {
		return nil
	}
	return current.resource
}

// CloseAll hard-tears every entry down for process shutdown. Servers still
// starting discover their displacement on completion and self-destroy.
func (t *Table) CloseAll() {
	t.mu.Lock()
	entries := make([]*entry, 0, len(t.entries))
	for _, current := range t.entries {
		current.draining = true
		entries = append(entries, current)
	}
	t.entries = map[string]*entry{}
	t.mu.Unlock()
	for _, current := range entries {
		if current.resource != nil {
			_ = current.resource.Close()
		}
	}
}

// reap collects the entry when its process exits on its own.
func (t *Table) reap(key string, held *entry, resource Resource) {
	<-resource.Done()
	t.mu.Lock()
	if t.entries[key] == held {
		delete(t.entries, key)
	}
	t.mu.Unlock()
	t.logger.Info("agent server exited", slog.String("server_key", key))
}

func alive(resource Resource) bool {
	select {
	case <-resource.Done():
		return false
	default:
		return true
	}
}
