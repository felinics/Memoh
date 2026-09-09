package application

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type stepTraceToolSpan struct {
	startedAt  time.Time
	durationMS int64
	done       bool
}

// stepTraceTracker collects the request trace of a run. Native step facts
// arrive on the provider seam, synchronously before the step commit barrier;
// tool and terminal facts arrive on the public event stream, which every
// runtime shares. The run rollup stays O(1) in the number of steps.
type stepTraceTracker struct {
	mu        sync.Mutex
	now       func() time.Time
	taken     int
	startedAt time.Time
	steps     []messagepkg.StepTraceMetadata
	committed []messagepkg.StepTraceMetadata
	rollup    contextfrag.RunTrace
	tools     map[string]*stepTraceToolSpan
	terminal  *messagepkg.StepTraceUsage
	observed  bool
}

func newStepTraceTracker(now func() time.Time) *stepTraceTracker {
	if now == nil {
		now = time.Now
	}
	return &stepTraceTracker{now: now, tools: make(map[string]*stepTraceToolSpan)}
}

// observeProvider consumes the provider seam: only a finished request yields a
// step, and a new attempt discards finished steps that no commit has taken
// yet, because the retry regenerates them.
func (t *stepTraceTracker) observeProvider(ev native.StreamEvent) {
	if t == nil {
		return
	}
	switch ev.Type {
	case native.EventRetry:
		t.mu.Lock()
		t.steps = nil
		t.mu.Unlock()
	case native.EventStepEnd:
		if ev.Timing == nil {
			return
		}
		trace := messagepkg.StepTraceMetadata{
			Version:        messagepkg.StepTraceVersion,
			StartedAtMS:    ev.Timing.StartedAtMS,
			FirstTokenAtMS: ev.Timing.FirstTokenAtMS,
			EndedAtMS:      ev.Timing.EndedAtMS,
			FinishReason:   strings.TrimSpace(ev.FinishReason),
			Usage:          stepTraceUsageFromRaw(ev.Usage),
		}
		t.mu.Lock()
		t.observed = true
		trace.StepIndex = t.taken + len(t.committed) + len(t.steps)
		t.steps = append(t.steps, trace)
		t.mu.Unlock()
	}
}

// observe consumes the public event stream shared by every runtime.
func (t *stepTraceTracker) observe(ev native.StreamEvent) {
	if t == nil {
		return
	}
	switch ev.Type {
	case native.EventAgentStart, native.EventToolCallStart, native.EventToolCallEnd, native.EventAgentEnd, native.EventAgentAbort:
	default:
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	switch ev.Type {
	case native.EventAgentStart:
		t.observed = true
		if t.rollup.StartedAtMS == 0 {
			t.startedAt = now
			t.rollup.StartedAtMS = now.UnixMilli()
		}
	case native.EventToolCallStart:
		id := strings.TrimSpace(ev.ToolCallID)
		if id == "" {
			return
		}
		t.observed = true
		if _, exists := t.tools[id]; !exists {
			t.tools[id] = &stepTraceToolSpan{startedAt: now}
		}
	case native.EventToolCallEnd:
		id := strings.TrimSpace(ev.ToolCallID)
		if id == "" {
			return
		}
		t.observed = true
		span, exists := t.tools[id]
		if !exists {
			span = &stepTraceToolSpan{startedAt: now}
			t.tools[id] = span
		}
		if span.done {
			return
		}
		span.done = true
		if timing, ok := executionTimingFromMetadata(ev.Metadata); ok {
			span.durationMS = max(timing.EndedAtMS-timing.StartedAtMS, 0)
		} else {
			span.durationMS = max(now.Sub(span.startedAt).Milliseconds(), 0)
		}
		t.rollup.ToolCalls++
		t.rollup.ToolMs += span.durationMS
	case native.EventAgentEnd, native.EventAgentAbort:
		t.observed = true
		t.rollup.EndedAtMS = now.UnixMilli()
		if t.rollup.StartedAtMS != 0 {
			t.rollup.EndedAtMS = t.rollup.StartedAtMS + now.Sub(t.startedAt).Milliseconds()
		}
		if usage := stepTraceUsageFromRaw(ev.Usage); usage != nil {
			t.terminal = usage
		}
	}
}

// checkpoint folds the finished steps into the durable unit so a later retry
// cannot discard them.
func (t *stepTraceTracker) checkpoint() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.foldPending()
	t.committed = append(t.committed, t.steps...)
	t.steps = nil
	t.mu.Unlock()
}

// take returns every completed step of the current durable unit in order and
// resets the unit for the next commit.
func (t *stepTraceTracker) take() []messagepkg.StepTraceMetadata {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.foldPending()
	traces := make([]messagepkg.StepTraceMetadata, 0, len(t.committed)+len(t.steps))
	traces = append(traces, t.committed...)
	traces = append(traces, t.steps...)
	t.taken += len(traces)
	t.committed = nil
	t.steps = nil
	return traces
}

func (t *stepTraceTracker) foldPending() {
	for _, trace := range t.steps {
		foldStepTrace(&t.rollup, trace)
	}
}

// runTrace returns the bounded rollup, or nil when nothing was observed. Steps
// no commit has taken yet count provisionally; a run without provider steps
// takes its usage from the terminal event.
func (t *stepTraceTracker) runTrace() *contextfrag.RunTrace {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.observed {
		return nil
	}
	rollup := t.rollup
	for _, trace := range t.steps {
		foldStepTrace(&rollup, trace)
	}
	if rollup.Steps == 0 && t.terminal != nil {
		addStepTraceUsage(&rollup, *t.terminal)
	}
	return &rollup
}

