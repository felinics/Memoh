package native

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentevent "github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestSpawnAdapterGenerateWithWatchdogKeepsRetryableAttemptNonTerminal(t *testing.T) {
	causes := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		err  error
	}{
		{
			name: "watchdog",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancelCause(context.Background())
				cancel(tools.ErrWatchdogTimedOut)
				return ctx, func() {}
			},
			err: tools.ErrWatchdogTimedOut,
		},
		{
			name: "safety deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			err: context.DeadlineExceeded,
		},
	}
	for _, tc := range causes {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			provider := &atomicMockProvider{
				handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
					return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
				},
			}
			adapter := NewSpawnAdapter(newTestAgent())
			var observed []StreamEvent
			adapter.SetRunObserverFactory(func(context.Context) SpawnRunObserver {
				return func(event StreamEvent) SpawnRunObservation {
					observed = append(observed, event)
					return SpawnRunObservation{}
				}
			})
			_, err := adapter.GenerateWithWatchdog(ctx, tools.SpawnRunConfig{
				Model:       &sdk.Model{ID: "spawn-retry-model", Provider: provider, Type: sdk.ModelTypeChat},
				Query:       "retry the task",
				SessionType: sessionmode.Subagent,
				Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
				Attempt:     1,
				MaxAttempts: 4,
				ResolveAttempt: func(got error) tools.SpawnAttemptDisposition {
					if errors.Is(got, tc.err) {
						return tools.SpawnAttemptRetry
					}
					return tools.SpawnAttemptFailure
				},
			}, func() {})
			if !errors.Is(err, tc.err) {
				t.Fatalf("GenerateWithWatchdog error = %v, want %v", err, tc.err)
			}
			assertSpawnAttemptObservedAsRetry(t, observed, 1, 4)
		})
	}
}

func TestSpawnAdapterGenerateWithWatchdogMakesFinalWatchdogAttemptTerminal(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(tools.ErrWatchdogTimedOut)
	provider := &atomicMockProvider{
		handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	adapter := NewSpawnAdapter(newTestAgent())
	var observed []StreamEvent
	var dispositionCalls int
	adapter.SetRunObserverFactory(func(context.Context) SpawnRunObserver {
		return func(event StreamEvent) SpawnRunObservation {
			observed = append(observed, event)
			return SpawnRunObservation{}
		}
	})
	_, err := adapter.GenerateWithWatchdog(ctx, tools.SpawnRunConfig{
		Model:       &sdk.Model{ID: "spawn-final-watchdog-model", Provider: provider, Type: sdk.ModelTypeChat},
		Query:       "finish the task",
		SessionType: sessionmode.Subagent,
		Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
		Attempt:     4,
		MaxAttempts: 4,
		ResolveAttempt: func(got error) tools.SpawnAttemptDisposition {
			dispositionCalls++
			if !errors.Is(got, tools.ErrWatchdogTimedOut) {
				t.Errorf("attempt error = %v, want watchdog timeout", got)
			}
			return tools.SpawnAttemptFailure
		},
	}, func() {})
	if !errors.Is(err, tools.ErrWatchdogTimedOut) {
		t.Fatalf("GenerateWithWatchdog error = %v, want watchdog timeout", err)
	}
	if dispositionCalls != 1 {
		t.Fatalf("attempt resolver calls = %d, want 1", dispositionCalls)
	}
	assertSpawnAbortObservedAsFailure(t, observed)
	for _, event := range observed {
		if event.Type == EventRetry {
			t.Fatalf("final watchdog attempt published retry: %#v", observed)
		}
	}
}

func TestSpawnAdapterGenerateWithWatchdogDoesNotRetryPersistedInterruptedCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	provider := &atomicMockProvider{
		stream: func(streamCtx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
			parts := make(chan sdk.StreamPart)
			go func() {
				defer close(parts)
				for _, part := range []sdk.StreamPart{
					&sdk.StartPart{},
					&sdk.StartStepPart{},
					&sdk.TextStartPart{ID: "partial"},
					&sdk.TextDeltaPart{ID: "partial", Text: "durable partial"},
				} {
					select {
					case <-streamCtx.Done():
						return
					case parts <- part:
					}
				}
				<-streamCtx.Done()
			}()
			return &sdk.StreamResult{Stream: parts}, nil
		},
	}
	adapter := NewSpawnAdapter(newTestAgent())
	var persisted atomic.Bool
	var interruptedCalls atomic.Int32
	adapter.SetStepCommitFactory(func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		_ string,
		_ *contextfrag.LifecycleHolder,
		onPersisted func(),
	) (func(context.Context, int, *sdk.StepResult) error, func(context.Context, int, *sdk.StepResult) error) {
		return func(context.Context, int, *sdk.StepResult) error { return nil },
			func(context.Context, int, *sdk.StepResult) error {
				interruptedCalls.Add(1)
				onPersisted()
				return nil
			}
	})
	var observed []StreamEvent
	adapter.SetRunObserverFactory(func(context.Context) SpawnRunObserver {
		return func(event StreamEvent) SpawnRunObservation {
			observed = append(observed, event)
			return SpawnRunObservation{}
		}
	})
	var touches atomic.Int32
	_, err := adapter.GenerateWithWatchdog(ctx, tools.SpawnRunConfig{
		Model:       &sdk.Model{ID: "spawn-interrupted-model", Provider: provider, Type: sdk.ModelTypeChat},
		Query:       "preserve interrupted output",
		SessionType: sessionmode.Subagent,
		Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
		Attempt:     1,
		MaxAttempts: 4,
		OnStepPersisted: func() {
			persisted.Store(true)
		},
		ResolveAttempt: func(error) tools.SpawnAttemptDisposition {
			if persisted.Load() {
				return tools.SpawnAttemptFailure
			}
			return tools.SpawnAttemptRetry
		},
	}, func() {
		if touches.Add(1) == 3 {
			cancel(tools.ErrWatchdogTimedOut)
		}
	})
	if !errors.Is(err, tools.ErrWatchdogTimedOut) {
		t.Fatalf("GenerateWithWatchdog error = %v, want watchdog timeout", err)
	}
	if interruptedCalls.Load() != 1 || !persisted.Load() {
		t.Fatalf("interrupted persistence = calls %d, persisted %v", interruptedCalls.Load(), persisted.Load())
	}
	assertSpawnAbortObservedAsFailure(t, observed)
	for _, event := range observed {
		if event.Type == EventRetry {
			t.Fatalf("persisted interrupted attempt published retry: %#v", observed)
		}
	}
}

