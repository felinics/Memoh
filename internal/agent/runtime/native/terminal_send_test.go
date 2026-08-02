package native

import (
	"context"
	"errors"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/sessionmode"
	agenttools "github.com/memohai/memoh/internal/agent/tool"
)

func terminalSendRunConfig(provider sdk.Provider) RunConfig {
	return RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("reply")},
		SupportsToolCall: true,
		SessionType:      sessionmode.Discuss,
		Identity: SessionContext{
			BotID: "bot-1", CurrentPlatform: "telegram", ReplyTarget: "chat-1",
		},
	}
}

func terminalSendTool(execute func(*sdk.ToolExecContext, any) (any, error)) sdk.Tool {
	return sdk.Tool{
		Name: "send", Parameters: &jsonschema.Schema{Type: "object"}, Execute: execute,
	}
}

func TestSuccessfulCurrentSendStopsGenerateWithoutSecondProviderCall(t *testing.T) {
	t.Parallel()
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call != 1 {
			t.Fatalf("unexpected endpoint call %d", call)
		}
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls: []sdk.ToolCall{{
				ToolCallID: "send-1", ToolName: "send", Input: map[string]any{"text": "hello"},
			}},
		}, nil
	}}
	agent := New(Deps{})
	agent.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{
		terminalSendTool(func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		}),
	}}})
	result, err := agent.Generate(context.Background(), terminalSendRunConfig(provider))
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
	if len(result.Messages) < 2 {
		t.Fatalf("canonical send call/result were not retained: %#v", result.Messages)
	}
}

func TestFailedSendAllowsNextProviderStep(t *testing.T) {
	t.Parallel()
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "send-1", ToolName: "send", Input: map[string]any{"text": "hello"}}},
			}, nil
		}
		return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop, Text: "retry handled"}, nil
	}}
	agent := New(Deps{})
	agent.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{
		terminalSendTool(func(*sdk.ToolExecContext, any) (any, error) {
			return nil, errors.New("delivery failed")
		}),
	}}})
	if _, err := agent.Generate(context.Background(), terminalSendRunConfig(provider)); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2 after failed send", provider.calls.Load())
	}
}

func TestSuccessfulCurrentSendStopsStreamWithoutSecondProviderCall(t *testing.T) {
	t.Parallel()
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call != 1 {
			t.Fatalf("unexpected endpoint call %d", call)
		}
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls:    []sdk.ToolCall{{ToolCallID: "send-1", ToolName: "send", Input: map[string]any{"text": "hello"}}},
		}, nil
	}}
	agent := New(Deps{})
	agent.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{
		terminalSendTool(func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		}),
	}}})
	for range agent.Stream(context.Background(), terminalSendRunConfig(provider)) {
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}
