package application

import (
	"sync"
)

// idempotencyCapacity bounds the in-process claim registry. Platform
// webhook retries arrive within seconds; thousands of retained claims
// comfortably outlive any retry window without growing unbounded.
const idempotencyCapacity = 4096

// idempotencyRegistry claims (team, key) pairs for the process lifetime,
// evicting the oldest claims beyond capacity. It makes webhook redelivery
// at-most-once per process; a persistent claim shared across processes
// arrives with the Run Journal (RFC §6), not this process-local registry.
type idempotencyRegistry struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	cap   int
}

func newIdempotencyRegistry(capacity int) *idempotencyRegistry {
	return &idempotencyRegistry{seen: make(map[string]struct{}, capacity), cap: capacity}
}

// claim returns true when the pair was free and is now claimed.
func (r *idempotencyRegistry) claim(teamID, key string) bool {
	composite := teamID + "\x00" + key
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.seen[composite]; dup {
		return false
	}
	r.seen[composite] = struct{}{}
	r.order = append(r.order, composite)
	if len(r.order) > r.cap {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.seen, oldest)
	}
	return true
}

// release frees a claim so a platform redelivery can retry the turn.
// Called only when a run ends in failure or cancellation: claim-on-start
// without release would swallow the redelivery of a turn that never
// produced a reply (at-zero-once instead of at-most-once).
func (r *idempotencyRegistry) release(teamID, key string) {
	composite := teamID + "\x00" + key
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[composite]; !ok {
		return
	}
	delete(r.seen, composite)
	for i, c := range r.order {
		if c == composite {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}
