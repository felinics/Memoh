// Package decision holds app-layer decision DTOs and shared waiting
// primitives. Storage is intentionally still owned by the kind-specific
// services: toolapproval uses tool_approval_requests, and userinput uses
// user_input_requests.
package decision

import (
	"context"
	"sync"
	"time"
)

const DefaultFallbackInterval = 500 * time.Millisecond

// Waiter is the single in-process waiting mechanism: instant in-memory
// broadcast for the common case, with a DB-fallback poll (the caller's poll
// func) so decisions recorded by another process or missed broadcasts still
// resolve. The registry (Register/Has) lets delivery paths distinguish "a
// live consumer owns this request" from an orphaned row.
type Waiter[T any] struct {
	mu          sync.Mutex
	subscribers map[string][]chan T
	registered  map[string]int
}

func NewWaiter[T any]() *Waiter[T] {
	return &Waiter[T]{
		subscribers: map[string][]chan T{},
		registered:  map[string]int{},
	}
}

// Subscribe registers a broadcast channel for the request BEFORE any status
// check, so a concurrent decision cannot slip between check and wait. The
// returned unsubscribe must run when the wait ends.
func (w *Waiter[T]) Subscribe(id string) (<-chan T, func()) {
	ch := make(chan T, 1)
	w.mu.Lock()
	if w.subscribers == nil {
		w.subscribers = map[string][]chan T{}
	}
	w.subscribers[id] = append(w.subscribers[id], ch)
	w.mu.Unlock()
	unsubscribe := func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		chans := w.subscribers[id]
		for i, c := range chans {
			if c == ch {
				w.subscribers[id] = append(chans[:i], chans[i+1:]...)
				break
			}
		}
		if len(w.subscribers[id]) == 0 {
			delete(w.subscribers, id)
		}
	}
	return ch, unsubscribe
}

// Notify wakes every subscriber of the request with the resolved value.
// Buffered, non-blocking sends: a subscriber that already left misses
// nothing - its fallback poll covers it.
func (w *Waiter[T]) Notify(id string, value T) {
	if w == nil {
		return
	}
	w.mu.Lock()
	chans := w.subscribers[id]
	delete(w.subscribers, id)
	w.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- value:
		default:
		}
	}
}

// Register records that a caller in this process owns the request's
// resolution. Callers that announce a pending request to users must register
// BEFORE announcing, or an instant response can be misjudged as orphaned.
func (w *Waiter[T]) Register(id string) func() {
	if w == nil {
		return func() {}
	}
	w.mu.Lock()
	if w.registered == nil {
		w.registered = map[string]int{}
	}
	w.registered[id]++
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.registered[id] > 1 {
			w.registered[id]--
			return
		}
		delete(w.registered, id)
	}
}

// Has reports whether anyone in this process is registered for the request.
func (w *Waiter[T]) Has(id string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.registered[id] > 0
}

// Await blocks until the request resolves: instantly via broadcast, or via
// the fallback poll (poll returns done=true with the terminal value). poll
// runs once immediately - a decision may already have landed - and then on
// every fallback interval. On ctx cancellation a last-chance channel drain
// runs so a decision delivered concurrently with cancellation is not lost.
func (w *Waiter[T]) Await(ctx context.Context, id string, fallback time.Duration, poll func(context.Context) (T, bool, error)) (T, error) {
	var zero T
	ch, unsubscribe := w.Subscribe(id)
	defer unsubscribe()

	value, done, err := poll(ctx)
	if err != nil {
		if ctx.Err() != nil {
			if v, ok := drain(ch); ok {
				return v, nil
			}
			return zero, ctx.Err()
		}
		return zero, err
	}
	if done {
		return value, nil
	}

	ticker := time.NewTicker(fallback)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if v, ok := drain(ch); ok {
				return v, nil
			}
			return zero, ctx.Err()
		case v := <-ch:
			return v, nil
		case <-ticker.C:
			value, done, err := poll(ctx)
			if err != nil {
				if ctx.Err() != nil {
					if v, ok := drain(ch); ok {
						return v, nil
					}
					return zero, ctx.Err()
				}
				return zero, err
			}
			if done {
				return value, nil
			}
		}
	}
}

func drain[T any](ch <-chan T) (T, bool) {
	select {
	case v := <-ch:
		return v, true
	default:
		var zero T
		return zero, false
	}
}
