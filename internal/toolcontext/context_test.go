package toolcontext

import (
	"context"
	"errors"
	"testing"
	"time"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

type ctxKey string

func TestBindUsesRunContextAsValueAuthority(t *testing.T) {
	key := ctxKey("tenant")
	runCtx := context.WithValue(context.Background(), key, "team-1")
	callbackCtx := context.WithValue(context.Background(), key, "transport-only")

	bound, cancel := Bind(callbackCtx, Session{RunContext: runCtx})
	defer cancel()

	if got := bound.Value(key); got != "team-1" {
		t.Fatalf("bound value = %v, want run context value", got)
	}
}

func TestBindStopsWhenCallbackContextIsCanceled(t *testing.T) {
	callbackCtx, cancelCallback := context.WithCancel(context.Background())
	bound, cancel := Bind(callbackCtx, Session{RunContext: context.Background()})
	defer cancel()

	cancelCallback()
	select {
	case <-bound.Done():
	case <-time.After(time.Second):
		t.Fatal("bound context was not canceled after callback cancellation")
	}
}

func TestBindStopsWhenOwningRunIsCanceled(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	bound, cancel := Bind(context.Background(), Session{RunContext: runCtx})
	defer cancel()

	cancelRun()
	select {
	case <-bound.Done():
	case <-time.After(time.Second):
		t.Fatal("bound context was not canceled after run cancellation")
	}
}

func TestBindWithAlreadyCanceledCallbackContext(t *testing.T) {
	callbackCtx, cancelCallback := context.WithCancel(context.Background())
	cancelCallback()
	bound, cancel := Bind(callbackCtx, Session{RunContext: context.Background()})
	defer cancel()

	if bound.Err() == nil {
		t.Fatal("bound context should already be canceled")
	}
}

func TestValidateRuntimeGuardRejectsCancellationDuringGuard(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	session := Session{RunContext: runCtx}
	bound, cancelBound := Bind(context.Background(), session)
	defer cancelBound()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		session.RuntimeGuard = func(context.Context) error {
			close(started)
			<-release
			return nil
		}
		done <- ValidateRuntimeGuard(bound, session)
	}()
	<-started
	cancelRun()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runtime guard error = %v, want context.Canceled", err)
	}
}

func TestMergePreservesStickyCapabilities(t *testing.T) {
	base := Session{BotID: "bot-1"}
	merged := Merge(base, Session{
		SupportsImageInput:  true,
		SupportsFileInput:   true,
		CanRequestUserInput: true,
	})
	if !merged.SupportsImageInput {
		t.Fatalf("SupportsImageInput = false, want true")
	}
	if !merged.SupportsFileInput {
		t.Fatalf("SupportsFileInput = false, want true")
	}
	if !merged.CanRequestUserInput {
		t.Fatalf("CanRequestUserInput = false, want true")
	}
}

func TestMergeCarriesReasoningIntent(t *testing.T) {
	t.Parallel()

	merged := Merge(Session{}, Session{
		ReasoningStoredEffort:    " high ",
		ReasoningRequestedEffort: " disable ",
	})
	if merged.ReasoningStoredEffort != "high" || merged.ReasoningRequestedEffort != "disable" {
		t.Fatalf("reasoning intent = stored %q, requested %q",
			merged.ReasoningStoredEffort, merged.ReasoningRequestedEffort)
	}
}

func TestMergePreservesRuntimeLifecycle(t *testing.T) {
	runCtx := context.Background()
	guard := func(context.Context) error { return nil }
	merged := Merge(Session{BotID: "bot-1"}, Session{
		RunContext: runCtx, RuntimeGuard: guard,
	})
	if merged.RunContext != runCtx || merged.RuntimeGuard == nil {
		t.Fatalf("runtime lifecycle = context:%v guard:%v", merged.RunContext, merged.RuntimeGuard != nil)
	}
}

func TestMergeCarriesContextBudgetAndToolExchangePolicy(t *testing.T) {
	t.Parallel()

	policy := &contextfrag.ToolExchangePolicy{}
	merged := Merge(Session{BotID: "bot-1"}, Session{
		ContextBudgetMaxTokens:    128000,
		ContextToolExchangePolicy: policy,
	})
	if merged.ContextBudgetMaxTokens != 128000 || merged.ContextToolExchangePolicy != policy {
		t.Fatalf("merged = %#v, want context budget and policy carried", merged)
	}
	kept := Merge(merged, Session{SessionID: "session-1"})
	if kept.ContextBudgetMaxTokens != 128000 || kept.ContextToolExchangePolicy != policy {
		t.Fatalf("kept = %#v, want context budget and policy preserved", kept)
	}
}