func foldStepTrace(rollup *contextfrag.RunTrace, trace messagepkg.StepTraceMetadata) {
	rollup.Steps++
	if span := trace.EndedAtMS - trace.StartedAtMS; span > 0 {
		rollup.LLMMs += span
	}
	if trace.FirstTokenAtMS > 0 {
		if rollup.Steps == 1 {
			rollup.TTFTMs = max(trace.FirstTokenAtMS-trace.StartedAtMS, 0)
		}
		if decode := trace.EndedAtMS - trace.FirstTokenAtMS; decode > 0 && trace.Usage != nil && trace.Usage.OutputTokens > 0 {
			rollup.DecodeMs += decode
			rollup.DecodeOutputTokens += trace.Usage.OutputTokens
		}
	}
	if trace.Usage != nil {
		addStepTraceUsage(rollup, *trace.Usage)
	}
}

func addStepTraceUsage(rollup *contextfrag.RunTrace, usage messagepkg.StepTraceUsage) {
	rollup.InputTokens += usage.InputTokens
	rollup.CachedInputTokens += usage.CachedInputTokens
	rollup.CacheWriteTokens += usage.CacheWriteTokens
	rollup.OutputTokens += usage.OutputTokens
	rollup.ReasoningTokens += usage.ReasoningTokens
}

func stepTraceUsageFromRaw(raw json.RawMessage) *messagepkg.StepTraceUsage {
	if len(raw) == 0 {
		return nil
	}
	var usage sdk.Usage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil
	}
	cached := usage.InputTokenDetails.CacheReadTokens
	if cached == 0 {
		cached = usage.CachedInputTokens
	}
	reasoning := usage.ReasoningTokens
	if reasoning == 0 {
		reasoning = usage.OutputTokenDetails.ReasoningTokens
	}
	out := messagepkg.StepTraceUsage{
		InputTokens:       usage.InputTokens,
		CachedInputTokens: cached,
		CacheWriteTokens:  usage.InputTokenDetails.CacheWriteTokens,
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   reasoning,
	}
	if out == (messagepkg.StepTraceUsage{}) {
		return nil
	}
	return &out
}

func executionTimingFromMetadata(metadata map[string]any) (event.ExecutionTiming, bool) {
	raw, ok := metadata[event.ExecutionTimingMetadataKey]
	if !ok || raw == nil {
		return event.ExecutionTiming{}, false
	}
	if timing, ok := raw.(event.ExecutionTiming); ok {
		return timing, timing.EndedAtMS >= timing.StartedAtMS && timing.StartedAtMS > 0
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return event.ExecutionTiming{}, false
	}
	var timing event.ExecutionTiming
	if err := json.Unmarshal(data, &timing); err != nil || timing.StartedAtMS <= 0 || timing.EndedAtMS < timing.StartedAtMS {
		return event.ExecutionTiming{}, false
	}
	return timing, true
}

// configureNativeStepTrace samples provider step boundaries before Twilight's
// step buffer and publishes the run rollup through the lifecycle holder.
// Durable step paths take traces inside their persistence barrier;
// compatibility paths checkpoint per committed step and take at the end.
func configureNativeStepTrace(cfg *native.RunConfig, tracker *stepTraceTracker, stepCommitter *agentStepCommitter) {
	if cfg == nil || tracker == nil {
		return
	}
	previousObserver := cfg.OnProviderStreamEventObserved
	cfg.OnProviderStreamEventObserved = func(ev native.StreamEvent) {
		if previousObserver != nil {
			previousObserver(ev)
		}
		tracker.observeProvider(ev)
	}
	previousAgentObserver := cfg.OnAgentEventObserved
	cfg.OnAgentEventObserved = func(ev native.StreamEvent) {
		if previousAgentObserver != nil {
			previousAgentObserver(ev)
		}
		tracker.observe(ev)
	}
	cfg.ContextLifecycle.SetRunTraceSource(tracker.runTrace)
	if stepCommitter != nil {
		stepCommitter.stepTrace = tracker
		return
	}
	previousCommit := cfg.OnStepCommitted
	cfg.OnStepCommitted = func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
		if previousCommit != nil {
			if err := previousCommit(ctx, stepIndex, step); err != nil {
				return err
			}
		}
		tracker.checkpoint()
		return nil
	}
}

func (opts storeRoundOptions) withStepTraceMetadata(messages []ModelMessage) storeRoundOptions {
	if len(opts.StepTraces) == 0 || len(messages) == 0 {
		return opts
	}
	traceIndex := 0
	for messageIndex, message := range messages {
		if traceIndex >= len(opts.StepTraces) {
			break
		}
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		trace := opts.StepTraces[traceIndex]
		traceIndex++
		if isEmptyAssistantMessage(message) {
			continue
		}
		if opts.MessageMetadataByIndex == nil {
			opts.MessageMetadataByIndex = make(map[int]map[string]any, 1)
		}
		existing := opts.MessageMetadataByIndex[messageIndex]
		if existing == nil {
			existing = map[string]any{}
		}
		existing[messagepkg.StepTraceMetadataKey] = trace
		opts.MessageMetadataByIndex[messageIndex] = existing
	}
	return opts
}
