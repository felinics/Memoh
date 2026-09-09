package application

import (
	"context"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type reasoningTimingActiveSegment struct {
	startedAt  time.Time
	hasContent bool
}

// reasoningTimingTracker observes the same normalized stream vocabulary used
// by Native and ACP. It keeps only the current, uncommitted model attempt so a
// retry cannot leak discarded timing into durable history.
type reasoningTimingTracker struct {
	mu        sync.Mutex
	now       func() time.Time
	active    *reasoningTimingActiveSegment
	segments  []messagepkg.ReasoningTimingSegment
	committed []messagepkg.ReasoningTimingSegment
}

func newReasoningTimingTracker(now func() time.Time) *reasoningTimingTracker {
	if now == nil {
		now = time.Now
	}
	return &reasoningTimingTracker{now: now}
}

func (t *reasoningTimingTracker) observe(event native.StreamEvent) {
	if t == nil {
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	switch event.Type {
	case native.EventRetry:
		// The terminal model transcript contains only the surviving attempt.
		// Drop both active and already-closed segments from the failed attempt.
		// Segments checkpointed by an earlier completed step remain part of the
		// final multi-step transcript.
		t.active = nil
		t.segments = nil
	case native.EventReasoningStart:
		t.finishActiveLocked(now, "completed")
		t.startLocked(now)
	case native.EventReasoningDelta:
		if t.active == nil {
			t.startLocked(now)
		}
		if strings.TrimSpace(event.Delta) != "" {
			t.active.hasContent = true
		}
	case native.EventReasoningEnd:
		t.finishActiveLocked(now, "completed")
	case native.EventAgentAbort:
		t.finishActiveLocked(now, "interrupted")
	case native.EventTextStart,
		native.EventTextDelta,
		native.EventTextEnd,
		native.EventToolCallInputStart,
		native.EventToolCallStart,
		native.EventToolCallMetadata,
		native.EventToolCallProgress,
		native.EventToolCallEnd,
		native.EventToolApprovalRequest,
		native.EventUserInputRequest,
		native.EventAttachment,
		native.EventReaction,
		native.EventSpeech,
		native.EventAgentEnd:
		t.finishActiveLocked(now, "completed")
	}
}

func (t *reasoningTimingTracker) startLocked(now time.Time) {
	t.active = &reasoningTimingActiveSegment{startedAt: now}
}

func (t *reasoningTimingTracker) finishActiveLocked(now time.Time, state string) {
	active := t.active
	t.active = nil
	if active == nil || !active.hasContent {
		return
	}
	duration := now.Sub(active.startedAt)
	if duration < 0 {
		duration = 0
	}
	t.segments = append(t.segments, messagepkg.ReasoningTimingSegment{
		DurationMS: duration.Milliseconds(),
		State:      state,
	})
}

// checkpoint closes the current step and protects its segments from the
// EventRetry marker emitted when the next provider call begins. Step-backed
// persistence uses take directly; terminal-snapshot paths checkpoint each
// completed step and take the full accumulated result at the end.
func (t *reasoningTimingTracker) checkpoint(state string) {
	if t == nil {
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.finishActiveLocked(now, state)
	t.committed = append(t.committed, t.segments...)
	t.segments = nil
}

// take closes an unterminated block at the persistence boundary, returns all
// segments for the current durable unit, and resets the unit for the next step.
func (t *reasoningTimingTracker) take(state string) []messagepkg.ReasoningTimingSegment {
	if t == nil {
		return nil
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.finishActiveLocked(now, state)
	segments := make([]messagepkg.ReasoningTimingSegment, 0, len(t.committed)+len(t.segments))
	segments = append(segments, t.committed...)
	segments = append(segments, t.segments...)
	t.committed = nil
	t.segments = nil
	return segments
}

func takeTerminalReasoningTiming(t *reasoningTimingTracker, eventType native.StreamEventType) []messagepkg.ReasoningTimingSegment {
	state := "completed"
	if eventType == native.EventAgentAbort {
		state = "interrupted"
	}
	return t.take(state)
}

// configureNativeReasoningTiming samples provider parts before Twilight's
// step buffer. Durable step paths consume timings inside their persistence
// barrier. Compatibility paths checkpoint completed steps and attach the full
// set to the terminal snapshot, so a later provider call cannot erase an
// earlier tool-loop step as if it were a retry.
func configureNativeReasoningTiming(
	cfg *native.RunConfig,
	tracker *reasoningTimingTracker,
	stepCommitter *agentStepCommitter,
) {
	if cfg == nil || tracker == nil {
		return
	}
	previousObserver := cfg.OnProviderStreamEventObserved
	cfg.OnProviderStreamEventObserved = func(event native.StreamEvent) {
		if previousObserver != nil {
			previousObserver(event)
		}
		tracker.observe(event)
	}
	if stepCommitter != nil {
		stepCommitter.reasoningTiming = tracker
		cfg.OnStepCommitted = stepCommitter.commit
		cfg.OnStepInterrupted = stepCommitter.interrupt
		return
	}

	previousCommit := cfg.OnStepCommitted
	cfg.OnStepCommitted = func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
		if previousCommit != nil {
			if err := previousCommit(ctx, stepIndex, step); err != nil {
				return err
			}
		}
		tracker.checkpoint("completed")
		return nil
	}
	previousInterrupt := cfg.OnStepInterrupted
	cfg.OnStepInterrupted = func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
		if previousInterrupt != nil {
			if err := previousInterrupt(ctx, stepIndex, step); err != nil {
				return err
			}
		}
		tracker.checkpoint("interrupted")
		return nil
	}
}

func (opts storeRoundOptions) withReasoningTimingMetadata(messages []ModelMessage) storeRoundOptions {
	if len(opts.ReasoningTiming) == 0 || len(messages) == 0 {
		return opts
	}
	segmentIndex := 0
	for messageIndex, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		rowSegments := make([]messagepkg.ReasoningTimingSegment, 0)
		for _, part := range message.ContentParts() {
			if !strings.EqualFold(strings.TrimSpace(part.Type), "reasoning") || strings.TrimSpace(part.Text) == "" {
				continue
			}
			if segmentIndex >= len(opts.ReasoningTiming) {
				break
			}
			segment := opts.ReasoningTiming[segmentIndex]
			segment.Ordinal = len(rowSegments)
			rowSegments = append(rowSegments, segment)
			segmentIndex++
		}
		if len(rowSegments) == 0 {
			continue
		}
		if opts.MessageMetadataByIndex == nil {
			opts.MessageMetadataByIndex = make(map[int]map[string]any, 1)
		}
		existing := opts.MessageMetadataByIndex[messageIndex]
		if existing == nil {
			existing = map[string]any{}
		}
		existing[messagepkg.ReasoningTimingMetadataKey] = messagepkg.ReasoningTimingMetadata{
			Version:  messagepkg.ReasoningTimingVersion,
			Segments: rowSegments,
		}
		opts.MessageMetadataByIndex[messageIndex] = existing
	}
	return opts
}