func assertSpawnAttemptObservedAsRetry(t *testing.T, events []StreamEvent, attempt, maxAttempts int) {
	t.Helper()
	errorIndex, retryIndex := -1, -1
	for i, event := range events {
		switch event.Type {
		case EventError:
			if event.Error == "agent run aborted" {
				errorIndex = i
			}
		case EventAgentAbort:
			t.Fatalf("retryable attempt published terminal abort: %#v", events)
		case EventRetry:
			if event.Attempt == attempt && event.MaxAttempt == maxAttempts {
				retryIndex = i
			}
		}
	}
	if errorIndex < 0 || retryIndex <= errorIndex {
		t.Fatalf("observed events = %#v, want generic error followed by retry", events)
	}
}

func TestObserveSpawnAttemptFailurePreservesProviderError(t *testing.T) {
	providerErr := errors.New("api error 500: provider unavailable")
	abort := &StreamEvent{Type: EventAgentAbort}
	tests := []struct {
		name        string
		disposition tools.SpawnAttemptDisposition
		wantType    agentevent.StreamEventType
		wantRetry   string
	}{
		{name: "retry", disposition: tools.SpawnAttemptRetry, wantType: EventRetry, wantRetry: providerErr.Error()},
		{name: "final", disposition: tools.SpawnAttemptFailure, wantType: EventAgentAbort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var observed []StreamEvent
			observeSpawnAttemptFailure(
				context.Background(),
				func(event StreamEvent) SpawnRunObservation {
					observed = append(observed, event)
					return SpawnRunObservation{}
				},
				tools.SpawnRunConfig{
					Attempt:        1,
					MaxAttempts:    2,
					ResolveAttempt: func(error) tools.SpawnAttemptDisposition { return tc.disposition },
				},
				abort,
				[]StreamEvent{{Type: EventError, Error: providerErr.Error()}},
				providerErr.Error(),
				providerErr,
			)
			if len(observed) != 2 || observed[0].Type != EventError || observed[1].Type != tc.wantType {
				t.Fatalf("observed events = %#v, want provider error then %q", observed, tc.wantType)
			}
			if observed[0].Error != providerErr.Error() || observed[1].RetryError != tc.wantRetry {
				t.Fatalf("provider/retry errors = %q/%q, want %q/%q", observed[0].Error, observed[1].RetryError, providerErr, tc.wantRetry)
			}
		})
	}
}

