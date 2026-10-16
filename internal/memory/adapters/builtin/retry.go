//nolint:sloglint // retry queue ops logs use inline key/value pairs for readability
package builtin

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// semanticRetryCapacity bounds the number of pending upserts kept per
	// runtime. When full, the oldest entry is dropped (the periodic Rebuild
	// path can always re-index everything from PG).
	semanticRetryCapacity = 256
	// semanticRetryInterval is how often the background loop re-attempts
	// pending upserts.
	semanticRetryInterval = 30 * time.Second
)

// semanticUpserter is the subset of pgvectorIndex the retry queue needs;
// abstracted so tests can fake failures without a real pgvector pool.
type semanticUpserter interface {
	Upsert(ctx context.Context, botID, nodeID, body, hash string) error
}

type semanticRetryEntry struct {
	botID  string
	nodeID string
	body   string
	hash   string
}

// semanticRetryQueue keeps failed pgvector upserts and re-attempts them in the
// background. The wiki store (PG) is the source of truth, so entries here only
// mean the semantic seed index is temporarily behind — never that a write was
// lost. Deduped by (botID, nodeID): the newest body/hash wins.
type semanticRetryQueue struct {
	mu       sync.Mutex
	pending  map[string]semanticRetryEntry
	order    []string // FIFO keys for capacity eviction
	started  bool
	stopped  bool
	stopCh   chan struct{}
	stopOnce sync.Once
	cancel   context.CancelFunc
	done     chan struct{}
	logger   *slog.Logger
}

func newSemanticRetryQueue(logger *slog.Logger) *semanticRetryQueue {
	if logger == nil {
		logger = slog.Default()
	}
	return &semanticRetryQueue{
		pending: map[string]semanticRetryEntry{},
		stopCh:  make(chan struct{}),
		logger:  logger,
	}
}

func semanticRetryKey(botID, nodeID string) string { return botID + "\x00" + nodeID }

// enqueue records a failed upsert for later retry, evicting the oldest entry
// when the queue is at capacity.
func (q *semanticRetryQueue) enqueue(entry semanticRetryEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := semanticRetryKey(entry.botID, entry.nodeID)
	if _, exists := q.pending[key]; !exists {
		if len(q.order) >= semanticRetryCapacity {
			oldest := q.order[0]
			q.order = q.order[1:]
			delete(q.pending, oldest)
			q.logger.Debug("semantic retry queue full, dropping oldest entry", "dropped_key", oldest)
		}
		q.order = append(q.order, key)
	}
	q.pending[key] = entry
}

// discard removes pending entries for the given node keys (e.g. after the
// node was deleted, retrying its upsert would resurrect a stale embedding).
func (q *semanticRetryQueue) discard(botID string, nodeIDs []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, nodeID := range nodeIDs {
		q.discardKeyLocked(semanticRetryKey(botID, nodeID))
	}
}

// discardBot removes all pending entries for a bot (after DeleteAll/Rebuild).
func (q *semanticRetryQueue) discardBot(botID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	keys := make([]string, 0, len(q.order))
	prefix := botID + "\x00"
	for _, key := range q.order {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		q.discardKeyLocked(key)
	}
}

func (q *semanticRetryQueue) discardKeyLocked(key string) {
	if _, ok := q.pending[key]; !ok {
		return
	}
	delete(q.pending, key)
	for i, k := range q.order {
		if k == key {
			q.order = append(q.order[:i], q.order[i+1:]...)
			break
		}
	}
}

// depth returns the number of pending retries, optionally scoped to a bot.
func (q *semanticRetryQueue) depth(botID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if botID == "" {
		return len(q.pending)
	}
	prefix := botID + "\x00"
	n := 0
	for key := range q.pending {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// flush re-attempts every pending upsert once. Entries that succeed are
// removed; failures stay queued for the next pass.
func (q *semanticRetryQueue) flush(ctx context.Context, index semanticUpserter) {
	if index == nil {
		return
	}
	q.mu.Lock()
	batch := make([]semanticRetryEntry, 0, len(q.order))
	for _, key := range q.order {
		batch = append(batch, q.pending[key])
	}
	q.mu.Unlock()

	for _, entry := range batch {
		if ctx.Err() != nil {
			return
		}
		attemptCtx, cancel := context.WithTimeout(ctx, semanticEmbedTimeout)
		err := index.Upsert(attemptCtx, entry.botID, entry.nodeID, entry.body, entry.hash)
		cancel()
		if err != nil {
			q.logger.DebugContext(ctx, "semantic retry upsert still failing", "bot_id", entry.botID, "node_id", entry.nodeID, "err", err)
			continue
		}
		q.mu.Lock()
		// Only clear if the entry was not replaced by a newer body meanwhile.
		key := semanticRetryKey(entry.botID, entry.nodeID)
		if cur, ok := q.pending[key]; ok && cur.hash == entry.hash {
			q.discardKeyLocked(key)
		}
		q.mu.Unlock()
	}
}

// start launches the background retry loop. Idempotent. The loop lives until
// the owning runtime closes it.
func (q *semanticRetryQueue) start(ctx context.Context, index semanticUpserter) {
	if index == nil {
		return
	}
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	q.mu.Lock()
	if q.started || q.stopped {
		q.mu.Unlock()
		cancel()
		return
	}
	q.started = true
	q.cancel = cancel
	q.done = make(chan struct{})
	done := q.done
	q.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(semanticRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if q.depth("") == 0 {
					continue
				}
				q.flush(workerCtx, index)
			case <-workerCtx.Done():
				return
			case <-q.stopCh:
				return
			}
		}
	}()
}

func (q *semanticRetryQueue) stop() {
	if q == nil {
		return
	}
	q.stopOnce.Do(func() {
		q.mu.Lock()
		q.stopped = true
		cancel := q.cancel
		done := q.done
		q.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		close(q.stopCh)
		if done != nil {
			<-done
		}
	})
}
