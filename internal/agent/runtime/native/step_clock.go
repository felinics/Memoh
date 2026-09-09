package native

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// stepClock measures every provider attempt at the provider seam: dispatch,
// first content part, and finish-step. Only attempts that reach finish-step
// complete; an errored or retried attempt leaves no completed record. The
// dispatch reading anchors the wall clock; later marks add monotonic elapsed
// time so a wall-clock step never reorders them. Completed records queue up
// in order, because the event loop that publishes them may run behind the
// seam by a whole step.
type stepClock struct {
	mu        sync.Mutex
	now       func() time.Time
	since     func(time.Time) time.Duration
	active    *activeStep
	completed []completedStep
}

type activeStep struct {
	startedAt time.Time
	timing    event.StepTiming
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
	clock := &stepClock{now: now}
	clock.since = func(startedAt time.Time) time.Duration { return clock.now().Sub(startedAt) }
	return clock
}

func (c *stepClock) begin() int64 {
	if c == nil {
		return 0
	}
	startedAt := c.now()
	c.mu.Lock()
	c.active = &activeStep{startedAt: startedAt, timing: event.StepTiming{StartedAtMS: startedAt.UnixMilli()}}
	c.mu.Unlock()
	return startedAt.UnixMilli()
}

func (c *stepClock) abandon() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.active = nil
	c.mu.Unlock()
}

// discardCompleted drops completed records no finish-step has taken yet: a
// retry regenerates the failed attempt, whose records would otherwise be
// paired with the next request's finish-step.
func (c *stepClock) discardCompleted() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.completed = nil
	c.mu.Unlock()
}

func (c *stepClock) mark() int64 {
	return c.active.timing.StartedAtMS + c.since(c.active.startedAt).Milliseconds()
}

func (c *stepClock) firstToken() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.timing.FirstTokenAtMS != 0 {
		return
	}
	c.active.timing.FirstTokenAtMS = c.mark()
}

// firstTokenText samples the first non-blank delta; the blank check runs only
// while the sample is still pending so streaming stays cheap.
func (c *stepClock) firstTokenText(text string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.timing.FirstTokenAtMS != 0 || strings.TrimSpace(text) == "" {
		return
	}
	c.active.timing.FirstTokenAtMS = c.mark()
}

func (c *stepClock) finish(usage sdk.Usage, reason sdk.FinishReason) (completedStep, bool) {
	if c == nil {
		return completedStep{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return completedStep{}, false
	}
	timing := c.active.timing
	timing.EndedAtMS = c.mark()
	c.active = nil
	record := completedStep{Timing: timing, Usage: usage, FinishReason: reason}
	c.completed = append(c.completed, record)
	return record, true
}

// takeCompleted hands out the oldest completed record, so the k-th
// finish-step the event loop sees pairs with the k-th request the seam
// finished.
func (c *stepClock) takeCompleted() (completedStep, bool) {
	if c == nil {
		return completedStep{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.completed) == 0 {
		return completedStep{}, false
	}
	record := c.completed[0]
	c.completed = c.completed[1:]
	return record, true
}

// stepBoundaryEmitter turns the provider's start-step and finish-step parts
// into public step events. Indexes count model requests; a retried request
// keeps its index because the previous attempt never finished.
type stepBoundaryEmitter struct {
	clock *stepClock
	index int
	open  bool
}

// reset rewinds the emitter to the last committed request after a retry: the
// failed attempt never finished, so its index is reused, its open start is
// forgotten, and any record its stream completed while being drained is
// dropped.
func (e *stepBoundaryEmitter) reset(index int) {
	if e == nil {
		return
	}
	e.open = false
	e.index = index
	e.clock.discardCompleted()
}

// abandon forgets the request the seam is still clocking, so the parts of a
// failed stream drained before a retry complete nothing.
func (e *stepBoundaryEmitter) abandon() {
	if e == nil {
		return
	}
	e.clock.abandon()
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
		if completed, ok := e.clock.takeCompleted(); ok {
			timing := completed.Timing
			ev.Timing = &timing
			ev.Usage = marshalUsage(completed.Usage)
		}
		e.index++
		return ev, true
	default:
		return StreamEvent{}, false
	}
}

// anthropicMessagesProvider is the Twilight provider whose input_tokens omit
// the cached prompt.
const anthropicMessagesProvider = "anthropic-messages"

// normalizeProviderUsage makes InputTokens count every prompt token the
// provider billed, so a cache-hit ratio reads the same for every provider.
// OpenAI-style providers already include cached tokens in the input count;
// Anthropic reports input_tokens without cache reads and writes.
func normalizeProviderUsage(providerName string, usage sdk.Usage) sdk.Usage {
	if providerName != anthropicMessagesProvider {
		return usage
	}
	cached := usage.InputTokenDetails.CacheReadTokens + usage.InputTokenDetails.CacheWriteTokens
	if cached == 0 {
		return usage
	}
	if usage.InputTokenDetails.NoCacheTokens == 0 {
		usage.InputTokenDetails.NoCacheTokens = usage.InputTokens
	}
	usage.InputTokens += cached
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return usage
}

// providerNameOf names the provider behind a model, or "" when unknown.
func providerNameOf(model *sdk.Model) string {
	if model == nil || model.Provider == nil {
		return ""
	}
	return model.Provider.Name()
}

func marshalUsage(usage sdk.Usage) json.RawMessage {
	data, err := json.Marshal(usage)
	if err != nil {
		return nil
	}
	return data
}
