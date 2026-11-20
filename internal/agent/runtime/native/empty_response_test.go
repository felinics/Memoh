package native

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
)

func TestAgentStreamFailsOnEmptyFirstStep(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(_ int, _ sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{
				FinishReason:    sdk.FinishReasonStop,
				RawFinishReason: "end_turn",
				Usage:           sdk.Usage{InputTokens: 4000, OutputTokens: 0},
			}, nil
		},
	}

	var committed int
	a := New(Deps{})
	var events []StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-empty", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("hello")},
		Identity: SessionContext{BotID: "bot-1"},
		OnStepCommitted: func(_ context.Context, _ int, _ *step.Record) (StepDirective, error) {
			committed++
			return StepDirective{}, nil
		},
	}) {
		events = append(events, event)
	}

	if committed != 0 {
		t.Fatalf("OnStepCommitted calls = %d, want 0 for empty first step", committed)
	}
	var sawEmptyErr bool
	var terminal StreamEvent
	for _, ev := range events {
		if ev.Type == EventError && errors.Is(ev.Cause, ErrEmptyResponse) {
			sawEmptyErr = true
		}
		if ev.IsTerminal() {
			terminal = ev
		}
	}
	if !sawEmptyErr {
		t.Fatalf("events = %#v, want EventError wrapping ErrEmptyResponse", events)
	}
	if terminal.Type != EventAgentAbort {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentAbort)
	}
}

func TestAgentGenerateFailsOnEmptyFirstStep(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(_ int, _ sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{
				FinishReason:    sdk.FinishReasonStop,
				RawFinishReason: "end_turn",
			}, nil
		},
	}

	a := New(Deps{})
	_, err := a.Generate(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-empty", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("hello")},
		Identity: SessionContext{BotID: "bot-1"},
	})
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("Generate() err = %v, want ErrEmptyResponse", err)
	}
}
