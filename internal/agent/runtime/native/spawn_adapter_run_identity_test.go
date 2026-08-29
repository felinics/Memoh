package native

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestSpawnRunConfigPreservesAdmittedRunID(t *testing.T) {
	const admittedRunID = "77777777-7777-4777-8777-777777777777"
	cfg := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{RunID: " \t" + admittedRunID + "\n"})

	if cfg.RunID != admittedRunID {
		t.Fatalf("RunID = %q, want admitted RunID %q", cfg.RunID, admittedRunID)
	}
}

func TestSpawnRunConfigMintsRunIDForDirectCaller(t *testing.T) {
	first := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{})
	second := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{RunID: " \t"})

	if _, err := uuid.Parse(first.RunID); err != nil {
		t.Fatalf("first RunID = %q, want minted UUID: %v", first.RunID, err)
	}
	if _, err := uuid.Parse(second.RunID); err != nil {
		t.Fatalf("second RunID = %q, want minted UUID: %v", second.RunID, err)
	}
	if first.RunID == second.RunID {
		t.Fatalf("direct callers received the same RunID %q", first.RunID)
	}
}

func TestSpawnAdapterStepCommitSharesLifecycleAndInstallsInterrupt(t *testing.T) {
	adapter := NewSpawnAdapter(newTestAgent())
	var captured *contextfrag.LifecycleHolder
	adapter.SetStepCommitFactory(func(
		_ context.Context,
		_, _, _, _ string,
		lifecycle *contextfrag.LifecycleHolder,
		_ func(),
	) (
		func(context.Context, int, *sdk.StepResult) error,
		func(context.Context, int, *sdk.StepResult) error,
	) {
		captured = lifecycle
		callback := func(context.Context, int, *sdk.StepResult) error { return nil }
		return callback, callback
	})
	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{})

	if !adapter.installStepCommit(context.Background(), tools.SpawnRunConfig{}, &rc) {
		t.Fatal("step persistence was not installed")
	}
	if captured == nil || captured != rc.ContextLifecycle {
		t.Fatalf("captured lifecycle = %p, run lifecycle = %p", captured, rc.ContextLifecycle)
	}
	if rc.OnStepCommitted == nil || rc.OnStepInterrupted == nil {
		t.Fatal("complete and interrupted step callbacks must be installed together")
	}
	captured.SetManifest(contextfrag.Manifest{
		View:   contextfrag.ViewRunConfigPreProvider,
		Counts: contextfrag.ManifestCounts{Fragments: 2},
	})
	captured.SetAssistantMessageID("assistant-1")
	snapshot, ok := rc.ContextLifecycle.Snapshot()
	if !ok || snapshot.Counts.Fragments != 2 || snapshot.AssistantMessageID != "assistant-1" {
		t.Fatalf("shared lifecycle snapshot = %#v, set = %v", snapshot, ok)
	}
}

