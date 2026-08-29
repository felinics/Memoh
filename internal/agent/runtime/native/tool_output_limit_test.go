package native

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestAgentGenerateLimitsToolOutputBeforeNextModelCall(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL"
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-big",
						ToolName:   "big_tool",
						Input:      map[string]any{},
					}},
				}, nil
			case 2:
				result, ok := findToolResult(params.Messages, "big_tool")
				if !ok {
					t.Fatalf("second model call missing big_tool result: %#v", params.Messages)
				}
				structured, ok := result.Result.(map[string]any)
				if !ok {
					t.Fatalf("tool result = %#v, want map", result.Result)
				}
				content, ok := structured["content"].(string)
				if !ok {
					t.Fatalf("tool content = %#v, want string", structured["content"])
				}
				if len(content) >= len(large) {
					t.Fatalf("tool output was not pruned: got %d bytes, original %d", len(content), len(large))
				}
				if !strings.Contains(content, "[memoh pruned]") {
					t.Fatalf("tool output missing prune marker:\n%s", content)
				}
				return &sdk.GenerateResult{
					Text:         "ok",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				t.Fatalf("unexpected model call %d", call)
				return nil, nil
			}
		},
	}

	a := New(Deps{Limits: Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []sdk.Tool{{
				Name:       "big_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return map[string]any{
						"content": large,
						"ok":      true,
					}, nil
				},
			}},
		},
	})

	result, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("run big tool")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("Generate() text = %q, want ok", result.Text)
	}
}

func TestAgentExecuteToolLimitsToolError(t *testing.T) {
	t.Parallel()

	largeErr := "HEAD\n" + strings.Repeat("error detail ", 300) + "\nTAIL"
	a := New(Deps{Limits: Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []sdk.Tool{{
				Name:       "broken_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return nil, errors.New(largeErr)
				},
			}},
		},
	})

	result, err := a.ExecuteTool(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model"},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
	}, sdk.ToolCall{
		ToolCallID: "call-broken",
		ToolName:   "broken_tool",
		Input:      map[string]any{},
	})
	if err != nil {
		t.Fatalf("ExecuteTool() error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("ExecuteTool() IsError = false, want true")
	}
	text, ok := result.Result.(string)
	if !ok {
		t.Fatalf("ExecuteTool() Result = %#v, want string", result.Result)
	}
	if len(text) >= len(largeErr) || !strings.Contains(text, "[memoh pruned]") {
		t.Fatalf("tool error was not pruned:\n%s", text)
	}
}

func findToolResult(messages []sdk.Message, toolName string) (sdk.ToolResultPart, bool) {
	for _, message := range messages {
		if message.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range message.Content {
			result, ok := part.(sdk.ToolResultPart)
			if ok && result.ToolName == toolName {
				return result, true
			}
		}
	}
	return sdk.ToolResultPart{}, false
}
