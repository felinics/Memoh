package native

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// A mid-stream retry regenerates the answer from the last committed boundary.
// The repeated-text detector must start over with it: the failed attempt's
// text is not part of the new answer, and carrying its windows over would
// abort a regenerated answer that merely repeats the discarded one.
func TestMidStreamRetryResetsTextLoopGuard(t *testing.T) {
	t.Parallel()

	// Three identical probe windows leave the guard one hit short of its
	// streak threshold; the retried attempt repeats them and finishes.
	window := strings.Repeat("the same words over and over ", 10)[:LoopDetectedProbeChars]
	repeated := strings.Repeat(window, LoopDetectedStreakThreshold)
	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		func(ch chan<- sdk.StreamPart) {
			ch <- &sdk.StartPart{}
			ch <- &sdk.StartStepPart{}
			ch <- &sdk.TextStartPart{ID: "t"}
			ch <- &sdk.TextDeltaPart{ID: "t", Text: repeated}
			ch <- &sdk.ErrorPart{Error: errors.New("unexpected EOF")}
		},
		scriptText(repeated),
	)
	a := New(Deps{})
	var events []StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		LoopDetection:    LoopDetectionConfig{Enabled: true},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            RetryConfig{MaxAttempts: 2, FastAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}) {
		events = append(events, ev)
	}
	if invocations.Load() != 2 {
		t.Fatalf("provider invocations = %d, want the failed attempt and its retry", invocations.Load())
	}
	terminal := events[len(events)-1]
	if terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q (%s), want %q: the retried answer tripped the guard the failed attempt had primed", terminal.Type, terminal.Error, EventAgentEnd)
	}
}
