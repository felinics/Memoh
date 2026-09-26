package native

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/hooks"
)

func TestAgentStreamRequestsContextRecomposeWithoutPublicFailure(t *testing.T) {
	provider := &atomicMockProvider{}
	agent := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, ErrContextRecompose
	}})
	var events []StreamEvent
	for event := range agent.Stream(t.Context(), RunConfig{
		Model:    &sdk.Model{ID: "model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("current")},
	}) {
		events = append(events, event)
	}
	if len(events) != 1 || events[0].Type != StreamEventType("context_recompose") || events[0].Error != "" {
		t.Fatalf("recompose events = %#v, want one control event without a public error", events)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("provider called %d times before recomposition", provider.calls.Load())
	}
}

func TestAgentContextRecomposeDoesNotRunTerminalHooks(t *testing.T) {
	for _, mode := range []string{"stream", "generate"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			container := newMockExecContainerService()
			container.setBehavior("terminal-observer", execBehavior{exitCode: 1})
			container.written[hooks.DefaultConfigPath] = []byte(`{
				"version": 1, "enabled": true,
				"hooks": [
					{"name": "observe end", "event": "TurnEnd", "actions": [{"type": "command", "command": "terminal-observer", "on_error": "ignore"}]},
					{"name": "observe error", "event": "TurnError", "actions": [{"type": "command", "command": "terminal-observer", "on_error": "ignore"}]}
				]
			}`)
			bridgeProvider, cleanup := setupExecTestInfra(t, container)
			t.Cleanup(cleanup)
			handler := &lifecycleRecordingHandler{}
			hookService := hooks.NewService(slog.New(handler), bridgeProvider)
			if _, err := hookService.Run(t.Context(), hooks.Request{BotID: "bot", Event: hooks.EventTurnEnd}, nil); err != nil {
				t.Fatal(err)
			}
			const observedMessage = "hook action failed but was ignored"
			if handler.countMessage(observedMessage) != 1 {
				t.Fatal("terminal hook observer was not reached by the positive control")
			}
			provider := &atomicMockProvider{}
			agent := New(Deps{
				BridgeProvider: bridgeProvider,
				HookService:    hookService,
				ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
					return cfg, ErrContextRecompose
				},
			})
			cfg := RunConfig{
				Model: &sdk.Model{ID: "model", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("current")},
				Identity: SessionContext{BotID: "bot"},
			}
			if mode == "stream" {
				for event := range agent.Stream(t.Context(), cfg) {
					if event.Type != EventContextRecompose || event.Error != "" {
						t.Errorf("expected private recompose control, got %#v", event)
					}
				}
			} else {
				_, err := agent.Generate(t.Context(), cfg)
				if !errors.Is(err, ErrContextRecompose) || apperror.CodeOf(err) != "" {
					t.Errorf("recompose must remain a private control signal, got %v", err)
				}
			}
			if provider.calls.Load() != 0 {
				t.Error("provider called before recomposition")
			}
			if got := handler.countMessage(observedMessage); got != 1 {
				t.Errorf("recompose ran %d terminal hook commands before any provider call", got-1)
			}
		})
	}
}
