package native

import (
	"context"
	"errors"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// TestStreamFailureSuppressesRawErrorAfterStepBudgetCancellation pins the
// budget fence on the engine's failure path: a provider error observed after
// the run context was cancelled with a step-budget cause must stay off the
// event wire (the segment owner publishes the stable public context error
// instead) and must abort the engine rather than trigger a retry.
func TestStreamFailureSuppressesRawErrorAfterStepBudgetCancellation(t *testing.T) {
	t.Parallel()

	streamCtx, cancel := context.WithCancelCause(context.Background())
	cancel(contextfrag.ErrProtectedContextOverflow)

	eng := &streamEngine{
		streamCtx: streamCtx,
		events:    make(chan StreamEvent, 4),
	}
	msg, retriable := eng.streamFailure(errors.New("raw provider cancellation must not escape"))

	if msg != "" || retriable {
		t.Fatalf("streamFailure() = (%q, %t), want suppressed and non-retryable", msg, retriable)
	}
	if !eng.aborted {
		t.Fatal("streamFailure() did not abort the engine after step budget cancellation")
	}
	select {
	case ev := <-eng.events:
		t.Fatalf("streamFailure() leaked raw provider error event: %#v", ev)
	default:
	}
	if eng.turnError != "" {
		t.Fatalf("streamFailure() recorded raw provider error as turn error: %q", eng.turnError)
	}
}
