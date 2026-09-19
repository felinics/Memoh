package native

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

var errModelSteered = errors.New("model invocation steered")

// The commit callback and PrepareStep run serially on the SDK loop. Reading
// here avoids a sender/forwarder race with an immediately following tool step.
func prepareQueuedSteer(prepare func(*sdk.GenerateParams) *sdk.GenerateParams, cfg RunConfig) func(*sdk.GenerateParams) *sdk.GenerateParams {
	if cfg.NextModelInputs == nil {
		return prepare
	}
	return func(params *sdk.GenerateParams) *sdk.GenerateParams {
		if prepare != nil {
			if override := prepare(params); override != nil {
				params = override
			}
		}
		if len(*cfg.NextModelInputs) > 0 {
			cfg.ContextMutations.Record(contextfrag.MutationInjectedMessage, fmt.Sprintf("messages=%d", len(*cfg.NextModelInputs)))
			params.Messages = append(params.Messages, *cfg.NextModelInputs...)
			*cfg.NextModelInputs = nil
		}
		return params
	}
}

// modelSteerGate serializes interruption with provider output BEFORE the SDK
// can execute tools or commit a completed step. Consumer-side stream flags are
// too late: provider events may already be buffered ahead of the UI consumer.
type modelSteerGate struct {
	mu       sync.Mutex
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
		pending, err := cfg.PendingSteer(ctx)
		if err != nil {
			if ctx.Err() == nil {
				a.logger.WarnContext(ctx, "check pending steer failed", slog.Any("error", err))
			}
			continue
		}
		if pending && gate.interrupt() {
			return
		}
	}
}

// SDK output omits inputs inserted by PrepareStep. Rebuild the committed
// transcript with their admitted provenance so a later steer cannot forget
// an earlier steer/read_media input. Initial inputs already live in cfg.Messages.
func steerContinuationMessages(cfg RunConfig, steps []sdk.StepResult, capture *stepMessageCapture) []sdk.Message {
	var messages []sdk.Message
	for i, step := range steps {
		inputs := capture.messages(i)
		if i == 0 {
			inputs = inputs[min(len(cfg.initialStepInputs), len(inputs)):]
		}
		messages = append(messages, inputs...)
		messages = append(messages, step.Messages...)
	}
	return messages
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
		*sdk.StreamToolCallPart, *sdk.FinishStepPart, *sdk.ErrorPart, *sdk.AbortPart:
		g.sampling = false
	}
	return true
}

func (g *modelSteerGate) interrupt() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.sampling || g.stopped {
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
