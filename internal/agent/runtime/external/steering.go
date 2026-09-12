package external

import "context"

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