func TestSpawnAdapterGenerateWithWatchdogCarriesLifecycleSnapshot(t *testing.T) {
	provider := &atomicMockProvider{
		handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	result, err := NewSpawnAdapter(newTestAgent()).GenerateWithWatchdog(
		context.Background(),
		tools.SpawnRunConfig{
			Model: &sdk.Model{
				ID:       "spawn-lifecycle-model",
				Provider: provider,
				Type:     sdk.ModelTypeChat,
			},
			Query:       "do the task",
			SessionType: sessionmode.Subagent,
			Identity: tools.SpawnIdentity{
				BotID:      "bot-1",
				SessionID:  "session-1",
				IsSubagent: true,
			},
		},
		func() {},
	)
	if err != nil {
		t.Fatalf("GenerateWithWatchdog error: %v", err)
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("GenerateWithWatchdog result = %#v, want lifecycle snapshot", result)
	}
	if result.ContextLifecycle.Counts.Fragments == 0 || result.ContextLifecycle.Counts.Messages == 0 {
		t.Fatalf("lifecycle counts = %+v, want assembled context", result.ContextLifecycle.Counts)
	}
}

func TestSpawnAdapterGenerateWithWatchdogClearsRecoveredStreamError(t *testing.T) {
	var streamCalls atomic.Int32
	var dispositionCalls atomic.Int32
	provider := &atomicMockProvider{
		stream: func(_ context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
			call := streamCalls.Add(1)
			parts := make(chan sdk.StreamPart, 8)
			parts <- &sdk.StartPart{}
			parts <- &sdk.StartStepPart{}
			if call == 1 {
				parts <- &sdk.ErrorPart{Error: errors.New("api error 500")}
				close(parts)
				return &sdk.StreamResult{Stream: parts}, nil
			}
			parts <- &sdk.TextStartPart{ID: "recovered"}
			parts <- &sdk.TextDeltaPart{ID: "recovered", Text: "recovered result"}
			parts <- &sdk.TextEndPart{ID: "recovered"}
			parts <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}
			parts <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
			close(parts)
			return &sdk.StreamResult{Stream: parts}, nil
		},
	}

	result, err := NewSpawnAdapter(newTestAgent()).GenerateWithWatchdog(
		context.Background(),
		tools.SpawnRunConfig{
			Model: &sdk.Model{
				ID:       "spawn-retry-model",
				Provider: provider,
				Type:     sdk.ModelTypeChat,
			},
			Query:       "recover the task",
			SessionType: sessionmode.Subagent,
			Identity: tools.SpawnIdentity{
				BotID:      "bot-1",
				SessionID:  "session-1",
				IsSubagent: true,
			},
			ResolveAttempt: func(error) tools.SpawnAttemptDisposition {
				dispositionCalls.Add(1)
				return tools.SpawnAttemptFailure
			},
		},
		func() {},
	)
	if err != nil {
		t.Fatalf("GenerateWithWatchdog retained recovered error: %v", err)
	}
	if got := streamCalls.Load(); got != 2 {
		t.Fatalf("stream calls = %d, want initial attempt plus one recovery", got)
	}
	if result == nil || result.Text != "recovered result" {
		t.Fatalf("GenerateWithWatchdog result = %#v, want recovered result", result)
	}
	if got := dispositionCalls.Load(); got != 0 {
		t.Fatalf("outer attempt resolver called %d times for recovered inner retry", got)
	}
}

func TestSpawnAdapterGenerateWithWatchdogRejectsProviderAbort(t *testing.T) {
	provider := &atomicMockProvider{
		stream: func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
			return closedAgentTestStream(
				&sdk.StartPart{},
				&sdk.StartStepPart{},
				&sdk.AbortPart{},
			), nil
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
	result, err := adapter.GenerateWithWatchdog(
		context.Background(),
		tools.SpawnRunConfig{
			Model:       &sdk.Model{ID: "spawn-abort-model", Provider: provider, Type: sdk.ModelTypeChat},
			Query:       "abort the task",
			SessionType: sessionmode.Subagent,
			Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
		},
		func() {},
	)
	if err == nil || err.Error() != "agent run aborted" {
		t.Fatalf("GenerateWithWatchdog error = %v, want generic abort cause", err)
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("GenerateWithWatchdog result = %#v, want failure lifecycle snapshot", result)
	}
	assertSpawnAbortObservedAsFailure(t, observed)
}

func TestSpawnAdapterGenerateWithWatchdogRejectsTextLoopAbort(t *testing.T) {
	repeatedChunk := strings.Repeat("abcd", 64)
	var observedCancel atomic.Bool
	provider := &atomicMockProvider{
		stream: func(ctx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
			parts := make(chan sdk.StreamPart, 16)
			go func() {
				defer close(parts)
				send := func(part sdk.StreamPart) bool {
					select {
					case <-ctx.Done():
						observedCancel.Store(true)
						return false
					case parts <- part:
						return true
					}
				}
				if !send(&sdk.StartPart{}) || !send(&sdk.StartStepPart{}) || !send(&sdk.TextStartPart{ID: "loop"}) {
					return
				}
				for range 4 {
					if !send(&sdk.TextDeltaPart{ID: "loop", Text: repeatedChunk}) {
						return
					}
				}
				select {
				case <-ctx.Done():
					observedCancel.Store(true)
					return
				case <-time.After(50 * time.Millisecond):
				}
				_ = send(&sdk.FinishPart{FinishReason: sdk.FinishReasonStop})
			}()
			return &sdk.StreamResult{Stream: parts}, nil
		},
	}
	outerCtx := context.Background()
	adapter := NewSpawnAdapter(newTestAgent())
	var observed []StreamEvent
	adapter.SetRunObserverFactory(func(context.Context) SpawnRunObserver {
		return func(event StreamEvent) SpawnRunObservation {
			observed = append(observed, event)
			return SpawnRunObservation{}
		}
	})
	result, err := adapter.GenerateWithWatchdog(
		outerCtx,
		tools.SpawnRunConfig{
			Model:         &sdk.Model{ID: "spawn-loop-model", Provider: provider, Type: sdk.ModelTypeChat},
			Query:         "loop the task",
			SessionType:   sessionmode.Subagent,
			LoopDetection: tools.SpawnLoopConfig{Enabled: true},
			Identity:      tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
		},
		func() {},
	)
	if err == nil || err.Error() != "agent run aborted" {
		t.Fatalf("GenerateWithWatchdog error = %v, want generic abort cause", err)
	}
	if outerCtx.Err() != nil {
		t.Fatalf("owning context was canceled: %v", outerCtx.Err())
	}
	if !observedCancel.Load() {
		t.Fatal("stream provider did not observe child cancellation")
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("GenerateWithWatchdog result = %#v, want failure lifecycle snapshot", result)
	}
	assertSpawnAbortObservedAsFailure(t, observed)
}

func assertSpawnAbortObservedAsFailure(t *testing.T, events []StreamEvent) {
	t.Helper()
	errorIndex, abortIndex := -1, -1
	for i, event := range events {
		switch event.Type {
		case EventError:
			if event.Error == "agent run aborted" {
				errorIndex = i
			}
		case EventAgentAbort:
			abortIndex = i
		}
	}
	if errorIndex < 0 || abortIndex <= errorIndex {
		t.Fatalf("observed events = %#v, want generic error before abort", events)
	}
}

func TestSpawnAdapterGenerateWithWatchdogPreservesOwningCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)
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
	_, err := adapter.GenerateWithWatchdog(
		ctx,
		tools.SpawnRunConfig{
			Model:       &sdk.Model{ID: "spawn-canceled-model", Provider: provider, Type: sdk.ModelTypeChat},
			Query:       "stop the task",
			SessionType: sessionmode.Subagent,
			Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
		},
		func() {},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateWithWatchdog error = %v, want owning cancellation", err)
	}
	abortSeen := false
	for _, event := range observed {
		if event.Type == EventError {
			t.Fatalf("owning cancellation published provider failure: %#v", observed)
		}
		abortSeen = abortSeen || event.Type == EventAgentAbort
	}
	if !abortSeen {
		t.Fatalf("observed events = %#v, want terminal abort", observed)
	}
}

