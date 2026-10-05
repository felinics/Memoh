package native

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// TestFailureAfterStepBudgetCancellationStaysOffTheWire pins the budget
// fence on the engine's failure paths: a provider or local failure observed
// after the run context was cancelled with a step-budget cause must stay off
// the event wire (the segment owner publishes the context error instead) and
// must abort the engine rather than trigger a retry, even when the failure is
// one a retry would otherwise take.
func TestFailureAfterStepBudgetCancellationStaysOffTheWire(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		fail func(*streamEngine) error
	}{
		{"provider failure", func(eng *streamEngine) error {
			return eng.providerFailure(&sdk.APIError{StatusCode: 503, Kind: sdk.KindServerError})
		}},
		{"local failure", func(eng *streamEngine) error {
			eng.localFailure(errors.New("raw commit failure must not escape"))
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			streamCtx, cancel := context.WithCancelCause(context.Background())
			cancel(contextfrag.ErrProtectedContextOverflow)

			eng := &streamEngine{
				streamCtx: streamCtx,
				events:    make(chan StreamEvent, 4),
			}
			if retry := tc.fail(eng); retry != nil {
				t.Fatalf("failure = %v, want suppressed and not retried", retry)
			}
			if !eng.aborted {
				t.Fatal("failure did not abort the engine after step budget cancellation")
			}
			select {
			case ev := <-eng.events:
				t.Fatalf("failure leaked an error event: %#v", ev)
			default:
			}
			if eng.turnError != "" {
				t.Fatalf("failure recorded a turn error: %q", eng.turnError)
			}
		})
	}
}
