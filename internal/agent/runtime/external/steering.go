package external

import (
	"context"
	"time"
)

// Steering is the application-owned, fenced queue for the current run. Drivers
// enable it only after their native turn is ready to accept same-turn input.
// Accepted means the native runtime confirmed delivery; transcript persistence
// remains part of the external runtime's ordinary round commit.
type Steering interface {
	Enable(context.Context) error
	Wake() <-chan struct{}
	Next(context.Context) (SteerInput, bool, error)
	Accepted(context.Context, string, int) error
	Close(context.Context) error
}

type SteerInput struct {
	ID   string
	Text string
}

// PumpSteering delivers the run's steering queue to a running turn, in queue
// order, until the turn ends (done closes) or the returned stop is called;
// stop returns once the pump has, so an old pump cannot deliver an input to a
// later turn. ready, when set, holds the pump until the native turn can take
// input, and supported says whether it can at all. submit delivers one input
// and returns once the runtime consumed it. A failed delivery is never retried
// or moved to another turn: onFailure reports it and the pump stops.
func PumpSteering(ctx context.Context, source Steering, done, ready <-chan struct{}, supported func() bool, submit func(context.Context, SteerInput) error, onFailure func(error)) func() {
	if source == nil {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() { _ = source.Close(ctx) }()
		if ready != nil {
			select {
			case <-ready:
			case <-done:
				return
			case <-workerCtx.Done():
				return
			}
		}
		if supported != nil && !supported() {
			return
		}
		if err := source.Enable(workerCtx); err != nil {
			return
		}
		// Recovers a lost or coalesced cross-owner wake.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-done:
				return
			default:
			}
			input, ok, err := source.Next(workerCtx)
			if err == nil && ok {
				err = submit(workerCtx, input)
			}
			if err != nil {
				if ctx.Err() == nil && (ok || workerCtx.Err() == nil) {
					onFailure(err)
				}
				return
			}
			if ok {
				continue
			}
			select {
			case <-workerCtx.Done():
				return
			case <-done:
				return
			case <-source.Wake():
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-finished }
}
