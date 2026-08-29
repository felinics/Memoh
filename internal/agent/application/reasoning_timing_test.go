package application

import (
	"context"
	"log/slog"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type reasoningTimingTestClock struct {
	now time.Time
}

func newReasoningTimingTestClock() *reasoningTimingTestClock {
	return &reasoningTimingTestClock{now: time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC)}
}

func (c *reasoningTimingTestClock) read() time.Time {
	return c.now
}

func (c *reasoningTimingTestClock) advance(duration time.Duration) {
	c.now = c.now.Add(duration)
}

func TestReasoningTimingTrackerMeasuresExplicitBlock(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newReasoningTimingTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningStart})
	clock.advance(400 * time.Millisecond)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "inspect"})
	clock.advance(1600 * time.Millisecond)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningEnd})

	segments := tracker.take("completed")
	if len(segments) != 1 {
		t.Fatalf("segments = %#v, want one", segments)
	}
	got := segments[0]
	if got.DurationMS != 2000 || got.State != "completed" {
		t.Fatalf("segment duration/state = %#v", got)
	}
}

func TestReasoningTimingTrackerDeltaOnlyFallback(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newReasoningTimingTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "plan"})
	clock.advance(1250 * time.Millisecond)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: " more"})
	clock.advance(750 * time.Millisecond)
	tracker.observe(native.StreamEvent{Type: native.EventTextDelta, Delta: "answer"})

	segments := tracker.take("completed")
	if len(segments) != 1 {
		t.Fatalf("segments = %#v, want one", segments)
	}
	if got := segments[0]; got.DurationMS != 2000 || got.State != "completed" {
		t.Fatalf("delta-only segment = %#v", got)
	}
}

func TestReasoningTimingTrackerRetryKeepsOnlySurvivingAttempt(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newReasoningTimingTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "discarded"})
	clock.advance(time.Second)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningEnd})
	tracker.observe(native.StreamEvent{Type: native.EventRetry})
	clock.advance(time.Second)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "survives"})
	clock.advance(3 * time.Second)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningEnd})

	segments := tracker.take("completed")
	if len(segments) != 1 {
		t.Fatalf("segments = %#v, want surviving attempt only", segments)
	}
	if got := segments[0]; got.DurationMS != 3000 {
		t.Fatalf("surviving segment = %#v", got)
	}
}

func TestConfigureNativeReasoningTimingKeepsCompletedStepsAcrossProviderCalls(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newReasoningTimingTracker(clock.read)
	cfg := native.RunConfig{}
	configureNativeReasoningTiming(&cfg, tracker, nil)

	cfg.OnProviderStreamEventObserved(native.StreamEvent{Type: native.EventRetry})
	cfg.OnProviderStreamEventObserved(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "first step"})
	clock.advance(2 * time.Second)
	if err := cfg.OnStepCommitted(context.Background(), 0, &sdk.StepResult{}); err != nil {
		t.Fatalf("commit first timing checkpoint: %v", err)
	}

	cfg.OnProviderStreamEventObserved(native.StreamEvent{Type: native.EventRetry})
	cfg.OnProviderStreamEventObserved(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "second step"})
	clock.advance(3 * time.Second)
	if err := cfg.OnStepCommitted(context.Background(), 1, &sdk.StepResult{}); err != nil {
		t.Fatalf("commit second timing checkpoint: %v", err)
	}

	segments := tracker.take("completed")
	if len(segments) != 2 {
		t.Fatalf("segments = %#v, want both completed steps", segments)
	}
	if segments[0].DurationMS != 2000 || segments[1].DurationMS != 3000 {
		t.Fatalf("multi-step durations = %#v", segments)
	}
}

func TestReasoningTimingTrackerMarksOpenSegmentInterrupted(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newReasoningTimingTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "partial"})
	clock.advance(2300 * time.Millisecond)

	segments := tracker.take("interrupted")
	if len(segments) != 1 {
		t.Fatalf("segments = %#v, want one", segments)
	}
	if got := segments[0]; got.DurationMS != 2300 || got.State != "interrupted" {
		t.Fatalf("interrupted segment = %#v", got)
	}
}

func TestReasoningTimingTrackerOmitsUnmeasuredEmptyReasoning(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newReasoningTimingTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningStart})
	clock.advance(time.Second)
	tracker.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "  "})
	tracker.observe(native.StreamEvent{Type: native.EventReasoningEnd})
	if got := tracker.take("completed"); len(got) != 0 {
		t.Fatalf("empty reasoning timing = %#v, want absent", got)
	}
}

func TestStoreRoundAttachesReasoningTimingAfterPersistedUserIsSkipped(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	service := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}
	assistant := sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.ReasoningPart{Text: "thinking"},
			sdk.TextPart{Text: "answer"},
		},
	}
	round := append([]ModelMessage{{Role: "user", Content: newTextContent("hello")}}, sdkMessagesToModelMessages([]sdk.Message{assistant})...)
	segment := messagepkg.ReasoningTimingSegment{
		DurationMS: 2000,
		State:      "completed",
	}

	_, err := service.storeRoundWithOptionsResult(t.Context(), ChatRequest{
		BotID:                "bot-1",
		ThreadID:             "session-1",
		Query:                "hello",
		UserMessagePersisted: true,
	}, round, "model-1", storeRoundOptions{ReasoningTiming: []messagepkg.ReasoningTimingSegment{segment}})
	if err != nil {
		t.Fatalf("storeRoundWithOptionsResult() error = %v", err)
	}
	if len(messages.persisted) != 1 || messages.persisted[0].Role != "assistant" {
		t.Fatalf("persisted = %#v, want one assistant row", messages.persisted)
	}
	segments := messagepkg.ReasoningTimingFromMetadata(messages.persisted[0].Metadata)
	if len(segments) != 1 || segments[0].DurationMS != 2000 || segments[0].Ordinal != 0 {
		t.Fatalf("persisted reasoning timing = %#v", segments)
	}
}
