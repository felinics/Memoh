package native

import (
	"encoding/json"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// stepClock measures every provider attempt at the provider seam: dispatch,
// first content part, and finish-step. Only attempts that reach finish-step
// complete; an errored or retried attempt leaves no completed record.
type stepClock struct {
	mu     sync.Mutex
	now    func() time.Time
	active *event.StepTiming
	last   *completedStep
}

type completedStep struct {
	Timing       event.StepTiming
	Usage        sdk.Usage
	FinishReason sdk.FinishReason
}

func newStepClock(now func() time.Time) *stepClock {
	if now == nil {
		now = time.Now
	}
	return &stepClock{now: now}
}

func (c *stepClock) begin() int64 {
	if c == nil {
		return 0
	}
	startedAt := c.now().UnixMilli()
	c.mu.Lock()
	c.active = &event.StepTiming{StartedAtMS: startedAt}
	c.mu.Unlock()
	return startedAt
}

func (c *stepClock) abandon() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.active = nil
	c.mu.Unlock()
}

func (c *stepClock) firstToken() {
	if c == nil {
		return
	}
	now := c.now().UnixMilli()
	c.mu.Lock()
	if c.active != nil && c.active.FirstTokenAtMS == 0 {
		c.active.FirstTokenAtMS = now
	}
	c.mu.Unlock()
}

func (c *stepClock) finish(usage sdk.Usage, reason sdk.FinishReason) (completedStep, bool) {
	if c == nil {
		return completedStep{}, false
	}
	now := c.now().UnixMilli()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return completedStep{}, false
	}
	timing := *c.active
	timing.EndedAtMS = now
	c.active = nil
	c.last = &completedStep{Timing: timing, Usage: usage, FinishReason: reason}
	return *c.last, true
}

func (c *stepClock) lastCompleted() (completedStep, bool) {
	if c == nil {
		return completedStep{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return completedStep{}, false
	}
	return *c.last, true
}

// stepBoundaryEmitter turns the provider's start-step and finish-step parts
// into public step events. Indexes count model requests; a retried request
// keeps its index because the previous attempt never finished.
type stepBoundaryEmitter struct {
	clock *stepClock
	index int
	open  bool
}

func (e *stepBoundaryEmitter) observe(part sdk.StreamPart) (StreamEvent, bool) {
	if e == nil {
		return StreamEvent{}, false
	}
	switch p := part.(type) {
	case *sdk.StartStepPart:
		if e.open {
			return StreamEvent{}, false
		}
		e.open = true
		return StreamEvent{Type: EventStepStart, StepIndex: e.index}, true
	case *sdk.FinishStepPart:
		e.open = false
		ev := StreamEvent{
			Type:         EventStepEnd,
			StepIndex:    e.index,
			FinishReason: string(p.FinishReason),
			Usage:        marshalUsage(p.Usage),
		}
		if last, ok := e.clock.lastCompleted(); ok {
			timing := last.Timing
			ev.Timing = &timing
		}
		e.index++
		return ev, true
	default:
		return StreamEvent{}, false
	}
}

func marshalUsage(usage sdk.Usage) json.RawMessage {
	data, err := json.Marshal(usage)
	if err != nil {
		return nil
	}
	return data
}
