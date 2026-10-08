package application

import (
	"context"
	"sync"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

// activeTurnTracker keeps the database open until canceled turns have finished
// their application-side persistence. A run's durable interruption is recorded
// before draining begins; this only waits for the old process to stop writing.
type activeTurnTracker struct {
	mu       sync.Mutex
	active   int
	draining bool
	done     chan struct{}
}

func (t *activeTurnTracker) begin() (func(), error) {
	t.mu.Lock()
	if t.draining {
		t.mu.Unlock()
		return nil, sessionruntime.ErrManagerClosed
	}
	t.active++
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.active--
			if t.draining && t.active == 0 && t.done != nil {
				close(t.done)
			}
			t.mu.Unlock()
		})
	}, nil
}

func (t *activeTurnTracker) drain(ctx context.Context) error {
	t.mu.Lock()
	t.draining = true
	if t.active == 0 {
		t.mu.Unlock()
		return nil
	}
	if t.done == nil {
		t.done = make(chan struct{})
	}
	done := t.done
	t.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DrainActiveTurns runs after transports cancel their requests and before the
// PostgreSQL pool closes. It is bounded by the caller's shutdown context.
func (s *Service) DrainActiveTurns(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.activeTurns.drain(ctx)
}