func TestSpawnAdapterGenerateWithWatchdogTreatsOtherContextCausesAsFailures(t *testing.T) {
	wantCause := errors.New("owning run failed")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(wantCause)
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
	_, err := adapter.GenerateWithWatchdog(
		ctx,
		tools.SpawnRunConfig{
			Model:       &sdk.Model{ID: "spawn-failed-model", Provider: provider, Type: sdk.ModelTypeChat},
			Query:       "fail the task",
			SessionType: sessionmode.Subagent,
			Identity:    tools.SpawnIdentity{BotID: "bot-1", SessionID: "session-1", IsSubagent: true},
		},
		func() {},
	)
	if !errors.Is(err, wantCause) {
		t.Fatalf("GenerateWithWatchdog error = %v, want owning failure %v", err, wantCause)
	}
	assertSpawnAbortObservedAsFailure(t, observed)
}

func TestSpawnAdapterGenerateFailureCarriesLifecycleSnapshot(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	provider := &atomicMockProvider{
		handler: func(_ int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return nil, providerErr
		},
	}
	result, err := NewSpawnAdapter(newTestAgent()).Generate(
		context.Background(),
		tools.SpawnRunConfig{
			Model: &sdk.Model{
				ID:       "spawn-failure-model",
				Provider: provider,
				Type:     sdk.ModelTypeChat,
			},
			Query:       "do the task",
			SessionType: sessionmode.Subagent,
			Identity: tools.SpawnIdentity{
				BotID:      "bot-1",
				SessionID:  "session-1",
				IsSubagent: true,
			},
		},
	)
	if err == nil {
		t.Fatal("Generate error = nil, want provider failure")
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("Generate result = %#v, want failure lifecycle snapshot", result)
	}
	if result.ContextLifecycle.Counts.Fragments == 0 || result.ContextLifecycle.Counts.Messages == 0 {
		t.Fatalf("failure lifecycle counts = %+v, want assembled context", result.ContextLifecycle.Counts)
	}
}