func TestObserveSpawnAttemptFailureMakesOwningCancellationAuthoritative(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)
	abort := &StreamEvent{
		Type:     EventAgentAbort,
		Messages: []byte(`[{"role":"assistant"}]`),
		Usage:    []byte(`{"output_tokens":7}`),
	}
	var observed []StreamEvent
	var dispositionCalls int
	observeSpawnAttemptFailure(
		ctx,
		func(event StreamEvent) SpawnRunObservation {
			observed = append(observed, event)
			return SpawnRunObservation{}
		},
		tools.SpawnRunConfig{ResolveAttempt: func(error) tools.SpawnAttemptDisposition {
			dispositionCalls++
			return tools.SpawnAttemptAbort
		}},
		abort,
		[]StreamEvent{{Type: EventError, Error: "private provider detail"}},
		"private provider detail",
		context.Canceled,
	)
	if dispositionCalls != 1 {
		t.Fatalf("attempt resolver calls = %d, want 1", dispositionCalls)
	}
	if len(observed) != 1 || observed[0].Type != EventAgentAbort ||
		string(observed[0].Messages) != string(abort.Messages) || string(observed[0].Usage) != string(abort.Usage) {
		t.Fatalf("observed events = %#v, want exact abort without raced error", observed)
	}
}

func TestObserveSpawnAttemptFailureResolvesWithoutObserver(t *testing.T) {
	var calls int
	observeSpawnAttemptFailure(
		context.Background(),
		nil,
		tools.SpawnRunConfig{ResolveAttempt: func(error) tools.SpawnAttemptDisposition {
			calls++
			return tools.SpawnAttemptFailure
		}},
		nil,
		nil,
		"",
		errors.New("failed before streaming"),
	)
	if calls != 1 {
		t.Fatalf("attempt resolver calls = %d, want 1 without observer", calls)
	}
}

func TestSpawnAdapterArbitratesCleanEndBeforePublishingTerminal(t *testing.T) {
	tests := []struct {
		name        string
		disposition tools.SpawnAttemptDisposition
		wantErr     error
		wantEvent   agentevent.StreamEventType
	}{
		{
			name:        "completed",
			disposition: tools.SpawnAttemptCompleted,
			wantEvent:   EventAgentEnd,
		},
		{
			name:        "stop won",
			disposition: tools.SpawnAttemptAbort,
			wantErr:     context.Canceled,
			wantEvent:   EventAgentAbort,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := &atomicMockProvider{
				handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
					return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
				},
			}
			adapter := NewSpawnAdapter(newTestAgent())
			var observed []StreamEvent
			adapter.SetRunObserverFactory(func(context.Context) SpawnRunObserver {
				return func(event StreamEvent) SpawnRunObservation {
					observed = append(observed, event)
					return SpawnRunObservation{}
				}
			})
			var resolutionCalls int
			_, err := adapter.GenerateWithWatchdog(context.Background(), tools.SpawnRunConfig{
				Model:       &sdk.Model{ID: "spawn-terminal-model", Provider: provider, Type: sdk.ModelTypeChat},
				Query:       "finish the task",
				SessionType: sessionmode.Subagent,
				Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
				ResolveCompletion: func() tools.SpawnAttemptDisposition {
					resolutionCalls++
					return tc.disposition
				},
			}, func() {})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("GenerateWithWatchdog error = %v, want %v", err, tc.wantErr)
			}
			if resolutionCalls != 1 {
				t.Fatalf("completion resolver calls = %d, want 1", resolutionCalls)
			}
			var terminals []agentevent.StreamEventType
			for _, event := range observed {
				if event.Type == EventAgentEnd || event.Type == EventAgentAbort {
					terminals = append(terminals, event.Type)
				}
			}
			if len(terminals) != 1 || terminals[0] != tc.wantEvent {
				t.Fatalf("terminal events = %v, want only %s", terminals, tc.wantEvent)
			}
		})
	}
}
