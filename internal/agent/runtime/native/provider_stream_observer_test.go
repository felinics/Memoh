package native

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

type stepClockTestTime struct {
	now time.Time
}

func (c *stepClockTestTime) read() time.Time { return c.now }

func (c *stepClockTestTime) advance(d time.Duration) { c.now = c.now.Add(d) }

func TestProviderStreamObserverEmitsStepTimingEvents(t *testing.T) {
	t.Parallel()

	fake := &stepClockTestTime{now: time.UnixMilli(1_000)}
	clock := newStepClock(fake.read)
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "hi"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 12, OutputTokens: 3}},
		), nil
	})
	var observed []StreamEvent
	model := modelWithProviderStreamObserver(&sdk.Model{ID: "mock", Provider: provider}, func(event StreamEvent) {
		observed = append(observed, event)
		switch event.Type {
		case EventStepStart:
			fake.advance(250 * time.Millisecond)
		case EventTextDelta:
			fake.advance(500 * time.Millisecond)
		}
	}, clock)

	result, err := model.Provider.DoStream(context.Background(), sdk.GenerateParams{})
	if err != nil {
		t.Fatalf("DoStream: %v", err)
	}
	for range result.Stream {
	}

	var start, end *StreamEvent
	for i := range observed {
		switch observed[i].Type {
		case EventStepStart:
			start = &observed[i]
		case EventStepEnd:
			end = &observed[i]
		}
	}
	if start == nil || start.Timing == nil || start.Timing.StartedAtMS != 1_000 {
		t.Fatalf("step_start = %#v, want started at 1000", start)
	}
	if end == nil || end.Timing == nil {
		t.Fatalf("step_end missing timing: %#v", observed)
	}
	if end.Timing.StartedAtMS != 1_000 || end.Timing.FirstTokenAtMS != 1_250 || end.Timing.EndedAtMS != 1_750 {
		t.Fatalf("step_end timing = %#v", end.Timing)
	}
	if end.FinishReason != string(sdk.FinishReasonStop) {
		t.Fatalf("finish reason = %q", end.FinishReason)
	}
	var usage sdk.Usage
	if err := json.Unmarshal(end.Usage, &usage); err != nil || usage.InputTokens != 12 || usage.OutputTokens != 3 {
		t.Fatalf("usage = %s (%v)", end.Usage, err)
	}
	last, ok := clock.takeCompleted()
	if !ok || last.Timing != *end.Timing {
		t.Fatalf("clock last completed = %#v, %v", last, ok)
	}
}

func TestProviderStreamObserverAbandonsAttemptWithoutFinish(t *testing.T) {
	t.Parallel()

	fake := &stepClockTestTime{now: time.UnixMilli(5_000)}
	clock := newStepClock(fake.read)
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "partial"},
		), nil
	})
	model := modelWithProviderStreamObserver(&sdk.Model{ID: "mock", Provider: provider}, nil, clock)
	result, err := model.Provider.DoStream(context.Background(), sdk.GenerateParams{})
	if err != nil {
		t.Fatalf("DoStream: %v", err)
	}
	for range result.Stream {
	}
	if _, ok := clock.takeCompleted(); ok {
		t.Fatalf("an attempt without finish-step must not complete")
	}
}

type namedStreamProvider struct {
	agentStreamTestProvider
	name string
}

func (p namedStreamProvider) Name() string { return p.name }

func TestProviderStreamObserverMakesAnthropicInputGross(t *testing.T) {
	t.Parallel()

	stream := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "hi"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{
				InputTokens:       300,
				OutputTokens:      40,
				TotalTokens:       340,
				CachedInputTokens: 30_000,
				InputTokenDetails: sdk.InputTokenDetail{CacheReadTokens: 30_000, CacheWriteTokens: 100},
			}},
		), nil
	})
	for _, tc := range []struct {
		name      string
		wantInput int
		wantTotal int
	}{{anthropicMessagesProvider, 30_400, 30_440}, {"openai-responses", 300, 340}} {
		clock := newStepClock(nil)
		var end *StreamEvent
		model := modelWithProviderStreamObserver(&sdk.Model{ID: "mock", Provider: namedStreamProvider{stream, tc.name}}, func(event StreamEvent) {
			if event.Type == EventStepEnd {
				end = &event
			}
		}, clock)
		result, err := model.Provider.DoStream(context.Background(), sdk.GenerateParams{})
		if err != nil {
			t.Fatalf("%s: DoStream: %v", tc.name, err)
		}
		for range result.Stream {
		}
		var usage sdk.Usage
		if end == nil || json.Unmarshal(end.Usage, &usage) != nil {
			t.Fatalf("%s: step_end = %#v", tc.name, end)
		}
		if usage.InputTokens != tc.wantInput || usage.TotalTokens != tc.wantTotal || usage.CachedInputTokens != 30_000 || usage.InputTokenDetails.CacheWriteTokens != 100 {
			t.Fatalf("%s: usage = %#v, want gross input %d", tc.name, usage, tc.wantInput)
		}
		completed, ok := clock.takeCompleted()
		if !ok || completed.Usage.InputTokens != tc.wantInput {
			t.Fatalf("%s: completed usage = %#v", tc.name, completed.Usage)
		}
	}
}
