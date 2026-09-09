package workspacedeps

import (
	"context"
	"sync"
)

func (s *Service) trackOperation(ctx context.Context) (func(), error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.active.Add(1)
	return func() { s.active.Done() }, nil
}

// Shutdown stops admitting operations, cancels Server observation, and waits
// for bounded finalization. An unconfirmed workspace process remains in
// progress for reconciliation rather than being declared failed on disconnect.
func (s *Service) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.stopping = true
	s.shutdownCancel()
	s.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() { s.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A reservation carries the same lock from authorized task creation through
// execution, so foreground and background callers cannot both start the key.
type (
	operationReservationKey struct{}
	operationReservation    struct {
		key     InstallationKey
		release func()
	}
)

func (s *Service) reserveOperation(ctx context.Context, key InstallationKey) (context.Context, func(), error) {
	if !s.locks.tryLock(key) {
		return nil, nil, ErrBusy
	}
	var once sync.Once
	release := func() { once.Do(func() { s.locks.unlock(key) }) }
	return context.WithValue(ctx, operationReservationKey{}, operationReservation{key: key, release: release}), release, nil
}

func (s *Service) acquireOperation(ctx context.Context, key InstallationKey) (func(), error) {
	if reservation, ok := ctx.Value(operationReservationKey{}).(operationReservation); ok && reservation.key == key {
		return reservation.release, nil
	}
	if !s.locks.tryLock(key) {
		return nil, ErrBusy
	}
	return func() { s.locks.unlock(key) }, nil
}

// cancelOnShutdown observes the independent service lifetime while each caller
// retains the request-derived operation context.
func (s *Service) cancelOnShutdown(cancel context.CancelFunc) func() bool {
	return context.AfterFunc(s.shutdownCtx, cancel)
}
