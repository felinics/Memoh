package codex

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// recyclable is the resource shape the server table manages: a process-backed
// handle that can be torn down and reports its own exit.
type recyclable interface {
	Close() error
	Done() <-chan struct{}
}

// serverTable owns the lifecycle of one shared resource per bot. It exists
// because the app-server is used concurrently (turns, model catalog, login)
// and recycled asynchronously (configuration updates, wedged-turn recovery),
// and ad-hoc checks over a bare map kept racing each other: kill-by-bot
// aborted sibling turns, an is-anyone-else-active check raced registration,
// and a recycle could miss a server still starting outside the lock.
//
// The model is reference counting plus displacement:
//
//   - acquire takes the current entry under the lock and holds a reference
//     for the whole use, so a user is visible from the moment it commits to
//     the server — there is no window between "decided to use it" and
//     "registered".
//   - recycle displaces the entry from the table. New acquires build a fresh
//     server (a new generation); existing references keep the displaced
//     server alive until the last release, which tears it down. A wedged
//     turn is thereby confined to a dying process instead of forcing a
//     choice between killing siblings and sharing the process forever.
//   - a server still starting when displaced discovers it on completion and
//     destroys itself; the acquire retries and rebuilds from current state.
type serverTable struct {
	mu      sync.Mutex
	entries map[string]*serverEntry
	start   func(ctx context.Context, botID string) (recyclable, error)
	logger  *slog.Logger
}

type serverEntry struct {
	resource recyclable
	refs     int
	draining bool
	starting bool
	ready    chan struct{}
}

func newServerTable(start func(ctx context.Context, botID string) (recyclable, error), logger *slog.Logger) *serverTable {
	return &serverTable{
		entries: map[string]*serverEntry{},
		start:   start,
		logger:  logger,
	}
}

var errServerDisplaced = errors.New("codex app-server was recycled during startup")

// acquire returns the bot's live server and a release the caller must invoke
// when done with it (idempotent). It waits for a concurrent startup, retries
// once when a startup loses to a recycle, and never returns a displaced or
// dead server.
func (t *serverTable) acquire(ctx context.Context, botID string) (recyclable, func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		t.mu.Lock()
		entry := t.entries[botID]
		if entry != nil {
			if entry.starting {
				ready := entry.ready
				t.mu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				continue
			}
			if entry.resource != nil && resourceAlive(entry.resource) {
				entry.refs++
				t.mu.Unlock()
				return entry.resource, t.releaseFunc(entry), nil
			}
			// A dead remnant the exit reaper has not collected yet.
			if t.entries[botID] == entry {
				delete(t.entries, botID)
			}
		}
		placeholder := &serverEntry{starting: true, ready: make(chan struct{})}
		t.entries[botID] = placeholder
		t.mu.Unlock()

		resource, err := t.start(ctx, botID)
		t.mu.Lock()
		displaced := t.entries[botID] != placeholder || placeholder.draining
		if err != nil {
			if t.entries[botID] == placeholder {
				delete(t.entries, botID)
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
		go t.reap(botID, placeholder, resource)
		return resource, t.releaseFunc(placeholder), nil
	}
	return nil, nil, errServerDisplaced
}

func (t *serverTable) releaseFunc(entry *serverEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			entry.refs--
			closeNow := entry.draining && entry.refs == 0 && entry.resource != nil
			t.mu.Unlock()
			if closeNow {
				_ = entry.resource.Close()
			}
		})
	}
}

// recycle displaces the bot's entry: new acquires build a fresh server, and
// the displaced one dies when idle — immediately if nothing holds it, at the
// last release otherwise, or on startup completion if it was still starting.
func (t *serverTable) recycle(botID string) {
	t.mu.Lock()
	entry := t.entries[botID]
	if entry == nil {
		t.mu.Unlock()
		return
	}
	delete(t.entries, botID)
	entry.draining = true
	closeNow := !entry.starting && entry.refs == 0 && entry.resource != nil
	resource := entry.resource
	t.mu.Unlock()
	if closeNow {
		_ = resource.Close()
	}
}

func (t *serverTable) recycleWhere(match func(string) bool) {
	t.mu.Lock()
	resources := make([]recyclable, 0)
	for key, entry := range t.entries {
		if !match(key) {
			continue
		}
		delete(t.entries, key)
		entry.draining = true
		if !entry.starting && entry.refs == 0 && entry.resource != nil {
			resources = append(resources, entry.resource)
		}
	}
	t.mu.Unlock()
	for _, resource := range resources {
		_ = resource.Close()
	}
}

// peek returns the bot's current live server without creating one and
// without taking a reference; callers may only touch in-memory state.
func (t *serverTable) peek(botID string) recyclable {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[botID]
	if entry == nil || entry.starting || entry.resource == nil || !resourceAlive(entry.resource) {
		return nil
	}
	return entry.resource
}

// closeAll hard-tears every entry down for process shutdown. Servers still
// starting discover their displacement on completion and self-destroy.
func (t *serverTable) closeAll() {
	t.mu.Lock()
	entries := make([]*serverEntry, 0, len(t.entries))
	for _, entry := range t.entries {
		entry.draining = true
		entries = append(entries, entry)
	}
	t.entries = map[string]*serverEntry{}
	t.mu.Unlock()
	for _, entry := range entries {
		if entry.resource != nil {
			_ = entry.resource.Close()
		}
	}
}

// reap collects the entry when its process exits on its own.
func (t *serverTable) reap(botID string, entry *serverEntry, resource recyclable) {
	<-resource.Done()
	t.mu.Lock()
	if t.entries[botID] == entry {
		delete(t.entries, botID)
	}
	t.mu.Unlock()
	t.logger.Info("codex app-server exited", slog.String("bot_id", botID))
}

func resourceAlive(resource recyclable) bool {
	select {
	case <-resource.Done():
		return false
	default:
		return true
	}
}
