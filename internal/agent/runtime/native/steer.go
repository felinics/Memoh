package native

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	sdk "github.com/felinics/twilight/sdk"
)

var errModelSteered = errors.New("model invocation steered")

// modelSteerGate serializes interruption with provider output BEFORE the loop
// can execute tools or commit a completed step. Consumer-side stream flags are
// too late: provider events may already be buffered ahead of the UI consumer.
//
// The gate covers one model call at a time. arm points it at that call's
// cancellation, begin opens the sampling window, and observe closes the window
// at the first part that signals tools or a finished step.
type modelSteerGate struct {
	mu sync.Mutex
	// call identifies the armed model call. A pending-steer probe spans a call
	// boundary, so the watcher carries the generation it probed for and a stale
	// decision is refused instead of cancelling the call that replaced it.
	call     uint64
	sampling bool
	stopped  bool
	cancel   context.CancelCauseFunc
	ready    chan struct{}
}

// Notifications must remain live while the main consumer is inside retry
// handling as well as its normal stream loop. This worker owns no input or
// history state and stops with this invocation's context.
func (a *Agent) watchSteer(ctx context.Context, cfg RunConfig, gate *modelSteerGate) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-cfg.SteerWake:
		case <-gate.ready:
		}
		call := gate.generation()
		pending, err := cfg.PendingSteer(ctx)
		if err != nil {
			if ctx.Err() == nil {
				a.logger.WarnContext(ctx, "check pending steer failed", slog.Any("error", err))
			}
			continue
		}
		if pending {
			// An interrupted call is checkpointed and the loop continues in
			// place, so the watcher stays for the calls that follow. The next
			// arm/begin pair reopens the window and republishes ready.
			gate.interrupt(call)
		}
	}
}

// arm binds the gate to the model call that is about to start. Each call owns
// its own cancellation, so a steer stops sampling without ending the run.
func (g *modelSteerGate) arm(cancel context.CancelCauseFunc) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.call++
	g.cancel = cancel
	g.sampling = false
	g.stopped = false
	// The previous call may have left an unread notification behind. It carries
	// no generation, so drop it rather than spend a probe on it.
	select {
	case <-g.ready:
	default:
	}
}

// generation reports the armed call a pending-steer probe is about to run for.
func (g *modelSteerGate) generation() uint64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.call
}

func (g *modelSteerGate) begin() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sampling = !g.stopped
	select {
	case g.ready <- struct{}{}:
	default:
	}
}

func (g *modelSteerGate) observe(part sdk.StreamPart) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return false
	}
	switch part.(type) {
	case *sdk.ToolInputStartPart, *sdk.ToolInputDeltaPart, *sdk.ToolInputEndPart,
		*sdk.StreamToolCallPart, *sdk.FinishStepPart, *sdk.ErrorPart:
		g.sampling = false
	}
	return true
}

func (g *modelSteerGate) interrupt(call uint64) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.call != call || !g.sampling || g.stopped || g.cancel == nil {
		return false
	}
	g.stopped = true
	g.cancel(errModelSteered)
	return true
}

// An unfinished reasoning block can lack the provider's final signature.
// Keep its text as a checkpoint, never replay opaque/incomplete reasoning.
func steerCheckpointMessages(messages []sdk.Message) []sdk.Message {
	result := make([]sdk.Message, 0, len(messages))
	for _, message := range cloneProviderMessages(messages) {
		var parts []sdk.MessagePart
		for _, part := range message.Content {
			switch p := part.(type) {
			case sdk.ReasoningPart:
				if p.Text != "" {
					parts = append(parts, sdk.TextPart{Text: "[Interrupted reasoning checkpoint]\n" + p.Text})
				}
			case *sdk.ReasoningPart:
				if p.Text != "" {
					parts = append(parts, sdk.TextPart{Text: "[Interrupted reasoning checkpoint]\n" + p.Text})
				}
			default:
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			message.Content = parts
			result = append(result, message)
		}
	}
	return result
}
