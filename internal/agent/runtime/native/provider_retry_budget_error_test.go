package native

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// delayedErrContext models cancellation becoming visible after the retry
// loop's ctx.Err check but before it handles the provider's ErrorPart. The
// first Err call is the retry preflight and the second is the loop guard;
// the third call is the ErrorPart budget check.
type delayedErrContext struct {
	context.Context
	errCalls atomic.Int32
}

func (c *delayedErrContext) Err() error {
	if c.errCalls.Add(1) <= 2 {
		return nil
	}
	return c.Context.Err()
}

func (*delayedErrContext) Done() <-chan struct{} {
	return nil
}

func TestRunMidStreamRetrySuppressesRawErrorAfterStepBudgetCancellation(t *testing.T) {
	t.Parallel()

	baseCtx, cancel := context.WithCancelCause(context.Background())
	streamCtx := &delayedErrContext{Context: baseCtx}
	var providerCalls atomic.Int32
	provider := &atomicMockProvider{
		stream: func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
			providerCalls.Add(1)
			cancel(contextfrag.ErrProtectedContextOverflow)
			parts := make(chan sdk.StreamPart, 1)
			parts <- &sdk.ErrorPart{Error: errors.New("raw provider cancellation must not escape")}
			close(parts)
			return &sdk.StreamResult{Stream: parts}, nil
		},
	}
	ledger := contextfrag.NewMutationLedger()
	cfg := captureProviderAttemptPrefix(RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
	})
	events := make(chan StreamEvent, 16)

	_, aborted := New(Deps{}).runMidStreamRetry(
		context.Background(),
		streamCtx,
		cancel,
		newToolAbortRegistry(),
		events,
		cfg,
		nil,
		nil,
		nil,
		&sdk.StreamResult{},
		&stepMessageCapture{},
		nil,
		&interruptedStepCapture{},
		0,
		"api error 500",
		&strings.Builder{},
		nil,
	)
	close(events)

	if !aborted {
		t.Fatal("runMidStreamRetry() did not abort after step budget cancellation")
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want the retry call that observed cancellation", got)
	}
	if !errors.Is(context.Cause(streamCtx), contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("stream cause = %v, want protected overflow", context.Cause(streamCtx))
	}
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("retry leaked raw provider error event: %#v", event)
		}
	}
}
