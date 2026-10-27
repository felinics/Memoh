package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestAgentGenerateLimitsToolOutputBeforeNextModelCall(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL"
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			switch call {
			case 1:
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-big",
						ToolName:   "big_tool",
						Input:      toolexec.ArgumentsFromValue(map[string]any{}),
					}},
				}, nil
			case 2:
				result, ok := findToolResult(params.Messages, "big_tool")
				if !ok {
					t.Fatalf("second model call missing big_tool result: %#v", params.Messages)
				}
				structured, ok := toolexec.OutputValue(result.Result).(map[string]any)
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
				return sdk.ModelResult{
					Text:         "ok",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				t.Fatalf("unexpected model call %d", call)
				return sdk.ModelResult{}, nil
			}
		},
	}

	a := New(Deps{Limits: Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{{
				Name:       "big_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(map[string]any{
						"content": large,
						"ok":      true,
					}), nil
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
			tools: []toolexec.Tool{{
				Name:       "broken_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return sdk.ToolOutput{}, errors.New(largeErr)
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
		Input:      toolexec.ArgumentsFromValue(map[string]any{}),
	})
	if err != nil {
		t.Fatalf("ExecuteTool() error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("ExecuteTool() IsError = false, want true")
	}
	text, ok := toolexec.OutputValue(result.Result).(string)
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

// An exec result is bounded once, when the tool returns: at the default limit
// its stdout keeps its head and tail under one marker, and every later request
// carries the result exactly as the step stored it.
func TestAgentGenerateBoundsExecOutputOnceAtTheDefaultLimit(t *testing.T) {
	t.Parallel()

	line := strings.Repeat("0123456789", 9) + "\n"
	svc := newMockExecContainerService()
	svc.setBehavior("cat build.log", execBehavior{stdout: "HEAD\n" + strings.Repeat(line, 1000) + "TAIL"})
	bp, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()

	var sent []sdk.ToolResultPart
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			if call > 1 {
				result, ok := findToolResult(params.Messages, "exec")
				if !ok {
					t.Fatalf("model call %d missing the exec result", call)
				}
				sent = append(sent, result)
			}
			if call < 3 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("call-%d", call),
						ToolName:   "exec",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"command": []string{"cat build.log", "echo hi"}[call-1]}),
					}},
				}, nil
			}
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{agenttools.NewContainerProvider(nil, bp, background.New(nil), "/data")})

	var stored []sdk.ToolResultPart
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("show the build log")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1", SessionID: "sess-1"},
		OnStepCommitted: func(_ context.Context, _ int, record *step.Record) (StepDirective, error) {
			if result, ok := findToolResult(record.Messages, "exec"); ok {
				stored = append(stored, result)
			}
			return StepDirective{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(stored) != 2 || len(sent) != 2 {
		t.Fatalf("stored/sent exec results = %d/%d, want 2/2", len(stored), len(sent))
	}
	if size := len(stored[0].Result.String()); size > 32*1024 {
		t.Fatalf("stored exec result = %d bytes, want at most 32 KiB", size)
	}
	output, _ := toolexec.OutputValue(stored[0].Result).(map[string]any)
	stdout, _ := output["stdout"].(string)
	if strings.Count(stdout, "[memoh pruned]") != 1 || !strings.HasPrefix(stdout, "[memoh pruned]") ||
		!strings.Contains(stdout, "HEAD") || !strings.HasSuffix(stdout, "TAIL") {
		t.Fatalf("stdout = %d bytes, want its head and tail under one marker", len(stdout))
	}
	for i, result := range sent {
		if !reflect.DeepEqual(result, stored[0]) {
			t.Fatalf("model call %d sent the exec result differently from how the step stored it", i+2)
		}
	}
}
