package external

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Caps is what a runtime declares about itself beyond the interfaces it
// implements: an optional capability is the interface (As finds it), never a
// flag repeating it.
type Caps struct {
	// Dependency is the managed workspace dependency providing the CLI, and
	// Command the executable it installs. Both are empty for a runtime whose
	// command comes from its configuration (custom ACP agents).
	Dependency string
	Command    string

	// ForkNeedsRuntime: the conversation's native session lives in its
	// runtime metadata with no owner, so a Memoh-side copy would share it. A
	// fork must then go through the runtime (ThreadForker); without one the
	// conversation cannot be forked at all.
	ForkNeedsRuntime bool
	// ForkAnchorKey names the runtime metadata key holding the only turn a
	// fork may start from. Empty: any turn can be forked.
	ForkAnchorKey string
	// ReadCommandsRequireIdle: a read command may open or replace the native
	// session, so it waits for the conversation to be idle.
	ReadCommandsRequireIdle bool
}

// ForkLatestOnly reports a runtime that forks only from its latest turn.
func (c Caps) ForkLatestOnly() bool { return c.ForkAnchorKey != "" }

// Driver runs turns for one external agent runtime type.
type Driver interface {
	// RuntimeType is the thread runtime type this driver serves (e.g. "codex").
	RuntimeType() string
	// Caps declares what the runtime supports.
	Caps() Caps
	// Prompt runs one turn. Stream events flow through input.Sink while the
	// turn runs; the returned result carries the transcript for persistence.
	// A context cancellation is an interrupt: the driver must stop the turn
	// and still return the partial transcript it has.
	Prompt(ctx context.Context, input PromptInput) (PromptResult, error)
}

// Quota is the usage of the account an Agent's runtime is signed in to.
type Quota struct {
	LimitReached bool
	// Windows are the rolling usage windows, the shortest first.
	Windows []QuotaWindow
}

// QuotaWindow is one rolling usage window.
type QuotaWindow struct {
	UsedPercent   int
	WindowMinutes int
	ResetsAt      *time.Time
}

// ErrQuotaUnavailable: the runtime could not read the account's usage.
var ErrQuotaUnavailable = errors.New("runtime account usage is unavailable")

// QuotaReader is implemented by runtimes whose account reports its usage.
type QuotaReader interface {
	Quota(ctx context.Context, botID, botAgentID string) (Quota, error)
}

// Registry holds every external runtime's driver: the one place callers find
// a runtime, what it declares, and the interface behind a capability.
type Registry struct {
	drivers map[string]Driver
	order   []string
}

// NewRegistry registers drivers, refusing one whose declaration cannot hold.
func NewRegistry(drivers ...Driver) (*Registry, error) {
	r := &Registry{drivers: map[string]Driver{}}
	var problems []string
	for _, d := range drivers {
		runtimeType := strings.TrimSpace(d.RuntimeType())
		if _, dup := r.drivers[runtimeType]; dup || runtimeType == "" {
			problems = append(problems, fmt.Sprintf("runtime %q registered twice or unnamed", runtimeType))
			continue
		}
		caps := d.Caps()
		_, forks := d.(ThreadForker)
		if (caps.Dependency == "") != (caps.Command == "") {
			problems = append(problems, runtimeType+": dependency and command must be declared together")
		}
		if caps.ForkAnchorKey != "" && !forks {
			problems = append(problems, runtimeType+": a fork anchor without fork")
		}
		r.drivers[runtimeType] = d
		r.order = append(r.order, runtimeType)
	}
	if len(problems) > 0 {
		return nil, errors.New("external runtime registry: " + strings.Join(problems, "; "))
	}
	return r, nil
}

// Driver returns the runtime's driver.
func (r *Registry) Driver(runtimeType string) (Driver, bool) {
	if r == nil {
		return nil, false
	}
	d, ok := r.drivers[strings.TrimSpace(runtimeType)]
	return d, ok
}

// Caps returns what the runtime declares; false for an unknown runtime.
func (r *Registry) Caps(runtimeType string) (Caps, bool) {
	d, ok := r.Driver(runtimeType)
	if !ok {
		return Caps{}, false
	}
	return d.Caps(), true
}

// All returns the drivers in registration order.
func (r *Registry) All() []Driver {
	if r == nil {
		return nil
	}
	out := make([]Driver, 0, len(r.order))
	for _, runtimeType := range r.order {
		out = append(out, r.drivers[runtimeType])
	}
	return out
}

// As returns the runtime's implementation of a capability interface, false
// when the runtime does not declare it.
func As[T any](r *Registry, runtimeType string) (T, bool) {
	var zero T
	d, ok := r.Driver(runtimeType)
	if !ok {
		return zero, false
	}
	impl, ok := d.(T)
	return impl, ok
}
